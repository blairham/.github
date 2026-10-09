# AGENTS.md — blairham/.github

Guidance for AI coding agents working in this repo. `CLAUDE.md` imports it, and
other tools read this file directly. The cross-repo working agreements in
`~/Developer/github.com/blairham/AGENTS.md` apply here too.

## Project Overview

The shared CI/release machinery and configuration baseline for blairham's
public Go repositories (the list is `repos.yml`). Two things live here:

1. **Reusable workflows** (`on: workflow_call`) that callers pin by the
   full commit SHA of the latest `vX.Y.Z` tag, with the tag as a comment
   (`@<sha> # v0.0.0`) so dependabot bumps it:
   - `.github/workflows/go-ci.yml` — Pre-commit, Detect changed files,
     Build and test (OS matrix), Fuzz. Output `code`.
   - `.github/workflows/go-release.yml` — GoReleaser, keyless cosign over
     `checksums.txt`, `actions/attest-build-provenance`, the bundle attached
     as `<repo>-<tag>.intoto.jsonl`, optional image provenance; `snapshot`
     input for a dry run.
   - `.github/workflows/go-changes.yml` — change detection alone, outputs
     `code` and `matches` (named regex filters), so repository-specific jobs
     can `needs: changes` and start without waiting for all of go-ci.
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
module path element). Go 1.26 (the latest patch, like every governed repo), gofumpt and golangci-lint pinned in go.mod's
`tool` block.

**Releases are signed annotated tags** (`git tag -s vX.Y.Z`), cut from a
green `main`; there is no GitHub release or artifact. A tag is what callers
pin, so tag after any change callers should pick up, and dependabot opens the
bump in every caller.

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

Knobs are a closed set defined in `internal/baseline/knobs.go`
(`go run ./cmd/baseline knobs` lists them); add one there, typed and narrow
and with a test, rather than reaching for a free-form patch. List-valued
knobs whose items differ in purpose (`golangci.exclusions`,
`dependabot.docker-ignore`) carry a `reason` per item, rendered as a comment
beside it. A knob that would render to nothing for a repository (a docker
ignore without a Dockerfile, a pending or disabled name the baseline does not
have) is refused. Every
departure must be approved by Blair before it is encoded — a new override is a
decision, not a fix.

**Drift's GitHub reads** authenticate with `GH_TOKEN`, then `GITHUB_TOKEN`,
then `gh auth token`; with none of them it warns and reads unauthenticated
(60 requests an hour per IP, shared by everything on the machine). A
repository it cannot read — a 403, a rate limit, a network error — is
reported as **CHECK FAILED**, never as drift, and the run exits 2. Exit
status: 0 nothing drifts, 1 drift (with `-exit-code`), 2 the tool could not
look.

Structural checks `drift` also runs: go.mod `go` and `.tool-versions`
`golang` both equal the **latest published patch** of `GoMinor` (1.26),
read from https://go.dev/dl/?mode=json on every run, and equal each other — the
baseline pins the minor, never a patch, because patch releases are how stdlib
vulnerabilities get fixed and go-vulncheck rejects the old one. If the release
list cannot be fetched or lists no stable 1.26.x, drift exits 2 rather than
passing; `ci.yml` calls `go-ci.yml` and
`release.yml` calls `go-release.yml` at the latest blairham/.github tag's
commit with `# <tag>` (an older pin is drift); no other workflow runs
GoReleaser; `CHANGELOG.md` exists unless `release.notes: generated`.

## Repository-specific jobs: `needs: changes`, not `needs: ci`

`needs: ci` makes a job wait for pre-commit and build/test before it starts
(sh measured ~10 → ~21 min wall clock). A job that only needs to know what
changed depends on go-changes.yml instead:

```yaml
jobs:
  ci:
    name: CI
    uses: blairham/.github/.github/workflows/go-ci.yml@<latest tag sha> # vX.Y.Z
  changes:
    name: Changes
    uses: blairham/.github/.github/workflows/go-changes.yml@<latest tag sha> # vX.Y.Z
    with:
      filters: '{"kafka": ["^internal/kafka/", "^hack/kafka"]}'
  integration:
    name: Kafka integration
    needs: changes
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@<sha> # vX
        if: fromJSON(needs.changes.outputs.matches).kafka == 'true'
      # every step gated the same way
```

Gate steps, never the job (a skipped required job never reports). `code` is
the same prose/code answer go-ci uses. A repository without
repository-specific jobs does not call go-changes.yml; drift accepts it
either way, but a call must be pinned like go-ci.yml.

Both workflows run `.github/actions/changes`, pinned **by a commit of this
repository** (a reusable workflow's `./` resolves in the caller's checkout).
Changing the action is therefore two PRs: the action, then both pins bumped
together to its merge commit — `TestChangesActionPinsAgree` fails if they
differ. Its behavior is tested in `internal/changes`, and ci.yml runs it by
local path.

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
