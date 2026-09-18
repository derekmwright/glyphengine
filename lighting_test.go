package glyphengine

import (
	"math"
	"testing"

	"github.com/go-gl/mathgl/mgl32"

	"github.com/derekmwright/glyphengine/renderer"
	"github.com/derekmwright/glyphengine/renderer/lightcluster"
)

// The tests below were checked against the pre-fix spotLightGpuLight (no
// Inner/Outer clamp, no NaN/range sanitizing, no cosine margin -- just
// dir.Len()==0 || Inner>Outer falling back to omnidirectional, and the raw
// cos() of each angle otherwise): TestSpotLightGpuLightHardEdge,
// TestSpotLightGpuLightInvertedAngles, TestSpotLightGpuLightNaNAngle,
// TestSpotLightGpuLightZeroOuter, and TestSpotLightGpuLightAngleClamping all
// failed against it, with messages matching exactly what each was written to
// catch (an omnidirectional fallback on Inner>=Outer, a NaN reaching the
// packed light, cosInner==cosOuter with no separating margin, and angles
// past [0,pi] reaching cos() unclamped). Restored immediately after.
const spotAngleEpsilon = 1e-5

func almostEqual(a, b, eps float32) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d <= eps
}

// TestSpotLightGpuLightHardEdge checks Inner == Outer: a hard-edged cone,
// not the "no cone" fallback a guard that treats any non-strict inequality
// as inverted would give it. Inner == Outer is the most natural thing a
// caller asking for a crisp-edged spot will write.
func TestSpotLightGpuLightHardEdge(t *testing.T) {
	sl := SpotLight{
		Pos: mgl32.Vec3{1, 2, 3}, Dir: mgl32.Vec3{0, -1, 0}, Range: 10,
		Color: mgl32.Vec3{1, 1, 1},
		Inner: mgl32.DegToRad(30), Outer: mgl32.DegToRad(30),
	}
	g := spotLightGpuLight(sl)

	if g.DirCone[0] == 0 && g.DirCone[1] == 0 && g.DirCone[2] == 0 {
		t.Fatal("hard-edged spot (Inner == Outer) fell back to omnidirectional")
	}
	wantCosOuter := float32(math.Cos(float64(mgl32.DegToRad(30))))
	if !almostEqual(g.DirCone[3], wantCosOuter, spotAngleEpsilon) {
		t.Errorf("cosOuter = %g, want %g", g.DirCone[3], wantCosOuter)
	}
	if g.Color[3] <= g.DirCone[3] {
		t.Errorf("cosInner (%g) must be strictly greater than cosOuter (%g), or the shader's smoothstep is undefined", g.Color[3], g.DirCone[3])
	}
}

// TestSpotLightGpuLightInvertedAngles checks Inner > Outer: clamped to a
// hard edge at Outer's angle, not turned into an omnidirectional point
// light. Falling back to omnidirectional floods the scene, which is the
// opposite of what asking for a narrower inner cone means.
func TestSpotLightGpuLightInvertedAngles(t *testing.T) {
	sl := SpotLight{
		Pos: mgl32.Vec3{0, 0, 0}, Dir: mgl32.Vec3{0, -1, 0}, Range: 10,
		Color: mgl32.Vec3{1, 1, 1},
		Inner: mgl32.DegToRad(40), Outer: mgl32.DegToRad(20),
	}
	g := spotLightGpuLight(sl)

	if g.DirCone[0] == 0 && g.DirCone[1] == 0 && g.DirCone[2] == 0 {
		t.Fatal("Inner > Outer fell back to omnidirectional; it must clamp to a hard edge at Outer instead")
	}
	wantCosOuter := float32(math.Cos(float64(mgl32.DegToRad(20))))
	if !almostEqual(g.DirCone[3], wantCosOuter, spotAngleEpsilon) {
		t.Errorf("cosOuter = %g, want %g (Outer's angle, unaffected by the inverted Inner)", g.DirCone[3], wantCosOuter)
	}
	if g.Color[3] <= g.DirCone[3] {
		t.Errorf("cosInner (%g) must be strictly greater than cosOuter (%g)", g.Color[3], g.DirCone[3])
	}
}

