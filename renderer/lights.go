package renderer

import (
	"encoding/binary"
	"math"

	"github.com/derekmwright/glyphengine/renderer/lightcluster"
)

// MaxLights is the maximum number of lights (point + spot combined) the GPU
// light buffer holds.
//
// It is lightcluster's budget rather than a second opinion about one: the
// binner decides which lights survive a frame, and a renderer-side ceiling
// that disagreed would either waste a buffer or drop lights the binner kept,
// with nothing to say which had happened.
const MaxLights = lightcluster.MaxLights

// MaxPointLights is an alias for MaxLights; see the comment there. It exists
// only for source compatibility with code written against the pre-clustering
// engine, which capped unshadowed point lights at 32.
const MaxPointLights = MaxLights

// Light buffer header flag bits (see the uvec4 flags field in
// shaders/lights.inc).
const (
	// LightFlagBruteForce makes the fragment shader loop every uploaded
	// light and ignore the cluster grid entirely. It is the reference
	// implementation clustered rendering is compared against: both modes
	// see the same uploaded array in the same order, so a difference
	// between them is the binning and nothing else. See `task lights`.
	LightFlagBruteForce uint32 = 1 << 0
	// LightFlagHeatmap replaces the lit colour with a ramp of each
	// fragment's cluster cell light count, independent of LightFlagBruteForce.
	LightFlagHeatmap uint32 = 1 << 1
	// LightFlagVolumetric says at least one uploaded light has a non-zero
	// GpuLight.Params.x, so the in-scattering march is worth entering at all.
	//
	// This is the whole of the early-out for a scene that does not use
	// volumetrics, and it is a header bit rather than a per-cell test because
	// of where the cost would otherwise land. Without it, every lit fragment
	// and every sky pixel would walk its froxel's light list once more per
	// step just to discover that nothing in it scatters -- Steps times the
	// cell's light count, on scenes that asked for none of it. With it the
	// march is behind one branch on a value that is uniform across the whole
	// draw, which every GPU this targets resolves for the wave rather than
	// per lane.
	//
	// Set by the caller that packs the lights, not derived in the shader:
	// deriving it would mean the loop this exists to avoid.
	//
	// A PER-CELL "has volumetric" bit was considered and not built, and the
	// reason is a measurement rather than a preference. 21-streetlights at
	// 1920x1080, every light in the same places, three ways, means of three
	// interleaved 200-frame runs of the opaque pass:
	//
	//	no light scatters (this flag clear)            0.121 ms
	//	7 spots scatter, 20 lamps tested and skipped   0.973 ms
	//	all 27 scatter and are evaluated               0.980 ms
	//
	// Fully evaluating twenty more lights costs 0.007 ms against loading and
	// skipping them: the per-light test is already within 1% of free. What
	// the march costs is the 0.85 ms between the first row and the other two,
	// and that is the steps and the per-step cell lookup, which a per-cell
	// bit does not touch for any cell that holds a volumetric light -- which
	// is every cell a visible beam passes through. It could only help a scene
	// where the marched cells mostly hold non-volumetric lights, and nothing
	// measured here is that scene. If one turns up, this is the number to
	// beat.
	LightFlagVolumetric uint32 = 1 << 2
)

// lightStorageBuffersPerSet is how many storage buffer bindings the shadow
// descriptor set layout now carries (bindings 3, 4, 5 — see shaders/lights.inc).
// Renderer.New checks the device's maxPerStageDescriptorStorageBuffers limit
// against this the same way it already checks push constants against
// pushConstantSize.
const lightStorageBuffersPerSet = 3

// gpuLightSize is the size in bytes of one GpuLight entry: four vec4s.
//
// It was three until volumetric scattering needed a per-light intensity (see
// GpuLight.Params). A fourth vec4 rather than a bit stolen out of one of the
// three: every float already there is a coordinate, a colour or a cosine, and
// packing an intensity into the spare bits of one of those would have made
// the shader unpack a value on every light of every fragment -- for eight
// bytes of a buffer that is 64 KB at the full MaxLights budget. The stride is
// what the shader's std430 array indexing assumes, so it has to agree in four
// places at once: here, lights.inc's struct, packLights, and
// TestPackLightsRoundTrip.
const gpuLightSize = 64

// lightHeaderSize is the size in bytes of the fixed header at the front of
// the LightBuffer SSBO, before the lights[] array: grid, zParams, screen and
// flags, each a 16-byte vec4/uvec4 with no implicit padding between them.
const lightHeaderSize = 64

// lightCellSize is the size in bytes of one Cell entry in the ClusterGrid
// SSBO: two uints.
const lightCellSize = 8

// lightBufferSize, clusterGridBufferSize and lightIndexBufferSize are the
// fixed sizes of the three storage buffers. Every one of them comes from
// lightcluster, which owns the budgets, and they are allocated once: the
// grid is a cell COUNT rather than a pixel size, so a window resize changes
// the contents of these buffers and never their size.
//
// Variables rather than constants only because lightcluster.DefaultGrid is a
// var -- it is a tunable the package expects to be changed with a
// measurement, and the buffers have to follow it rather than pin it. They
// read it at package init, which is before any game's main can run, so
// editing the constant sizes these correctly and assigning to it at runtime
// would not.
var (
	lightBufferSize       = lightHeaderSize + MaxLights*gpuLightSize
	clusterGridBufferSize = lightcluster.DefaultGrid.Cells() * lightCellSize
	lightIndexBufferSize  = lightcluster.MaxLightIndices * 4
)

