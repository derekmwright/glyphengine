package glyphengine

import (
	"math"

	"github.com/go-gl/mathgl/mgl32"
)

// Vec2 is a 2D position on the XZ plane.
type Vec2 struct {
	X, Z float32
}

// NavGrid is a walkability grid derived from the heightmap and scene colliders.
// Cells are indexed [z*Width + x].
type NavGrid struct {
	Width, Height int
	CellSize      float32
	OriginX       float32
	OriginZ       float32
	Walkable      []bool
}

// BuildNavGrid creates a NavGrid from a heightmap and scene collider AABBs.
// slopeThreshold is the minimum Y-component of the surface normal for a cell
// to be walkable (0.707 ~ 45 degrees). Cells overlapping any collider AABB are blocked.
func BuildNavGrid(hm *Heightmap, colliders []AABB, hulls []HullColliderInfo, cellSize float32, slopeThreshold float32) *NavGrid {
	w := int(math.Ceil(float64(hm.WorldW / cellSize)))
	h := int(math.Ceil(float64(hm.WorldD / cellSize)))
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}

	grid := &NavGrid{
		Width:    w,
		Height:   h,
		CellSize: cellSize,
		OriginX:  hm.OriginX,
		OriginZ:  hm.OriginZ,
		Walkable: make([]bool, w*h),
	}

	for gz := 0; gz < h; gz++ {
		for gx := 0; gx < w; gx++ {
			wx := hm.OriginX + (float32(gx)+0.5)*cellSize
			wz := hm.OriginZ + (float32(gz)+0.5)*cellSize

			// Check heightmap bounds.
			_, inBounds := hm.HeightAt(wx, wz)
			if !inBounds {
				continue
			}

			// Check slope.
			n := hm.NormalAt(wx, wz)
			if n[1] < slopeThreshold {
				continue
			}

			// Check collider overlap: expand cell to AABB and test all colliders.
			blocked := false
			cellMin := AABB{
				Min: mgl32.Vec3{wx - cellSize*0.5, -1e6, wz - cellSize*0.5},
				Max: mgl32.Vec3{wx + cellSize*0.5, 1e6, wz + cellSize*0.5},
			}
			for _, col := range colliders {
				if cellMin.OverlapsXZ(col) {
					blocked = true
					break
				}
			}
			if !blocked {
				for _, h := range hulls {
					if HullFootprintOverlapsCell(h.Hull, h.Transform,
						wx-cellSize*0.5, wz-cellSize*0.5, wx+cellSize*0.5, wz+cellSize*0.5) {
						blocked = true
						break
					}
				}
			}
			if blocked {
				continue
			}

			grid.Walkable[gz*w+gx] = true
		}
	}

	return grid
}

// OverlapsXZ tests if two AABBs overlap on the XZ plane only (ignoring Y).
func (a AABB) OverlapsXZ(b AABB) bool {
	return a.Min.X() < b.Max.X() && a.Max.X() > b.Min.X() &&
		a.Min.Z() < b.Max.Z() && a.Max.Z() > b.Min.Z()
}

// IsWalkable returns whether grid cell (gx, gz) is walkable.
func (g *NavGrid) IsWalkable(gx, gz int) bool {
	if gx < 0 || gz < 0 || gx >= g.Width || gz >= g.Height {
		return false
	}
	return g.Walkable[gz*g.Width+gx]
}

// WorldToGrid converts world XZ coordinates to grid cell indices.
func (g *NavGrid) WorldToGrid(x, z float32) (int, int) {
	gx := int((x - g.OriginX) / g.CellSize)
	gz := int((z - g.OriginZ) / g.CellSize)
	return gx, gz
}

// GridToWorld converts grid cell indices to world XZ center coordinates.
func (g *NavGrid) GridToWorld(gx, gz int) (float32, float32) {
	x := g.OriginX + (float32(gx)+0.5)*g.CellSize
	z := g.OriginZ + (float32(gz)+0.5)*g.CellSize
	return x, z
}

