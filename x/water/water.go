// Package water is the underwater volume: what a body of water does to the
// light that crosses it, between the eye and whatever the eye can see.
//
// It is an opinion, in the sense ADR 0012 gives that word. The absorption
// coefficients, the body colour, the scattering tint and the daylight ramps
// below are one particular water, carried over from the game they were tuned
// in, and a second game can reasonably want a different one. The engine's own
// water (docs/agents/water.md) is a *surface*: a Gerstner mesh that refracts
// the bed it was built over and reflects the sky by Fresnel. This package adds
// the half of the problem that surface cannot see, because it is on the wrong
// side of it -- the volume the camera is standing in once it goes under.
//
// Three application passes, in frame-graph order:
//
//	water scattering   half resolution, its own RGBA16F target. Single
//	                   scattering of refracted sunlight along the view ray,
//	                   plus the scene depth it was sampled at, in alpha.
//	water composite    full resolution, its own RGBA16F target. Reads scene
//	                   colour and depth, absorbs the scene by the water path
//	                   length, adds the body's own radiance, and reconstructs
//	                   the half-resolution scattering with a depth-aware
//	                   bilateral filter.
//	water present      replaces the HDR scene with the composite.
//
// The passes are created disabled. SetEnabled is the game's intent; Update
// decides per frame whether that intent can do anything, and above the surface
// it cannot: the package contributes nothing at all when the eye is out of the
// water, and the frame is then byte-identical to one rendered without it. That
// test lives inside the package rather than in every game, which is the part of
// this that is reusable.
//
// # What this package deliberately does not own
//
// Two things sit next to it and are not in it. They are named here because the
// first reaction on reading this code is to wonder where they went.
//
// **The refracted sunlight field** -- the wave surface, its curvature, the
// Jacobian that turns a patch of sunlight into an intensity, and the cached
// caustic atlas built from them. It is left out on the evidence of who reads
// it: in the game this came from, the field's principal consumer is the
// *terrain* fragment shader lighting the seabed, not either water pass. A water
// package that owned it would be writing a light field for a shader it does not
// own, and would have to dictate the game's terrain shading to deliver it. It
// also costs three of the engine's four application texture slots, a compute
// dispatch, four more passes and a world-anchored atlas with its own drift
// policy -- all of which belong to the field, not to the water. Water samples
// it; water is not it. Until it exists, every sample in the integral below
// carries a focusing factor of exactly 1, which is the same value the source
// returns wherever its atlas is inactive or out of coverage, so this is a path
// that shipped rather than one invented here. The shafts are correspondingly
// smooth: they darken and redden with depth, and they have no caustic texture.
//
// **The air** -- Rayleigh and Mie scattering, the transmission LUT, the sky
// seen through Snell's window, and the analytic surface shading that reflects
// it. All of it integrates the atmosphere, which is a sibling system's job.
//
// # Units and frame
//
// Distances are engine world units throughout, and Absorption is per world
// unit. The surface is the horizontal plane at Options.Level, so "up" is +Y and
// depth is Level minus the eye's Y. The source this came from works in
// kilometres on a sphere; the conversion is in the field comments where it
// matters.
package water

import (
	_ "embed"
	"encoding/binary"
	"fmt"
	"math"

	"github.com/go-gl/mathgl/mgl32"

	"github.com/derekmwright/glyphengine/renderer"
	"github.com/derekmwright/glyphengine/shaders"
)

//go:embed water-scatter.frag.spv
var scatterSPV []byte

//go:embed water-composite.frag.spv
var compositeSPV []byte

//go:embed water-present.frag.spv
var presentSPV []byte

