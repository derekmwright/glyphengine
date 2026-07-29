package glyphengine

// EnvironmentSource supplies the sky, the light, and the air for a frame.
//
// The engine holds one of these on the Scene and asks it for state each frame.
// That makes the whole environment replaceable: a game that wants weather,
// scripted lighting, an interior, or a sky that owes nothing to a sun can
// implement this and plug it in, without the engine needing to know.
//
//	type stormy struct{ intensity float32 }
//
//	func (s *stormy) Advance(dt float32) { s.intensity = ... }
//
//	func (s *stormy) State() EnvironmentState {
//	    return glyph.EnvironmentState{
//	        SunDir:     [3]float32{0.3, 0.5, 0.2},
//	        SunColor:   [3]float32{0.35, 0.36, 0.40},
//	        Ambient:    [3]float32{0.10, 0.11, 0.13},
//	        FogDensity: 0.02 + s.intensity*0.05,
//	        DrawSky:    true,
//	    }
//	}
//
//	scene.Env = &stormy{}
//
// This governs the values. To change how the sky is *drawn*, replace the sky
// shaders through renderer.WithShaders — the two are deliberately separate, so
// a custom sky does not force a custom lighting model or the reverse.
type EnvironmentSource interface {
	// Advance moves the environment forward on the simulation tick. It runs
	// at the fixed rate, so anything driven from it is frame-rate independent.
	// Implementations with nothing to advance can leave it empty.
	Advance(dt float32)

	// State returns the current environment. It is called once per rendered
	// frame and must not mutate anything — Advance is where change belongs.
	State() EnvironmentState
}

// EnvironmentState is the per-frame contract between an environment and the
// renderer. Its zero value is an empty world: no sky, no light, no fog.
type EnvironmentState struct {
	// SunDir points *toward* the directional light. SunColor is its colour;
	// black means no directional light at all.
	SunDir   [3]float32
	SunColor [3]float32

	// SunElevation is the height of the real sun, -1 to 1, and is what the
	// atmosphere derives its palette from.
	//
	// It is separate from SunDir because SunDir is whichever body is currently
	// lighting the scene. At night that is the moon, which rides highest
	// exactly when the sky should be darkest, so driving the sky from the
	// light direction paints a noon sky at midnight.
	SunElevation float32

	// Ambient is uniform fill light.
	Ambient [3]float32

	// FogDensity blends distant geometry toward the horizon colour. Zero
	// disables it.
	FogDensity float32

	// ClearColor is used when DrawSky is false. With a sky it is unused: the
	// dome is opaque and drawn first.
	ClearColor [3]float32

	// StarFade is how visible the stars are, 0 to 1.
	StarFade float32

	// DrawSky draws the procedural dome. DrawStars, DrawSun and DrawMoon add
	// the stars and the celestial billboards.
	DrawSky   bool
	DrawStars bool
	DrawSun   bool
	DrawMoon  bool

	// SunDiscDir and SunDiscColor place and colour the sun billboard;
	// MoonDiscDir does the same for the moon. Ignored unless DrawSun/DrawMoon.
	SunDiscDir   [3]float32
	SunDiscColor [3]float32
	MoonDiscDir  [3]float32

	// CloudSteps is the volumetric cloud sample count; zero draws none.
	CloudSteps int

	// CastShadows enables the shadow pass. Turning it off when the only light
	// is a dim moon saves the cascades for shadows nobody can see.
	CastShadows bool
}

// Environment is the engine's own environment: composed from separate pieces,
// each of which is optional.
//
//	// A lit outdoor world with a moving sun.
//	scene.Env = glyph.DefaultEnvironment()
//
//	// An interior: no sky, no sun, no weather. Just fill light.
//	scene.Env = &glyph.Environment{
//	    Ambient:    &glyph.AmbientLight{Color: [3]float32{0.18, 0.17, 0.20}},
//	    ClearColor: [3]float32{0.02, 0.02, 0.03},
//	}
//
//	// Nothing at all.
//	scene.Env = nil
//
// It used to be a field on Scene, a field on Engine, and an unconditional draw
// in the command recorder, which meant a game got a procedural sky and a
// sunrise whether it asked for them or not.
type Environment struct {
	// Cycle advances time and derives the sun and moon from it. When set it
	// supplies the directional light, the ambient, and the sun elevation,
	// overriding Sun and Ambient below.
	//
	// Nil means time does not pass; use Sun and Ambient for fixed lighting.
	Cycle *DayNight

	// Sky draws the procedural dome, the celestial discs, and the stars. Nil
	// means none of them, and the frame clears to ClearColor instead.
	Sky *Sky

	// Sun is a fixed directional light, used when Cycle is nil. A scene with
	// neither has no directional light — only ambient and its own point lights.
	Sun *DirectionalLight

	// Ambient is fixed fill light, used when Cycle is nil.
	Ambient *AmbientLight

	// Fog blends distant geometry toward the horizon. Nil disables it.
	Fog *Fog

	// ClearColor is what the frame clears to when Sky is nil.
	ClearColor [3]float32
}

