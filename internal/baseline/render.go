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
	out := make(map[string][]byte, len(Files))
	for _, file := range Files {
		raw, err := readBaseline(root, file.Template)
		if err != nil {
			return nil, err
		}
		tmpl, err := template.New(file.Template).
			Delims("[%", "%]").
			Option("missingkey=error").
			Funcs(funcs).
			Parse(string(raw))
		if err != nil {
			return nil, err
		}
		var buf bytes.Buffer
		if execErr := tmpl.Execute(&buf, f); execErr != nil {
			return nil, fmt.Errorf("%s: %w", file.Template, execErr)
		}
		b := buf.Bytes()
		if file.Path == ".golangci.yml" {
			if b, err = dropPendingLinters(b, f.Repo, f.Knobs.GolangciLintersPending); err != nil {
				return nil, err
			}
		}
		out[file.Path] = b
	}
	return out, nil
}

// Linters reads the linters the baseline enables, in order, from the
// template itself — there is no second copy of the list to fall out of step.
func Linters(root string) ([]string, error) {
	raw, err := readBaseline(root, "golangci.yml.tmpl")
	if err != nil {
		return nil, err
	}
	var out []string
	walkLinterEnable(strings.SplitAfter(string(raw), "\n"), func(name string) bool {
		if name != "" {
			out = append(out, name)
		}
		return true
	})
	return out, nil
}

// walkLinterEnable calls visit for every line, with the linter name when the
// line is an entry of the top-level `linters:` → `enable:` list and "" when
// it is not. A line is kept when visit returns true.
func walkLinterEnable(lines []string, visit func(name string) bool) []string {
	var kept []string
	section, inEnable := "", false
	for _, line := range lines {
		trimmed := strings.TrimRight(line, "\n")
		if trimmed != "" && trimmed[0] != ' ' && trimmed[0] != '#' {
			section = trimmed
		}
		name := ""
		switch {
		case section == "linters:" && trimmed == "  enable:":
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

// dropPendingLinters removes the pending linters from linters.enable, and
// says so in a comment, failing if any of them was not there to remove: an
// override that removed nothing would be one that silently stopped working.
func dropPendingLinters(b []byte, repo string, pending []string) ([]byte, error) {
	if len(pending) == 0 {
		return b, nil
	}
	removed := map[string]bool{}
	kept := walkLinterEnable(strings.SplitAfter(string(b), "\n"), func(name string) bool {
		if name != "" && slices.Contains(pending, name) {
			removed[name] = true
			return false
		}
		return true
	})
	for _, p := range pending {
		if !removed[p] {
			return nil, fmt.Errorf(".golangci.yml: pending linter %q is not in the baseline's linters.enable", p)
		}
	}
	note := []string{
		"    # Not yet enabled here — overrides/" + repo + ".yml says why. Adopt one\n",
		"    # per pull request and shrink that list:\n",
	}
	for _, p := range pending {
		note = append(note, "    #   "+p+"\n")
	}
	// The first `  enable:` is linters' (formatters follow it).
	i := slices.Index(kept, "  enable:\n")
	if i < 0 {
		return nil, fmt.Errorf(".golangci.yml: no linters.enable list")
	}
	out := slices.Concat(kept[:i+1], note, kept[i+1:])
	return []byte(strings.Join(out, "")), nil
}

var linterLine = regexp.MustCompile(`^ {4}- ([a-z0-9]+)(?:\s+#.*)?$`)

var funcs = template.FuncMap{"comment": comment}

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
