package lightcluster

import (
	"math"
	"math/rand/v2"
	"reflect"
	"testing"

	"github.com/go-gl/mathgl/mgl32"
)

// testParams is a plain 1280x720 camera at the origin looking down -Z, which
// makes view space and world space the same thing and lets a test place a light
// at a depth by writing it down.
func testParams(g Grid) Params {
	return Params{
		View:   mgl32.LookAtV(mgl32.Vec3{0, 0, 0}, mgl32.Vec3{0, 0, -1}, mgl32.Vec3{0, 1, 0}),
		Proj:   reverseZProjection(60, 1280.0/720.0, 0.1, 500),
		Width:  1280,
		Height: 720,
		Near:   0.1,
		Far:    500,
		Grid:   g,
	}
}

// snapshot copies a Result, because Build hands back its own buffers and the
// next Build overwrites them. A test that compared two of them without copying
// would compare a slice to itself and pass no matter what, which is the exact
// shape of the prediction test AGENTS.md remembers.
type snapshot struct {
	Order   []int32
	Cells   []Cell
	Indices []uint32
	Stats   Stats
	Mapping Mapping
}

func snap(r *Result) snapshot {
	return snapshot{
		Order:   append([]int32(nil), r.Order...),
		Cells:   append([]Cell(nil), r.Cells...),
		Indices: append([]uint32(nil), r.Indices...),
		Stats:   r.Stats,
		Mapping: r.Mapping,
	}
}

func TestMappingMatchesShaderArithmetic(t *testing.T) {
	p := testParams(DefaultGrid)
	p.SliceStart = p.Near // slice 0 starts at the near plane for this test
	m := NewMapping(p)

	if m.ScreenScaleX != 16.0/1280.0 || m.ScreenScaleY != 9.0/720.0 {
		t.Fatalf("screen scale = %v %v, want grid/framebuffer", m.ScreenScaleX, m.ScreenScaleY)
	}
	for _, tc := range []struct {
		px, py float32
		x, y   int
	}{
		{0, 0, 0, 0},
		{79.9, 79.9, 0, 0},
		{80, 80, 1, 1},
		{639.5, 359.5, 7, 4},
		{1279.9, 719.9, 15, 8},
		{1280, 720, 15, 8},    // the far edge clamps in rather than out
		{5000, 5000, 15, 8},   // and so does nonsense
		{-10, -10, 0, 0},      // a negative pixel cannot happen in the shader
		{640, 360.0001, 8, 4}, // exactly on a tile boundary goes to the higher tile
		{639.9999, 360, 7, 4}, //
	} {
		if x, y := m.Tile(tc.px), m.Row(tc.py); x != tc.x || y != tc.y {
			t.Errorf("pixel (%v,%v) -> tile (%d,%d), want (%d,%d)", tc.px, tc.py, x, y, tc.x, tc.y)
		}
	}

	// Slices: the boundary between slice k and k+1 is exp((k-bias)/scale), and
	// the whole range is covered with no gaps and no overflow.
	if s := m.Slice(p.Near); s != 0 {
		t.Errorf("slice at the near plane = %d, want 0", s)
	}
	if s := m.Slice(p.Near * 0.5); s != 0 {
		t.Errorf("slice nearer than the near plane = %d, want 0 (the clamp)", s)
	}
	if s := m.Slice(p.Far); s != DefaultGrid.Z-1 {
		t.Errorf("slice at the far plane = %d, want %d", s, DefaultGrid.Z-1)
	}
	if s := m.Slice(p.Far * 100); s != DefaultGrid.Z-1 {
		t.Errorf("slice past the far plane = %d, want %d (the clamp)", s, DefaultGrid.Z-1)
	}
	last := -1
	for i := 0; i <= 2000; i++ {
		d := p.Near * float32(math.Exp(float64(i)/2000*math.Log(float64(p.Far/p.Near))))
		s := m.Slice(d)
		if s < last || s > last+1 {
			t.Fatalf("slice at depth %v jumped from %d to %d", d, last, s)
		}
		last = s
	}
	if last != DefaultGrid.Z-1 {
		t.Errorf("walking near to far ended on slice %d, want %d", last, DefaultGrid.Z-1)
	}

	// Every slice boundary lands where the inverse of the formula says, which
	// is what the GPU will compute from zParams.
	for k := 1; k < DefaultGrid.Z; k++ {
		d := float32(math.Exp(float64((float32(k) - m.SliceBias) / m.SliceScale)))
		if s := m.Slice(d * 1.0001); s != k {
			t.Errorf("just past boundary %d (depth %v) is slice %d", k, d, s)
		}
		if s := m.Slice(d * 0.9999); s != k-1 {
			t.Errorf("just before boundary %d (depth %v) is slice %d", k, d, s)
		}
	}

	// And the flat index is the shader's x + y*gx + z*gx*gy.
	if got, want := m.CellIndex(640, 360, 50), 8+4*16+m.Slice(50)*16*9; got != want {
		t.Errorf("CellIndex = %d, want %d", got, want)
	}
}

