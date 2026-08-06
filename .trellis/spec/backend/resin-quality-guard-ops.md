# Resin Quality Guard Operations

## Scenario: Isolation-driven Resin IP rotation

### 1. Scope / Trigger

- Applies to the external `/root/resin` automation used by grok2api quality-guard nodes `14..23`.
- Triggered only after the quality guard disables a managed node and calls the authenticated rotation webhook.
- This integration must not require grok2api application source changes or a local binary rebuild.

### 2. Signatures

- Webhook: `POST http://172.22.0.1:2271/rotate`
- Request: `{"nodeId":"14".."23","oldExitIp":"<IPv4>"}`
- Success: `{"changed":true,"nodeId":"...","slot":"qgN","oldExitIp":"...","newExitIp":"...","oldIpWeight":<number>}`
- Maintenance command: `/root/resin/run_maintain_grok2api.sh`
- Persistent reputation file: `/root/resin/data/grok2api_ip_weights.json`

### 3. Contracts

- `nodeId 14..23` maps one-to-one to Resin platforms `qg1..qg10`.
- The main `grok2api` Resin platform targets 60 US nodes, with an allowed 55-65 band, `sticky_ttl=24h`, and `PREFER_LOW_LATENCY`.
- Raw probe score must be at least 90. Candidate prefilter latency is at most 400ms and latency probing uses three attempts.
- IP reputation starts at 100. Each guard isolation subtracts 25, with a floor of 10.
- Effective selection rank is `raw_probe_score * ip_weight / 100`; reputation changes preference but never bypasses the raw quality gate.
- Rotation replaces only the slot tag whose Resin `egress_ip` equals `oldExitIp`; unrelated healthy tags remain unchanged.
- Active rotation overrides are merged with the current slot bucket and must never shrink an expanded slot. With a 60-node main pool, every healthy `qgN` target is six routable tags.
- Resin has no native per-node weight field. The maintenance and rotation scripts own this reputation contract.

### 4. Validation & Error Matrix

| Condition | Required behavior |
|---|---|
| Missing or empty `oldExitIp` | Return HTTP 503; do not change the slot |
| `oldExitIp` is not in the mapped `qgN` slot | Return HTTP 503; do not change the slot |
| Node ID outside `14..23` | Return HTTP 503 |
| Fewer replacement IPs than isolated tags | Return HTTP 503; keep the previous slot |
| Replacement platform has zero routable nodes | Roll back the previous slot payload |
| Observed exit IP does not change | Roll back the previous slot payload |
| Probe wave yields fewer than half `min-keep` | Leave the main platform unchanged |
| Active override contains fewer tags than a new bucket | Preserve override tags, then fill from the bucket to its target size |

### 5. Good/Base/Bad Cases

- Good: one of six `qg7` tags matches the isolated IP; only that tag is replaced, its IP weight becomes 75, and the other five tags stay unchanged.
- Base: an IP has no reputation record; treat it as weight 100.
- Bad: a stale webhook names an IP no longer present in the slot; reject it instead of rotating an unrelated IP.

### 6. Tests Required

- Unit: node ID to slot mapping accepts exactly `14..23`.
- Unit: replacement changes only tags matching the isolated IP.
- Unit: missing/stale isolated IP is rejected.
- Unit: higher IP reputation wins replacement and pool ranking.
- Unit: repeated penalties clamp at weight 10 and survive JSON round-trip.
- Regression: merging a two-tag active override into a six-tag bucket returns six tags.
- Integration: main platform is 60 US routable nodes and every `qg1..qg10` platform has six routable nodes.
- Runtime: webhook health is reachable from both the host and grok2api container.

### 7. Wrong vs Correct

#### Wrong

Replace all tags in a `qgN` slot when only one exit IP was isolated, or assign an old three-tag override directly to a new six-tag bucket.

#### Correct

Identify tags by exact `egress_ip`, replace only those tags with the highest-reputation eligible candidates, and merge active override tags with the current bucket up to the bucket's target size.
