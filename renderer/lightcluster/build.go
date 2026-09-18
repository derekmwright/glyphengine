package lightcluster

import (
	"math"
	"slices"

	"github.com/go-gl/mathgl/mgl32"
)

// pixelMargin widens every screen bound by a pixel on each side.
//
// Three things it covers, all of them smaller than a pixel: the binner works in
// float64 while the shader works in float32, so a bound that lands exactly on a
// tile edge can round either way; MSAA centroid interpolation moves the shaded
// point up to half a pixel off gl_FragCoord's centre, and the cell comes from
// gl_FragCoord while the lighting comes from the interpolated position; and the
// oracle test projects with mgl32's float32 arithmetic, not the binner's.
// One pixel is 0.8% of a tile at 1920x16, so the waste is not worth measuring
// and the alternative is a tile-shaped seam that only appears when a light edge
// happens to line up with a tile edge.
const pixelMargin = 1.0

// depthMargin widens every depth bound by this fraction, for the same reasons
// as pixelMargin. It only changes the slice range when a light's extent ends
// within 0.1% of a slice boundary, and slices are a factor of 1.43 apart.
const depthMargin = 1e-3

// Builder bins lights into froxels, reusing its buffers between frames.
//
// Not safe for concurrent use: one Builder per camera, and a second lit camera
// (planar reflection, split screen) needs its own grid anyway.
type Builder struct {
	res    Result
	cand   []candidate
	boxes  []cellBox
	counts []uint32
	bounds froxelBounds
	grid   Grid // grid the buffers are sized for
}

// candidate is a light that survived the frustum test, with the bounding sphere
// already in view space so the binning pass does not touch the world again.
type candidate struct {
	key    float32 // distance from the eye to the light minus its range
	index  int32   // index in the slice handed to Build
	center mgl32.Vec3
	radius float32
}

// cellBox is the inclusive froxel range a light was bound to. It is the range
// the sphere-against-cell test is run over, not the set of cells the light ends
// up in.
type cellBox struct{ x0, x1, y0, y1, z0, z1 int32 }

func (b cellBox) cells() int {
	return int(b.x1-b.x0+1) * int(b.y1-b.y0+1) * int(b.z1-b.z0+1)
}

// New returns a Builder sized for DefaultGrid. It grows on the first Build
// with a larger grid or more lights than it has seen, and allocates nothing
// after that; BenchmarkBuild and TestBuildDoesNotAllocate hold it to that.
func New() *Builder {
	b := &Builder{}
	b.reserve(DefaultGrid)
	b.cand = make([]candidate, 0, MaxLights)
	b.boxes = make([]cellBox, 0, MaxLights)
	b.res.Order = make([]int32, 0, MaxLights)
	return b
}

// reserve sizes the per-cell buffers, which only changes when the grid does.
// The grid is a cell COUNT, so a window resize does not come through here.
func (b *Builder) reserve(g Grid) {
	if b.grid == g && b.counts != nil {
		return
	}
	b.grid = g
	n := g.Cells()
	b.counts = make([]uint32, n)
	b.res.Cells = make([]Cell, n)
	// The index buffer never needs more than every cell full, and for a small
	// grid that is far less than MaxLightIndices.
	budget := n * MaxLightsPerCell
	if budget > MaxLightIndices {
		budget = MaxLightIndices
	}
	b.res.Indices = make([]uint32, 0, budget)
}