// TestSliceStartAbsorbsTheNearMetre checks the one place the slice formula does
// something other than the obvious: starting the log at 1 m and letting the
// shader's clamp fold everything nearer into slice 0. If this ever needs a
// third parameter the shader contract has to change, so it is worth a test that
// says out loud that it does not.
func TestSliceStartAbsorbsTheNearMetre(t *testing.T) {
	p := testParams(DefaultGrid)
	m := NewMapping(p) // SliceStart zero -> DefaultSliceStart
	if p.Near != 0.1 {
		t.Fatal("this test assumes near 0.1")
	}
	for _, d := range []float32{0.1, 0.3, 0.999} {
		if s := m.Slice(d); s != 0 {
			t.Errorf("depth %v is slice %d, want 0", d, s)
		}
	}
	if s := m.Slice(1.001); s != 0 {
		t.Errorf("depth just past the slice start is %d, want 0", s)
	}
	// The first slice spans [1, exp(1/scale)) = [1, 1.29), so 1.5 is the first
	// depth that has to be past it.
	if s := m.Slice(1.5); s != 1 {
		t.Errorf("depth 1.5 is slice %d, want 1: slicing did not start at 1 m", s)
	}
	if s := m.Slice(p.Far); s != DefaultGrid.Z-1 {
		t.Errorf("far is slice %d, want the last one", s)
	}

	// Compared with slicing from the near plane, the same depth range now gets
	// more slices: the first metre had 6.5 of 24 and now has 1.
	pn := p
	pn.SliceStart = p.Near
	mn := NewMapping(pn)
	if mn.Slice(1.0) <= 1 {
		t.Fatalf("slicing from near put depth 1 m in slice %d; the waste this "+
			"avoids is not there and DefaultSliceStart has no reason to exist", mn.Slice(1.0))
	}
	t.Logf("slices spent before 1 m: %d from the near plane, %d from DefaultSliceStart",
		mn.Slice(1.0), m.Slice(1.0))
}

// TestBuildIsDeterministic builds the same scene twice with another scene in
// between, which is where a reused buffer that is not reset shows up.
func TestBuildIsDeterministic(t *testing.T) {
	rng := rand.New(rand.NewPCG(19, 23))
	pa := testParams(DefaultGrid)
	pb := testParams(Grid{X: 8, Y: 8, Z: 8})
	pb.View = mgl32.LookAtV(mgl32.Vec3{10, 5, 10}, mgl32.Vec3{0, 0, 0}, mgl32.Vec3{0, 1, 0})

	a := makeLights(rng, 300, pa)
	b := makeLights(rng, 137, pb)

	builder := New()
	first := snap(builder.Build(a, pa))
	other := snap(builder.Build(b, pb))
	second := snap(builder.Build(a, pa))
	if !reflect.DeepEqual(first, second) {
		t.Error("the same scene built differently after another scene: buffers are leaking state")
	}
	if len(other.Indices) == 0 {
		t.Fatal("the in-between scene produced nothing, so it proved nothing")
	}

	// A second Builder must agree with the first, or the result depends on
	// history rather than input.
	if fresh := snap(New().Build(a, pa)); !reflect.DeepEqual(first, fresh) {
		t.Error("a fresh Builder disagreed with a reused one")
	}

	// Ties must break on submission index, not on whatever order the sort
	// happened to leave them in. Identical lights are the worst case: every
	// key is equal, so only the tie-break decides.
	same := make([]Light, MaxLights+64)
	for i := range same {
		same[i] = Light{Pos: mgl32.Vec3{0, 0, -20}, Range: 5}
	}
	res := builder.Build(same, pa)
	if res.Stats.Uploaded != MaxLights {
		t.Fatalf("uploaded %d of %d identical lights, want %d", res.Stats.Uploaded, len(same), MaxLights)
	}
	for i, idx := range res.Order {
		if idx != int32(i) {
			t.Fatalf("Order[%d] = %d; ties did not break on submission index", i, idx)
			break
		}
	}
}

