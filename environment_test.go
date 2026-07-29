package glyphengine

import "testing"

// TestNilEnvironmentIsEmpty is the property the split exists for: a game that
// does not ask for an environment does not get one.
//
// The sky used to be drawn unconditionally, so an interior scene, a stylized
// flat-shaded look, or anyone supplying their own skybox got a procedural
// sunrise on top of it with no way to decline.
func TestNilEnvironmentIsEmpty(t *testing.T) {
	s := NewScene()
	s.Env = nil

	got := s.Environment()
	if got != (EnvironmentState{}) {
		t.Errorf("nil environment resolved to %+v; want the zero state", got)
	}
	if got.DrawSky {
		t.Error("nil environment still draws a sky")
	}

	// And it must not panic when ticked.
	s.Tick(1.0 / 60)
}

// TestEnvironmentPiecesAreIndependent checks each piece can be present or
// absent on its own, which is what "composable" has to mean to be worth doing.
func TestEnvironmentPiecesAreIndependent(t *testing.T) {
	t.Run("sky without a cycle", func(t *testing.T) {
		env := &Environment{Sky: DefaultSky()}
		s := env.State()
		if !s.DrawSky {
			t.Error("sky not drawn")
		}
		// No cycle means no bodies to place, so no discs.
		if s.DrawSun || s.DrawMoon {
			t.Error("celestial discs drawn with no cycle to position them")
		}
		if s.SunColor != ([3]float32{}) {
			t.Errorf("sun light %v with no cycle and no fixed sun", s.SunColor)
		}
	})

	t.Run("cycle without a sky", func(t *testing.T) {
		env := &Environment{Cycle: &DayNight{TimeOfDay: 0.5}}
		s := env.State()
		if s.DrawSky || s.DrawStars || s.DrawSun || s.DrawMoon {
			t.Error("something was drawn with no Sky")
		}
		// The light still works: a game can supply its own skybox and keep the
		// engine's sun.
		if s.SunColor == ([3]float32{}) {
			t.Error("no sun light at noon")
		}
		if !s.CastShadows {
			t.Error("no shadows at noon")
		}
	})

	t.Run("fixed light without a cycle", func(t *testing.T) {
		env := &Environment{
			Sun:     &DirectionalLight{Direction: [3]float32{0, 1, 0}, Color: [3]float32{1, 1, 1}},
			Ambient: &AmbientLight{Color: [3]float32{0.2, 0.2, 0.2}},
		}
		s := env.State()
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

	t.Run("a cycle overrides fixed light", func(t *testing.T) {
		env := &Environment{
			Cycle: &DayNight{TimeOfDay: 0.5},
			Sun:   &DirectionalLight{Direction: [3]float32{1, 0, 0}, Color: [3]float32{9, 9, 9}},
		}
		if s := env.State(); s.SunColor == ([3]float32{9, 9, 9}) {
			t.Error("fixed sun used while a cycle is present")
		}
	})

	t.Run("fog is independent", func(t *testing.T) {
		if s := (&Environment{}).State(); s.FogDensity != 0 {
			t.Errorf("fog %v with no Fog", s.FogDensity)
		}
		env := &Environment{Fog: &Fog{Density: 0.02}}
		if s := env.State(); s.FogDensity != 0.02 {
			t.Errorf("fog density %v; want 0.02", s.FogDensity)
		}
	})

	t.Run("stars can be disabled independently", func(t *testing.T) {
		env := &Environment{
			Cycle: &DayNight{TimeOfDay: 0}, // midnight
			Sky:   &Sky{Stars: false, SunDisc: true, MoonDisc: true},
		}
		s := env.State()
		if !s.DrawSky {
			t.Error("sky not drawn")
		}
		if s.DrawStars {
			t.Error("stars drawn with Stars=false")
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

	// The built-in conveniences must not misreport a custom environment as
	// having a cycle it does not have.
	if s.DayNight() != nil {
		t.Error("DayNight() returned a cycle for a custom environment")
	}
	if s.TimeOfDay() != 0 {
		t.Errorf("TimeOfDay() = %v for a custom environment", s.TimeOfDay())
	}
	// And must not panic.
	s.SetTimeOfDay(0.5)
	s.SetDayCycleSpeed(1)
}

// TestDefaultEnvironmentMatchesOldBehaviour guards the migration: a scene that
// says nothing about its environment should look like it did before the split.
func TestDefaultEnvironmentMatchesOldBehaviour(t *testing.T) {
	s := NewScene()
	got := s.Environment()

	if !got.DrawSky {
		t.Error("default scene has no sky")
	}
	if s.TimeOfDay() != 0.25 {
		t.Errorf("default time of day %v; want sunrise at 0.25", s.TimeOfDay())
	}
	if dn := s.DayNight(); dn == nil {
		t.Fatal("default scene has no day/night cycle")
	} else if dn.Speed != 0 {
		t.Errorf("default cycle speed %v; time should not pass unless asked", dn.Speed)
	}
	if got.FogDensity != DefaultFogDensity {
		t.Errorf("default fog %v; want %v", got.FogDensity, DefaultFogDensity)
	}
}
