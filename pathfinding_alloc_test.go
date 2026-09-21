package glyphengine

import (
	"fmt"
	"hash/fnv"
	"testing"
)

// Alternating openings force the search to explore beyond the straight line.
func allocationNavGrid(detour bool) *NavGrid {
	const size = 64
	g := &NavGrid{Width: size, Height: size, CellSize: 1, Walkable: make([]bool, size*size)}
	for i := range g.Walkable {
		g.Walkable[i] = true
	}
	if detour {
		for x := 12; x < size-1; x += 12 {
			for z := 0; z < size; z++ {
				if (x/12%2 == 0 && z < 8) || (x/12%2 != 0 && z >= size-8) {
					continue
				}
				g.Walkable[z*size+x] = false
			}
		}
	}
	return g
}

func BenchmarkFindPath(b *testing.B) {
	for _, detour := range []bool{false, true} {
		b.Run(fmt.Sprintf("detour=%v", detour), func(b *testing.B) {
			g := allocationNavGrid(detour)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if len(g.FindPath(1.5, 1.5, 62.5, 62.5, 20000)) < 2 {
					b.Fatal("no path")
				}
			}
		})
	}
}

// Capture on the original container/heap implementation before optimizing it.
// Includes budget exhaustion and ties, pinning the exact route, not just length.
// Verified to fail when equal-priority children choose the right child instead
// of the left: 47327d58df8369bb becomes 2f9254a42947d560.
func TestFindPathRouteFingerprint(t *testing.T) {
	h := fnv.New64a()
	for _, detour := range []bool{false, true} {
		g := allocationNavGrid(detour)
		for _, budget := range []int{1, 30, 200, 2000, 20000} {
			for _, dest := range [][2]float32{{62.5, 62.5}, {48.5, 17.5}, {1.5, 62.5}} {
				p := g.FindPath(1.5, 1.5, dest[0], dest[1], budget)
				fmt.Fprintf(h, "%v;", p)
			}
		}
	}
	const want = uint64(0x47327d58df8369bb)
	if h.Sum64() != want {
		t.Fatalf("route fingerprint: %016x, want %016x", h.Sum64(), want)
	}
}

// This ceiling allows slice/map growth but rejects per-node allocations.
// Restoring the pointer heap fails this check: 324 (open) and 5710 (detour)
// allocations per search, versus 14 and 36 with the value heap.
func TestFindPathAllocationBudget(t *testing.T) {
	for _, detour := range []bool{false, true} {
		g := allocationNavGrid(detour)
		if n := testing.AllocsPerRun(20, func() {
			if len(g.FindPath(1.5, 1.5, 62.5, 62.5, 20000)) < 2 {
				t.Fatal("no path")
			}
		}); n > 64 {
			t.Errorf("detour=%v: %v allocations, want at most 64", detour, n)
		}
	}
}
