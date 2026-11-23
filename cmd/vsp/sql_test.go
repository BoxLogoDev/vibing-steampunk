package main

import (
	"strings"
	"testing"
)

func TestFoldSQL(t *testing.T) {
	cols := strings.Repeat("col_name_x, ", 40) // ~480 chars on one line
	lit := "'" + strings.Repeat("a b ", 10) + "'" // literal with spaces inside
	sql := "SELECT " + cols + "z FROM t WHERE f = " + lit + " AND g = 'x''y z'"
	out := foldSQL(sql, 200)

	for _, line := range strings.Split(out, "\n") {
		if len(line) > 255 {
			t.Fatalf("line over 255: %d", len(line))
		}
	}
	// folding only replaces spaces with newlines, and never inside literals
	if strings.ReplaceAll(out, "\n", " ") != sql {
		t.Fatal("folding changed more than whitespace")
	}
	if !strings.Contains(out, lit) || !strings.Contains(out, "'x''y z'") {
		t.Fatal("a string literal was split")
	}
	if !strings.Contains(out, "\n") {
		t.Fatal("long line was not folded")
	}
}
