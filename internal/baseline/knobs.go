// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

package baseline

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Knobs are the values a repository may set away from the baseline. Each one
// is named, typed and documented here; an override naming anything else is
// refused, so there is no way to depart from the baseline silently.
type Knobs struct {
	// GitleaksAllowlist is TOML appended to .gitleaks.toml.
	GitleaksAllowlist string
	// ReleaseNotes is "changelog" (CHANGELOG.md drives the notes) or
	// "generated" (GoReleaser's commit list; no CHANGELOG.md).
	ReleaseNotes string
	// GolangciTimeout is .golangci.yml's run.timeout.
	GolangciTimeout string
	// PreCommitGeneratedExclude is the regex of generated files the
	// rewriting hooks and the misspell hook skip.
	PreCommitGeneratedExclude string
	// GolangciLintersPending are baseline linters the repository does not
	// run yet; they are removed from its .golangci.yml.
	GolangciLintersPending []string
	// GolangciFormattersPending are baseline formatters it does not run yet.
	GolangciFormattersPending []string
	// GolangciLintersExtra are linters beyond the baseline, with settings.
	GolangciLintersExtra []ExtraLinter
	// GolangciExclusions are path-scoped linters.exclusions.rules, each
	// with its own reason.
	GolangciExclusions []Exclusion
	// GolangciSettingsPending are baseline settings not yet on here, staged
	// like GolangciLintersPending: "govet.<analyzer>" or "errcheck.<flag>".
	GolangciSettingsPending []string
	// GolangciSettingsOff are baseline settings turned off for good.
	GolangciSettingsOff []string
	// GolangciGosecExcludesExtra are gosec rules excluded beyond the
	// baseline's.
	GolangciGosecExcludesExtra []string
	// MisspellIgnore are words misspell (the hook and golangci-lint's)
	// accepts as written.
	MisspellIgnore []string
	// DependabotDockerIgnore are extra ignores on the docker ecosystem.
	DependabotDockerIgnore []DockerIgnore
	// GolangciConcurrency is .golangci.yml's run.concurrency.
	GolangciConcurrency int
	// PreCommitGolangciLint keeps the golangci-lint hooks in
	// .pre-commit-config.yaml. False means the repository lints in a CI job.
	PreCommitGolangciLint bool
	// PreCommitGofumptCmd formats with gofumpt alone (my-cmd-repo alias)
	// instead of `golangci-lint-fmt`.
	PreCommitGofumptCmd bool
	// PreCommitGoVulncheck keeps the go-vulncheck hook.
	PreCommitGoVulncheck bool
}

// ExtraLinter is a linter beyond the baseline and its settings block.
type ExtraLinter struct {
	Name     string    `yaml:"name"`
	Settings yaml.Node `yaml:"settings"`
}

// Exclusion is one linters.exclusions.rules entry: exactly one of Path or
// PathExcept, at least one linter, an optional text match, and a reason
// rendered beside it.
type Exclusion struct {
	Path       string   `yaml:"path"`
	PathExcept string   `yaml:"path-except"`
	Text       string   `yaml:"text"`
	Reason     string   `yaml:"reason"`
	Linters    []string `yaml:"linters"`
}

// DockerIgnore is one dependabot docker-ecosystem ignore, with its reason.
type DockerIgnore struct {
	DependencyName string   `yaml:"dependency-name"`
	Reason         string   `yaml:"reason"`
	UpdateTypes    []string `yaml:"update-types"`
}

// DefaultKnobs is the baseline.
func DefaultKnobs() Knobs {
	return Knobs{
		ReleaseNotes:              "changelog",
		GolangciTimeout:           "10m",
		GolangciConcurrency:       4,
		PreCommitGeneratedExclude: "^dist/",
		PreCommitGolangciLint:     true,
		PreCommitGoVulncheck:      true,
	}
}

// knobSpec documents and applies one knob.
type knobSpec struct {
	apply func(k *Knobs, v *yaml.Node) error
	doc   string
}

