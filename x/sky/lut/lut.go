// Package lut is a second sky for the engine's sky slot, whose whole per-pixel
// cost is a lookup: one table, baked on the CPU at startup, read twice per
// fragment.
//
// It is a sibling of x/sky rather than a mode of it. The two share the seam --
// glyphengine.EnvironmentSource and renderer.ShaderSet's sky slot -- and
// nothing else: no Go identifier, no shader, no table. That is the point of the
// step this package is: a slot that only ever had one filling is a slot by
// assertion, and ADR 0012's claim that the look moved out of the engine is only
// proved by a second look moving in.
//
// What it is for is a target that cannot afford x/sky. x/sky's dome is cheap on
// its own, but its sky is a cloud march, a star field, two billboards and a
// screen-space shaft pass as well, and on hardware where the frame is already
// over budget all of that has to go before the sky does. This draws the dome
// and nothing else:
//
//	e, err := glyph.New(&game{}, glyph.WithShaders(skylut.Shaders()))
//	...
//	func (g *game) Init(e *glyph.Engine) error {
//	    sky, err := skylut.New(e.Renderer(), skylut.DefaultOptions())
//	    if err != nil {
//	        return err
//	    }
//	    e.Scene.Env = sky
//	    return nil
//	}
//
// # Two halves, and you need both
//
// Shaders fills the one stage this package supplies and New returns the
// EnvironmentSource that asks for it. Neither errors without the other, exactly
// as x/sky's page records: a source asking for a dome the renderer has no
// pipeline for renders the clear colour with the right light on it, and a
// pipeline nothing asks for is built and never used. The halves also include a
// third thing here, which x/sky does not have: the table. New uploads it and
// binds it to an application shader-texture slot, so a game that constructs the
// source without calling New -- there is no way to, which is why New takes the
// renderer -- would sample the white fallback and get a white sky.
//
// # What it does not have
//
// No clouds, no stars, no Milky Way, no sun or moon billboard, no light shafts
// and no moon. StarsFrag and CloudsFrag stay nil, so the engine builds no
// pipeline and records no draw for either, and the state leaves DrawStars,
// DrawSun and DrawMoon false so the engine places no bodies. The sun is visible
// as the scattering halo the table carries around its direction, which is what
// a dome has instead of a disc.
//
// The moon is the one that is a decision rather than an omission. x/sky hands
// the scene's single directional light from the sun to the moon as the sun
// sets, which needs a second body, a second intensity ramp and a handover
// window placed so that no shadow in the scene flips direction in one frame --
// three tuned things for a light that is a twentieth of the sun's. Here the
// directional light simply fades out with the sun and night is ambient plus
// whatever lamps the game placed. See Key.
//
// # Where the numbers come from
//
// Two different places, and the difference matters for keeping them honest.
//
// The KEYS are x/sky's, sampled. DefaultKeys is x/sky's day cycle read at four
// times of day -- midnight, sunrise, noon and sunset -- so the two skies put
// the sun in the same place and light the ground the same colour at those four
// hours. TestDefaultKeysAreXSkysCycle holds that by importing x/sky and
// comparing; it is the only place in this package that mentions x/sky, and it
// is a test rather than the package so nothing here depends on it at run time.
//
// The TABLE is a re-derivation. The dome's model lives in GLSL -- the engine's
// shaders/include/atmosphere.inc plus the gradient in x/sky's sky.frag -- and
// there is no way to evaluate GLSL from Go, so bake.go computes the same
// functions again in Go. That is a copy, with everything a copy implies: see
// bake.go, where the drift is named and TestAtmosphereIncStillSaysWhatWeCopied
// is what notices it.
package lut

import (
	_ "embed"
	"fmt"
	"math"

	glyph "github.com/derekmwright/glyphengine"
	"github.com/derekmwright/glyphengine/renderer"
)

