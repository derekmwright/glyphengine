package renderer

import (
	"encoding/binary"
	"math"
)

// MaxLights is the maximum number of lights (point + spot combined) the GPU
// light buffer holds. MaxPointLights is kept as an alias, not a separate
// constant, so existing code that checked against the old 32-light ceiling
// gets the raised one for free rather than silently keeping the old cap.
const MaxLights = 1024

// MaxPointLights is an alias for MaxLights; see the comment there. It exists
// only for source compatibility with code written against the pre-clustering
// engine.
const MaxPointLights = MaxLights

// LightGridX, LightGridY and LightGridZ are the cluster grid's fixed cell
// counts (not pixel size, so buffers do not depend on resolution -- see
// decision 2 in the clustered-lighting spec). Starting values, to be
// justified by measurement once a real clusterer exists to measure.
const (
	LightGridX     = 16
	LightGridY     = 9
	LightGridZ     = 24
	LightGridCells = LightGridX * LightGridY * LightGridZ
)

// MaxLightIndices bounds the flat per-cell light index list a clusterer
// writes into the LightIndices buffer.
const MaxLightIndices = 65536

// Light buffer header flag bits (see the uvec4 flags field in
// shaders/lights.inc).
const (
	// LightFlagBruteForce makes the fragment shader loop every uploaded
	// light and ignore the cluster grid entirely. It is the reference
	// implementation clustered rendering is compared against, and — until
	// renderer/lightcluster exists — the only mode that lights anything.
	LightFlagBruteForce uint32 = 1 << 0
	// LightFlagHeatmap replaces the lit colour with a ramp of each
	// fragment's cluster cell light count, independent of LightFlagBruteForce.
	LightFlagHeatmap uint32 = 1 << 1
)

// lightStorageBuffersPerSet is how many storage buffer bindings the shadow
// descriptor set layout now carries (bindings 3, 4, 5 — see shaders/lights.inc).
// Renderer.New checks the device's maxPerStageDescriptorStorageBuffers limit
// against this the same way it already checks push constants against
// pushConstantSize.
const lightStorageBuffersPerSet = 3

// gpuLightSize is the size in bytes of one GpuLight entry: three vec4s.
const gpuLightSize = 48

// lightHeaderSize is the size in bytes of the fixed header at the front of
// the LightBuffer SSBO, before the lights[] array: grid, zParams, screen and
// flags, each a 16-byte vec4/uvec4 with no implicit padding between them.
const lightHeaderSize = 64

// lightCellSize is the size in bytes of one Cell entry in the ClusterGrid
// SSBO: two uints.
const lightCellSize = 8

// lightBufferSize, clusterGridBufferSize and lightIndexBufferSize are the
// fixed sizes of the three storage buffers, sized for the maximums above so
// they never need to be resized at runtime.
const (
	lightBufferSize       = lightHeaderSize + MaxLights*gpuLightSize
	clusterGridBufferSize = LightGridCells * lightCellSize
	lightIndexBufferSize  = MaxLightIndices * 4
)

// GpuLight is one light's 48-byte entry in the LightBuffer SSBO (the GpuLight
// struct in shaders/lights.inc). It replaces the old 32-byte PointLightData
// now that a light can also be a spot: DirCone is the zero vector for a point
// light, which is why a GpuLight's zero value already means what
// PointLightData's zero value meant -- no cone, and (with PosRange.w <= 0) no
// light at all.
type GpuLight struct {
	PosRange [4]float32 // xyz world position, w range (<=0 = disabled)
	Color    [4]float32 // rgb linear colour*intensity, a = cos(inner half-angle)
	DirCone  [4]float32 // xyz unit direction the light points, w = cos(outer half-angle)
}

// LightGridCell is one cell's 8-byte entry in the ClusterGrid SSBO: an offset
// and count into the LightIndices buffer. renderer/lightcluster produces
// these; this phase only carries the type and keeps every cell at its zero
// value (see shadowResources.uploadLights).
type LightGridCell struct {
	Offset uint32
	Count  uint32
}

// packLightHeader writes the fixed 64-byte LightBuffer header into dst[:64]:
// grid dimensions and light count, z-slice parameters, framebuffer size, and
// flags. Matches the std430 layout in shaders/lights.inc exactly -- four
// consecutive 16-byte fields, so there is no padding to account for.
//
// A plain function on byte slices rather than a method, so the integration
// phase can call it directly with whatever the clusterer computes, without
// needing a live Renderer.
func packLightHeader(dst []byte, gridX, gridY, gridZ, numLights uint32, zScale, zBias, screenW, screenH float32, flags uint32) {
	binary.LittleEndian.PutUint32(dst[0:4], gridX)
	binary.LittleEndian.PutUint32(dst[4:8], gridY)
	binary.LittleEndian.PutUint32(dst[8:12], gridZ)
	binary.LittleEndian.PutUint32(dst[12:16], numLights)
	binary.LittleEndian.PutUint32(dst[16:20], math.Float32bits(zScale))
	binary.LittleEndian.PutUint32(dst[20:24], math.Float32bits(zBias))
	// zParams.z, zParams.w reserved.
	binary.LittleEndian.PutUint32(dst[32:36], math.Float32bits(screenW))
	binary.LittleEndian.PutUint32(dst[36:40], math.Float32bits(screenH))
	binary.LittleEndian.PutUint32(dst[40:44], math.Float32bits(float32(gridX)/screenW))
	binary.LittleEndian.PutUint32(dst[44:48], math.Float32bits(float32(gridY)/screenH))
	binary.LittleEndian.PutUint32(dst[48:52], flags)
	// flags.y, flags.z, flags.w reserved.
}

// packLights writes lights into dst starting at its beginning -- callers pass
// the sub-slice after the header, i.e. dst[lightHeaderSize:]. Lights beyond
// what dst can hold are silently dropped rather than overrunning the buffer;
// callers that care about the MaxLights ceiling truncate before calling this
// (see the TODO in Engine.gatherLights), so in practice this bound is never
// reached. Returns the number of lights written.
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

// lightZSliceParams derives the log-depth slicing constants so that
// slice(near) == 0 and slice(far) == slices-1 (decision 2 in the
// clustered-lighting spec: slice = floor(log(viewDepth)*scale + bias)).
//
// Falls back to the engine's documented default near/far (0.1/500) for a
// degenerate input (zero, negative, or far <= near) rather than feeding
// log() a domain error that would send every fragment's cluster lookup to
// the same garbage slice.
func lightZSliceParams(near, far float32, slices uint32) (scale, bias float32) {
	if near <= 0 || far <= near {
		near, far = 0.1, 500
	}
	scale = float32(slices-1) / float32(math.Log(float64(far/near)))
	bias = -scale * float32(math.Log(float64(near)))
	return scale, bias
}
