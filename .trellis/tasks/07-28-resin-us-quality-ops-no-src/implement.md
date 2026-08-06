# Implement: Resin US 优质池运维（不改 grok2api 源码）

## Checklist

1. **Inventory current state**
   - Confirm platform `grok2api` id, sticky 24h, region us, current routable count
   - Confirm grok2api egress still points at `grok2api.{account}@resin:2260`
   - Confirm `resin_default` network still attached

2. **Extend / replace maintenance script under `/root/resin`**
   - Prefer evolving `setup_grok2api_platform.py` or add `maintain_grok2api_platform.py`
   - Features:
     - `--regions us` (default)
     - `--keep 60` (default), min/max band 55–65
     - raw quality floor 90, reference latency prefilter 400ms, 3 latency attempts
     - `--quarantine-hours 2`
     - dual targets: cli-chat-proxy.grok.com + grok.com + latency URL
     - incremental retain of current platform tags when still good
     - quarantine file read/write
     - dry-run mode
     - atomic platform patch; on failure do not partially clobber without backup
   - Reuse patterns from `resin_quality_check.py` (admin client, proxy probe, temp pin) to avoid reinventing
   - **Improve target classification markers for grok** (not only ChatGPT keywords)

3. **Quarantine store**
   - Path e.g. `/root/resin/data/grok2api_quarantine.json`
   - Key by egress IP primarily; record tag, reason, until ISO time

4. **Reporting**
   - `/root/resin/data/grok2api_platform_latest.json`
   - `/root/resin/data/reports/grok2api_YYYYMMDD_HHMMSS.json`
   - Include: kept/evicted/added, scores, IP weight/effective rank, probe outcomes, duration, routable_node_count after apply

5. **Backup before apply**
   - Save current platform payload each successful pre-apply snapshot

6. **Host scheduler**
   - systemd timer **or** cron every 10 minutes
   - `flock` to prevent overlap
   - logs to `/root/resin/data/log/grok2api_maintain.log` (or journald)
   - working directory `/root/resin`, env from `.env`

7. **One-shot cutover**
   - Run once manually to expand/rebuild toward ~60 US quality nodes with new targets
   - Verify platform routable ~55–65
   - Spot-check proxy via `grok2api.{account}` egress IP region=us
   - Optionally probe grok2api admin egress test for four nodes (no source change; API only)

8. **Docs note**
   - Short ops README section under `/root/resin` (e.g. `GROK2API_PLATFORM.md`): behavior matrix, timer, manual command, rollback
   - Optional pointer from grok2api trellis task only (not required app README change)

## Validation commands

```bash
# dry run
cd /root/resin && python3 maintain_grok2api_platform.py --dry-run

# apply once
python3 maintain_grok2api_platform.py

# platform shape
source .env
curl -s -H "Authorization: Bearer $RESIN_ADMIN_TOKEN" \
  'http://127.0.0.1:2260/api/v1/platforms?limit=50' | python3 -c \
  'import sys,json; [print(p) for p in json.load(sys.stdin)["items"] if p["name"]=="grok2api"]'

# timer installed (example)
systemctl list-timers | grep -i grok2api || crontab -l | grep -i grok2api

# flock / no overlap smoke: run twice concurrently; second should skip
```

## Risky files / rollback points

| Item | Risk | Rollback |
|---|---|---|
| Platform regex replace | Accounts rebound if tags churn | Restore backup JSON via PATCH |
| Timer every 10m | Load on Resin / false evicts | Stop timer; lengthen interval |
| Pool expansion 30→60 | More probes per timer run | Keep flock and verify runtime remains below timer interval |
| Target false positives | Underfilled pool | Loosen hard gate; rely on score |

**Never** modify grok2api `backend/**` or frontend for this task.

## Follow-ups before `task.py start`

- [ ] User approved PRD/design/implement
- [ ] Confirm script name preference (extend setup vs new maintain_*)
- [ ] Confirm timer mechanism available (systemd vs cron) on host at implement time


## Implementation status (2026-07-28)

- [x] ` /root/resin/maintain_grok2api_platform.py`
- [x] ` /root/resin/run_maintain_grok2api.sh` (flock)
- [x] systemd timer `grok2api-resin-maintain.timer` every ~10m
- [x] quarantine + reports under `/root/resin/data/`
- [x] first apply: platform routable=30, US-only, sticky 24h
- [x] ops doc ` /root/resin/GROK2API_PLATFORM.md`
- [x] no grok2api source changes
- [x] quality-guard rotation webhook `/root/resin/grok2api_qg_rotator.py`
- [x] node IDs 14..23 mapped to Resin `qg1..qg10` with authenticated immediate rotation
- [x] rotated slot overrides survive the 10-minute maintainer for 2 hours
- [x] logical-node recovery retry reduced to 5 minutes; real-model probe remains the restore gate
- [x] existing isolation backlog processed: 8/10 qg nodes healthy, 2 correctly remain isolated after repeated unhealthy probes
- [x] follow-up: pool target 60 (55–65), raw quality floor 90, 3-attempt/400ms prefilter
- [x] follow-up: replace only the isolated IP tag and persist isolation-based IP reputation
