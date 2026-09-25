package glyphengine

import "github.com/derekmwright/glyphengine/ecs"

const defaultCellSize = float32(32)

// cellKey is an integer grid coordinate for spatial hashing.
type cellKey [2]int

// SpatialGrid is a flat-cell spatial hash for 2D (XZ) entity lookups.
// Rebuilt every snapshot tick.
type SpatialGrid struct {
	cellSize float32
	cells    map[cellKey][]ecs.Entity
	queryBuf []ecs.Entity // reusable buffer for QueryRadius results
}

// NewSpatialGrid creates an empty grid with the given cell size.
func NewSpatialGrid(cellSize float32) *SpatialGrid {
	if cellSize <= 0 {
		cellSize = defaultCellSize
	}
	return &SpatialGrid{
		cellSize: cellSize,
		cells:    make(map[cellKey][]ecs.Entity),
	}
}

// cellFor converts world XZ to a grid cell key.
func (g *SpatialGrid) cellFor(x, z float32) cellKey {
	cx := int(x / g.cellSize)
	cz := int(z / g.cellSize)
	if x < 0 {
		cx--
	}
	if z < 0 {
		cz--
	}
	return cellKey{cx, cz}
}

// Update rebuilds the grid from every living entity with a Transform.
func (g *SpatialGrid) Update(w *ecs.World, transforms *ecs.Store[Transform]) {
	// Clear cells — reuse map to avoid reallocation.
	for k := range g.cells {
		g.cells[k] = g.cells[k][:0]
	}

	transforms.Each(func(entity ecs.Entity, t *Transform) {
		if !w.Alive(entity) {
			return
		}
		key := g.cellFor(t.Position.X(), t.Position.Z())
		g.cells[key] = append(g.cells[key], entity)
	})
}

// QueryRadius returns all entities within radius of (x, z) on the XZ plane.
// The returned slice is reused across calls — callers must not retain it.
// NOT safe for concurrent use — use QueryRadiusAlloc for parallel paths.
func (g *SpatialGrid) QueryRadius(x, z, radius float32) []ecs.Entity {
	g.queryBuf = g.queryBuf[:0]
	g.appendCells(&g.queryBuf, x, z, radius)
	return g.queryBuf
}

// QueryRadiusAlloc returns entities within radius, allocating a fresh slice.
// Safe for concurrent use from multiple goroutines.
func (g *SpatialGrid) QueryRadiusAlloc(x, z, radius float32) []ecs.Entity {
	var result []ecs.Entity
	g.appendCells(&result, x, z, radius)
	return result
}

func (g *SpatialGrid) appendCells(dst *[]ecs.Entity, x, z, radius float32) {
	g.eachCell(x, z, radius, func(entities []ecs.Entity) {
		*dst = append(*dst, entities...)
	})
}

// eachInRadius reads the cells directly, without copying candidates into a
// temporary slice. Like QueryRadiusAlloc it has no shared query scratch, so
// concurrent readers are safe while the grid is frozen.
// BenchmarkRaycastGrid / BenchmarkOverlapGridMiss (64 colliders, both grids)
// measure 6 allocations / 1792 bytes per query with copied candidates, 0 here.
func (g *SpatialGrid) eachInRadius(x, z, radius float32, visit func(ecs.Entity)) {
	g.eachCell(x, z, radius, func(entities []ecs.Entity) {
		for _, entity := range entities {
			visit(entity)
		}
	})
}

func (g *SpatialGrid) eachCell(x, z, radius float32, visit func([]ecs.Entity)) {
	minCX, maxCX, minCZ, maxCZ := g.cellRange(x, z, radius)
	for cx := minCX; cx <= maxCX; cx++ {
		for cz := minCZ; cz <= maxCZ; cz++ {
			visit(g.cells[cellKey{cx, cz}])
		}
	}
}

// cellRange returns the inclusive cell bounds a query at (x, z) with the given
// radius covers. It is a function rather than repeated arithmetic because
// walkReaches has to answer for exactly the cells eachCell reads: a rounding
// difference between the two would let the broad-phase dedupe drop an entity
// the other grid's walk never reaches (see Scene.staticWalkProduces).
func (g *SpatialGrid) cellRange(x, z, radius float32) (minCX, maxCX, minCZ, maxCZ int) {
	minCX = int((x - radius) / g.cellSize)
	maxCX = int((x + radius) / g.cellSize)
	minCZ = int((z - radius) / g.cellSize)
	maxCZ = int((z + radius) / g.cellSize)

	if x-radius < 0 {
		minCX--
	}
	if z-radius < 0 {
		minCZ--
	}
	return minCX, maxCX, minCZ, maxCZ
}

// walkReaches reports whether eachInRadius over (x, z, radius) reads the given
// cell. Cells, not distance: what decides whether a walk hands an entity over
// is the cell the entity sits in, and two grids with different cell sizes
// cover different ground for the same query.
func (g *SpatialGrid) walkReaches(key cellKey, x, z, radius float32) bool {
	minCX, maxCX, minCZ, maxCZ := g.cellRange(x, z, radius)
	return key[0] >= minCX && key[0] <= maxCX && key[1] >= minCZ && key[1] <= maxCZ
}

// Clear resets the grid without reallocating the map.
func (g *SpatialGrid) Clear() {
	for k := range g.cells {
		g.cells[k] = g.cells[k][:0]
	}
}

// Insert adds an entity to the grid at the given world XZ position.
func (g *SpatialGrid) Insert(entity ecs.Entity, x, z float32) {
	key := g.cellFor(x, z)
	g.cells[key] = append(g.cells[key], entity)
}

// HasAnyInRadius returns true if any cell within radius of (x,z) has entities.
// This is a coarse check (cell-level, not per-entity distance) suitable for
// cheap proximity heuristics.
func (g *SpatialGrid) HasAnyInRadius(x, z, radius float32) bool {
	minCX, maxCX, minCZ, maxCZ := g.cellRange(x, z, radius)
	for cx := minCX; cx <= maxCX; cx++ {
		for cz := minCZ; cz <= maxCZ; cz++ {
			if len(g.cells[cellKey{cx, cz}]) > 0 {
				return true
			}
		}
	}
	return false
}
