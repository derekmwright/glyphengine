package sky

import glyph "github.com/derekmwright/glyphengine"

// Environment is this package's environment: composed from separate pieces,
// each of which is optional.
//
//	// A lit outdoor world with a moving sun.
//	scene.Env = sky.DefaultEnvironment()
//
//	// A sky frozen at one hour, with fixed light under it.
//	scene.Env = &sky.Environment{
//	    Sun:     &glyph.DirectionalLight{Direction: [3]float32{0.4, 0.8, 0.3}, Color: [3]float32{0.7, 0.68, 0.62}},
//	    Ambient: &glyph.AmbientLight{Color: [3]float32{0.18, 0.17, 0.20}},
//	    Sky:     &sky.Sky{FixedSunElevation: 0.4},
//	}
//
// An interior wants no sky at all and so wants none of this: glyph.StaticSource
// is the engine's own fixed light and air, and it is what stays behind in the
// engine once this package has the dome.
//
// This used to be glyphengine.Environment, a field on Scene, a field on Engine,
// and an unconditional draw in the command recorder — which meant a game got a
// procedural sky and a sunrise whether it asked for them or not. Scene.Env is
// now nil by default and this is one of the things that can be put in it.
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
	Sun *glyph.DirectionalLight

	// Ambient is fixed fill light, used when Cycle is nil.
	Ambient *glyph.AmbientLight

	// Fog blends distant geometry toward the horizon. Nil disables it.
	Fog *glyph.Fog

	// ClearColor is what the frame clears to when Sky is nil.
	ClearColor [3]float32
}

// DefaultEnvironment is a lit outdoor world: full sky, a day/night cycle
// frozen at sunrise, and light haze.
//
// The cycle is frozen because time passing is a decision a game should make
// deliberately. Set Cycle.Speed to start it.
func DefaultEnvironment() *Environment {
	return &Environment{
		Cycle: &DayNight{TimeOfDay: 0.25},
		Sky:   DefaultSky(),
		Fog:   &glyph.Fog{Density: glyph.DefaultFogDensity},
	}
}

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
//
// The two branches are DayCycleSource and the fixed-light path, which are those
// same two resolutions as sources in their own right. Environment is the
// composite that picks between them, and it picks on Cycle because a cycle
// already knows where the sun is: a fixed light beside one would be a second
// answer to the same question.
func (env *Environment) State() glyph.EnvironmentState {
	if env == nil {
		return glyph.EnvironmentState{}
	}
	a := air{sky: env.Sky, fog: env.Fog, clearColor: env.ClearColor}
	if env.Cycle != nil {
		return dayCycleState(env.Cycle, a)
	}
	return staticState(env.Sun, env.Ambient, a)
}

// DayCycleSource is this sky's day cycle on its own: a clock places the sun and
// the moon, and the keyframe curves in daynight.go derive the directional light,
// the ambient, the disc colours and the star fade from where they are.
//
//	scene.Env = &sky.DayCycleSource{
//	    Cycle: sky.DayNight{TimeOfDay: 0.25, Speed: 1.0 / 300},
//	    Sky:   sky.DefaultSky(),
//	    Fog:   &glyph.Fog{Density: glyph.DefaultFogDensity},
//	}
//
// That is the same environment DefaultEnvironment builds, which every example
// uses through Environment.
type DayCycleSource struct {
	// Cycle is the clock. Advance moves it; set TimeOfDay through
	// DayNight.SetTimeOfDay so values outside [0,1) wrap.
	Cycle DayNight

	// Sky draws the dome, the discs and the stars. Nil draws none of them and
	// the frame clears to ClearColor instead.
	Sky *Sky

	// Fog blends distant geometry toward the horizon. Nil disables it.
	Fog *glyph.Fog

	// ClearColor is what the frame clears to when Sky is nil.
	ClearColor [3]float32
}

// Advance steps the clock. It is the only thing here that moves.
func (d *DayCycleSource) Advance(dt float32) { d.Cycle.Advance(dt) }

// State resolves this frame's environment from the clock.
func (d *DayCycleSource) State() glyph.EnvironmentState {
	return dayCycleState(&d.Cycle, air{sky: d.Sky, fog: d.Fog, clearColor: d.ClearColor})
}

// air is the part of an environment that does not depend on where the light
// comes from: the drawn sky, the fog and the clear colour. Both resolutions
// below take one, so the rules that follow from Sky are written once.
//
// Unexported and passed by value: it is a grouping of three existing fields to
// keep two function signatures readable, not a type a game configures.
type air struct {
	sky        *Sky
	fog        *glyph.Fog
	clearColor [3]float32
}

// fillAir writes the fields that do not depend on the light. It runs before
// either light path, because resolveSky reads what those paths set.
//
// Only dayCycleState calls it. The fixed-light path gets the same three fields
// from glyph.StaticSource, which is the engine's own resolution of them and the
// one place they are decided once the cycle lives out here.
func (a air) fillAir(s *glyph.EnvironmentState) {
	s.ClearColor = a.clearColor
	if a.fog != nil {
		s.FogDensity = a.fog.Density
		s.FogHeight = a.fog.Height
		s.FogBaseHeight = a.fog.BaseHeight
	}
}

