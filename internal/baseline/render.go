// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

package baseline

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"strings"
	"text/template"

	"go.yaml.in/yaml/v3"
)

// File is one synced file: where it lands in a repository, and the baseline
// template it is rendered from.
type File struct {
	Path     string
	Template string
}

// Files is every file the baseline owns, in report order.
var Files = []File{
	{Path: ".golangci.yml", Template: "golangci.yml.tmpl"},
	{Path: ".editorconfig", Template: "editorconfig.tmpl"},
	{Path: ".pre-commit-config.yaml", Template: "pre-commit-config.yaml.tmpl"},
	{Path: ".yamllint.yml", Template: "yamllint.yml.tmpl"},
	{Path: ".gitleaks.toml", Template: "gitleaks.toml.tmpl"},
	{Path: ".github/dependabot.yml", Template: "dependabot.yml.tmpl"},
	{Path: ".github/CODEOWNERS", Template: "CODEOWNERS.tmpl"},
	{Path: ".github/workflows/scorecard.yml", Template: "scorecard.yml.tmpl"},
	{Path: ".github/workflows/codeql.yml", Template: "codeql.yml.tmpl"},
}

// readBaseline reads a file under root's baseline/ directory.
func readBaseline(root, name string) ([]byte, error) {
	return fs.ReadFile(os.DirFS(root), "baseline/"+name)
}

// Render renders every baseline file for one repository. root is the
// blairham/.github checkout holding baseline/.
func Render(root string, f *Facts) (map[string][]byte, error) {
	if err := checkKnobsAgainstFacts(root, f); err != nil {
		return nil, err
	}
	out := make(map[string][]byte, len(Files))
	govetSeen := map[string]bool{}
	fm := template.FuncMap{
		"comment":        comment,
		"yamlIndent":     yamlIndent,
		"misspellIgnore": misspellIgnore,
		// govet lists the baseline's analyzers minus the disabled ones,
		// recording which it was asked about.
		"govet": func(all ...string) []string {
			var on []string
			for _, a := range all {
				govetSeen[a] = true
				if !slices.Contains(f.Knobs.GolangciGovetDisable, a) {
					on = append(on, a)
				}
			}
			return on
		},
	}
	for _, file := range Files {
		b, err := renderOne(root, file, f, fm)
		if err != nil {
			return nil, err
		}
		out[file.Path] = b
	}
	for _, a := range f.Knobs.GolangciGovetDisable {
		if !govetSeen[a] {
			return nil, fmt.Errorf("golangci.govet-disable: %q is not a baseline govet analyzer", a)
		}
	}
	return out, nil
}

func renderOne(root string, file File, f *Facts, fm template.FuncMap) ([]byte, error) {
	raw, err := readBaseline(root, file.Template)
	if err != nil {
		return nil, err
	}
	tmpl, err := template.New(file.Template).
		Delims("[%", "%]").
		Option("missingkey=error").
		Funcs(fm).
		Parse(string(raw))
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if execErr := tmpl.Execute(&buf, f); execErr != nil {
		return nil, fmt.Errorf("%s: %w", file.Template, execErr)
	}
	b := buf.Bytes()
	if file.Path != ".golangci.yml" {
		return b, nil
	}
	b, err = dropPending(b, "linters:", f.Repo, f.Knobs.GolangciLintersPending)
	if err != nil {
		return nil, err
	}
	return dropPending(b, "formatters:", f.Repo, f.Knobs.GolangciFormattersPending)
}

// checkKnobsAgainstFacts refuses knobs that would render to nothing for this
// repository: an override that has no effect is one nobody notices is stale.
func checkKnobsAgainstFacts(root string, f *Facts) error {
	if len(f.Knobs.DependabotDockerIgnore) > 0 && !f.HasDockerfile {
		return fmt.Errorf("dependabot.docker-ignore: %s has no Dockerfile, so no docker ecosystem", f.Repo)
	}
	base, err := Linters(root)
	if err != nil {
		return err
	}
	for _, x := range f.Knobs.GolangciLintersExtra {
		if slices.Contains(base, x.Name) {
			return fmt.Errorf("golangci.linters-extra: %s is already a baseline linter", x.Name)
		}
	}
	return nil
}

