// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

package baseline

import (
	"strings"
	"testing"
)

// FuzzUnifiedDiff checks what the drift report relies on: equal inputs never
// diff, unequal ones always do, and the diff's - and + lines are exactly the
// lines that leave a and arrive in b (so a reader can trust the counts).
func FuzzUnifiedDiff(f *testing.F) {
	f.Add("a\nb\nc\n", "a\nx\nc\n")
	f.Add("", "x\n")
	f.Add("1\n2\n3\n4\n5\n6\n7\n8\n9\n", "1\n2\n3\n4\n5\n6\n7\n8\n9\nnew\n")
	f.Add("no newline", "no newline\n")
	f.Fuzz(func(t *testing.T, a, b string) {
		d := UnifiedDiff("a", "b", []byte(a), []byte(b))
		if (d == "") != (a == b) {
			t.Fatalf("a==b is %v but diff is %q", a == b, d)
		}
		if d == "" {
			return
		}
		// The two header lines come first; after them a line's first byte is
		// its kind, whatever the rest of it looks like.
		removed, added := 0, 0
		for i, line := range strings.Split(strings.TrimSuffix(d, "\n"), "\n") {
			switch {
			case i < 2, strings.HasPrefix(line, "@@ "):
			case line[0] == '-':
				removed++
			case line[0] == '+':
				added++
			}
		}
		al, bl := splitLines([]byte(a)), splitLines([]byte(b))
		// Every a line is removed or kept, every b line added or kept, and
		// the kept lines are common to both: |a| - removed == |b| - added.
		if len(al)-removed != len(bl)-added {
			t.Fatalf("|a|=%d removed=%d |b|=%d added=%d\n%s", len(al), removed, len(bl), added, d)
		}
		if ChangedLines(d) != removed+added {
			t.Fatalf("ChangedLines=%d, want %d\n%s", ChangedLines(d), removed+added, d)
		}
	})
}