// TestBudgetKeepsTheNearestSurfaces checks the priority rule itself: what
// survives over budget is what has a surface nearest the camera, measured as
// distance minus range, and not what happened to be submitted first.
func TestBudgetKeepsTheNearestSurfaces(t *testing.T) {
	p := testParams(DefaultGrid)
	rng := rand.New(rand.NewPCG(5, 6))
	n := MaxLights + 500
	lights := make([]Light, n)
	for i := range lights {
		// All in front of the camera and all inside the frustum, so nothing is
		// culled and the budget is the only thing that drops anything.
		depth := 1 + rng.Float32()*300
		lights[i] = Light{
			Pos:   unproject(p, rng.Float32()*1.6-0.8, rng.Float32()*1.6-0.8, depth),
			Range: 1 + rng.Float32()*40,
		}
	}

	res := New().Build(lights, p)
	if res.Stats.Culled != 0 {
		t.Fatalf("%d lights were culled; this test needs all of them inside the frustum", res.Stats.Culled)
	}
	if res.Stats.Uploaded != MaxLights || res.Stats.DroppedOverBudget != n-MaxLights {
		t.Fatalf("uploaded %d dropped %d, want %d and %d",
			res.Stats.Uploaded, res.Stats.DroppedOverBudget, MaxLights, n-MaxLights)
	}

	key := func(i int32) float32 { return lights[i].Pos.Len() - lights[i].Range }
	for i := 1; i < len(res.Order); i++ {
		if key(res.Order[i]) < key(res.Order[i-1]) {
			t.Fatalf("Order is not ascending in (distance - range) at %d", i)
		}
	}
	// Nothing dropped may be nearer than anything kept.
	kept := map[int32]bool{}
	for _, idx := range res.Order {
		kept[idx] = true
	}
	worstKept := key(res.Order[len(res.Order)-1])
	for i := range lights {
		if kept[int32(i)] {
			continue
		}
		if k := key(int32(i)); k < worstKept {
			t.Fatalf("light %d (key %v) was dropped while %v was kept", i, k, worstKept)
		}
	}
}

// TestCellOverflowKeepsThePriorityPrefix drives one cell past MaxLightsPerCell
// and checks what it keeps: the first MaxLightsPerCell lights in the same
// priority order, counted in stats rather than dropped quietly.
func TestCellOverflowKeepsThePriorityPrefix(t *testing.T) {
	p := testParams(Grid{X: 1, Y: 1, Z: 1})
	n := MaxLightsPerCell + 40
	lights := make([]Light, n)
	for i := range lights {
		// Concentric shells around a point straight ahead: light i has a
		// surface (i+1) metres from the camera, so priority order is
		// submission order and the expected prefix is obvious.
		lights[i] = Light{Pos: mgl32.Vec3{0, 0, -50}, Range: 50 - float32(i)*0.25}
	}

	res := New().Build(lights, p)
	if res.Stats.Uploaded != n {
		t.Fatalf("uploaded %d of %d, want all of them", res.Stats.Uploaded, n)
	}
	if res.Stats.CellsOverflowed != 1 || res.Stats.MaxCellDemand != n {
		t.Fatalf("stats = %+v, want 1 cell overflowed with demand %d", res.Stats, n)
	}
	c := res.Cells[0]
	if c.Count != MaxLightsPerCell || res.Stats.MaxCellLights != MaxLightsPerCell {
		t.Fatalf("cell count %d, want %d", c.Count, MaxLightsPerCell)
	}
	for i := uint32(0); i < c.Count; i++ {
		if got := res.Indices[c.Offset+i]; got != i {
			t.Fatalf("index %d of the cell is light %d, want %d: the cell did not keep "+
				"the priority prefix", i, got, i)
		}
	}
	// And the prefix really is the near end: light 0 has the nearest surface.
	if res.Order[0] != 0 {
		t.Fatalf("Order[0] = %d, want the light with the nearest surface", res.Order[0])
	}
}

