# PRD: Resin quality guard immediate IP rotation

## Goal and user value

Prevent API accounts from continuing to use an IP that causes upstream model
degradation. The system must select IPs using real model quality signals and,
after an IP is isolated, move affected traffic to a different verified IP
immediately instead of waiting for the normal maintenance cycle or sticky TTL.

## Problem statement

An IP can remain connectable while the upstream returns degraded model behavior.
Latency-only and generic HTTP probes therefore cannot decide whether an IP is
safe for production. The existing quality guard detects this condition with a
real-model probe and usually rotates the Resin slot, but rotation fails when the
observed IP is retained by an old sticky lease and is no longer present in the
slot's current tag set.

## Confirmed facts

- Resin now has an enabled `HighScore-LowRisk` subscription with stable ID
  `15e2270d-68c3-4acf-bc84-d5033a4380ae`.
- Subscription membership is dynamic because the user periodically replaces it
  with newly screened high-score IPs. The maintainer must discover the live
  count on every run and must not assume a fixed total. On 2026-08-07 it exposed
  31 enabled US nodes with 31 distinct observed exit IPs.
- The four-hour high-score publisher previously regenerated tags from rank, IP,
  and rounded score, then replaced the local subscription in one PATCH. At
  2026-08-07 12:15:12 this invalidated every managed platform binding; Resin
  reported `NO_AVAILABLE_NODES` until maintenance repaired the pool at 12:22.
- Resin automatically probes each proxy node's egress IP and resolves its
  lowercase ISO country code from probe metadata or the GeoIP database. Its
  Platform `region_filters` apply to that proxy exit region only.
- The live `grok2api` and `qg1` through `qg10` platforms all enforce
  `region_filters: ["us"]`; current eligible `HighScore-LowRisk` nodes resolve
  to region `us`.
- Resin treats the account portion of `Platform.Account` as an opaque sticky
  lease key. It neither stores nor automatically detects the Grok account's
  registration country, and grok2api's provider-account model also has no
  account country/region field.
- The user confirms that every Grok account in scope was registered in the US.
  Account-region inference and mixed-region account routing are therefore not
  required; enforcing a US proxy exit is sufficient for geographic alignment.
- The existing rotator does not filter candidates by subscription. It derives
  `pool_tags` from every node matched by the broad Resin `grok2api` platform,
  so adding the subscription alone does not constrain production selection.
- grok2api uses Resin account-aware proxy identities through `{account}`.
- The quality guard runs in hybrid mode and evaluates both active real-model
  probes and passive production audits.
- Active thresholds are currently `softTPS=500`, `hardTPS=1000`; hard anomalies
  isolate immediately and two consecutive soft anomalies isolate.
- The Resin main pool is US-only, targets 60 nodes, and is refreshed every 10
  minutes. Each quality-guard slot currently contains six routable tags.
- Resin sticky TTL is 24 hours to avoid unnecessary IP changes for healthy
  accounts.
- Current runtime evidence shows the main loop works: 182 quarantines and 179
  restores, with 217 successful rotations in the inspected guard logs.
- The current failure is reproducible for `qg2` and `qg7`: the rotator returns
  `isolated exit IP is not present in the requested slot`, leaving both logical
  nodes disabled and retrying the same operation.
- The 10-minute maintainer can replace many qg slot members while old account
  leases still refer to removed members. Rewriting platform tags alone does not
  invalidate those leases.
- The previous task deliberately did not modify grok2api or Resin source. This
  task now includes the minimum grok2api changes required for deterministic
  account-aware confirmation and IP/account attribution; compiled artifacts
  must be produced by GitHub Actions.
- Resin `v1.1.2` already exposes authenticated lease management endpoints:
  list/get/delete by `platform ID + account`, plus delete-all. The deployed
  Resin instance responds successfully to the lease-list endpoint, so a new
  Resin core API is not currently required for the MVP.
- Resin leases expose account, node tag, egress IP, expiry, and last-accessed
  time. This is sufficient to find and delete only leases bound to a confirmed
  degraded IP while preserving unrelated leases.
- grok2api already derives a stable, non-secret Resin account identity from the
  selected credential, but the quality-probe result does not currently return
  that identity or the actual request exit IP to the sidecar.
