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
	pin := Pin{Tag: "v0.0.1", SHA: sha}
	good := memSource{
		"go.mod":                        testGoMod,
		".tool-versions":                "golang 1.26.8\n",
		".github/workflows/ci.yml":      "    uses: blairham/.github/.github/workflows/go-ci.yml@" + sha + " # v0.0.1\n",
		".github/workflows/release.yml": "    uses: blairham/.github/.github/workflows/go-release.yml@" + sha + " # v0.0.1\n",
		"CHANGELOG.md":                  "# Changelog\n",
	}
	knobs := DefaultKnobs()
	for _, c := range structuralChecks(good, &knobs, pin) {
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
	for _, c := range structuralChecks(bad, &knobs, pin) {
		if !c.OK {
			failed++
		}
	}
	if failed != 6 {
		t.Errorf("bad tree failed %d checks, want all 6", failed)
	}
}

func TestPinCheck(t *testing.T) {
	t.Parallel()
	latest, older := strings.Repeat("b", 40), strings.Repeat("a", 40)
	pin := Pin{Tag: "v0.0.2", SHA: latest}
	line := func(sha, comment string) string {
		return "    uses: blairham/.github/.github/workflows/go-ci.yml@" + sha + comment + "\n"
	}
	for _, tc := range []struct {
		name, content string
		pin           Pin
		ok            bool
	}{
		{"latest tag, tag comment", line(latest, " # v0.0.2"), pin, true},
		{"older pin", line(older, " # v0.0.1"), pin, false},
		{"latest sha, branch comment", line(latest, " # main"), pin, false},
		{"latest sha, no comment", line(latest, ""), pin, false},
		{"no tag yet", line(older, " # main"), Pin{}, true},
		{"not a sha", "    uses: blairham/.github/.github/workflows/go-ci.yml@main\n", pin, false},
	} {
		if got := pinCheck("x", tc.content, goCIRef, tc.pin); got.OK != tc.ok {
			t.Errorf("%s: OK=%v, want %v (%s)", tc.name, got.OK, tc.ok, got.Detail)
		}
	}
}

func TestParseSemver(t *testing.T) {
	t.Parallel()
	if !semverLess([3]int{0, 0, 9}, [3]int{0, 0, 10}) {
		t.Error("v0.0.9 < v0.0.10 must compare numerically")
	}
	if _, ok := parseSemver("v1.2"); ok {
		t.Error("v1.2 parsed")
	}
	if v, ok := parseSemver("v1.20.3"); !ok || v != [3]int{1, 20, 3} {
		t.Errorf("v1.20.3 -> %v %v", v, ok)
	}
}

