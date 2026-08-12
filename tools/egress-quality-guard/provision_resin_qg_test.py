import importlib.util
import json
import stat
import sys
import tempfile
import unittest
from argparse import Namespace
from pathlib import Path
from unittest import mock


PATH = Path(__file__).with_name("provision_resin_qg.py")
sys.path.insert(0, str(PATH.parent))
SPEC = importlib.util.spec_from_file_location("provision_resin_qg", PATH)
module = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = module
SPEC.loader.exec_module(module)
api_module = sys.modules[module.JsonClient.__module__]


class PlanTests(unittest.TestCase):
    def mapping(self):
        return {"version": 1, "nodes": {"14": {"slot": "qg1"}, "15": {"slot": "qg2"}}}

    def reserve(self, count=3):
        return {
            "version": 1,
            "subscription_id": module.ALLOWED_SUBSCRIPTION_ID,
            "entries": [
                {
                    "tag": f"HighScore-LowRisk/node-{index}",
                    "ip": f"192.0.2.{index}",
                    "subscription_id": module.ALLOWED_SUBSCRIPTION_ID,
                }
                for index in range(1, count + 1)
            ],
        }

    def test_plan_fills_only_missing_slots(self):
        plan = module.select_plan(self.mapping(), self.reserve(), 4, module.ALLOWED_SUBSCRIPTION_ID)
        self.assertEqual([item["slot"] for item in plan], ["qg3", "qg4"])
        self.assertEqual([item["name"] for item in plan], ["resin-qg-3", "resin-qg-4"])

    def test_plan_can_use_subset_of_larger_reserve(self):
        plan = module.select_plan(self.mapping(), self.reserve(3), 3, module.ALLOWED_SUBSCRIPTION_ID)
        self.assertEqual(len(plan), 1)

    def test_plan_fails_closed_on_subscription_mismatch(self):
        reserve = self.reserve()
        reserve["subscription_id"] = "other"
        with self.assertRaises(module.ProvisionError):
            module.select_plan(self.mapping(), reserve, 3, module.ALLOWED_SUBSCRIPTION_ID)

    def test_plan_deduplicates_ips_and_checks_capacity(self):
        reserve = self.reserve(2)
        reserve["entries"][1]["ip"] = reserve["entries"][0]["ip"]
        with self.assertRaisesRegex(module.ProvisionError, "capacity exhausted"):
            module.select_plan(self.mapping(), reserve, 4, module.ALLOWED_SUBSCRIPTION_ID)

    def test_payloads_are_disabled_single_ip_us(self):
        item = {"slot": "qg11", "name": "resin-qg-11", "tag": "sub/a+b", "ip": "192.0.2.1"}
        platform = module.platform_payload(item)
        node = module.node_payload(item, "secret")
        self.assertEqual(platform["region_filters"], ["us"])
        self.assertEqual(platform["regex_filters"], [r"^sub/a\+b$"])
        self.assertFalse(node["enabled"])
        self.assertIn("qg11.{account}", node["proxyURL"])

    def test_quality_gate_rejects_marker_only_and_soft_tps(self):
        healthy = {
            "expectedMatched": True,
            "outputTokens": 100,
            "generationMs": 1500,
            "visibleTokensPerSecond": 100,
        }
        self.assertTrue(module.quality_probe_healthy(healthy))
        self.assertFalse(module.quality_probe_healthy({**healthy, "generationMs": 50}))
        self.assertFalse(module.quality_probe_healthy({**healthy, "visibleTokensPerSecond": 500}))

    def test_json_client_accepts_empty_success_response(self):
        response = mock.MagicMock()
        response.__enter__.return_value.read.return_value = b""
        with mock.patch.object(api_module.urllib.request, "urlopen", return_value=response):
            self.assertEqual(module.JsonClient("http://example").call("DELETE", "/item"), {})

    def test_find_node_requires_exact_created_id(self):
        nodes = {"items": [{"id": "24", "enabled": False}]}
        self.assertFalse(module.find_node(nodes, "24")["enabled"])
        with self.assertRaises(module.ProvisionError):
            module.find_node(nodes, "25")

    def test_provision_keeps_probe_node_disabled(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            args = Namespace(backup_dir=root)
            plan = [{"slot": "qg11", "name": "resin-qg-11", "tag": "sub/node", "ip": "192.0.2.1"}]

            class FakeResin:
                def call(self, method, path, body=None):
                    self.request = (method, path, body)
                    return {"id": "platform-1", "routable_node_count": 1}

            class FakeGrok:
                def __init__(self):
                    self.calls = []

                def call(self, method, path, body=None):
                    self.calls.append((method, path, body))
                    if path == "/api/admin/v1/egress-nodes" and method == "POST":
                        return {"id": "24"}
                    if path.endswith("/test"):
                        return {"nodeId": "24", "expectedMatched": True, "outputTokens": 100,
                                "generationMs": 1500, "visibleTokensPerSecond": 100}
                    return {"items": [{"id": "24", "enabled": False}]}

            grok = FakeGrok()
            result = module.provision(args, plan, FakeResin(), grok, "token")
            self.assertEqual(result["created"][0]["node_id"], "24")
            self.assertFalse(any(call[0] == "PATCH" for call in grok.calls))

    def test_provision_disables_and_fails_if_probe_enables_node(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            args = Namespace(backup_dir=root)
            plan = [{"slot": "qg11", "name": "resin-qg-11", "tag": "sub/node", "ip": "192.0.2.1"}]

            class FakeResin:
                def call(self, *_args, **_kwargs):
                    return {"id": "platform-1", "routable_node_count": 1}

            class FakeGrok:
                def __init__(self):
                    self.patched = False

                def call(self, method, path, body=None):
                    if method == "PATCH":
                        self.patched = True
                        return {"updated": 1}
                    if path == "/api/admin/v1/egress-nodes" and method == "POST":
                        return {"id": "24"}
                    if path.endswith("/test"):
                        return {"nodeId": "24", "expectedMatched": True, "outputTokens": 100,
                                "generationMs": 1500, "visibleTokensPerSecond": 100}
                    return {"items": [{"id": "24", "enabled": True}]}

            grok = FakeGrok()
            with self.assertRaisesRegex(module.ProvisionError, "unexpectedly enabled"):
                module.provision(args, plan, FakeResin(), grok, "token")
            self.assertTrue(grok.patched)
            manifest = json.loads((root / "provision-manifest.json").read_text())
            self.assertEqual(manifest["created"][0]["node_id"], "24")

    def test_backup_is_private_and_contains_both_control_planes(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = root / "config.yaml"
            source.write_text("secret", encoding="utf-8")
            backup = root / "backup"
            module.backup_state(backup, (source,), {"items": [1]}, {"items": [2]})
            self.assertEqual(stat.S_IMODE(backup.stat().st_mode), 0o700)
            for path in backup.iterdir():
                self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o600)
            self.assertEqual(json.loads((backup / "resin-platforms.json").read_text())["items"], [1])

    def test_claim_plan_atomically_removes_only_claimed_reserve(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            mapping = root / "mapping.json"
            reserve = root / "reserve.json"
            mapping.write_text(json.dumps(self.mapping()), encoding="utf-8")
            reserve.write_text(json.dumps(self.reserve(4)), encoding="utf-8")
            args = Namespace(
                mapping=mapping,
                reserve=reserve,
                target_slots=4,
                subscription_id=module.ALLOWED_SUBSCRIPTION_ID,
                minimum_remaining_reserve=2,
            )
            plan = module.claim_plan(args)
            remaining = json.loads(reserve.read_text())["entries"]
            self.assertEqual([item["slot"] for item in plan], ["qg3", "qg4"])
            self.assertEqual([item["ip"] for item in remaining], ["192.0.2.3", "192.0.2.4"])
            self.assertEqual(stat.S_IMODE(reserve.stat().st_mode), 0o600)

    def test_claim_plan_does_not_write_when_reserve_floor_fails(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            mapping = root / "mapping.json"
            reserve = root / "reserve.json"
            mapping.write_text(json.dumps(self.mapping()), encoding="utf-8")
            original = json.dumps(self.reserve(3))
            reserve.write_text(original, encoding="utf-8")
            args = Namespace(
                mapping=mapping,
                reserve=reserve,
                target_slots=4,
                subscription_id=module.ALLOWED_SUBSCRIPTION_ID,
                minimum_remaining_reserve=2,
            )
            with self.assertRaisesRegex(module.ProvisionError, "remaining reserve"):
                module.claim_plan(args)
            self.assertEqual(reserve.read_text(), original)


if __name__ == "__main__":
    unittest.main()