- The current rotator receives only `nodeId` plus the egress node's cached
  `exitIp`. That cached node-level value is not guaranteed to equal the sticky
  lease used by the account selected for the real-model probe.
- grok2api's quality guard, egress enable/disable state, passive audit counters,
  minimum-healthy protection, and recovery gate are all keyed by logical egress
  node ID. They do not model multiple physical IPs inside one logical node.
- The current deployment violates that implicit invariant: each `resin-qg-*`
  logical node maps to a Resin `qgN` platform containing six physical IP tags.
  Different accounts on the same logical node therefore use different physical
  IPs, while the guard observes and isolates them as one object.
- The stored grok2api node `exitIp` is produced by the independent
  `egress_probe` Resin identity. It is not request-level route metadata and
  cannot identify the physical IP used by the account selected for a model
  quality probe.
- The gateway's quality probe result currently exposes model timing/content
  evidence but not the selected account or Resin lease. Adding deterministic
  account targeting would cross the gateway selector, internal HTTP contract,
  account identity resolution, passive audit projection, and sidecar state; it
  is not a small response-field-only change.
- Resin routing already has the required failover behavior: if a sticky lease's
  node/IP is no longer in the platform's routable view, Resin first attempts a
  same-IP node and, when none exists, releases the old lease and creates a new
  lease on a different eligible IP.
- Production currently has 2,081 Build accounts. Of these, 1,820 are assigned
  to the quality-guard nodes; enabled qg nodes currently carry about 260
  accounts each. Increasing the number of one-IP logical nodes would reduce the
  account blast radius and IP sharing per node.

## Requirements

### R1. Quality-based IP admission

- Every active or reserve quality-guard candidate must belong to the Resin
  subscription ID `15e2270d-68c3-4acf-bc84-d5033a4380ae`. Nodes from every
  other Resin subscription are a hard deny for this project, even if they
  share a similar tag or have a better generic score.
- Subscription membership must be checked from Resin node tag metadata by
  immutable subscription ID, not inferred only from the mutable display-tag
  prefix or subscription name.
- Preserve Resin's `us` exit-region filter as a hard candidate requirement.
  Unknown or non-US exit regions must fail closed.
- Production IPs must pass a real Grok model probe, not only connectivity,
  geolocation, or latency checks.
- A probe must validate expected content and detect the established degraded
  output signature using generation timing and output behavior.
- Generic Resin pre-screening remains useful for candidate reduction, but it
  must not be treated as proof of model quality.
- Probe outcomes, exit IP, classification, reason, and timing must remain
  auditable without exposing proxy or API credentials.
- Passive production anomalies may mark a node/IP suspect and schedule an
  active real-model probe, but passive evidence alone must not rotate a slot.
- Degradation classification must use visible output tokens over the visible
  generation window. Total output and reasoning-token rates remain diagnostic
  fields and must not be mixed with a post-first-visible-token denominator.
- Passive evidence must carry the selected account ID so confirmation can use
  the same account, model, and logical node.

### R2. Immediate isolation and replacement

- A hard degradation signal must isolate the affected logical node immediately.
- Soft degradation must follow the configured consecutive-strike policy.
- Isolation must invalidate or bypass the affected account's old Resin lease and
  obtain an exit IP different from the isolated IP.
- The replacement IP must pass the same real-model recovery probe before the
  logical node is restored to production.
- A failed replacement must not restore traffic to the degraded IP. The workflow
  must try another eligible IP within a bounded retry policy, then keep the node
  isolated and report a terminal reason.
- Repeated retries must not loop forever on a structurally impossible rotation.
- After active confirmation, disable the affected logical node, quarantine its
  single physical IP for two hours, and replace that slot with a different
  eligible reserve IP. All accounts assigned to that logical node then rebind
  through Resin on their next request; accounts on other logical nodes do not
  move.
- Recovery must first retest the affected account on the replacement IP. If it
  remains degraded, a known-healthy sentinel account distinguishes an
  account/model degradation from another bad IP before consuming more reserve
  addresses.
- Account/model degradation removes only that account from the affected model
  for a bounded cooldown; unrelated models and accounts remain schedulable.

### R3. Maintainer and rotator ownership

- The 10-minute pool maintainer must not overwrite an active rotation decision
  or make a quality-guard slot inconsistent with outstanding sticky leases.
