// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

package baseline

import (
	"io/fs"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// root is the repository root, where baseline/ and overrides/ live.
const root = "../.."

func TestParseOverridesRefusesMissingReason(t *testing.T) {
	t.Parallel()
	_, err := ParseOverrides([]byte("overrides:\n  - knob: release.notes\n    value: generated\n"))
	if err == nil || !strings.Contains(err.Error(), "no reason") {
		t.Fatalf("want a no-reason error, got %v", err)
	}
	// The same entry with a reason is accepted, so the refusal above was the
	// reason and not something else about the entry.
	if _, err := ParseOverrides(
		[]byte("overrides:\n  - knob: release.notes\n    value: generated\n    reason: because\n"),
	); err != nil {
		t.Fatalf("with a reason: %v", err)
	}
}

func TestParseOverridesRefusesUnknownKnobAndField(t *testing.T) {
	t.Parallel()
	for name, doc := range map[string]string{
		"unknown knob":  "overrides:\n  - knob: golangci.anything\n    value: 1\n    reason: r\n",
		"unknown field": "overrides:\n  - knob: release.notes\n    value: generated\n    reason: r\n    file: x\n",
		"set twice":     "overrides:\n  - knob: release.notes\n    value: generated\n    reason: r\n  - knob: release.notes\n    value: changelog\n    reason: r\n",
		"no value":      "overrides:\n  - knob: release.notes\n    reason: r\n",
	} {
		if _, err := ParseOverrides([]byte(doc)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// Every overrides file in the repository parses and applies.
func TestCommittedOverrides(t *testing.T) {
	t.Parallel()
	matches, err := filepath.Glob(filepath.Join(root, "overrides", "*.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 {
		t.Fatal("no overrides files found; the glob is wrong")
	}
	for _, m := range matches {
		repo := strings.TrimSuffix(filepath.Base(m), ".yml")
		ovs, err := LoadOverrides(root, repo)
		if err != nil {
			t.Errorf("%s: %v", repo, err)
			continue
		}
		if _, err := ApplyOverrides(ovs); err != nil {
			t.Errorf("%s: %v", repo, err)
		}
	}
}

// The baseline is the shared 36-linter set, read from the template itself.
func TestLinters(t *testing.T) {
	t.Parallel()
	got, err := Linters(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 36 {
		t.Fatalf("the baseline is the 36-linter set; template enables %d: %v", len(got), got)
	}
	for _, want := range []string{"errcheck", "importas", "wastedassign"} { //nolint:misspell // importas is a linter, not a typo
		if !slices.Contains(got, want) {
			t.Errorf("%s missing from %v", want, got)
		}
	}
}

func TestRenderRefusesUnknownPendingLinter(t *testing.T) {
	t.Parallel()
	groups, err := LoadGroups(root)
	if err != nil {
		t.Fatal(err)
	}
	facts, err := GatherFacts("example", memSource{"go.mod": testGoMod}, groups)
	if err != nil {
		t.Fatal(err)
	}
	facts.Knobs = DefaultKnobs()
	facts.Knobs.GolangciLintersPending = []string{"funlen"}
	if _, err := Render(root, &facts); err != nil {
		t.Fatalf("a real baseline linter was refused: %v", err)
	}
	facts.Knobs.GolangciLintersPending = []string{"nosuchlinter"}
	if _, err := Render(root, &facts); err == nil {
		t.Fatal("a pending linter outside the baseline was accepted")
	}
}

// memSource is an in-memory tree.
type memSource map[string]string

func (m memSource) ReadFile(p string) ([]byte, error) {
	s, ok := m[p]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return []byte(s), nil
}

func (m memSource) Paths() ([]string, error) {
	out := make([]string, 0, len(m))
	for p := range m {
		out = append(out, p)
	}
	slices.Sort(out)
	return out, nil
}

const testGoMod = `module github.com/blairham/example

go 1.26.8

require (
	github.com/aws/aws-sdk-go-v2/service/s3 v1.0.0
	k8s.io/client-go v0.1.0 // indirect
)
`

func renderFor(t *testing.T, src memSource, overrides string) map[string][]byte {
	t.Helper()
	groups, err := LoadGroups(root)
	if err != nil {
		t.Fatal(err)
	}
	facts, err := GatherFacts("example", src, groups)
	if err != nil {
		t.Fatal(err)
	}
	ovs, err := ParseOverrides([]byte(overrides))
	if err != nil {
		t.Fatal(err)
	}
	if facts.Knobs, err = ApplyOverrides(ovs); err != nil {
		t.Fatal(err)
	}
	out, err := Render(root, &facts)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRenderDerivesFacts(t *testing.T) {
	t.Parallel()
	plain := renderFor(t, memSource{"go.mod": testGoMod}, "overrides: []\n")
	dep := string(plain[".github/dependabot.yml"])
	if !strings.Contains(dep, "aws-sdk:") {
		t.Error("a direct aws-sdk-go-v2 require did not produce the aws-sdk group")
	}
	if strings.Contains(dep, "kubernetes:") {
		t.Error("an indirect k8s.io require produced the kubernetes group")
	}
	if strings.Contains(dep, "package-ecosystem: docker") {
		t.Error("docker ecosystem without a Dockerfile")
	}
	if !strings.Contains(string(plain[".golangci.yml"]), "- github.com/blairham/example") {
		t.Error("module path not templated into goimports local-prefixes")
	}
	if strings.Contains(string(plain[".pre-commit-config.yaml"]), "charts/") {
		t.Error("chart excludes without charts")
	}

	withImage := renderFor(t, memSource{
		"go.mod":               testGoMod,
		"Dockerfile":           "FROM --platform=$BUILDPLATFORM public.ecr.aws/docker/library/golang:1.26-alpine AS build\n",
		"charts/x/Chart.yaml":  "name: x\n",
		"config/crd/bases/a.y": "",
	}, "overrides: []\n")
	dep = string(withImage[".github/dependabot.yml"])
	if !strings.Contains(dep, "package-ecosystem: docker") ||
		!strings.Contains(dep, "dependency-name: docker/library/golang") {
		t.Errorf("Dockerfile did not produce the docker ecosystem with the mirror name:\n%s", dep)
	}
	pc := string(withImage[".pre-commit-config.yaml"])
	if !strings.Contains(pc, "exclude: ^(charts/[^/]+/templates/|config/)") {
		t.Errorf("charts + config/crd did not produce the yamllint exclude:\n%s", pc)
	}
	if !strings.Contains(string(withImage[".yamllint.yml"]), "charts/*/templates/") {
		t.Error("charts did not produce the yamllint ignore")
	}
}

func TestRenderAppliesKnobs(t *testing.T) {
	t.Parallel()
	src := memSource{"go.mod": testGoMod}
	base := renderFor(t, src, "overrides: []\n")
	if !strings.Contains(string(base[".pre-commit-config.yaml"]), "alias: golangci-lint-new") {
		t.Fatal("the baseline lacks the CI golangci-lint alias")
	}
	if !strings.Contains(string(base[".golangci.yml"]), "    - funlen # ") {
		t.Fatal("the baseline lacks funlen; the test below would prove nothing")
	}

	out := renderFor(t, src, `overrides:
  - knob: golangci.linters-pending
    value: [funlen, wastedassign]
    reason: r
  - knob: pre-commit.golangci-lint
    value: false
    reason: r
  - knob: pre-commit.gofumpt-cmd
    value: true
    reason: r
  - knob: pre-commit.go-vulncheck
    value: false
    reason: r
  - knob: gitleaks.allowlist
    value: |
      [allowlist]
      regexes = ['''EXAMPLE''']
    reason: r
`)
	gc := string(out[".golangci.yml"])
	for _, l := range []string{"funlen", "wastedassign"} {
		if regexp.MustCompile(`(?m)^    - ` + l + `\b`).MatchString(gc) {
			t.Errorf("%s still enabled", l)
		}
		if !strings.Contains(gc, "    #   "+l+"\n") {
			t.Errorf("%s not listed as pending", l)
		}
	}
	// funlen's settings block stays: only the enable entry goes.
	if !strings.Contains(gc, "    funlen:\n") {
		t.Error("funlen's settings were removed with its enable entry")
	}
	pc := string(out[".pre-commit-config.yaml"])
	if strings.Contains(pc, "golangci-lint") && strings.Contains(pc, "repo: https://github.com/golangci/golangci-lint") {
		t.Error("golangci-lint hooks kept with pre-commit.golangci-lint=false")
	}
	if !strings.Contains(pc, "alias: gofumpt") {
		t.Error("gofumpt-cmd did not add the my-cmd-repo alias")
	}
	if strings.Contains(pc, "go-vulncheck") {
		t.Error("go-vulncheck kept")
	}
	if !strings.Contains(string(out[".gitleaks.toml"]), "regexes = ['''EXAMPLE''']") {
		t.Errorf("allowlist not appended:\n%s", out[".gitleaks.toml"])
	}
}

func TestUnifiedDiff(t *testing.T) {
	t.Parallel()
	if d := UnifiedDiff("a", "b", []byte("x\n"), []byte("x\n")); d != "" {
		t.Fatalf("equal inputs diffed: %q", d)
	}
	a := []byte("1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n")
	b := []byte("1\n2\n3\n4\nfive\n6\n7\n8\n9\n10\neleven\n")
	d := UnifiedDiff("a", "b", a, b)
	want := "--- a\n+++ b\n@@ -2,9 +2,10 @@\n 2\n 3\n 4\n-5\n+five\n 6\n 7\n 8\n 9\n 10\n+eleven\n"
	if d != want {
		t.Fatalf("got\n%s\nwant\n%s", d, want)
	}
	if n := ChangedLines(d); n != 3 {
		t.Fatalf("ChangedLines = %d, want 3", n)
	}
}

func TestStructuralChecks(t *testing.T) {
	t.Parallel()
	sha := strings.Repeat("a", 40)
	good := memSource{
		"go.mod":                        testGoMod,
		".tool-versions":                "golang 1.26.8\n",
		".github/workflows/ci.yml":      "    uses: blairham/.github/.github/workflows/go-ci.yml@" + sha + " # main\n",
		".github/workflows/release.yml": "    uses: blairham/.github/.github/workflows/go-release.yml@" + sha + " # main\n",
		"CHANGELOG.md":                  "# Changelog\n",
	}
	knobs := DefaultKnobs()
	for _, c := range structuralChecks(good, &knobs, sha) {
		if !c.OK {
			t.Errorf("good tree failed %s: %s", c.Name, c.Detail)
		}
	}
	bad := memSource{
		"go.mod":                           strings.Replace(testGoMod, "go 1.26.8", "go 1.26.6", 1),
		".tool-versions":                   "golang 1.26.6\n",
		".github/workflows/ci.yml":         "jobs: {}\n",
		".github/workflows/goreleaser.yml": "uses: goreleaser/goreleaser-action@x\n",
	}
	failed := 0
	for _, c := range structuralChecks(bad, &knobs, sha) {
		if !c.OK {
			failed++
		}
	}
	if failed != 6 {
		t.Errorf("bad tree failed %d checks, want all 6", failed)
	}
}