// Options is the water. Every field is a number the source game tuned, named
// for what it controls, and every default below is that game's value -- see
// DefaultOptions for where each one came from. Nothing in the shaders is
// hard-coded past what Options cannot describe, which is the wave spectrum and
// the focusing field, and neither of those is in this package.
//
// The zero value renders nothing: Clarity, ScatterSamples, ScatterSpan and
// ScatterScale all have to be positive. Start from DefaultOptions.
type Options struct {
	// Level is the world Y of the still surface, the same number the engine's
	// WaterOptions.Level carries. The two have to agree: this package decides
	// whether the camera is submerged from Level alone, and the engine decides
	// where to draw the surface from its own copy.
	Level float32

	// Clarity divides Absorption and BodyDepthFalloff, so one number moves the
	// view distance, the seabed light and the depth at which the body stops
	// brightening together -- they are one physical process and a game that
	// wants murkier water wants all three. Larger is clearer.
	Clarity float32

	// Absorption is how much each channel is absorbed per world unit of water
	// at Clarity 1, before Clarity divides it. Red goes first, which is why
	// the three numbers are so far apart and why deep water is blue.
	Absorption [3]float32

	// BodyColor is the water's own radiance -- what fills in as the scene
	// behind it is absorbed away. BodyNightFloor is the share of it that
	// survives with no sun at all, so a night dive is dark rather than black.
	BodyColor      [3]float32
	BodyNightFloor float32

	// BodyDepthFalloff is how fast the daylit part of BodyColor dies with the
	// camera's own depth, per world unit, before Clarity divides it. It is the
	// camera's depth and not the pixel's: it describes how much daylight
	// reaches the water the camera is standing in.
	BodyDepthFalloff float32

	// BodyDaylightOnset and BodyDaylightFull are the sun heights, as the sine
	// of its elevation, over which the body's daylit term ramps from nothing to
	// all of it. Onset is below zero on purpose: the water is still faintly lit
	// after the sun has set.
	BodyDaylightOnset float32
	BodyDaylightFull  float32

	// ScatterColor is the scattering coefficient per channel, multiplied by the
	// sun's colour. It is small because it is multiplied by a path length in
	// world units and by the phase function, which peaks well above 1.
	ScatterColor [3]float32

	// ScatterSamples is how many points along the view ray the integral uses.
	// Each one is a stratified position inside its own interval, jittered per
	// pixel, so raising it trades grain for cost without changing the energy.
	ScatterSamples int

	// ScatterSpan bounds the integration path in world units regardless of how
	// far the ray actually reaches. Past it the water has absorbed everything
	// that was going to scatter, and marching further only costs.
	ScatterSpan float32

	// ScatterMaxDepth is the camera depth past which the shafts are switched
	// off entirely: there is no sunlight left down there to scatter.
	ScatterMaxDepth float32

	// ScatterPhase is the Henyey-Greenstein asymmetry of the scattering, 0 for
	// isotropic and positive for forward scattering. Positive is what makes the
	// shafts brighten when you look toward the refracted sun.
	ScatterPhase float32

	// ScatterDaylightOnset and ScatterDaylightFull are the shafts' own sun
	// height ramp, separate from the body's because a visible shaft needs a
	// higher sun than a faintly lit volume does.
	ScatterDaylightOnset float32
	ScatterDaylightFull  float32

	// RefractiveIndex is water's, and it does one job here: it bends the
	// sunlight at the surface, which both tilts the shafts away from the sun's
	// own direction and lengthens the path the light travelled to each sample.
	RefractiveIndex float32

	// SunPathFloor clamps the cosine between the refracted sun and up when
	// converting a sample's depth into the distance the light travelled to
	// reach it. Without it a sun on the horizon divides by nearly zero and the
	// whole volume goes black.
	SunPathFloor float32

	// ScatterScale is the scattering target's size relative to the swapchain.
	// The integral is the expensive part and it is low-frequency, so it runs
	// small and is reconstructed against full-resolution depth. 1 disables the
	// reduction; the reconstruction is still bilateral and still correct.
	ScatterScale float32

	// DepthTolerance is the relative scene-depth difference at which a
	// neighbouring low-resolution sample stops counting during reconstruction.
	// It is relative because reverse-Z depth is not linear in distance, so a
	// fixed tolerance would be far too tight near the camera and useless away
	// from it.
	DepthTolerance float32
}

