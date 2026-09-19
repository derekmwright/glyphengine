package main

import (
	"fmt"
	"math"
	"sort"
)

// surfaceMergeEpsilon is how close two ray hits at the same XZ column have
// to be, in world units, to count as the same surface rather than two.
//
// It exists for the classic rasteriser bug: a sample that lands exactly on
// an edge or vertex shared by several triangles is "inside" every one of
// them under an edge-inclusive point-in-triangle test (see
// barycentricHeight's tolerance), and each one reports its own
// barycentric-interpolated height. On a watertight mesh those heights are
// the same point in space and should differ only by float64 rounding from
// composing the node's World matrix and interpolating -- comfortably under
// 1e-4 for meshes at the metre scale this engine's world units assume
// (docs/agents/blender-pipeline.md's "Units" section). Two hits farther
// apart than this are a real overhang: a second, physically different
// surface at that column, not rounding noise on the first one.
const surfaceMergeEpsilon = 1e-4

// barycentricHeight returns the height of triangle t's surface at world
// position (x, z), and whether (x, z) is over the triangle at all.
//
// This is a 2D (XZ) point-in-triangle test with the height read off the
// same barycentric weights, not a 3D ray-triangle intersection: a heightmap
// samples along a vertical ray, so the ray direction is fixed and every
// triangle whose XZ projection contains the sample contributes exactly one
// height, however steep it is. A triangle with zero XZ area -- a vertical
// wall -- cannot be a "top" surface for any column and is correctly skipped
// rather than divided-by-zero.
//
// edgeEps admits a sample exactly on an edge or vertex as inside, rather
// than requiring it strictly inside: without it, floating point pushes a
// sample sitting precisely on a shared edge outside BOTH triangles that
// share it about as often as it pushes it inside both, and "outside both"
// is exactly the false hole the diagonal-sample test in raster_test.go
// exists to catch.
//
// baryEdgeEpsRel is a fraction of the COORDINATE MAGNITUDE involved, not a
// fixed number, because the actual source of the rounding this tolerance
// absorbs is float32: every vertex position comes off a glTF POSITION
// accessor as float32, and node.World is an mgl32.Mat4 (also float32, the
// same precision the rest of this engine uses throughout) -- so the
// absolute error in a transformed coordinate scales with the coordinate's
// own magnitude, not with the triangle's size. A tolerance sized for a
// triangle near the origin is too tight for the identical triangle a few
// hundred units away.
//
// Measured, not assumed (cmd/heightmapconv/testdata/blender_terrain.glb,
// whose TerrainParent sits at world X~190-200 specifically to expose this):
// a FIXED baryEdgeEps of 1e-7 -- which passed every earlier synthetic test
// in this package, all built near the origin -- left 17 of that fixture's
// 121 grid samples reading as holes, each one a sample sitting exactly on a
// shared vertex/edge of the real mesh that the tolerance was too tight to
// admit at that magnitude. Scaling by 1e-6 * max(1, largest |coordinate|
// involved) -- about 2e-4 at this fixture's ~200-unit magnitude -- brought
// holes to 0 without loosening anything for a mesh built near the origin,
// where the scale factor floors at 1 and the tolerance is unchanged from
// the fixed 1e-7 this replaced turned out to be too tight even for.
const baryEdgeEpsRel = 1e-6

func barycentricHeight(t triangle, x, z float64) (y float64, ok bool) {
	x0, z0 := t.v0[0], t.v0[2]
	x1, z1 := t.v1[0], t.v1[2]
	x2, z2 := t.v2[0], t.v2[2]

	d := (z1-z2)*(x0-x2) + (x2-x1)*(z0-z2)
	if math.Abs(d) < 1e-12 {
		return 0, false
	}
	a := ((z1-z2)*(x-x2) + (x2-x1)*(z-z2)) / d
	b := ((z2-z0)*(x-x2) + (x0-x2)*(z-z2)) / d
	c := 1 - a - b

	eps := baryEdgeEpsRel * edgeToleranceScale(x0, z0, x1, z1, x2, z2, x, z)
	if a < -eps || b < -eps || c < -eps {
		return 0, false
	}
	return a*t.v0[1] + b*t.v1[1] + c*t.v2[1], true
}

// edgeToleranceScale is the largest coordinate magnitude among the
// triangle's three XZ vertices and the sample point, floored at 1 so a mesh
// built near the origin keeps the same tight tolerance a fixed constant
// would have given it.
func edgeToleranceScale(coords ...float64) float64 {
	scale := 1.0
	for _, c := range coords {
		if a := math.Abs(c); a > scale {
			scale = a
		}
	}
	return scale
}

