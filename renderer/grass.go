package renderer

import (
	"encoding/binary"
	"log"
	"math"
	"math/rand"
	"sort"
	"unsafe"

	"github.com/vkngwrapper/core/v3/core1_0"
)

// Grass thinning: how much of a tile's instances are drawn, by distance.
//
// The flora are real meshes rather than cards -- about 340 triangles each -- so a
// field is millions of triangles, and past a certain distance most of them are
// smaller than a pixel. A sub-pixel triangle is the worst thing a rasterizer can
// be given: it still shades a full 2x2 quad, so four fragments of work land on
// something that covers less than one.
//
// Blades already shrink and dissolve over the same range (see grass.vert), so
// removing some of them there costs far less than it saves. Full density is kept
// close to the camera where it is actually legible.
//
// The numbers live in GrassLOD now; see grasslod.go.

// GrassMaxDistance is the default hard cull distance for grass blades.
//
// Deprecated as a tuning knob: it is DefaultGrassLOD().MaxDistance, and the
// live value is whatever SetGrassLOD was given. Kept because it is the right
// figure for sizing a world -- an island much smaller than this has grass
// culled before its far edge.
const GrassMaxDistance = 80.0

// grassBladeScale is the mesh scale applied in grass.vert (grassScale).
const grassBladeScale = 0.35

// grassTileSize is the world-space edge length of one culling tile.
const grassTileSize = 16.0

// GrassInstance is the per-instance data uploaded to the GPU (16 bytes).
// Matches the shader's binding 1, location 4: vec4(x, y, z, rotation).
type GrassInstance struct {
	X, Y, Z  float32
	Rotation float32
}

// variantTiles is one variant's share of the tiles drawn as impostors, so the
// billboard pass can run once for all variants rather than interleaving with
// the mesh draws and rebinding the pipeline each time.
//
// It holds a range rather than a slice on purpose. The tiles are collected
// while the per-variant sort scratch is still being reused by later variants,
// so a slice of that scratch would be rewritten under it before the billboard
// pass ever ran.
type variantTiles struct {
	variant    int
	start, end int // into GrassSystem.impostorScratch
}

// splitImpostorTiles divides one variant's distance-sorted visible tiles at the
// impostor distance, returning the near tiles to draw as meshes and appending
// the far ones to scratch for the billboard pass.
//
// cut is squared, matching tileDraw.dist2. The far tiles are copied because the
// caller's visible slice is scratch that the next variant resets, while the
// billboard pass does not run until every variant has been collected.
func splitImpostorTiles(visible []tileDraw, cut float32, scratch []tileDraw, variants []variantTiles, variant int) (mesh, outScratch []tileDraw, outVariants []variantTiles) {
	split := len(visible)
	for i, vt := range visible {
		if vt.dist2 > cut {
			split = i
			break
		}
	}
	if split < len(visible) {
		start := len(scratch)
		scratch = append(scratch, visible[split:]...)
		variants = append(variants, variantTiles{variant: variant, start: start, end: len(scratch)})
	}
	return visible[:split], scratch, variants
}

// tileDraw pairs a visible tile with its squared distance, so the draw order can
// be sorted without recomputing it.
type tileDraw struct {
	tile  GrassTile
	dist2 float32
}

// GrassTile is a contiguous instance-buffer range covering one world-space
// tile, with a bounding sphere for frustum/distance culling.
type GrassTile struct {
	FirstInstance int
	Count         int
	Center        [3]float32
	Radius        float32
}

// GrassModelSpec names a flora model and its relative spawn weight.
type GrassModelSpec struct {
	Path   string
	Weight float32
}

// GrassVariant holds a mesh and its instance buffer for one flora model variant.
// Instances are ordered by tile so each tile is a contiguous draw range.
type GrassVariant struct {
	Mesh           *Mesh
	Texture        *Texture // variant's own texture (flowers/clover differ from grass)
	InstanceBuffer core1_0.Buffer
	InstanceMemory core1_0.DeviceMemory
	InstanceCount  int
	Tiles          []GrassTile
}

// GrassSystem manages instanced grass rendering with multiple mesh variants.
type GrassSystem struct {
	Variants []GrassVariant
	Texture  *Texture // shared grass texture (from glTF)

	// visibleScratch is reused by the draw loop for the per-frame front-to-back
	// tile sort, so sorting costs no allocation.
	visibleScratch []tileDraw

	// impostorScratch accumulates every variant's far tiles across the whole
	// variant loop, because the billboard pass runs after it. It cannot share
	// visibleScratch, which each variant resets.
	impostorScratch  []tileDraw
	impostorVariants []variantTiles
}

