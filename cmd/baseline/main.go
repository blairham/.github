// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

// Command baseline renders blairham/.github's baseline files into a
// repository (sync) and reports how far every governed repository's default
// branch has drifted from them (drift).
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/blairham/dotgithub/internal/baseline"
)

const usage = `usage:
  baseline sync -repo NAME -dir PATH     render the baseline into a checkout
  baseline drift [-o FILE] [-diffs FILE] compare every repo's main with the baseline
  baseline overrides                     validate every overrides/*.yml
  baseline knobs                         list what an override may set`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "sync":
		err = runSync(os.Args[2:])
	case "drift":
		err = runDrift(os.Args[2:])
	case "overrides":
		err = runOverrides(os.Args[2:])
	case "knobs":
		for _, n := range baseline.KnobNames() {
			fmt.Printf("%-26s %s\n", n, baseline.KnobDoc(n))
		}
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	var exit exitError
	if errors.As(err, &exit) {
		os.Exit(int(exit))
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "baseline:", err)
		os.Exit(1)
	}
}

type exitError int

func (e exitError) Error() string { return fmt.Sprintf("exit %d", int(e)) }

func runSync(args []string) error {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	repo := fs.String("repo", "", "repository name (overrides/<repo>.yml)")
	dir := fs.String("dir", "", "checkout to render into")
	root := fs.String("root", ".", "blairham/.github checkout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *repo == "" || *dir == "" {
		return errors.New("sync needs -repo and -dir")
	}
	groups, err := baseline.LoadGroups(*root)
	if err != nil {
		return err
	}
	ovs, err := baseline.LoadOverrides(*root, *repo)
	if err != nil {
		return err
	}
	knobs, err := baseline.ApplyOverrides(ovs)
	if err != nil {
		return err
	}
	facts, err := baseline.GatherFacts(*repo, baseline.DirSource{Dir: *dir}, groups)
	if err != nil {
		return err
	}
	facts.Knobs = knobs
	files, err := baseline.Render(*root, &facts)
	if err != nil {
		return err
	}
	for _, f := range baseline.Files {
		state, err := writeIfChanged(filepath.Join(*dir, filepath.FromSlash(f.Path)), files[f.Path])
		if err != nil {
			return err
		}
		fmt.Printf("%-10s %s\n", state, f.Path)
	}
	for _, o := range ovs {
		fmt.Printf("override   %s — %s\n", o.Knob, o.Reason)
	}
	return nil
}

// writeIfChanged writes data to path unless it already holds exactly that.
func writeIfChanged(path string, data []byte) (string, error) {
	old, err := os.ReadFile(path) //nolint:gosec // a baseline path under the named checkout
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err == nil && bytes.Equal(old, data) {
		return "unchanged", nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return "", err
	}
	// 0644 like every tracked file in a checkout; an existing file keeps its mode.
	if err := os.WriteFile(path, data, 0o644); err != nil { //nolint:gosec // tracked config, not a secret
		return "", err
	}
	return "written", nil
}

type reposFile struct {
	Owner string   `yaml:"owner"`
	Repos []string `yaml:"repos"`
}

func runDrift(args []string) error {
	fs := flag.NewFlagSet("drift", flag.ContinueOnError)
	root := fs.String("root", ".", "blairham/.github checkout")
	out := fs.String("o", "", "write the summary report here (default stdout)")
	diffsOut := fs.String("diffs", "", "also write the report with every diff here")
	sha := fs.String("baseline-sha", "", "blairham/.github commit the workflows should be pinned to")
	runURL := fs.String("run-url", "", "link to the workflow run, for the report")
	only := fs.String("repo", "", "check just this repository")
	exitCode := fs.Bool("exit-code", false, "exit 1 when anything drifts")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rf, err := loadRepos(*root, *only)
	if err != nil {
		return err
	}
	groups, err := baseline.LoadGroups(*root)
	if err != nil {
		return err
	}
	ctx := context.Background()
	results := make([]*baseline.RepoResult, 0, len(rf.Repos))
	for _, repo := range rf.Repos {
		src, srcErr := baseline.NewGitHubSource(ctx, rf.Owner, repo)
		if srcErr != nil {
			results = append(results, &baseline.RepoResult{Repo: repo, Err: srcErr})
			continue
		}
		results = append(results, baseline.CheckRepo(*root, repo, src, groups, *sha))
	}
	if err := writeReports(results, *sha, *runURL, *out, *diffsOut); err != nil {
		return err
	}
	failed := 0
	for _, r := range results {
		if r.Err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", r.Repo, r.Err)
		}
		if r.Drifted() {
			failed++
		}
	}
	fmt.Fprintf(os.Stderr, "%d of %d repositories drift\n", failed, len(results))
	if *exitCode && failed > 0 {
		return exitError(1)
	}
	return nil
}

// loadRepos reads repos.yml, narrowed to one repository when only is set.
func loadRepos(root, only string) (reposFile, error) {
	var rf reposFile
	data, err := os.ReadFile(filepath.Join(root, "repos.yml")) //nolint:gosec // the checkout's own repos.yml
	if err != nil {
		return rf, err
	}
	if uerr := yaml.Unmarshal(data, &rf); uerr != nil {
		return rf, uerr
	}
	if only != "" {
		if !slices.Contains(rf.Repos, only) {
			return rf, fmt.Errorf("%s is not in repos.yml", only)
		}
		rf.Repos = []string{only}
	}
	if rf.Owner == "" || len(rf.Repos) == 0 {
		return rf, errors.New("repos.yml names no owner or no repositories")
	}
	return rf, nil
}

func writeReports(results []*baseline.RepoResult, sha, runURL, out, diffsOut string) error {
	summary := baseline.Report(results, sha, runURL, false)
	if out == "" {
		fmt.Print(summary)
	} else if err := os.WriteFile(out, []byte(summary), 0o600); err != nil {
		return err
	}
	if diffsOut == "" {
		return nil
	}
	return os.WriteFile(diffsOut, []byte(baseline.Report(results, sha, runURL, true)), 0o600)
}

func runOverrides(args []string) error {
	fs := flag.NewFlagSet("overrides", flag.ContinueOnError)
	root := fs.String("root", ".", "blairham/.github checkout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	matches, err := filepath.Glob(filepath.Join(*root, "overrides", "*.yml"))
	if err != nil {
		return err
	}
	if len(matches) == 0 {
		return errors.New("no overrides/*.yml found; wrong -root?")
	}
	rf, err := loadRepos(*root, "")
	if err != nil {
		return err
	}
	var errs []error
	for _, m := range matches {
		repo := strings.TrimSuffix(filepath.Base(m), ".yml")
		if !slices.Contains(rf.Repos, repo) {
			errs = append(errs, fmt.Errorf("%s: %s is not in repos.yml", m, repo))
		}
		ovs, loadErr := baseline.LoadOverrides(*root, repo)
		if loadErr == nil {
			_, loadErr = baseline.ApplyOverrides(ovs)
		}
		if loadErr != nil {
			errs = append(errs, loadErr)
			continue
		}
		fmt.Printf("%s: %d override(s) ok\n", repo, len(ovs))
	}
	return errors.Join(errs...)
}