// Build bins lights for one frame and returns the result, which points into the
// Builder's storage and stays valid until the next Build.
func (b *Builder) Build(lights []Light, p Params) *Result {
	p = p.normalized()
	m := NewMapping(p)
	b.reserve(p.Grid)
	b.bounds.update(p, m)

	res := &b.res
	res.Mapping = m
	res.Order = res.Order[:0]
	res.Indices = res.Indices[:0]
	res.Stats = Stats{Submitted: len(lights)}
	clear(b.counts)
	clear(res.Cells)

	// ── Frustum cull, in view space ──
	//
	// The planes come out of the projection matrix, so the near and far planes
	// are wherever the matrix puts them; nothing here assumes reverse-Z, or
	// even which of the two depth planes is the near one.
	planes := viewFrustumPlanes(p.Proj)
	rscale := viewRadiusScale(p.View)
	eye := cameraPosition(p.View)
	b.cand = b.cand[:0]
	for i := range lights {
		l := &lights[i]
		if !(l.Range > 0) { // NaN too
			res.Stats.Culled++
			continue
		}
		wc, wr := influenceSphere(l)
		center := mgl32.Vec3{
			p.View[0]*wc[0] + p.View[4]*wc[1] + p.View[8]*wc[2] + p.View[12],
			p.View[1]*wc[0] + p.View[5]*wc[1] + p.View[9]*wc[2] + p.View[13],
			p.View[2]*wc[0] + p.View[6]*wc[1] + p.View[10]*wc[2] + p.View[14],
		}
		radius := wr * rscale
		if !finite3(center) || !(radius > 0 && radius < math.MaxFloat32) {
			res.Stats.Culled++
			continue
		}
		if !sphereInFrustum(&planes, center, radius) {
			res.Stats.Culled++
			continue
		}
		b.cand = append(b.cand, candidate{
			key:    l.Pos.Sub(eye).Len() - l.Range,
			index:  int32(i),
			center: center,
			radius: radius,
		})
	}

	// ── Priority order and the light budget ──
	//
	// Ascending (distance to the eye - range) puts the lights whose surface is
	// nearest the camera first, so what is dropped over budget is what is
	// furthest from having anything visible to light. The submission index
	// breaks ties, which is what makes the whole thing reproducible: without it
	// two lights at the same distance could swap places between frames and the
	// index buffer would differ byte for byte with nothing having moved.
	slices.SortFunc(b.cand, compareCandidates)
	kept := b.cand
	if len(kept) > MaxLights {
		res.Stats.DroppedOverBudget = len(kept) - MaxLights
		kept = kept[:MaxLights]
	}
	if len(kept) > cap(res.Order) {
		res.Order = make([]int32, 0, len(kept))
		b.boxes = make([]cellBox, 0, len(kept))
	}

	// ── Count what each cell wants ──
	bn := newBinner(m, p)
	b.boxes = b.boxes[:0]
	for i := range kept {
		box, unbounded := bn.box(kept[i].center, float64(kept[i].radius))
		if unbounded {
			res.Stats.UnboundedLights++
		}
		b.boxes = append(b.boxes, box)
		res.Order = append(res.Order, kept[i].index)
		binned, wide := b.bin(i, box, kept[i].center, kept[i].radius, false)
		if wide {
			res.Stats.ScreenWideLights++
		}
		res.Stats.CellsTested += box.cells()
		res.Stats.CellsBinned += binned
	}
	res.Stats.Uploaded = len(res.Order)

	// ── Lay out the index buffer ──
	//
	// Two caps bite here, and they are different failures. A cell over
	// MaxLightsPerCell keeps its first N lights, which are the N nearest the
	// camera, and the rest of the grid is unaffected. Running out of index
	// buffer is worse: it truncates whatever cells come last in cell order, so
	// it shows up as the far end of the grid going dark. Both are counted.
	budget := cap(res.Indices)
	total := 0
	for i := range res.Cells {
		want := int(b.counts[i])
		if want == 0 {
			continue
		}
		if want > res.Stats.MaxCellDemand {
			res.Stats.MaxCellDemand = want
		}
		got := want
		if got > MaxLightsPerCell {
			got = MaxLightsPerCell
			res.Stats.CellsOverflowed++
		}
		if total+got > budget {
			got = budget - total
			res.Stats.CellsTruncated++
		}
		res.Cells[i] = Cell{Offset: uint32(total), Count: uint32(got)}
		total += got
		res.Stats.TotalCellLights += got
		if got > 0 {
			// Cells that wanted lights but got none because the index buffer
			// ran out are not counted here: NonEmptyCells exists to divide
			// TotalCellLights by, and counting them would report an average
			// list length shorter than any cell actually has.
			res.Stats.NonEmptyCells++
		}
		if got > res.Stats.MaxCellLights {
			res.Stats.MaxCellLights = got
		}
	}
	res.Indices = res.Indices[:total]
	res.Stats.IndexCount = total

	// ── Fill it ──
	//
	// Same light order as the counting pass, so a cell that overflowed keeps
	// its first Count lights in priority order. counts is reused as the write
	// cursor; it has done its job by now.
	clear(b.counts)
	for li := range b.boxes {
		b.bin(li, b.boxes[li], kept[li].center, kept[li].radius, true)
	}
	return res
}

