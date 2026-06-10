# Style

Audit prose against [`docs/style-guide.md`](../../docs/style-guide.md) and the
project Vale rules, then **apply fixes by default**. Reports what was fixed
(and what was deliberately left).

The default is fix-and-report. Editorial review happens at read-time, not at
audit-time — fast turnaround beats per-finding approval.

To audit without fixing, pass `--report-only`.

To pre-load the rules before drafting (the preventive loop, not a post-hoc
audit), pass `--preload`: the command reads the rule files + style guide and
stops, putting the swap list and hedging tokens into your context without
running an audit.

## Step 1: Determine scope

Parse the argument:

- **File path** (`/style README.md`) — fix one file
- **Directory** (`/style docs/`) — fix every `.md` under it
- **No arg** — fix git WIP: every modified, staged, or untracked `.md` in the
  working tree

For "no arg" mode, get the file list with:

```
git status --porcelain | awk '{print $2}' | grep -E '\.md$'
```

Filter to **prose surfaces only** (the same set `make prose` lints):

- `README.md`, `CHANGELOG.md`, `CONTRIBUTING.md`, `SECURITY.md`, `CLAUDE.md`
- `docs/*.md` (spec, schema, security, threat-model, style-guide, engagement-guide, ADRs under `docs/adr/`)
- `.claude/agent_prompt_template.md`, `.claude/diff_review.md`, `.claude/commands/*.md`

Skip anything under `tmp/`, `node_modules/`, `vendor/`, or `.git/`. Skip
`LICENSE` (verbatim Apache-2.0) and `CODE_OF_CONDUCT.md` (Contributor Covenant
standard text).

If the scope is empty, say so and stop.

## Step 2: Load the rules

Read these three files in full:

- `.vale/styles/xray/AntiLLM.yml` — substitution list (~50 swaps)
- `.vale/styles/xray/Hedging.yml` — banned hedging tokens
- `docs/style-guide.md` — load-bearing phrases (do not soften security
  guarantees), severity policy, override conventions

If `--preload` was passed, stop here. The rules are in context; draft in the
next turn.

## Step 3: Audit

For each file in scope, scan for:

- **AntiLLM substitutions** — each banned phrase in `AntiLLM.yml` mapped to
  its replacement. *Compound pattern:* when a banned qualifier
  (*comprehensive, robust, seamless*) is followed by an em-dash
  parenthetical that defines what the qualifier was hedging, cut both — the
  list carries the meaning.
<!-- vale xray.Hedging = NO -->
- **Hedging tokens** — each pattern in `Hedging.yml`. *Sentence-fragment
  form:* "Simply X" or "Just X" as a sentence opener is the same violation
  as the mid-sentence form.
<!-- vale xray.Hedging = YES -->
- **Load-bearing phrases** (style-guide.md §"Load-bearing phrases") — do
  **not** rewrite "no source content stored", "read-only", "tokens never
  logged", "team-level only", or similar security guarantees. Leave them.
  If Vale flags them, the override is a `<!-- vale off -->` block, not a
  rewrite.
- **Sentence case in headings** (`xray.SentenceCase`) — only the first word
  and proper nouns are capitalised. The exception list covers product names
  and acronyms.
- **Spelling against the vocab** (`Vale.Spelling` + `accept.txt`). If a new
  technical term is correctly spelled but flagged, add it to
  `.vale/styles/config/vocabularies/xray/accept.txt` in the same commit;
  do not rephrase around it.

Severity classification:

- **fix** — banned-phrase violation, hedging token, sentence-case heading
  drift, anything `make prose` would report as `warning` or `error`
- **leave** — load-bearing phrase, deliberate term in vocab, anything inside
  a documented `<!-- vale … -->` block

## Step 4: Apply fixes (default mode)

Apply every **fix** classification in place. Use `Edit` for targeted
substitutions, `Write` only for whole-section rewrites.

Common fix patterns:

- **Banned phrase**: substitute the recommended replacement from
  `AntiLLM.yml`. If no obvious replacement fits the sentence, restructure
  to remove the qualifier — terse beats clever.
<!-- vale xray.Hedging = NO -->
<!-- vale xray.AntiLLM = NO -->
- **Hedging opener**: cut the leading "Simply " / "Just " / "Obviously " —
  the sentence reads cleaner.
- **Compound qualifier + parenthetical**: cut both. *"a robust solution —
  detection, prevention, recovery —"* → *"detection, prevention, recovery"*.
<!-- vale xray.Hedging = YES -->
<!-- vale xray.AntiLLM = YES -->
- **Heading title-case → sentence case**: lowercase every word after the
  first that isn't a proper noun or acronym.

For `--report-only`: list each finding with file:line, the rule it tripped,
and the proposed fix. Do not edit.

## Step 5: Verify

Run `make prose` after the edits:

```
make prose
```

The bar is **0 errors**. Warnings are expected (token-rule violations
intentionally stay at `warning` per `docs/style-guide.md` — see the
explanation under `AntiLLM.yml`). New warnings in your diff are a smell;
investigate before declaring done.

## Step 6: Report

Summarise to the user:

- Files audited
- Fixes applied (count by category: banned-phrase, hedging, heading,
  load-bearing override)
- Findings deliberately left (with reason: load-bearing, vocab-correct,
  inside override block)
- `make prose` result (errors + warning delta)

Do not offer follow-up tasks; end the message.

## Notes on the loop

This command exists because `make prose` is a **reactive** gate (write →
fail → rewrite) and the project's preferred loop is **preventive** (load
the rules → draft against them → backstop with Vale). The `--preload` flag
is the preventive entry; the no-flag default is the cleanup pass for
prose that landed before the rules were loaded.

Gauge Intelligence's `vale_promotion_policy.md` is the source for keeping
token-level rules at `warning`: an agent shown a literal-phrase rule pack
routes around it, producing semantically-identical filler in novel surface
forms. The defence is internalising the register, not memorising the swap
list.
