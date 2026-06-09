## Summary

<!-- What does this PR change and why? -->

## Pre-flight checklist

- [ ] `make gates` passes (lint + govulncheck + coverage)
- [ ] Schema change? If yes: `schema_version` bumped and `docs/schema.md` updated
- [ ] New dependency? If yes: ADR entry added to `tmp/adr.md`
- [ ] Connector change? Reviewed against invariants in `CLAUDE.md` (read-only, no source content, provenance per-row, tokens never logged)
- [ ] User-visible change? `README.md` and/or `docs/spec.md` updated
