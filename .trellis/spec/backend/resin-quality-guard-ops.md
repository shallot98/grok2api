# Resin Quality Guard Operations

## Scenario: Dynamic one-IP Resin quality slots

### 1. Scope / Trigger

- Applies to `/root/resin` automation backing grok2api quality-guard nodes.
- Also applies when `/root/proxy/proxyscrape_reg/maintain_high_score_sub.py`
  replaces the dynamic `HighScore-LowRisk` subscription.
- A confirmed real-model degradation disables one logical node and rotates only
  the one physical IP behind its mapped Resin `qgN` platform.
- No grok2api or Resin release build is required. Python automation is restarted
  directly; compiled changes must use GitHub Actions.

### 2. Signatures

- Webhook: `POST http://172.22.0.1:2271/rotate`
- Header: `Authorization: Bearer <QG_ROTATOR_TOKEN>`
- Request: `{"nodeId":"<mapped-id>","oldExitIp":"<optional-cached-ip>"}`
- Success: `{"changed":true,"nodeId":"...","slot":"qgN","oldExitIp":"...","newExitIp":"...","oldIpWeight":75,"staleExpectedExitIp":false}`
- Mapping: `/root/resin/data/grok2api_qg_map.json`
- Active inventory: `/root/resin/data/grok2api_qg_assignments.json`
- Reserve inventory: `/root/resin/data/grok2api_qg_reserve.json`
- Audit: `python3 /root/resin/audit_grok2api_qg.py --allowed-subscription-id <uuid> --reserve-target 20`
- Staged reconciliation: `/root/resin/run_maintain_grok2api.sh --wait-lock --allowed-tags-file <tags.txt>`
- `tags.txt` contains exact Resin tags, including the subscription prefix, one
  per line: `HighScore-LowRisk/hq-<stable-endpoint-id>`.
- Required rotator env: `QG_ALLOWED_SUBSCRIPTION_ID`, `QG_ROTATOR_TOKEN`.
- Sidecar env: `QG_ACTIVE_CONCURRENCY=1`, `QG_MAX_ROTATION_ATTEMPTS=3`.
- Sidecar liveness field: `state.json.updated_at`; refresh interval while a
  model request is pending: `LIVENESS_INTERVAL_SECONDS=15`.

### 3. Contracts

- Candidate authorization is immutable Resin subscription ID
  `15e2270d-68c3-4acf-bc84-d5033a4380ae`, never a name/prefix match.
- Subscription membership is dynamic. Every maintenance run reads the current
  member set; 50 main entries and 20 reserve entries are upper targets, not a
  fixed expected IP count.
- High-score Clash node names are stable for a proxy `host:port`; ranking and
  score are metadata and must never be embedded in the tag identity.
- Subscription replacement is a staged handoff: publish the union of old and
  desired proxies, reconcile all managed platforms using only the desired
  exact-tag allowlist, then publish desired-only content.
- A failed or lock-blocked reconciliation must leave the compatibility union
  published and return a failure. It must never remove the old routes.
- The active hard floor is the number of entries in the explicit mapping. If
  fewer distinct score-90 US IPs pass, leave the current pool unchanged.
- One mapped grok2api node equals one Resin platform equals one routable US exit
  IP. Logical node IDs and account assignments remain stable across rotation.
- Multiple webhook node IDs may alias one Resin slot, but capacity, assignment,
  reserve exclusion, maintenance repair, and audit counts must collapse aliases
  by unique slot. An alias must never consume a second active IP.
- Healthy active slots are preserved. Missing, cross-subscription, duplicate,
  non-routable, or multi-IP slots are repaired from ranked eligible candidates.
- Active and reserve IPs are distinct. Reserve shortage is a warning; empty
  reserve makes rotation fail closed and keeps the logical node disabled.
- Current slot IP is authoritative. Missing or stale request `oldExitIp` is
  auditable but cannot block rotation when the slot is valid and one-IP.
- Old IP/tag is quarantined for two hours and loses 25 reputation points, with a
  weight floor of 10. Quarantine outranks current main-platform membership.
