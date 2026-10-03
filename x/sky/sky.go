// Package sky is the engine's Earth sky, moved out of the engine: a day cycle,
// the palette curves it drives, the dome, the sun and moon discs, the stars and
// the Milky Way, and the cloud layers.
//
// It is an opinion in the sense ADR 0012 gives that word, and it fails all
// three parts of the rule-14 test at once. The keyframe tables in daynight.go
// are a specific look. The sun disc's brightness boost, the moon's, the width
// of the twilight lobe and the star curve's lag behind sunset are tuned
// constants. And a game on another planet, in a storm, or underground can
// reasonably want a different one — which is the whole reason the engine now
// has an EnvironmentSource rather than a sky.
//
// # The split: the engine keeps the slot, this package paints it
//
// Nothing about *when* or *where* the dome is drawn is here. The engine keeps
// all of it, because all of it is mechanism:
//
//	the fullscreen triangle at the reverse-Z far plane (sky.vert) and the
//	GreaterOrEqual depth state that makes the dome shade only the pixels
//	nothing else reached;
//	the pass order — clouds, then the dome, then the in-scattering, then the
//	stars, then the discs — and the alpha the dome writes for the layers after
//	it to blend against;
//	the half-resolution cloud target, its two-buffer history and the barriers
//	that order the march against the dome sampling it;
//	the push-constant and UBO packing every shader in the frame shares;
//	the billboard the discs are drawn on, placed just inside the far plane;
//	the volumetric march, the fog, the light shafts and the lighting pack,
//	which read the environment and own no sky of their own.
//
// What this package supplies is the two things that decide what the sky looks
// like: an EnvironmentSource that says where the sun is and what colour
// everything is this frame, and the three fragment shaders that paint the dome,
// the stars and the clouds. The engine leaves those three stages nil in
// renderer.DefaultShaders — the sky slot — and draws nothing where they are
// absent. See Shaders.
//
//	e, err := glyph.New(glyph.Config{
//	    Game:    &game{},
//	    Options: []glyph.Option{glyph.WithShaders(sky.Shaders())},
//	})
//	...
//	e.Scene.Env = sky.DefaultEnvironment()
//
// Both halves are needed and neither is enough on its own: without the shaders
// the source asks for a dome the renderer has no pipeline for, and without the
// source the shaders are never asked to draw. A game that wants the look but
// has already overridden other stages uses Fill.
//
// # What this package deliberately does not own
//
// **The atmosphere's shared GLSL.** atmDaylight, atmTwilight, atmSkyPalette,
// atmSunGlow and atmNightShift stay in the engine's exported include set, and
// this package's shaders #include "atmosphere.inc" like any other consumer.
// They are not here because they are not only the dome's: the fog distant
// geometry fades into, the water's reflection and every lit surface's night
// grade call the same functions, and one copy is the only form of agreement
// that cannot drift. The *data* those curves blend between is already a
// uniform — renderer.SkyPalette — so a package that wants a different sky
// changes six colours rather than vendoring the lighting chain.
//
// **The palette and night-grade defaults.** renderer.DefaultSkyPalette and
// renderer.DefaultNightGrade stay in the engine because a scene with no sky at
// all still fogs and still grades. This package leaves EnvironmentState's
// SkyPalette and NightGrade at their zero value, which is the sentinel for "the
// scene's" — so Scene.SetSkyPalette keeps working against this sky exactly as
// it did against the built-in one.
//
// **The disc magnification.** How much larger a body looks near the horizon is
// a look, and it is the one piece of one the engine kept: see sky.md.
package sky

import (
	_ "embed"

	glyph "github.com/derekmwright/glyphengine"
	"github.com/derekmwright/glyphengine/renderer"
)

// The three fragment stages the engine leaves nil. They are committed as
// SPIR-V, embedded here, for the reason AGENTS.md rule 2 gives for the engine's
// own: a game that `go get`s this package has the module and not a Vulkan SDK.
// spirv_test.go is both the check that they match the GLSL beside them and the
// generator that writes them.
//
//go:embed sky.frag.spv
var skyFragSPV []byte

//go:embed stars.frag.spv
var starsFragSPV []byte

//go:embed clouds.frag.spv
var cloudsFragSPV []byte

// Shaders is the sky slot filled in: the three stages renderer.DefaultShaders
// leaves nil, and nothing else.
//
// Every other stage stays nil, which renderer fills from the engine's embedded
// defaults, so this is the whole of what a game that wants this sky and no
// other shader change has to pass:
//
//	glyph.WithShaders(sky.Shaders())
func Shaders() renderer.ShaderSet {
	return Fill(renderer.ShaderSet{})
}

// Fill returns s with this package's three stages set, for a game that is
// already overriding others:
//
//	custom := renderer.DefaultShaders()
//	custom.LitFrag = myLitFragSpv
//	glyph.WithShaders(sky.Fill(custom))
//
// Stages already set are left alone, so a game can take this sky's dome and
// keep its own clouds.
func Fill(s renderer.ShaderSet) renderer.ShaderSet {
	if s.SkyFrag == nil {
		s.SkyFrag = skyFragSPV
	}
	if s.StarsFrag == nil {
		s.StarsFrag = starsFragSPV
	}
	if s.CloudsFrag == nil {
		s.CloudsFrag = cloudsFragSPV
	}
	return s
}

