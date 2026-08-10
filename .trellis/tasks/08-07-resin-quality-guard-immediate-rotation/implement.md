# Implement: deterministic Resin IP quality guard

## Phase 1: detection correctness and contracts

- Change active and passive degradation classification to use visible output
  tokens over the visible generation window; retain total/reasoning values for
  diagnostics.
- Add regression tests for reasoning-heavy probes whose legacy total-token TPS
  exceeds the hard threshold while visible TPS remains healthy.
- Add account ID to the restricted passive-audit response.
- Extend the internal quality-probe request/result with an optional account ID
  and pin it through the existing selector while forcing the requested node.
- Return the actual selected account ID and test pinned, borrowed, and missing
  account behavior.

## Phase 2: account/IP attribution

- Pin passive confirmations to the audit account.
- Carry the affected account across disable, rotation, and replacement probe.
- Add sentinel confirmation when the affected account remains degraded on a new
  IP.
- Add a bounded model-scoped `quality_degraded` cooldown path and remove the
  affected account from sticky/model routing without penalizing unrelated
  models.
- Record affected account ID, confirmation kind, old/new IP, attribution, and
  recovery latency without logging account credentials or Resin lease keys.

## Phase 3: routing coordination

- Add a guard-owned temporary suspension distinct from permanent node disable,
  or an equivalent persisted reason/lease that survives sidecar restart.
- Make ordinary account selection skip accounts bound to suspended nodes before
  acquiring account concurrency.
- Prevent the automatic assignment worker from moving accounts solely because
  their node is under guard-owned immediate recovery.
- Do not silently apply direct/fixed fallback for an explicitly bound account
  whose node is quality-suspended; retry a different account instead.

## Phase 4: Resin single-IP recovery

- Snapshot the configured allowed subscription ID and export its current node
  membership without source URLs or credentials.
- Snapshot all qg platform payloads, egress nodes, account assignment counts,
  quality-guard runtime config/state, timers, and current Resin reports.
- Add a read-only audit command that validates the current mapping and reports
  multi-IP slots, duplicate active IPs, missing slots, reserve capacity, and
  any active/reserve node outside the allowed subscription.
- Add tests reproducing the present failure: a six-IP slot whose cached node IP
  is not represented by the model-probe account lease.

## Phase 5: single-IP state model

- Add one required allowed-subscription ID setting and resolve membership from
  Resin node tag metadata; do not select by display-name prefix alone.
- Replace numeric node-ID arithmetic in `/root/resin/grok2api_qg_rotator.py`
  with a validated explicit mapping file.
- Add atomic active-assignment and reserve state helpers.
- Change rotation to require one current tag/IP, promote one distinct reserve
  entry, persist generation/state, and preserve rollback payloads.
- Change `/root/resin/maintain_grok2api_platform.py` so routine runs preserve
  healthy active qg assignments and replenish reserve entries instead of
  round-robin rewriting every slot.
- Apply the subscription allowlist at initial fill, reserve refresh, and final
  rotation selection, with fail-closed tests for empty or undersized pools.
- Retain `region_filters: ["us"]` on every created or updated qg platform and
  test that unknown/non-US candidates cannot enter assignments or reserve.
- Keep shared locking, IP reputation penalties, two-hour quarantine, and secret
  redaction.

## Phase 6: sidecar scale and recovery

- Extend `tools/egress-quality-guard/quality_guard.py` with bounded scheduled
  probe concurrency and serialized state application.
- Add per-node single-flight recovery and a maximum of three immediate distinct
  replacement attempts.
- Keep passive anomalies confirmation-only.
- Add structured events for reserve promotion, attempted IP, attempt number,
  exhaustion, recovery latency, and stale/mismatched mapping.

## Phase 7: provision and canary

- Create the additional Resin `qgN` platforms and grok2api egress nodes through
  existing Admin APIs; capture the actual generated node IDs in the mapping.
- Configure every qg platform with exactly one unique US tag and verify its
  routable count and observed exit IP.
- Update `qualityGuard.nodeIDs` and `rotatableNodeIDs` from the mapping.
- Rebalance Build accounts using existing auto-assignment operations.
- Run a 10-node canary, then expand to 30 and 50 after one healthy cycle at each
  stage.

## Phase 8: fault-injection acceptance

- Force one canary slot to a known test-bad/degraded candidate.
- Verify classification to disable/rotation start is at most 5 seconds.
- Verify the logical node ID remains unchanged and only its Resin tag/IP changes.
- Verify `newExitIp != oldExitIp`, old IP enters quarantine, and no other healthy
  slot changes.
- Verify existing old-IP leases rebind through Resin on their next request.
- Verify healthy recovery restores within 90 seconds under normal conditions.
- Verify three failed candidates leave the node disabled and report exhaustion.
- Inject a high-quality node from another Resin subscription and verify it is
  excluded from active fill, reserve, and immediate rotation.
- Run rotation concurrently with the 10-minute maintainer and prove the active
  assignment cannot be overwritten.
- Refresh the high-score subscription with reordered scores and changed
  membership; verify old routes remain live until exact desired-tag
  reconciliation completes, then verify obsolete tags are removed.
- Force reconciliation failure and verify the compatibility union remains and
  no main/qg platform reaches zero routable nodes.
- Verify restart recovery from persisted mapping, assignments, reserve, and
  sidecar state.

## Validation

Lightweight local checks only:

```bash
go test ./backend/internal/application/gateway ./backend/internal/application/egress ./backend/internal/transport/http/audit ./backend/internal/transport/http/egress
python3 -m unittest /root/resin/test_grok2api_qg_rotator.py
python3 -m unittest /root/resin/test_maintain_grok2api_platform.py
python3 -m unittest tools/egress-quality-guard/test_quality_guard.py
```

Operational checks:

```bash
systemctl status grok2api-qg-rotator.service grok2api-resin-maintain.timer
docker compose ps
docker logs --since 30m grok2api-egress-quality-guard
```

No local Go release build is permitted. Go source changes use lightweight unit
tests locally, then the exact production server artifact is built through
GitHub Actions and replaced before runtime acceptance.

## Rollback points

- Before provisioning: restore script/state-file backups.
- After platform creation: new inactive nodes/platforms can be left unused while
  the previous 10-node configuration remains active.
- During canary: revert only the canary mapping and saved platform payloads.
- After full rollout: stop mutation services, restore all saved payloads and the
  previous quality-guard ID list, then restart the two application services.

## Review gates before start

- [x] User approves the 50-node one-IP architecture and increased probe volume.
- [x] User approves elastic screened capacity: up to 50 active and a target of
      up to 20 warm reserve, without assuming a fixed subscription IP count.
- [x] Existing dirty changes in `/root/resin-src` are out of scope and remain
      untouched because the MVP plans no compiled Resin changes.
- [x] User reviews and approves the complete PRD, design, and implementation
      plan.

## Execution gates after start

- [ ] Capture baseline backups and exact rollback commands before any mutation.
- [x] Complete 10-node canary acceptance before expanding to 30 nodes.
- [ ] Complete 30-node acceptance before expanding to 50 nodes.