- Maintainer and rotator share `/root/resin/data/grok2api_maintain.lock`; state
  inventories are written atomically.
- Both model probes and Resin candidate probes run at concurrency 1 in the
  current environment. Canary evidence showed concurrency 4/10 can cause broad
  proxy 502s and Resin circuit-open cascades.
- Passive anomalies only request active confirmation. Only active model evidence
  may disable or rotate a node, and a replacement is restored only after a
  healthy real-model probe.
- Ordinary request-health writes must preserve the persisted
  `quality guard suspended` reason. Only the quality-suspension API may clear
  that ownership marker during a verified restore.
- Active model probes routinely exceed the admin UI's 60-second freshness
  floor. Every blocking quality probe path, including scheduled concurrency,
  passive confirmation, replacement verification, and sentinel attribution,
  must refresh `updated_at` while pending without changing probe results or
  guard metadata.
- Upstream sidecar replacements must preserve the full cross-layer contract:
  visible-token classification, same-account passive confirmation, quality
  suspension, model-scoped account cooldown, sentinel attribution, and bounded
  distinct-IP retries. Passing a reduced upstream test suite is not evidence
  that these local contracts remain intact.
- The sidecar script is bind-mounted into the container. If an update replaces
  the host file inode (for example, Git restore/revert), restart/recreate the
  sidecar and verify the host and container script checksums match before
  treating the source change as deployed.
- New `resin-qg-*` nodes must be created disabled. The ordinary admin
  connectivity probe persists health and may enable a healthy node, so it is
  forbidden during pre-provisioning. Validate the exact one-tag/one-routable-IP
  Resin platform first, then run only the forced real-model probe and re-read
  the node to prove it remained disabled.
- A staged capacity expansion claims candidates under the shared maintainer
  lock, then releases the lock before slow model probes. It must leave at least
  20 reserve entries available to the live rotator and must not hold immediate
  recovery behind multi-minute provisioning work.

### 4. Validation & Error Matrix

| Condition | Required behavior |
|---|---|
| Node ID absent from mapping | HTTP 503; no Resin mutation |
| Slot has zero/multiple tags or routable count is not 1 | HTTP 503; repair via maintainer first |
| Missing/stale request `oldExitIp` | Use current one-IP slot; return `staleExpectedExitIp: true` |
| Candidate outside allowed subscription or non-US | Exclude before ranking |
| Candidate is active, quarantined, same IP, or circuit-open | Exclude from reserve promotion |
| Replacement count is not exactly one | Restore prior slot payload and return HTTP 503 |
| Observed replacement IP differs from selected IP or equals old IP | Restore prior payload; HTTP 503 |
| Passing unique IPs are fewer than mapped active slots | Report active capacity exhaustion; do not patch main/qg platforms |
| Reserve below target | Apply healthy active state; emit warning with actual/target counts |
| Reserve empty during degradation | Keep logical node disabled; report candidate exhaustion |
| Passive-only anomaly | Run active confirmation; do not mutate routing directly |
| Desired tag file missing or empty | Abort reconciliation; retain old/union subscription routes |
| Desired tags do not resolve inside the allowed subscription ID | Fail closed; do not finalize desired-only content |
| Staged reconciliation fails or times out | Keep the old+new compatibility union; surface a non-zero result |
| Staged reconciliation succeeds | Finalize desired-only subscription content; all main/qg tags must be desired tags |
| Model probe remains pending for more than 60 seconds | Refresh `updated_at` at most every 15 seconds; keep the sidecar status fresh |
| Model probe raises or completes | Preserve the original exception/result after heartbeat updates |
| Upstream sidecar update removes account pinning, suspension, sentinel attribution, or bounded retries | Reject the update; restore the local contract and its regression tests |
| Host and container sidecar script checksums differ after an update | Recreate the sidecar container before runtime acceptance |
| Pre-provisioning probe changes a new node to enabled | Disable it immediately, abort the stage, rebalance any assigned accounts, and use the manifest to remove created nodes/platforms |
| Claiming expansion candidates would leave fewer than 20 reserve IPs | Abort before writing reserve state or creating platforms |

