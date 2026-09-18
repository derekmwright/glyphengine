// Package lightcluster bins point and spot lights into a view-space froxel grid
// so a fragment shader can loop over the lights that reach it instead of over
// every light in the scene.
//
// The grid holds a fixed COUNT of cells rather than a fixed pixel size, so the
// GPU buffers do not change shape when the window resizes: Grid.X by Grid.Y
// tiles across the framebuffer, Grid.Z slices logarithmic in view depth. A
// fragment finds its cell with
//
//	vec2  t = clamp(gl_FragCoord.xy * screen.zw, vec2(0.0), vec2(grid.xy) - 1.0);
//	float d = 1.0 / gl_FragCoord.w;                 // positive view depth
//	float s = clamp(floor(log(d) * zParams.x + zParams.y), 0.0, float(grid.z) - 1.0);
//	uint  cell = uint(t.x) + uint(t.y) * grid.x + uint(s) * grid.x * grid.y;
//
// Clamp before the conversion to uint: converting a negative float to uint is
// undefined in GLSL, and log(d) goes negative for every fragment nearer than
// the slice start.
//
// [Mapping.CellIndex] is that arithmetic in Go, and it is the only copy of it on
// this side — the binner calls it too, so the binner and the shader cannot drift
// apart without a test noticing. 1.0/gl_FragCoord.w is the positive view depth
// for any projection whose clip w is -z_view, which is every projection this
// engine builds; TestClipWIsPositiveViewDepth pins that against a copy of
// app.go's reverseZProjection, with no dependence on near, far or the depth
// mapping.
//
// # The contract
//
// Binning is CONSERVATIVE: if a world point lies inside a light's range (and,
// for a spot, inside its cone) and inside the frustum, the cell containing that
// point lists that light — unless the light lost the MaxLights budget or the
// cell overflowed, both of which are counted in [Stats]. A false positive costs
// a few shader instructions. A false negative is a tile-shaped hole in the
// light, which is what a clustered renderer looks like when it is subtly wrong,
// and it will not show up in an average-error metric. Where an exact bound is
// awkward — a light sphere that reaches past the near plane, a projection that
// is not a plain symmetric perspective — the binner widens to the whole screen
// rather than guess.
//
// # Scope
//
// Pure Go, no Vulkan and no cgo: this package must build and test on a machine
// with no GPU, because the oracle test in lightcluster_test.go is the only
// cheap check that binning is conservative.
package lightcluster

import (
	"math"

	"github.com/go-gl/mathgl/mgl32"
)

// MaxLights is how many lights survive to the GPU in one frame. Lights beyond
// it are dropped in priority order (nearest surface first) and counted in
// Stats.DroppedOverBudget, so the loss is visible rather than silent.
//
// 1024 lights at 48 bytes is a 48 KB storage buffer, which is nothing, and
// BenchmarkBuild bins 1024 submitted lights in 86 us on a Ryzen 9 5900X
// (median of five runs, spread 80 to 91). The ceiling exists so the buffers
// can be sized once at startup, not because the cost demands it.
const MaxLights = 1024

// MaxLightsPerCell caps one cell's light list, and so caps the shader's inner
// loop.
//
// Measured, not chosen. BenchmarkBuild's street scene — lights of range 5 to
// 15 m scattered over a 400 m square, a quarter of them downward spots, camera
// 30 m up looking down 45 degrees, 1920x1080, the 16x9x24 grid — peaks at 20
// lights in a cell with 1024 submitted and 64 with 4096 submitted (168 and 602
// of them in the frustum), against averages of 2.3 and 12.2 over the non-empty
// cells. Dropping the camera to 2 m, where lights reach past the near plane
// and widen to the whole screen, peaks lower (30) but fills more cells. 128 is
// twice the worst of those.
//
// It cannot be sized for the worst case, because the worst case is unbounded:
// BenchmarkBuildPathological puts 1024 lights around the camera with ranges
// past 40 m and every one of them lands in every cell. That is what
// Stats.CellsOverflowed is for. Overflow keeps the first 128 in priority
// order, which is the 128 whose surfaces are nearest the camera.
const MaxLightsPerCell = 128

