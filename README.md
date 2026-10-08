# blairham/.github

Shared CI and release workflows, and the configuration baseline, for
blairham's public Go repositories. See [AGENTS.md](AGENTS.md) for how it fits
together.

- **Reusable workflows** — `go-ci.yml`, `go-release.yml`, `go-image.yml`,
  `go-chart.yml` under `.github/workflows/`, called by SHA from each
  repository's `ci.yml` / `release.yml`.
- **Baseline** — `baseline/` rendered into a repository with
  `make sync REPO=<name> DIR=<checkout>`; reasoned departures in
  `overrides/<repo>.yml`.
- **Drift** — `make drift` locally, and `drift.yml` weekly, which keeps one
  issue here listing every repository that differs.

Licensed under the [Apache License 2.0](LICENSE).
