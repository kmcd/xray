# Class scan

Use before writing any bug fix. The unit of work is the class of the bug, not the instance.

The rule in [`CLAUDE.md`](../../CLAUDE.md) → Workflow → *Same-class scan before fixing* is the policy; this skill is the mechanism. Also run as the second pass at `/ready` Step 4 to catch drift between planned scope and what the diff actually covers.

## Step 1: name the abstract shape

State, in one sentence, the shape of the mistake — not the specific identifier, the pattern. Use repo vocabulary.

- "External tool's raw error is surfaced to the operator instead of being translated into actionable guidance."
- "Spec example uses one identifier shape; connector code requires a different shape; no validation between them."
- "Read endpoint that returns 403/404 is not recorded as `EndpointStatus{Accessible: false}` in provenance."
- "Permission-gated GraphQL query swallows errors instead of appending to `prov.Errors`."

"Bug in error handling" is not a shape. "Raw external-tool error not translated for the operator" is.

## Step 2: grep for siblings

Run real searches with actual identifiers. Memory drifts; grep does not.

- Class touches a helper function: `grep -rn "splitSlug" internal/connectors/` — every call site is a peer.
- Class touches connector code: walk every `internal/connectors/<name>/` — they are peers by construction.
- Class is spec/code drift: grep `docs/spec.md` and the connector that owns the field; check both ends.
- Class is a call-site pattern: grep the call shape, not the symptom.

Record every hit. Treating one as out of scope is fine; missing it is not.

## Step 3: classify each hit

For each peer:

- **In scope** — fix in the same commit. Same intent, same review surface.
- **Out of scope, but the same class** — file **one** class-level issue. Name the class in the title; list every instance in the body. Never file N instance-level issues for the same class.
- **Not actually the same class** — explain why; do not suppress silently.

## Step 4: name the class in the commit

If the fix covers more than one instance, the commit subject names the class, not the instances:

```
github: record EndpointStatus on splitSlug failure for all repo endpoints
```

Not:

```
github: fix branches.go, releases.go, extract.go invalid-slug handling
```

The first form survives `git log --oneline`; the second is noise once context fades.

## When to run

- **At plan time**, before writing the fix. The scan informs which files the fix should touch.
- **At `/ready` Step 4**, as the second pass. Catches drift between planned scope and what the diff actually covers.

If running standalone (not inside `/ready`), produce a three-list handoff:

- **Class** — one sentence (Step 1 output).
- **Instances in scope** — fix list (Step 3 → in-scope hits).
- **Instances out of scope** — class-level issue title + bulleted instance list (Step 3 → out-of-scope hits).