// MaxLightIndices caps the whole index buffer. Every cell full would be
// 3456 x 128 = 442k entries and nothing like that is ever wanted.
//
// Measured on the same scenes: 5626 indices at 1024 submitted and 14701 at
// 4096 (22 KB and 57 KB), worst of all the variants 37k at 4096 lights with
// the camera at 2 m. 131072 entries is 512 KB, three times that worst case,
// and it is a fixed allocation rather than a per-frame one. Cells that do not
// fit are truncated in cell order and counted in Stats.CellsTruncated.
const MaxLightIndices = 128 * 1024

// DefaultSliceStart is the view depth where logarithmic slicing begins, in
// metres. It is deliberately not the near plane.
//
// Slicing from near = 0.1 spends log(1/0.1)/log(far/near) of the slices on the
// first metre in front of the eye: 6 of 24 at far = 500, a quarter of the grid,
// on depths where geometry is rare and a light that close covers most of the
// screen anyway. Starting at 1 m hands those slices back to the range the scene
// is actually in. The shader formula does not change — its clamp folds
// everything nearer than the start into slice 0 — so this stays two numbers
// plus a clamp, as the GPU contract requires.
//
// Measured with TestGridOccupancy, average lights per non-empty cell, 1 m
// start against 0.1 m start: 12.2 vs 13.6 at 4096 lights with the camera 30 m
// up, 6.8 vs 7.4 at 1024 lights with the camera at 2 m, 19.0 vs 20.3 at 4096
// and 2 m — and 2.33 vs 2.00 at 1024 lights 30 m up, which is the one case it
// makes worse, and the sparsest (168 lights in view over 2420 occupied cells,
// where the average is dominated by cells holding one or two lights).
//
// It is not free either: finer slices split a light across more of them, so
// the index buffer grows in the sparse case (57 KB vs 47 KB at 4096 lights,
// 30 m up) and shrinks in the dense one (145 KB vs 188 KB at 2 m). Both are
// far inside MaxLightIndices, and the dense case is the one that hurts, so the
// trade goes this way. Set Params.SliceStart to Near to turn it off.
const DefaultSliceStart = 1.0

// DefaultGrid is the starting grid: 16x9 matches a 16:9 framebuffer so the
// tiles are square-ish (120x120 px at 1920x1080), and 24 depth slices from
// DefaultSliceStart to a 500 m far plane make each slice a factor of 1.29 in
// depth. Both are tunables; change them with a measurement, not a preference —
// TestGridOccupancy is the measurement.
var DefaultGrid = Grid{X: 16, Y: 9, Z: 24}

// Grid is the froxel count in each axis. Cell index is
// x + y*X + z*X*Y, which is what the shader computes.
type Grid struct {
	X, Y, Z int
}

// Cells returns the number of cells in the grid.
func (g Grid) Cells() int { return g.X * g.Y * g.Z }

// valid reports whether every axis has at least one cell.
func (g Grid) valid() bool { return g.X > 0 && g.Y > 0 && g.Z > 0 }

// Light is one light's geometry, in world space, as the binner sees it. Colour
// and intensity do not affect binning and are not here.
type Light struct {
	// Pos is the world-space position; Range is the distance past which the
	// light contributes nothing. Range <= 0 disables the light.
	Pos   mgl32.Vec3
	Range float32

	// Dir is the unit direction the light POINTS. The zero vector means an
	// omnidirectional point light and CosOuter is ignored, so the zero value of
	// these two fields is the point light the engine has today.
	//
	// A direction that is not unit length is treated as a point light rather
	// than trusted: the binner cannot know what a shader will make of it, and
	// the range sphere is conservative whatever the cone test does, since the
	// cone can only ever remove light.
	Dir mgl32.Vec3

	// CosOuter is the cosine of the outer half-angle. A fragment is lit when
	// dot(Dir, -L) >= CosOuter, matching the shader's
	// smoothstep(cosOuter, cosInner, dot(dir, -L)).
	CosOuter float32
}