// GrassHeightmap is the minimal interface needed for grass instance placement.
type GrassHeightmap interface {
	HeightAt(x, z float32) (float32, bool)
	NormalAt(x, z float32) [3]float32
}

// grassHash is a deterministic hash for instance placement.
func grassHash(x, z int) uint32 {
	n := uint32(x*374761393 + z*668265263)
	n = (n ^ (n >> 13)) * 1274126177
	return n ^ (n >> 16)
}

// patchNoise returns smooth deterministic value noise in [0,1] at world
// coordinates, with ~14-unit features. Used to vary flora density into
// natural thick and thin patches instead of a uniform carpet.
func patchNoise(x, z float32) float32 {
	const cell = 14.0
	gx := float64(x / cell)
	gz := float64(z / cell)
	ix := int(math.Floor(gx))
	iz := int(math.Floor(gz))
	fx := float32(gx - math.Floor(gx))
	fz := float32(gz - math.Floor(gz))
	// Smoothstep the lattice fractions
	fx = fx * fx * (3 - 2*fx)
	fz = fz * fz * (3 - 2*fz)

	lattice := func(x, z int) float32 {
		return float32(grassHash(x, z)&0xFFFF) / 65535.0
	}
	n00 := lattice(ix, iz)
	n10 := lattice(ix+1, iz)
	n01 := lattice(ix, iz+1)
	n11 := lattice(ix+1, iz+1)

	return n00*(1-fx)*(1-fz) + n10*fx*(1-fz) + n01*(1-fx)*fz + n11*fx*fz
}

// uploadInstanceBuffer serializes grass instances and uploads to a device-local buffer.
func uploadInstanceBuffer(r *Renderer, instances []GrassInstance) (core1_0.Buffer, core1_0.DeviceMemory, error) {
	dataSize := len(instances) * int(unsafe.Sizeof(GrassInstance{}))
	data := make([]byte, dataSize)
	for i, inst := range instances {
		off := i * 16
		binary.LittleEndian.PutUint32(data[off:], math.Float32bits(inst.X))
		binary.LittleEndian.PutUint32(data[off+4:], math.Float32bits(inst.Y))
		binary.LittleEndian.PutUint32(data[off+8:], math.Float32bits(inst.Z))
		binary.LittleEndian.PutUint32(data[off+12:], math.Float32bits(inst.Rotation))
	}
	return r.createDeviceLocalBuffer(data, core1_0.BufferUsageVertexBuffer)
}

// GrassDensityMask controls grass spawn probability at each world position.
// Values 0.0 = no grass, 1.0 = full density.
type GrassDensityMask struct {
	Data             []float32 // row-major [z*Width + x]
	Width, Height    int
	OriginX, OriginZ float32
	CellSize         float32
}

// Sample returns the density at a world position (bilinear interpolated).
// Returns 1.0 if position is outside the mask bounds.
func (m *GrassDensityMask) Sample(wx, wz float32) float32 {
	if m == nil || len(m.Data) == 0 {
		return 1.0
	}
	gx := (wx - m.OriginX) / m.CellSize
	gz := (wz - m.OriginZ) / m.CellSize
	if gx < 0 || gz < 0 || gx >= float32(m.Width-1) || gz >= float32(m.Height-1) {
		return 1.0
	}
	ix := int(gx)
	iz := int(gz)
	fx := gx - float32(ix)
	fz := gz - float32(iz)

	d00 := m.Data[iz*m.Width+ix]
	d10 := m.Data[iz*m.Width+ix+1]
	d01 := m.Data[(iz+1)*m.Width+ix]
	d11 := m.Data[(iz+1)*m.Width+ix+1]

	return d00*(1-fx)*(1-fz) + d10*fx*(1-fz) + d01*(1-fx)*fz + d11*fx*fz
}