// bin walks the cells of one light's candidate box and, for each cell the
// light's sphere actually reaches, either counts it (fill false) or writes the
// light into it (fill true). It returns how many cells the light landed in and
// whether it landed in every tile of some slice.
//
// One function for both passes rather than two loops that must agree: they have
// to visit exactly the same cells in the same order, or a cell would be
// reserved for one light and filled by another, and nothing about that would
// look wrong until a light appeared in the wrong place. The fill flag is loop
// invariant, so the branch costs nothing measurable.
//
// The cell test is a sphere against the cell's view-space bounding box, which
// contains the froxel, so a cell the sphere really touches is never rejected.
// The axis distances accumulate outside in, which lets a whole slice or a whole
// row drop out on one comparison: for a light containing the eye that is most
// of the work, because it reaches everything in the near slices and nothing at
// the sides of the far ones.
func (b *Builder) bin(li int, box cellBox, center mgl32.Vec3, radius float32, fill bool) (binned int, wide bool) {
	g := b.bounds.grid
	tiles := g.X * g.Y
	r2 := radius * radius
	cx, cy, cz := center[0], center[1], center[2]

	for z := int(box.z0); z <= int(box.z1); z++ {
		dz2 := axisDistanceSquared(cz, b.bounds.z[z])
		if dz2 > r2 {
			continue
		}
		// One slice of the extents, hoisted: this is the difference between an
		// index multiply and two bounds checks per cell and none.
		xs := b.bounds.x[z*g.X : (z+1)*g.X : (z+1)*g.X]
		ys := b.bounds.y[z*g.Y : (z+1)*g.Y : (z+1)*g.Y]

		inSlice := 0
		for y := int(box.y0); y <= int(box.y1); y++ {
			d2 := dz2 + axisDistanceSquared(cy, ys[y])
			if d2 > r2 {
				continue
			}
			// Columns run left to right and do not overlap except by the pixel
			// margin, so the distance to them falls and then rises and the ones
			// the sphere reaches are a contiguous run. Walking in from both
			// ends finds that run in (rejected + 2) tests instead of testing
			// every column: in the colony scene two thirds of the candidates
			// are accepted, so testing each one was paying most of the cost to
			// answer yes.
			//
			// The obvious next step -- narrow the slice's column range once
			// before walking its rows, so nine rows do not each rediscover the
			// same rejected columns -- was written and thrown away. Three
			// interleaved rounds: the colony scene went 541 to 518 us (inside a
			// spread of 447 to 637) and BenchmarkBuildPathological/1024 went
			// 9.0 to 11.7 ms, consistently. It buys noise and costs 30% of the
			// ceiling, most likely to register pressure in this loop, since
			// nothing stopped being inlined.
			lo, hi := int(box.x0), int(box.x1)
			for lo <= hi && d2+axisDistanceSquared(cx, xs[lo]) > r2 {
				lo++
			}
			for hi > lo && d2+axisDistanceSquared(cx, xs[hi]) > r2 {
				hi--
			}
			if lo > hi {
				continue
			}
			base := z*tiles + y*g.X
			if fill {
				for x := lo; x <= hi; x++ {
					i := base + x
					c := &b.res.Cells[i]
					if k := b.counts[i]; k < c.Count {
						b.res.Indices[c.Offset+k] = uint32(li)
						b.counts[i] = k + 1
					}
				}
			} else {
				for x := lo; x <= hi; x++ {
					b.counts[base+x]++
				}
			}
			inSlice += hi - lo + 1
		}
		binned += inSlice
		if inSlice == tiles {
			wide = true
		}
	}
	return binned, wide
}

// compareCandidates is the priority order: nearest surface first, ties broken
// by submission index so the result is a total order and therefore stable
// without a stable sort.
func compareCandidates(a, b candidate) int {
	switch {
	case a.key < b.key:
		return -1
	case a.key > b.key:
		return 1
	case a.index < b.index:
		return -1
	case a.index > b.index:
		return 1
	}
	return 0
}