// DefaultOptions returns the source game's water, with the surface at level.
//
// Every number here was read out of that game's shaders rather than chosen, and
// each one says where. The citations are the point: they are what makes a later
// edit to one of these a decision someone took rather than a drift nobody
// noticed, and `TestDefaultsAreTheSourceConstants` pins them.
func DefaultOptions(level float32) Options {
	return Options{
		Level: level,
		// WATER_CLARITY, and the comment above it: "One shared setting controls
		// view absorption, seabed light, and cutoff distance."
		Clarity: 2.0,
		// WATER_ABSORPTION's numerator, per metre. The source's world unit is
		// the metre too -- its shaders carry kilometres and multiply path
		// lengths by 1000 before they reach this -- so the numbers transfer
		// unchanged.
		Absorption: [3]float32{0.20, 0.075, 0.035},
		// underwaterColor's fill term: vec3(0.006,0.035,0.047) scaled by
		// (0.01 + day*exp(-depth*0.025/clarity)).
		BodyColor:         [3]float32{0.006, 0.035, 0.047},
		BodyNightFloor:    0.01,
		BodyDepthFalloff:  0.025,
		BodyDaylightOnset: -0.12,
		BodyDaylightFull:  0.25,
		// underwaterShafts' output scale, vec3(0.0002,0.0006,0.0008).
		ScatterColor: [3]float32{0.0002, 0.0006, 0.0008},
		// Six, the source's uncached sample count. Its twelve is the cached
		// path's, and the cache is the field this package does not own; taking
		// twelve here would be paying for grain reduction the extra samples
		// only buy when there is a focusing term to resolve.
		ScatterSamples: 6,
		// min(travel, 60) and the depth > 80 early-out, both in metres.
		ScatterSpan:     60,
		ScatterMaxDepth: 80,
		// g in underwaterShafts' phase function.
		ScatterPhase: 0.65,
		// smoothstep(0.08, 0.4, dot(up, sun)) in underwaterShafts.
		ScatterDaylightOnset: 0.08,
		ScatterDaylightFull:  0.4,
		// The 1.0/1.333 that appears in every refract() call in the source.
		RefractiveIndex: 1.333,
		// max(dot(waterSun, up), 0.15) in underwaterShafts' transmission term.
		SunPathFloor: 0.15,
		// RenderTargetDesc{Scale: 0.5} on the source's "underwater light"
		// target, and the comment in underwaterShafts: "The default path
		// evaluates this at half resolution and upsamples with scene depth".
		ScatterScale: 0.5,
		// max(depth*0.08, 0.000002) in the source's water-composite bilateral
		// weight. The absolute floor is in the shader, where it belongs: it
		// exists to keep a background pixel's zero depth from dividing by zero,
		// which is not something a game tunes.
		DepthTolerance: 0.08,
	}
}

func (o Options) validate() error {
	bad := func(field string, why string) error {
		return fmt.Errorf("x/water: Options.%s: %s", field, why)
	}
	for _, f := range []struct {
		name  string
		value float32
	}{
		{"Clarity", o.Clarity}, {"ScatterSpan", o.ScatterSpan},
		{"ScatterScale", o.ScatterScale}, {"RefractiveIndex", o.RefractiveIndex},
		{"SunPathFloor", o.SunPathFloor}, {"DepthTolerance", o.DepthTolerance},
	} {
		if !(f.value > 0) || math.IsInf(float64(f.value), 0) {
			return bad(f.name, "must be positive and finite")
		}
	}
	if o.ScatterSamples < 1 {
		return bad("ScatterSamples", "must be at least 1")
	}
	if o.ScatterMaxDepth < 0 {
		return bad("ScatterMaxDepth", "must not be negative")
	}
	if o.ScatterDaylightFull <= o.ScatterDaylightOnset {
		return bad("ScatterDaylightFull", "must exceed ScatterDaylightOnset")
	}
	if o.BodyDaylightFull <= o.BodyDaylightOnset {
		return bad("BodyDaylightFull", "must exceed BodyDaylightOnset")
	}
	return nil
}

// Water owns the three passes and two targets. The renderer owns the Vulkan
// objects behind them and releases anything still live at Renderer.Destroy, so
// a game that keeps its water for the whole run need not call Destroy at all.
type Water struct {
	opts Options

	scatter, composite, present *renderer.AppPass
	scatterTarget, composed     *renderer.RenderTarget

	enabled bool

	// Retained so Update writes no garbage. SetPushConstants copies out of
	// these and keeps nothing, and the engine's own copy allocates nothing on
	// the success path, so a frame that only calls Update allocates zero bytes.
	// TestUpdateAllocatesNothing pins the Go half; the gate's -allocs mode
	// measures a real frame loop.
	scatterPush   [128]byte
	compositePush [128]byte
}

