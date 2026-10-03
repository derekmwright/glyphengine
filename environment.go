package glyphengine

import "github.com/derekmwright/glyphengine/renderer"

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
// This governs the values. What *draws* a dome, stars or clouds from them is
// the sky slot in renderer.ShaderSet, supplied through renderer.WithShaders —
// the two are deliberately separate, so a custom sky does not force a custom
// lighting model or the reverse. The engine ships neither half: x/sky is the
// built-in Earth sky, and it is both a source and the three shaders.
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

	// RealSunDir points toward the real sun, and SunElevation is its height,
	// -1 to 1. Together they are what the atmosphere derives its palette and
	// its scattering from.
	//
	// They are separate from SunDir because SunDir is whichever body is
	// currently lighting the scene. At night that is the moon, which rides
	// highest exactly when the sky should be darkest, so driving the sky from
	// the light direction paints a noon sky at midnight — and hangs the warm
	// sunset halo on the moon, which is the other half of the same mistake.
	//
	// SunElevation is RealSunDir's y. It stays a separate field because most
	// of the atmosphere only needs the height, and because a fixed sun can set
	// an elevation for the sky without the two having to agree.
	RealSunDir   [3]float32
	SunElevation float32

	// Ambient is uniform fill light.
	Ambient [3]float32

	// FogDensity blends distant geometry toward the horizon colour. Zero
	// disables it.
	FogDensity float32

	// FogHeight and FogBaseHeight describe the vertical falloff; see Fog.
	// A zero FogHeight selects uniform density.
	FogHeight     float32
	FogBaseHeight float32

	// ClearColor is used when DrawSky is false. With a sky it is unused: the
	// dome is opaque and drawn first.
	ClearColor [3]float32

	// StarFade is how visible the stars are, 0 to 1.
	StarFade float32

	// MilkyWay is the galactic band's strength, 0 to 1. The star pass reads it;
	// what it means is the source's, not the engine's.
	MilkyWay float32

	// StarDensity scales the star count, 1 being whatever the star shader calls
	// its own default and 0 leaving an empty sky.
	StarDensity float32

	// DrawSky draws the procedural dome. DrawStars, DrawSun and DrawMoon add
	// the stars and the celestial billboards.
	DrawSky   bool
	DrawStars bool
	DrawSun   bool
	DrawMoon  bool

	// SunDiscDir and SunDiscColor place and colour the sun billboard;
	// MoonDiscDir and MoonDiscColor do the same for the moon. Ignored unless
	// DrawSun/DrawMoon.
	//
	// Both colours arrive with their horizon fade and their brightness boost
	// already applied, because a disc is a light source rather than a surface
	// and how bright it is at a given elevation is a look. The moon's used to
	// be three constants and a fade inside buildMoonObject, which left the
	// engine owning a colour after the environment was meant to own all of
	// them: a replacement sky could move the moon and not tint it.
	SunDiscDir    [3]float32
	SunDiscColor  [3]float32
	MoonDiscDir   [3]float32
	MoonDiscColor [3]float32

	// CloudSteps is the volumetric cloud sample count; zero disables cumulus.
	CloudSteps int

	// Cirrus is the high, thin cloud layer strength, 0 to 1. Zero disables it.
	// Independent of CloudSteps.
	Cirrus float32

	// LightShafts is the god-ray strength; zero disables them. It carries the
	// fade with the sun disc's elevation already applied, so a sun on its way
	// down arrives here weaker rather than being cut off at the horizon.
	LightShafts float32

	// LightShaftShape is the shape the source asked the shaft pass for, passed
	// through untouched. Zero fields mean the engine's defaults; see
	// LightShaftShape.
	LightShaftShape LightShaftShape

	// CastShadows enables the shadow pass. Turning it off when the only light
	// is a dim moon saves the cascades for shadows nobody can see.
	CastShadows bool

	// SkyPalette is the six colours the dome, the fog and the water's
	// reflection all blend between; NightGrade is the scotopic grade lit
	// surfaces take on as daylight goes. Both are here because the fog, the
	// water and the clouds read them every frame, so a source that replaces the
	// sky has to be able to say what colour the air is -- otherwise an alien
	// sky still hazes into Earth-blue.
	//
	// An all-zero value means "the scene's", which Scene.SetSkyPalette and
	// Scene.SetNightGrade write and NewScene initialises to the engine's
	// defaults. That sentinel is what makes these safe to have here at all: a
	// source written before the field existed returns the zero value and its
	// sky, haze and water stay exactly as they were, instead of the six black
	// endpoints a plain value field would have handed them. Scene.Environment
	// is the one place it is resolved.
	//
	// The cost is two values that cannot be asked for: a palette of six exact
	// blacks, and a grade with Strength 0 *and* a zero Tint. For a night with
	// no scotopic shift set Strength 0 and any tint -- the tint is unread at
	// zero strength -- and for a black world set Sky nil and ClearColor black,
	// which is what an empty environment already means.
	SkyPalette SkyPalette
	NightGrade NightGrade
}