// The dome's fragment stage, committed as SPIR-V and embedded for the reason
// AGENTS.md rule 2 gives for the engine's own: a game that `go get`s this has
// the module and not a Vulkan SDK. spirv_test.go is both the check that it
// matches the GLSL beside it and the generator that writes it.
//
//go:embed skylut.frag.spv
var skyFragSPV []byte

// ShaderTextureSlot is the application shader-texture slot New binds the table
// to, and it is fixed rather than an option because skylut.frag names the
// binding it reads at compile time: slot n is light-set binding 7+n, and a
// configurable slot would mean four compiled variants of the same shader.
//
// It is the LAST of the four rather than the first so that a game allocating
// slots from zero -- which is what every example that uses them does, and what
// examples/24-custom-passes does at binding 7 -- collides with this last
// instead of first. A game that needs all four cannot use this sky; that is a
// real limit and it is on the page rather than discovered at a white horizon.
const ShaderTextureSlot = renderer.ShaderTextureSlots - 1

// Shaders is the sky slot with this package's dome in it and the other two
// stages left nil.
//
// Nil is not "take the engine's" for these three -- the engine embeds no dome,
// stars or clouds at all -- so this is also what turns the star and cloud draws
// off. See renderer.ShaderSet's sky slot.
func Shaders() renderer.ShaderSet {
	return Fill(renderer.ShaderSet{})
}

// Fill returns s with this package's dome set if it is not set already, for a
// game that is overriding other stages:
//
//	custom := renderer.DefaultShaders()
//	custom.LitFrag = myLitFragSpv
//	glyph.WithShaders(skylut.Fill(custom))
//
// Unlike x/sky's Fill it touches one stage, because there is only one. A game
// that wants this dome under x/sky's clouds can have it -- pass this set
// through xsky.Fill afterwards -- but the clouds will be marched at their full
// cost, which is most of what choosing this sky was for.
func Fill(s renderer.ShaderSet) renderer.ShaderSet {
	if s.SkyFrag == nil {
		s.SkyFrag = skyFragSPV
	}
	return s
}

// Key is the cycle at one time of day: where the sun is, what colour it is at
// full strength, and what the ambient fill is.
//
// Four of these are a day (see DefaultKeys). The driver interpolates between
// adjacent keys and wraps from the last back to the first, so the keys are a
// closed loop and midnight needs no special case.
//
// SunColor is the sun's colour at FULL STRENGTH, before the fade that takes it
// out as it sets. The fade is a function of elevation rather than of the clock
// -- the same smoothstep x/sky's DayNight.SunIntensity uses, re-derived in
// sunIntensity -- and that is deliberate rather than tidiness: with the fade
// keyed instead, interpolating from a black midnight toward a lit sunrise lights
// the scene from a sun 40 degrees below the horizon for a sixth of the cycle.
// Keying the full-strength colour and fading it on elevation means the
// directional light goes out when the sun goes down, which is the one thing
// about a four-key day that must not be approximate.
//
// There is no moon key for the same reason the package has no moon; see the
// package doc.
type Key struct {
	// TimeOfDay is [0,1), 0 being midnight, as x/sky's DayNight.TimeOfDay is.
	TimeOfDay float32

	// SunDir points toward the sun. It does not have to be unit length: the
	// driver normalizes the interpolated vector, since interpolating two unit
	// vectors does not give one.
	SunDir [3]float32

	// SunColor is the directional light's colour with the sun high, before the
	// elevation fade. Ambient is the uniform fill, which is not faded: it is
	// the sky's own contribution and the keys already carry it going dark.
	SunColor [3]float32
	Ambient  [3]float32
}

