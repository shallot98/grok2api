#!/usr/bin/env python3
"""Provision disabled one-IP Resin quality slots before a guarded rollout."""

from __future__ import annotations

import argparse
import fcntl
import json
import os
import re
import shutil
import tempfile
import time
from pathlib import Path
from typing import Any

from provision_resin_qg_api import ApiError, JsonClient, login, read_env


ALLOWED_SUBSCRIPTION_ID = "15e2270d-68c3-4acf-bc84-d5033a4380ae"
DEFAULT_RESIN_URL = "http://127.0.0.1:2260"
DEFAULT_GROK_URL = "http://127.0.0.1:8000"
MIN_OUTPUT_TOKENS = 32
MIN_GENERATION_MS = 1000
SOFT_TPS = 500.0


class ProvisionError(ApiError):
    pass


def slot_number(name: str) -> int:
    match = re.fullmatch(r"qg([1-9][0-9]*)", name)
    if not match:
        raise ProvisionError(f"invalid quality slot: {name}")
    return int(match.group(1))


def existing_slots(mapping: dict[str, Any]) -> set[str]:
    nodes = mapping.get("nodes") if isinstance(mapping, dict) else None
    if mapping.get("version") != 1 or not isinstance(nodes, dict):
        raise ProvisionError("invalid quality mapping")
    result = {str((value or {}).get("slot") or "") for value in nodes.values()}
    for name in result:
        slot_number(name)
    return result


def select_plan(
    mapping: dict[str, Any], reserve: dict[str, Any], target_slots: int, subscription_id: str
) -> list[dict[str, Any]]:
    if reserve.get("version") != 1 or reserve.get("subscription_id") != subscription_id:
        raise ProvisionError("reserve subscription does not match the allowed subscription")
    current = existing_slots(mapping)
    wanted = [f"qg{index}" for index in range(1, target_slots + 1) if f"qg{index}" not in current]
    entries = list(reserve.get("entries") or [])
    eligible: list[dict[str, Any]] = []
    seen_ips: set[str] = set()
    for entry in entries:
        ip = str(entry.get("ip") or "")
        tag = str(entry.get("tag") or "")
        if entry.get("subscription_id") != subscription_id or not ip or not tag or ip in seen_ips:
            continue
        seen_ips.add(ip)
        eligible.append(entry)
    if len(eligible) < len(wanted):
        raise ProvisionError(f"reserve capacity exhausted: {len(eligible)}/{len(wanted)}")
    return [
        {"slot": name, "name": f"resin-qg-{slot_number(name)}", "tag": entry["tag"], "ip": entry["ip"]}
        for name, entry in zip(wanted, eligible[:len(wanted)], strict=True)
    ]


def platform_payload(item: dict[str, Any]) -> dict[str, Any]:
    return {
        "name": item["slot"],
        "sticky_ttl": "24h",
        "regex_filters": ["^" + re.escape(str(item["tag"])) + "$"],
        "region_filters": ["us"],
        "allocation_policy": "PREFER_LOW_LATENCY",
        "reverse_proxy_miss_action": "TREAT_AS_EMPTY",
        "reverse_proxy_empty_account_behavior": "RANDOM",
        "reverse_proxy_fixed_account_header": "",
        "passive_circuit_breaker_disabled": False,
    }


def node_payload(item: dict[str, Any], proxy_token: str) -> dict[str, Any]:
    account = "{account}"
    return {
        "name": item["name"],
        "scope": "grok_build",
        "enabled": False,
        "proxyPool": True,
        "accountCapacity": 0,
        "proxyURL": f"socks5h://{item['slot']}.{account}:{proxy_token}@resin:2260",
    }


def quality_probe_healthy(value: dict[str, Any]) -> bool:
    return (
        bool(value.get("expectedMatched"))
        and int(value.get("outputTokens") or 0) >= MIN_OUTPUT_TOKENS
        and int(value.get("generationMs") or 0) >= MIN_GENERATION_MS
        and float(value.get("visibleTokensPerSecond") or 0.0) < SOFT_TPS
    )


