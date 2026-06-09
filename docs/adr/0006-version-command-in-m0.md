## Status

Accepted. v0.1.0.

## Context

M0 exit criteria required `xray version` to work. The M1 milestone would
introduce a cobra-based command tree. Choosing how to handle `xray version`
in M0 without causing a migration headache was needed.

## Decision

A minimal `xray version` command exists in `cmd/xray/main.go` from M0 onward,
using stdlib `flag`. M1 replaces this with the cobra-based command tree
without losing the `version` command.

## Consequences

**Positive.** M0 exit criteria met. No cobra dependency in M0 keeps the M0
module dep-free.

**Negative.** None identified.

**Neutral.** Cobra replaces `flag` in M1 without user-visible change.

## How to apply

N/A — already applied in M0/M1.
