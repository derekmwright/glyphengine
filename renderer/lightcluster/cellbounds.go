package lightcluster

import (
	"math"

	"github.com/go-gl/mathgl/mgl32"
)

// froxelBounds holds the view-space extent of every cell, so a light that could
// not be bounded on screen can still be tested against cells one at a time
// instead of being handed all of them.
//
// This exists because "a light containing the eye is a corner case" turned out
// to be false. The consumer game's orbit camera at minimum zoom puts the eye
// 1.31 above the ground among lamps of range 4, which is 18 spheres containing
// the eye at the zoom a player builds at, and each of those used to land in all
// 144 tiles of every slice it touched — the every-light loop coming back for
// exactly the lights nearest the camera.
//
// The extents are separable, which is what makes the test cheap: view z depends
// only on the slice, view x on the slice and the tile column, view y on the
// slice and the row. So a sphere-versus-box test decomposes into three
// independent axis distances that can be accumulated with an early out per
// slice and per row, and the storage is Z + Z*X + Z*Y pairs rather than a
// bounding box per cell.
//
// They depend only on the projection, the grid, the framebuffer size and the
// depth range — never on the view matrix — so the cache survives the camera
// moving, which is every frame.
type froxelBounds struct {
	key  boundsKey
	ok   bool // false when the projection is not a plain perspective
	grid Grid
	z    [][2]float32 // per slice:            view z range (negative, zmin <= zmax)
	x    [][2]float32 // slice*Grid.X + column: view x range
	y    [][2]float32 // slice*Grid.Y + row:    view y range
}

// boundsKey is everything the extents depend on. Comparable on purpose: the
// whole cache validity check is one == against the parameters of the frame,
// and View is deliberately not in it.
type boundsKey struct {
	proj          mgl32.Mat4
	grid          Grid
	width, height int
	near, far     float32
	sliceStart    float32
}

// update rebuilds the extents if anything they depend on changed. p must
// already be normalized and m derived from it.
func (fb *froxelBounds) update(p Params, m Mapping) {
	k := boundsKey{
		proj: p.Proj,
		grid: p.Grid, width: p.Width, height: p.Height,
		near: p.Near, far: p.Far, sliceStart: p.SliceStart,
	}
	if fb.key == k && fb.z != nil {
		return
	}
	fb.key = k
	fb.grid = p.Grid
	fb.z = resize(fb.z, p.Grid.Z)
	fb.x = resize(fb.x, p.Grid.Z*p.Grid.X)
	fb.y = resize(fb.y, p.Grid.Z*p.Grid.Y)

	ax, ay, ok := perspectiveAxes(p.Proj)
	fb.ok = ok
	if !ok {
		// Without clip.x = ax*view.x there is no way back from a tile edge to a
		// view-space plane, so this projection keeps the whole-screen fallback.
		// Infinite extents say that in data rather than in a branch: every
		// sphere reaches every cell, the binner needs no special case, and the
		// inner loop has no test to predict.
		neg, pos := float32(math.Inf(-1)), float32(math.Inf(1))
		for i := range fb.z {
			fb.z[i] = [2]float32{neg, pos}
		}
		for i := range fb.x {
			fb.x[i] = [2]float32{neg, pos}
		}
		for i := range fb.y {
			fb.y[i] = [2]float32{neg, pos}
		}
		return
	}

	for z := 0; z < p.Grid.Z; z++ {
		d0, d1 := sliceDepths(z, p, m)
		fb.z[z] = [2]float32{float32(-d1), float32(-d0)}
		for t := 0; t < p.Grid.X; t++ {
			n0, n1 := tileNDC(t, p.Grid.X, float64(p.Width))
			fb.x[z*p.Grid.X+t] = axisExtent(n0, n1, d0, d1, float64(ax))
		}
		for r := 0; r < p.Grid.Y; r++ {
			n0, n1 := tileNDC(r, p.Grid.Y, float64(p.Height))
			fb.y[z*p.Grid.Y+r] = axisExtent(n0, n1, d0, d1, float64(ay))
		}
	}
}

func resize(s [][2]float32, n int) [][2]float32 {
	if cap(s) < n {
		return make([][2]float32, n)
	}
	return s[:n]
}

// sliceDepths returns the positive view depth range slice z covers, widened by
// depthMargin.
//
// The two end slices are not the formula's: the shader clamps, so slice 0 holds
// every fragment nearer than its upper boundary and the last slice holds
// everything beyond its lower one. Fragments only exist between the near and
// far planes, so slice 0 starts at Near — which is the whole point of
// DefaultSliceStart being allowed to sit above it — and the last slice ends at
// Far. Starting slice 0 at the formula's boundary instead would leave the first
// metre uncovered and take a light out of the cells right in front of the eye,
// which is the worst place to be wrong.
func sliceDepths(z int, p Params, m Mapping) (d0, d1 float64) {
	d0 = math.Exp((float64(z) - float64(m.SliceBias)) / float64(m.SliceScale))
	d1 = math.Exp((float64(z+1) - float64(m.SliceBias)) / float64(m.SliceScale))
	if z == 0 {
		d0 = math.Min(d0, float64(p.Near))
	}
	if z == p.Grid.Z-1 {
		// Unlike the near end this is only a guard against rounding: the
		// formula's last boundary is already the far plane by construction.
		// Removing it changes no test, which is recorded rather than trusted.
		d1 = math.Max(d1, float64(p.Far))
	}
	return d0 / (1 + depthMargin), d1 * (1 + depthMargin)
}

// tileNDC returns the normalized device coordinate range a froxel column or row
// covers, widened by pixelMargin. Mapping.Tile sends pixel px to column
// floor(px*count/size), so column t covers [t*size/count, (t+1)*size/count).
func tileNDC(t, count int, size float64) (n0, n1 float64) {
	p0 := float64(t)*size/float64(count) - pixelMargin
	p1 := float64(t+1)*size/float64(count) + pixelMargin
	return 2*p0/size - 1, 2*p1/size - 1
}

// axisExtent returns the view-space range of ndc*depth/a over a rectangle of
// normalized device coordinates and depths. All four corners are evaluated
// rather than reasoned about, because a is negative on the y axis (the Vulkan
// Y flip lives in proj[5]) and a sign error here is a tile-shaped hole.
func axisExtent(n0, n1, d0, d1, a float64) [2]float32 {
	v0, v1 := n0*d0/a, n0*d1/a
	v2, v3 := n1*d0/a, n1*d1/a
	lo := math.Min(math.Min(v0, v1), math.Min(v2, v3))
	hi := math.Max(math.Max(v0, v1), math.Max(v2, v3))
	// Round outwards: float32 rounding of the bound itself must not shrink it.
	return [2]float32{float32(lo) - boundEpsilon, float32(hi) + boundEpsilon}
}

// boundEpsilon covers the float64-to-float32 rounding of a cell extent. A
// millimetre in a world measured in metres, which is nothing next to the pixel
// and depth margins already in the extent, and it means the conversion cannot
// round a bound inwards.
const boundEpsilon = 1e-3

// axisDistanceSquared returns the squared distance from c to the interval, or
// zero if it is inside. Summing this over the three axes is the exact distance
// from a point to a box, so comparing the sum with r*r is an exact sphere
// against box test: it never rejects a box the sphere touches.
func axisDistanceSquared(c float32, b [2]float32) float32 {
	if c < b[0] {
		d := b[0] - c
		return d * d
	}
	if c > b[1] {
		d := c - b[1]
		return d * d
	}
	return 0
}
