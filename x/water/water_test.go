package water

import (
	"encoding/binary"
	"math"
	"reflect"
	"testing"

	"github.com/go-gl/mathgl/mgl32"
)

// TestDefaultsAreTheSourceConstants pins every default to the number it came
// out of, so a later edit to one of them is a decision rather than a drift.
//
// The whole point of lifting these out of a shader was that a second game can
// change them; the cost of that is that nothing about the first game's look is
// protected by the compiler any more. This is what protects it. The citation
// beside each value is where it was read from, and the set is deliberately
// exhaustive -- a field added to Options without a line here fails the count
// check at the end, because a tunable with no recorded origin is exactly the
// "tuned constant with no measurement behind it" AGENTS.md rule 14 names.
//
// Verified by breaking it, 2026-10-02: see water.md.
func TestDefaultsAreTheSourceConstants(t *testing.T) {
	o := DefaultOptions(12.5)

	if o.Level != 12.5 {
		t.Errorf("Level = %v, want the level passed in", o.Level)
	}

	scalars := []struct {
		name   string
		got    float32
		want   float32
		source string
	}{
		{"Clarity", o.Clarity, 2.0, "WATER_CLARITY in ocean.glsl"},
		{"BodyNightFloor", o.BodyNightFloor, 0.01, "the 0.01 in underwaterColor's fill term"},
		{"BodyDepthFalloff", o.BodyDepthFalloff, 0.025, "exp(-depth*0.025/WATER_CLARITY) in underwaterColor"},
		{"BodyDaylightOnset", o.BodyDaylightOnset, -0.12, "smoothstep(-0.12,0.25,..) in underwaterColor"},
		{"BodyDaylightFull", o.BodyDaylightFull, 0.25, "smoothstep(-0.12,0.25,..) in underwaterColor"},
		{"ScatterSpan", o.ScatterSpan, 60, "min(travel*1000.0,60.0) in underwaterShafts"},
		{"ScatterMaxDepth", o.ScatterMaxDepth, 80, "depth>80.0 early-out in underwaterShafts"},
		{"ScatterPhase", o.ScatterPhase, 0.65, "g=0.65 in underwaterShafts"},
		{"ScatterDaylightOnset", o.ScatterDaylightOnset, 0.08, "smoothstep(0.08,0.4,..) in underwaterShafts"},
		{"ScatterDaylightFull", o.ScatterDaylightFull, 0.4, "smoothstep(0.08,0.4,..) in underwaterShafts"},
		{"RefractiveIndex", o.RefractiveIndex, 1.333, "the 1.0/1.333 in every refract() in the source"},
		{"SunPathFloor", o.SunPathFloor, 0.15, "max(dot(waterSun,up),0.15) in underwaterShafts"},
		{"ScatterScale", o.ScatterScale, 0.5, "Scale: 0.5 on the source's underwater light target"},
		{"DepthTolerance", o.DepthTolerance, 0.08, "max(depth*0.08,..) in the source's water composite"},
	}
	for _, c := range scalars {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v (%s)", c.name, c.got, c.want, c.source)
		}
	}

	if o.ScatterSamples != 6 {
		t.Errorf("ScatterSamples = %d, want 6 (the source's uncached sample count; 12 is its cached path's, and the cache is not in this package)", o.ScatterSamples)
	}

	colors := []struct {
		name   string
		got    [3]float32
		want   [3]float32
		source string
	}{
		{"Absorption", o.Absorption, [3]float32{0.20, 0.075, 0.035}, "WATER_ABSORPTION's numerator, per metre"},
		{"BodyColor", o.BodyColor, [3]float32{0.006, 0.035, 0.047}, "vec3(0.006,0.035,0.047) in underwaterColor"},
		{"ScatterColor", o.ScatterColor, [3]float32{0.0002, 0.0006, 0.0008}, "vec3(0.0002,0.0006,0.0008) in underwaterShafts"},
	}
	for _, c := range colors {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v (%s)", c.name, c.got, c.want, c.source)
		}
	}

	// A field nobody pinned is a constant nobody can defend. Count the struct's
	// fields and require every one of them to appear above, so adding a tunable
	// without recording where it came from fails here.
	pinned := 1 + len(scalars) + 1 + len(colors) // Level, scalars, ScatterSamples, colours
	if fields := optionsFieldCount(); fields != pinned {
		t.Errorf("Options has %d fields but %d are pinned; add the new one to this test with the number's source", fields, pinned)
	}
}