// TestIndexBudgetTruncatesInCellOrder fills the index buffer, which is the
// failure that is not local: it takes whatever cells come last. The point of
// the test is that it stays inside the buffer and says so in stats.
func TestIndexBudgetTruncatesInCellOrder(t *testing.T) {
	p := testParams(DefaultGrid)
	// Lights around the camera, each reaching every cell, and enough of them to
	// want a quarter more index entries than the budget allows. Counted from
	// the constants rather than written down, because the last time
	// MaxLightIndices moved this test quietly stopped testing anything.
	n := MaxLightIndices/DefaultGrid.Cells() + MaxLightIndices/DefaultGrid.Cells()/4 + 2
	lights := make([]Light, n)
	for i := range lights {
		lights[i] = Light{Pos: mgl32.Vec3{0, 0, float32(i) * 0.01}, Range: 400}
	}

	b := New()
	res := b.Build(lights, p)
	if res.Stats.CellsTruncated == 0 {
		t.Fatalf("nothing was truncated: %+v; this test is not testing truncation", res.Stats)
	}
	if res.Stats.IndexCount != cap(res.Indices) {
		t.Errorf("index count %d, want the buffer full at %d", res.Stats.IndexCount, cap(res.Indices))
	}
	checkConsistent(t, res)

	// Truncation must be a function of the input alone, not of which frame it
	// is: the cells that lose are the ones with the highest cell index.
	if !reflect.DeepEqual(snap(res), snap(b.Build(lights, p))) {
		t.Error("truncation was not reproducible")
	}
	var lastNonEmpty int
	for i, c := range res.Cells {
		if c.Count > 0 {
			lastNonEmpty = i
		}
	}
	if lastNonEmpty == len(res.Cells)-1 {
		t.Error("the last cell still has lights; truncation did not run out at the end")
	}
}

// checkConsistent holds the output to the shape the shader will read it as:
// every cell's window lies inside the index buffer, the windows are laid out in
// cell order without overlapping, and every index names a light that was
// actually uploaded.
func checkConsistent(t *testing.T, r *Result) {
	t.Helper()
	if len(r.Cells) != r.Mapping.Grid.Cells() {
		t.Fatalf("%d cells for a %v grid", len(r.Cells), r.Mapping.Grid)
	}
	total := 0
	end := uint32(0)
	for i, c := range r.Cells {
		if c.Count == 0 {
			continue
		}
		if c.Offset < end {
			t.Fatalf("cell %d starts at %d, inside the previous cell which ended at %d", i, c.Offset, end)
		}
		if int(c.Offset+c.Count) > len(r.Indices) {
			t.Fatalf("cell %d runs to %d, past the %d indices", i, c.Offset+c.Count, len(r.Indices))
		}
		for _, li := range r.Indices[c.Offset : c.Offset+c.Count] {
			if int(li) >= len(r.Order) {
				t.Fatalf("cell %d names light %d of %d uploaded", i, li, len(r.Order))
			}
		}
		end = c.Offset + c.Count
		total += int(c.Count)
	}
	if total != r.Stats.TotalCellLights || total != len(r.Indices) {
		t.Fatalf("cells hold %d indices, stats say %d, buffer holds %d",
			total, r.Stats.TotalCellLights, len(r.Indices))
	}
	if r.Stats.IndexCount != len(r.Indices) {
		t.Fatalf("IndexCount %d, len(Indices) %d", r.Stats.IndexCount, len(r.Indices))
	}
	if r.Stats.Uploaded != len(r.Order) {
		t.Fatalf("Uploaded %d, len(Order) %d", r.Stats.Uploaded, len(r.Order))
	}
	if r.Stats.Submitted != r.Stats.Culled+r.Stats.Uploaded+r.Stats.DroppedOverBudget {
		t.Fatalf("submitted %d != culled %d + uploaded %d + dropped %d",
			r.Stats.Submitted, r.Stats.Culled, r.Stats.Uploaded, r.Stats.DroppedOverBudget)
	}
}

