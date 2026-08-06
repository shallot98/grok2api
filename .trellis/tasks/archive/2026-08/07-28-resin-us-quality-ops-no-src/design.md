# Design: Resin US 优质池运维（不改 grok2api 源码）

## Architecture / boundaries

```text
┌──────────────────────────┐
│ systemd timer (every 10m)│
└────────────┬─────────────┘
             │
             v
┌────────────────────────────────────────────┐
│ /root/resin maintain script (Python)       │
│ 1. load quarantine + current platform tags │
│ 2. prefilter US healthy low-latency nodes  │
│ 3. quality probe via Resin forward proxy   │
│ 4. score + decide keep/evict/add           │
│ 5. patch platform grok2api regex (+ US)    │
│ 6. write report + update quarantine store  │
└────────────┬───────────────────────────────┘
             │ Admin API only
             v
┌──────────────────────┐     socks5h://grok2api.{account}@resin
│ Resin platform       │◄────────────────────────────────────
│ name=grok2api        │     grok2api egress nodes (unchanged
│ region=us, sticky=24h│     source; already configured)
│ ~60 quality tags     │
└──────────────────────┘
```

**Trust boundary**
- All automation lives under `/root/resin` (+ optional host timer units).
- **No changes** to grok2api Go/frontend source or image build for this task.
- grok2api continues using existing encrypted egress proxy URLs.

## Data flow

1. **Candidate set**
   - List Resin nodes; keep enabled, has egress, not circuit-open, `region=us`, not in quarantine.
   - Prefer currently platform-selected tags first (incremental retain).

2. **Probe** (through Resin, single-node pin via temp platform or existing quality-check pattern)
   - Latency: gstatic generate_204 (or Resin default latency URL)
   - Business: `https://cli-chat-proxy.grok.com` (connectivity / non-block classification)
   - Edge: `https://grok.com` (CF/block markers)
   - Produce score components; hard-fail rules separate from soft score.

3. **Decision**
   - **Keep** if still in pool, passes hard gates, score acceptable.
   - **Evict** if hard-fail or score below floor → add IP/tag to quarantine for 2h.
   - **Add** from non-pool candidates if `kept < 55` until ~60, highest effective rank first.
   - Cap ~65; effective rank is `probe score × persistent IP weight / 100`.
   - A quality-guard isolation lowers the affected IP weight by 25, down to a floor of 10.

4. **Apply**
   - `PATCH/POST` platform `grok2api`:
     - `regex_filters`: OR-anchored tags of final set
     - `region_filters: ["us"]`
     - `sticky_ttl: 24h`
     - `allocation_policy: PREFER_LOW_LATENCY`
   - Avoid deleting platform identity if possible (patch in place) so platform id stable.

5. **Persist**
   - Report JSON under `/root/resin/data/` (timestamped + `latest`)
   - Quarantine JSON/SQLite-less file: `{ip|tag, reason, until}`

## Contracts

### Hard gates (evict / not eligible)
- Not US / no egress / circuit open
- In quarantine
- Business or edge target classified **blocked** (or connect hard-fail beyond attempts)
- Availability below threshold (e.g. 0 success on required probes)

### Soft ranking
- Reuse weighted score idea from `resin_quality_check.py`, with grok-oriented target classification (not ChatGPT-only markers).
- Entry floor: raw probe score ≥90; IP weight changes preference but cannot make a low-quality node pass the hard gate.
- Resin has no native per-node weight field; selection weight is owned by the maintenance and rotation scripts.

### Timer contract
- Every **10 minutes**
- Overlap policy: **skip if previous still running** (flock)
- Exit non-zero on Admin API failure; leave previous platform regex intact on failed apply

## Compatibility

- Existing grok2api egress node IDs/URLs unchanged.
- Platform name must remain `grok2api` (username scheme).
- Expanding the pool to ~60 preserves existing healthy members when their effective rank remains competitive.

## Trade-offs

| Choice | Trade-off |
|---|---|
| No grok2api source | No per-request smart rotate; 10m detection SLA instead |
| Pool 60 | More failover capacity; stronger probes and a larger candidate scan increase each maintenance run |
| Sticky 24h | Stable sessions; bad IP escape depends on eviction not TTL |
| Quarantine 2h | Anti flapping; temporary underfill possible |
| No escape platform | Simpler; collective outage needs human/sub refresh |

## Ops / rollback

- Rollback: restore previous regex from `data/grok2api_platform_backup_*.json` or re-run setup with last good report.
- Disable timer to stop mutation.
- Emergency widen pool: manual run with `--keep 50` (if flag provided) or restore backup.
- Do not restart grok2api for routine refreshes.