// influenceSphere returns a world-space sphere containing every point the light
// can light.
//
// For a point light that is the range sphere. For a spot it is the tight sphere
// around the spherical sector (cone of directions clipped to the range), which
// is a much smaller bound than the range sphere for a narrow cone: a 20 degree
// spot's sector sphere has 0.53 of the radius and 0.15 of the volume.
//
// Nothing about the cone is trusted beyond what keeps this conservative. A
// direction that is not unit length, or a cosine outside [-1,1], gives the full
// range sphere, because a cone can only ever remove light and the range sphere
// bounds the light whatever the shader decides the cone is.
func influenceSphere(l *Light) (center mgl32.Vec3, radius float32) {
	dirLen2 := l.Dir[0]*l.Dir[0] + l.Dir[1]*l.Dir[1] + l.Dir[2]*l.Dir[2]
	cos := l.CosOuter
	// cos <= 0.5 is a half-angle of 60 degrees or more, where the sector sphere
	// (radius range/2cos) is no smaller than the range sphere, so there is
	// nothing to gain and the division gets unstable towards 90 degrees.
	if dirLen2 < 0.999 || dirLen2 > 1.001 || !(cos > 0.5) || !(cos <= 1) {
		return l.Pos, l.Range
	}
	// Centre the sphere on the axis at range/(2cos) from the apex, where it
	// passes through both the apex and the whole rim of the cap: every point of
	// the sector is inside it, and no smaller sphere contains both.
	h := l.Range / (2 * cos)
	return l.Pos.Add(l.Dir.Mul(h)), h
}

// finite3 reports whether a view-space centre is a real, usable position, which
// keeps a NaN light from poisoning the sort order or the cell bounds.
//
// One squared length rather than six calls to math.IsNaN and math.IsInf: those
// were 15% of Build's profile at 4096 lights, for a guard that fires never.
// NaN and infinity both fail the comparison, and so does a position past 1e19,
// which no frustum with a finite far plane can contain anyway.
func finite3(v mgl32.Vec3) bool {
	d := v[0]*v[0] + v[1]*v[1] + v[2]*v[2]
	return d < math.MaxFloat32
}

// cameraPosition recovers the eye in world space from a view matrix. Only the
// priority order uses it, so a singular view matrix giving the origin back
// costs ordering, not correctness.
func cameraPosition(view mgl32.Mat4) mgl32.Vec3 {
	inv := view.Inv()
	return mgl32.Vec3{inv[12], inv[13], inv[14]}
}

// viewRadiusScale returns a factor by which the view matrix can grow a sphere's
// radius, so the binner can transform a world-space bounding sphere by moving
// its centre.
//
// Every view matrix this engine builds is rigid (mgl32.LookAtV), which returns
// 1. Rather than assume that, the rotation part is checked for orthonormality
// and anything else falls back to the Frobenius norm, which is an upper bound
// on the largest singular value and therefore still conservative — just
// wasteful, which is the right way round.
func viewRadiusScale(view mgl32.Mat4) float32 {
	c0 := mgl32.Vec3{view[0], view[1], view[2]}
	c1 := mgl32.Vec3{view[4], view[5], view[6]}
	c2 := mgl32.Vec3{view[8], view[9], view[10]}
	const tol = 1e-4
	rigid := absf(c0.Dot(c0)-1) < tol && absf(c1.Dot(c1)-1) < tol && absf(c2.Dot(c2)-1) < tol &&
		absf(c0.Dot(c1)) < tol && absf(c0.Dot(c2)) < tol && absf(c1.Dot(c2)) < tol
	if rigid {
		return 1
	}
	return float32(math.Sqrt(float64(c0.Dot(c0) + c1.Dot(c1) + c2.Dot(c2))))
}

