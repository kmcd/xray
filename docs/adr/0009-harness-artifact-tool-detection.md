## Status

Accepted. v0.1.0.

## Context

The `harness_artifacts` table needed a way to detect which AI coding tool
produced each file. Options: content sniffing (requires reading file bodies,
violates the no-source-content constraint) or path-based mapping.

## Decision

Path-based mapping in a static table; no content sniffing.

| Pattern                                | tool         | kind         |
| -------------------------------------- | ------------ | ------------ |
| `CLAUDE.md`                            | claude_code  | instructions |
| `.claude/**`                           | claude_code  | (subdir-dependent: rules/skills/agents/commands) |
| `AGENTS.md`                            | unknown      | instructions |
| `.cursor/rules` / `.cursor/rules/**`   | cursor       | rules        |
| `.cursorrules`                         | cursor       | rules        |
| `.github/copilot-instructions.md`      | copilot      | instructions |
| `.aider*` / `aider.conf.yml`           | aider        | rules        |
| `.windsurfrules`                       | windsurf     | rules        |
| `.continue/**`                         | continue     | rules        |
| `.mcp.json` / `**/mcp.json`            | generic_mcp  | mcp          |
| `.github/workflows/*` invoking AI bots | (detected per-workflow) | workflow |

## Consequences

**Positive.** Path-based is what the spec calls for; content-free satisfies
the static-binary constraint.

**Negative.** New AI tools not in the table go undetected until the table is
updated.

**Neutral.** The `unknown` tool value is used when the file matches no known
pattern.

## How to apply

The static table lives in the harness-artifacts extractor. Add new patterns
as new AI tools become relevant.