// DefaultKeys is x/sky's day cycle at four times of day: midnight, sunrise,
// noon and sunset.
//
// The sun directions are x/sky's orbit evaluated at those four times -- the
// same expression, in the same order, so the two agree to the bit at the keys
// and TestDefaultKeysAreXSkysCycle can compare them exactly. Between keys they
// part company, because this interpolates the direction and x/sky walks the
// circle: the largest disagreement in the sun's elevation over the whole cycle
// is recorded in that test, which is also where the bound lives.
//
// The colours are x/sky's keyframe tables sampled at the same four times:
// sunColorKeyframes for SunColor (whose values at 0.25, 0.5 and 0.75 are exactly
// the table's own keys, and at 0 are its clamped first key), and
// ambientKeyframes for Ambient. Four keys is a quarter of x/sky's twelve ambient
// keys, and the difference shows where that table is densest -- the two or three
// hundredths of the cycle either side of sunrise and sunset, which is where
// x/sky deliberately put extra keys to give dusk a shape. Dawn therefore arrives
// earlier here and more gradually. That is the look this sky has, not a bug to
// fix by adding keys; a game that wants x/sky's dusk can hand Options a longer
// list, or use x/sky.
func DefaultKeys() []Key {
	return []Key{
		// Midnight. The sun is nearly straight down, so sunIntensity is zero and
		// SunColor is unread -- it is the table's clamped first key rather than
		// black, so that interpolating toward sunrise moves the hue and not the
		// strength.
		{TimeOfDay: 0.00, SunDir: sunDirAt(0.00), SunColor: [3]float32{1.0, 0.6, 0.3}, Ambient: [3]float32{0.010, 0.013, 0.030}},
		// Sunrise: the sun on the horizon, orange, with the warm dim ambient
		// x/sky gives the first minutes of the day.
		{TimeOfDay: 0.25, SunDir: sunDirAt(0.25), SunColor: [3]float32{1.0, 0.6, 0.3}, Ambient: [3]float32{0.140, 0.115, 0.105}},
		// Noon.
		{TimeOfDay: 0.50, SunDir: sunDirAt(0.50), SunColor: [3]float32{1.0, 1.0, 0.95}, Ambient: [3]float32{0.250, 0.250, 0.300}},
		// Sunset. Redder than sunrise by a tenth in green and blue, which is
		// x/sky's table and the one asymmetry between its mornings and its
		// evenings. See glowColorAt for the one place this package cannot keep
		// it.
		{TimeOfDay: 0.75, SunDir: sunDirAt(0.75), SunColor: [3]float32{1.0, 0.5, 0.2}, Ambient: [3]float32{0.140, 0.105, 0.100}},
	}
}

// sunDirAt is x/sky's orbit: the sun circles in the XY plane with a fixed Z
// tilt, above the horizon for the half of the cycle from 0.25 to 0.75.
//
// Transcribed from x/sky's DayNight.SunDir rather than called, because this
// package does not import it, and transcribed expression for expression
// including the order of the normalize so that the two produce the same
// float32s rather than merely the same angle. An orbit is geometry and not a
// look, which is why copying it is the right call where copying the keyframe
// tables would not have been.
func sunDirAt(timeOfDay float32) [3]float32 {
	angle := (float64(timeOfDay) - 0.25) * 2 * math.Pi
	x := float32(math.Cos(angle))
	y := float32(math.Sin(angle))
	z := float32(0.2)
	l := float32(math.Sqrt(float64(x*x + y*y + z*z)))
	return [3]float32{x / l, y / l, z / l}
}