// strict decodes a node refusing unknown fields, so a misspelled key in an
// override is an error rather than a silently ignored value.
func strict(v *yaml.Node, out any) error {
	b, err := yaml.Marshal(v)
	if err != nil {
		return err
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	return dec.Decode(out)
}

var (
	gosecRule   = regexp.MustCompile(`^G\d{3}$`)
	linterName  = regexp.MustCompile(`^[a-z0-9]+$`)
	updateTypes = map[string]bool{
		"version-update:semver-major": true,
		"version-update:semver-minor": true,
		"version-update:semver-patch": true,
	}
)

var settingKey = regexp.MustCompile(`^(govet\.[a-z]+|errcheck\.[a-z-]+)$`)

// settingKeys decodes a list of setting keys. Their shape is checked here;
// that each names a setting the baseline actually has is checked at render,
// where the template says which settings exist.
func settingKeys(field string, v *yaml.Node, out *[]string) error {
	var l []string
	if err := strict(v, &l); err != nil {
		return err
	}
	if len(l) == 0 {
		return fmt.Errorf("%s: empty list; drop the override instead", field)
	}
	seen := map[string]bool{}
	for _, key := range l {
		if !settingKey.MatchString(key) {
			return fmt.Errorf("%s: %q is not a govet.<analyzer> or errcheck.<setting> key", field, key)
		}
		if seen[key] {
			return fmt.Errorf("%s: %q listed twice", field, key)
		}
		seen[key] = true
	}
	*out = l
	return nil
}

func names(field string, out *[]string) func(*Knobs, *yaml.Node) error {
	return func(_ *Knobs, v *yaml.Node) error {
		var l []string
		if err := strict(v, &l); err != nil {
			return err
		}
		if len(l) == 0 {
			return fmt.Errorf("%s: empty list; drop the override instead", field)
		}
		for _, n := range l {
			if !linterName.MatchString(n) {
				return fmt.Errorf("%s: %q is not a linter, formatter or analyzer name", field, n)
			}
		}
		*out = l
		return nil
	}
}

var knobSpecs = map[string]knobSpec{
	"gitleaks.allowlist": {
		doc: "TOML ([allowlist] table) appended to .gitleaks.toml",
		apply: func(k *Knobs, v *yaml.Node) error {
			return strict(v, &k.GitleaksAllowlist)
		},
	},
	"release.notes": {
		doc: `"changelog" (default) or "generated"; generated drops the CHANGELOG.md requirement`,
		apply: func(k *Knobs, v *yaml.Node) error {
			var s string
			if err := strict(v, &s); err != nil {
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
		doc: "baseline linters this repository does not run yet (staged adoption)",
		apply: func(k *Knobs, v *yaml.Node) error {
			return names("golangci.linters-pending", &k.GolangciLintersPending)(k, v)
		},
	},
	"golangci.formatters-pending": {
		doc: "baseline formatters this repository does not run yet (staged adoption)",
		apply: func(k *Knobs, v *yaml.Node) error {
			return names("golangci.formatters-pending", &k.GolangciFormattersPending)(k, v)
		},
	},
	"golangci.linters-extra": {
		doc: "linters beyond the baseline: [{name, settings}]",
		apply: func(k *Knobs, v *yaml.Node) error {
			var l []ExtraLinter
			if err := strict(v, &l); err != nil {
				return err
			}
			for i := range l {
				if !linterName.MatchString(l[i].Name) {
					return fmt.Errorf("golangci.linters-extra: %q is not a linter name", l[i].Name)
				}
				if l[i].Settings.Kind != 0 && l[i].Settings.Kind != yaml.MappingNode {
					return fmt.Errorf("golangci.linters-extra: %s settings must be a mapping", l[i].Name)
				}
			}
			k.GolangciLintersExtra = l
			return nil
		},
	},
	"golangci.exclusions": {
		doc: "path-scoped exclusion rules: [{path | path-except, linters, text?, reason}]",
		apply: func(k *Knobs, v *yaml.Node) error {
			var l []Exclusion
			if err := strict(v, &l); err != nil {
				return err
			}
			var errs []error
			for i, e := range l {
				where := fmt.Sprintf("golangci.exclusions[%d]", i)
				if (e.Path == "") == (e.PathExcept == "") {
					errs = append(errs, fmt.Errorf("%s: exactly one of path and path-except", where))
				}
				if len(e.Linters) == 0 {
					errs = append(errs, fmt.Errorf("%s: no linters — an exclusion is scoped to named linters", where))
				}
				if strings.TrimSpace(e.Reason) == "" {
					errs = append(errs, fmt.Errorf("%s: no reason", where))
				}
				for _, p := range []string{e.Path, e.PathExcept, e.Text} {
					if _, err := regexp.Compile(p); err != nil {
						errs = append(errs, fmt.Errorf("%s: %w", where, err))
					}
				}
			}
			k.GolangciExclusions = l
			return errors.Join(errs...)
		},
	},
	"golangci.settings-pending": {
		// Render refuses a key outside the baseline's settings.
		doc: "baseline settings not on yet, staged to shrink to empty: govet.<analyzer>, errcheck.check-blank, errcheck.check-type-assertions",
		apply: func(k *Knobs, v *yaml.Node) error {
			return settingKeys("golangci.settings-pending", v, &k.GolangciSettingsPending)
		},
	},
	"golangci.settings-off": {
		doc: "baseline settings off for good (same keys as settings-pending)",
		apply: func(k *Knobs, v *yaml.Node) error {
			return settingKeys("golangci.settings-off", v, &k.GolangciSettingsOff)
		},
	},
	"golangci.gosec-excludes-extra": {
		doc: "gosec rules (G123) excluded beyond the baseline's",
		apply: func(k *Knobs, v *yaml.Node) error {
			var l []string
			if err := strict(v, &l); err != nil {
				return err
			}
			for _, r := range l {
				if !gosecRule.MatchString(r) {
					return fmt.Errorf("golangci.gosec-excludes-extra: %q is not a gosec rule ID", r)
				}
			}
			k.GolangciGosecExcludesExtra = l
			return nil
		},
	},
	"golangci.concurrency": {
		doc: "run.concurrency (baseline 4)",
		apply: func(k *Knobs, v *yaml.Node) error {
			var n int
			if err := strict(v, &n); err != nil {
				return err
			}
			if n < 1 || n > 64 {
				return fmt.Errorf("golangci.concurrency: %d is out of 1..64", n)
			}
			k.GolangciConcurrency = n
			return nil
		},
	},
	"golangci.timeout": {
		doc: "run.timeout (baseline 10m)",
		apply: func(k *Knobs, v *yaml.Node) error {
			var s string
			if err := strict(v, &s); err != nil {
				return err
			}
			if _, err := time.ParseDuration(s); err != nil {
				return fmt.Errorf("golangci.timeout: %w", err)
			}
			k.GolangciTimeout = s
			return nil
		},
	},
	"misspell.ignore": {
		doc: "words misspell accepts as written, in the prose hook and golangci-lint alike",
		apply: func(k *Knobs, v *yaml.Node) error {
			var l []string
			if err := strict(v, &l); err != nil {
				return err
			}
			for _, w := range l {
				if w == "" || strings.ContainsAny(w, ", ") {
					return fmt.Errorf("misspell.ignore: %q is not one word", w)
				}
			}
			k.MisspellIgnore = l
			return nil
		},
	},
	"pre-commit.generated-exclude": {
		doc: "regex of generated files the whitespace fixers and misspell skip (baseline ^dist/)",
		apply: func(k *Knobs, v *yaml.Node) error {
			var s string
			if err := strict(v, &s); err != nil {
				return err
			}
			if _, err := regexp.Compile(s); err != nil {
				return fmt.Errorf("pre-commit.generated-exclude: %w", err)
			}
			k.PreCommitGeneratedExclude = s
			return nil
		},
	},
	"pre-commit.golangci-lint": {
		doc: "false drops the golangci-lint hooks (lint runs as a CI job instead)",
		apply: func(k *Knobs, v *yaml.Node) error {
			return strict(v, &k.PreCommitGolangciLint)
		},
	},
	"pre-commit.gofumpt-cmd": {
		doc: "true formats with `go tool gofumpt` (my-cmd-repo alias) instead of golangci-lint-fmt",
		apply: func(k *Knobs, v *yaml.Node) error {
			return strict(v, &k.PreCommitGofumptCmd)
		},
	},
	"pre-commit.go-vulncheck": {
		doc: "false drops the go-vulncheck hook",
		apply: func(k *Knobs, v *yaml.Node) error {
			return strict(v, &k.PreCommitGoVulncheck)
		},
	},
	"dependabot.docker-ignore": {
		doc: "extra docker-ecosystem ignores: [{dependency-name, update-types, reason}]",
		apply: func(k *Knobs, v *yaml.Node) error {
			var l []DockerIgnore
			if err := strict(v, &l); err != nil {
				return err
			}
			for i, d := range l {
				if d.DependencyName == "" || strings.TrimSpace(d.Reason) == "" || len(d.UpdateTypes) == 0 {
					return fmt.Errorf("dependabot.docker-ignore[%d]: needs dependency-name, update-types and reason", i)
				}
				for _, u := range d.UpdateTypes {
					if !updateTypes[u] {
						return fmt.Errorf("dependabot.docker-ignore[%d]: unknown update type %q", i, u)
					}
				}
			}
			k.DependabotDockerIgnore = l
			return nil
		},
	},
}
