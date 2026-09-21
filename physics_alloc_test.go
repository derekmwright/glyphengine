package glyphengine

import (
	"testing"

	"github.com/go-gl/mathgl/mgl32"
)

func queryAllocationScene() *Scene {
	s := NewScene()
	for i := 0; i < 64; i++ {
		spawnStaticCollider(s, mgl32.Vec3{float32(i%8)*4 - 14, 0, float32(i/8)*4 - 14}, mgl32.Vec3{1, 1, 1})
	}
	s.UpdateSpatialGrid()
	s.RebuildStatics()
	return s
}

func BenchmarkRaycastGrid(b *testing.B) {
	s := queryAllocationScene()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, ok := s.Raycast(mgl32.Vec3{2, 10, 2}, mgl32.Vec3{0, -1, 0}, 20, 0); !ok {
			b.Fatal("ray missed fixture")
		}
	}
}

func BenchmarkOverlapGridMiss(b *testing.B) {
	s := queryAllocationScene()
	box := AABB{Min: mgl32.Vec3{-1, 8, -1}, Max: mgl32.Vec3{1, 9, 1}}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if len(s.OverlapAABB(box, 0)) != 0 {
			b.Fatal("unexpected overlap")
		}
	}
}

// Both grids contain candidates; the ray hits and the overlap misses only on Y.
// Restoring QueryRadiusAlloc makes both checks fail at 6 allocations/query.
func TestGridQueriesDoNotAllocateCandidates(t *testing.T) {
	s := queryAllocationScene()
	if n := testing.AllocsPerRun(50, func() {
		if _, ok := s.Raycast(mgl32.Vec3{2, 10, 2}, mgl32.Vec3{0, -1, 0}, 20, 0); !ok {
			t.Fatal("ray missed fixture")
		}
	}); n != 0 {
		t.Errorf("raycast: %v allocations, want 0", n)
	}
	box := AABB{Min: mgl32.Vec3{-1, 8, -1}, Max: mgl32.Vec3{1, 9, 1}}
	if n := testing.AllocsPerRun(50, func() {
		if len(s.OverlapAABB(box, 0)) != 0 {
			t.Fatal("unexpected overlap")
		}
	}); n != 0 {
		t.Errorf("overlap miss: %v allocations, want 0", n)
	}
}
