# Design: deterministic Resin IP quality guard

## Architecture

```text
                  every 10 minutes
  Resin HighScore-LowRisk subscription -----+
      |                                      |
      v                                      v
 generic/edge screening              warm reserve target: up to 20 IPs
      |                                      |
      +------ ranked US candidate state -----+
                                             |
                    promote one different IP |
                                             v
  grok2api node ID <-> explicit map <-> Resin qgN platform
  resin-qg-N                           exactly one IP tag
      |                                      |
      +-------- real Grok model probe -------+
                         |
               healthy: serve traffic
               degraded: disable -> rotate -> verify -> restore
```

The central invariant is:

```text
one grok2api quality-guard node == one Resin platform == one physical exit IP
```

The candidate boundary is independently enforced:

```text
candidate subscription ID == 15e2270d-68c3-4acf-bc84-d5033a4380ae
```

Display names and tag prefixes are retained for operator readability, but are
not authorization boundaries.

Resin's region boundary remains `region_filters: ["us"]`. This is an exit-IP
property: the egress probe records IP/region and GeoIP provides fallback. The
`Account` portion of the Resin proxy identity is used only for lease stickiness
and carries no registration-region semantics. All in-scope Grok accounts are
known to be US-registered, so the US exit filter provides the required match
without adding account-region state.

The grok2api node and its account assignments remain stable configuration. IP
rotation mutates only the single tag selected by the mapped Resin platform.

## Components and ownership

### grok2api

- Continue using the existing egress nodes, internal quality-test endpoint,
  passive audit feed, enable/disable API, and minimum-healthy-node protection.
- Classify degradation with visible tokens over the visible generation window;
  expose total/reasoning/visible token measurements separately.
- Add an optional account ID to the internal quality-probe contract. Resolve it
  through the existing pinned-account selector while still forcing the target
  egress node; do not create a second account scheduler.
- Include account ID in the restricted passive-audit projection and return the
  selected account ID from active probes.
- Maintain model-scoped account quality cooldowns through the existing selector
  invalidation path.
- Treat guard-owned suspension as a routing stop that preserves account-node
  bindings and prevents automatic reassignment during immediate recovery.
- Expand the configured quality-guard node set to 50 nodes through existing
  Admin/API operations.
- Backend Go changes are in scope; frontend and Resin compiled-source changes
  remain out of scope. Build the Go artifact only through GitHub Actions.
- Do not add an account-country schema or assignment rule for the MVP. Preserve
  stable account bindings and use real-model outcomes as the compatibility
  signal inside the US-only exit pool.
- Restart the service only to load base configuration changes; no local build.

### Quality guard sidecar

- Probe each enabled mapped node with the fixed real-model prompt.
- Treat a hard active result as immediate isolation; retain configured
  consecutive-strike behavior for soft results.
- Treat a passive anomaly as a request for an active confirmation. Passive data
  alone cannot rotate an IP.
- Pin passive confirmation to the account recorded by the triggering audit.
- After replacement, probe the affected account first. If it still fails, probe
  a sentinel account through the same new IP: sentinel healthy means account
  cooldown and node restore; sentinel unhealthy means reject the IP and rotate
  again.
- Run scheduled active probes with bounded concurrency while serializing state
  mutation and per-node recovery decisions. The 10-node canary showed that
  concurrency 4 can overload the current Resin/proxy path, so production starts
  at 1 and may be raised to 2 only after a separate healthy canary.
- The Resin candidate maintainer is also serialized at concurrency 1. Canary
  evidence showed that concurrency 10 can trip widespread proxy failures even
  when each worker targets a different physical IP.
- Keep the logical node disabled until a different IP passes connectivity and
  real-model recovery probes.

### Resin maintainer

- Build both active assignments and reserve inventory only from nodes whose tag
  metadata contains the configured `HighScore-LowRisk` subscription ID.
- Reject cross-subscription nodes before generic ranking or model probing, and
  fail closed rather than widening the pool when capacity is low.
- Continue generic connectivity, Grok edge, region, latency, quarantine, and
  reputation screening.
- Maintain the elastic main quality inventory and a separate reserve targeting
  up to 20 IPs from the current screened membership.
- Never rebucket or rewrite a healthy active qg slot during routine maintenance.
- Repair only slots that are uninitialized, missing, unroutable, or explicitly
  released by rotation state.
- Share one host lock and atomic state files with the rotator.

### Resin rotator

- Resolve `nodeId` through an explicit mapping file; do not use arithmetic.
- Lock against the maintainer and other rotations.
- Read the current qg slot and require exactly one routable physical IP.
- Penalize and quarantine the old IP for two hours.
- Select the highest-ranked reserve IP that is different, not quarantined, not
  active in another slot, not already attempted by this recovery, and still a
  member of the configured allowed subscription ID at selection time.
- Patch the same qg platform to exactly one new tag.
- Verify through a fresh Resin identity that the physical exit changed.
- Return the new IP to the sidecar; the existing real-model recovery gate makes
  the final restore decision.