def find_node(nodes: dict[str, Any], node_id: str) -> dict[str, Any]:
    for item in nodes.get("items") or []:
        if str(item.get("id") or "") == node_id:
            return item
    raise ProvisionError(f"created node is missing: {node_id}")


def backup_state(
    directory: Path,
    sources: tuple[Path, ...],
    resin_platforms: Any,
    grok_nodes: Any,
) -> None:
    directory.mkdir(parents=True, exist_ok=False, mode=0o700)
    os.chmod(directory, 0o700)
    for source in sources:
        target = directory / source.name
        shutil.copy2(source, target)
        os.chmod(target, 0o600)
    for name, value in (("resin-platforms.json", resin_platforms), ("grok-egress-nodes.json", grok_nodes)):
        target = directory / name
        target.write_text(json.dumps(value, indent=2), encoding="utf-8")
        os.chmod(target, 0o600)


def write_json_atomic(path: Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.NamedTemporaryFile("w", encoding="utf-8", dir=path.parent, delete=False) as handle:
        json.dump(value, handle, indent=2)
        handle.flush()
        os.fsync(handle.fileno())
        temporary = Path(handle.name)
    os.chmod(temporary, 0o600)
    temporary.replace(path)


def claim_plan(args: argparse.Namespace) -> list[dict[str, Any]]:
    mapping = json.loads(args.mapping.read_text(encoding="utf-8"))
    reserve = json.loads(args.reserve.read_text(encoding="utf-8"))
    plan = select_plan(mapping, reserve, args.target_slots, args.subscription_id)
    claimed_ips = {str(item["ip"]) for item in plan}
    remaining = [item for item in reserve.get("entries") or [] if str(item.get("ip") or "") not in claimed_ips]
    if len(remaining) < args.minimum_remaining_reserve:
        raise ProvisionError(f"remaining reserve is too small: {len(remaining)}/{args.minimum_remaining_reserve}")
    reserve["entries"] = remaining
    reserve["target"] = len(remaining)
    reserve["updated_at"] = time.time()
    write_json_atomic(args.reserve, reserve)
    return plan


def clients(args: argparse.Namespace) -> tuple[JsonClient, JsonClient, str]:
    env = read_env(args.resin_env)
    resin_token = env.get("RESIN_ADMIN_TOKEN", "")
    proxy_token = env.get("RESIN_PROXY_TOKEN", "")
    if not resin_token or not proxy_token:
        raise ProvisionError("Resin admin/proxy token is missing")
    resin = JsonClient(args.resin_url, resin_token)
    grok = JsonClient(args.grok_url)
    grok.token = login(grok)
    return resin, grok, proxy_token


def capture_baseline(args: argparse.Namespace, resin: JsonClient, grok: JsonClient) -> None:
    backup_state(
        args.backup_dir,
        (args.mapping, args.assignments, args.reserve, args.config),
        resin.call("GET", "/api/v1/platforms?limit=200&offset=0&sort_by=name&sort_order=asc"),
        grok.call("GET", "/api/admin/v1/egress-nodes"),
    )


def provision(
    args: argparse.Namespace,
    plan: list[dict[str, Any]],
    resin: JsonClient,
    grok: JsonClient,
    proxy_token: str,
) -> dict[str, Any]:
    manifest: dict[str, Any] = {"version": 1, "created_at": time.time(), "created": []}
    manifest_path = args.backup_dir / "provision-manifest.json"
    for item in plan:
        platform = resin.call("POST", "/api/v1/platforms", platform_payload(item))
        created = {**item, "platform_id": platform["id"], "node_id": ""}
        manifest["created"].append(created)
        manifest_path.write_text(json.dumps(manifest, indent=2), encoding="utf-8")
        if int(platform.get("routable_node_count") or 0) != 1:
            raise ProvisionError(f"new slot is not single-IP routable: {item['slot']}")
        node = grok.call("POST", "/api/admin/v1/egress-nodes", node_payload(item, proxy_token))
        created["node_id"] = str(node["id"])
        manifest_path.write_text(json.dumps(manifest, indent=2), encoding="utf-8")
        test = grok.call("POST", f"/api/admin/v1/egress-quality-guard/nodes/{node['id']}/test", {})
        current = find_node(grok.call("GET", "/api/admin/v1/egress-nodes"), str(node["id"]))
        if current.get("enabled"):
            grok.call("PATCH", "/api/admin/v1/egress-nodes/batch", {"ids": [str(node["id"])], "enabled": False})
            raise ProvisionError(f"quality probe unexpectedly enabled new node: {item['slot']}")
        if str(test.get("nodeId") or "") != str(node["id"]) or not quality_probe_healthy(test):
            raise ProvisionError(f"real-model quality probe failed: {item['slot']}")
    return manifest


def rollback(args: argparse.Namespace) -> dict[str, Any]:
    manifest = json.loads(args.rollback_manifest.read_text(encoding="utf-8"))
    env = read_env(args.resin_env)
    resin = JsonClient(args.resin_url, env.get("RESIN_ADMIN_TOKEN", ""))
    grok = JsonClient(args.grok_url)
    grok.token = login(grok)
    removed: list[dict[str, str]] = []
    for item in reversed(list(manifest.get("created") or [])):
        node_id = str(item.get("node_id") or "")
        platform_id = str(item.get("platform_id") or "")
        if node_id:
            grok.call("DELETE", f"/api/admin/v1/egress-nodes/{node_id}")
        if platform_id:
            resin.call("DELETE", f"/api/v1/platforms/{platform_id}")
        removed.append({"node_id": node_id, "platform_id": platform_id})
    return {"rolled_back": removed}


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--target-slots", type=int, default=30)
    parser.add_argument("--minimum-remaining-reserve", type=int, default=20)
    parser.add_argument("--subscription-id", default=ALLOWED_SUBSCRIPTION_ID)
    parser.add_argument("--mapping", type=Path, default=Path("/root/resin/data/grok2api_qg_map.json"))
    parser.add_argument("--reserve", type=Path, default=Path("/root/resin/data/grok2api_qg_reserve.json"))
    parser.add_argument("--assignments", type=Path, default=Path("/root/resin/data/grok2api_qg_assignments.json"))
    parser.add_argument("--config", type=Path, default=Path("/root/grok2api/config.yaml"))
    parser.add_argument("--resin-env", type=Path, default=Path("/root/resin/.env"))
    parser.add_argument("--resin-url", default=DEFAULT_RESIN_URL)
    parser.add_argument("--grok-url", default=DEFAULT_GROK_URL)
    parser.add_argument("--lock", type=Path, default=Path("/root/resin/data/grok2api_maintain.lock"))
    parser.add_argument("--backup-dir", type=Path)
    parser.add_argument("--rollback-manifest", type=Path)
    parser.add_argument("--apply", action="store_true")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    if args.rollback_manifest is not None:
        if not args.apply:
            raise ProvisionError("--rollback-manifest requires --apply")
        print(json.dumps(rollback(args), indent=2))
        return 0
    mapping = json.loads(args.mapping.read_text(encoding="utf-8"))
    reserve = json.loads(args.reserve.read_text(encoding="utf-8"))
    plan = select_plan(mapping, reserve, args.target_slots, args.subscription_id)
    if not args.apply:
        print(json.dumps({"apply": False, "target_slots": args.target_slots, "plan": plan}, indent=2))
        return 0
    if args.backup_dir is None:
        raise ProvisionError("--backup-dir is required with --apply")
    resin, grok, proxy_token = clients(args)
    args.lock.parent.mkdir(parents=True, exist_ok=True)
    with args.lock.open("a+", encoding="utf-8") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        capture_baseline(args, resin, grok)
        plan = claim_plan(args)
    print(json.dumps(provision(args, plan, resin, grok, proxy_token), indent=2))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (OSError, ValueError, ApiError) as exc:
        raise SystemExit(str(exc)) from exc
