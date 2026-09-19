package main

import (
	"math"
	"testing"
	"time"
)

// TestBarycentricHeightOnSharedEdgesAndVertices is the classic rasteriser
// bug the issue calls out by name: a flat quad split into two triangles
// along its diagonal, sampled exactly on that diagonal and at its four
// corners, must report exactly one surface everywhere -- never zero (a
// false hole, from a sample landing just outside both triangles) and never
// two (a false overhang, from counting the same flat surface twice where
// both triangles claim the point).
//
// Quad corners at XZ (0,0),(2,0),(2,2),(0,2) with y=x (0 at x=0, 2 at x=2,
// regardless of z) -- a simple slope, not flat, so a bug that picked the
// wrong triangle would also show up as the wrong height, not just the wrong
// hit count. Vertices are [3]float64{x,y,z}: A=(0,0,0), B=(2,2,0),
// C=(2,2,2), D=(0,0,2), split into {A,C,D} and {A,B,C} -- the diagonal from
// (0,0) to (2,2) in XZ.
func TestBarycentricHeightOnSharedEdgesAndVertices(t *testing.T) {
	a := [3]float64{0, 0, 0}
	b := [3]float64{2, 2, 0}
	c := [3]float64{2, 2, 2}
	d := [3]float64{0, 0, 2}
	t0 := newTriangle(a, c, d)
	t1 := newTriangle(a, b, c)
	tris := []triangle{t0, t1}
	idx := newTriIndex(tris)

	cases := []struct {
		name  string
		x, z  float64
		wantY float64
	}{
		{"corner (0,0)", 0, 0, 0},
		{"corner (2,0)", 2, 0, 2},
		{"corner (0,2)", 0, 2, 0},
		{"corner (2,2)", 2, 2, 2},
		{"diagonal midpoint", 1, 1, 1},
		{"diagonal near (0,0)", 0.25, 0.25, 0.25},
		{"diagonal near (2,2)", 1.75, 1.75, 1.75},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			surfaces := idx.sample(c.x, c.z)
			if len(surfaces) != 1 {
				t.Fatalf("sample(%g,%g) found %d surfaces %v, want exactly 1 (a shared edge/vertex must not read as a hole or an overhang)", c.x, c.z, len(surfaces), surfaces)
			}
			if diff := surfaces[0] - c.wantY; diff > 1e-9 || diff < -1e-9 {
				t.Errorf("sample(%g,%g) = %g, want %g", c.x, c.z, surfaces[0], c.wantY)
			}
		})
	}
}

// TestRasterizeMeshReportsHolesAndOverhangs builds a mesh with three
// columns: one covered by a single flat triangle pair (normal), one with NO
// geometry at all (a hole), and one covered by two triangle pairs at
// different heights (an overhang/bridge) -- and checks that rasterizeMesh
// sorts samples into exactly those three buckets, taking the topmost height
// for the overhang column.
func TestRasterizeMeshReportsHolesAndOverhangs(t *testing.T) {
	var tris []triangle
	// Normal column: a flat quad at x in [0,1], z in [0,1], y=5.
	tris = append(tris,
		newTriangle([3]float64{0, 5, 0}, [3]float64{1, 5, 0}, [3]float64{1, 5, 1}),
		newTriangle([3]float64{0, 5, 0}, [3]float64{1, 5, 1}, [3]float64{0, 5, 1}),
	)
	// Hole column: x in [2,3], z in [0,1] -- deliberately no geometry.

	// Overhang column: x in [4,5], z in [0,1], a floor at y=1 and a
	// ceiling at y=9 -- two physically real surfaces at one column, which
	// is exactly what a heightmap cannot represent and this tool must
	// report rather than silently average or pick the floor.
	tris = append(tris,
		newTriangle([3]float64{4, 1, 0}, [3]float64{5, 1, 0}, [3]float64{5, 1, 1}),
		newTriangle([3]float64{4, 1, 0}, [3]float64{5, 1, 1}, [3]float64{4, 1, 1}),
		newTriangle([3]float64{4, 9, 0}, [3]float64{5, 9, 0}, [3]float64{5, 9, 1}),
		newTriangle([3]float64{4, 9, 0}, [3]float64{5, 9, 1}, [3]float64{4, 9, 1}),
	)

	// Grid samples exactly the centre of each column: x = 0.5, 2.5, 4.5 -> a
	// 3-wide grid over [0.5, 4.5], z fixed at 0.5 (a 1-row grid needs at
	// least 2, so mirror the row).
	res := rasterizeMesh(tris, 3, 2, 0.5, 0, 4.5, 1)

	if len(res.holes) != 2 { // one hole column x 2 rows (z=0 and z=1 samples)
		t.Fatalf("holes = %d, want 2 (the middle column at both z samples): %v", len(res.holes), res.holes)
	}
	if len(res.overhangs) != 2 {
		t.Fatalf("overhangs = %d, want 2 (the third column at both z samples): %v", len(res.overhangs), res.overhangs)
	}
	for _, o := range res.overhangs {
		i := o.iz*3 + o.ix
		if got := res.heights[i]; got != 9 {
			t.Errorf("overhang column height = %g, want 9 (the topmost surface)", got)
		}
	}
	for _, c := range res.overhangCounts {
		if c != 2 {
			t.Errorf("overhang surface count = %d, want 2", c)
		}
	}
	// Column 0 (x=0.5) must be a normal sample at height 5, not a hole and
	// not an overhang.
	for iz := 0; iz < 2; iz++ {
		if got := res.heights[iz*3+0]; got != 5 {
			t.Errorf("normal column height at row %d = %g, want 5", iz, got)
		}
	}
}