// New creates the targets and the passes. The passes start disabled, so a game
// that creates the water and says nothing else renders exactly as it did.
func New(r *renderer.Renderer, opts Options) (*Water, error) {
	if r == nil {
		return nil, fmt.Errorf("x/water: nil renderer")
	}
	if err := opts.validate(); err != nil {
		return nil, err
	}
	w := &Water{opts: opts}
	var err error
	// RGBA16F rather than RGB: the alpha channel carries the scene depth each
	// low-resolution sample was taken at, which is what the reconstruction
	// weighs its neighbours by. A separate depth target would be a second image
	// and a second read for one float.
	if w.scatterTarget, err = r.CreateRenderTarget(renderer.RenderTargetDesc{
		Name: "water scattering", Format: renderer.TargetRGBA16F, Scale: opts.ScatterScale,
	}); err != nil {
		w.Destroy(r)
		return nil, err
	}
	if w.composed, err = r.CreateRenderTarget(renderer.RenderTargetDesc{
		Name: "water composed scene", Format: renderer.TargetRGBA16F, Scale: 1,
	}); err != nil {
		w.Destroy(r)
		return nil, err
	}
	depth := r.SceneDepth()
	// StageBeforeBloom for all three: it is the first stage where the HDR scene
	// is complete *including the engine's water surface*, and the surface is
	// part of what the volume absorbs. StageAfterScene runs before water copies
	// the scene, so the surface would be missing from what we absorb.
	if w.scatter, err = r.CreateAppPass(renderer.AppPassDesc{
		Name: "water scattering", Stage: renderer.StageBeforeBloom, Target: w.scatterTarget,
		Fullscreen: true, Vert: shaders.DepthResolveVertSpv, Frag: scatterSPV,
		Reads: []*renderer.Texture{depth}, Timed: true,
	}); err != nil {
		w.Destroy(r)
		return nil, err
	}
	if w.composite, err = r.CreateAppPass(renderer.AppPassDesc{
		Name: "water composite", Stage: renderer.StageBeforeBloom, Target: w.composed,
		Fullscreen: true, Vert: shaders.DepthResolveVertSpv, Frag: compositeSPV,
		Reads: []*renderer.Texture{r.SceneColor(), depth, w.scatterTarget.Texture()}, Timed: true,
	}); err != nil {
		w.Destroy(r)
		return nil, err
	}
	// The composite cannot write HDR directly, because it reads scene colour
	// and a pass may not sample its own destination. Hence the copy: one
	// fullscreen triangle, which is the price of reading what you replace.
	if w.present, err = r.CreateAppPass(renderer.AppPassDesc{
		Name: "water present", Stage: renderer.StageBeforeBloom, Target: nil, Load: true,
		Fullscreen: true, Vert: shaders.DepthResolveVertSpv, Frag: presentSPV,
		Reads: []*renderer.Texture{w.composed.Texture()}, Timed: true,
	}); err != nil {
		w.Destroy(r)
		return nil, err
	}
	w.apply(false)
	return w, nil
}

// Options returns the options the water was created with.
func (w *Water) Options() Options { return w.opts }

// SetEnabled records the game's intent. It does not by itself switch the passes
// on: Update decides, every frame, whether the intent can do anything, and
// above the surface it cannot.
func (w *Water) SetEnabled(enabled bool) { w.enabled = enabled }

// Enabled reports the intent SetEnabled recorded, not whether the passes ran.
func (w *Water) Enabled() bool { return w.enabled }

