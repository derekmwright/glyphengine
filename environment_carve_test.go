package glyphengine

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/go-gl/mathgl/mgl32"

	"github.com/derekmwright/glyphengine/renderer"
)

// staticCases are the fixed-light configurations the pins below were taken from:
// generated out of the engine's Environment.State as it stood before the
// environment contract was carved, printed at full float32 precision, and pasted
// in unedited. They are not re-derivations of the rules, they are what the engine
// actually produced -- which is the only thing that makes "nothing moved" a claim
// rather than a hope.
//
// Three cases, where there were eight. The other five had a Sky in them and went
// to x/sky with the dome; they are fixedSkyCases there, pinned against the same
// generation. What is left is the whole of the environment the engine itself
// resolves, which is the point of the split: these are the only fixed-light rules
// that remain in one place, and x/sky's fixed-hour path calls them rather than
// repeating them.
var staticCases = []struct {
	name string
	src  StaticSource
}{
	{"bare", StaticSource{}},
	{"sun+ambient", StaticSource{
		Sun:     &DirectionalLight{Direction: [3]float32{0.4, 0.8, 0.3}, Color: [3]float32{0.7, 0.68, 0.62}},
		Ambient: &AmbientLight{Color: [3]float32{0.18, 0.17, 0.20}},
	}},
	{"interior", StaticSource{
		Ambient:    &AmbientLight{Color: [3]float32{0.18, 0.17, 0.20}},
		ClearColor: [3]float32{0.03, 0.03, 0.045},
	}},
}

// staticPins is what each case above resolved to before the carve.
var staticPins = []struct {
	name  string
	state EnvironmentState
}{
	{"bare", EnvironmentState{}},
	{"sun+ambient", EnvironmentState{SunDir: [3]float32{0.4, 0.8, 0.3}, SunColor: [3]float32{0.7, 0.68, 0.62}, RealSunDir: [3]float32{0.4, 0.8, 0.3}, Ambient: [3]float32{0.18, 0.17, 0.2}, CastShadows: true}},
	{"interior", EnvironmentState{Ambient: [3]float32{0.18, 0.17, 0.2}, ClearColor: [3]float32{0.03, 0.03, 0.045}}},
}

// diffState names the fields two states disagree on. A %+v of an
// EnvironmentState is four lines of mostly identical numbers, and the whole
// value of a pin is being told which one moved.
func diffState(got, want EnvironmentState) []string {
	var out []string
	g, w := reflect.ValueOf(got), reflect.ValueOf(want)
	for i := 0; i < g.NumField(); i++ {
		if g.Field(i).Interface() != w.Field(i).Interface() {
			out = append(out, fmt.Sprintf("%s: got %v want %v",
				g.Type().Field(i).Name, g.Field(i).Interface(), w.Field(i).Interface()))
		}
	}
	return out
}

// TestStaticSourceIsUnchanged is the proof for the fixed-light path: the
// configuration that survives into the engine once the sky leaves.
//
// It mattered for the carve, and it matters more now. StaticSource stopped
// sharing a resolver with the day cycle in this change -- the shared envAir and
// staticState went to x/sky with the dome -- so these three states are produced
// by code that was rewritten rather than merely moved, and x/sky's fixed-hour
// path calls into it. A rule dropped here is a rule missing from both.
//
// Verified to fail: dropping the RealSunDir assignment, which is the one line
// whose absence no capture of a fixed-light scene would show -- nothing in a
// scene with no dome reads it -- reports `StaticSource "sun+ambient" moved:` and
// `RealSunDir: got [0 0 0] want [0.4 0.8 0.3]`.
func TestStaticSourceIsUnchanged(t *testing.T) {
	if len(staticCases) != len(staticPins) {
		t.Fatalf("%d static cases against %d pins", len(staticCases), len(staticPins))
	}
	for i, c := range staticCases {
		p := staticPins[i]
		if c.name != p.name {
			t.Fatalf("case %d is %q and its pin is %q", i, c.name, p.name)
		}
		src := c.src
		if got := src.State(); got != p.state {
			t.Errorf("StaticSource %q moved:\n  %s", c.name, strings.Join(diffState(got, p.state), "\n  "))
		}
	}

	// And it draws no dome, whatever else it says. That is the engine's half of
	// the migration: with no sky package there is no sky, so a frame with nothing
	// in it is the clear colour rather than a gradient.
	for _, c := range staticCases {
		src := c.src
		st := src.State()
		if st.DrawSky || st.DrawStars || st.DrawSun || st.DrawMoon || st.CloudSteps != 0 || st.LightShafts != 0 {
			t.Errorf("StaticSource %q asked for something to be drawn in the sky: %+v", c.name, st)
		}
	}
}

