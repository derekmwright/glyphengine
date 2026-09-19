package main

import "testing"

// TestBlenderFixturePeakAndRidgeLandWhereHandComputationSays is the "for
// real" half of the orientation proof: testdata/blender_terrain.glb (built
// by tools/blender/build_terrain_fixture.py, real Blender 5.0.1, see
// testdata/README.md) is an asymmetric terrain -- a single-vertex spike at
// one corner, a ridge along one axis only -- with its mesh object
// translated AND its parent Empty rotated, converted here and checked
// against world coordinates derived independently by hand (testdata/README.md's
// "Hand-derived Blender -> engine world-space formula" section) rather than
// against this package's own output.
//
// A symmetric hill could not catch an axis swap: it looks the same either
// way. This mesh cannot pass by accident -- see testdata/README.md's table
// for what every wrong axis assignment or dropped rotation would move the
// peak or ridge to instead.
//
// Found by this specific fixture, not predicted by the hand derivation:
// with a fixed (non-magnitude-scaled) baryEdgeEps, this test's rasterised
// grid came back with 17 of its 121 samples reading as holes -- every one a
// sample sitting exactly on a shared vertex/edge of the real mesh, at this
// fixture's ~200-unit world-space magnitude, where a tolerance sized for a
// mesh near the origin (every earlier synthetic test in this package) was
// too tight. See raster.go's baryEdgeEpsRel doc for the fix and the
// numbers; this test is what caught it.
func TestBlenderFixturePeakAndRidgeLandWhereHandComputationSays(t *testing.T) {
	tris, nodeName, err := loadMeshTriangles("testdata/blender_terrain.glb", "")
	if err != nil {
		t.Fatalf("loadMeshTriangles: %v", err)
	}
	if nodeName != "Terrain" {
		t.Errorf("chose node %q, want Terrain", nodeName)
	}

	minX, minZ, maxX, maxZ := meshBounds(tris)
	// testdata/README.md's table: X in [190,200], Z in [-10,0].
	const wantMinX, wantMinZ, wantMaxX, wantMaxZ float64 = 190, -10, 200, 0
	const boundsTol = 1e-3 // the real export's float32 vertices land a few 1e-6 off the nominal integers
	if abs64(minX-wantMinX) > boundsTol || abs64(minZ-wantMinZ) > boundsTol ||
		abs64(maxX-wantMaxX) > boundsTol || abs64(maxZ-wantMaxZ) > boundsTol {
		t.Fatalf("mesh bounds = %g,%g..%g,%g, want ~%g,%g..%g,%g",
			minX, minZ, maxX, maxZ, wantMinX, wantMinZ, wantMaxX, wantMaxZ)
	}

	// 11x11 so each grid sample lands exactly on one of the mesh's own 11x11
	// vertices -- no interpolation ambiguity between hand computation and
	// rasterised output.
	res := rasterizeMesh(tris, 11, 11, minX, minZ, maxX, maxZ)
	if len(res.holes) != 0 {
		t.Fatalf("%d hole(s) in a fully-covered mesh: %v", len(res.holes), res.holes)
	}
	if len(res.overhangs) != 0 {
		t.Fatalf("%d overhang(s) in a single-surface mesh: %v", len(res.overhangs), res.overhangs)
	}

	// world_x = 200 - vy, and the grid's ix runs 0..10 over minX=190..maxX=200
	// in steps of 1, so ix = worldX - 190 = 10 - vy (ix DECREASES as vy
	// increases -- vy=0, the peak's row, lands at the FAR column ix=10).
	// Symmetrically world_z = -vx gives iz = 10 - vx.
	at := func(vx, vy int) float32 { return res.heights[(10-vx)*11+(10-vy)] }

	const tol = 1e-3
	check := func(name string, vx, vy int, want float32) {
		t.Helper()
		got := at(vx, vy)
		if diff := got - want; diff > tol || diff < -tol {
			t.Errorf("%s (vx=%d,vy=%d): got %g, want %g", name, vx, vy, got, want)
		}
	}

	// The peak: (vx=10, vy=0), Blender height 20 -> world_y 23.
	check("peak", 10, 0, 23)
	// The ridge crest at three different vx (proving it runs the whole
	// length of the mesh, not just at one column): Blender height 4 -> world_y 7.
	check("ridge crest at vx=0", 0, 8, 7)
	check("ridge crest at vx=5", 5, 8, 7)
	check("ridge crest at vx=10", 10, 8, 7)
	// Ridge shoulders, tapering: height 2 -> world_y 5.
	check("ridge shoulder vy=6", 3, 6, 5)
	check("ridge shoulder vy=10", 3, 10, 5)
	// Flat elsewhere (vy <= 5, outside the ridge's iy>=6 range): Blender
	// height 0 -> world_y 3, checked at three points away from both
	// features. vy=10 is NOT flat -- it is the ridge's own far shoulder
	// (height 2 there, not 0) -- so this deliberately avoids the Y=10 edge
	// the two checks above already cover from the ridge side.
	check("flat corner vx=0,vy=0", 0, 0, 3)
	check("flat corner vx=10,vy=5", 10, 5, 3)
	check("flat edge vx=0,vy=5", 0, 5, 3)
}

func abs64(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