// ClearCircle sets density to 0 in a circular area around (cx, cz) with given radius.
// Blends smoothly from 0 at center to current value at edge using a falloff band.
func (m *GrassDensityMask) ClearCircle(cx, cz, radius, falloff float32) {
	for iz := 0; iz < m.Height; iz++ {
		for ix := 0; ix < m.Width; ix++ {
			wx := m.OriginX + float32(ix)*m.CellSize
			wz := m.OriginZ + float32(iz)*m.CellSize
			dx := wx - cx
			dz := wz - cz
			dist := float32(math.Sqrt(float64(dx*dx + dz*dz)))
			if dist < radius {
				m.Data[iz*m.Width+ix] = 0
			} else if dist < radius+falloff {
				t := (dist - radius) / falloff
				current := m.Data[iz*m.Width+ix]
				m.Data[iz*m.Width+ix] = current * t
			}
		}
	}
}

// NewDensityMask creates a mask filled with 1.0 (full density everywhere).
func NewDensityMask(originX, originZ, worldW, worldD, cellSize float32) *GrassDensityMask {
	w := int(worldW/cellSize) + 1
	h := int(worldD/cellSize) + 1
	data := make([]float32, w*h)
	for i := range data {
		data[i] = 1.0
	}
	return &GrassDensityMask{
		Data: data, Width: w, Height: h,
		OriginX: originX, OriginZ: originZ, CellSize: cellSize,
	}
}

// CreateGrassFromModels loads glTF flora models and scatters instances across
// the heightmap, choosing variants by spawn weight. Large-scale patch noise
// varies density into natural thick/thin areas; if densityMask is non-nil,
// spawn probability is additionally modulated by the mask.
func CreateGrassFromModels(r *Renderer, models []*Model, weights []float32, hm GrassHeightmap, originX, originZ, worldW, worldD float32, densityMask *GrassDensityMask) (*GrassSystem, error) {
	if len(models) == 0 {
		return nil, nil
	}

	const spacing = 0.35
	variantCount := len(models)

	// Cumulative weight thresholds for deterministic variant selection.
	if len(weights) != variantCount {
		weights = make([]float32, variantCount)
		for i := range weights {
			weights[i] = 1
		}
	}
	var totalWeight float32
	for _, w := range weights {
		totalWeight += w
	}
	cumulative := make([]float32, variantCount)
	acc := float32(0)
	for i, w := range weights {
		acc += w / totalWeight
		cumulative[i] = acc
	}

	// Scatter instances, assigning each to a variant based on hash
	variantInstances := make([][]GrassInstance, variantCount)

	stepsX := int(worldW / spacing)
	stepsZ := int(worldD / spacing)

	for iz := 0; iz < stepsZ; iz++ {
		for ix := 0; ix < stepsX; ix++ {
			h := grassHash(ix, iz)

			jx := float32(h&0xFF) / 255.0 * spacing
			jz := float32((h>>8)&0xFF) / 255.0 * spacing

			wx := originX + float32(ix)*spacing + jx
			wz := originZ + float32(iz)*spacing + jz

			height, ok := hm.HeightAt(wx, wz)
			if !ok {
				continue
			}

			normal := hm.NormalAt(wx, wz)
			if normal[1] < 0.7 {
				continue
			}

			// Patch noise: thick and thin areas instead of a uniform carpet.
			density := 0.35 + 0.95*patchNoise(wx, wz)
			if density > 1.0 {
				density = 1.0
			}
			// Density mask: clearings (camps etc.) thin or remove flora.
			if densityMask != nil {
				density *= densityMask.Sample(wx, wz)
			}
			if density <= 0 {
				continue
			}
			if density < 1.0 {
				// Use hash bits to make the decision deterministic (no flicker).
				threshold := float32((h>>12)&0xFFF) / 4095.0
				if threshold > density {
					continue
				}
			}

			rot := float32(h>>16) / 65535.0 * 2 * math.Pi

			// Weighted variant pick from the top hash byte.
			pick := float32((h>>24)&0xFF) / 255.0
			variant := variantCount - 1
			for vi, c := range cumulative {
				if pick <= c {
					variant = vi
					break
				}
			}

			variantInstances[variant] = append(variantInstances[variant], GrassInstance{
				X: wx, Y: height, Z: wz, Rotation: rot,
			})
		}
	}

	// Use the texture from the first model that has one as the fallback
	var sharedTexture *Texture
	for _, model := range models {
		for _, mm := range model.Meshes {
			if mm.Texture != nil {
				sharedTexture = mm.Texture
				break
			}
		}
		if sharedTexture != nil {
			break
		}
	}

	// Build variants
	var variants []GrassVariant
	total := 0
	tileCount := 0
	for vi, model := range models {
		if len(model.Meshes) == 0 || len(variantInstances[vi]) == 0 {
			continue
		}

		mesh := model.Meshes[0].Mesh
		instances, tiles := buildGrassTiles(variantInstances[vi], mesh, originX, originZ)

		buf, mem, err := uploadInstanceBuffer(r, instances)
		if err != nil {
			return nil, err
		}

		variants = append(variants, GrassVariant{
			Mesh:           mesh,
			Texture:        model.Meshes[0].Texture,
			InstanceBuffer: buf,
			InstanceMemory: mem,
			InstanceCount:  len(instances),
			Tiles:          tiles,
		})
		total += len(instances)
		tileCount += len(tiles)
	}

	log.Printf("Flora: %d instances across %d variants (%d culling tiles)", total, len(variants), tileCount)
	return &GrassSystem{
		Variants: variants,
		Texture:  sharedTexture,
	}, nil
}

