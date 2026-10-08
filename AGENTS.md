# AGENTS.md — blairham/.github

Guidance for AI coding agents working in this repo. `CLAUDE.md` imports it, and
other tools read this file directly. The cross-repo working agreements in
`~/Developer/github.com/blairham/AGENTS.md` apply here too.

## Project Overview

The shared CI/release machinery and configuration baseline for blairham's
public Go repositories (the list is `repos.yml`). Two things live here:

1. **Reusable workflows** (`on: workflow_call`) that callers pin by full
   commit SHA:
   - `.github/workflows/go-ci.yml` — Pre-commit, Detect changed files,
     Build and test (OS matrix), Fuzz. Output `code`.
   - `.github/workflows/go-release.yml` — GoReleaser, keyless cosign over
     `checksums.txt`, `actions/attest-build-provenance`, the bundle attached
     as `<repo>-<tag>.intoto.jsonl`, optional image provenance; `snapshot`
     input for a dry run.
   - `.github/workflows/go-image.yml` — build-only Dockerfile check.
   - `.github/workflows/go-chart.yml` — publish and sign Helm charts.
   Scorecard and CodeQL are deliberately **not** reusable: they are synced
   files, because Scorecard's `publish_results` only accepts a workflow that
   runs in the repository it grades.
2. **The config baseline**: `baseline/*.tmpl` rendered into each repository
   by `make sync`, with `overrides/<repo>.yml` for reasoned departures, and
   `make drift` / `drift.yml` reporting how far each repository's `main` has
   drifted.

The module is `github.com/blairham/dotgithub` (`.github` is not a legal
module path element). Go 1.26.8, gofumpt and golangci-lint pinned in go.mod's
`tool` block. No release: callers pin commits of `main`.

## Quick Reference

```sh
make sync REPO=k8s-controller-kit DIR=../k8s-controller-kit-baseline  # render into a checkout
make drift                                                           # read-only report, all repos
go run ./cmd/baseline knobs                                          # what an override may set
go run ./cmd/baseline overrides                                      # validate overrides/*.yml
make test
```

## Baseline and overrides

Synced files (`internal/baseline/render.go` `Files`): `.golangci.yml`,
`.editorconfig`, `.pre-commit-config.yaml`, `.yamllint.yml`, `.gitleaks.toml`,
`.github/dependabot.yml`, `.github/CODEOWNERS`,
`.github/workflows/scorecard.yml`, `.github/workflows/codeql.yml`.
Templates use `[% %]` delimiters so `${{ }}` in workflows passes through.

**Derived values are not overrides.** The module path, Dockerfile presence
(docker ecosystem; `golang` vs `docker/library/golang` from its `FROM`),
`charts/*/Chart.yaml` (Helm template excludes), `config/crd/` (controller-gen
output excluded from yamllint) and dependabot groups (from go.mod's direct
requires, table in `baseline/dependabot-groups.yml`) all come from the
repository's tree.

**An override** is an entry in `overrides/<repo>.yml`:

```yaml
overrides:
  - knob: pre-commit.go-vulncheck   # must be a known knob
    value: false
    reason: >-                       # required; the tool refuses an entry without one
      Why this repository differs.
    ref: https://github.com/blairham/<repo>/issues/N   # optional
```

Knobs are a closed set defined in `internal/baseline/overrides.go`; add one
there (with a test) rather than reaching for a free-form patch. Every
departure must be approved by Blair before it is encoded — a new override is a
decision, not a fix.

Structural checks `drift` also runs: go.mod `go` and `.tool-versions`
`golang` equal `GoVersion` (1.26.8); `ci.yml` calls `go-ci.yml` and
`release.yml` calls `go-release.yml` by full SHA; no other workflow runs
GoReleaser; `CHANGELOG.md` exists unless `release.notes: generated`.

## Conventions

- Every action pinned by full commit SHA with a `# vX.Y.Z` comment.
- A reusable workflow's required-check names are `<caller job name> /
  <callee job name>`; renaming a callee job renames every caller's required
  check. Treat job names as API.
- Gate code jobs inside their steps on `code`, never with a job-level `if:`
  or a workflow `paths:` filter: a required check that never runs is
  pending forever.
- Pre-commit in CI is diff-scoped (`--from-ref/--to-ref`), never
  `--all-files`, and golangci-lint runs as the `golangci-lint-new` alias
  (`--new-from-merge-base=origin/main --fix=false`).
- After changing anything a caller uses, the repository's own `ci.yml` runs
  `go-ci.yml` by local path, and its `Baseline in sync` job fails if this
  repository's own synced files differ from what `make sync` renders.

## Testing

`go test -race ./...`. Tests read the real `baseline/` and `overrides/` (no
network); `drift` against GitHub is exercised by `make drift` and the
workflow, and must be shown to report drift on a repository known to
differ, not only a clean result.
