// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

package baseline

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"strings"
)

// FileResult is one synced file's state in one repository.
type FileResult struct {
	Path    string
	State   string // "in sync", "drifted", "missing"
	Diff    string // unified diff, repository → baseline
	Changed int
}

// Check is one structural rule that is not a whole file: the Go pin, the
// workflows calling the reusable ones, CHANGELOG.md.
type Check struct {
	Name   string
	Detail string
	OK     bool
}

// RepoResult is everything drift found in one repository.
type RepoResult struct {
	Err       error
	Repo      string
	Commit    string
	Files     []FileResult
	Checks    []Check
	Overrides []Override
}

// Drifted reports whether anything in the repository departs from the
// baseline beyond its reasoned overrides.
func (r *RepoResult) Drifted() bool {
	if r.Err != nil {
		return true
	}
	for _, f := range r.Files {
		if f.State != stateInSync {
			return true
		}
	}
	for _, c := range r.Checks {
		if !c.OK {
			return true
		}
	}
	return false
}

const (
	stateInSync  = "in sync"
	stateDrifted = "drifted"
	stateMissing = "missing"
)

var (
	goCIRef      = regexp.MustCompile(`uses:\s*blairham/\.github/\.github/workflows/go-ci\.yml@([0-9a-f]{40})`)
	goReleaseRef = regexp.MustCompile(`uses:\s*blairham/\.github/\.github/workflows/go-release\.yml@([0-9a-f]{40})`)
)

// CheckRepo compares one repository's tree with the rendered baseline.
// baselineSHA, when set, is the blairham/.github commit the reusable
// workflows should be pinned to; a different pin is reported, not failed,
// because dependabot moves it.
func CheckRepo(root, repo string, src Source, groups []Group, baselineSHA string) *RepoResult {
	r := &RepoResult{Repo: repo}
	if gs, ok := src.(*GitHubSource); ok {
		r.Commit = gs.Commit
	}
	ovs, err := LoadOverrides(root, repo)
	if err != nil {
		r.Err = err
		return r
	}
	r.Overrides = ovs
	knobs, err := ApplyOverrides(ovs)
	if err != nil {
		r.Err = err
		return r
	}
	facts, err := GatherFacts(repo, src, groups)
	if err != nil {
		r.Err = err
		return r
	}
	facts.Knobs = knobs
	want, err := Render(root, &facts)
	if err != nil {
		r.Err = err
		return r
	}
	for _, f := range Files {
		have, err := src.ReadFile(f.Path)
		res := FileResult{Path: f.Path}
		switch {
		case errors.Is(err, fs.ErrNotExist):
			res.State = stateMissing
		case err != nil:
			r.Err = err
			return r
		case bytes.Equal(have, want[f.Path]):
			res.State = stateInSync
		default:
			res.State = stateDrifted
			res.Diff = UnifiedDiff(repo+"/"+f.Path, "baseline/"+f.Path, have, want[f.Path])
			res.Changed = ChangedLines(res.Diff)
		}
		r.Files = append(r.Files, res)
	}
	r.Checks = structuralChecks(src, &knobs, baselineSHA)
	return r
}

func structuralChecks(src Source, knobs *Knobs, baselineSHA string) []Check {
	read := func(p string) string {
		b, err := src.ReadFile(p)
		if err != nil {
			return ""
		}
		return string(b)
	}

	goDirective := GoDirective([]byte(read("go.mod")))
	toolVersions := ToolVersionsGolang([]byte(read(".tool-versions")))
	checks := []Check{
		{
			Name: "go.mod go directive", OK: goDirective == GoVersion,
			Detail: fmt.Sprintf("%s (baseline %s)", orNone(goDirective), GoVersion),
		},
		{
			Name: ".tool-versions golang", OK: toolVersions == GoVersion,
			Detail: fmt.Sprintf("%s (baseline %s)", orNone(toolVersions), GoVersion),
		},
		pinCheck("ci.yml calls go-ci.yml", read(".github/workflows/ci.yml"), goCIRef, baselineSHA),
		pinCheck("release.yml calls go-release.yml", read(".github/workflows/release.yml"), goReleaseRef, baselineSHA),
		strayReleaseCheck(src, read),
	}

	_, err := src.ReadFile("CHANGELOG.md")
	hasChangelog := err == nil
	if knobs.ReleaseNotes == "generated" {
		return append(checks, Check{
			Name: "release notes", OK: true,
			Detail: "generated (override); CHANGELOG.md not required",
		})
	}
	return append(checks, Check{
		Name: "CHANGELOG.md drives release notes", OK: hasChangelog,
		Detail: map[bool]string{true: "present", false: "missing"}[hasChangelog],
	})
}

