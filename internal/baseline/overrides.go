// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

package baseline

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Override is one reasoned departure from the baseline, as written in
// overrides/<repo>.yml.
type Override struct {
	Knob   string    `yaml:"knob"`
	Reason string    `yaml:"reason"`
	Ref    string    `yaml:"ref,omitempty"`
	Value  yaml.Node `yaml:"value"`
}

type overridesFile struct {
	Overrides []Override `yaml:"overrides"`
}

// Knobs are the values a repository may set away from the baseline. Each one
// is named, typed and documented here; an override naming anything else is
// refused, so there is no way to depart from the baseline silently.
type Knobs struct {
	// GitleaksAllowlist is TOML appended to .gitleaks.toml.
	GitleaksAllowlist string
	// ReleaseNotes is "changelog" (CHANGELOG.md drives the notes) or
	// "generated" (GoReleaser's commit list; no CHANGELOG.md).
	ReleaseNotes string
	// GolangciLintersPending are baseline linters the repository does not
	// run yet; they are removed from its .golangci.yml.
	GolangciLintersPending []string
	// PreCommitGolangciLint keeps the golangci-lint hooks in
	// .pre-commit-config.yaml. False means the repository lints in a CI job.
	PreCommitGolangciLint bool
	// PreCommitGofumptCmd formats with gofumpt alone (my-cmd-repo alias)
	// instead of `golangci-lint-fmt`.
	PreCommitGofumptCmd bool
	// PreCommitGoVulncheck keeps the go-vulncheck hook.
	PreCommitGoVulncheck bool
}

// DefaultKnobs is the baseline.
func DefaultKnobs() Knobs {
	return Knobs{
		ReleaseNotes:          "changelog",
		PreCommitGolangciLint: true,
		PreCommitGoVulncheck:  true,
	}
}

// knobSpec documents and applies one knob.
type knobSpec struct {
	apply func(k *Knobs, v *yaml.Node) error
	doc   string
}

var knobSpecs = map[string]knobSpec{
	"gitleaks.allowlist": {
		doc: "TOML ([allowlist] table) appended to .gitleaks.toml",
		apply: func(k *Knobs, v *yaml.Node) error {
			return v.Decode(&k.GitleaksAllowlist)
		},
	},
	"release.notes": {
		doc: `"changelog" (default) or "generated"; generated drops the CHANGELOG.md requirement`,
		apply: func(k *Knobs, v *yaml.Node) error {
			var s string
			if err := v.Decode(&s); err != nil {
				return err
			}
			if s != "changelog" && s != "generated" {
				return fmt.Errorf("release.notes must be changelog or generated, not %q", s)
			}
			k.ReleaseNotes = s
			return nil
		},
	},
	"golangci.linters-pending": {
		// Render refuses a name that is not in the baseline's
		// linters.enable, so a typo cannot pass as an adopted linter.
		doc: "baseline linters this repository does not run yet",
		apply: func(k *Knobs, v *yaml.Node) error {
			return v.Decode(&k.GolangciLintersPending)
		},
	},
	"pre-commit.golangci-lint": {
		doc: "false drops the golangci-lint hooks (lint runs as a CI job instead)",
		apply: func(k *Knobs, v *yaml.Node) error {
			return v.Decode(&k.PreCommitGolangciLint)
		},
	},
	"pre-commit.gofumpt-cmd": {
		doc: "true formats with `go tool gofumpt` (my-cmd-repo alias) instead of golangci-lint-fmt",
		apply: func(k *Knobs, v *yaml.Node) error {
			return v.Decode(&k.PreCommitGofumptCmd)
		},
	},
	"pre-commit.go-vulncheck": {
		doc: "false drops the go-vulncheck hook",
		apply: func(k *Knobs, v *yaml.Node) error {
			return v.Decode(&k.PreCommitGoVulncheck)
		},
	},
}

// KnobNames lists every knob an override may set, sorted.
func KnobNames() []string {
	names := make([]string, 0, len(knobSpecs))
	for n := range knobSpecs {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

// KnobDoc returns a knob's one-line description.
func KnobDoc(name string) string { return knobSpecs[name].doc }

// ParseOverrides reads an overrides file's bytes. Every entry must name a
// known knob, at most once, and carry a non-empty reason.
func ParseOverrides(data []byte) ([]Override, error) {
	var f overridesFile
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var errs []error
	for i, o := range f.Overrides {
		where := fmt.Sprintf("overrides[%d]", i)
		if o.Knob == "" {
			errs = append(errs, fmt.Errorf("%s: no knob", where))
			continue
		}
		where += " (" + o.Knob + ")"
		if _, ok := knobSpecs[o.Knob]; !ok {
			errs = append(errs, fmt.Errorf("%s: unknown knob; known: %s", where, strings.Join(KnobNames(), ", ")))
		}
		if strings.TrimSpace(o.Reason) == "" {
			errs = append(errs, fmt.Errorf("%s: no reason — every departure from the baseline says why", where))
		}
		if o.Value.Kind == 0 {
			errs = append(errs, fmt.Errorf("%s: no value", where))
		}
		if seen[o.Knob] {
			errs = append(errs, fmt.Errorf("%s: set twice", where))
		}
		seen[o.Knob] = true
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return f.Overrides, nil
}

// LoadOverrides reads overrides/<repo>.yml under root. A missing file means
// the repository takes the baseline unchanged.
func LoadOverrides(root, repo string) ([]Override, error) {
	path := filepath.Join(root, "overrides", repo+".yml")
	data, err := os.ReadFile(path) //nolint:gosec // a path under the checkout, built from a repo name
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	ovs, err := ParseOverrides(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return ovs, nil
}

// ApplyOverrides returns the baseline knobs with the overrides applied.
func ApplyOverrides(ovs []Override) (Knobs, error) {
	k := DefaultKnobs()
	for i := range ovs {
		o := &ovs[i]
		if err := knobSpecs[o.Knob].apply(&k, &o.Value); err != nil {
			return k, fmt.Errorf("%s: %w", o.Knob, err)
		}
	}
	return k, nil
}