- Slot membership updates, lease invalidation, IP reputation penalties, and
  rotation state must have one explicit concurrency/locking contract.
- A missing old IP in the current slot must be handled as a stale-lease case,
  not as an unrecoverable tag lookup error.
- Isolated IPs must receive a persistent reputation penalty and must not be
  immediately selected again for the same recovery attempt.
- Subscription publishing and platform maintenance must use a staged handoff:
  old routes remain available until all managed platforms bind the desired new
  tags. A failed handoff must retain the compatibility union.
- Proxy tag identity must be stable for a `host:port`; rank and score changes
  must not rename an otherwise unchanged proxy.

### R4. Capacity and fail-safe behavior

- Healthy accounts retain stable IPs; the system must not rotate unrelated
  accounts or all qg slots because one IP degrades.
- A guard-owned temporary node suspension must stop new traffic without causing
  automatic account rebalancing or silently routing explicitly bound accounts
  through a fallback IP.
- The configured minimum healthy-node protection remains enforced.
- If no verified replacement is available, the affected logical node remains
  isolated while other healthy nodes continue serving traffic.
- Administrative failures, Resin API failures, and probe failures must preserve
  the last known healthy pool and expose distinct reasons.

### R5. Operations and observability

- Operators must be able to see detection time, isolation time, lease eviction,
  each replacement attempt, recovery-probe result, and final restoration time.
- Metrics/reports must distinguish model degradation, connectivity failure,
  stale lease, no replacement capacity, and internal rotation failure.
- Rollback must restore the previous platform and disable the new mutation path
  without requiring a grok2api rebuild.

## Acceptance criteria

1. A controlled hard-degradation event isolates its logical node and starts IP
   replacement within 5 seconds of classification.
2. The first recovery attempt uses an exit IP different from the isolated IP;
   no request is restored to the old IP.
3. Successful replacement and real-model verification restore the logical node
   within 90 seconds under normal Resin/API conditions.
4. When the isolated IP is absent from current slot tags but retained by a sticky
   lease, the workflow evicts/bypasses that lease and completes replacement
   instead of returning `isolated exit IP is not present in the requested slot`.
5. When the first candidate is also degraded, the workflow selects another IP
   according to the bounded retry policy and records every attempt.
6. When all candidates fail, the node stays isolated, other healthy nodes remain
   available, and the reason is reported as capacity/quality exhaustion rather
   than a generic rotation error.
7. A concurrent 10-minute maintenance run cannot overwrite an in-flight or
   protected rotation; a deterministic concurrency test covers this case.
8. Healthy accounts on unrelated logical nodes retain their existing sticky IP
   throughout another node's isolation and recovery.
9. Regression validation covers active detection, passive detection, stale
   lease eviction, different-IP enforcement, recovery gating, minimum healthy
   nodes, retry exhaustion, and restart/state recovery.
10. No secrets appear in logs, reports, task artifacts, or error responses.
11. One actively confirmed degradation rotates the affected one-IP logical
    node, but does not change any other logical node or its account leases.
12. One passive anomaly alone never rotates a slot; it produces a same-account
    active confirmation request and an auditable suspect event.
13. Visible-token TPS does not classify reasoning-heavy healthy probes as
    degraded, and both visible and total token measurements remain auditable.
14. Active assignments, warm reserve entries, and rotation replacements all
    resolve to nodes carrying the configured `HighScore-LowRisk` subscription
    ID; injecting a healthy node from another subscription cannot make it
    eligible.
15. If the allowed subscription cannot provide enough verified distinct IPs,
    the system reports allowlist capacity exhaustion and keeps affected nodes
   isolated instead of falling back to another subscription.
16. A scheduled high-score subscription refresh never reduces the managed main
    pool or any mapped qg slot to zero routable nodes while reconciliation runs.
17. Reconciliation failure leaves old routes available and does not finalize
    desired-only subscription content.

## Constraints

- Do not weaken the real-model recovery gate to gain faster restoration.
- Do not globally clear all Resin leases for a single degraded account/IP.
- Do not fall back to any Resin subscription outside `HighScore-LowRisk` when
  the allowed pool is exhausted.
