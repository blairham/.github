// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

package baseline

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

// GoVersion is the toolchain every repository pins, in go.mod's `go`
// directive and .tool-versions' golang line alike.
const GoVersion = "1.26.8"

// Source is a repository's tree: a local checkout for `sync`, the default
// branch on GitHub for `drift`.
type Source interface {
	// ReadFile returns a file's contents, or fs.ErrNotExist.
	ReadFile(path string) ([]byte, error)
	// Paths lists every file in the tree, slash-separated.
	Paths() ([]string, error)
}

// DirSource is a local checkout.
type DirSource struct{ Dir string }

// ReadFile implements Source.
func (d DirSource) ReadFile(path string) ([]byte, error) {
	return os.ReadFile(filepath.Join(d.Dir, filepath.FromSlash(path)))
}

// Paths implements Source. .git and dist are skipped.
func (d DirSource) Paths() ([]string, error) {
	var out []string
	err := filepath.WalkDir(d.Dir, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(d.Dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if e.IsDir() {
			if rel == ".git" || rel == "dist" || rel == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		out = append(out, rel)
		return nil
	})
	return out, err
}

// Group is a dependabot gomod group.
type Group struct {
	Name     string   `yaml:"name"`
	Comment  string   `yaml:"comment"`
	Patterns []string `yaml:"patterns"`
}

// Facts are the derived, non-override values a template reads: what the
// repository is and contains, never what someone chose for it.
type Facts struct {
	Repo             string
	Module           string
	DockerGolangName string
	YamllintExclude  string
	DependabotGroups []Group
	Knobs            Knobs
	HasDockerfile    bool
	HasCharts        bool
	HasControllerGen bool
}

var (
	chartRE    = regexp.MustCompile(`^charts/[^/]+/Chart\.yaml$`)
	fromGolang = regexp.MustCompile(`(?im)^FROM\s+(?:--\S+\s+)*(\S*golang)[:@]`)
)

// GatherFacts derives a repository's facts from its tree.
func GatherFacts(repo string, src Source, groups []Group) (Facts, error) {
	f := Facts{Repo: repo}
	gomod, err := src.ReadFile("go.mod")
	if err != nil {
		return f, fmt.Errorf("%s: go.mod: %w", repo, err)
	}
	f.Module = ModulePath(gomod)
	if f.Module == "" {
		return f, fmt.Errorf("%s: go.mod has no module line", repo)
	}
	paths, err := src.Paths()
	if err != nil {
		return f, err
	}
	for _, p := range paths {
		switch {
		case p == "Dockerfile":
			f.HasDockerfile = true
		case chartRE.MatchString(p):
			f.HasCharts = true
		case strings.HasPrefix(p, "config/crd/"):
			f.HasControllerGen = true
		}
	}
	if f.DockerGolangName, err = dockerGolangName(src, f.HasDockerfile); err != nil {
		return f, err
	}
	f.YamllintExclude = yamllintExclude(f.HasCharts, f.HasControllerGen)
	reqs := DirectRequires(gomod)
	for _, g := range groups {
		if anyMatch(g.Patterns, reqs) {
			f.DependabotGroups = append(f.DependabotGroups, g)
		}
	}
	return f, nil
}

// dockerGolangName is the name dependabot gives the Dockerfile's golang base
// image: docker.io/library/golang and a mirror such as
// public.ecr.aws/docker/library/golang surface as docker/library/golang.
func dockerGolangName(src Source, hasDockerfile bool) (string, error) {
	if !hasDockerfile {
		return golangImage, nil
	}
	df, err := src.ReadFile("Dockerfile")
	if err != nil {
		return "", err
	}
	if m := fromGolang.FindSubmatch(df); len(m) == 2 && strings.Contains(string(m[1]), "/") {
		return "docker/library/golang", nil
	}
	return golangImage, nil
}

// golangImage is the official Go image's name on Docker Hub.
const golangImage = "golang"

// yamllintExclude keeps yamllint off YAML nobody writes by hand: Helm
// templates (Go templates until rendered) and controller-gen's config/.
func yamllintExclude(charts, controllerGen bool) string {
	switch {
	case charts && controllerGen:
		return `^(charts/[^/]+/templates/|config/)`
	case charts:
		return `^charts/[^/]+/templates/`
	case controllerGen:
		return `^config/`
	}
	return ""
}

// LoadGroups reads baseline/dependabot-groups.yml.
func LoadGroups(root string) ([]Group, error) {
	data, err := readBaseline(root, "dependabot-groups.yml")
	if err != nil {
		return nil, err
	}
	var doc struct {
		Groups []Group `yaml:"groups"`
	}
	if uerr := yaml.Unmarshal(data, &doc); uerr != nil {
		return nil, uerr
	}
	if len(doc.Groups) == 0 {
		return nil, errors.New("dependabot-groups.yml: no groups")
	}
	return doc.Groups, nil
}

// ModulePath returns go.mod's module path.
func ModulePath(gomod []byte) string {
	for line := range strings.Lines(string(gomod)) {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.Trim(strings.TrimSpace(rest), `"`)
		}
	}
	return ""
}

// GoDirective returns go.mod's `go` directive.
func GoDirective(gomod []byte) string {
	for line := range strings.Lines(string(gomod)) {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "go "); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

// ToolVersionsGolang returns .tool-versions' golang pin.
func ToolVersionsGolang(tv []byte) string {
	for line := range strings.Lines(string(tv)) {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "golang" {
			return fields[1]
		}
	}
	return ""
}

// DirectRequires lists the modules go.mod requires without `// indirect`.
func DirectRequires(gomod []byte) []string {
	var out []string
	in := false
	sc := bufio.NewScanner(bytes.NewReader(gomod))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "require (":
			in = true
			continue
		case in && line == ")":
			in = false
			continue
		case strings.HasPrefix(line, "require "):
			line = strings.TrimPrefix(line, "require ")
		case !in:
			continue
		}
		if strings.Contains(line, "// indirect") {
			continue
		}
		if fields := strings.Fields(line); len(fields) >= 2 {
			out = append(out, fields[0])
		}
	}
	return out
}

func anyMatch(patterns, mods []string) bool {
	for _, p := range patterns {
		re := regexp.MustCompile("^" + strings.ReplaceAll(regexp.QuoteMeta(p), `\*`, ".*") + "$")
		for _, m := range mods {
			if re.MatchString(m) {
				return true
			}
		}
	}
	return false
}