// Options is the sky. Every field has a working default; start from
// DefaultOptions.
type Options struct {
	// Keys are the cycle, in ascending TimeOfDay. At least one is required and
	// the list wraps, so a single key is a sky frozen at one hour.
	Keys []Key

	// Palette is the six colours the dome blends between, and it is BAKED INTO
	// THE TABLE rather than read per frame.
	//
	// That is the one real cost of this sky over x/sky's: Scene.SetSkyPalette
	// moves the fog, the water's reflection and every lit surface's night grade,
	// and it does not move this dome, because the dome is a texture that was
	// baked before the scene said anything. A game with its own palette sets it
	// in BOTH places -- here and on the scene -- and the two have to agree or
	// the result is the mistake environment.md describes under "a sky that is
	// not Earth's", with the dome and the haze from different planets.
	//
	// The zero value means glyphengine.DefaultSkyPalette, which is what a new
	// Scene starts with, so a game that says nothing gets a matched pair.
	Palette glyph.SkyPalette

	// Fog blends distant geometry toward the horizon. Nil disables it. It is
	// passed through to glyphengine.StaticSource, so the fields mean exactly
	// what they mean there.
	Fog *glyph.Fog

	// ClearColor is what the frame clears to. The dome is opaque and drawn over
	// every pixel nothing else reached, so it is not normally visible; it is
	// here because StaticSource resolves it and a game that turns the dome off
	// by not passing Shaders still gets a frame.
	ClearColor [3]float32

	// TimeOfDay is the clock's starting value, wrapped into [0,1). Speed is
	// cycles per simulation second, so 1.0/120 is a two-minute day and 0 is a
	// sky that does not move.
	TimeOfDay float32
	Speed     float32
}

// DefaultOptions is Earth at sunrise with light haze and a stationary clock,
// which is the same world x/sky's DefaultEnvironment describes.
//
// The clock is frozen for the same reason x/sky's is: time passing is a decision
// a game should make deliberately. Set Speed to start it.
func DefaultOptions() Options {
	return Options{
		Keys:      DefaultKeys(),
		Palette:   glyph.DefaultSkyPalette(),
		Fog:       &glyph.Fog{Density: glyph.DefaultFogDensity},
		TimeOfDay: 0.25,
	}
}

func (o Options) validate() error {
	if len(o.Keys) == 0 {
		return fmt.Errorf("x/sky/lut: no keys; DefaultKeys is four")
	}
	for i, k := range o.Keys {
		if k.TimeOfDay < 0 || k.TimeOfDay >= 1 {
			return fmt.Errorf("x/sky/lut: key %d has TimeOfDay %g, outside [0,1)", i, k.TimeOfDay)
		}
		if i > 0 && k.TimeOfDay <= o.Keys[i-1].TimeOfDay {
			return fmt.Errorf("x/sky/lut: key %d is at %g, not after key %d at %g; keys must ascend",
				i, k.TimeOfDay, i-1, o.Keys[i-1].TimeOfDay)
		}
		if length3(k.SunDir) == 0 {
			return fmt.Errorf("x/sky/lut: key %d has a zero SunDir", i)
		}
	}
	return nil
}

// Sky is the baked table, the clock that drives it, and the EnvironmentSource
// the renderer reads each frame.
type Sky struct {
	opts    Options
	palette glyph.SkyPalette

	tex  *renderer.Texture
	time float32

	// The light and the air this frame, resolved by resolve rather than by
	// State, for two reasons. State must not mutate -- EnvironmentSource says so
	// -- and it is called once per rendered frame while the clock only moves on
	// the fixed tick, so resolving here is also resolving once per change
	// instead of once per frame. Addressed by State and handed to
	// glyphengine.StaticSource, which is what keeps State allocation-free.
	sun     glyph.DirectionalLight
	ambient glyph.AmbientLight
}

