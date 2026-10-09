// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

package baseline

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// GoDownloadsURL lists the current Go releases: the latest patch of each
// supported minor.
const GoDownloadsURL = "https://go.dev/dl/?mode=json"

// LatestGoPatch returns the newest stable minor.N release listed at url, e.g.
// "1.26.9" for minor "1.26". It fails — never guesses — when the list cannot
// be fetched or parsed, or names no stable release on that minor (which is
// also what it looks like once the minor is out of support).
func LatestGoPatch(ctx context.Context, url, minor string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return "", err
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching Go releases: %w", err)
	}
	body, err := io.ReadAll(resp.Body)
	if cerr := resp.Body.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", fmt.Errorf("fetching Go releases: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetching Go releases: %s", resp.Status)
	}
	return latestPatch(body, minor)
}

func latestPatch(body []byte, minor string) (string, error) {
	var releases []struct {
		Version string `json:"version"`
		Stable  bool   `json:"stable"`
	}
	if err := json.Unmarshal(body, &releases); err != nil {
		return "", fmt.Errorf("parsing Go releases: %w", err)
	}
	best, bestPatch := "", -1
	prefix := "go" + minor + "."
	for _, r := range releases {
		rest, ok := strings.CutPrefix(r.Version, prefix)
		if !ok || !r.Stable {
			continue
		}
		patch, err := strconv.Atoi(rest)
		if err != nil {
			continue
		}
		if patch > bestPatch {
			best, bestPatch = minor+"."+rest, patch
		}
	}
	if best == "" {
		return "", fmt.Errorf("no stable Go %s.x among %d listed releases (is %s out of support?)",
			minor, len(releases), minor)
	}
	return best, nil
}
