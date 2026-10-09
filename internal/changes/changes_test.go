// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

package changes

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// script is the action's implementation, run as the action runs it.
var script, _ = filepath.Abs("../../.github/actions/changes/changes.sh")

// repo makes a git repository with a base commit holding base, then a head
// commit adding changed, and returns its directory and the base commit.
// Git runs with no user or system config, so neither the person's identity,
// signing setup nor hook template reaches it.
func repo(t *testing.T, changed ...string) (dir, base string) {
	t.Helper()
	dir = t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "--template=", "-b", "main")
	write(t, dir, "README.md", "x\n")
	git("add", "-A")
	git("commit", "-q", "-m", "base")
	base = git("rev-parse", "HEAD")
	for _, f := range changed {
		write(t, dir, f, "changed\n")
	}
	git("add", "-A")
	git("commit", "-q", "--allow-empty", "-m", "head")
	return dir, base
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// run runs the script and returns its outputs, or the failure.
func run(t *testing.T, dir, event, base, filters string) (map[string]string, error) {
	t.Helper()
	out := filepath.Join(t.TempDir(), "output")
	cmd := exec.Command("bash", script)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "EVENT_NAME="+event, "BASE="+base, "FILTERS="+filters, "GITHUB_OUTPUT="+out,
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	if b, err := cmd.CombinedOutput(); err != nil {
		return nil, &runError{err: err, out: string(b)}
	}
	b, err := os.ReadFile(out) //nolint:gosec // the test's own temp file
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for line := range strings.Lines(string(b)) {
		k, v, _ := strings.Cut(strings.TrimSpace(line), "=")
		got[k] = v
	}
	return got, nil
}

type runError struct {
	err error
	out string
}

func (e *runError) Error() string { return e.err.Error() + "\n" + e.out }

const filters = `{"go": ["\\.go$"], "kafka": ["^internal/kafka/", "^hack/kafka"], "never": ["^no-such-path$"]}`

func TestPullRequest(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		code    string
		matches string
		changed []string
	}{
		{name: "prose only", changed: []string{"docs/a.md", "LICENSE"}, code: "false", matches: `{"go":"false","kafka":"false","never":"false"}`},
		{name: "go outside kafka", changed: []string{"cmd/x/main.go"}, code: "true", matches: `{"go":"true","kafka":"false","never":"false"}`},
		{name: "kafka by its second regex", changed: []string{"hack/kafka-up.sh"}, code: "true", matches: `{"go":"false","kafka":"true","never":"false"}`},
		{name: "both", changed: []string{"internal/kafka/admin.go", "README.md"}, code: "true", matches: `{"go":"true","kafka":"true","never":"false"}`},
		{name: "non-prose, no filter", changed: []string{"Makefile"}, code: "true", matches: `{"go":"false","kafka":"false","never":"false"}`},
	} {
		dir, base := repo(t, tc.changed...)
		got, err := run(t, dir, "pull_request", base, filters)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got["code"] != tc.code || got["matches"] != tc.matches {
			t.Errorf("%s: code=%s matches=%s, want code=%s matches=%s",
				tc.name, got["code"], got["matches"], tc.code, tc.matches)
		}
	}
}

// Outside a pull request, or without the base commit, everything is true —
// including a filter no path could match, which proves the "run everything"
// answer is not just the diff happening to match.
func TestRunsEverythingWhenUnsure(t *testing.T) {
	t.Parallel()
	dir, base := repo(t, "docs/a.md")
	want := `{"go":"true","kafka":"true","never":"true"}`
	for name, args := range map[string][2]string{
		"push":           {"push", base},
		"schedule":       {"schedule", ""},
		"missing base":   {"pull_request", strings.Repeat("f", 40)},
		"empty base":     {"pull_request", ""},
		"all-zero (new)": {"pull_request", strings.Repeat("0", 40)},
	} {
		got, err := run(t, dir, args[0], args[1], filters)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got["code"] != "true" || got["matches"] != want {
			t.Errorf("%s: code=%s matches=%s, want true and %s", name, got["code"], got["matches"], want)
		}
	}
}

func TestNoFilters(t *testing.T) {
	t.Parallel()
	dir, base := repo(t, "main.go")
	for _, f := range []string{"", "{}"} {
		got, err := run(t, dir, "pull_request", base, f)
		if err != nil {
			t.Fatal(err)
		}
		if got["code"] != "true" || got["matches"] != "{}" {
			t.Errorf("filters %q: code=%s matches=%s", f, got["code"], got["matches"])
		}
	}
}

func TestRefusesBadFilters(t *testing.T) {
	t.Parallel()
	dir, base := repo(t, "main.go")
	for _, f := range []string{
		`["\\.go$"]`,            // not an object
		`{"go": "\\.go$"}`,      // value not a list
		`{"go": [1]}`,           // not strings
		`{"go": ["(unclosed"]}`, // does not compile
		`not json`,
	} {
		if _, err := run(t, dir, "pull_request", base, f); err == nil {
			t.Errorf("filters %s: accepted", f)
		}
	}
}