## Data and state

### Node/slot mapping

`/root/resin/data/grok2api_qg_map.json`

```json
{
  "version": 1,
  "nodes": {
    "14": {"slot": "qg1"},
    "15": {"slot": "qg2"}
  }
}
```

The production file contains all 50 actual IDs. It is written atomically after
Admin API creation and validated for unique node IDs and slot names.

### Active assignments and reserve

`grok2api_qg_assignments.json` records one tag/IP per slot, last verification,
and rotation generation. `grok2api_qg_reserve.json` records ranked eligible
reserve entries, including their source subscription ID. Existing quarantine
and IP reputation files remain the shared source of exclusion and penalty
state. State loading revalidates subscription membership so stale entries from
a removed or retagged subscription cannot be promoted.

No credentials are stored in these files.

## Detection and recovery flow

1. A scheduled active probe runs with a stable sentinel, or a passive-triggered
   confirmation runs with the audit's account `A`, on logical node `N`.
2. A hard result, or configured consecutive soft results, disables `N`.
3. The sidecar calls the authenticated rotator with `nodeId` and the observed
   old IP. With one tag, the node health-probe IP and every Resin account lease
   refer to the same physical exit.
4. The rotator quarantines/penalizes the old IP and promotes one reserve IP into
   the same slot.
5. Resin's existing router sees old leases whose node/IP is no longer routable.
   It cannot find a same-IP route, so it releases them and creates leases on the
   new IP on their next request.
6. The sidecar probes account `A` through the replacement IP on `N`.
7. If `A` is healthy, enable `N`. If `A` remains degraded, probe a sentinel on
   the same IP. A healthy sentinel applies a model-scoped cooldown to `A` and
   restores `N`; an unhealthy sentinel rejects the IP and starts another bounded
   rotation attempt.

## Quality state boundaries

```text
account quality: (accountID, upstream model) -> healthy / suspect / cooling
IP quality:      (logical node ID, exit IP)  -> healthy / quarantined
```

The sidecar owns short-lived detection/recovery state. grok2api owns durable
account schedulability and node routing state. Resin owns the physical one-IP
slot and reserve inventory. Disabling a slot invalidates local clients, but the
assignment worker must preserve the logical binding while guard recovery is in
flight.

Normal first-candidate recovery should complete within 90 seconds. At most
three immediate candidates are attempted; exhaustion keeps the node disabled
and retries after the configured quarantine interval without weakening the
minimum healthy pool.

## Dynamic subscription handoff

The high-score publisher names each proxy from stable `host:port` identity and
emits an exact Resin tag manifest. Refresh uses three ordered states:

1. Publish the union of the previous and desired Clash proxy definitions.
2. Under the maintainer lock, probe and bind only tags in the desired manifest;
   update main, mirrors, qg assignments, and reserve state.
3. After successful reconciliation, replace the union with desired-only
   content.

If step 2 fails, step 3 is forbidden. The union is intentionally retained so
old platform regexes continue routing while operators inspect the failure.
This avoids relying on timer alignment and preserves dynamic subscription size.

## Concurrency contract

- The maintainer and rotator use the same `flock` file.
- Per-slot recovery is single-flight in the sidecar.
- Active probes may execute concurrently, but all writes to sidecar state are
  serialized and saved atomically.
- Reserve selection and slot patch occur under the host lock so one reserve IP
  cannot be promoted into two slots.
- Routine maintenance reads active assignments under the same lock and cannot
  overwrite them.

## Rollout

1. Build the mapping and create inactive one-IP qg slots/nodes in batches.
2. Fill each slot from the ranked pool and verify its actual IP.
3. Start with a 10-node canary using the new one-IP contract.
4. Observe model health, probe duration, Resin load, account distribution, and
   rotations for at least one full active cycle.
5. Expand in batches to 30, then 50 nodes.
6. Enable bounded concurrent probing only after the canary establishes a safe
   concurrency level.

## Rollback

- Stop the quality-guard sidecar and maintainer timer before rollback.
- Restore saved Resin platform payloads and the previous 10-slot mapping.
- Restore the previous grok2api egress configuration and quality-guard node ID
  list through Admin/config operations.
- Restart only the affected services/processes; do not reboot the host or build
  a local binary.
- Preserve quarantine/reputation evidence for diagnosis; do not reuse degraded
  IPs merely to restore capacity.

## Trade-offs

- The allowed subscription membership changes whenever the user publishes a
  new screened set. Active capacity is bounded by the live unique-IP count;
  50 active and 20 reserve are upper targets rather than fixed assumptions.
- Real-model probe volume increases by roughly five times at 50 active IPs.
- Bounded concurrency shortens a cycle but consumes multiple accounts and more
  upstream capacity simultaneously.
- More logical nodes increase operational state, but reduce account/IP sharing
  from roughly 260 accounts per enabled qg node toward roughly 42.
- The design avoids a new grok2api private account-routing API and keeps future
  upstream upgrades substantially simpler.
