package glyphengine

import (
	"testing"

	"github.com/go-gl/mathgl/mgl32"

	"github.com/derekmwright/glyphengine/renderer"
)

// TestNilEnvironmentIsEmpty is the property the split exists for: a game that
// does not ask for an environment does not get one.
//
// The sky used to be drawn unconditionally, so an interior scene, a stylized
// flat-shaded look, or anyone supplying their own skybox got a procedural
// sunrise on top of it with no way to decline.
func TestNilEnvironmentIsEmpty(t *testing.T) {
	s := NewScene()
	s.Env = nil

	// The palette and the grade are the two fields a silent source does not get
	// zeroed on: Scene.Environment resolves their sentinel to the scene's,
	// because six black endpoints are what the haze and the water reflection
	// would otherwise reach. No dome is drawn here to use them, but applyFog and
	// the water still are, which is why they are filled in even on this path.
	want := EnvironmentState{SkyPalette: DefaultSkyPalette(), NightGrade: DefaultNightGrade()}
	got := s.Environment()
	if got != want {
		t.Errorf("nil environment resolved to %+v; want the zero state carrying the scene's palette and grade", got)
	}
	if got.DrawSky {
		t.Error("nil environment still draws a sky")
	}

	// And it must not panic when ticked.
	s.Tick(1.0 / 60)
}

// TestStaticSourcePiecesAreIndependent checks each piece of the engine's own
// environment can be present or absent on its own, which is what "composable"
// has to mean to be worth doing.
//
// It is the surviving half of a test that covered the Environment composite. The
// cycle-and-dome half went to x/sky with them, and what is left is the rules the
// engine still owns -- which x/sky's fixed-hour path now delegates to, so these
// subtests are load-bearing for two packages.
func TestStaticSourcePiecesAreIndependent(t *testing.T) {
	t.Run("fixed light", func(t *testing.T) {
		src := &StaticSource{
			Sun:     &DirectionalLight{Direction: [3]float32{0, 1, 0}, Color: [3]float32{1, 1, 1}},
			Ambient: &AmbientLight{Color: [3]float32{0.2, 0.2, 0.2}},
		}
		s := src.State()
		if s.SunDir != ([3]float32{0, 1, 0}) || s.SunColor != ([3]float32{1, 1, 1}) {
			t.Errorf("fixed sun not used: dir %v color %v", s.SunDir, s.SunColor)
		}
		if s.Ambient != ([3]float32{0.2, 0.2, 0.2}) {
			t.Errorf("fixed ambient not used: %v", s.Ambient)
		}
		if !s.CastShadows {
			t.Error("a fixed sun with colour should cast shadows")
		}
	})

	t.Run("a black sun casts nothing", func(t *testing.T) {
		src := &StaticSource{Sun: &DirectionalLight{Direction: [3]float32{0, 1, 0}}}
		if src.State().CastShadows {
			t.Error("a sun with no colour in it still built the cascades")
		}
	})

	t.Run("fog is independent", func(t *testing.T) {
		if s := (&StaticSource{}).State(); s.FogDensity != 0 {
			t.Errorf("fog %v with no Fog", s.FogDensity)
		}
		src := &StaticSource{Fog: &Fog{Density: 0.02}}
		if s := src.State(); s.FogDensity != 0.02 {
			t.Errorf("fog density %v; want 0.02", s.FogDensity)
		}
	})

	t.Run("ambient alone", func(t *testing.T) {
		src := &StaticSource{Ambient: &AmbientLight{Color: [3]float32{0.1, 0.1, 0.12}}, ClearColor: [3]float32{0.02, 0.02, 0.03}}
		s := src.State()
		if s.Ambient != ([3]float32{0.1, 0.1, 0.12}) || s.ClearColor != ([3]float32{0.02, 0.02, 0.03}) {
			t.Errorf("an interior resolved to %+v", s)
		}
		if s.SunColor != ([3]float32{}) || s.CastShadows {
			t.Error("ambient alone produced a directional light")
		}
	})
}

// fakeEnv is a minimal custom EnvironmentSource, standing in for a game's own
// weather or lighting model.
type fakeEnv struct {
	ticks int
	state EnvironmentState
}

func (f *fakeEnv) Advance(dt float32)      { f.ticks++ }
func (f *fakeEnv) State() EnvironmentState { return f.state }