func TestNewKnobsRefuseBadValues(t *testing.T) {
	t.Parallel()
	for name, doc := range map[string]string{
		"exclusion without reason":  "  - knob: golangci.exclusions\n    reason: r\n    value: [{path: x/, linters: [funlen]}]\n",
		"exclusion without linters": "  - knob: golangci.exclusions\n    reason: r\n    value: [{path: x/, linters: [], reason: r}]\n",
		"exclusion with both paths": "  - knob: golangci.exclusions\n    reason: r\n    value: [{path: x/, path-except: y/, linters: [funlen], reason: r}]\n",
		"exclusion unknown field":   "  - knob: golangci.exclusions\n    reason: r\n    value: [{paths: x/, linters: [funlen], reason: r}]\n",
		"gosec not a rule":          "  - knob: golangci.gosec-excludes-extra\n    reason: r\n    value: [G1]\n",
		"concurrency zero":          "  - knob: golangci.concurrency\n    reason: r\n    value: 0\n",
		"timeout not a duration":    "  - knob: golangci.timeout\n    reason: r\n    value: soon\n",
		"misspell two words":        "  - knob: misspell.ignore\n    reason: r\n    value: [\"a,b\"]\n",
		"docker ignore no reason":   "  - knob: dependabot.docker-ignore\n    reason: r\n    value: [{dependency-name: x, update-types: [version-update:semver-major]}]\n",
		"docker ignore bad type":    "  - knob: dependabot.docker-ignore\n    reason: r\n    value: [{dependency-name: x, update-types: [major], reason: r}]\n",
	} {
		ovs, err := ParseOverrides([]byte("overrides:\n" + doc))
		if err == nil {
			_, err = ApplyOverrides(ovs)
		}
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestRenderRefusesKnobsWithNoEffect(t *testing.T) {
	t.Parallel()
	groups, err := LoadGroups(root)
	if err != nil {
		t.Fatal(err)
	}
	facts, err := GatherFacts("example", memSource{"go.mod": testGoMod}, groups)
	if err != nil {
		t.Fatal(err)
	}
	for name, set := range map[string]func(*Knobs){
		"govet analyzer not in the baseline": func(k *Knobs) { k.GolangciGovetDisable = []string{"nosuch"} },
		"formatter not in the baseline":      func(k *Knobs) { k.GolangciFormattersPending = []string{"nosuch"} },
		"extra linter already in baseline":   func(k *Knobs) { k.GolangciLintersExtra = []ExtraLinter{{Name: "funlen"}} },
		"docker ignore without a Dockerfile": func(k *Knobs) {
			k.DependabotDockerIgnore = []DockerIgnore{{DependencyName: "x", UpdateTypes: []string{"version-update:semver-major"}, Reason: "r"}}
		},
	} {
		f := facts
		f.Knobs = DefaultKnobs()
		set(&f.Knobs)
		if _, renderErr := Render(root, &f); renderErr == nil {
			t.Errorf("%s: rendered", name)
		}
	}
	// The real names render, so the refusals above are about the names.
	f := facts
	f.Knobs = DefaultKnobs()
	f.Knobs.GolangciGovetDisable = []string{"fieldalignment"}
	f.Knobs.GolangciFormattersPending = []string{"golines"}
	out, err := Render(root, &f)
	if err != nil {
		t.Fatal(err)
	}
	gc := string(out[".golangci.yml"])
	if strings.Contains(gc, "        - fieldalignment\n") || strings.Contains(gc, "    - golines\n") {
		t.Errorf("disabled analyzer or pending formatter still enabled:\n%s", gc)
	}
	if !strings.Contains(gc, "    golines:\n") {
		t.Error("golines settings removed with its enable entry")
	}
}

func TestMisspellIgnore(t *testing.T) {
	t.Parallel()
	want := "import" + "as,colour" // split so no misspell --fix can rewrite it
	if got := misspellIgnore([]string{"colour"}); got != want {
		t.Fatalf("misspellIgnore = %q, want %q", got, want)
	}
}

// fixersWouldLeave is what pre-commit's trailing-whitespace and
// end-of-file-fixer hooks leave of b: no trailing spaces or tabs on any line,
// and exactly one final newline (an empty file stays empty).
func fixersWouldLeave(b []byte) []byte {
	lines := strings.Split(string(b), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	s := strings.TrimRight(strings.Join(lines, "\n"), "\n")
	if s == "" {
		return nil
	}
	return []byte(s + "\n")
}

// Every rendered file, for the baseline and for every repository's
// overrides, must survive the commit hook's whitespace fixers unchanged;
// otherwise the committed file can never equal the render and drift flags it
// forever.
func TestRenderedFilesSurviveWhitespaceFixers(t *testing.T) {
	t.Parallel()
	groups, err := LoadGroups(root)
	if err != nil {
		t.Fatal(err)
	}
	matches, err := filepath.Glob(filepath.Join(root, "overrides", "*.yml"))
	if err != nil {
		t.Fatal(err)
	}
	repos := make([]string, 0, 1+len(matches))
	repos = append(repos, "no-overrides")
	for _, m := range matches {
		repos = append(repos, strings.TrimSuffix(filepath.Base(m), ".yml"))
	}
	if len(repos) < 5 {
		t.Fatalf("only %d repositories found; the glob is wrong", len(repos))
	}
	// A tree with everything a knob can depend on, so every override renders.
	src := memSource{
		"go.mod":              testGoMod,
		"Dockerfile":          "FROM golang:1.26 AS build\n",
		"charts/x/Chart.yaml": "name: x\n",
		"config/crd/a.yaml":   "",
	}
	for _, repo := range repos {
		ovs, err := LoadOverrides(root, repo)
		if err != nil {
			t.Fatal(err)
		}
		facts, err := GatherFacts(repo, src, groups)
		if err != nil {
			t.Fatal(err)
		}
		if facts.Knobs, err = ApplyOverrides(ovs); err != nil {
			t.Fatal(err)
		}
		out, err := Render(root, &facts)
		if err != nil {
			t.Fatalf("%s: %v", repo, err)
		}
		for path, b := range out {
			if fixed := fixersWouldLeave(b); string(fixed) != string(b) {
				t.Errorf("%s %s: the whitespace fixers would change it:\n%s", repo, path,
					UnifiedDiff("render", "fixed", b, fixed))
			}
		}
	}
}