// strayReleaseCheck: the release workflow is always release.yml; another
// workflow running GoReleaser is a second, unsynced release path.
func strayReleaseCheck(src Source, read func(string) string) Check {
	const name = "no release workflow but release.yml"
	paths, err := src.Paths()
	if err != nil {
		return Check{Name: name, Detail: err.Error()}
	}
	var stray []string
	for _, p := range paths {
		if strings.HasPrefix(p, ".github/workflows/") && p != ".github/workflows/release.yml" &&
			strings.Contains(read(p), "goreleaser/goreleaser-action") {
			stray = append(stray, p)
		}
	}
	return Check{Name: name, OK: len(stray) == 0, Detail: orNone(strings.Join(stray, ", "))}
}

func pinCheck(name, content string, re *regexp.Regexp, baselineSHA string) Check {
	m := re.FindStringSubmatch(content)
	if len(m) != 2 {
		if content == "" {
			return Check{Name: name, Detail: "workflow missing"}
		}
		return Check{Name: name, Detail: "does not call it by a full commit SHA"}
	}
	detail := "pinned at " + m[1][:12]
	if baselineSHA != "" && m[1] != baselineSHA {
		detail += " (baseline main is " + short(baselineSHA) + "; dependabot moves the pin)"
	}
	return Check{Name: name, OK: true, Detail: detail}
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

const (
	markOK    = "ok"
	markDrift = "**DRIFT**"
)

// Report renders results as Markdown. With diffs false it is the summary an
// issue body can hold; with diffs true every drifted file's diff follows.
func Report(results []*RepoResult, baselineSHA, runURL string, diffs bool) string {
	var sb strings.Builder
	drifted := 0
	for _, r := range results {
		if r.Drifted() {
			drifted++
		}
	}
	sb.WriteString("# Configuration drift\n\n")
	base := "working tree"
	if baselineSHA != "" {
		base = "`" + short(baselineSHA) + "`"
	}
	fmt.Fprintf(&sb, "Baseline: blairham/.github %s. **%d of %d** repositories drift from it.\n\n",
		base, drifted, len(results))
	sb.WriteString("Files are compared byte for byte with what `make sync` would render; ")
	sb.WriteString("a known exception is an override in `overrides/<repo>.yml`, listed with its reason.\n")
	if runURL != "" {
		fmt.Fprintf(&sb, "Full diffs: the `drift-report` artifact of [this run](%s).\n", runURL)
	}
	writeSummaryTable(&sb, results)
	for _, r := range results {
		writeRepoSection(&sb, r, diffs)
	}
	return sb.String()
}

func writeSummaryTable(sb *strings.Builder, results []*RepoResult) {
	sb.WriteString("\n| Repository | Files drifted | Files missing | Checks failing | Known exceptions |\n")
	sb.WriteString("|---|---|---|---|---|\n")
	for _, r := range results {
		if r.Err != nil {
			fmt.Fprintf(sb, "| %s | error | | | |\n", r.Repo)
			continue
		}
		nd, nm, nc := 0, 0, 0
		for _, f := range r.Files {
			switch f.State {
			case stateDrifted:
				nd++
			case stateMissing:
				nm++
			}
		}
		for _, c := range r.Checks {
			if !c.OK {
				nc++
			}
		}
		fmt.Fprintf(sb, "| %s | %d | %d | %d | %d |\n", r.Repo, nd, nm, nc, len(r.Overrides))
	}
}

func writeRepoSection(sb *strings.Builder, r *RepoResult, diffs bool) {
	fmt.Fprintf(sb, "\n## %s", r.Repo)
	if r.Commit != "" {
		fmt.Fprintf(sb, " @ `%s`", short(r.Commit))
	}
	sb.WriteString("\n\n")
	if r.Err != nil {
		fmt.Fprintf(sb, "**Error:** %v\n", r.Err)
		return
	}
	for _, f := range r.Files {
		mark, detail := markOK, f.State
		switch f.State {
		case stateDrifted:
			mark, detail = markDrift, fmt.Sprintf("drifted (%d lines)", f.Changed)
		case stateMissing:
			mark = markDrift
		}
		fmt.Fprintf(sb, "- %s `%s` — %s\n", mark, f.Path, detail)
	}
	for _, c := range r.Checks {
		mark := markOK
		if !c.OK {
			mark = markDrift
		}
		fmt.Fprintf(sb, "- %s %s — %s\n", mark, c.Name, c.Detail)
	}
	if len(r.Overrides) > 0 {
		sb.WriteString("\nKnown exceptions:\n")
		for _, o := range r.Overrides {
			fmt.Fprintf(sb, "- `%s` — %s", o.Knob, strings.TrimSpace(o.Reason))
			if o.Ref != "" {
				fmt.Fprintf(sb, " (%s)", o.Ref)
			}
			sb.WriteString("\n")
		}
	}
	if !diffs {
		return
	}
	for _, f := range r.Files {
		if f.Diff != "" {
			fmt.Fprintf(sb, "\n<details><summary><code>%s</code></summary>\n\n```diff\n%s```\n\n</details>\n",
				f.Path, f.Diff)
		}
	}
}