// TestCustomEnvironmentSource checks the interface is genuinely a replacement
// and not just a hook: a game's own implementation must drive the light, the
// fog, and whether a sky is drawn at all, and must be ticked by the scene.
func TestCustomEnvironmentSource(t *testing.T) {
	f := &fakeEnv{state: EnvironmentState{
		SunDir:     [3]float32{0, 0.5, 0.5},
		SunColor:   [3]float32{0.3, 0.3, 0.4},
		Ambient:    [3]float32{0.1, 0.1, 0.12},
		FogDensity: 0.05,
		DrawSky:    false,
		ClearColor: [3]float32{0.01, 0.0, 0.02},
		StarFade:   0.42,
	}}

	s := NewScene()
	s.Env = f

	s.Tick(1.0 / 60)
	if f.ticks != 1 {
		t.Errorf("custom environment advanced %d times; want 1", f.ticks)
	}

	got := s.Environment()
	if got.FogDensity != 0.05 {
		t.Errorf("fog %v; want the custom value", got.FogDensity)
	}
	if got.DrawSky {
		t.Error("custom environment asked for no sky and got one")
	}
	if got.SunColor != ([3]float32{0.3, 0.3, 0.4}) {
		t.Errorf("sun colour %v; want the custom value", got.SunColor)
	}

	// StarVisibility is the one convenience that survived the cycle leaving,
	// because it reads the resolved state rather than reaching for a clock. A
	// custom source has to be able to drive it.
	if got := s.StarVisibility(); got != 0.42 {
		t.Errorf("StarVisibility() = %v for a custom source that said 0.42", got)
	}
}

// TestSkyPaletteSurvivesACustomEnvironment is the property that lets the palette
// be a field on EnvironmentState at all.
//
// A game with its own EnvironmentSource returns a struct it wrote before the
// palette existed, so the field arrives as its zero value: six black colours,
// which is what the sky, the fog and the water reflections would all reach on a
// dependency bump with nobody choosing it. The sentinel is what stops that --
// Scene.Environment reads an all-zero palette as "the scene's", which NewScene
// initialises to Earth's -- and this is the test of it. The same goes for the
// night grade, whose zero value is no scotopic shift at all.
//
// Verified to catch two real mistakes: dropping skyPalette from NewScene's
// literal fails the assertions that ask for the default, reporting six zero
// endpoints; and removing the sentinel from Scene.Environment reports `a silent
// source resolved to the palette {ZenithDay:[0 0 0] ...}; want the scene's`,
// which is the half that is new.
func TestSkyPaletteSurvivesACustomEnvironment(t *testing.T) {
	s := NewScene()
	if got := s.SkyPalette(); got != DefaultSkyPalette() {
		t.Errorf("NewScene gave the palette %+v; want DefaultSkyPalette", got)
	}

	// An environment written before the field existed: it cannot mention the
	// palette, so resolving a frame through it must leave the scene's alone and
	// hand the scene's on to the frame.
	s.Env = &fakeEnv{state: EnvironmentState{SunDir: [3]float32{0, 1, 0}, DrawSky: true}}
	s.Tick(1.0 / 60)
	if st := s.Environment(); st.SkyPalette != DefaultSkyPalette() {
		t.Errorf("a silent source resolved to the palette %+v; want the scene's", st.SkyPalette)
	} else if st.NightGrade != DefaultNightGrade() {
		t.Errorf("a silent source resolved to the grade %+v; want the scene's", st.NightGrade)
	}
	if got := s.SkyPalette(); got != DefaultSkyPalette() {
		t.Errorf("a custom environment moved the palette to %+v; it cannot reach it", got)
	}

	// And a game that does want an alien sky still gets one, under the same
	// custom source.
	alien := SkyPalette{
		ZenithDay:       mgl32.Vec3{0.30, 0.10, 0.62},
		HorizonDay:      mgl32.Vec3{0.95, 0.55, 0.22},
		ZenithTwilight:  mgl32.Vec3{0.18, 0.04, 0.30},
		HorizonTwilight: mgl32.Vec3{0.95, 0.22, 0.30},
		ZenithNight:     mgl32.Vec3{0.0040, 0.0012, 0.0060},
		HorizonNight:    mgl32.Vec3{0.0110, 0.0035, 0.0055},
	}
	s.SetSkyPalette(alien)
	s.Tick(1.0 / 60)
	if got := s.SkyPalette(); got != alien {
		t.Errorf("SetSkyPalette then a tick gave %+v; want what was set", got)
	}
}

// TestNewSceneHasNoEnvironment is the engine half of the sky migration, stated
// as a property: a scene the engine builds has no opinion about the sky in it.
//
// It is the inverse of the test it replaced. TestDefaultEnvironmentMatchesOldBehaviour
// asserted that NewScene handed out a dome, a day cycle frozen at sunrise and
// the engine's haze, because it did -- whether the game wanted them or not.
// Those are x/sky's now, and sky.DefaultEnvironment is what a scene that wants
// them says. What the engine owes is nothing at all.
//
// Verified to fail: putting a source back in NewScene's literal reports
// `NewScene installed an environment: *glyphengine.StaticSource`.
func TestNewSceneHasNoEnvironment(t *testing.T) {
	s := NewScene()
	if s.Env != nil {
		t.Fatalf("NewScene installed an environment: %T", s.Env)
	}
	st := s.Environment()
	if st.DrawSky || st.DrawStars || st.DrawSun || st.DrawMoon {
		t.Errorf("a fresh scene draws something in the sky: %+v", st)
	}
	if st.SunColor != ([3]float32{}) || st.Ambient != ([3]float32{}) || st.FogDensity != 0 {
		t.Errorf("a fresh scene has light or air in it: %+v", st)
	}
	// The two sentinel fields are still filled in, because applyFog and the
	// water read them with no sky at all. See TestNilEnvironmentIsEmpty.
	if st.SkyPalette != DefaultSkyPalette() || st.NightGrade != DefaultNightGrade() {
		t.Errorf("a fresh scene lost the palette or the grade: %+v", st)
	}
}