// TestCelestialDiscsAreColouredFromTheState is the half of the carve's
// moon-colour test that belongs to the engine: whatever the source decided, the
// billboard is drawn in it.
//
// It is the half that would fail silently. The arithmetic in a sky package could
// be perfect and buildMoonObject could still be colouring the disc from
// something of its own -- the three constants and the horizon fade it held until
// the carve -- and a night capture is the only other thing that would say so.
//
// The colours here are deliberately not a cycle's. The carve's version read them
// off a pinned table of real moon colours and was weaker for it: a disc coloured
// from a second copy of the same curve would have passed. Values no curve would
// produce is what makes "it came from the state" the only way to get them.
//
// Verified to fail: colouring the moon {0.85, 0.88, 0.95} again, which is what
// buildMoonObject did before the carve, reports `the moon was drawn
// [0.85 0.88 0.95] with the state saying [0.31 0.32 0.33]` -- and takes
// TestCustomSourceReachesEveryReader with it, which is the other half of the same
// claim from the other direction.
func TestCelestialDiscsAreColouredFromTheState(t *testing.T) {
	// far and cameraEye are all the billboard needs; no renderer is involved.
	e := &Engine{far: 500}
	st := EnvironmentState{
		SunDiscDir:    [3]float32{0.1, 0.9, 0.2},
		SunDiscColor:  [3]float32{4.1, 0.37, 2.9},
		MoonDiscDir:   [3]float32{-0.1, -0.9, 0.2},
		MoonDiscColor: [3]float32{0.31, 0.32, 0.33},
	}
	if got := e.buildSunObject(mgl32.Ident4(), st).Color; got != st.SunDiscColor {
		t.Errorf("the sun was drawn %v with the state saying %v", got, st.SunDiscColor)
	}
	if got := e.buildMoonObject(mgl32.Ident4(), st).Color; got != st.MoonDiscColor {
		t.Errorf("the moon was drawn %v with the state saying %v", got, st.MoonDiscColor)
	}
}

// customState is a state with every field set to something no built-in source
// would produce, so that "the custom value reached the reader" cannot be true by
// coincidence. Nothing here is physically sensible, deliberately.
func customState() EnvironmentState {
	return EnvironmentState{
		SunDir:          [3]float32{0.11, 0.12, 0.13},
		SunColor:        [3]float32{0.21, 0.22, 0.23},
		RealSunDir:      [3]float32{0.31, 0.32, 0.33},
		SunElevation:    0.41,
		Ambient:         [3]float32{0.51, 0.52, 0.53},
		FogDensity:      0.61,
		FogHeight:       0.62,
		FogBaseHeight:   0.63,
		ClearColor:      [3]float32{0.71, 0.72, 0.73},
		StarFade:        0.81,
		MilkyWay:        0.82,
		StarDensity:     0.83,
		DrawSky:         true,
		DrawStars:       true,
		DrawSun:         true,
		DrawMoon:        true,
		SunDiscDir:      [3]float32{0.91, 0.92, 0.93},
		SunDiscColor:    [3]float32{1.01, 1.02, 1.03},
		MoonDiscDir:     [3]float32{1.11, 1.12, 1.13},
		MoonDiscColor:   [3]float32{1.21, 1.22, 1.23},
		CloudSteps:      23,
		Cirrus:          0.37,
		LightShafts:     0.47,
		LightShaftShape: LightShaftShape{Radius: 1.31, Decay: 0.91, Threshold: [2]float32{0.33, 0.55}},
		CastShadows:     true,
		SkyPalette: SkyPalette{
			ZenithDay:       mgl32.Vec3{0.301, 0.101, 0.621},
			HorizonDay:      mgl32.Vec3{0.951, 0.551, 0.221},
			ZenithTwilight:  mgl32.Vec3{0.181, 0.041, 0.301},
			HorizonTwilight: mgl32.Vec3{0.951, 0.221, 0.301},
			ZenithNight:     mgl32.Vec3{0.0041, 0.0013, 0.0061},
			HorizonNight:    mgl32.Vec3{0.0111, 0.0036, 0.0056},
		},
		NightGrade: NightGrade{Strength: 0.37, Tint: mgl32.Vec3{0.71, 0.85, 1.29}},
	}
}