// viewFrustumPlanes extracts the six clip planes in VIEW space (Gribb-Hartmann
// on the projection matrix alone), each normalized so a plane test is a
// distance.
//
// The z planes use Vulkan's clip volume, 0 <= z <= w, not OpenGL's -w <= z <= w.
// Under reverse-Z that makes planes[4] the far plane and planes[5] the near
// one; the binner never needs to know which is which, only that a sphere
// outside any of them is outside the frustum.
func viewFrustumPlanes(proj mgl32.Mat4) [6][4]float32 {
	r0 := [4]float32{proj[0], proj[4], proj[8], proj[12]}
	r1 := [4]float32{proj[1], proj[5], proj[9], proj[13]}
	r2 := [4]float32{proj[2], proj[6], proj[10], proj[14]}
	r3 := [4]float32{proj[3], proj[7], proj[11], proj[15]}

	var pl [6][4]float32
	for i := 0; i < 4; i++ {
		pl[0][i] = r3[i] + r0[i] // x >= -w
		pl[1][i] = r3[i] - r0[i] // x <=  w
		pl[2][i] = r3[i] + r1[i] // y >= -w
		pl[3][i] = r3[i] - r1[i] // y <=  w
		pl[4][i] = r2[i]         // z >=  0
		pl[5][i] = r3[i] - r2[i] // z <=  w
	}
	for i := range pl {
		p := &pl[i]
		l := math.Sqrt(float64(p[0]*p[0] + p[1]*p[1] + p[2]*p[2]))
		if l == 0 {
			// A degenerate plane cannot say anything; leaving it unnormalized
			// keeps the test from turning into NaN, which would cull nothing.
			continue
		}
		inv := float32(1 / l)
		p[0] *= inv
		p[1] *= inv
		p[2] *= inv
		p[3] *= inv
	}
	return pl
}

// sphereInFrustum reports whether the sphere might be inside. It answers yes
// for some spheres that are outside near the corners, which costs a few
// froxels, and never no for a sphere that is inside, which is the direction
// that matters.
func sphereInFrustum(planes *[6][4]float32, c mgl32.Vec3, r float32) bool {
	for i := range planes {
		p := &planes[i]
		if p[0]*c[0]+p[1]*c[1]+p[2]*c[2]+p[3] < -r {
			return false
		}
	}
	return true
}

// binner turns a view-space bounding sphere into a froxel range.
type binner struct {
	m             Mapping
	width, height float64
	near          float64
	ax, ay        float64 // proj[0], proj[5]
	simple        bool
}

func newBinner(m Mapping, p Params) binner {
	ax, ay, ok := perspectiveAxes(p.Proj)
	return binner{
		m:      m,
		width:  float64(p.Width),
		height: float64(p.Height),
		near:   float64(p.Near),
		ax:     float64(ax),
		ay:     float64(ay),
		simple: ok,
	}
}

// perspectiveAxes reports whether the projection is the plain symmetric
// perspective the screen bound assumes — clip.x from view x alone, clip.y from
// view y alone, clip.w the positive view depth — and returns the two scale
// factors if so.
//
// The comparisons are exact because the zeros are exact: mgl32.Perspective
// writes literal zeros and reverseZProjection only touches elements 5, 10 and
// 14. An off-centre or sheared projection (a portal, a jittered TAA matrix
// with the jitter in the matrix rather than the viewport) fails this and every
// light gets a whole-screen bound, which is slow and correct rather than fast
// and wrong.
func perspectiveAxes(proj mgl32.Mat4) (ax, ay float32, ok bool) {
	ax, ay = proj[0], proj[5]
	ok = ax != 0 && ay != 0 &&
		proj[4] == 0 && proj[8] == 0 && proj[12] == 0 &&
		proj[1] == 0 && proj[9] == 0 && proj[13] == 0 &&
		proj[3] == 0 && proj[7] == 0 && proj[15] == 0 &&
		proj[11] == -1
	return ax, ay, ok
}