// Params is everything about the camera and the grid that binning depends on.
// The projection is passed as the matrix the renderer actually uses rather than
// as a field of view, so the binner cannot disagree with the GPU about the Y
// flip, the aspect ratio or the depth mapping.
type Params struct {
	View mgl32.Mat4
	Proj mgl32.Mat4

	// Width and Height are the framebuffer size in pixels, matching the range
	// of gl_FragCoord.xy.
	Width, Height int

	// Near and Far bound the depth range the grid covers. They are passed
	// explicitly because a projection matrix does not have to be invertible in
	// the way that recovering them would need, and because the reverse-Z matrix
	// this engine builds encodes them in two elements that the binner would
	// otherwise have to assume the meaning of.
	Near, Far float32

	// SliceStart is the view depth where logarithmic slicing begins; fragments
	// nearer than it land in slice 0. Zero means DefaultSliceStart. Values
	// below Near or at/above Far fall back to Near.
	SliceStart float32

	Grid Grid
}

// normalized fills in defaults and clamps the values that would otherwise
// divide by zero or take the log of a non-positive number. Garbage in gets a
// conservative grid rather than a panic or a NaN scale, because this runs every
// frame inside a renderer and a NaN here would silently unlight the world.
func (p Params) normalized() Params {
	if !p.Grid.valid() {
		p.Grid = DefaultGrid
	}
	if p.Width <= 0 {
		p.Width = 1
	}
	if p.Height <= 0 {
		p.Height = 1
	}
	if !(p.Near > 0) {
		p.Near = minNear
	}
	if !(p.Far > p.Near) {
		p.Far = p.Near * 2
	}
	switch {
	case p.SliceStart == 0:
		p.SliceStart = DefaultSliceStart
	case !(p.SliceStart > 0):
		p.SliceStart = p.Near
	}
	if p.SliceStart < p.Near || p.SliceStart >= p.Far {
		p.SliceStart = p.Near
	}
	return p
}

// minNear is the smallest near plane the slice maths will accept. Log slicing
// needs a positive start and the scale grows as the start shrinks; a near plane
// of zero is a caller bug, and this keeps it from becoming an infinity.
const minNear = 1e-4

// Mapping is the froxel lookup, and the one place the cell arithmetic is
// written on the CPU side. The renderer uploads these four numbers as
// zParams.xy and screen.zw; the shader reproduces CellIndex from them.
type Mapping struct {
	Grid Grid

	// ScreenScaleX and ScreenScaleY are Grid.X/Width and Grid.Y/Height, the
	// shader's screen.zw.
	ScreenScaleX, ScreenScaleY float32

	// SliceScale and SliceBias satisfy slice = floor(log(depth)*scale + bias),
	// clamped to [0, Grid.Z-1]. They are the shader's zParams.xy.
	SliceScale, SliceBias float32
}

// NewMapping derives the froxel lookup from the camera parameters.
func NewMapping(p Params) Mapping {
	p = p.normalized()
	start := float64(p.SliceStart)
	logRange := math.Log(float64(p.Far) / start)
	scale := float64(p.Grid.Z) / logRange
	return Mapping{
		Grid:         p.Grid,
		ScreenScaleX: float32(p.Grid.X) / float32(p.Width),
		ScreenScaleY: float32(p.Grid.Y) / float32(p.Height),
		SliceScale:   float32(scale),
		SliceBias:    float32(-scale * math.Log(start)),
	}
}

// Tile returns the froxel column for a framebuffer x coordinate in pixels.
func (m Mapping) Tile(pixelX float32) int {
	return clampInt(int(pixelX*m.ScreenScaleX), 0, m.Grid.X-1)
}

// Row returns the froxel row for a framebuffer y coordinate in pixels, with
// y = 0 at the top of the image as gl_FragCoord has it under Vulkan. The Y
// flip lives in the projection matrix (proj[5] *= -1), so nothing here has to
// know about it.
func (m Mapping) Row(pixelY float32) int {
	return clampInt(int(pixelY*m.ScreenScaleY), 0, m.Grid.Y-1)
}

// Slice returns the depth slice for a positive view depth, which is
// 1.0/gl_FragCoord.w in the shader. Depths nearer than the slice start and
// beyond the far plane clamp into the end slices, so every depth has a cell.
func (m Mapping) Slice(viewDepth float32) int {
	if !(viewDepth > 0) {
		// log(0) is -Inf and log(negative) is NaN; both mean "in front of
		// everything the grid covers", which is slice 0.
		return 0
	}
	s := float32(math.Log(float64(viewDepth)))*m.SliceScale + m.SliceBias
	if !(s >= 0) { // also catches NaN
		return 0
	}
	return clampInt(int(math.Floor(float64(s))), 0, m.Grid.Z-1)
}