// TestCustomSourceReachesEveryReader is the seam's real test: one assertion per
// field of EnvironmentState, from a source the engine knows nothing about to the
// thing that consumes it.
//
// The lighting pack is where most of them land, and renderer/commands_test.go and
// renderer/litubo_test.go carry it the rest of the way into the push block and
// the uniform buffer. The four disc fields and the two shadow and shaft
// decisions are the ones that do not go through the pack, and they are checked
// where they do go.
//
// Verified to fail: pointing applyEnvironment back at the scene for the palette
// -- `p := e.Scene.SkyPalette()` and packing that, which is where it read from
// before the carve -- fails with
//
//	SkyPalette.ZenithDay = [0.13 0.3 0.78], want [0.301 0.101 0.621]
//
// while every other field still passes, which is exactly the shape of the bug:
// one reader left behind, and a custom sky hazing into Earth-blue.
func TestCustomSourceReachesEveryReader(t *testing.T) {
	want := customState()

	s := NewScene()
	s.Env = &fakeEnv{state: want}
	got := s.Environment()
	if got != want {
		t.Fatalf("the scene did not hand the frame what the source returned:\n  %s", strings.Join(diffState(got, want), "\n  "))
	}

	e := &Engine{far: 500, Scene: s}
	var l renderer.SceneLighting
	e.applyEnvironment(&l, got)

	check := func(name string, got, want any) {
		t.Helper()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
	check("SceneLighting.SunDir", l.SunDir, want.SunDir)
	check("SceneLighting.SunColor", l.SunColor, want.SunColor)
	check("SceneLighting.RealSunDir", l.RealSunDir, want.RealSunDir)
	check("SceneLighting.SunElevation", l.SunElevation, want.SunElevation)
	check("SceneLighting.Ambient", l.Ambient, want.Ambient)
	check("SceneLighting.FogDensity", l.FogDensity, want.FogDensity)
	check("SceneLighting.FogHeight", l.FogHeight, want.FogHeight)
	check("SceneLighting.FogBaseHeight", l.FogBaseHeight, want.FogBaseHeight)
	// ClearColor becomes SkyColor, with an opaque alpha the renderer requires.
	check("SceneLighting.SkyColor", l.SkyColor, [4]float32{want.ClearColor[0], want.ClearColor[1], want.ClearColor[2], 1})
	check("SceneLighting.NightFactor", l.NightFactor, want.StarFade)
	check("SceneLighting.MilkyWay", l.MilkyWay, want.MilkyWay)
	check("SceneLighting.StarDensity", l.StarDensity, want.StarDensity)
	check("SceneLighting.DrawSky", l.DrawSky, want.DrawSky)
	check("SceneLighting.DrawStars", l.DrawStars, want.DrawStars)
	check("SceneLighting.CloudSteps", l.CloudSteps, want.CloudSteps)
	check("SceneLighting.Cirrus", l.Cirrus, want.Cirrus)
	check("SceneLighting.ShaftShape", l.ShaftShape, renderer.LightShaftShape{
		Radius: want.LightShaftShape.Radius, Decay: want.LightShaftShape.Decay, Threshold: want.LightShaftShape.Threshold,
	})

	if l.SkyPalette == nil || l.NightGrade == nil {
		t.Fatal("the lighting pack has no palette or no grade, so the renderer would use its own defaults")
	}
	check("SkyPalette.ZenithDay", l.SkyPalette.ZenithDay, [3]float32(want.SkyPalette.ZenithDay))
	check("SkyPalette.HorizonDay", l.SkyPalette.HorizonDay, [3]float32(want.SkyPalette.HorizonDay))
	check("SkyPalette.ZenithTwilight", l.SkyPalette.ZenithTwilight, [3]float32(want.SkyPalette.ZenithTwilight))
	check("SkyPalette.HorizonTwilight", l.SkyPalette.HorizonTwilight, [3]float32(want.SkyPalette.HorizonTwilight))
	check("SkyPalette.ZenithNight", l.SkyPalette.ZenithNight, [3]float32(want.SkyPalette.ZenithNight))
	check("SkyPalette.HorizonNight", l.SkyPalette.HorizonNight, [3]float32(want.SkyPalette.HorizonNight))
	check("NightGrade.Strength", l.NightGrade.Strength, want.NightGrade.Strength)
	check("NightGrade.Tint", l.NightGrade.Tint, [3]float32(want.NightGrade.Tint))

	// The six that do not travel in the pack. A billboard sits along its
	// direction from the camera, so the translation column, normalized, is the
	// direction the state asked for.
	// The direction comes back through a normalize and a scale, so it is checked
	// to within a float32 epsilon rather than exactly; the colours are copied
	// and are checked exactly.
	billboardDir := func(name string, model [16]float32, want [3]float32) {
		t.Helper()
		got := mgl32.Vec3{model[12], model[13], model[14]}.Normalize()
		w := mgl32.Vec3(want).Normalize()
		for i := range got {
			if !almostEqual(got[i], w[i], 1e-6) {
				t.Errorf("%s = %v, want %v", name, got, w)
				return
			}
		}
	}
	billboardDir("the sun disc's position", e.buildSunObject(mgl32.Ident4(), got).Model, want.SunDiscDir)
	check("the sun disc's colour", e.buildSunObject(mgl32.Ident4(), got).Color, want.SunDiscColor)
	billboardDir("the moon disc's position", e.buildMoonObject(mgl32.Ident4(), got).Model, want.MoonDiscDir)
	check("the moon disc's colour", e.buildMoonObject(mgl32.Ident4(), got).Color, want.MoonDiscColor)
	// DrawSun, DrawMoon and CastShadows are branches in renderFrame rather than
	// values, so what is checked is that the branch reads the state. Taking them
	// away has to change what the frame does.
	if !got.DrawSun || !got.DrawMoon {
		t.Error("DrawSun/DrawMoon did not survive the resolve, so no celestial would be built")
	}
	if !got.CastShadows {
		t.Error("CastShadows did not survive the resolve, so ShadowEnabled would be false")
	}
	// LightShafts reaches the pack through the edge fade, which needs a camera.
	// Zero in, zero out is the half that matters -- a zero keeps the frame out of
	// the water pass entirely -- and the fade itself is covered by task shafts.
	if want.LightShafts == 0 {
		t.Fatal("customState has no shafts to carry")
	}
	if shaftEdgeFade([2]float32{0.5, 0.5}) != 1 {
		t.Error("the edge fade is not 1 with the sun centred, so LightShafts cannot reach the pass unchanged")
	}
}

// envStateReaders names what reads each field of EnvironmentState. It is the
// list TestCustomSourceReachesEveryReader asserts against, written out so that
// the next field added to the state has to be routed somewhere before the tests
// pass.
//
// That is the failure this guards: a field in the state that nothing reads is
// invisible. The built-in sources mostly leave such a field at zero, so no
// capture moves, and the one game that sets it quietly gets nothing.
var envStateReaders = map[string]string{
	"SunDir":          "SceneLighting.SunDir, and ComputeCascadeVPsWithCoverage",
	"SunColor":        "SceneLighting.SunColor",
	"RealSunDir":      "SceneLighting.RealSunDir",
	"SunElevation":    "SceneLighting.SunElevation",
	"Ambient":         "SceneLighting.Ambient",
	"FogDensity":      "SceneLighting.FogDensity",
	"FogHeight":       "SceneLighting.FogHeight",
	"FogBaseHeight":   "SceneLighting.FogBaseHeight",
	"ClearColor":      "SceneLighting.SkyColor",
	"StarFade":        "SceneLighting.NightFactor",
	"MilkyWay":        "SceneLighting.MilkyWay",
	"StarDensity":     "SceneLighting.StarDensity",
	"DrawSky":         "SceneLighting.DrawSky",
	"DrawStars":       "SceneLighting.DrawStars",
	"DrawSun":         "renderFrame, which builds the sun billboard or does not",
	"DrawMoon":        "renderFrame, which builds the moon billboard or does not",
	"SunDiscDir":      "buildSunObject, and the shaft anchor renderFrame projects",
	"SunDiscColor":    "buildSunObject",
	"MoonDiscDir":     "buildMoonObject",
	"MoonDiscColor":   "buildMoonObject",
	"CloudSteps":      "SceneLighting.CloudSteps",
	"Cirrus":          "SceneLighting.Cirrus",
	"LightShafts":     "SceneLighting.LightShafts, after renderFrame's edge fade",
	"LightShaftShape": "SceneLighting.ShaftShape",
	"CastShadows":     "SceneLighting.ShadowEnabled, if the cascades could be built",
	"SkyPalette":      "SceneLighting.SkyPalette",
	"NightGrade":      "SceneLighting.NightGrade",
}

// TestEveryEnvironmentStateFieldHasAReader fails if the state grows a field that
// envStateReaders does not account for, or keeps a name the map still claims.
//
// Verified to fail both ways: adding an unused `Exposure float32` to
// EnvironmentState reports `EnvironmentState.Exposure has no reader recorded in
// envStateReaders`, and deleting the MoonDiscColor entry reports
// `envStateReaders names MoonDiscColor, which EnvironmentState does not have`.
func TestEveryEnvironmentStateFieldHasAReader(t *testing.T) {
	st := reflect.TypeOf(EnvironmentState{})
	seen := map[string]bool{}
	for i := 0; i < st.NumField(); i++ {
		name := st.Field(i).Name
		seen[name] = true
		if _, ok := envStateReaders[name]; !ok {
			t.Errorf("EnvironmentState.%s has no reader recorded in envStateReaders", name)
		}
	}
	var stale []string
	for name := range envStateReaders {
		if !seen[name] {
			stale = append(stale, name)
		}
	}
	sort.Strings(stale)
	for _, name := range stale {
		t.Errorf("envStateReaders names %s, which EnvironmentState does not have", name)
	}
}

// envLeaf is one settable scalar inside an EnvironmentState, with the path that
// reaches it.
type envLeaf struct {
	path string
	v    reflect.Value
}

// envLeaves walks a settable EnvironmentState down to its scalars.
func envLeaves(v reflect.Value, path string, out *[]envLeaf) {
	switch v.Kind() {
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			envLeaves(v.Field(i), path+"."+v.Type().Field(i).Name, out)
		}
	case reflect.Array:
		for i := 0; i < v.Len(); i++ {
			envLeaves(v.Index(i), fmt.Sprintf("%s[%d]", path, i), out)
		}
	default:
		*out = append(*out, envLeaf{path, v})
	}
}