// Update hands the passes this frame's camera and sun, and returns whether the
// water contributed anything. Call it once per frame, after the camera has
// moved -- LateUpdate is the hook that guarantees that.
//
// inverseVP is the inverse of Engine.ViewProjection, eye is the camera's world
// position, sunDir points toward the light and sunColor is its colour, both as
// Scene.Environment reports them. They are parameters rather than an *Engine so
// that the packing they feed can be tested without a window.
func (w *Water) Update(inverseVP mgl32.Mat4, eye mgl32.Vec3, sunDir, sunColor [3]float32) error {
	// The live extent, not ScatterScale times the window: the renderer rounds a
	// relative target's size, and a reconstruction one texel out of register
	// draws a seam along every silhouette. Reading it here is also what makes a
	// swapchain resize correct on the frame after it, without a resize hook.
	tw, th := w.scatterTarget.Extent()
	active := w.pack(inverseVP, eye, sunDir, sunColor, float32(tw), float32(th))
	w.apply(active)
	if !active {
		return nil
	}
	if err := w.scatter.SetPushConstants(w.scatterPush[:]); err != nil {
		return err
	}
	return w.composite.SetPushConstants(w.compositePush[:])
}

func (w *Water) apply(active bool) {
	w.scatter.SetEnabled(active)
	w.composite.SetEnabled(active)
	w.present.SetEnabled(active)
}

// Destroy releases the passes and then the targets, in that order, because
// destroying a target also destroys the passes writing it and a game reading
// the returned error wants the pass failure, not a surprise. It is idempotent
// and safe on a partially constructed Water, which is how New unwinds.
func (w *Water) Destroy(r *renderer.Renderer) {
	for _, p := range []**renderer.AppPass{&w.present, &w.composite, &w.scatter} {
		if *p != nil {
			r.DestroyAppPass(*p)
			*p = nil
		}
	}
	for _, t := range []**renderer.RenderTarget{&w.composed, &w.scatterTarget} {
		if *t != nil {
			r.DestroyRenderTarget(*t)
			*t = nil
		}
	}
}

// pack fills both push blocks and reports whether the water does anything this
// frame. It is separate from Update so a test can read what the shaders will
// see without a GPU, and so the allocation check has something to call.
//
// Three kinds of work are done here rather than per pixel, and all three are
// per-frame scalars: the daylight ramps, the camera-depth falloff, and the
// refraction of the sun at the surface. Hoisting them is not only cheaper --
// it is what fits the whole parameter set into the 128 application push bytes,
// which is the only per-pass uniform storage the engine offers an application
// pass. See water.md.
func (w *Water) pack(inverseVP mgl32.Mat4, eye mgl32.Vec3, sunDir, sunColor [3]float32, scatterWidth, scatterHeight float32) bool {
	depth := w.opts.Level - eye.Y()
	if !w.enabled || depth <= 0 {
		return false
	}

	up := mgl32.Vec3{0, 1, 0}
	sun := mgl32.Vec3(sunDir)
	if sun.Len() > 0 {
		sun = sun.Normalize()
	}
	// Both ramps read the sine of the sun's elevation, which for a horizontal
	// surface is dot(up, sun) -- the source's spherical dot(normalize(eye),
	// sun) with the sphere flattened.
	sunY := sun.Dot(up)
	scatterDay := smoothstep(w.opts.ScatterDaylightOnset, w.opts.ScatterDaylightFull, sunY)
	bodyDay := smoothstep(w.opts.BodyDaylightOnset, w.opts.BodyDaylightFull, sunY)

	// Where the sun is once the surface has bent it: a direction pointing *at*
	// the refracted sun, not the way the light travels. refract() takes the
	// incident direction, which is the way the light travels, so the sun vector
	// is negated going in and the result is negated coming back out -- the same
	// two negations the source writes, and they matter, because the phase
	// function's cosine below is taken against the view ray as it leaves the eye.
	waterSun := refract(sun.Mul(-1), up, 1/w.opts.RefractiveIndex).Mul(-1)
	// Entering a denser medium cannot total-internally-reflect, so refract only
	// returns zero here for a degenerate sun vector.
	if waterSun.Len() == 0 {
		waterSun = up
	} else {
		waterSun = waterSun.Normalize()
	}
	// A sample at depth d was reached by light that travelled d / cos from the
	// surface, where cos is how steeply the refracted sun descends. The floor
	// is what keeps a horizon sun from dividing by nothing.
	sunPathScale := 1 / maxf(waterSun.Dot(up), w.opts.SunPathFloor)

	absorption := [3]float32{
		w.opts.Absorption[0] / w.opts.Clarity,
		w.opts.Absorption[1] / w.opts.Clarity,
		w.opts.Absorption[2] / w.opts.Clarity,
	}

	// Past ScatterMaxDepth the source's integral returns exactly zero, and so
	// does a sunless one. Both are hoisted to here: an additive term that is
	// zero everywhere is the same frame as no term, and this way the march
	// costs nothing instead of costing six samples of nothing. The pass still
	// runs and still clears its target, so the composite never reads a stale
	// one -- a disabled pass would leave last frame's shafts in the image.
	shaftLight := [3]float32{}
	if depth <= w.opts.ScatterMaxDepth && scatterDay > 0 {
		for i := range shaftLight {
			shaftLight[i] = sunColor[i] * w.opts.ScatterColor[i] * scatterDay
		}
	}

	// exp(-depth * falloff / clarity): how much daylight reaches the water the
	// camera is in. BodyNightFloor is the share that does not depend on the sun
	// at all.
	reach := w.opts.BodyNightFloor + bodyDay*expf(-depth*w.opts.BodyDepthFalloff/w.opts.Clarity)
	var bodyRadiance [3]float32
	for i := range bodyRadiance {
		bodyRadiance[i] = w.opts.BodyColor[i] * reach
	}

	p := packer{buf: w.scatterPush[:]}
	p.mat4(inverseVP)
	p.vec4(waterSun.X(), waterSun.Y(), waterSun.Z(), sunPathScale)
	p.vec4(shaftLight[0], shaftLight[1], shaftLight[2], w.opts.Level)
	p.vec4(absorption[0], absorption[1], absorption[2], w.opts.ScatterPhase)
	p.vec4(w.opts.ScatterSpan, float32(w.opts.ScatterSamples), scatterWidth, scatterHeight)

	c := packer{buf: w.compositePush[:]}
	c.mat4(inverseVP)
	c.vec4(absorption[0], absorption[1], absorption[2], w.opts.Level)
	c.vec4(bodyRadiance[0], bodyRadiance[1], bodyRadiance[2], w.opts.DepthTolerance)
	// The composite reads 24 of the 128 bytes. The tail is written rather than
	// left alone because SetPushConstants wants a multiple of 16 and because a
	// shader that later grows a field should find zeros there, not whatever the
	// scatter block happened to put in the same buffer.
	c.vec4(0, 0, 0, 0)
	c.vec4(0, 0, 0, 0)
	return true
}

