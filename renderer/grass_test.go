package renderer

import (
	"math"
	"testing"
)

// TestBuildGrassTiles verifies that tile bucketing produces contiguous,
// complete instance ranges with bounding spheres that contain their instances.
func TestBuildGrassTiles(t *testing.T) {
	mesh := &Mesh{BoundRadius: 2.0}

	// Scatter instances across a 3x3-tile area with varied heights.
	var instances []GrassInstance
	for i := 0; i < 500; i++ {
		x := float32(i%40) * 1.2 // 0..46.8 → spans 3 tiles at size 16
		z := float32(i/40) * 3.5 // 0..43.75 → spans 3 tiles
		y := float32(i%7) * 0.5
		instances = append(instances, GrassInstance{X: x, Y: y, Z: z, Rotation: float32(i)})
	}

	ordered, tiles := buildGrassTiles(instances, mesh, 0, 0)

	if len(ordered) != len(instances) {
		t.Fatalf("instance count changed: got %d, want %d", len(ordered), len(instances))
	}
	if len(tiles) == 0 {
		t.Fatal("no tiles produced")
	}

	// Ranges must be contiguous, non-overlapping, and cover all instances.
	next := 0
	total := 0
	for ti, tile := range tiles {
		if tile.FirstInstance != next {
			t.Errorf("tile %d: FirstInstance=%d, want %d (ranges must be contiguous)", ti, tile.FirstInstance, next)
		}
		if tile.Count <= 0 {
			t.Errorf("tile %d: empty tile should not be emitted", ti)
		}
		next = tile.FirstInstance + tile.Count
		total += tile.Count

		// Every instance in the range must be inside the tile's bounding sphere.
		for i := tile.FirstInstance; i < tile.FirstInstance+tile.Count; i++ {
			inst := ordered[i]
			dx := float64(inst.X - tile.Center[0])
			dy := float64(inst.Y - tile.Center[1])
			dz := float64(inst.Z - tile.Center[2])
			if dist := math.Sqrt(dx*dx + dy*dy + dz*dz); dist > float64(tile.Radius)+1e-4 {
				t.Errorf("tile %d: instance %d outside bounding sphere (dist %.3f > radius %.3f)", ti, i, dist, tile.Radius)
			}
		}
	}
	if total != len(instances) {
		t.Errorf("tile counts sum to %d, want %d", total, len(instances))
	}

	// Every original instance must appear exactly once in the reordered slice.
	seen := make(map[float32]bool, len(instances))
	for _, inst := range ordered {
		if seen[inst.Rotation] {
			t.Fatalf("duplicate instance (rotation key %v)", inst.Rotation)
		}
		seen[inst.Rotation] = true
	}

	// Determinism: same input must produce identical output.
	ordered2, tiles2 := buildGrassTiles(instances, mesh, 0, 0)
	for i := range ordered {
		if ordered[i] != ordered2[i] {
			t.Fatalf("non-deterministic instance order at %d", i)
		}
	}
	for i := range tiles {
		if tiles[i] != tiles2[i] {
			t.Fatalf("non-deterministic tile at %d", i)
		}
	}
}