// dedupeHeights sorts and merges hits within surfaceMergeEpsilon of their
// neighbour, returning one height per real surface, highest first.
func dedupeHeights(ys []float64) []float64 {
	if len(ys) == 0 {
		return nil
	}
	sort.Float64s(ys)
	groups := []float64{ys[0]}
	for _, y := range ys[1:] {
		if y-groups[len(groups)-1] > surfaceMergeEpsilon {
			groups = append(groups, y)
		}
	}
	for i, j := 0, len(groups)-1; i < j; i, j = i+1, j-1 {
		groups[i], groups[j] = groups[j], groups[i]
	}
	return groups
}

// triIndex bins triangles into a uniform grid over their combined XZ extent
// so a sample only tests the triangles that could plausibly cover its
// column, instead of every triangle in the mesh.
//
// Why binning rather than a kd-tree/BVH: the input is a heightmap-shaped
// mesh -- triangles of roughly similar size spread roughly evenly over the
// XZ plane -- which is exactly the case a uniform grid is good at and a
// tree's extra construction cost buys nothing for. See
// TestRasterizePerformanceOnLargeMesh for the number this produces on
// 100,000 triangles.
type triIndex struct {
	tris         []triangle
	minX, minZ   float64
	cellW, cellH float64
	cols, rows   int
	cells        [][]int32
}

// newTriIndex picks a cell count so the average cell holds a small, roughly
// constant number of triangles regardless of mesh size: cols*rows scales
// with len(tris), so lookup cost per sample stays roughly O(1) rather than
// O(triangles) as the mesh grows.
func newTriIndex(tris []triangle) *triIndex {
	minX, minZ := tris[0].minX, tris[0].minZ
	maxX, maxZ := tris[0].maxX, tris[0].maxZ
	for _, t := range tris[1:] {
		minX = math.Min(minX, t.minX)
		minZ = math.Min(minZ, t.minZ)
		maxX = math.Max(maxX, t.maxX)
		maxZ = math.Max(maxZ, t.maxZ)
	}
	w, h := maxX-minX, maxZ-minZ
	if w <= 0 {
		w = 1
	}
	if h <= 0 {
		h = 1
	}

	// Roughly 2 triangles per cell on average -- see the performance test
	// for the number this produces; a smaller target buys nothing once
	// bounding-box overlap already puts most triangles in a handful of
	// cells, and a larger one lets candidate lists grow with mesh size.
	const targetTrisPerCell = 2.0
	target := math.Max(1, float64(len(tris))/targetTrisPerCell)
	side := math.Sqrt(target)
	cols := int(math.Ceil(side * math.Sqrt(w/h)))
	rows := int(math.Ceil(side * math.Sqrt(h/w)))
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}

	idx := &triIndex{
		tris:  tris,
		minX:  minX,
		minZ:  minZ,
		cellW: w / float64(cols),
		cellH: h / float64(rows),
		cols:  cols,
		rows:  rows,
		cells: make([][]int32, cols*rows),
	}
	for ti, t := range tris {
		c0, r0 := idx.cellCoord(t.minX, t.minZ)
		c1, r1 := idx.cellCoord(t.maxX, t.maxZ)
		for r := r0; r <= r1; r++ {
			for c := c0; c <= c1; c++ {
				ci := r*idx.cols + c
				idx.cells[ci] = append(idx.cells[ci], int32(ti))
			}
		}
	}
	return idx
}

func (idx *triIndex) cellCoord(x, z float64) (c, r int) {
	c = int((x - idx.minX) / idx.cellW)
	r = int((z - idx.minZ) / idx.cellH)
	if c < 0 {
		c = 0
	}
	if c >= idx.cols {
		c = idx.cols - 1
	}
	if r < 0 {
		r = 0
	}
	if r >= idx.rows {
		r = idx.rows - 1
	}
	return
}

// sample returns every distinct surface height at world column (x, z),
// highest first. A candidate triangle whose bounding box overlaps the cell
// but does not actually cover (x, z) is filtered by barycentricHeight, not
// here -- the cell only narrows which triangles are worth the exact test.
func (idx *triIndex) sample(x, z float64) []float64 {
	c, r := idx.cellCoord(x, z)
	var raw []float64
	for _, ti := range idx.cells[r*idx.cols+c] {
		if y, ok := barycentricHeight(idx.tris[ti], x, z); ok {
			raw = append(raw, y)
		}
	}
	return dedupeHeights(raw)
}

// coord names one grid cell for the hole/overhang reports below: its grid
// index and the world position it samples.
type coord struct {
	ix, iz int
	x, z   float64
}

// rasterResult is rasterizeMesh's full output: the heights (row-major,
// matching Heightmap.Heights), and the holes and overhangs it found before
// any -fill was applied.
type rasterResult struct {
	heights          []float32
	holes, overhangs []coord
	overhangCounts   []int   // parallel to overhangs: how many surfaces that column had
	elapsed          float64 // seconds; reported by -mesh's caller, not asserted on
}