// Linters reads the linters the baseline enables, in order, from the
// template itself — there is no second copy of the list to fall out of step.
func Linters(root string) ([]string, error) {
	raw, err := readBaseline(root, "golangci.yml.tmpl")
	if err != nil {
		return nil, err
	}
	var out []string
	walkEnable(strings.SplitAfter(string(raw), "\n"), "linters:", func(name string) bool {
		if name != "" {
			out = append(out, name)
		}
		return true
	})
	return out, nil
}

// walkEnable calls visit for every line, with the name when the line is an
// entry of the top-level section's `enable:` list (section is "linters:" or
// "formatters:") and "" when it is not. A line is kept when visit returns
// true.
func walkEnable(lines []string, section string, visit func(name string) bool) []string {
	var kept []string
	current, inEnable := "", false
	for _, line := range lines {
		trimmed := strings.TrimRight(line, "\n")
		if trimmed != "" && trimmed[0] != ' ' && trimmed[0] != '#' && trimmed[0] != '[' {
			current = trimmed
		}
		name := ""
		switch {
		case current == section && trimmed == "  enable:":
			inEnable = true
		case inEnable && trimmed != "" && !strings.HasPrefix(trimmed, "    "):
			inEnable = false
		case inEnable:
			if m := linterLine.FindStringSubmatch(trimmed); len(m) == 2 {
				name = m[1]
			}
		}
		if visit(name) {
			kept = append(kept, line)
		}
	}
	return kept
}

// dropPending removes the pending names from a section's enable list, and
// says so in a comment, failing if any of them was not there to remove: an
// override that removed nothing would be one that silently stopped working.
func dropPending(b []byte, section, repo string, pending []string) ([]byte, error) {
	if len(pending) == 0 {
		return b, nil
	}
	what := strings.TrimSuffix(section, ":")
	removed := map[string]bool{}
	kept := walkEnable(strings.SplitAfter(string(b), "\n"), section, func(name string) bool {
		if name != "" && slices.Contains(pending, name) {
			removed[name] = true
			return false
		}
		return true
	})
	for _, p := range pending {
		if !removed[p] {
			return nil, fmt.Errorf(".golangci.yml: pending %s entry %q is not in the baseline's %s.enable", what, p, what)
		}
	}
	note := []string{
		"    # Not yet enabled here — overrides/" + repo + ".yml says why. Adopt one\n",
		"    # per pull request and shrink that list:\n",
	}
	for _, p := range pending {
		note = append(note, "    #   "+p+"\n")
	}
	i := slices.Index(kept, section+"\n")
	if i < 0 {
		return nil, fmt.Errorf(".golangci.yml: no %s section", what)
	}
	j := slices.Index(kept[i:], "  enable:\n")
	if j < 0 {
		return nil, fmt.Errorf(".golangci.yml: no %s.enable list", what)
	}
	at := i + j + 1
	out := slices.Concat(kept[:at], note, kept[at:])
	return []byte(strings.Join(out, "")), nil
}

var linterLine = regexp.MustCompile(`^ {4}- ([a-z0-9]+)(?:\s+#.*)?$`)

// yamlIndent renders a YAML node indented by n spaces, without a trailing
// newline.
func yamlIndent(n int, node yaml.Node) (string, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&node); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	pad := strings.Repeat(" ", n)
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = pad + l
		}
	}
	return strings.Join(lines, "\n"), nil
}

// misspellIgnore is the misspell hook's -i list: the baseline's own
// linterAlias (a linter's name, in every .golangci.yml) and the repository's.
func misspellIgnore(extra []string) string {
	return strings.Join(append([]string{linterAlias}, extra...), ",")
}

// linterAlias is the import-alias linter's name. golangci-lint's misspell, run
// with --fix by the commit hook, has twice rewritten this literal to
// "imports"; TestMisspellIgnore catches it if it happens again.
const linterAlias = "importas" //nolint:misspell // a linter's name, not a typo

// comment word-wraps text into YAML/TOML comment lines indented by indent
// spaces, at most 79 columns wide, without a trailing newline.
func comment(indent int, text string) string {
	prefix := strings.Repeat(" ", indent) + "# "
	var lines []string
	line := ""
	for _, w := range strings.Fields(text) {
		switch {
		case line == "":
			line = w
		case len(prefix)+len(line)+1+len(w) > 79:
			lines = append(lines, prefix+line)
			line = w
		default:
			line += " " + w
		}
	}
	if line != "" {
		lines = append(lines, prefix+line)
	}
	return strings.Join(lines, "\n")
}