func TestStatsAccountForEveryLight(t *testing.T) {
	p := testParams(DefaultGrid)
	lights := []Light{
		{Pos: mgl32.Vec3{0, 0, -10}, Range: 5},    // in front, visible
		{Pos: mgl32.Vec3{0, 0, 10}, Range: 5},     // behind the camera
		{Pos: mgl32.Vec3{0, 0, -10}, Range: 0},    // disabled
		{Pos: mgl32.Vec3{0, 0, -10}, Range: -3},   // disabled
		{Pos: mgl32.Vec3{0, 0, -900}, Range: 50},  // past the far plane
		{Pos: mgl32.Vec3{900, 0, -10}, Range: 50}, // off to the side
		{Pos: mgl32.Vec3{0, 0, -0.05}, Range: 2},  // straddling the near plane
		{Pos: mgl32.Vec3{0, 0, -30}, Range: 8, // a spot pointing away from us
			Dir: mgl32.Vec3{0, 0, -1}, CosOuter: 0.9},
	}
	res := New().Build(lights, p)
	checkConsistent(t, res)

	if res.Stats.Submitted != len(lights) {
		t.Errorf("Submitted = %d, want %d", res.Stats.Submitted, len(lights))
	}
	if res.Stats.Culled != 5 {
		t.Errorf("Culled = %d, want 5 (behind, two disabled, past far, off to the side): %+v",
			res.Stats.Culled, res.Stats)
	}
	if res.Stats.Uploaded != 3 {
		t.Errorf("Uploaded = %d, want 3", res.Stats.Uploaded)
	}
	// The light straddling the near plane cannot be bounded on screen, so every
	// tile is a candidate; at 2 m of range it really does reach every tile of
	// the first slice, so it is screen-wide in the sense the stat claims. The
	// two counts mean different things and this is the case that separates
	// them: UnboundedLights is what the cell test had to sort out, and
	// ScreenWideLights is what it could not narrow.
	if res.Stats.UnboundedLights != 1 {
		t.Errorf("UnboundedLights = %d, want 1 (the one straddling the near plane)",
			res.Stats.UnboundedLights)
	}
	if res.Stats.ScreenWideLights != 1 {
		t.Errorf("ScreenWideLights = %d, want 1", res.Stats.ScreenWideLights)
	}
	if res.Stats.CellsBinned > res.Stats.CellsTested || res.Stats.CellsBinned == 0 {
		t.Errorf("binned %d of %d candidate cells", res.Stats.CellsBinned, res.Stats.CellsTested)
	}
	if res.Stats.NonEmptyCells == 0 || res.Stats.MaxCellLights == 0 {
		t.Errorf("stats say nothing landed anywhere: %+v", res.Stats)
	}

	// A light in front of the camera lands in the cells around the centre of
	// the screen and nowhere else, which is the cheapest end-to-end check that
	// the whole pipeline is wired the right way round.
	m := res.Mapping
	cell := res.Cells[m.CellIndex(640, 360, 10)]
	if cell.Count == 0 {
		t.Fatal("the centre cell at depth 10 lists no lights")
	}
	corner := res.Cells[m.CellIndex(5, 5, 400)]
	if corner.Count != 0 {
		t.Errorf("the far top-left corner lists %d lights", corner.Count)
	}
}

// TestBuildDoesNotAllocate is the steady-state requirement: Build runs every
// frame, and a garbage collector is a frame-time problem, not a memory one.
func TestBuildDoesNotAllocate(t *testing.T) {
	p := testParams(DefaultGrid)
	rng := rand.New(rand.NewPCG(31, 37))
	lights := makeLights(rng, 900, p)
	b := New()
	b.Build(lights, p) // grow everything that is allowed to grow once

	if n := testing.AllocsPerRun(20, func() { b.Build(lights, p) }); n != 0 {
		t.Errorf("Build allocated %v times per frame, want 0", n)
	}

	// Over budget takes a different path through the sort and the budget cut.
	many := append(append([]Light(nil), lights...), makeLights(rng, MaxLights, p)...)
	b.Build(many, p)
	if n := testing.AllocsPerRun(20, func() { b.Build(many, p) }); n != 0 {
		t.Errorf("Build over budget allocated %v times per frame, want 0", n)
	}
}