// rasterizeMesh samples tris on a gridW x gridH grid over the world-space
// rectangle (minX,minZ)-(maxX,maxZ), taking the highest surface at each
// column. A hole (zero surfaces) gets height 0 in the returned slice --
// main.go's caller must apply -fill or refuse to write the file, since 0 is
// not a real height, only a placeholder the caller has to notice via holes.
func rasterizeMesh(tris []triangle, gridW, gridH int, minX, minZ, maxX, maxZ float64) rasterResult {
	idx := newTriIndex(tris)
	stepX := (maxX - minX) / float64(gridW-1)
	stepZ := (maxZ - minZ) / float64(gridH-1)

	res := rasterResult{heights: make([]float32, gridW*gridH)}
	for iz := 0; iz < gridH; iz++ {
		z := minZ + float64(iz)*stepZ
		for ix := 0; ix < gridW; ix++ {
			x := minX + float64(ix)*stepX
			surfaces := idx.sample(x, z)
			i := iz*gridW + ix
			switch len(surfaces) {
			case 0:
				res.holes = append(res.holes, coord{ix, iz, x, z})
			case 1:
				res.heights[i] = float32(surfaces[0])
			default:
				res.heights[i] = float32(surfaces[0])
				res.overhangs = append(res.overhangs, coord{ix, iz, x, z})
				res.overhangCounts = append(res.overhangCounts, len(surfaces))
			}
		}
	}
	return res
}

// fillMode is how -fill resolves a hole. The zero value (kind == "") means
// "no fill was requested" -- rasterizeMesh's caller treats any hole as fatal
// in that case rather than guessing.
type fillMode struct {
	kind  string // "nearest", "min", or "value"
	value float32
}

func parseFillMode(s string) (fillMode, error) {
	switch {
	case s == "nearest":
		return fillMode{kind: "nearest"}, nil
	case s == "min":
		return fillMode{kind: "min"}, nil
	case len(s) > 6 && s[:6] == "value:":
		var v float32
		if _, err := fmt.Sscanf(s[6:], "%g", &v); err != nil {
			return fillMode{}, fmt.Errorf("-fill value:%s: not a number", s[6:])
		}
		return fillMode{kind: "value", value: v}, nil
	default:
		return fillMode{}, fmt.Errorf("-fill %q: want nearest, min, or value:N", s)
	}
}

// applyFill fills every hole in heights (row-major gridW x gridH, with holes
// giving their grid coordinates) according to mode, mutating heights in
// place.
//
// "nearest" is a multi-source breadth-first fill from every non-hole cell at
// once, which finds each hole's nearest (by grid steps, a good enough proxy
// for world distance since HeightAt's own grid is regular) filled neighbour
// in a single O(gridW*gridH) pass rather than a per-hole search that
// degrades on a heightmap with a large hole region.
func applyFill(heights []float32, gridW, gridH int, holes []coord, mode fillMode) {
	if len(holes) == 0 {
		return
	}
	switch mode.kind {
	case "value":
		for _, h := range holes {
			heights[h.iz*gridW+h.ix] = mode.value
		}
	case "min":
		min := float32(math.Inf(1))
		isHole := make(map[int]bool, len(holes))
		for _, h := range holes {
			isHole[h.iz*gridW+h.ix] = true
		}
		for i, v := range heights {
			if !isHole[i] && v < min {
				min = v
			}
		}
		if math.IsInf(float64(min), 1) {
			min = 0 // every cell is a hole; nothing to take a minimum of
		}
		for _, h := range holes {
			heights[h.iz*gridW+h.ix] = min
		}
	case "nearest":
		fillNearest(heights, gridW, gridH, holes)
	}
}

func fillNearest(heights []float32, gridW, gridH int, holes []coord) {
	type cell struct{ ix, iz int }
	isHole := make(map[cell]bool, len(holes))
	for _, h := range holes {
		isHole[cell{h.ix, h.iz}] = true
	}

	queue := make([]cell, 0, gridW*gridH-len(holes))
	visited := make([]bool, gridW*gridH)
	for iz := 0; iz < gridH; iz++ {
		for ix := 0; ix < gridW; ix++ {
			if !isHole[cell{ix, iz}] {
				i := iz*gridW + ix
				visited[i] = true
				queue = append(queue, cell{ix, iz})
			}
		}
	}

	dirs := [4]cell{{1, 0}, {-1, 0}, {0, 1}, {0, -1}}
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		v := heights[c.iz*gridW+c.ix]
		for _, d := range dirs {
			nx, nz := c.ix+d.ix, c.iz+d.iz
			if nx < 0 || nx >= gridW || nz < 0 || nz >= gridH {
				continue
			}
			ni := nz*gridW + nx
			if visited[ni] {
				continue
			}
			visited[ni] = true
			heights[ni] = v
			queue = append(queue, cell{nx, nz})
		}
	}
}