// TestApplyFillModes covers all three -fill modes against the same set of
// holes.
func TestApplyFillModes(t *testing.T) {
	// 3x1 grid: valid, hole, valid (heights 10 and 30 either side).
	base := func() []float32 { return []float32{10, 0, 30} }
	holes := []coord{{ix: 1, iz: 0, x: 1, z: 0}}

	t.Run("value", func(t *testing.T) {
		h := base()
		applyFill(h, 3, 1, holes, fillMode{kind: "value", value: 99})
		if h[1] != 99 {
			t.Errorf("value fill = %g, want 99", h[1])
		}
	})
	t.Run("min", func(t *testing.T) {
		h := base()
		applyFill(h, 3, 1, holes, fillMode{kind: "min"})
		if h[1] != 10 {
			t.Errorf("min fill = %g, want 10 (the smaller of the two valid neighbours)", h[1])
		}
	})
	t.Run("nearest", func(t *testing.T) {
		h := base()
		applyFill(h, 3, 1, holes, fillMode{kind: "nearest"})
		// Equidistant from both valid neighbours (grid distance 1 either
		// way) -- BFS visits index 0 before index 2 in row-major scan
		// order, so the left neighbour's value wins deterministically.
		if h[1] != 10 {
			t.Errorf("nearest fill = %g, want 10 (equidistant, left neighbour wins by scan order)", h[1])
		}
	})
}

func TestParseFillMode(t *testing.T) {
	cases := []struct {
		in      string
		wantErr bool
		wantVal float32
	}{
		{"nearest", false, 0},
		{"min", false, 0},
		{"value:12.5", false, 12.5},
		{"value:-3", false, -3},
		{"bogus", true, 0},
		{"value:notanumber", true, 0},
	}
	for _, c := range cases {
		m, err := parseFillMode(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseFillMode(%q) succeeded, want an error", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseFillMode(%q): %v", c.in, err)
			continue
		}
		if m.kind == "value" && m.value != c.wantVal {
			t.Errorf("parseFillMode(%q).value = %g, want %g", c.in, m.value, c.wantVal)
		}
	}
}

// TestRasterizePerformanceOnLargeMesh measures (not asserts a tight bound
// on, since CI hardware varies) rasterizeMesh's wall time on a mesh sized
// the way the issue's performance requirement names: 100,000 triangles onto
// a 512x512 grid. See the final report for the number this produced.
//
// The generous 60s ceiling is a smoke check against catastrophic O(triangles
// * samples) behaviour (an unbinned brute force over this input is
// 100,000 * 262,144 = > 26 billion triangle tests, which does not finish in
// any reasonable time) rather than a performance assertion -- the real
// number is logged, not compared against a target, per AGENTS.md's "measure,
// do not assume".
func TestRasterizePerformanceOnLargeMesh(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	const trisWant = 100_000
	tris, n, w, hgt := gridMesh(trisWant)
	t.Logf("built %d triangles over a %gx%g grid mesh (%dx%d vertices)", len(tris), w, hgt, n, n)

	start := time.Now()
	res := rasterizeMesh(tris, 512, 512, 0, 0, w, hgt)
	elapsed := time.Since(start)
	t.Logf("rasterised %d triangles onto 512x512 in %s", len(tris), elapsed)
	if len(res.holes) != 0 {
		t.Errorf("a flat generated grid mesh has %d holes, want 0", len(res.holes))
	}
	if elapsed.Seconds() > 60 {
		t.Errorf("rasterising took %s, want well under 60s (catastrophic blowup?)", elapsed)
	}
}

// gridMesh builds a flat, slightly undulating vertex grid sized to produce
// at least wantTris triangles (2 per quad cell), for the performance test
// above. Height is a cheap sinusoid so the mesh is not degenerate (all
// triangles coplanar would not exercise anything the binning couldn't also
// get right by accident).
func gridMesh(wantTris int) (tris []triangle, verticesPerSide int, worldW, worldD float64) {
	cellsPerSide := 1
	for cellsPerSide*cellsPerSide*2 < wantTris {
		cellsPerSide++
	}
	n := cellsPerSide + 1
	const cell = 1.0
	worldW, worldD = float64(cellsPerSide)*cell, float64(cellsPerSide)*cell

	height := func(ix, iz int) float64 {
		return 2*math.Sin(float64(ix)*0.3) + 2*math.Sin(float64(iz)*0.2)
	}
	for iz := 0; iz < cellsPerSide; iz++ {
		for ix := 0; ix < cellsPerSide; ix++ {
			x0, x1 := float64(ix)*cell, float64(ix+1)*cell
			z0, z1 := float64(iz)*cell, float64(iz+1)*cell
			y00, y10 := height(ix, iz), height(ix+1, iz)
			y01, y11 := height(ix, iz+1), height(ix+1, iz+1)
			tris = append(tris,
				newTriangle([3]float64{x0, y00, z0}, [3]float64{x1, y10, z0}, [3]float64{x1, y11, z1}),
				newTriangle([3]float64{x0, y00, z0}, [3]float64{x1, y11, z1}, [3]float64{x0, y01, z1}),
			)
		}
	}
	return tris, n, worldW, worldD
}
