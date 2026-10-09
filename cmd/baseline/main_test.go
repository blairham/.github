// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blairham/dotgithub/internal/baseline"
)

// A GitHub API that rate-limits the repository being checked — the 403 a
// lane hit with the unauthenticated quota spent — must fail the run (exit 2)
// and say the check failed, never report the repository as drifting.
// Not parallel: it sets the package's API base and the environment.
func TestDriftFetchErrorIsAFailedCheckNotDrift(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/blairham/.github/tags":
			_, _ = w.Write([]byte(`[]`))
		case "/go":
			_, _ = w.Write([]byte(`[{"version": "go1.26.9", "stable": true}]`))
		default:
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message": "API rate limit exceeded for 127.0.0.1."}`))
		}
	}))
	defer srv.Close()
	old := baseline.APIBase
	baseline.APIBase = srv.URL
	defer func() { baseline.APIBase = old }()
	t.Setenv("GH_TOKEN", "test-token") // never reach a real gh

	out := filepath.Join(t.TempDir(), "drift.md")
	err := runDrift([]string{
		"-root", "../..", "-repo", "tuikit", "-exit-code",
		"-go-releases", srv.URL + "/go", "-o", out,
	})
	if code := exitCode(err); code != 2 {
		t.Fatalf("exit code %d (err %v), want 2", code, err)
	}
	b, rerr := os.ReadFile(out) //nolint:gosec // the test's own temp file
	if rerr != nil {
		t.Fatal(rerr)
	}
	report := string(b)
	for _, want := range []string{"**0 of 1** repositories drift", "CHECK FAILED for 1 of 1", "| tuikit | check failed |", "rate limit"} {
		if !strings.Contains(report, want) {
			t.Errorf("report lacks %q:\n%s", want, report)
		}
	}
}

func TestExitCode(t *testing.T) {
	t.Parallel()
	if exitCode(nil) != 0 || exitCode(exitError(1)) != 1 || exitCode(os.ErrNotExist) != 2 {
		t.Fatal("exit codes: want 0 clean, 1 drift, 2 any other error")
	}
}