// TestSpotLightGpuLightZeroDir checks that a zero-length Dir produces a
// plain point light (DirCone all zero), matching the documented fallback on
// SpotLight in scene.go.
func TestSpotLightGpuLightZeroDir(t *testing.T) {
	sl := SpotLight{
		Pos: mgl32.Vec3{4, 5, 6}, Dir: mgl32.Vec3{0, 0, 0}, Range: 12,
		Color: mgl32.Vec3{0.5, 0.6, 0.7},
		Inner: mgl32.DegToRad(10), Outer: mgl32.DegToRad(20),
	}
	g := spotLightGpuLight(sl)
	want := renderer.GpuLight{
		PosRange: [4]float32{4, 5, 6, 12},
		Color:    [4]float32{0.5, 0.6, 0.7, 0},
	}
	if g != want {
		t.Errorf("spotLightGpuLight(zero Dir) = %+v, want %+v", g, want)
	}
}

// TestSpotLightGpuLightNonUnitDir checks that Dir is normalized before it
// reaches the GPU -- shaders/lights.inc's dot(dir, -L) assumes a unit
// vector.
func TestSpotLightGpuLightNonUnitDir(t *testing.T) {
	sl := SpotLight{
		Pos: mgl32.Vec3{0, 0, 0}, Dir: mgl32.Vec3{0, 5, 0}, Range: 10,
		Color: mgl32.Vec3{1, 1, 1},
		Inner: mgl32.DegToRad(10), Outer: mgl32.DegToRad(45),
	}
	g := spotLightGpuLight(sl)
	want := [3]float32{0, 1, 0}
	got := [3]float32{g.DirCone[0], g.DirCone[1], g.DirCone[2]}
	if got != want {
		t.Errorf("DirCone.xyz = %v, want unit %v", got, want)
	}
}

// TestSpotLightGpuLightNaNAngle checks that a NaN half-angle, in either
// Inner or Outer, does not propagate into the packed light -- a NaN cosine
// would poison every lit fragment's smoothstep for the life of the light.
func TestSpotLightGpuLightNaNAngle(t *testing.T) {
	nan := float32(math.NaN())
	cases := []SpotLight{
		{Pos: mgl32.Vec3{0, 0, 0}, Dir: mgl32.Vec3{0, -1, 0}, Range: 10, Color: mgl32.Vec3{1, 1, 1}, Inner: nan, Outer: mgl32.DegToRad(30)},
		{Pos: mgl32.Vec3{0, 0, 0}, Dir: mgl32.Vec3{0, -1, 0}, Range: 10, Color: mgl32.Vec3{1, 1, 1}, Inner: mgl32.DegToRad(10), Outer: nan},
	}
	for i, sl := range cases {
		g := spotLightGpuLight(sl)
		if math.IsNaN(float64(g.Color[3])) || math.IsNaN(float64(g.DirCone[3])) {
			t.Fatalf("case %d: NaN half-angle produced a NaN packed light: %+v", i, g)
		}
		if g.Color[3] <= g.DirCone[3] {
			t.Errorf("case %d: cosInner (%g) must be strictly greater than cosOuter (%g)", i, g.Color[3], g.DirCone[3])
		}
	}
}