// GpuLight is one light's 64-byte entry in the LightBuffer SSBO (the GpuLight
// struct in shaders/lights.inc). It replaces the old 32-byte PointLightData
// now that a light can also be a spot: DirCone is the zero vector for a point
// light, which is why a GpuLight's zero value already means what
// PointLightData's zero value meant -- no cone, and (with PosRange.w <= 0) no
// light at all.
//
// Params.x continues that property to volumetrics: zero is "this light does
// not scatter", which is what every light written before the field existed
// packs as, and what makes those scenes render byte-identically and pay
// nothing.
type GpuLight struct {
	PosRange [4]float32 // xyz world position, w range (<=0 = disabled)
	Color    [4]float32 // rgb linear colour*intensity, a = cos(inner half-angle)
	DirCone  [4]float32 // xyz unit direction the light points, w = cos(outer half-angle)
	// Params.x is the volumetric scattering intensity -- how much of this
	// light the air between the eye and a surface sends back toward the eye.
	// 0 (the default) means the march skips this light entirely. yzw are
	// spare; they are packed as zero rather than left alone, because this
	// buffer is host-visible memory a frame in flight reuses.
	Params [4]float32
}

// LightGridCell is one cell's 8-byte entry in the ClusterGrid SSBO: an offset
// and count into the LightIndices buffer.
//
// An alias, not a copy, because the binner produces these and the renderer
// only ever copies them to the GPU. A second struct with the same two fields
// would compile for exactly as long as it took someone to reorder one of
// them.
type LightGridCell = lightcluster.Cell

// packLightHeader writes the fixed 64-byte LightBuffer header into dst[:64]:
// grid dimensions and light count, z-slice parameters, framebuffer size, and
// flags. Matches the std430 layout in shaders/lights.inc exactly -- four
// consecutive 16-byte fields, so there is no padding to account for.
//
// Every number describing the grid comes from the Mapping the binner used for
// this frame, including screen.zw. Recomputing grid.x/width here would be a
// second copy of the shader's cell arithmetic, and a copy that rounded the
// division differently would move light lists a tile sideways -- which looks
// like a binning bug and is not one.
//
// A plain function on byte slices rather than a method, so it can be called
// with whatever the clusterer produced without needing a live Renderer.
func packLightHeader(dst []byte, m lightcluster.Mapping, numLights uint32, screenW, screenH float32, flags uint32) {
	binary.LittleEndian.PutUint32(dst[0:4], uint32(m.Grid.X))
	binary.LittleEndian.PutUint32(dst[4:8], uint32(m.Grid.Y))
	binary.LittleEndian.PutUint32(dst[8:12], uint32(m.Grid.Z))
	binary.LittleEndian.PutUint32(dst[12:16], numLights)
	binary.LittleEndian.PutUint32(dst[16:20], math.Float32bits(m.SliceScale))
	binary.LittleEndian.PutUint32(dst[20:24], math.Float32bits(m.SliceBias))
	// zParams.z is the per-cell light cap, which only the debug heatmap reads:
	// it is the count at which a cell starts dropping lights, so it is the only
	// number that makes the ramp mean something rather than look alarming. Sent
	// rather than written into the shader because a constant copied into GLSL
	// is a constant that will disagree with Go the first time it is tuned.
	binary.LittleEndian.PutUint32(dst[24:28], math.Float32bits(float32(lightcluster.MaxLightsPerCell)))
	// zParams.w reserved.
	binary.LittleEndian.PutUint32(dst[32:36], math.Float32bits(screenW))
	binary.LittleEndian.PutUint32(dst[36:40], math.Float32bits(screenH))
	binary.LittleEndian.PutUint32(dst[40:44], math.Float32bits(m.ScreenScaleX))
	binary.LittleEndian.PutUint32(dst[44:48], math.Float32bits(m.ScreenScaleY))
	binary.LittleEndian.PutUint32(dst[48:52], flags)
	// flags.y, flags.z, flags.w reserved.
}

// packLights writes lights into dst starting at its beginning -- callers pass
// the sub-slice after the header, i.e. dst[lightHeaderSize:]. Lights beyond
// what dst can hold are silently dropped rather than overrunning the buffer;
// the binner has already applied the MaxLights budget by the time anything
// gets here, so in practice this bound is never reached. Returns the number
// of lights written.
func packLights(dst []byte, lights []GpuLight) int {
	n := len(lights)
	if limit := len(dst) / gpuLightSize; n > limit {
		n = limit
	}
	for i := 0; i < n; i++ {
		off := i * gpuLightSize
		l := lights[i]
		for c := 0; c < 4; c++ {
			binary.LittleEndian.PutUint32(dst[off+c*4:], math.Float32bits(l.PosRange[c]))
			binary.LittleEndian.PutUint32(dst[off+16+c*4:], math.Float32bits(l.Color[c]))
			binary.LittleEndian.PutUint32(dst[off+32+c*4:], math.Float32bits(l.DirCone[c]))
			binary.LittleEndian.PutUint32(dst[off+48+c*4:], math.Float32bits(l.Params[c]))
		}
	}
	return n
}

// packCells writes cells (the ClusterGrid SSBO's contents) into dst. Same
// overrun behaviour as packLights.
func packCells(dst []byte, cells []LightGridCell) int {
	n := len(cells)
	if limit := len(dst) / lightCellSize; n > limit {
		n = limit
	}
	for i := 0; i < n; i++ {
		off := i * lightCellSize
		binary.LittleEndian.PutUint32(dst[off:], cells[i].Offset)
		binary.LittleEndian.PutUint32(dst[off+4:], cells[i].Count)
	}
	return n
}

// packIndices writes indices (the LightIndices SSBO's contents) into dst.
// Same overrun behaviour as packLights.
func packIndices(dst []byte, indices []uint32) int {
	n := len(indices)
	if limit := len(dst) / 4; n > limit {
		n = limit
	}
	for i := 0; i < n; i++ {
		binary.LittleEndian.PutUint32(dst[i*4:], indices[i])
	}
	return n
}