// TestFogHeightFlowsThrough checks the vertical falloff reaches the resolved
// state, and that leaving it unset keeps the uniform behaviour every existing
// scene was tuned against.
func TestFogHeightFlowsThrough(t *testing.T) {
	uniform := (&StaticSource{Fog: &Fog{Density: 0.01}}).State()
	if uniform.FogHeight != 0 {
		t.Errorf("FogHeight %v with no Height set; zero selects uniform density", uniform.FogHeight)
	}
	if uniform.FogDensity != 0.01 {
		t.Errorf("FogDensity %v", uniform.FogDensity)
	}

	height := (&StaticSource{Fog: &Fog{Density: 0.01, Height: 6, BaseHeight: 3}}).State()
	if height.FogHeight != 6 || height.FogBaseHeight != 3 {
		t.Errorf("height fog resolved to H=%v base=%v", height.FogHeight, height.FogBaseHeight)
	}
	// Density must not change meaning between the two modes: the shader
	// applies the same exp-squared curve either way, and Height only
	// redistributes fog vertically.
	if height.FogDensity != uniform.FogDensity {
		t.Errorf("density changed with Height set: %v vs %v", height.FogDensity, uniform.FogDensity)
	}
}

// TestDefaultLightShaftShapeIsDrawable pins the shape a zero LightShaftShape
// resolves to, which is the engine's and stays here: the shaft pass is the
// engine's own, and the state carries whatever a source asked for untouched.
//
// Whether the pass-through happens is x/sky's test, where the Sky field that
// feeds it lives. This is the other end: the numbers a source that asks for
// nothing gets.
func TestDefaultLightShaftShapeIsDrawable(t *testing.T) {
	if d := DefaultLightShaftShape(); d.Radius <= 0 || d.Decay <= 0 || d.Decay > 1 || !(d.Threshold[1] > d.Threshold[0]) {
		t.Errorf("DefaultLightShaftShape is %+v, which is not a drawable shape", d)
	}
}

// TestDefaultsMatchTheRenderers pins the three defaults this package repeats
// from the renderer against the renderer's own copies.
//
// Scene has no renderer dependency on purpose -- that is what lets a headless
// tool drive one -- so every look default that ends up in a GPU buffer is
// written down twice: here, where a game reads it, and in the renderer, where
// the shader's fallback for a nil pointer lives. Nothing checked that the two
// agreed. They do today, and this is what says so tomorrow: the failure mode
// is a game asking for Scene.NightGrade() and getting one number while a
// caller driving the renderer directly gets another, which no capture of
// either alone would show.
//
// Verified to catch a real mistake: changing this package's DefaultVolumetrics
// anisotropy to 0.5 and leaving the renderer's at 0.4 fails here immediately.
func TestDefaultsMatchTheRenderers(t *testing.T) {
	if got, want := DefaultNightGrade(), renderer.DefaultNightGrade(); got.Strength != want.Strength ||
		got.Tint.X() != want.Tint[0] || got.Tint.Y() != want.Tint[1] || got.Tint.Z() != want.Tint[2] {
		t.Errorf("DefaultNightGrade() = %+v, renderer.DefaultNightGrade() = %+v", got, want)
	}

	g, r := DefaultSkyPalette(), renderer.DefaultSkyPalette()
	for _, tc := range []struct {
		name string
		a, b [3]float32
	}{
		{"ZenithDay", g.ZenithDay, r.ZenithDay},
		{"HorizonDay", g.HorizonDay, r.HorizonDay},
		{"ZenithTwilight", g.ZenithTwilight, r.ZenithTwilight},
		{"HorizonTwilight", g.HorizonTwilight, r.HorizonTwilight},
		{"ZenithNight", g.ZenithNight, r.ZenithNight},
		{"HorizonNight", g.HorizonNight, r.HorizonNight},
	} {
		if tc.a != tc.b {
			t.Errorf("DefaultSkyPalette().%s = %v, renderer's = %v", tc.name, tc.a, tc.b)
		}
	}

	if got, want := DefaultVolumetrics(), renderer.DefaultVolumetrics(); got.Anisotropy != want.Anisotropy || got.Steps != want.Steps {
		t.Errorf("DefaultVolumetrics() = %+v, renderer.DefaultVolumetrics() = %+v", got, want)
	}
}
