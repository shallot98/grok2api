# Quality Guidelines

> Code quality standards for backend development.

---

## Overview

<!--
Document your project's quality standards here.

Questions to answer:
- What patterns are forbidden?
- What linting rules do you enforce?
- What are your testing requirements?
- What code review standards apply?
-->

(To be filled by the team)

---

## Forbidden Patterns

<!-- Patterns that should never be used and why -->

(To be filled by the team)

---

## Required Patterns

<!-- Patterns that must always be used -->

(To be filled by the team)

---

## Testing Requirements

<!-- What level of testing is expected -->

(To be filled by the team)

---

## Code Review Checklist

<!-- What reviewers should check -->

(To be filled by the team)

---

## Scenario: Quality Guard Rotates Resin-backed Egress Slots

### 1. Scope / Trigger

- Trigger: a quality-guard managed egress node is a stable logical node (`resin-qg-*`) whose physical exit is selected by a Resin `qgN` platform.
- A Resin platform refresh alone does not clear grok2api quarantine because quarantine ownership is keyed by the grok2api node ID.

### 2. Signatures

- Quality guard sends `POST qualityGuard.rotationURL` after disabling a node.
- Resin webhook accepts `POST /rotate` and exposes unauthenticated `GET /healthz` only on the trusted Docker bridge.
- Node IDs `14..23` map exactly to Resin platforms `qg1..qg10`.

### 3. Contracts

- Request JSON: `{"nodeId":"14","oldExitIp":"203.0.113.10"}`.
- Required header: `Authorization: Bearer <qualityGuard.rotationToken>`.
- Success JSON must include `changed: true`, `nodeId`, `oldExitIp`, and a non-empty, different `newExitIp`.
- The webhook must replace all tags in the affected slot, verify a fresh-account exit through Resin, and preserve the override across the periodic pool maintainer.
- The old slot combination remains avoided for two hours; logical-node recovery retries every five minutes.

### 4. Validation & Error Matrix

- Missing/incorrect bearer token -> HTTP 401; no Resin mutation.
- Node ID outside `14..23` -> HTTP 503 error response; no Resin mutation.
- Missing main/slot platform or fewer than three candidates -> HTTP 503; no mutation.
- Replacement has zero routable nodes or the observed exit does not change -> restore the prior slot regex and return HTTP 503.
- Rotation succeeds but the real-model probe is unhealthy -> keep the grok2api node disabled and retry after the quality-guard quarantine interval.

### 5. Good/Base/Bad Cases

- Good: isolation rotates to a different IP, the model probe is healthy, and the guard immediately re-enables the logical node.
- Base: rotation succeeds but the model probe stays suspicious; the node remains isolated while other healthy slots serve traffic.
- Bad: returning `changed: true` without verifying `newExitIp != oldExitIp` can restore the same failing physical route.

### 6. Tests Required

- Unit-test the node-ID-to-slot boundary (`14 -> qg1`, `23 -> qg10`, reject others).
- Assert replacement excludes current tags and the old exit IP.
- Assert active slot overrides prefer tags not reserved by other rotations.
- Assert malformed/expired rotation state does not break periodic maintenance.
- Operationally verify `node_rotated` is followed by either `node_restored` or `quarantine_extended` from a real-model probe.

### 7. Wrong vs Correct

#### Wrong

Patch `qgN` every ten minutes but leave `rotationURL` empty; grok2api continues quarantining the unchanged logical node ID.

#### Correct

On quarantine, call the authenticated Resin webhook, verify the physical IP change, then let one real-model quality probe decide whether to restore the logical node.