// TestSpotLightGpuLightZeroOuter checks the degenerate case where Outer is
// (sanitized to) 0: cosOuter is already at the ceiling of 1, so there is no
// room to nudge cosInner above it -- cosOuter has to move down instead, or
// the gap collapses back to zero.
func TestSpotLightGpuLightZeroOuter(t *testing.T) {
	sl := SpotLight{
		Pos: mgl32.Vec3{0, 0, 0}, Dir: mgl32.Vec3{1, 0, 0}, Range: 10,
		Color: mgl32.Vec3{1, 1, 1}, Inner: 0, Outer: 0,
	}
	g := spotLightGpuLight(sl)
	if g.Color[3] != 1 {
		t.Errorf("cosInner = %g, want 1 (Outer's cosine is already the ceiling)", g.Color[3])
	}
	if g.DirCone[3] >= 1 {
		t.Errorf("cosOuter = %g, must be pushed below the ceiling for the gap to survive float32", g.DirCone[3])
	}
	if g.Color[3]-g.DirCone[3] < 1e-5 {
		t.Errorf("cosInner - cosOuter = %g, too small to survive float32 rounding", g.Color[3]-g.DirCone[3])
	}
}

// TestSpotLightGpuLightObtuseOuter checks an outer half-angle past pi/2:
// cos() legitimately goes negative there, and nothing should clamp it back
// into an acute range the way a "half-angle means at most 90 degrees"
// assumption might.
func TestSpotLightGpuLightObtuseOuter(t *testing.T) {
	sl := SpotLight{
		Pos: mgl32.Vec3{0, 0, 0}, Dir: mgl32.Vec3{0, -1, 0}, Range: 10,
		Color: mgl32.Vec3{1, 1, 1}, Inner: 1.0, Outer: 2.5,
	}
	g := spotLightGpuLight(sl)
	want := float32(math.Cos(2.5))
	if !almostEqual(g.DirCone[3], want, spotAngleEpsilon) {
		t.Errorf("cosOuter = %g, want %g (cos of an obtuse half-angle, unclamped)", g.DirCone[3], want)
	}
}

// TestSpotLightGpuLightAngleClamping checks that a negative angle clamps to
// 0 and an angle past pi clamps to pi, rather than either reaching cos()
// unsanitized.
func TestSpotLightGpuLightAngleClamping(t *testing.T) {
	sl := SpotLight{
		Pos: mgl32.Vec3{0, 0, 0}, Dir: mgl32.Vec3{0, -1, 0}, Range: 10,
		Color: mgl32.Vec3{1, 1, 1}, Inner: -1.0, Outer: 10.0,
	}
	g := spotLightGpuLight(sl)
	wantCosOuter := float32(math.Cos(math.Pi)) // Outer sanitizes to pi
	if !almostEqual(g.DirCone[3], wantCosOuter, spotAngleEpsilon) {
		t.Errorf("cosOuter = %g, want %g (Outer clamped to pi)", g.DirCone[3], wantCosOuter)
	}
	wantCosInner := float32(1) // Inner sanitizes to 0, whose cosine is 1
	if !almostEqual(g.Color[3], wantCosInner, spotAngleEpsilon) {
		t.Errorf("cosInner = %g, want %g (Inner clamped to 0)", g.Color[3], wantCosInner)
	}
}

// clusterTestEngine is an Engine with just enough filled in to bin lights: no
// window, no renderer, no GPU. clusterFrameLights takes the framebuffer size
// as an argument precisely so it can be called like this -- the light path is
// the half of the frame that can be tested on a machine with no Vulkan at all.
func clusterTestEngine(points, spots int) *Engine {
	e := &Engine{Scene: NewScene(), lightCluster: lightcluster.New(), near: 0.1, far: 500}

	pl := make([]PointLight, 0, points)
	for i := 0; i < points; i++ {
		a := float64(i) * 0.61803398
		pl = append(pl, PointLight{
			Pos:   mgl32.Vec3{float32(math.Cos(a) * float64(i%37)), 1.5, float32(math.Sin(a) * float64(i%41))},
			Range: 6,
			Color: mgl32.Vec3{1, 0.8, 0.6},
		})
	}
	sl := make([]SpotLight, 0, spots)
	for i := 0; i < spots; i++ {
		a := float64(i) * 0.41421356
		sl = append(sl, SpotLight{
			Pos:   mgl32.Vec3{float32(math.Cos(a) * 12), 5, float32(math.Sin(a) * 12)},
			Dir:   mgl32.Vec3{0, -1, 0},
			Range: 14,
			Color: mgl32.Vec3{1, 0.75, 0.4},
			Inner: mgl32.DegToRad(18),
			Outer: mgl32.DegToRad(32),
		})
	}
	e.Scene.SetPointLights(pl)
	e.Scene.SetSpotLights(sl)
	return e
}

