package renderer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A disabled trace is a nil pointer, and every method has to return on it
// without touching anything. The frame loop calls these unconditionally, so if
// one of them allocated -- a string concat, a boxed argument, an escaping
// slice -- every frame of every shipped build would pay for a diagnostic
// nobody asked for.
//
// Verified to fail: giving Str a `...any` signature and formatting its
// argument before the nil check takes this from 0 to 1 allocs/op, and the test
// reports it.
func TestStateTraceDisabledAllocatesNothing(t *testing.T) {
	var tr *StateTrace
	instances := make([]ParticleInstance, 64)

	got := testing.AllocsPerRun(200, func() {
		tr.Begin(7)
		tr.Int("slot", 1)
		tr.Str("outcome", "present")
		tr.Hash("cam", NewHash.Float32(1.5))
		tr.CountHash("particles", len(instances), NewHash)
		tr.End()
	})
	if got != 0 {
		t.Fatalf("a disabled state trace allocated %v times per run; it must be free", got)
	}
	if err := tr.Close(); err != nil {
		t.Fatalf("closing a disabled trace: %v", err)
	}
}

// The hash has to depend on order, or the field that exists to catch a
// reordering cannot catch one. Folding the same values in a different sequence
// must not land on the same value.
func TestHasherDependsOnOrder(t *testing.T) {
	a := NewHash.Float32(1).Float32(2)
	b := NewHash.Float32(2).Float32(1)
	if a == b {
		t.Fatal("the hash is order-independent; a reordered draw list would trace identically")
	}
}

// Every field the trace writes has to survive a round trip through the file,
// and a record has to be one line -- the workflow is `diff a.trace b.trace`,
// and a field that wrapped would make every later line look different.
func TestStateTraceWritesOneLinePerRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.trace")
	tr, err := OpenStateTrace(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	for i := 1; i <= 3; i++ {
		tr.Begin(i)
		tr.Int("rendered", i)
		tr.CountHash("draws", 2, NewHash.Int(i))
		tr.Str("outcome", "present")
		tr.End()
	}
	if err := tr.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3:\n%s", len(lines), b)
	}
	if !strings.HasPrefix(lines[0], "loop=1 ") || !strings.Contains(lines[0], "outcome=present") {
		t.Fatalf("first record is not the shape the diff workflow expects: %q", lines[0])
	}
}

// HashPOD reads a slice's memory image, so it has to see a changed element.
// A version that hashed len() alone would pass every other test here.
func TestHashPODSeesContent(t *testing.T) {
	a := []ParticleInstance{{X: 1}, {Y: 2}}
	b := []ParticleInstance{{X: 1}, {Y: 3}}
	if HashPOD(NewHash, a) == HashPOD(NewHash, b) {
		t.Fatal("HashPOD ignored a changed instance")
	}
	if HashPOD(NewHash, a) != HashPOD(NewHash, []ParticleInstance{{X: 1}, {Y: 2}}) {
		t.Fatal("HashPOD is not a function of the contents alone")
	}
}