// TestEnvTraceCoversEveryField is what keeps `env=` honest: every scalar in the
// state has to move the hash.
//
// The field it replaced hashed nine of the state's twenty-seven, so a source
// that changed the cirrus, the Milky Way, a disc colour or the palette produced
// a different frame and an identical trace line. `task determinism` compares
// this field between two runs, and a hash that does not cover what moved is a
// gate reporting that nothing moved.
//
// Verified to fail: deleting `Float32(env.Cirrus)` from hashEnvironment reports
// `.Cirrus: hashEnvironment did not notice 0.37 becoming 1.37`.
func TestEnvTraceCoversEveryField(t *testing.T) {
	base := customState()
	var leaves []envLeaf
	ref := reflect.ValueOf(&base).Elem()
	envLeaves(ref, "", &leaves)
	if len(leaves) < 27 {
		t.Fatalf("walked only %d scalars out of EnvironmentState; the walk is wrong", len(leaves))
	}

	want := hashEnvironment(base)
	for _, leaf := range leaves {
		before := leaf.v.Interface()
		switch leaf.v.Kind() {
		case reflect.Float32:
			leaf.v.SetFloat(leaf.v.Float() + 1)
		case reflect.Bool:
			leaf.v.SetBool(!leaf.v.Bool())
		case reflect.Int:
			leaf.v.SetInt(leaf.v.Int() + 1)
		default:
			t.Fatalf("%s is a %s, which this test does not know how to move", leaf.path, leaf.v.Kind())
		}
		if hashEnvironment(base) == want {
			t.Errorf("%s: hashEnvironment did not notice %v becoming %v", leaf.path, before, leaf.v.Interface())
		}
		leaf.v.Set(reflect.ValueOf(before))
	}
	if hashEnvironment(base) != want {
		t.Fatal("the walk did not restore the state, so the results above are not independent")
	}
}