// CellCoords returns the froxel coordinates for a fragment.
func (m Mapping) CellCoords(pixelX, pixelY, viewDepth float32) (x, y, z int) {
	return m.Tile(pixelX), m.Row(pixelY), m.Slice(viewDepth)
}

// CellIndex returns the flat cell index for a fragment, exactly as the shader
// computes it: x + y*Grid.X + z*Grid.X*Grid.Y.
func (m Mapping) CellIndex(pixelX, pixelY, viewDepth float32) int {
	x, y, z := m.CellCoords(pixelX, pixelY, viewDepth)
	return x + y*m.Grid.X + z*m.Grid.X*m.Grid.Y
}

// Cell is one froxel's slice of the index buffer.
type Cell struct {
	Offset uint32
	Count  uint32
}

// Stats is what a frame's binning did. Every way a light can fail to reach a
// fragment is counted here; none of them are silent.
type Stats struct {
	// Submitted is the length of the slice handed to Build.
	Submitted int
	// Culled counts lights outside the frustum or with a non-positive range.
	Culled int
	// Uploaded is len(Order): the lights the GPU will see.
	Uploaded int
	// DroppedOverBudget counts lights that passed the frustum test but lost
	// the MaxLights budget.
	DroppedOverBudget int
	// CellsOverflowed counts cells that wanted more than MaxLightsPerCell.
	CellsOverflowed int
	// CellsTruncated counts cells cut short because the whole index buffer hit
	// MaxLightIndices. This is a harder failure than CellsOverflowed: it
	// depends on cell order, so the cells that lose are the ones with the
	// highest indices.
	CellsTruncated int
	// MaxCellLights is the largest count actually stored in a cell, so it is
	// bounded by MaxLightsPerCell.
	MaxCellLights int
	// MaxCellDemand is the largest count before the per-cell cap, which is the
	// number that says whether MaxLightsPerCell is big enough.
	MaxCellDemand int
	// NonEmptyCells and TotalCellLights give the average list length over the
	// cells that have one, which is the number worth tuning the grid against.
	// The average over all cells is mostly a count of empty sky.
	NonEmptyCells   int
	TotalCellLights int
	// IndexCount is len(Indices).
	IndexCount int

	// ScreenWideLights counts lights that ended up in every tile of at least
	// one slice, which is measured from what was binned rather than from what
	// was attempted. Those are the lights every fragment in that slice
	// evaluates, so this is the number that says how much of the every-light
	// loop is still there.
	ScreenWideLights int
	// UnboundedLights counts lights whose screen rectangle could not be
	// computed — they reach past the near plane, contain the eye, or the
	// projection is not a plain perspective — so every tile became a candidate
	// and the cell test had to sort it out. High is expected for a camera down
	// among its lights; it only costs time if ScreenWideLights is high too.
	UnboundedLights int
	// CellsTested and CellsBinned are the candidate cells and the cells the
	// lights actually reached. The gap between them is what the cell test
	// removes, and a ratio near 1 means it is not earning its place.
	CellsTested int
	CellsBinned int
}

// Result is the binning of one frame. Every slice points into the Builder's
// own storage and is overwritten by the next Build, so a caller that needs to
// keep one must copy it.
type Result struct {
	// Mapping is the lookup the shader must reproduce; the renderer uploads
	// its four numbers alongside the lights.
	Mapping Mapping
	// Order holds the submission indices of the surviving lights in upload
	// order. The GPU's lights[i] is the light Order[i] of the slice passed to
	// Build.
	Order []int32
	// Cells has one entry per froxel, indexed x + y*Grid.X + z*Grid.X*Grid.Y.
	Cells []Cell
	// Indices holds each cell's light list, as positions in Order (which is to
	// say indices into the shader's lights[] array), concatenated in cell
	// order.
	Indices []uint32
	Stats   Stats
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