// buildGrassTiles buckets instances into world-space tiles and returns them
// reordered so each tile occupies a contiguous instance-buffer range, along
// with per-tile bounding spheres for frustum/distance culling at draw time.
func buildGrassTiles(instances []GrassInstance, mesh *Mesh, originX, originZ float32) ([]GrassInstance, []GrassTile) {
	type tileKey struct{ tx, tz int }
	buckets := make(map[tileKey][]GrassInstance)
	for _, inst := range instances {
		k := tileKey{
			tx: int(math.Floor(float64((inst.X - originX) / grassTileSize))),
			tz: int(math.Floor(float64((inst.Z - originZ) / grassTileSize))),
		}
		buckets[k] = append(buckets[k], inst)
	}

	// Deterministic tile order (row-major) so rebuilds are reproducible.
	keys := make([]tileKey, 0, len(buckets))
	for k := range buckets {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(a, b int) bool {
		if keys[a].tz != keys[b].tz {
			return keys[a].tz < keys[b].tz
		}
		return keys[a].tx < keys[b].tx
	})

	// Sphere must cover blades at the tile edge: mesh extent at blade scale,
	// plus a small margin for wind sway.
	bladeMargin := mesh.BoundRadius*grassBladeScale + 0.5

	// Shuffled with a fixed seed, which makes any prefix of a tile's instances a
	// spatially uniform sample of it. That is what lets the draw loop thin distant
	// tiles by simply drawing fewer instances -- see grassKeepFraction. Left in
	// scatter order, a prefix would be whichever band of the tile the scatter
	// happened to visit first, and thinning would eat the tile from one side.
	//
	// Fixed seed rather than time-based so a rebuild is reproducible, which the
	// tile ordering above already goes out of its way to be.
	shuffle := rand.New(rand.NewSource(0x5eed))

	ordered := make([]GrassInstance, 0, len(instances))
	tiles := make([]GrassTile, 0, len(keys))
	for _, k := range keys {
		bucket := buckets[k]
		shuffle.Shuffle(len(bucket), func(i, j int) { bucket[i], bucket[j] = bucket[j], bucket[i] })
		first := len(ordered)
		ordered = append(ordered, bucket...)

		minX, minY, minZ := bucket[0].X, bucket[0].Y, bucket[0].Z
		maxX, maxY, maxZ := minX, minY, minZ
		for _, inst := range bucket[1:] {
			minX = min(minX, inst.X)
			maxX = max(maxX, inst.X)
			minY = min(minY, inst.Y)
			maxY = max(maxY, inst.Y)
			minZ = min(minZ, inst.Z)
			maxZ = max(maxZ, inst.Z)
		}
		cx := (minX + maxX) / 2
		cy := (minY + maxY) / 2
		cz := (minZ + maxZ) / 2
		dx := (maxX - minX) / 2
		dy := (maxY - minY) / 2
		dz := (maxZ - minZ) / 2
		radius := float32(math.Sqrt(float64(dx*dx+dy*dy+dz*dz))) + bladeMargin

		tiles = append(tiles, GrassTile{
			FirstInstance: first,
			Count:         len(bucket),
			Center:        [3]float32{cx, cy, cz},
			Radius:        radius,
		})
	}
	return ordered, tiles
}

// Destroy releases GPU resources for the grass system.
func (gs *GrassSystem) Destroy(deviceDriver core1_0.DeviceDriver) {
	for _, v := range gs.Variants {
		if v.InstanceBuffer.Handle() != 0 {
			deviceDriver.DestroyBuffer(v.InstanceBuffer, nil)
			deviceDriver.FreeMemory(v.InstanceMemory, nil)
		}
	}
}