// Sky configures the procedural sky dome.
//
// This is about what gets drawn. The colours are derived from sun elevation in
// shaders/atmosphere.inc; to change those, replace the sky shaders through
// renderer.WithShaders.
type Sky struct {
	// Stars fade in as night falls.
	Stars bool

	// SunDisc and MoonDisc draw the celestial billboards. A game can keep the
	// sky's light and colour without visible bodies in it.
	SunDisc  bool
	MoonDisc bool

	// FixedSunElevation is the sun height the atmosphere uses when there is no
	// Cycle, from -1 to 1. It picks the palette: 0.6 is a high bright sky, 0
	// is sunset, -0.5 is night.
	FixedSunElevation float32

	// CloudSteps is how many samples the volumetric cloud raymarch takes.
	// Zero draws no clouds at all.
	//
	// This is the most expensive thing the engine draws per pixel, and it is
	// meant to be a graphics setting a game exposes rather than a constant.
	// Measured at 1280x720, MSAA 4x, on a Radeon RX 7900 XTX, whole frame:
	//
	//	CloudsOff    0.28 ms   3593 fps
	//	CloudsLow    0.76 ms   1323 fps
	//	CloudsHigh   1.11 ms    898 fps
	//
	// Those are one GPU's numbers and the absolute values will not transfer,
	// but the ratios roughly do: clouds cost about three times the rest of a
	// simple scene at CloudsHigh, and about half that at CloudsLow.
	//
	// Safe to change at runtime, every frame if you like — the value is read
	// when the environment resolves, so a settings slider takes effect on the
	// next frame with nothing to rebuild.
	CloudSteps int
}

// Cloud quality presets for Sky.CloudSteps.
const (
	// CloudsOff draws no clouds. The sky keeps its gradient and sun glow.
	CloudsOff = 0
	// CloudsLow is a coarse march: cloud shapes read correctly, edges are
	// softer and thin wisps can shimmer as the camera moves.
	CloudsLow = 16
	// CloudsHigh is the default.
	CloudsHigh = 32
)

// DefaultSky is a full sky: dome, volumetric clouds, stars, and both discs.
func DefaultSky() *Sky {
	return &Sky{Stars: true, SunDisc: true, MoonDisc: true, CloudSteps: CloudsHigh}
}

// DirectionalLight is a fixed sun: one direction, one colour, no clock.
type DirectionalLight struct {
	// Direction points *toward* the light, matching DayNight.SunDir.
	Direction [3]float32
	Color     [3]float32
}

// AmbientLight is uniform fill light with no direction.
type AmbientLight struct {
	Color [3]float32
}

// Fog blends geometry toward the horizon colour with an exp-squared falloff.
type Fog struct {
	// Density in inverse world units. Around 0.008 fades over a few hundred
	// units; zero is the same as no Fog at all.
	Density float32
}

// DefaultEnvironment is a lit outdoor world: full sky, a day/night cycle
// frozen at sunrise, and light haze.
//
// The cycle is frozen because time passing is a decision a game should make
// deliberately. Call Scene.SetDayCycleSpeed to start it.
func DefaultEnvironment() *Environment {
	return &Environment{
		Cycle: &DayNight{TimeOfDay: 0.25},
		Sky:   DefaultSky(),
		Fog:   &Fog{Density: DefaultFogDensity},
	}
}

// DefaultFogDensity gives about 35% fog at the 80-unit grass cull distance,
// which is enough to hide where the scatter stops without flattening the view.
const DefaultFogDensity = 0.0075

// Advance ticks the day/night cycle. Everything else here is static.
func (env *Environment) Advance(dt float32) {
	if env == nil || env.Cycle == nil {
		return
	}
	env.Cycle.Advance(dt)
}

// State collapses the pieces into a frame's worth of environment.
//
// Keeping the conditional rules here — a cycle overrides fixed light, no sky
// means no stars — is the point of resolving at all. They were previously
// spread through the draw path, which is how the sky ended up impossible to
// turn off.
func (env *Environment) State() EnvironmentState {
	var s EnvironmentState
	if env == nil {
		return s
	}

	s.ClearColor = env.ClearColor
	if env.Fog != nil {
		s.FogDensity = env.Fog.Density
	}

	if dn := env.Cycle; dn != nil {
		s.SunDir, s.SunColor = dn.PrimaryLight()
		s.SunElevation = dn.SunDir()[1]
		s.Ambient = dn.AmbientColor()
		s.StarFade = dn.StarVisibility()
		s.CastShadows = dn.SunAboveHorizon()
		s.SunDiscDir = dn.SunDir()
		s.SunDiscColor = dn.SunDiscColor()
		s.MoonDiscDir = dn.MoonDir()
	} else {
		if env.Sun != nil {
			s.SunDir = env.Sun.Direction
			s.SunColor = env.Sun.Color
			s.CastShadows = s.SunColor[0]+s.SunColor[1]+s.SunColor[2] > 0
		}
		if env.Ambient != nil {
			s.Ambient = env.Ambient.Color
		}
		if env.Sky != nil {
			s.SunElevation = env.Sky.FixedSunElevation
		}
	}

	if env.Sky != nil {
		s.DrawSky = true
		s.CloudSteps = env.Sky.CloudSteps
		s.DrawStars = env.Sky.Stars && s.StarFade > 0
		// The discs are the cycle's bodies; without one there is nothing to
		// place them by.
		s.DrawSun = env.Sky.SunDisc && env.Cycle != nil && s.SunDiscDir[1] > -0.15
		s.DrawMoon = env.Sky.MoonDisc && env.Cycle != nil && s.MoonDiscDir[1] > -0.15
	}
	return s
}