func optionsFieldCount() int { return reflect.TypeOf(Options{}).NumField() }

// TestValidateRejectsAnUnusableOptions covers the fields whose zero value would
// divide by nothing or march zero samples. New returns these before touching the
// renderer, so a game gets a named field rather than an empty frame.
func TestValidateRejectsAnUnusableOptions(t *testing.T) {
	if err := (Options{}).validate(); err == nil {
		t.Fatal("the zero Options validated; it divides by Clarity 0")
	}
	cases := map[string]func(*Options){
		"Clarity":             func(o *Options) { o.Clarity = 0 },
		"ScatterSpan":         func(o *Options) { o.ScatterSpan = -1 },
		"ScatterScale":        func(o *Options) { o.ScatterScale = 0 },
		"RefractiveIndex":     func(o *Options) { o.RefractiveIndex = 0 },
		"SunPathFloor":        func(o *Options) { o.SunPathFloor = 0 },
		"DepthTolerance":      func(o *Options) { o.DepthTolerance = 0 },
		"ScatterSamples":      func(o *Options) { o.ScatterSamples = 0 },
		"ScatterMaxDepth":     func(o *Options) { o.ScatterMaxDepth = -1 },
		"ScatterDaylightFull": func(o *Options) { o.ScatterDaylightFull = o.ScatterDaylightOnset },
		"BodyDaylightFull":    func(o *Options) { o.BodyDaylightFull = o.BodyDaylightOnset - 1 },
	}
	for field, breakIt := range cases {
		o := DefaultOptions(0)
		breakIt(&o)
		err := o.validate()
		if err == nil {
			t.Errorf("%s: broken value validated", field)
			continue
		}
		if !contains(err.Error(), field) {
			t.Errorf("%s: error does not name the field: %v", field, err)
		}
	}
	if err := DefaultOptions(0).validate(); err != nil {
		t.Fatalf("the defaults do not validate: %v", err)
	}
}

// TestPackIsInactiveAboveTheSurface is the package's central promise: out of the
// water it contributes nothing, so a frame with it created is the same frame.
//
// pack is what Update calls to decide, and a false return is what switches all
// three passes off. The `task xwater` gate proves the pixels; this proves the
// decision, including the boundary, where the eye is exactly at the surface.
func TestPackIsInactiveAboveTheSurface(t *testing.T) {
	w := &Water{opts: DefaultOptions(10), enabled: true}
	vp := testViewProjection(mgl32.Vec3{0, 5, 0})
	for _, c := range []struct {
		eyeY float32
		want bool
	}{
		{20, false}, // well above
		{10.001, false},
		{10, false},   // exactly at the surface: zero water, nothing to show
		{9.999, true}, // just under
		{5, true},
	} {
		got := w.pack(vp, mgl32.Vec3{0, c.eyeY, 0}, [3]float32{0, 1, 0}, [3]float32{1, 1, 1}, 64, 36)
		if got != c.want {
			t.Errorf("eye Y %v: active = %v, want %v", c.eyeY, got, c.want)
		}
	}
	// Intent still gates it.
	w.enabled = false
	if w.pack(vp, mgl32.Vec3{0, 5, 0}, [3]float32{0, 1, 0}, [3]float32{1, 1, 1}, 64, 36) {
		t.Error("pack reported active with SetEnabled(false)")
	}
}

