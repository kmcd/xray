# Prose style guide

This document covers the prose-linting rules enforced by [Vale](https://vale.sh)
and how to work with them when editing docs.

## What is linted

Vale runs on:

- `README.md`
- `CHANGELOG.md`
- `CONTRIBUTING.md`
- `SECURITY.md`
- `CLAUDE.md`
- `docs/*.md` (spec, schema, security, threat model, ADRs)

Vale does **not** run on:

- `tmp/**`
- `.claude/**`
- `LICENSE` (verbatim Apache-2.0 text)
- `CODE_OF_CONDUCT.md` (Contributor Covenant standard text)
- Go source comments (different tooling, out of scope)

## Style packs

| Pack | Source | What it checks |
|------|--------|----------------|
| `Microsoft` | upstream | Terminology, contractions, abbreviations |
| `Google` | upstream | Developer documentation style, sentence case |
| `write-good` | upstream | Passive voice, weasel words, wordy constructions |
| `alex` | upstream | Insensitive and inconsiderate wording |
| `xray` | project | Project-specific rules (below) |

Upstream packs are downloaded via `vale sync` and are **not** committed to
the repository (only the `xray/` pack is committed).

## Project-specific rules

Four rules live under `.vale/styles/xray/`:

<!-- vale xray.SentenceCase = NO -->
### `AntiLLM.yml` — anti-AI-phrasing
<!-- vale xray.SentenceCase = YES -->

Severity: **warning**

<!-- vale xray.AntiLLM = NO -->
Catches phrasing common in AI-generated text: "delve into", "leverage",
"comprehensive solution", "robust framework", "navigate the complexities of",
"it's important to note", "moreover", "in conclusion", and similar tells.
<!-- vale xray.AntiLLM = YES -->

These phrases undermine trust in technical documentation. Rewrite to say
the thing directly.

This rule starts at `warning` (non-blocking). Once the existing docs are
clean, promote to `error`.

### `SentenceCase.yml` — heading capitalisation

Severity: **warning**

Headings use sentence case: only the first word and proper nouns are
capitalised. This prevents drift now that AI tools habitually generate
title-case headings.

The exceptions list covers product names, acronyms, and established proper
nouns (GitHub, GraphQL, SQLite, CircleCI, etc.).

### `Hedging.yml` — hedging words

Severity: **warning**

<!-- vale xray.Hedging = NO -->
<!-- vale alex.Condescending = NO -->
Flags words that hedge unnecessarily in customer-facing docs: "simply",
"just", "obviously", "clearly", "of course". These words often signal that
the author knows the explanation is incomplete.
<!-- vale xray.Hedging = YES -->
<!-- vale alex.Condescending = YES -->

### `Vocab.yml` (vocabulary accept list)

The project vocabulary lives in `.vale/styles/config/vocabularies/xray/accept.txt`.
It lists technical terms that are valid but not in standard dictionaries:
`xray`, `TOML`, `SQLite`, `goroutine`, `numstat`, `cosign`, and many
connector-specific terms.

Add new project terms here when Vale's spell checker flags them incorrectly.

## Severity levels

| Level | Blocks PR? | What triggers it |
|-------|-----------|-----------------|
| `error` | Yes | Spelling errors, undefined terms, broken syntax |
| `warning` | No | AntiLLM tells, hedging, heading case |
| `suggestion` | No (local only) | Subjective style preferences |

## Running Vale locally <!-- vale xray.SentenceCase = NO -->

```bash
make prose
```

This runs the same command as CI:

```bash
vale README.md CHANGELOG.md CONTRIBUTING.md SECURITY.md CLAUDE.md docs/
```

Install Vale from the [releases page](https://github.com/errata-ai/vale/releases).
After installing, run `vale sync` once to download the upstream style packs.

## Overriding a rule

When a rule fires on a term that is intentionally correct, use Vale's
inline suppression syntax:

```markdown
<!-- vale off -->
Text that should not be linted goes here.
<!-- vale on -->
```

To suppress a single rule without disabling all linting:

```markdown
<!-- vale xray.AntiLLM = NO -->
This sentence contains a technical term that looks like an LLM tell.
<!-- vale xray.AntiLLM = YES -->
```

Use overrides sparingly. When you add one, note the reason in a comment
so the next person understands why.

## Load-bearing phrases

Some phrases in `docs/security.md` and `docs/threat-model.md` are
security guarantees, not marketing language. Do not soften them:

- "no source content stored"
- "no secrets in the artifact"
- "read-only"
- "team-level only"
- "tokens never logged"

If Vale flags these constructions as hedging or passive, use a
`<!-- vale off -->` block and note the reason.

## Adding a new style rule

1. Add the YAML rule file to `.vale/styles/xray/`.
2. Test locally with `make prose`.
3. Document the rule in this file under "Project-specific rules".
4. If the rule fires on existing docs, fix the flagged phrases in the
   same commit.