// New bakes the table, uploads it, and binds it to ShaderTextureSlot.
//
// The bake is CPU work at startup -- 65,536 evaluations of the dome model, a
// few milliseconds -- and it happens once. Nothing here recomputes it: the
// clock moves the sun's elevation, which is one of the table's axes, so a day
// passing is a different place in the same texture rather than a different
// texture.
//
// It used to return an error for a palette brighter than the table's 8-bit
// encoding could hold. There is no such ceiling now; see Bake.
func New(r *renderer.Renderer, opts Options) (*Sky, error) {
	if r == nil {
		return nil, fmt.Errorf("x/sky/lut: nil renderer")
	}
	if err := opts.validate(); err != nil {
		return nil, err
	}
	s := &Sky{opts: opts, palette: opts.Palette}
	if s.palette == (glyph.SkyPalette{}) {
		s.palette = glyph.DefaultSkyPalette()
	}
	pixels, w, h, err := bake(s.palette, opts.Keys)
	if err != nil {
		return nil, err
	}
	// Linear filtering because the sampler is what interpolates two of the
	// table's three axes -- the view elevation and the proximity to the sun; see
	// skylut.frag, which filters only the third itself. Nearest would turn the
	// gradient into 64 visible bands.
	//
	// Clamp-to-edge and no mip chain because nothing reaches either. Every axis
	// is inset half a texel, so no sample lands close enough to an edge for the
	// addressing mode to matter, and skylut.frag fetches with textureLod at level
	// 0. The eight-bit constructor this used -- CreateDataTexture -- built the
	// chain anyway: 11 unused levels, about 85 KB on top of the table. The wide
	// table is 512 KB where the eight-bit one was 256, and asking for no chain
	// gives 85 of those 256 back.
	if s.tex, err = r.CreateTextureRGBA16F(pixels, w, h, renderer.TextureOptions{Filter: renderer.FilterLinear}); err != nil {
		return nil, fmt.Errorf("x/sky/lut: upload the table: %w", err)
	}
	if err := r.SetShaderTexture(ShaderTextureSlot, s.tex); err != nil {
		r.DestroyTexture(s.tex)
		s.tex = nil
		return nil, fmt.Errorf("x/sky/lut: bind the table: %w", err)
	}
	s.SetTimeOfDay(opts.TimeOfDay)
	return s, nil
}

// Destroy releases the slot and then the table. It is idempotent.
//
// The slot is cleared first: a destroyed texture left in a slot falls back to
// white on the next flush anyway, but a white dome is a confusing way to learn
// that a sky was destroyed while a scene still pointed at it.
func (s *Sky) Destroy(r *renderer.Renderer) {
	if s == nil || s.tex == nil {
		return
	}
	_ = r.SetShaderTexture(ShaderTextureSlot, nil)
	r.DestroyTexture(s.tex)
	s.tex = nil
}

// Options returns the options the sky was created with.
func (s *Sky) Options() Options { return s.opts }

// TimeOfDay is the clock, in [0,1).
func (s *Sky) TimeOfDay() float32 { return s.time }

// SetTimeOfDay sets the clock, wrapping values outside [0,1) the way Advance
// does, and resolves the light and the air at the new time.
//
// The wrap is here rather than left to the caller for the reason x/sky's
// SetTimeOfDay records: a game assigning an unwrapped 1.2 or -0.1 would get a
// clamped palette endpoint instead of the hour it asked for.
func (s *Sky) SetTimeOfDay(t float32) {
	s.time = t - float32(math.Floor(float64(t)))
	s.resolve()
}

// Speed is cycles per simulation second. SetSpeed changes it; 0 freezes the sky.
func (s *Sky) Speed() float32     { return s.opts.Speed }
func (s *Sky) SetSpeed(v float32) { s.opts.Speed = v }

// Advance steps the clock. Scene.Tick calls it on the fixed tick, so the sky
// runs in simulation seconds and a paused scene has a stationary sun.
func (s *Sky) Advance(dt float32) {
	if s.opts.Speed == 0 {
		return
	}
	s.SetTimeOfDay(s.time + s.opts.Speed*dt)
}

