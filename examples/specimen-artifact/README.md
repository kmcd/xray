## Specimen artifact

`specimen.tar.gz` is a pre-built xray artifact from
[git-chglog/git-chglog](https://github.com/git-chglog/git-chglog), a
public GitHub repository. It exists so an engineer can inspect exactly
what xray produces before letting the binary touch their organisation.

The artifact covers a short 7-day window (2026-06-03..2026-06-10). Run
`make regen` for a fuller 90-day extraction against
[goreleaser/goreleaser](https://github.com/goreleaser/goreleaser), which
has richer PR and review history.

The artifact contains:

- SQLite metrics from public git and GitHub data
- No source code
- No secrets
- No raw PR or commit bodies (structural data only)

### Inspect

```bash
xray inspect examples/specimen-artifact/specimen.tar.gz
```

### Regenerate

```bash
export GITHUB_TOKEN="ghp_..."
make -C examples/specimen-artifact regen
```

`make regen` requires `GITHUB_TOKEN` to be set. It targets
goreleaser/goreleaser with a 90-day window using `config.toml`.