// Sky configures the procedural sky dome.
//
// This is about what gets drawn. Scene.SetSkyPalette controls the colours
// shared by sky, fog, water and cloud ambient fill. WithShaders can replace
// the procedural shaders when a palette is not enough.
type Sky struct {
	// Stars fade in as night falls.
	Stars bool

	// StarDensity scales how many stars are drawn, 1 being the shipped default
	// and 0 leaving an empty sky. It scales the whole field at once; the
	// regional thinning that keeps the sky from looking uniform is applied on
	// top of it either way.
	StarDensity float32

	// MilkyWay is the galactic band's strength, 0 to 1. Default 1.
	//
	// A band of amber and violet cloud with dust lanes cutting through it,
	// carrying its own grain of unresolved stars. It rides in the star pass and
	// fades on the same night factor, so it needs Stars, and the noise is
	// branched around when this is zero -- an empty sky costs nothing.
	//
	// It is drawn in four passes: a midtone cloud field, warm highlights
	// confined to a lane along the spine, dust blocked in over the top, and
	// grain. All four read off the same cloud field so they nest instead of
	// fighting, and the dust occludes the band's own grain but not the star
	// field, which is nearer than the galaxy.
	MilkyWay float32

	// SunDisc and MoonDisc draw the celestial billboards. A game can keep the
	// sky's light and colour without visible bodies in it.
	SunDisc  bool
	MoonDisc bool

	// FixedSunElevation is the sun height the atmosphere uses when there is no
	// Cycle, from -1 to 1. It picks the palette: 0.6 is a high bright sky, 0
	// is sunset, -0.5 is night.
	FixedSunElevation float32

	// CloudSteps sets the coarse sample budget for volumetric cumulus.
	// Occupied intervals use quarter-sized steps, up to four times this count.
	// Zero disables cumulus; Cirrus controls the high layer separately.
	//
	// Use CloudsLow or CloudsHigh as graphics presets. Lower counts also change
	// which noise octaves resolve, so the shape can change along with the cost.
	// Measure with task bench; docs/agents/clouds.md records the current setup.
	// Safe to change at runtime without rebuilding resources.
	CloudSteps int

	// Cirrus is the high, thin cloud layer strength, 0 to 1. Zero disables it.
	// Independent of CloudSteps; DefaultSky keeps this at zero.
	Cirrus float32

	// LightShafts is the strength of screen-space light shafts, or god rays:
	// the smear of brightness radiating from the sun past whatever occludes
	// it. Zero disables them; DefaultSky sets 0.25.
	//
	// Measured at dusk with the sun coming up over a ridge behind pillars
	// (`09-water -time 0.72 -yaw 1.771 -pitch -0.185 -pillars`), as mean sRGB
	// luma added over ground lit through a gap and over the occluder itself:
	// 0.20 gives +26.0 and +29.1, 0.25 gives +31.2 and +34.7, 0.35 gives +40.7
	// and +44.7, 0.50 gives +53.1 and +57.7.
	//
	// Watch the second number, not the first, and that is why the default is
	// 0.25 rather than the 0.35 this field shipped with. The gaps beside a
	// setting sun are already at the top of the display range, so the only
	// pixels with headroom left to brighten are the dark ones -- which means
	// the strength that decides whether this reads as light or as a dirty lens
	// is really the strength at which a silhouette stops being one. That pillar
	// reads 67 with the shafts off and 245 for the sky beside it; 102 at 0.25,
	// 112 at 0.35, and 158 at 1.0, which is a pale shape rather than a dark
	// one.
	//
	// They are screen-space, so they only exist while the sun is on screen,
	// and they fade as it approaches the edge rather than popping out. That is
	// a property of the technique, not a tuning failure — there is nothing to
	// smear from once the sun leaves the frame. They also need a Cycle: the
	// shafts radiate from the sun billboard, and only a cycle places one.
	//
	// Costs one fullscreen pass of 48 taps -- 0.163 ms at 1280x720 MSAA 4x on
	// a Radeon RX 7900 XTX with the sun centred, 0.095 with it at the edge --
	// and only while the sun is up and in frame. In a scene with no water that
	// pass also drags in a copy of the scene colour and a second render pass;
	// see docs/agents/environment.md.
	//
	// The pass itself is the engine's, and so is its shape: see
	// glyphengine.LightShaftShape, whose defaults were measured on the palette
	// below. This field is only how much of it to ask for.
	LightShafts float32

	// LightShaftShape is how the shafts look, where LightShafts is how strong
	// they are. The zero value is the engine's default shape, so a Sky built
	// by hand gets it without asking.
	LightShaftShape glyph.LightShaftShape
}

// Cloud quality presets for Sky.CloudSteps.
const (
	// CloudsOff disables volumetric cumulus. Cirrus is controlled separately.
	CloudsOff = 0
	// CloudsLow is a coarse march: cloud shapes read correctly, edges are
	// softer and thin wisps can shimmer as the camera moves.
	CloudsLow = 16
	// CloudsHigh is the default.
	CloudsHigh = 32
)

// DefaultSky is a full sky: dome, volumetric clouds, stars, and both discs.
func DefaultSky() *Sky {
	return &Sky{Stars: true, StarDensity: 1, MilkyWay: 1, SunDisc: true, MoonDisc: true, CloudSteps: CloudsHigh, LightShafts: 0.25}
}