// State is this frame's environment: the light and the air from
// glyphengine.StaticSource, plus the one flag that asks for the dome.
//
// Calling StaticSource rather than repeating it is the same choice x/sky's
// fixed-hour path makes, and for the same reason: a fixed sun, a fixed ambient,
// the fog and the clear colour are already resolved in exactly one place on the
// engine's side of the seam, and a second copy out here would be free to drift
// across a module boundary where no compiler would notice.
//
// SunElevation is the one field StaticSource does not fill, because a fixed sun
// can want a sky at an elevation its light does not come from. Here they are the
// same vector -- there is no sun/moon handover to make them differ -- so the
// dome's elevation is the light's.
func (s *Sky) State() glyph.EnvironmentState {
	base := glyph.StaticSource{Sun: &s.sun, Ambient: &s.ambient, Fog: s.opts.Fog, ClearColor: s.opts.ClearColor}
	st := base.State()
	st.SunElevation = s.sun.Direction[1]
	st.DrawSky = true
	// Everything else is deliberately left at its zero value, and the zeros are
	// load-bearing rather than unset: DrawStars, DrawSun and DrawMoon say the
	// engine places no bodies, CloudSteps and Cirrus say there is nothing to
	// march, LightShafts says there is no disc to radiate from, and SkyPalette
	// and NightGrade are the sentinel for "the scene's" -- so Scene.SetSkyPalette
	// still reaches the fog and the water even though it cannot reach this dome.
	return st
}

// resolve interpolates the keys at the current time and writes the light and the
// ambient State will hand to StaticSource.
func (s *Sky) resolve() {
	a, b, frac := segmentAt(s.opts.Keys, s.time)
	dir := normalize3(lerp3(a.SunDir, b.SunDir, frac))
	if length3(dir) == 0 {
		// Two opposite keys, exactly half way between them. validate rejects a
		// zero SunDir but not an antipodal pair, and a zero direction here would
		// make the dome's own axis meaningless rather than merely wrong.
		dir = normalize3(a.SunDir)
	}
	col := lerp3(a.SunColor, b.SunColor, frac)
	i := sunIntensity(dir[1])
	s.sun = glyph.DirectionalLight{Direction: dir, Color: [3]float32{col[0] * i, col[1] * i, col[2] * i}}
	s.ambient = glyph.AmbientLight{Color: lerp3(a.Ambient, b.Ambient, frac)}
}

// segmentAt returns the keys bracketing t and how far between them it is,
// wrapping from the last key to the first through midnight.
func segmentAt(keys []Key, t float32) (a, b Key, frac float32) {
	i := -1
	for j := range keys {
		if keys[j].TimeOfDay <= t {
			i = j
		}
	}
	if i < 0 {
		// Before the first key, which is inside the segment that wraps.
		i = len(keys) - 1
	}
	a = keys[i]
	b = keys[(i+1)%len(keys)]
	t0, t1 := a.TimeOfDay, b.TimeOfDay
	if t1 <= t0 {
		t1 += 1
	}
	if t < t0 {
		t += 1
	}
	return a, b, (t - t0) / (t1 - t0)
}

// sunIntensity is how much directional light the sun delivers, from its
// elevation.
//
// x/sky's DayNight.SunIntensity, re-derived, including the lower edge below
// zero: the sun still lights the sky for a while after it sets, which is what
// civil twilight is. The two have to stay the same curve or the two skies stop
// agreeing about when the day ends, which is the one thing DefaultKeys is for.
func sunIntensity(sunY float32) float32 { return smoothstep(-0.14, 0.06, sunY) }

// smoothstep is GLSL's, so the curves computed here and the ones computed in a
// shader agree. Edges in either order, which is what sky.frag's below-horizon
// fade needs.
func smoothstep(edge0, edge1, x float32) float32 {
	if edge1 == edge0 {
		return 0
	}
	t := (x - edge0) / (edge1 - edge0)
	if t < 0 {
		t = 0
	} else if t > 1 {
		t = 1
	}
	return t * t * (3 - 2*t)
}

func lerp3(a, b [3]float32, t float32) [3]float32 {
	return [3]float32{
		a[0] + (b[0]-a[0])*t,
		a[1] + (b[1]-a[1])*t,
		a[2] + (b[2]-a[2])*t,
	}
}

func length3(v [3]float32) float32 {
	return float32(math.Sqrt(float64(v[0]*v[0] + v[1]*v[1] + v[2]*v[2])))
}

func normalize3(v [3]float32) [3]float32 {
	l := length3(v)
	if l == 0 {
		return [3]float32{}
	}
	return [3]float32{v[0] / l, v[1] / l, v[2] / l}
}
