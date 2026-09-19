package glyphengine

import (
	"encoding/binary"
	"fmt"
	"io"
	"io/fs"
	"math"
)

// maxHeightmapGridDim bounds GridW/GridH against a corrupt or truncated
// header trying to make LoadHeightmap allocate gigabytes. Nothing in the file
// format proves the header is legitimate before the heights are read, so a
// file that lost its first bytes (or was handed a different format entirely)
// can carry a GridW/GridH near 0xFFFFFFFF; multiplying two such values before
// this check existed overflowed int on the read path and made make([]float32,
// count) either panic with "makeslice: len out of range" or, for a count that
// happened to stay positive, try to allocate tens of gigabytes and thrash the
// allocator for minutes before failing. 16384 per side is a 1 GiB grid at 4
// bytes/height -- far past any heightmap this engine has shipped (256x256 is
// the largest example) and still a hard ceiling on a corrupt file's damage.
const maxHeightmapGridDim = 16384

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

// LoadHeightmap reads a .heightmap binary file from fsys.
//
// Format: gridW(u32) gridH(u32) worldW(f32) worldD(f32) originX(f32)
// originZ(f32) then gridW*gridH little-endian f32 heights.
func LoadHeightmap(fsys fs.FS, name string) (*Heightmap, error) {
	f, err := fsys.Open(name)
	if err != nil {
		return nil, fmt.Errorf("open heightmap %q: %w", name, err)
	}
	defer f.Close()

	var header struct {
		GridW, GridH     uint32
		WorldW, WorldD   float32
		OriginX, OriginZ float32
	}
	if err := binary.Read(f, binary.LittleEndian, &header); err != nil {
		return nil, fmt.Errorf("read header of %q: %w", name, err)
	}

	// Same floor NewHeightmap enforces on a procedurally built grid: a 0x1 or
	// 1x1 grid has no cell for HeightAt/TerrainMesh to interpolate across and
	// GridW-1 or GridH-1 becomes zero, which is a division a caller down the
	// line performs (Heightmap.NormalAt's eps, TerrainMesh's stepX/stepZ).
	// LoadHeightmap used to skip this check entirely -- a file with a zeroed
	// or single-row header read back as a Heightmap that panicked or divided
	// by zero the first time anything touched it, nowhere near this function.
	if header.GridW < 2 || header.GridH < 2 {
		return nil, fmt.Errorf("heightmap %q: grid must be at least 2x2, got %dx%d", name, header.GridW, header.GridH)
	}
	if header.GridW > maxHeightmapGridDim || header.GridH > maxHeightmapGridDim {
		return nil, fmt.Errorf("heightmap %q: grid %dx%d exceeds the %dx%d sanity limit -- this is almost certainly a truncated or corrupt file, not a legitimate heightmap",
			name, header.GridW, header.GridH, maxHeightmapGridDim, maxHeightmapGridDim)
	}
	// Same rule NewHeightmap enforces: HeightAt divides world position by
	// WorldW/WorldD to find the grid cell, so zero, negative, NaN or Inf here
	// used to load "successfully" and then hand back NaN/Inf from every
	// HeightAt call -- wrong ground silently, not a load-time error.
	if !validWorldSize(header.WorldW) || !validWorldSize(header.WorldD) {
		return nil, fmt.Errorf("heightmap %q: world size must be positive and finite, got %gx%g", name, header.WorldW, header.WorldD)
	}

	count := int(header.GridW) * int(header.GridH)
	heights := make([]float32, count)
	if err := binary.Read(f, binary.LittleEndian, heights); err != nil {
		return nil, fmt.Errorf("read %d heights of %q: %w", count, name, err)
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

// validWorldSize reports whether a world-space dimension is usable as a
// divisor: positive and finite. Shared by LoadHeightmap's header validation
// and WriteTo's pre-write check so a heightmap that would fail one way in
// also fails the other way out.
func validWorldSize(v float32) bool {
	return v > 0 && !math.IsInf(float64(v), 0) && !math.IsNaN(float64(v))
}

// WriteTo writes h in the format LoadHeightmap reads: gridW(u32) gridH(u32)
// worldW(f32) worldD(f32) originX(f32) originZ(f32), then gridW*gridH
// little-endian f32 heights in row-major order ([z*gridW+x]). It is
// LoadHeightmap run backwards, so a tool or a game-side generator that wants
// to persist a Heightmap has exactly one encoder to share rather than each
// reimplementing this layout.
//
// This is (*Heightmap).WriteTo rather than a path-taking SaveHeightmap:
// LoadHeightmap takes an fs.FS because the engine has no business choosing
// where a file lives, and the same reasoning applies on the way out --
// io.WriterTo lets a caller hand it an *os.File, a bytes.Buffer, or anything
// else that implements io.Writer.
func (h *Heightmap) WriteTo(w io.Writer) (int64, error) {
	if h.GridW < 0 || h.GridH < 0 {
		return 0, fmt.Errorf("heightmap: write: negative grid dimensions %dx%d", h.GridW, h.GridH)
	}
	if h.GridW > maxHeightmapGridDim || h.GridH > maxHeightmapGridDim {
		return 0, fmt.Errorf("heightmap: write: grid %dx%d exceeds the %dx%d limit LoadHeightmap would accept back",
			h.GridW, h.GridH, maxHeightmapGridDim, maxHeightmapGridDim)
	}
	if want := h.GridW * h.GridH; len(h.Heights) != want {
		return 0, fmt.Errorf("heightmap: write: %d heights, want %d for a %dx%d grid", len(h.Heights), want, h.GridW, h.GridH)
	}
	if !validWorldSize(h.WorldW) || !validWorldSize(h.WorldD) {
		return 0, fmt.Errorf("heightmap: write: world size must be positive and finite, got %gx%g", h.WorldW, h.WorldD)
	}

	header := struct {
		GridW, GridH     uint32
		WorldW, WorldD   float32
		OriginX, OriginZ float32
	}{
		GridW: uint32(h.GridW), GridH: uint32(h.GridH),
		WorldW: h.WorldW, WorldD: h.WorldD,
		OriginX: h.OriginX, OriginZ: h.OriginZ,
	}

	cw := &countingWriter{w: w}
	if err := binary.Write(cw, binary.LittleEndian, header); err != nil {
		return cw.n, fmt.Errorf("heightmap: write header: %w", err)
	}
	if err := binary.Write(cw, binary.LittleEndian, h.Heights); err != nil {
		return cw.n, fmt.Errorf("heightmap: write heights: %w", err)
	}
	return cw.n, nil
}

// countingWriter tracks bytes written so WriteTo can report an accurate n
// (io.WriterTo's contract) even though binary.Write does not return one.
type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
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