func clusterTestCamera() (view, proj mgl32.Mat4) {
	return mgl32.LookAtV(mgl32.Vec3{0, 3, 18}, mgl32.Vec3{0, 1, 0}, mgl32.Vec3{0, 1, 0}),
		reverseZProjection(60, 1920.0/1080.0, 0.1, 500)
}

// TestClusterFrameLightsDoesNotAllocate is the steady-state requirement on the
// per-frame light path. It runs every frame with however many lights the scene
// submitted, so an allocation here is an allocation per frame that scales with
// the light count -- exactly the cost clustering exists to avoid paying.
//
// Broken on purpose by gathering into a fresh slice (`lights := []GpuLight{}`
// in gatherLights instead of e.lightBuf[:0]): 11 allocs/op for 800 lights.
// The order test below was broken the same way, by uploading in submission
// order instead of res.Order, and reported the first light that moved.
func TestClusterFrameLightsDoesNotAllocate(t *testing.T) {
	e := clusterTestEngine(600, 200)
	view, proj := clusterTestCamera()

	// Warm up: the buffers grow on the first frames and are reused after, and
	// it is the reuse this is measuring.
	uploaded, res := e.clusterFrameLights(view, proj, 1920, 1080)
	if len(uploaded) == 0 || res.Stats.IndexCount == 0 {
		t.Fatalf("nothing was binned, so the measurement below means nothing: %+v", res.Stats)
	}

	if n := testing.AllocsPerRun(20, func() { e.clusterFrameLights(view, proj, 1920, 1080) }); n != 0 {
		t.Errorf("clusterFrameLights allocates %g times per frame, want 0", n)
	}
}

// TestClusterFrameLightsUploadsInBinnerOrder pins the contract between the two
// halves of the light path: the shader's lights[i] must be the light the cell
// lists mean by i. Getting this wrong lights the scene with the right number
// of lights in the wrong places, which is a much harder thing to see than a
// black screen.
//
// It also pins where the cone comes from. spotLightGpuLight can widen cosOuter
// to keep the shader's smoothstep defined, so the cone the shader evaluates is
// not always the one the SpotLight asked for, and it is the shader's cone the
// binner has to bound. Reading the geometry back out of the packed light is
// what makes those the same number; building it from the SpotLight instead
// would bound a cone a hair narrower than the one being drawn, and lose a rim
// of the spot at tile edges.
func TestClusterFrameLightsUploadsInBinnerOrder(t *testing.T) {
	e := clusterTestEngine(40, 8)
	view, proj := clusterTestCamera()
	uploaded, res := e.clusterFrameLights(view, proj, 1280, 720)

	submitted := e.gatherLights()
	if len(res.Order) != len(uploaded) {
		t.Fatalf("Order has %d entries, uploaded %d", len(res.Order), len(uploaded))
	}
	if len(uploaded) == 0 {
		t.Fatal("no lights survived, so the order below is not being checked")
	}
	for i, idx := range res.Order {
		if uploaded[i] != submitted[idx] {
			t.Fatalf("uploaded[%d] is not submitted[%d]", i, idx)
		}
	}

	// Every spot's cone in the binner's input is the packed cone, bit for bit.
	for i := range submitted {
		g := e.lightGeomBuf[i]
		if g.CosOuter != submitted[i].DirCone[3] || g.Dir[0] != submitted[i].DirCone[0] ||
			g.Dir[1] != submitted[i].DirCone[1] || g.Dir[2] != submitted[i].DirCone[2] {
			t.Fatalf("light %d: binner cone %v/%g is not the packed cone %v/%g",
				i, g.Dir, g.CosOuter, submitted[i].DirCone[:3], submitted[i].DirCone[3])
		}
	}
}
