package main

import (
	"os"
	"path/filepath"
	"testing"
)

// draws= and drawsset= sit on the same line, and drawsset= is the
// order-INDEPENDENT hash -- the one field that cannot fail the gate this feeds.
// A prefix match would have picked it, and `task determinism` would have
// reported the draw sequence as identical for the rest of time.
func TestFieldMatchesTheWholeKey(t *testing.T) {
	const line = "loop=3 draws=7/abc drawsset=def dynmeshorder=1"

	if got, ok := field(line, "draws"); !ok || got != "7/abc" {
		t.Errorf("draws = %q, %v; want \"7/abc\", true", got, ok)
	}
	if got, ok := field(line, "drawsset"); !ok || got != "def" {
		t.Errorf("drawsset = %q, %v; want \"def\", true", got, ok)
	}
	if _, ok := field(line, "draw"); ok {
		t.Error(`"draw" matched a line that only has "draws" and "drawsset"`)
	}
}

func TestExtractReportsAMissingFieldRatherThanDroppingTheLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.trace")
	body := "loop=1 draws=1/aa outcome=present\n" +
		"loop=2 outcome=skip-minimized\n" +
		"loop=3 draws=1/bb outcome=present\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	lines, found, err := extract(path, "draws")
	if err != nil {
		t.Fatal(err)
	}
	if found != 2 {
		t.Errorf("found %d lines with the field, want 2", found)
	}
	want := []string{"loop=1 draws=1/aa", "loop=2 draws=-", "loop=3 draws=1/bb"}
	if len(lines) != len(want) {
		t.Fatalf("got %d lines, want %d", len(lines), len(want))
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, lines[i], want[i])
		}
	}
}

// A trace that never carried the field is the shape a vacuous gate has: two
// empty extractions compare equal and the gate prints a column of "identical".
func TestExtractFindsNothingWhenTheFieldIsAbsent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.trace")
	if err := os.WriteFile(path, []byte("loop=1 outcome=present\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, found, err := extract(path, "draws")
	if err != nil {
		t.Fatal(err)
	}
	if found != 0 {
		t.Errorf("found = %d, want 0 -- main exits non-zero on this", found)
	}
}
