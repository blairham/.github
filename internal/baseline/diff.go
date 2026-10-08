// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

package baseline

import (
	"bytes"
	"fmt"
	"strings"
)

// UnifiedDiff returns a unified diff from a to b with three lines of context,
// or "" when they are equal. The files here are a few hundred lines, so a
// plain LCS table is fast enough and easy to trust.
func UnifiedDiff(aName, bName string, a, b []byte) string {
	if bytes.Equal(a, b) {
		return ""
	}
	al, bl := splitLines(a), splitLines(b)
	ops := lcsOps(al, bl)

	const ctx = 3
	var sb strings.Builder
	fmt.Fprintf(&sb, "--- %s\n+++ %s\n", aName, bName)
	// Group ops into hunks: runs of changes with up to 2*ctx equal lines
	// between them belong to one hunk.
	for i := 0; i < len(ops); {
		if ops[i].kind == ' ' {
			i++
			continue
		}
		start := max(i-ctx, 0)
		end := i
		for end < len(ops) {
			if ops[end].kind != ' ' {
				end++
				continue
			}
			run := end
			for run < len(ops) && ops[run].kind == ' ' {
				run++
			}
			if run == len(ops) || run-end > 2*ctx {
				end = min(end+ctx, len(ops))
				break
			}
			end = run
		}
		writeHunk(&sb, ops[start:end])
		i = end
	}
	return sb.String()
}

type op struct {
	line string
	kind byte // ' ', '-', '+'
	ai   int  // 1-based line in a; for '+', the a line it follows
	bi   int  // 1-based line in b; for '-', the b line it follows
}

func writeHunk(sb *strings.Builder, ops []op) {
	// An empty side starts at the line before the hunk, as diff(1) writes it.
	aStart, bStart, aLen, bLen := ops[0].ai, ops[0].bi, 0, 0
	for _, o := range ops {
		if o.kind != '+' {
			if aLen == 0 {
				aStart = o.ai
			}
			aLen++
		}
		if o.kind != '-' {
			if bLen == 0 {
				bStart = o.bi
			}
			bLen++
		}
	}
	fmt.Fprintf(sb, "@@ -%d,%d +%d,%d @@\n", aStart, aLen, bStart, bLen)
	for _, o := range ops {
		sb.WriteByte(o.kind)
		sb.WriteString(o.line)
		sb.WriteByte('\n')
	}
}

func lcsOps(a, b []string) []op {
	n, m := len(a), len(b)
	// dp[i][j] is the LCS length of a[i:] and b[j:].
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else {
				dp[i][j] = max(dp[i+1][j], dp[i][j+1])
			}
		}
	}
	ops := make([]op, 0, n+m)
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && a[i] == b[j]:
			ops = append(ops, op{kind: ' ', line: a[i], ai: i + 1, bi: j + 1})
			i++
			j++
		// Deletions first, as diff(1) orders a replaced line.
		case i < n && (j == m || dp[i+1][j] >= dp[i][j+1]):
			ops = append(ops, op{kind: '-', line: a[i], ai: i + 1, bi: j})
			i++
		default:
			ops = append(ops, op{kind: '+', line: b[j], ai: i, bi: j + 1})
			j++
		}
	}
	return ops
}

func splitLines(b []byte) []string {
	s := string(b)
	if s == "" {
		return nil
	}
	lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	if !strings.HasSuffix(s, "\n") {
		lines[len(lines)-1] += " (no newline at end of file)"
	}
	return lines
}

// ChangedLines counts the lines a diff adds or removes. Only the lines
// before the first hunk are headers: a removed line that itself starts with
// "-- " is written "--- …" inside a hunk, and is still a change.
func ChangedLines(d string) int {
	n := 0
	inHunk := false
	for line := range strings.Lines(d) {
		switch {
		case strings.HasPrefix(line, "@@ "):
			inHunk = true
		case inHunk && (line[0] == '+' || line[0] == '-'):
			n++
		}
	}
	return n
}