// TestDegenerateParamsDoNotPanic: Build runs inside the render loop, so a bad
// camera has to come out as a useless grid rather than as a NaN scale that
// silently unlights the world or an index out of range that kills the process.
func TestDegenerateParamsDoNotPanic(t *testing.T) {
	lights := []Light{
		{Pos: mgl32.Vec3{0, 0, -10}, Range: 5},
		{Pos: mgl32.Vec3{0, 0, -10}, Range: float32(math.NaN())},
		{Pos: mgl32.Vec3{float32(math.Inf(1)), 0, -10}, Range: 5},
		{Pos: mgl32.Vec3{float32(math.NaN()), 0, -10}, Range: 5},
		{Pos: mgl32.Vec3{0, 0, -10}, Range: 5, Dir: mgl32.Vec3{0, 0, 2}, CosOuter: 0.5},
		{Pos: mgl32.Vec3{0, 0, -10}, Range: 5, Dir: mgl32.Vec3{0, 0, -1}, CosOuter: float32(math.NaN())},
	}
	b := New()
	for _, p := range []Params{
		{},
		{Grid: Grid{X: -1, Y: 0, Z: 3}},
		{Width: 1280, Height: 720, Near: 0, Far: 0, Grid: DefaultGrid,
			Proj: reverseZProjection(60, 1.7, 0.1, 500)},
		{Width: 1280, Height: 720, Near: 10, Far: 1, Grid: DefaultGrid,
			Proj: reverseZProjection(60, 1.7, 0.1, 500)},
		func() Params { p := testParams(DefaultGrid); p.SliceStart = 1e9; return p }(),
		func() Params { p := testParams(DefaultGrid); p.View = mgl32.Mat4{}; return p }(),
		func() Params { p := testParams(DefaultGrid); p.Proj = mgl32.Mat4{}; return p }(),
	} {
		res := b.Build(lights, p)
		checkConsistent(t, res)
		m := res.Mapping
		if math.IsNaN(float64(m.SliceScale)) || math.IsInf(float64(m.SliceScale), 0) ||
			math.IsNaN(float64(m.SliceBias)) || math.IsInf(float64(m.SliceBias), 0) {
			t.Errorf("params %+v gave slice params %v %v", p, m.SliceScale, m.SliceBias)
		}
		for i := 0; i < m.Grid.Cells(); i++ {
			_ = res.Cells[i]
		}
	}

	// A sheared or off-centre projection is not something the binner can bound
	// tightly, so it must widen rather than guess.
	p := testParams(DefaultGrid)
	p.Proj[8] = 0.2 // an off-centre frustum
	res := b.Build([]Light{{Pos: mgl32.Vec3{0, 0, -10}, Range: 1}}, p)
	if res.Stats.ScreenWideLights != 1 {
		t.Errorf("an off-centre projection did not widen the bound: %+v", res.Stats)
	}
}

// TestEmptyInput: no lights is the common case for most of a scene's life.
func TestEmptyInput(t *testing.T) {
	res := New().Build(nil, testParams(DefaultGrid))
	checkConsistent(t, res)
	if len(res.Order) != 0 || len(res.Indices) != 0 {
		t.Errorf("no lights produced %d order and %d indices", len(res.Order), len(res.Indices))
	}
	for _, c := range res.Cells {
		if c.Count != 0 {
			t.Fatal("no lights produced a non-empty cell")
		}
	}
}

// TestViewRadiusScale: the binner moves a world-space bounding sphere into view
// space by transforming its centre, which is only sound if the view matrix
// cannot grow the sphere.
func TestViewRadiusScale(t *testing.T) {
	rigid := mgl32.LookAtV(mgl32.Vec3{3, 4, 5}, mgl32.Vec3{-1, 0, 2}, mgl32.Vec3{0, 1, 0})
	if s := viewRadiusScale(rigid); s != 1 {
		t.Errorf("a look-at view scaled radii by %v, want 1", s)
	}
	scaled := rigid.Mul4(mgl32.Scale3D(2, 2, 2))
	if s := viewRadiusScale(scaled); s < 2 {
		t.Errorf("a 2x scaled view returned %v, which is below the 2 it needs to be conservative", s)
	}
	if s := viewRadiusScale(mgl32.Mat4{}); s != 0 {
		t.Errorf("a zero view returned %v", s)
	}
}