- Do not rebuild grok2api locally. Any required compiled Resin change must use
  the repository's GitHub Actions artifact workflow under the project-wide
  build policy.
- Preserve the existing 24-hour sticky behavior for healthy traffic unless later
  evidence proves a narrower account-scoped change is required.
- Do not locally compile the grok2api Go service. Backend changes are validated
  with lightweight tests and built through the existing GitHub Actions flow.

## Out of scope

- Guaranteeing that an IP will never be degraded by the upstream.
- Purchasing or changing proxy providers.
- Rotating every account on a schedule.
- Redesigning unrelated grok2api routing or model selection.
- Treating generic latency score as a replacement for the real-model probe.
- Inferring a Grok account registration country from an opaque Resin identity,
  email address, token contents, or request failures.

## Open questions

- None. The user approved the final plan on 2026-08-07.

## Decisions

1. Use one physical IP per grok2api quality-guard logical node.
   - Keep logical node IDs stable and rotate only the single Resin tag/IP behind
     the corresponding qg platform.
   - Active confirmation quarantines the old IP and moves all accounts on that
     exact IP through Resin's existing unroutable-lease rebinding behavior.
   - No Resin compiled-source change is planned for the MVP. If later evidence
     proves one necessary, return to planning and build it through GitHub
     Actions rather than locally.

2. Use the `HighScore-LowRisk` subscription as a hard candidate allowlist.
   - Match by subscription ID `15e2270d-68c3-4acf-bc84-d5033a4380ae`.

3. Attribute quality failures before repeated rotation.
   - Keep independent state for `(accountID, model)` and `(nodeID, exitIP)`.
   - Confirm passive anomalies with the same account on the same node.
   - After replacement, use the affected account first and a sentinel only when
     the result remains degraded.
   - Apply the allowlist to initial slot fill, reserve replenishment, routine
     maintenance, and immediate replacement.
   - Continue real-model admission and recovery probes inside the allowlist;
     the provider's score is only a pre-filter.
   - Fail closed when eligible allowed-subscription capacity is exhausted.

3. Filter proxy exit geography, not inferred account geography.
   - Keep Resin `region_filters: ["us"]` as a hard requirement for every active
     and reserve candidate.
   - Rely on Resin's automatic egress probe and GeoIP fallback for node region.
   - Do not add a Grok account country field or infer registration country in
     the MVP. All in-scope accounts are known to be US-registered.
   - Preserve per-account sticky routing and use real-model outcomes to detect
     practical account/IP incompatibility within the US pool.

4. Passive signals cannot directly mutate slots or leases.
   - One passive anomaly creates suspect state and schedules a targeted active
     real-model probe.

5. Manage the live screened subscription capacity without assuming its size.
   - Use up to 50 one-IP logical nodes for production traffic and target up to
     20 screened warm reserve IPs from the remaining eligible capacity.
   - Roll out in batches of 10, then 30, then 50 active nodes.

## Proposed architecture after source analysis

- Preserve stable grok2api logical egress nodes; never delete/recreate a node to
  rotate its physical exit.
- Expand to 50 quality-guard logical nodes and map each node explicitly to one
  Resin `qgN` platform containing exactly one physical IP tag.
- Maintain a separate warm reserve targeting up to 20 eligible US IPs. Reserve IPs
  pass generic Resin/Grok edge screening before use; every promoted IP must pass
  the real-model recovery probe before its logical node is restored.
- Stop the 10-minute maintainer from periodically redistributing healthy qg
  slots. It may refresh the main candidate pool, replenish reserve capacity, and
  repair missing/unroutable slots, but active-slot ownership belongs to the
  rotator/quality guard.
- Use an explicit persisted `grok2api node ID -> Resin slot` map instead of
  assuming a contiguous numeric node-ID range.
- Add bounded concurrency to active quality probes so 50 IPs can be checked
  within the configured cycle without unbounded load.
- Keep grok2api compiled source unchanged for the MVP. The one-IP invariant
  makes its existing node-level probe, passive audit, quarantine, and recovery
  contracts accurate again.
- The previously discussed account-first/IP-second policy is superseded under
  this architecture: every account on one logical node is known to share the
  same physical IP, so a confirmed node-level degradation is an exact IP-level
  signal rather than a six-IP aggregate.