// TestPackedScatterBlockMatchesTheShaderLayout reads the bytes back the way
// water-scatter.frag declares them, because the push block is an interface
// between Go and SPIR-V that nothing else checks. A field swapped with its
// neighbour compiles, links, renders, and renders nonsense.
func TestPackedScatterBlockMatchesTheShaderLayout(t *testing.T) {
	o := DefaultOptions(10)
	w := &Water{opts: o, enabled: true}
	eye := mgl32.Vec3{3, 4, 5} // 6 units under the surface
	vp := testViewProjection(eye)
	sun := [3]float32{0, 1, 0} // straight overhead: no bending, no path stretch
	if !w.pack(vp, eye, sun, [3]float32{1, 0.9, 0.8}, 640, 360) {
		t.Fatal("pack reported inactive for a submerged camera in daylight")
	}
	f := floats(w.scatterPush[:])

	for i := 0; i < 16; i++ {
		if f[i] != vp[i] {
			t.Fatalf("inverseVP[%d] = %v, want %v", i, f[i], vp[i])
		}
	}
	// A sun at the zenith is not bent at all, so the vector pointing at the
	// refracted sun is still straight up and the path stretch is exactly 1 --
	// which makes the whole refraction arithmetic checkable by hand.
	near(t, "waterSun.y", f[17], 1)
	near(t, "sunPathScale", f[19], 1)

	day := smoothstep(o.ScatterDaylightOnset, o.ScatterDaylightFull, 1)
	near(t, "shaft light r", f[20], 1*o.ScatterColor[0]*day)
	near(t, "shaft light g", f[21], 0.9*o.ScatterColor[1]*day)
	near(t, "shaft light b", f[22], 0.8*o.ScatterColor[2]*day)
	near(t, "level", f[23], o.Level)

	near(t, "absorption r", f[24], o.Absorption[0]/o.Clarity)
	near(t, "absorption g", f[25], o.Absorption[1]/o.Clarity)
	near(t, "absorption b", f[26], o.Absorption[2]/o.Clarity)
	near(t, "phase g", f[27], o.ScatterPhase)

	near(t, "span", f[28], o.ScatterSpan)
	near(t, "samples", f[29], float32(o.ScatterSamples))
	near(t, "target width", f[30], 640)
	near(t, "target height", f[31], 360)

	c := floats(w.compositePush[:])
	for i := 0; i < 16; i++ {
		if c[i] != vp[i] {
			t.Fatalf("composite inverseVP[%d] = %v, want %v", i, c[i], vp[i])
		}
	}
	near(t, "composite absorption r", c[16], o.Absorption[0]/o.Clarity)
	near(t, "composite level", c[19], o.Level)
	bodyDay := smoothstep(o.BodyDaylightOnset, o.BodyDaylightFull, 1)
	reach := o.BodyNightFloor + bodyDay*expf(-6*o.BodyDepthFalloff/o.Clarity)
	near(t, "body radiance r", c[20], o.BodyColor[0]*reach)
	near(t, "body radiance g", c[21], o.BodyColor[1]*reach)
	near(t, "body radiance b", c[22], o.BodyColor[2]*reach)
	near(t, "depth tolerance", c[23], o.DepthTolerance)
	for i := 24; i < 32; i++ {
		if c[i] != 0 {
			t.Errorf("composite tail float %d = %v, want 0", i, c[i])
		}
	}
}

// TestShaftLightIsZeroWhereTheSourceReturnsZero pins the two early-outs that
// moved from the shader to the CPU. They have to produce exactly zero, because
// "an additive term that is zero everywhere is the same frame as no term" is the
// argument for hoisting them at all; a near-zero would be a quiet change to
// every deep or night frame.
func TestShaftLightIsZeroWhereTheSourceReturnsZero(t *testing.T) {
	o := DefaultOptions(0)
	w := &Water{opts: o, enabled: true}
	vp := testViewProjection(mgl32.Vec3{})
	noon := [3]float32{0, 1, 0}
	white := [3]float32{1, 1, 1}

	// Deeper than ScatterMaxDepth: the source's `if(depth>80.0) return vec3(0)`.
	w.pack(vp, mgl32.Vec3{0, -o.ScatterMaxDepth - 0.5, 0}, noon, white, 64, 36)
	shaftsZero(t, w, "below ScatterMaxDepth")
	// Exactly at it is still lit, matching the source's strict comparison.
	w.pack(vp, mgl32.Vec3{0, -o.ScatterMaxDepth, 0}, noon, white, 64, 36)
	if floats(w.scatterPush[:])[22] == 0 {
		t.Error("at exactly ScatterMaxDepth the shafts are off; the source's test is depth > max, not >=")
	}
	// Sun at or below the ramp's onset: the source's `if(day<=0.0) return vec3(0)`.
	// A unit vector whose Y is exactly the ramp's onset. Passing {0, onset, 0}
	// would normalize to the zenith and silently test noon instead, which is
	// what the first version of this did.
	onset := mgl32.Vec3{sqrtf(1 - o.ScatterDaylightOnset*o.ScatterDaylightOnset), o.ScatterDaylightOnset, 0}
	w.pack(vp, mgl32.Vec3{0, -5, 0}, [3]float32{onset.X(), onset.Y(), onset.Z()}, white, 64, 36)
	shaftsZero(t, w, "sun at the daylight onset")
	w.pack(vp, mgl32.Vec3{0, -5, 0}, [3]float32{0, -1, 0}, white, 64, 36)
	shaftsZero(t, w, "sun below the horizon")

	// The body term is NOT zero at night, which is what BodyNightFloor is for:
	// a night dive is dark, not black. If this ever reads zero the hoist above
	// has leaked into the wrong term.
	c := floats(w.compositePush[:])
	if c[20] == 0 || c[21] == 0 || c[22] == 0 {
		t.Errorf("body radiance %v %v %v is zero at night; BodyNightFloor should survive", c[20], c[21], c[22])
	}
}

