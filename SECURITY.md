# Security Policy

## What this repository is

blairham/.github holds the reusable GitHub Actions workflows that build,
sign and release blairham's Go repositories, and the baseline configuration
they share. Its workflows run with the permissions *their callers* grant,
including `contents: write`, `id-token: write` and `packages: write` during a
release, so a defect here is a defect in every caller's release.

- **`go-release.yml` is the signing identity.** Keyless cosign signatures and
  build-provenance attestations made by a caller's release are issued to
  `https://github.com/blairham/.github/.github/workflows/go-release.yml@<sha>`,
  with the calling repository and tag in the certificate's GitHub Workflow
  Repository and Ref extensions. Each caller's `SECURITY.md` shows how to
  verify against that identity.
- Callers pin every reusable workflow by **full commit SHA**, and every
  action used here is pinned the same way, so a change here reaches a
  caller only through a reviewed pin bump.
- Secrets reach a reusable workflow only as **named** `secrets:` (for
  example `HOMEBREW_TAP_TOKEN`), never `secrets: inherit`.
- `drift.yml` reads the governed repositories through the API with the
  default, read-only `GITHUB_TOKEN`, and writes only this repository's
  issues.

## Supported versions

Only `main`. Callers pin a commit of it.

## Reporting a vulnerability

**Do not open a public issue.** Report it privately through GitHub:
[Security → Report a vulnerability](https://github.com/blairham/.github/security/advisories/new).

Please include the affected workflow and commit, what an attacker can do, and
the steps to reproduce. You should receive a response within a week.

In scope, among others:

- a reusable workflow that signs, attests or publishes something other than
  what the caller's tagged tree builds
- a path by which a pull request (from a fork or otherwise) reaches a token,
  a secret or the OIDC signing identity
- an input that is interpolated into a shell without quoting
- the drift check writing anywhere but this repository's issues