// box returns the inclusive froxel range a view-space sphere touches, and
// whether the screen bound had to widen to the whole framebuffer.
//
// Conservative because every axis is bounded independently and each bound is a
// superset of the sphere's extent on that axis:
//
//   - Depth is the sphere's own [d-r, d+r], and Mapping.Slice is monotonic and
//     clamped, so every depth in that interval maps into [z0, z1].
//   - The screen bounds are the exact extremes of ndc.x and ndc.y over the
//     sphere (see tangentBounds), and pixel position is monotonic in ndc, and
//     Mapping.Tile/Row are monotonic in pixels.
//
// The exactness of the screen bound depends on the whole sphere being in front
// of the camera; a sphere that reaches past the near plane projects to
// something unbounded (the limit of a point approaching the eye plane is the
// whole image), so it gets the whole screen. That is the case that eats the
// classic false negative, where a naive projected-centre rect silently wraps.
func (bn binner) box(c mgl32.Vec3, r float64) (cellBox, bool) {
	var box cellBox

	// Depth first: it is the same for both branches.
	d := float64(-c[2])
	rr := r
	dmin := (d - rr) * (1 - depthMargin)
	dmax := (d + rr) * (1 + depthMargin)
	if !(dmax > 0) {
		dmax = 0
	}
	if !(dmin > 0) {
		dmin = 0
	}
	box.z0 = int32(bn.m.Slice(float32(dmin)))
	box.z1 = int32(bn.m.Slice(float32(dmax)))

	cx, cy, cz := float64(c[0]), float64(c[1]), float64(c[2])
	if !bn.simple || !(cz+rr < -bn.near) {
		box.x1 = int32(bn.m.Grid.X - 1)
		box.y1 = int32(bn.m.Grid.Y - 1)
		return box, true
	}

	xlo, xhi, ok := tangentBounds(bn.ax, cx, cz, rr)
	ylo, yhi, ok2 := tangentBounds(bn.ay, cy, cz, rr)
	if !ok || !ok2 {
		box.x1 = int32(bn.m.Grid.X - 1)
		box.y1 = int32(bn.m.Grid.Y - 1)
		return box, true
	}
	box.x0 = int32(bn.m.Tile(pixelFromNDC(xlo, bn.width, -pixelMargin)))
	box.x1 = int32(bn.m.Tile(pixelFromNDC(xhi, bn.width, +pixelMargin)))
	box.y0 = int32(bn.m.Row(pixelFromNDC(ylo, bn.height, -pixelMargin)))
	box.y1 = int32(bn.m.Row(pixelFromNDC(yhi, bn.height, +pixelMargin)))
	return box, false
}

// pixelFromNDC converts a normalized device coordinate to a framebuffer pixel,
// widens it by margin, and clamps it to the framebuffer. The clamp is what
// keeps a light far off screen from turning into an integer overflow on the
// way to a tile index.
func pixelFromNDC(ndc, size, margin float64) float32 {
	px := (ndc*0.5+0.5)*size + margin
	if !(px > 0) { // NaN too
		return 0
	}
	if px > size {
		return float32(size)
	}
	return float32(px)
}

// tangentBounds returns the exact extremes of m*u/(-z) over a sphere of radius
// r whose centre is (u, z) in that plane — that is, the range of one normalized
// device coordinate over the sphere.
//
// Why not centre +/- r/depth: the projection of a sphere is not centred on the
// projection of its centre. Off axis the far side of the sphere is foreshortened
// and the near side stretched, so a symmetric rect around the projected centre
// misses the sphere on both sides — for a unit sphere 5 units off axis at 10
// units depth it misses by 15% of the rect on one side, which is a tile at any
// sane grid size, and the first tile-shaped seam anyone would have found.
//
// The extremes are where a plane through the eye is tangent to the sphere:
// |m*u + t*z| = r*sqrt(m^2 + t^2) for the boundary ndc t, which is a quadratic
// in t with roots
//
//	t = (-m*u*z +/- |m|*r*sqrt(u^2 + z^2 - r^2)) / (z^2 - r^2)
//
// ok is false when the sphere is not strictly in front of the eye plane
// (z^2 <= r^2) or contains the eye (u^2 + z^2 <= r^2), where the projection is
// unbounded and the caller must widen to the whole screen. Callers only get
// here with the sphere entirely in front of the near plane, so this is a
// backstop rather than a path.
func tangentBounds(m, u, z, r float64) (lo, hi float64, ok bool) {
	den := z*z - r*r
	disc := u*u + z*z - r*r
	if !(den > 0) || !(disc > 0) {
		return 0, 0, false
	}
	s := math.Abs(m) * r * math.Sqrt(disc)
	c := -m * u * z
	lo, hi = (c-s)/den, (c+s)/den
	if math.IsNaN(lo) || math.IsNaN(hi) {
		return 0, 0, false
	}
	return lo, hi, true
}

func absf(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}