// packer writes little-endian float32s into a fixed buffer. SetPushConstants
// wants a multiple of 16 bytes, so both blocks are the full 128 and the tail is
// zeroed by construction.
type packer struct {
	buf []byte
	at  int
}

func (p *packer) f32(v float32) {
	binary.LittleEndian.PutUint32(p.buf[p.at:], math.Float32bits(v))
	p.at += 4
}

func (p *packer) vec4(a, b, c, d float32) { p.f32(a); p.f32(b); p.f32(c); p.f32(d) }

func (p *packer) mat4(m mgl32.Mat4) {
	for i := 0; i < 16; i++ {
		p.f32(m[i])
	}
}

func smoothstep(edge0, edge1, x float32) float32 {
	t := (x - edge0) / (edge1 - edge0)
	t = clampf(t, 0, 1)
	return t * t * (3 - 2*t)
}

// refract is GLSL's refract for unit vectors: i is the direction light travels,
// n faces the side i comes from, and eta is the ratio of the indices. A zero
// vector means total internal reflection, exactly as GLSL returns.
func refract(i, n mgl32.Vec3, eta float32) mgl32.Vec3 {
	d := n.Dot(i)
	k := 1 - eta*eta*(1-d*d)
	if k < 0 {
		return mgl32.Vec3{}
	}
	return i.Mul(eta).Sub(n.Mul(eta*d + sqrtf(k)))
}

func clampf(v, lo, hi float32) float32 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func maxf(a, b float32) float32 {
	if a > b {
		return a
	}
	return b
}

func sqrtf(v float32) float32 { return float32(math.Sqrt(float64(v))) }
func expf(v float32) float32  { return float32(math.Exp(float64(v))) }
