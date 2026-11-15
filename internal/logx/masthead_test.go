package logx

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestMastheadGrid(t *testing.T) {
	for r, g := range glyphs {
		if len(g) != 6 {
			t.Fatalf("%c rows %d", r, len(g))
		}
		for i, row := range g {
			if n := utf8.RuneCountInString(row); n != 7 {
				t.Fatalf("%c row %d width %d %q", r, i, n, row)
			}
		}
	}
	word := wordmark("TRANSARC")
	for i, line := range arch(runeLen(word[0])) {
		if n := utf8.RuneCountInString(line); n != runeLen(word[0]) {
			t.Fatalf("arch row %d width %d, word %d %q", i, n, runeLen(word[0]), line)
		}
	}
	lines := Masthead()
	if len(lines) != 9 {
		t.Fatalf("lines %d", len(lines))
	}
	for i, line := range lines {
		if n := utf8.RuneCountInString(line); n != BoxWidth {
			t.Fatalf("line %d width %d %q", i, n, line)
		}
	}
	if strings.TrimSpace(lines[0]) == "" || !strings.Contains(lines[3], "█") {
		t.Fatalf("masthead\n%s", strings.Join(lines, "\n"))
	}
	t.Logf("\n%s", strings.Join(lines, "\n"))
}