func shaftsZero(t *testing.T, w *Water, when string) {
	t.Helper()
	f := floats(w.scatterPush[:])
	for i, name := range []string{"r", "g", "b"} {
		if f[20+i] != 0 {
			t.Errorf("%s: shaft light %s = %v, want exactly 0", when, name, f[20+i])
		}
	}
}

// TestRefractedSunMatchesGLSL checks the Go refract against hand-computed
// values, because it is the one piece of shader arithmetic that moved to the CPU
// and so the one piece no shader compiler is checking any more.
func TestRefractedSunMatchesGLSL(t *testing.T) {
	up := mgl32.Vec3{0, 1, 0}
	eta := float32(1.0 / 1.333)

	// Straight down stays straight down.
	got := refract(mgl32.Vec3{0, -1, 0}, up, eta)
	near(t, "normal incidence y", got.Y(), -1)
	near(t, "normal incidence x", got.X(), 0)

	// 45 degrees: Snell gives sin(t) = sin(45)/1.333 = 0.5304627, so the
	// horizontal component of the unit refracted ray is that, and the vertical
	// is -sqrt(1 - 0.5304627^2) = -0.8477083.
	in := mgl32.Vec3{1, -1, 0}.Normalize()
	got = refract(in, up, eta)
	near(t, "45 degrees horizontal", got.X(), 0.5304627)
	near(t, "45 degrees vertical", got.Y(), -0.8477083)
	near(t, "45 degrees length", got.Len(), 1)

	// Entering a denser medium can never total-internally-reflect, so refract
	// must not return the zero vector for any incidence. A zero here would send
	// the shader a NaN path scale.
	for _, angle := range []float64{0.01, 0.5, 1.0, 1.5, 1.5707} {
		d := mgl32.Vec3{float32(math.Sin(angle)), -float32(math.Cos(angle)), 0}
		if refract(d, up, eta).Len() == 0 {
			t.Errorf("refract returned zero at %.4f rad entering water", angle)
		}
	}
}

// TestUpdateAllocatesNothing covers the Go half of "zero allocations per frame".
//
// pack is the only place in the per-frame path where a Go allocation could
// appear: everything it touches is a fixed array on the Water, and
// AppPass.SetPushConstants copies out of those arrays into a fixed array of its
// own with no allocation on the success path. The gate's -allocs mode measures a
// real frame loop with a real renderer, which is the half this cannot see.
func TestUpdateAllocatesNothing(t *testing.T) {
	w := &Water{opts: DefaultOptions(10), enabled: true}
	vp := testViewProjection(mgl32.Vec3{0, 4, 0})
	eye := mgl32.Vec3{0, 4, 0}
	sun := [3]float32{0.3, 0.8, 0.5}
	col := [3]float32{1, 0.95, 0.9}
	if n := testing.AllocsPerRun(200, func() {
		w.pack(vp, eye, sun, col, 640, 360)
	}); n != 0 {
		t.Errorf("pack allocates %v times per frame, want 0", n)
	}
}

// testViewProjection builds the engine's reverse-Z view-projection for a camera
// at eye looking down -Z, then inverts it -- so the matrices these tests feed
// pack are the ones Engine.ViewProjection().Inv() actually produces rather than
// an identity that would hide a transposition.
func testViewProjection(eye mgl32.Vec3) mgl32.Mat4 {
	const near, far = 0.1, 300
	proj := mgl32.Perspective(mgl32.DegToRad(50), 16.0/9.0, near, far)
	proj[10] = near / (far - near)
	proj[14] = (far * near) / (far - near)
	proj[5] *= -1
	view := mgl32.LookAtV(eye, eye.Add(mgl32.Vec3{0, 0, -1}), mgl32.Vec3{0, 1, 0})
	return proj.Mul4(view).Inv()
}

func floats(b []byte) []float32 {
	out := make([]float32, len(b)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return out
}

func near(t *testing.T, name string, got, want float32) {
	t.Helper()
	if math.Abs(float64(got-want)) > 1e-5*math.Max(1, math.Abs(float64(want))) {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