// astarNode is used internally by A* search.
type astarNode struct {
	x, z   int
	g, f   float32
	parent int // index into nodes slice, -1 = start
}

// FindPath finds a path from (fx, fz) to (tx, tz) using A* with 8-connected neighbors.
// maxNodes caps the number of nodes expanded to prevent runaway searches.
// Returns nil if no path exists or the budget is exceeded.
func (g *NavGrid) FindPath(fx, fz, tx, tz float32, maxNodes int) []Vec2 {
	sx, sz := g.WorldToGrid(fx, fz)
	ex, ez := g.WorldToGrid(tx, tz)

	// Clamp start/end to grid bounds.
	sx = clampInt(sx, 0, g.Width-1)
	sz = clampInt(sz, 0, g.Height-1)
	ex = clampInt(ex, 0, g.Width-1)
	ez = clampInt(ez, 0, g.Height-1)

	if !g.IsWalkable(sx, sz) || !g.IsWalkable(ex, ez) {
		return nil
	}

	if sx == ex && sz == ez {
		wx, wz := g.GridToWorld(ex, ez)
		return []Vec2{{wx, wz}}
	}

	// Node storage.
	nodes := make([]astarNode, 0, 256)
	// Closed set: grid cell key → node index.
	closed := make(map[int32]int, 256)

	// Open set as binary min-heap.
	var open astarHeap

	startKey := int32(sz)*int32(g.Width) + int32(sx)
	h0 := astarHeuristic(sx, sz, ex, ez) * g.CellSize
	nodes = append(nodes, astarNode{x: sx, z: sz, g: 0, f: h0, parent: -1})
	closed[startKey] = 0
	open.push(astarHeapItem{key: startKey, f: h0, nodeIdx: 0})

	expanded := 0

	// 8-connected neighbor offsets.
	type dir struct {
		dx, dz int
		cost   float32
	}
	sqrt2 := float32(math.Sqrt2)
	dirs := [8]dir{
		{1, 0, 1}, {-1, 0, 1}, {0, 1, 1}, {0, -1, 1},
		{1, 1, sqrt2}, {1, -1, sqrt2}, {-1, 1, sqrt2}, {-1, -1, sqrt2},
	}

	endKey := int32(ez)*int32(g.Width) + int32(ex)

	for len(open) > 0 {
		cur := open.pop()
		curNodeIdx := cur.nodeIdx
		cn := nodes[curNodeIdx]

		if cur.key == endKey {
			return g.reconstructPath(nodes, curNodeIdx)
		}

		expanded++
		if expanded >= maxNodes {
			return nil
		}

		for _, d := range dirs {
			nx := cn.x + d.dx
			nz := cn.z + d.dz
			if !g.IsWalkable(nx, nz) {
				continue
			}
			// Diagonal: require both orthogonal neighbors to be walkable (no corner-cutting).
			if d.dx != 0 && d.dz != 0 {
				if !g.IsWalkable(cn.x+d.dx, cn.z) || !g.IsWalkable(cn.x, cn.z+d.dz) {
					continue
				}
			}

			ng := cn.g + d.cost*g.CellSize
			nKey := int32(nz)*int32(g.Width) + int32(nx)

			if existingIdx, found := closed[nKey]; found {
				if ng >= nodes[existingIdx].g {
					continue
				}
				// Found a shorter path to this node.
				nodes[existingIdx].g = ng
				nodes[existingIdx].f = ng + astarHeuristic(nx, nz, ex, ez)*g.CellSize
				nodes[existingIdx].parent = curNodeIdx
				open.push(astarHeapItem{key: nKey, f: nodes[existingIdx].f, nodeIdx: existingIdx})
			} else {
				h := astarHeuristic(nx, nz, ex, ez) * g.CellSize
				nn := astarNode{x: nx, z: nz, g: ng, f: ng + h, parent: curNodeIdx}
				nodeIdx := len(nodes)
				nodes = append(nodes, nn)
				closed[nKey] = nodeIdx
				open.push(astarHeapItem{key: nKey, f: ng + h, nodeIdx: nodeIdx})
			}
		}
	}

	return nil // no path
}