### 5. Good/Base/Bad Cases

- Good: node `20` is actively classified `hard_tps`; its current qg IP is
  quarantined, one reserve IP is promoted, the model probe passes, and node `20`
  is restored without changing its ID.
- Base: a screened subscription changes from 31 to 80 members. The next serial
  maintainer run discovers the new set and rebuilds 10 active plus up to 20
  reserve entries without configuration changes.
- Bad: treating an empty cached `exitIp` as fatal strands a repaired slot. The
  one-IP platform must identify the old IP authoritatively.
- Bad: raising probe concurrency without a canary can open many Resin circuits;
  do not infer safe concurrency from CPU capacity.
- Good: a four-hour high-score refresh keeps old routes present while the
  maintainer probes and binds the new stable tags, then removes obsolete tags.
- Bad: naming nodes `hq001-<ip>-s99` makes every ranking change invalidate all
  platform regex bindings and produces `NO_AVAILABLE_NODES` until the next
  maintenance cycle.
- Good: an 85-second real-model probe keeps `updated_at` fresh while pending,
  then applies the actual probe classification exactly once.
- Bad: updating state only before and after a long probe makes the running
  sidecar appear stale and causes operators to restart a healthy guard.

### 6. Tests Required

- Unit: explicit mapping rejects malformed IDs and duplicate slots.
- Unit: allowed-subscription normalization excludes all other subscriptions.
- Unit: pool decisions and reserve ranking deduplicate physical exit IPs.
- Unit: reserve entries revalidate their per-entry subscription ID, US region,
  active overlap, quarantine, outbound status, and circuit status.
- Regression: missing cached old IP rotates the current one-IP slot.
- Regression: quarantined current members cannot re-enter candidate/reserve
  selection.
- Audit: detect missing/multi-IP slots, duplicate active IPs, assignment drift,
  cross-subscription reserve entries, non-US nodes, and reserve shortage.
- Sidecar: passive evidence requires active confirmation, scheduled probes obey
  concurrency, and recovery tries at most three distinct replacements.
- Runtime: webhook works from host and grok2api container; audit returns `ok`;
  one controlled degradation changes IP, preserves node ID, and restores only
  after a healthy real-model probe.
- Unit: stable proxy tags do not change with ranking, score, or credential
  rotation for the same `host:port`.
- Unit: staged publish writes old+new first, invokes exact-tag reconciliation,
  and writes desired-only last; reconciliation failure performs no final write.
- Sidecar regression: a blocked single probe and a blocked scheduled concurrent
  cycle both advance persisted `updated_at` before the request completes.
- Sidecar regression: a passive hard anomaly runs a pinned active confirmation
  and performs no suspension or rotation when that confirmation is healthy.
- Deployment: after replacing the mounted script, assert `sha256sum` matches on
  the host and at `/usr/local/bin/grok2api-egress-quality-guard` in the running
  sidecar.
- Provisioning: `provision_resin_qg.py` defaults to a read-only plan; `--apply`
  requires a new private backup directory and writes `provision-manifest.json`
  after every created platform/node. Test empty DELETE responses, partial
  manifests, reserve-floor failure, and unexpected node enablement.

### 7. Wrong vs Correct

#### Wrong

Derive `qgN` from node-ID arithmetic, put six IPs behind one logical node,
assume the subscription always contains 80 IPs, or re-add a quarantined current
member because it still matches the main platform regex. Do not overwrite a
live subscription with ranking-derived tags before managed platforms rebind.

#### Correct

Resolve the node through the persisted mapping, keep exactly one unique allowed
US IP per slot, discover screened capacity on each run, preserve healthy slots,
and promote only a revalidated reserve candidate under the shared lock. Replace
screened subscription content through the compatibility-union handoff and
finalize only after exact desired-tag reconciliation succeeds.
Keep the persisted sidecar heartbeat advancing during every blocking model
request so status freshness represents process liveness rather than probe
latency.
Do not replace this sidecar with an upstream variant that disables nodes from
passive evidence or bypasses the persisted quality-suspension and account
attribution APIs, even if its narrower unit tests pass.
