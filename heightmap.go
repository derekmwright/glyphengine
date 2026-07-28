package glyphengine

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
)

// Heightmap stores a grid of height values for terrain collision.
// Heights are indexed [z*GridW + x] in row-major order.
type Heightmap struct {
	GridW, GridH     int
	WorldW, WorldD   float32 // total world-space dimensions
	OriginX, OriginZ float32 // world-space origin (min corner)
	Heights          []float32
}

// NewHeightmap wraps an existing grid of heights, which must hold exactly
// gridW*gridH values in row-major order ([z*gridW + x]). The grid covers the
// world-space rectangle from (originX, originZ) to (originX+worldW,
// originZ+worldD).
//
// This is the constructor for procedurally generated terrain; LoadHeightmap
// reads the same structure from a file.
func NewHeightmap(gridW, gridH int, worldW, worldD, originX, originZ float32, heights []float32) (*Heightmap, error) {
	if gridW < 2 || gridH < 2 {
		return nil, fmt.Errorf("heightmap: grid must be at least 2x2, got %dx%d", gridW, gridH)
	}
	if len(heights) != gridW*gridH {
		return nil, fmt.Errorf("heightmap: got %d heights, want %d for a %dx%d grid",
			len(heights), gridW*gridH, gridW, gridH)
	}
	if worldW <= 0 || worldD <= 0 {
		return nil, fmt.Errorf("heightmap: world size must be positive, got %gx%g", worldW, worldD)
	}
	return &Heightmap{
		GridW:   gridW,
		GridH:   gridH,
		WorldW:  worldW,
		WorldD:  worldD,
		OriginX: originX,
		OriginZ: originZ,
		Heights: heights,
	}, nil
}

// Bounds returns the world-space extent covered by the heightmap.
func (h *Heightmap) Bounds() (minX, minZ, maxX, maxZ float32) {
	return h.OriginX, h.OriginZ, h.OriginX + h.WorldW, h.OriginZ + h.WorldD
}

// LoadHeightmap reads a .heightmap binary file.
// Format: gridW(u32) gridH(u32) worldW(f32) worldD(f32) originX(f32) originZ(f32) heights(f32 * gridW * gridH)
func LoadHeightmap(path string) (*Heightmap, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open heightmap: %w", err)
	}
	defer f.Close()

	var header struct {
		GridW, GridH     uint32
		WorldW, WorldD   float32
		OriginX, OriginZ float32
	}
	if err := binary.Read(f, binary.LittleEndian, &header); err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}

	count := int(header.GridW) * int(header.GridH)
	heights := make([]float32, count)
	if err := binary.Read(f, binary.LittleEndian, heights); err != nil {
		return nil, fmt.Errorf("read heights: %w", err)
	}

	return &Heightmap{
		GridW:   int(header.GridW),
		GridH:   int(header.GridH),
		WorldW:  header.WorldW,
		WorldD:  header.WorldD,
		OriginX: header.OriginX,
		OriginZ: header.OriginZ,
		Heights: heights,
	}, nil
}

// HeightAt returns the interpolated terrain height at world position (x, z).
// Points outside the heightmap return -infinity (no ground).
func (h *Heightmap) HeightAt(x, z float32) (float32, bool) {
	// Convert world coords to grid-local [0, gridW-1] range.
	gx := (x - h.OriginX) / h.WorldW * float32(h.GridW-1)
	gz := (z - h.OriginZ) / h.WorldD * float32(h.GridH-1)

	if gx < 0 || gz < 0 || gx >= float32(h.GridW-1) || gz >= float32(h.GridH-1) {
		return 0, false
	}

	// Integer grid cell and fractional offset.
	ix := int(gx)
	iz := int(gz)
	fx := gx - float32(ix)
	fz := gz - float32(iz)

	// Clamp for safety at exact boundary.
	if ix >= h.GridW-1 {
		ix = h.GridW - 2
		fx = 1.0
	}
	if iz >= h.GridH-1 {
		iz = h.GridH - 2
		fz = 1.0
	}

	// Bilinear interpolation of the four surrounding grid points.
	h00 := h.Heights[iz*h.GridW+ix]
	h10 := h.Heights[iz*h.GridW+ix+1]
	h01 := h.Heights[(iz+1)*h.GridW+ix]
	h11 := h.Heights[(iz+1)*h.GridW+ix+1]

	top := h00*(1-fx) + h10*fx
	bot := h01*(1-fx) + h11*fx
	return top*(1-fz) + bot*fz, true
}

// HeightAtRayDown performs a downward raycast against the heightmap.
// Returns the distance along the ray and the hit Y position, or false if no hit.
func (h *Heightmap) HeightAtRayDown(originX, originY, originZ, maxDist float32) (dist float32, hitY float32, ok bool) {
	height, inBounds := h.HeightAt(originX, originZ)
	if !inBounds {
		return 0, 0, false
	}

	// Ray goes downward from originY; hit if terrain is within maxDist above or below.
	d := originY - height
	if d > maxDist {
		return 0, 0, false
	}
	// If player is below terrain (d < 0), report dist=0 so ground snap
	// pushes them back up to the surface.
	if d < 0 {
		d = 0
	}
	return d, height, true
}

// NormalAt computes an approximate surface normal at world position (x, z)
// using central differences.
func (h *Heightmap) NormalAt(x, z float32) [3]float32 {
	eps := h.WorldW / float32(h.GridW-1) * 0.5
	hL, okL := h.HeightAt(x-eps, z)
	hR, okR := h.HeightAt(x+eps, z)
	hD, okD := h.HeightAt(x, z-eps)
	hU, okU := h.HeightAt(x, z+eps)

	if !okL || !okR || !okD || !okU {
		return [3]float32{0, 1, 0}
	}

	nx := hL - hR
	nz := hD - hU
	ny := 2.0 * eps
	length := float32(math.Sqrt(float64(nx*nx + ny*ny + nz*nz)))
	return [3]float32{nx / length, ny / length, nz / length}
}