// TestEnvironmentResolvesWithoutAllocating is the per-frame cost of the seam.
//
// The state is a plain value and the engine copies it once a frame, which is the
// reason it is a value and not an interface the renderer holds: a per-frame
// allocation in the draw path is paid by every frame of every game, and the two
// pointer fields the lighting pack needs are Engine fields precisely to avoid
// one. Nothing here may allocate, including the custom-source path, where the
// state crosses an interface boundary. x/sky measures its own two sources,
// which is where the day cycle's zero now lives.
//
// Verified to fail: handing the pack a fresh `&renderer.NightGrade{...}` rather
// than the Engine field reports `1 allocations per frame resolving and applying
// the environment` for all four sources.
func TestEnvironmentResolvesWithoutAllocating(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  EnvironmentSource
	}{
		{"StaticSource", &StaticSource{Sun: &DirectionalLight{Direction: [3]float32{0, 1, 0}, Color: [3]float32{1, 1, 1}}, Fog: &Fog{Density: 0.01}}},
		{"a custom source", &fakeEnv{state: customState()}},
	} {
		s := NewScene()
		s.Env = tc.src
		e := &Engine{far: 500, Scene: s}
		var l renderer.SceneLighting
		if n := testing.AllocsPerRun(200, func() {
			st := s.Environment()
			e.applyEnvironment(&l, st)
		}); n != 0 {
			t.Errorf("%s: %v allocations per frame resolving and applying the environment", tc.name, n)
		}
		if n := testing.AllocsPerRun(200, func() { tc.src.Advance(1.0 / 60) }); n != 0 {
			t.Errorf("%s: %v allocations per tick advancing", tc.name, n)
		}
	}
}