// resolveSky applies Sky to an already-lit state. haveBodies says whether
// anything placed a sun and a moon, which is what decides the discs: a Sky on
// its own has colours and stars but nothing in it to draw.
func (a air) resolveSky(s *glyph.EnvironmentState, haveBodies bool) {
	if a.sky == nil {
		return
	}
	s.DrawSky = true
	s.CloudSteps = a.sky.CloudSteps
	s.Cirrus = a.sky.Cirrus
	// Shafts come from the sun disc in the drawn sky, so they live and die
	// with it rather than with the horizon.
	//
	// This used to be `if s.SunElevation > 0`, which deleted them in a
	// single frame at the moment they look best. The disc does not go out
	// at zero elevation -- DrawSun keeps drawing it to -0.15 and
	// SunDiscColor keeps it at most of its boost the whole way down -- so
	// the sky the shafts are built from is still in full sunset while they
	// had already stopped. Measured, `09-water -yaw 1.771 -pitch -0.185
	// -pillars`: at time 0.745, elevation +0.031, the pass added mean sRGB
	// luma +1.83 across the frame and peaked at +74; at 0.755, elevation
	// -0.031, it added exactly nothing. That is a blink, not a sunset.
	//
	// The window ends where DrawSun does, so the shafts are gone before
	// their source stops being drawn. It is deliberately not SunDiscColor's
	// own (-0.20, -0.02): that one is wider because the disc's COLOUR has
	// to stay continuous as the cycle wraps past midnight, and a shaft
	// radiating from a disc nobody is drawing is a different mistake.
	s.LightShafts = a.sky.LightShafts * smoothstep(-0.15, -0.02, s.SunElevation)
	s.LightShaftShape = a.sky.LightShaftShape
	s.DrawStars = a.sky.Stars && s.StarFade > 0
	s.StarDensity = a.sky.StarDensity
	if s.StarDensity < 0 {
		s.StarDensity = 0
	}
	s.MilkyWay = a.sky.MilkyWay
	if s.MilkyWay < 0 {
		s.MilkyWay = 0
	} else if s.MilkyWay > 1 {
		s.MilkyWay = 1
	}
	// The discs are the cycle's bodies; without one there is nothing to
	// place them by.
	s.DrawSun = a.sky.SunDisc && haveBodies && s.SunDiscDir[1] > -0.15
	s.DrawMoon = a.sky.MoonDisc && haveBodies && s.MoonDiscDir[1] > -0.15
}

// dayCycleState is the day-cycle resolution: the clock supplies the light, the
// ambient, the bodies and the star fade, and overrides any fixed light beside
// it.
func dayCycleState(dn *DayNight, a air) glyph.EnvironmentState {
	var s glyph.EnvironmentState
	a.fillAir(&s)

	s.SunDir, s.SunColor = dn.PrimaryLight()
	// Derived from RealSunDir rather than fetched again, so the elevation
	// the palette uses and the direction the glow uses cannot disagree.
	s.RealSunDir = dn.SunDir()
	s.SunElevation = s.RealSunDir[1]
	s.Ambient = dn.AmbientColor()
	s.StarFade = dn.StarVisibility()
	s.CastShadows = dn.SunAboveHorizon()
	s.SunDiscDir = dn.SunDir()
	s.SunDiscColor = dn.SunDiscColor()
	s.MoonDiscDir = dn.MoonDir()
	s.MoonDiscColor = dn.MoonDiscColor()

	a.resolveSky(&s, true)
	return s
}

// staticState is the fixed-light resolution: whatever was put there, plus a sky
// frozen at Sky.FixedSunElevation.
//
// The light and the air come from glyph.StaticSource rather than from a second
// copy of those rules here. That is deliberate: the engine kept StaticSource
// when the cycle left, so a fixed sun, a fixed ambient, the fog and the clear
// colour are already resolved in exactly one place, and the only thing this
// adds is the dome. A copy would be a second answer to a question the engine
// still answers, and the two would be free to drift across a module boundary
// where no compiler would notice.
//
// Taken by value and addressed locally so the delegation allocates nothing; the
// zero-allocation test in this package is what holds that.
func staticState(sun *glyph.DirectionalLight, ambient *glyph.AmbientLight, a air) glyph.EnvironmentState {
	base := glyph.StaticSource{Sun: sun, Ambient: ambient, Fog: a.fog, ClearColor: a.clearColor}
	s := base.State()

	if a.sky != nil {
		s.SunElevation = a.sky.FixedSunElevation
		// The same curve the cycle uses. Without this a sky frozen below the
		// horizon draws the night palette over an empty field, because
		// StarFade stays at its zero value and DrawStars reads it.
		s.StarFade = starVisibilityAt(s.SunElevation)
	}

	a.resolveSky(&s, false)
	return s
}