func (g *NavGrid) reconstructPath(nodes []astarNode, endIdx int) []Vec2 {
	length := 0
	for i := endIdx; i >= 0; i = nodes[i].parent {
		length++
	}
	path := make([]Vec2, length)
	j := length - 1
	for i := endIdx; i >= 0; i = nodes[i].parent {
		x, z := g.GridToWorld(nodes[i].x, nodes[i].z)
		path[j] = Vec2{x, z}
		j--
	}
	return path
}

// SmoothPath removes redundant intermediate waypoints using line-of-sight checks.
func (g *NavGrid) SmoothPath(path []Vec2) []Vec2 {
	if len(path) <= 2 {
		return path
	}

	smoothed := []Vec2{path[0]}
	current := 0

	for current < len(path)-1 {
		farthest := current + 1
		for next := current + 2; next < len(path); next++ {
			if g.lineOfSight(path[current], path[next]) {
				farthest = next
			}
		}
		smoothed = append(smoothed, path[farthest])
		current = farthest
	}

	return smoothed
}

// lineOfSight checks if a straight line between two world-space points
// passes only through walkable cells using Bresenham's line algorithm.
func (g *NavGrid) lineOfSight(a, b Vec2) bool {
	ax, az := g.WorldToGrid(a.X, a.Z)
	bx, bz := g.WorldToGrid(b.X, b.Z)

	dx := navAbs(bx - ax)
	dz := navAbs(bz - az)
	sx := 1
	if ax > bx {
		sx = -1
	}
	sz := 1
	if az > bz {
		sz = -1
	}

	err := dx - dz
	x, z := ax, az

	for {
		if !g.IsWalkable(x, z) {
			return false
		}
		if x == bx && z == bz {
			return true
		}
		e2 := err * 2
		if e2 > -dz {
			err -= dz
			x += sx
		}
		if e2 < dx {
			err += dx
			z += sz
		}
	}
}

// WalkableCount returns the number of walkable cells (for logging).
func (g *NavGrid) WalkableCount() int {
	c := 0
	for _, w := range g.Walkable {
		if w {
			c++
		}
	}
	return c
}

func astarHeuristic(ax, az, bx, bz int) float32 {
	dx := float64(ax - bx)
	dz := float64(az - bz)
	return float32(math.Sqrt(dx*dx + dz*dz))
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func navAbs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// --- Binary min-heap for A* open set ---

type astarHeapItem struct {
	key     int32 // grid cell key
	f       float32
	nodeIdx int
}

// Values avoid one escaping object per queued node. Keep container/heap's
// strict-less comparisons and left-child tie choice: ties affect the route
// and even whether a search reaches its goal within the node budget.
// BenchmarkFindPath, 64x64 grids, Ryzen 5900X, three interleaved 500ms runs:
// open grid 324 -> 14 allocations; alternating wall gaps 5710 -> 36.
// At a 20000-node budget, open-grid time was 26.4-27.2 -> 17.6-18.9 us;
// the detour was 1.424-1.438 -> 1.154-1.175 ms. These are search costs, not FPS.
// The route fingerprint in pathfinding_alloc_test.go pins the original output.
type astarHeap []astarHeapItem

func (h *astarHeap) push(item astarHeapItem) {
	*h = append(*h, item)
	for j := len(*h) - 1; j > 0; {
		i := (j - 1) / 2
		if !((*h)[j].f < (*h)[i].f) {
			break
		}
		(*h)[i], (*h)[j] = (*h)[j], (*h)[i]
		j = i
	}
}

func (h *astarHeap) pop() astarHeapItem {
	a := *h
	n := len(a) - 1
	item := a[0]
	a[0] = a[n]
	a = a[:n]
	for i := 0; ; {
		left := 2*i + 1
		if left >= n || left < 0 {
			break
		}
		j := left
		if right := left + 1; right < n && a[right].f < a[left].f {
			j = right
		}
		if !(a[j].f < a[i].f) {
			break
		}
		a[i], a[j] = a[j], a[i]
		i = j
	}
	*h = a
	return item
}
