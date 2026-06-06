# Agent instructions

`xray` is a read-only Go CLI that extracts engineering metrics from a
client's systems into a single portable SQLite + JSON artifact. See
[`README.md`](README.md) for users and [`docs/spec.md`](docs/spec.md) for
the full product spec.

This file is the cross-tool agent surface. Tool-specific extensions live
adjacent (e.g. [`CLAUDE.md`](CLAUDE.md) + [`.claude/`](.claude/) for
Claude Code).

---

## Non-negotiable rules

- **Connectors are read-only.** No `POST` / `PATCH` / `PUT` / `DELETE`;
  no `Create*` / `Update*` / `Delete*` / `Edit*` / `Add*` / `Remove*`
  SDK methods.
- **No source content stored.** Numstat, byte-scan stats, and structured
  signals only — never patch text, diff text, or message bodies. Bodies
  parse at extract time, contribute structured columns, and drop out of
  scope in the same function.
- **Team-level only.** No individual-developer identifiers beyond opaque
  `*_handle` strings used for linkage.
- **No new dependencies** without an ADR entry. `go.mod` is the contract.
- **Tokens never logged** at any level.

Full constraint sheet (settled context, doors closed, invariants,
non-goals, schema-versioning rules) is in [`CLAUDE.md`](CLAUDE.md).

---

## Gates before declaring done

- `make gates` — runs `lint`, `govulncheck`, and `coverage` (the three
  CI gates locally).
- `bin/ship` — pre-push wrapper for `make gates`.

Claude Code users: run `/ready` for the full completion gate (gates →
deterministic review → inferential review → scope sweep → docs →
handoff). Other tools should run `make gates` plus an equivalent
review pass before submitting work.

---

## Commits

- Concise English imperative subject.
- No AI co-author trailer.
- No emojis.
- Body only when not self-evident from the diff.

---

## Tool-specific guidance

- **Claude Code** — see [`CLAUDE.md`](CLAUDE.md), [`.claude/`](.claude/),
  and the `/ready` slash command.
- **Other tools** — extend with your own adjacent file (`.cursorrules`,
  `.aider.conf.yml`, etc.) and keep the rules above intact.