// LightShaftShape tunes the look of the light shafts: how far they reach, how
// hard a streak an occluder casts, and what counts as bright enough to be a
// source. Each field's zero value keeps the engine's default for that field,
// so a game sets the one it cares about:
//
//	sky.LightShaftShape.Radius = 1.2 // reach further across the frame
//
// The defaults were measured on the default sky at dusk (the tables are beside
// the constants in renderer/commands.go). They are defaults and not constants
// because they are a look, and because Threshold in particular is a statement
// about how bright the sky is -- which, since SetSkyPalette, is the game's to
// decide.
type LightShaftShape struct {
	// Radius is how far from the sun the shafts reach, in screen heights.
	// Default 0.90. Wider reaches further and starts to haze ground a few
	// metres from the eye: the pass has no depth buffer, and distance from the
	// sun on screen is what stands in for it.
	Radius float32

	// Decay is the weight each of the 48 steps toward the sun keeps from the
	// one before, in (0, 1]. Default 0.96. Lower gives an occluder a harder
	// streak and the shafts less reach; 1 is an even wash.
	Decay float32

	// Threshold is the linear-luminance window a pixel has to clear to count as
	// a source, as {low, high}. Default {0.62, 0.88}: on the default palette
	// that admits cloud, the sunset glow and the disc, and rejects plain sky
	// and everything on the ground. A palette with a much brighter sky wants it
	// raised, or the whole dome smears into itself; a much dimmer one wants it
	// lowered, or nothing clears it and the pass draws nothing. {0, 0} is the
	// default -- for a window that really starts at zero, give high a value.
	Threshold [2]float32
}

// DefaultLightShaftShape is the shape a zero LightShaftShape resolves to,
// spelled out for a game that wants to start from the numbers.
func DefaultLightShaftShape() LightShaftShape {
	d := renderer.DefaultLightShaftShape()
	return LightShaftShape{Radius: d.Radius, Decay: d.Decay, Threshold: d.Threshold}
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

// Fog blends geometry toward the horizon colour with distance.
type Fog struct {
	// Density in inverse world units. Around 0.008 fades over a few hundred
	// units; zero is the same as no Fog at all.
	Density float32

	// Height is the altitude over which density falls to 1/e of its value at
	// BaseHeight. Zero means uniform fog at every altitude.
	//
	// Real fog settles, and a uniform one cannot express that at any density:
	// a valley floor has the same haze as the ridge above it, so nothing reads
	// as low-lying. With a Height set, mist pools in the low ground and peaks
	// rise out of it.
	//
	// Sensible values are on the order of the terrain's vertical scale — a
	// Height of 8 over a 15-unit landscape puts most of the fog in the bottom
	// third of it.
	Height float32

	// BaseHeight is the world Y at which density equals Density. Above it fog
	// thins, below it thickens. Usually the ground or water level.
	BaseHeight float32
}

// DefaultFogDensity gives about 35% fog at the 80-unit grass cull distance,
// which is enough to hide where the scatter stops without flattening the view.
const DefaultFogDensity = 0.0075

// StaticSource is light and air that do not move: one direction, one colour, no
// clock. It is the whole of the environment the engine itself has, now that the
// day cycle and the dome are in x/sky — flat colours, a sun that stays where it
// is put, and fog.
//
//	scene.Env = &glyph.StaticSource{
//	    Sun:     &glyph.DirectionalLight{Direction: [3]float32{0.4, 0.8, 0.3}, Color: [3]float32{0.7, 0.68, 0.62}},
//	    Ambient: &glyph.AmbientLight{Color: [3]float32{0.18, 0.17, 0.20}},
//	}
//
// It draws no sky. DrawSky stays false, so the frame keeps ClearColor: an
// interior, a diagnostic scene, or a world whose sky comes from somewhere else.
// For a dome — fixed at one hour or on a clock — use a package that supplies
// one; x/sky's Environment takes exactly these pieces and adds a Sky to them.
//
// It is also the one resolution of fixed light the engine still owns, which is
// why x/sky's fixed-light path calls it rather than repeating it. The rules are
// small but they are rules: a sun with a black colour casts no shadows, and
// without a cycle there is no handover, so the light and the real sun are the
// same vector.
type StaticSource struct {
	// Sun is the directional light. Nil means none: only ambient and the
	// scene's own point and spot lights.
	Sun *DirectionalLight

	// Ambient is uniform fill light. Nil means none.
	Ambient *AmbientLight

	// Fog blends distant geometry toward the horizon. Nil disables it.
	Fog *Fog

	// ClearColor is what the frame clears to. It is what the frame clears to
	// unconditionally here, since this source never draws a dome over it.
	ClearColor [3]float32
}

// Advance does nothing: nothing here moves. The method exists because the
// interface needs it, and an empty one is the honest implementation.
func (s *StaticSource) Advance(float32) {}

// State resolves this frame's environment from the fixed pieces.
//
// The arithmetic is kept in the order it was written in when this shared a
// resolver with the day cycle. Nothing here is subtle enough for that to matter
// on its own, but every committed capture of a fixed-light scene was taken
// through it, and a reassociated float32 is a moved low bit.
func (s *StaticSource) State() EnvironmentState {
	var st EnvironmentState
	st.ClearColor = s.ClearColor
	if s.Fog != nil {
		st.FogDensity = s.Fog.Density
		st.FogHeight = s.Fog.Height
		st.FogBaseHeight = s.Fog.BaseHeight
	}
	if s.Sun != nil {
		st.SunDir = s.Sun.Direction
		st.SunColor = s.Sun.Color
		// Without a cycle there is no sun/moon handover, so the light and
		// the real sun are the same thing.
		st.RealSunDir = s.Sun.Direction
		st.CastShadows = st.SunColor[0]+st.SunColor[1]+st.SunColor[2] > 0
	}
	if s.Ambient != nil {
		st.Ambient = s.Ambient.Color
	}
	return st
}
