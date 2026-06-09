## Status

Accepted. v0.1.0.

## Context

The `is_dependency_manifest` column on `file_metrics` needed a definition.
Options: a generic build-system detector, or a static allowlist. The static
binary constraint ruled out anything with native dependencies or model loading.

## Decision

Static list of filenames recognised as dependency manifests:
`Gemfile`, `Gemfile.lock`, `package.json`, `package-lock.json`, `yarn.lock`,
`pnpm-lock.yaml`, `go.mod`, `go.sum`, `Cargo.toml`, `Cargo.lock`,
`requirements.txt`, `Pipfile`, `Pipfile.lock`, `poetry.lock`, `pyproject.toml`,
`composer.json`, `composer.lock`, `pom.xml`, `build.gradle`,
`build.gradle.kts`, `Podfile`, `Podfile.lock`, `mix.exs`, `mix.lock`.

## Consequences

**Positive.** Avoids dragging in a generic build-system detector; the set is
small and stable.

**Negative.** New package managers not in the list go undetected until the
list is updated.

**Neutral.** Path-based detection; no content inspection required.

## How to apply

`docs/schema.md` — `file_metrics.is_dependency_manifest` column notes
reference this ADR. The static list lives in the file_metrics extractor.
