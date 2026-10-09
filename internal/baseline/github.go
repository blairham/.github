// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

package baseline

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"time"
)

// GitHubSource is a repository's default branch, read through the REST API.
// Every read is pinned to one commit, so a report describes a single tree
// even if the branch moves while it runs. Read-only: it needs no scope a
// workflow's default GITHUB_TOKEN lacks on a public repository.
type GitHubSource struct {
	client *http.Client
	files  map[string][]byte
	Owner  string
	Repo   string
	Commit string
	token  string
	paths  []string
}

// NewGitHubSource resolves the default branch's head commit. The token comes
// from GH_TOKEN or GITHUB_TOKEN; unauthenticated reads work but are limited
// to 60 requests an hour.
func NewGitHubSource(ctx context.Context, owner, repo string) (*GitHubSource, error) {
	g := &GitHubSource{
		Owner:  owner,
		Repo:   repo,
		client: &http.Client{Timeout: 30 * time.Second},
		files:  map[string][]byte{},
		token:  os.Getenv("GH_TOKEN"),
	}
	if g.token == "" {
		g.token = os.Getenv("GITHUB_TOKEN")
	}
	var meta struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := g.getJSON(ctx, fmt.Sprintf("/repos/%s/%s", owner, repo), &meta); err != nil {
		return nil, err
	}
	var branch struct {
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	if err := g.getJSON(
		ctx,
		fmt.Sprintf("/repos/%s/%s/branches/%s", owner, repo, url.PathEscape(meta.DefaultBranch)),
		&branch,
	); err != nil {
		return nil, err
	}
	g.Commit = branch.Commit.SHA
	if g.Commit == "" {
		return nil, fmt.Errorf("%s/%s: could not resolve the head of %s", owner, repo, meta.DefaultBranch)
	}
	var tree struct {
		Tree []struct {
			Path string `json:"path"`
			Type string `json:"type"`
		} `json:"tree"`
		Truncated bool `json:"truncated"`
	}
	if err := g.getJSON(
		ctx,
		fmt.Sprintf("/repos/%s/%s/git/trees/%s?recursive=1", owner, repo, g.Commit),
		&tree,
	); err != nil {
		return nil, err
	}
	if tree.Truncated {
		// Facts derived from a partial listing would be wrong silently.
		return nil, fmt.Errorf("%s/%s: tree listing truncated", owner, repo)
	}
	for _, e := range tree.Tree {
		if e.Type == "blob" {
			g.paths = append(g.paths, e.Path)
		}
	}
	if len(g.paths) == 0 {
		return nil, fmt.Errorf("%s/%s: tree at %s lists no files", owner, repo, g.Commit)
	}
	return g, nil
}

// Paths implements Source.
func (g *GitHubSource) Paths() ([]string, error) { return g.paths, nil }

// ReadFile implements Source.
func (g *GitHubSource) ReadFile(path string) ([]byte, error) {
	if b, ok := g.files[path]; ok {
		if b == nil {
			return nil, fs.ErrNotExist
		}
		return b, nil
	}
	found := false
	for _, p := range g.paths {
		if p == path {
			found = true
			break
		}
	}
	if !found {
		g.files[path] = nil
		return nil, fs.ErrNotExist
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	u := fmt.Sprintf("/repos/%s/%s/contents/%s?ref=%s", g.Owner, g.Repo, path, g.Commit)
	b, err := g.get(ctx, u, "application/vnd.github.raw")
	if err != nil {
		return nil, err
	}
	g.files[path] = b
	return b, nil
}

func (g *GitHubSource) getJSON(ctx context.Context, path string, v any) error {
	b, err := g.get(ctx, path, "application/vnd.github+json")
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func (g *GitHubSource) get(ctx context.Context, path, accept string) (body []byte, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com"+path, http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if g.token != "" {
		req.Header.Set("Authorization", "Bearer "+g.token)
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()
	body, err = io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s: %.200s", path, resp.Status, body)
	}
	return body, nil
}

// LatestTag returns the highest vX.Y.Z tag of owner/repo and the commit it
// names, or a zero Pin when there is none.
func LatestTag(ctx context.Context, owner, repo string) (Pin, error) {
	g := &GitHubSource{
		client: &http.Client{Timeout: 30 * time.Second},
		token:  os.Getenv("GH_TOKEN"),
	}
	if g.token == "" {
		g.token = os.Getenv("GITHUB_TOKEN")
	}
	var tags []struct {
		Name   string `json:"name"`
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	if err := g.getJSON(ctx, fmt.Sprintf("/repos/%s/%s/tags?per_page=100", owner, repo), &tags); err != nil {
		return Pin{}, err
	}
	var best Pin
	var bestV [3]int
	for _, t := range tags {
		v, ok := parseSemver(t.Name)
		if !ok {
			continue
		}
		if best.Tag == "" || semverLess(bestV, v) {
			best, bestV = Pin{Tag: t.Name, SHA: t.Commit.SHA}, v
		}
	}
	return best, nil
}

var semverTag = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)$`)

func parseSemver(s string) ([3]int, bool) {
	m := semverTag.FindStringSubmatch(s)
	if m == nil {
		return [3]int{}, false
	}
	var v [3]int
	for i := range 3 {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return v, false
		}
		v[i] = n
	}
	return v, true
}

func semverLess(a, b [3]int) bool {
	for i := range 3 {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}
