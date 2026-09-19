package glyphengine

import (
	"fmt"
	"log"
	"math"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/go-gl/mathgl/mgl32"
	"github.com/vkngwrapper/core/v3/common"

	"github.com/derekmwright/glyphengine/ecs"
	"github.com/derekmwright/glyphengine/input"
	"github.com/derekmwright/glyphengine/renderer"
	"github.com/derekmwright/glyphengine/renderer/lightcluster"
	"github.com/derekmwright/glyphengine/window"
)

// DefaultTickRate is the fixed simulation rate in ticks per second.
const DefaultTickRate = 60

// DefaultMaxCatchUp is how much simulation time one frame may make up after a
// stall. See WithMaxCatchUp.
const DefaultMaxCatchUp = 250 * time.Millisecond

// Game is implemented by the game to set up its scene and run per-frame logic.
//
// Init runs once, after the window and renderer exist and before the first
// frame. Update runs once per rendered frame with the real frame delta in
// seconds, which makes it the only correct place to read input: GLFW events are
// polled per frame, so edge-triggered queries like KeyPressed are meaningful
// exactly once per frame.
//
// Implement the optional interfaces below for fixed-timestep simulation,
// post-simulation work, shutdown, and resize.
type Game interface {
	Init(e *Engine) error
	Update(e *Engine, dt float32)
}

// FixedUpdateGame is an optional Game interface. FixedUpdate runs on the fixed
// simulation tick, immediately after Scene.Tick, with the tick delta — never
// the frame delta. Put anything that must be deterministic here: character
// movement, physics-driven gameplay, anything a server also simulates.
//
// It runs zero, one, or several times per frame depending on how the frame
// rate divides into the tick rate. On a 144Hz display against a 60Hz tick,
// roughly 59% of frames run no tick at all.
//
// That is why input must not be read here. A KeyPressed edge inside
// FixedUpdate is silently dropped on a zero-tick frame and fired twice on a
// two-tick frame. Sample input in Update, latch the edges, and consume the
// latch here:
//
//	func (g *game) Update(e *glyphengine.Engine, dt float32) {
//		g.intent.Right, g.intent.Forward = g.binds.Direction(g.move)
//		if g.binds.Pressed(g.jump) {
//			g.jumpQueued = true // latch the edge
//		}
//	}
//
//	func (g *game) FixedUpdate(e *glyphengine.Engine, dt float32) {
//		intent := g.intent
//		intent.Jump = g.jumpQueued
//		g.jumpQueued = false // consume exactly once
//		e.MoveCharacter(g.player, intent, dt)
//	}
type FixedUpdateGame interface {
	FixedUpdate(e *Engine, dt float32)
}

// LateUpdateGame is an optional Game interface. LateUpdate runs once per frame
// with the frame delta, after every fixed tick and after animation sampling —
// so it sees final transforms and final poses for this frame.
//
// This is where a camera follows its target. Following in Update instead would
// read positions from before this frame's simulation, leaving the camera a
// tick behind whatever it is tracking.
type LateUpdateGame interface {
	LateUpdate(e *Engine, dt float32)
}

// ShutdownGame is an optional Game interface. Shutdown is called once after
// the main loop exits and before renderer and window teardown, so a game can
// save state while its resources are still valid.
type ShutdownGame interface {
	Shutdown(e *Engine)
}

// ResizeGame is an optional Game interface. OnResize is called when the
// framebuffer size changes, after the renderer has been notified.
type ResizeGame interface {
	OnResize(e *Engine, width, height int)
}

// Option configures an Engine at construction.
type Option func(*config)

type config struct {
	scene          *Scene
	msaa           int
	width          int
	height         int
	title          string
	appName        string
	appVersion     common.Version
	fullscreen     bool
	resizable      bool
	validation     bool
	vsync          bool
	interp         bool
	tickRate       int
	maxCatchUp     time.Duration
	maxFrames      int
	screenshot     string
	fixedFrameTime time.Duration
	fov            float32
	near, far      float32
	quitKey        input.Key
	hasQuitKey     bool
	debugKeys      bool
	shaders        renderer.ShaderSet
	hasShaders     bool
	uiGlow         bool
}

// rendererOptions translates the engine's config into the renderer's options.
//
// Split out of New so it can be tested without a GPU: the options are plain
// functions, so a test can apply them to a Renderer and read back what arrived.
// That is the failure this guards against -- an option that exists on Engine,
// is documented, and never reaches renderer.New.
func (c *config) rendererOptions() []renderer.Option {
	appName := c.appName
	if appName == "" {
		appName = c.title
	}
	opts := []renderer.Option{
		renderer.WithApplicationName(appName, c.appVersion),
		renderer.WithValidation(c.validation),
		renderer.WithVSync(c.vsync),
	}
	if c.msaa != 0 {
		opts = append(opts, renderer.WithMSAASamples(c.msaa))
	}
	if c.hasShaders {
		opts = append(opts, renderer.WithShaders(c.shaders))
	}
	if c.uiGlow {
		opts = append(opts, renderer.WithUIGlowLayer())
	}
	return opts
}

// WithScene injects an externally created Scene instead of building a fresh
// one — for a game that loads or replicates its world before the window opens.
func WithScene(s *Scene) Option {
	return func(c *config) { c.scene = s }
}

// WithMSAA requests an MSAA sample count (1, 2, 4, or 8); the renderer clamps
// it to what the GPU supports. Zero keeps the renderer default.
func WithMSAA(n int) Option {
	return func(c *config) { c.msaa = n }
}

// WithUIGlow gives the screen-space UI its own HDR layer, so a UI element can be
// brighter than 1 and bloom across the elements around it.
//
// Off by default and free when off. An element asks for glow through
// renderer.UIRenderObject.Glow or renderer.TextLine.Glow, both of which are inert
// without this; how the glow looks is tuned at run time through
// Renderer().SetUIGlow and Renderer().SetUIExposure, which are the game's to set
// rather than the engine's to decide.
//
// A straight passthrough to renderer.WithUIGlowLayer, and it exists for the same
// reason WithShaders does: the layer allocates targets at construction and on
// resize, so it cannot be reached by a game that only has an Engine otherwise.
// See docs/agents/overlay-composite.md.
func WithUIGlow() Option {
	return func(c *config) { c.uiGlow = true }
}

// WithShaders replaces the SPIR-V the renderer builds its pipelines from.
//
// Fields left nil fall back to the engine's embedded shader for that stage, so
// a game can override one pipeline without supplying all of them:
//
//	custom := renderer.DefaultShaders()
//	custom.SkyFrag = myAlienSkySpv
//	e, err := glyph.New(&game{}, glyph.WithShaders(custom))
//
// This is a straight passthrough to renderer.WithShaders, and exists because
// without it the seam was unreachable from Engine. A game that wanted a sky
// that is not Earth's had to call renderer.New directly and then reimplement
// the frame loop, the fixed timestep, interpolation, the draw-list build and
// the environment resolve that Run already provides -- a steep price for one
// field.
//
// See renderer.ShaderSet for what a replacement has to match. A mismatch is a
// pipeline-creation failure at startup or, worse, a shader that links and draws
// nothing, so develop one with WithValidation on.
func WithShaders(set renderer.ShaderSet) Option {
	return func(c *config) { c.shaders, c.hasShaders = set, true }
}

// WithValidation enables the Vulkan validation layer, which reports API misuse
// through the standard logger. Off by default — it costs frame time and needs
// the Vulkan SDK, which players do not have. A missing layer logs a warning
// instead of failing to start.
//
// GLYPHENGINE_VALIDATION=1 in the environment turns it on for any build,
// which is how to get validation out of a binary you did not compile.
func WithValidation(enabled bool) Option {
	return func(c *config) { c.validation = enabled }
}

// WithVSync controls frame pacing. It defaults to true.
//
// On, the presentation engine blocks at the refresh rate of the display the
// window is actually on — the right place for a frame limit, since it costs no
// CPU, follows the window between monitors, and cannot disagree with the
// hardware. A 144Hz display gets 144fps; a 60Hz one gets 60.
//
// Off, rendering runs unbounded. Use it for benchmarking, profiling, or
// latency-sensitive input, and expect a pegged GPU.
//
// Note that the simulation rate is independent either way — see WithTickRate.
func WithVSync(enabled bool) Option {
	return func(c *config) { c.vsync = enabled }
}

// WithInterpolation controls whether rendering blends between simulation
// ticks. It defaults to true.
//
// Simulation runs at a fixed rate (60Hz by default) while rendering runs at the
// display rate. Without interpolation a 144Hz monitor draws each simulated
// position for two or three frames and then jumps, which reads as stutter on
// anything the tick moves. With it on, each frame draws the transform blended
// between the last two ticks.
//
// The cost is one transform copy per non-Static entity per tick. Turn it off
// for a game that moves nothing on the tick, or when profiling the simulation
// in isolation.
func WithInterpolation(enabled bool) Option {
	return func(c *config) { c.interp = enabled }
}

// WithWindowSize sets the windowed-mode size in screen coordinates.
func WithWindowSize(width, height int) Option {
	return func(c *config) { c.width, c.height = width, height }
}

// WithTitle sets the window title. Unless WithApplicationName overrides it,
// the title is also what the engine reports to Vulkan as the application name.
func WithTitle(title string) Option {
	return func(c *config) { c.title = title }
}

// WithApplicationName overrides the application name and version reported to
// Vulkan, which otherwise default to the window title at version 0.1.0.
//
// Driver tools, GPU profilers, and vendor control panels display this, and
// some drivers key per-application optimizations off it — so a shipping game
// should set a stable name here even if its window title changes at runtime.
func WithApplicationName(name string, major, minor, patch int) Option {
	return func(c *config) {
		c.appName = name
		c.appVersion = common.CreateVersion(uint32(major), uint32(minor), uint32(patch))
	}
}

// WithFullscreen opens fullscreen on the primary monitor at its native
// resolution, ignoring WithWindowSize.
func WithFullscreen() Option {
	return func(c *config) { c.fullscreen = true }
}

// WithResizable controls whether a windowed-mode window can be resized.
// Defaults to true.
func WithResizable(resizable bool) Option {
	return func(c *config) { c.resizable = resizable }
}

// WithTickRate sets the fixed simulation rate in ticks per second.
// Defaults to DefaultTickRate.
func WithTickRate(hz int) Option {
	return func(c *config) { c.tickRate = hz }
}

// WithMaxCatchUp bounds how much simulation time a single frame may make up
// after a stall — an alt-tab, a breakpoint, a hitch. Defaults to
// DefaultMaxCatchUp. Values below one tick are raised to one tick.
//
// It is a *duration*, not a tick count, because the two stop being equivalent
// the moment a game changes its tick rate. A budget of two ticks is 33ms at
// 60Hz but only 16ms at 128Hz — less than one frame at 60fps — so every frame
// would discard time it could never make up and the whole game would run in
// slow motion for anyone below ~120fps.
//
// The size of the budget is a genre decision:
//
//   - A large budget keeps in-game time tracking wall-clock time. After a
//     stall the next frame simulates the backlog, so a fast-paced or
//     single-player game stays honest about how much time passed. The cost is
//     a burst of movement when a stalled client resumes.
//   - A small budget makes a struggling client run in slow motion instead of
//     bursting. That is often what a server-authoritative game wants: the
//     server is the truth and will correct the client anyway, and a smooth
//     slow client beats a lurching one.
//
// Whatever the budget, exceeding it means the simulation falls behind rather
// than trying to repay a debt it cannot — which is what stops the classic
// spiral where catch-up work makes the next frame slower still.
func WithMaxCatchUp(d time.Duration) Option {
	return func(c *config) { c.maxCatchUp = d }
}

// WithMaxFrames stops the main loop after n frames. Zero, the default, runs
// until the window closes. This is what makes the engine testable on CI and
// profilable over a fixed workload.
//
// It counts iterations that got as far as rendering, which is all but the ones
// skipped while minimized. Almost always that is also the number of frames
// presented; the exception is a swapchain rebuilt at a new size, where the
// frame already built for the old one is dropped. Under a fixed clock the
// difference matters -- see Repeatable renders in docs/agents/game-loop.md.
func WithMaxFrames(n int) Option {
	return func(c *config) { c.maxFrames = n }
}

// WithFixedFrameTime makes every frame advance the clock by exactly d instead
// of by however long the frame took, so a run is a function of its frame count
// rather than of the machine it ran on. Zero, the default, uses the real delta.
//
// This is a diagnostic setting, not a pacing one: it does not slow the loop
// down to d, it lies to the simulation about how much time passed. Wind,
// clouds, water, particles, animation and the day-night cycle all read that
// clock, so with it set two runs at the same -frames produce the same image,
// bit for bit.
//
// Without it they do not, and that is expensive when you are hunting an
// artifact. Comparing two renders means comparing them against a noise floor
// measured from two identical runs, and anything smaller than the floor is
// invisible -- a single-pixel sparkle counted 7 times in one run and 14 in the
// next is not a measurement, and a fix judged by one run of each is a coin
// toss. It also makes bisecting a visual bug possible at all: without a fixed
// clock, "it looks different now" cannot be told from "the wind moved".
//
// Set GLYPHENGINE_FIXED_FRAME_TIME to a Go duration ("16.667ms") to force it on
// a binary you did not compile, the way GLYPHENGINE_TIMING and
// GLYPHENGINE_VALIDATION work. The environment wins over this option.
func WithFixedFrameTime(d time.Duration) Option {
	return func(c *config) { c.fixedFrameTime = d }
}

// WithScreenshot writes a PNG of the last rendered frame to path when Run
// finishes, then returns.
//
// Pair it with WithMaxFrames so the run is deterministic: render a fixed
// number of frames, capture, exit. That is how the images in the README are
// produced, and it is reproducible rather than a hand-taken grab.
//
// Capture reads the presented swapchain image back from the GPU, so it costs a
// device-idle wait — fine once at the end of a run, not something to do per
// frame.
func WithScreenshot(path string) Option {
	return func(c *config) { c.screenshot = path }
}

// WithQuitKey closes the window when that key is pressed.
//
// The engine otherwise never reads input on the game's behalf, and this is a
// deliberate exception rather than the start of a pattern: getting out of a
// window is a harness concern, not a gameplay one. It is also not optional in
// practice, because WithFullscreen leaves no close button, so every example
// that offers fullscreen has to handle a quit key or trap whoever runs it.
//
// Hand-rolling it costs four lines and an import of the input package in every
// example, which is four lines of noise in front of whatever the example is
// actually there to show. A game that wants quitting to mean something more --
// a confirmation prompt, saving first -- should leave this unset and bind its
// own action.
func WithQuitKey(key input.Key) Option {
	return func(c *config) { c.quitKey, c.hasQuitKey = key, true }
}

// WithDebugKeys binds F1 to toggle bloom and F2 to cycle the tonemap curve.
//
// The second exception to "the engine never reads input on the game's behalf",
// and admitted for the same reason as the first: judging a post-process means
// looking at the same frame with it on and off, and restarting the program
// between the two comparisons is not looking at the same frame. That is a
// harness concern, exactly like getting out of a window.
//
// Opt-in, so nothing takes F1 and F2 away from a game that wants them. F-keys
// because they do not collide with movement.
//
// F1 turns bloom on even in a scene that never called SetBloom, using the
// defaults documented in bloom.md. That is deliberate -- most scenes have never
// asked for bloom, and those are the interesting ones to try it on.
func WithDebugKeys() Option {
	return func(c *config) { c.debugKeys = true }
}

// WithProjection overrides the vertical field of view in degrees and the near
// and far clip planes. Defaults are 45°, 0.1, and 500.
func WithProjection(fovDegrees, near, far float32) Option {
	return func(c *config) { c.fov, c.near, c.far = fovDegrees, near, far }
}

// Engine owns the window, renderer, input, and frame loop, and drives a Scene.
// The embedded *Scene means engine.C, engine.Raycast, engine.Spawn and friends
// are reachable directly from a *Engine.
type Engine struct {
	*Scene

	window   *window.Window
	renderer *renderer.Renderer
	input    *input.Input
	game     Game

	// Optional Game interfaces, resolved once at construction rather than
	// type-asserted every frame.
	fixedUpdate FixedUpdateGame
	lateUpdate  LateUpdateGame

	cameraEye    mgl32.Vec3
	cameraCenter mgl32.Vec3
	cameraUp     mgl32.Vec3

	fov, near, far float32

	overlays     []renderer.RenderObject
	uiOverlays   []renderer.UIRenderObject
	msdfOverlays []renderer.RenderObject

	// Debug text queued this frame, and the font built for it on first use.
	debugLines  []string
	debugFont   *renderer.Font
	debugText   *renderer.MSDFText
	smoothDelta float64

	moonMesh *renderer.Mesh
	sunMesh  *renderer.Mesh
	elapsed  float32 // running time counter for shader animation

	// nightGrade is Scene.NightGrade in the renderer's own shape, kept here
	// so the per-frame SceneLighting can point at it without allocating.
	nightGrade renderer.NightGrade

	// skyPalette is Scene.SkyPalette in the renderer's shape, for the same
	// reason.
	skyPalette renderer.SkyPalette

	// volumetrics is Scene.Volumetrics in the renderer's shape, for the same
	// reason again.
	volumetrics renderer.Volumetrics

	// The draw list is built into drawBuf, then permuted into drawSorted by
	// the order sortDraws works out in drawOrderBuf. Three buffers rather than
	// one sort in place: see drawOrder for what sorting 224-byte RenderObjects
	// costs. All three are kept between frames so a steady scene allocates
	// nothing for them.
	drawBuf      []renderer.RenderObject
	drawSorted   []renderer.RenderObject
	drawOrderBuf []drawOrder
	animScratch  renderer.AnimScratch // reused each frame by TickAnimations

	tickDuration time.Duration
	maxCatchUp   time.Duration
	maxFrames    int
	frameCount   int

	// loopCount counts iterations of Run's loop, including the ones that
	// render nothing, and tickCount counts fixed ticks. frameCount counts only
	// rendered frames, so on its own it cannot show a run that simulated more
	// times than it drew -- which is the divergence issue #40 is about. Both
	// are unconditional: an increment is cheaper than the branch that would
	// skip it, and the state trace needs them to mean something.
	loopCount int
	tickCount int

	// trace is the per-frame state trace; nil unless GLYPHENGINE_STATE_TRACE
	// is set. See statetrace.go.
	trace *renderer.StateTrace

	// provoke injects, on chosen frames, the things a run normally suffers by
	// accident. Inert unless asked for; see provoke.go.
	provoke provocations

	// fixedFrameTime replaces the measured frame delta when non-zero; see
	// WithFixedFrameTime.
	fixedFrameTime time.Duration
	screenshot     string
	alpha          float32 // fraction between the last two ticks; see Alpha

	// See WithQuitKey. hasQuitKey is separate because key zero is a real key.
	quitKey    input.Key
	hasQuitKey bool

	// See WithDebugKeys. debugBloom remembers the intensity F1 switched off, so
	// switching it back on restores what the scene chose rather than a guess.
	debugKeys  bool
	debugBloom float32

	// timeScale multiplies the simulation clock; see SetTimeScale. 1 is real
	// time, 0 is paused. Not persisted anywhere -- a game that wants to resume
	// at a particular speed sets it again.
	timeScale float32

	// accumulator is the unspent simulation time carried between frames. It is
	// engine state rather than a local in Run so that advanceSimulation can be
	// driven directly by a test: the frame loop itself needs a window and a
	// GPU, and a test that re-implemented its arithmetic would be testing the
	// copy rather than the engine.
	accumulator time.Duration

	// lightDebugMode selects what the fragment shader's light loop does; see
	// SetLightDebugMode.
	lightDebugMode LightDebugMode

	// lightBuf, lightGeomBuf and lightUploadBuf are reused each frame to avoid
	// allocs, like drawBuf: the packed lights in submission order, the
	// geometry the binner sees, and the packed lights again in the binner's
	// upload order. Three buffers rather than one sort in place, because the
	// cell lists index the upload order while the binner works in submission
	// indices, and reordering underneath it would invalidate both.
	lightBuf       []renderer.GpuLight
	lightGeomBuf   []lightcluster.Light
	lightUploadBuf []renderer.GpuLight

	// lightCluster bins the lights into froxels once per frame; lightStats is
	// what that did, for LightStats.
	lightCluster *lightcluster.Builder
	lightStats   lightcluster.Stats

	// cpu accumulates per-phase CPU cost; see cputimer.go.
	cpu cpuTimer
}

// LightDebugMode selects what the fragment shader does with the clustered
// light data; see Engine.SetLightDebugMode.
type LightDebugMode int

const (
	// LightDebugOff renders normally: every fragment evaluates the lights its
	// froxel lists, which is the path games ship.
	LightDebugOff LightDebugMode = iota
	// LightDebugHeatmap replaces the lit colour with a ramp of each fragment's
	// cluster cell light count, for inspecting how lights distribute across
	// the grid.
	LightDebugHeatmap
	// LightDebugBruteForce forces the reference loop-every-light path
	// regardless of the cluster grid.
	//
	// It is the same lights in the same order as the clustered path -- only
	// the iteration differs -- so the two must render pixel for pixel alike,
	// and `task lights` requires exactly that. A difference between them is a
	// binning bug and nothing else, which is what makes this worth carrying in
	// the shipped shader rather than in a test build.
	LightDebugBruteForce
)

// SetLightDebugMode selects how the fragment shader evaluates the light
// list: normal, the per-cell light-count heatmap, or the brute-force
// reference path. See LightDebugMode.
func (e *Engine) SetLightDebugMode(mode LightDebugMode) { e.lightDebugMode = mode }

// lightFlags packs the current debug mode into the header bits
// shaders/lights.inc reads: bit0 = brute force, bit1 = debug heatmap.
func (e *Engine) lightFlags() uint32 {
	switch e.lightDebugMode {
	case LightDebugHeatmap:
		return renderer.LightFlagHeatmap
	case LightDebugBruteForce:
		return renderer.LightFlagBruteForce
	default:
		// Clustered: no flags. The grid is filled every frame by
		// clusterFrameLights, so this is the path unless a debug mode asks
		// for something else.
		return 0
	}
}

// volumetricFlag returns LightFlagVolumetric when this frame has anything for
// the in-scattering march to do, and 0 otherwise.
//
// Scanning the uploaded lights once on the CPU, rather than letting the
// shader discover it: the shader's version of this question is the loop the
// flag exists to skip, and it would ask it per step per pixel. A linear pass
// over at most MaxLights entries costs microseconds and answers it once.
//
// The lights scanned are the UPLOADED ones, after binning. A volumetric light
// the binner culled or dropped over budget cannot reach a froxel, so a frame
// where every volumetric light was culled correctly reports nothing to do.
//
// Steps == 0 short-circuits it: a scene that turned the march off entirely
// must not pay the scan either, and must not set a flag that promises the
// shader work it will then do with a zero-iteration loop.
func volumetricFlag(lights []renderer.GpuLight, v Volumetrics) uint32 {
	if v.Steps <= 0 {
		return 0
	}
	for i := range lights {
		if lights[i].Params[0] > 0 {
			return renderer.LightFlagVolumetric
		}
	}
	return 0
}

// gatherLights combines the scene's point and spot lights into one list for
// the GPU light buffer: points first, then spots, in submission order. A
// scene written before spot lights existed therefore lights exactly as it
// did before, in the same order.
// volumetricIntensity sanitizes a light's Volumetric field on its way to the
// GPU.
//
// Negative would subtract light from the air, driving a fragment's colour
// below zero on its way into the tonemap. NaN is worse, and worse in a way
// that points at the wrong file: it survives every add in the march, so the
// whole pixel goes to NaN and comes out of the tonemap black -- a hole in the
// geometry, which nobody would trace back to a light's intensity field. Both
// become 0, the value that means "this light does not scatter", so a bad
// number is a light that quietly does not glow.
//
// The shader tests `> 0.0` to skip a light, so exactly +0.0 here is what buys
// the early-out; clamping to a small epsilon instead would cost the march on
// every light in the scene.
func volumetricIntensity(v float32) float32 {
	if !(v > 0) { // also catches NaN
		return 0
	}
	return v
}

func (e *Engine) gatherLights() []renderer.GpuLight {
	pls := e.Scene.pointLights
	spls := e.Scene.spotLights
	lights := e.lightBuf[:0]

	for _, pl := range pls {
		lights = append(lights, renderer.GpuLight{
			PosRange: [4]float32{pl.Pos.X(), pl.Pos.Y(), pl.Pos.Z(), pl.Range},
			Color:    [4]float32{pl.Color.X(), pl.Color.Y(), pl.Color.Z(), 0},
			// DirCone stays zero: lightSpotFactor in shaders/lights.inc reads
			// that as "omnidirectional", which is the value a plain point
			// light has to reach the GPU with.
			Params: [4]float32{volumetricIntensity(pl.Volumetric), 0, 0, 0},
		})
	}

	for _, sl := range spls {
		lights = append(lights, spotLightGpuLight(sl))
	}

	// No truncation here. The binner owns the budget: it drops the lights
	// whose surfaces are furthest from the camera, not whichever ones the
	// scene happened to append last, and counts what it dropped in
	// Stats.DroppedOverBudget. Cutting the list here would take that decision
	// away from it and report nothing.
	//
	// The list can therefore be as long as the scene likes, which is why it
	// lives in a reused buffer: a colony handing over 3000 lamps must not
	// allocate 3000 lights every frame.
	e.lightBuf = lights
	return lights
}

// clusterFrameLights bins this frame's lights and returns them in the order
// the GPU will see them, together with the grid the shader reads.
//
// The binner is given the geometry of the PACKED lights, not of the scene's
// SpotLight values. spotLightGpuLight nudges cosOuter down when a caller asks
// for a cone the smoothstep cannot express, so the cone the shader evaluates
// is a hair wider than the one the scene asked for -- and it is the shader's
// cone that has to be bounded. Reading it back out of the packed light also
// means there is one conversion from scene lights to light geometry rather
// than two that can disagree.
//
// view and proj must be the matrices this frame renders with, and w/h its
// framebuffer: the cell a fragment reads is computed from gl_FragCoord and the
// header, so a grid built for a different camera is not approximately right,
// it is tile-shaped nonsense.
func (e *Engine) clusterFrameLights(view, proj mgl32.Mat4, w, h int) ([]renderer.GpuLight, *lightcluster.Result) {
	lights := e.gatherLights()

	geom := e.lightGeomBuf[:0]
	for i := range lights {
		l := &lights[i]
		geom = append(geom, lightcluster.Light{
			Pos:      mgl32.Vec3{l.PosRange[0], l.PosRange[1], l.PosRange[2]},
			Range:    l.PosRange[3],
			Dir:      mgl32.Vec3{l.DirCone[0], l.DirCone[1], l.DirCone[2]},
			CosOuter: l.DirCone[3],
		})
	}
	e.lightGeomBuf = geom

	res := e.lightCluster.Build(geom, lightcluster.Params{
		View:   view,
		Proj:   proj,
		Width:  w,
		Height: h,
		Near:   e.near,
		Far:    e.far,
		Grid:   lightcluster.DefaultGrid,
	})
	e.lightStats = res.Stats

	// Reorder into the array the cell lists index. Brute force uploads the
	// same array in the same order, so the two modes iterate the same lights
	// in the same relative order and a light that reaches no fragment adds
	// exactly zero to it -- which is why they can be required to match to the
	// bit rather than to a tolerance.
	out := e.lightUploadBuf[:0]
	for _, i := range res.Order {
		out = append(out, lights[i])
	}
	e.lightUploadBuf = out
	return out, res
}

// LightStats reports what the last frame's light binning did.
//
// Four of the fields are the ones a game should watch, because each is a way
// light can go missing that nothing else will report:
//
//   - DroppedOverBudget: more lights passed the frustum test than
//     renderer.MaxLights. The furthest-from-lighting-anything are dropped.
//   - CellsOverflowed: a froxel wanted more lights than it can hold. It keeps
//     the ones nearest the camera; the rest do not light that cell.
//   - CellsTruncated: the whole index buffer filled up. Worse than an
//     overflowed cell, because it takes whole cells in cell order rather than
//     the tail of one list.
//   - ScreenWideLights: lights that reached every tile of a slice. These are
//     the ones clustering did not help with, so a scene where this grows is a
//     scene drifting back towards the every-light loop.
//
// The rest are for tuning the grid; see lightcluster.Stats.
func (e *Engine) LightStats() lightcluster.Stats { return e.lightStats }

// spotLightGpuLight converts one SpotLight into the GpuLight the shader
// reads (shaders/lights.inc's packed form): DirCone.xyz is the unit
// direction the light points, DirCone.w is cos(outer half-angle), and
// Color.a is cos(inner half-angle). A zero-length Dir has no cone to aim, so
// it reaches the GPU as a plain point light (DirCone left at zero) -- see
// the SpotLight doc comment in scene.go for why that is the chosen
// behaviour rather than an error.
//
// Two invariants have to hold in the angles this packs, or the shader's
// smoothstep(cosOuter, cosInner, x) misbehaves:
//
//   - Inner must not exceed Outer. A caller asking for a narrower inner cone
//     than the outer one is not asking for "no cone at all" -- clamping
//     Inner down to Outer gives a hard edge instead, which is also what
//     Inner == Outer already has to mean, so the two cases collapse into
//     one rather than needing separate handling.
//   - cosInner must be strictly greater than cosOuter, by more than a
//     float32 rounding error can erase. GLSL leaves smoothstep undefined
//     when its two edges are equal, and a hard-edged cone (Inner == Outer,
//     or Outer small enough that Inner clamps to the same float32 value) is
//     the most natural thing a caller will write, not a rare mistake to
//     shrug off. cosineMargin (1e-4) is roughly 800x the spacing between
//     representable float32 values near +-1, which is where cosines of
//     small angles and angles near pi actually land, so nudging by it
//     always produces a different float rather than rounding straight back
//     to the value it started from.
func spotLightGpuLight(sl SpotLight) renderer.GpuLight {
	g := renderer.GpuLight{
		PosRange: [4]float32{sl.Pos.X(), sl.Pos.Y(), sl.Pos.Z(), sl.Range},
		Color:    [4]float32{sl.Color.X(), sl.Color.Y(), sl.Color.Z(), 0},
		Params:   [4]float32{volumetricIntensity(sl.Volumetric), 0, 0, 0},
	}

	dir := sl.Dir
	if dir.Len() == 0 {
		return g
	}
	dir = dir.Normalize()

	outer := sanitizeSpotAngle(sl.Outer)
	inner := sanitizeSpotAngle(sl.Inner)
	if inner > outer {
		inner = outer
	}

	cosOuter := float32(math.Cos(float64(outer)))
	cosInner := float32(math.Cos(float64(inner)))

	const cosineMargin = 1e-4
	if cosInner-cosOuter < cosineMargin {
		cosInner = cosOuter + cosineMargin
		if cosInner > 1 {
			// Outer itself is near zero, so cosOuter is already near the
			// ceiling and there is no room left to push cosInner above it --
			// push cosOuter down instead. The cone this produces is a
			// hair-thin sliver rather than truly zero width, which is a
			// numerical safety floor, not a design choice: a caller wanting
			// an actual zero-radius cone has asked for something with no
			// correct answer here.
			cosInner = 1
			cosOuter = 1 - cosineMargin
		}
	}

	g.Color[3] = cosInner
	g.DirCone = [4]float32{dir.X(), dir.Y(), dir.Z(), cosOuter}
	return g
}

// sanitizeSpotAngle clamps a half-angle to [0, pi] and maps NaN to 0. Both
// are angles no caller can have sanely meant, and 0 -- the narrowest
// possible cone -- is a safer fallback than letting either reach cos() and
// carry a NaN into the packed light.
func sanitizeSpotAngle(a float32) float32 {
	switch {
	case math.IsNaN(float64(a)):
		return 0
	case a < 0:
		return 0
	case a > math.Pi:
		return math.Pi
	default:
		return a
	}
}

// resolveMaxCatchUp applies the default and makes sure the budget can fit at
// least one tick — a smaller budget would starve the simulation completely.
// resolveFixedFrameTime lets the environment force a fixed clock on a binary
// that never asked for one, which is the point: the artifact you need to
// reproduce is usually in an example someone else built. An unparseable value
// is reported rather than silently ignored -- a run that is quietly still
// non-deterministic wastes more time than a bad flag.
func resolveFixedFrameTime(requested time.Duration) time.Duration {
	if v := os.Getenv("GLYPHENGINE_FIXED_FRAME_TIME"); v != "" {
		d, err := time.ParseDuration(v)
		switch {
		case err != nil:
			log.Printf("GLYPHENGINE_FIXED_FRAME_TIME=%q is not a duration (want e.g. 16.667ms), ignoring", v)
		case d <= 0:
			log.Printf("GLYPHENGINE_FIXED_FRAME_TIME=%q is not positive, ignoring", v)
		default:
			return d
		}
	}
	if requested < 0 {
		return 0
	}
	return requested
}

func resolveMaxCatchUp(requested, tickDuration time.Duration) time.Duration {
	if requested <= 0 {
		requested = DefaultMaxCatchUp
	}
	if requested < tickDuration {
		return tickDuration
	}
	return requested
}

// New creates the window, renderer, and input system, then calls g.Init so the
// game can build its scene. Call Destroy when Run returns.
func New(g Game, opts ...Option) (*Engine, error) {
	cfg := config{
		width:     1280,
		height:    720,
		title:     "GlyphEngine",
		resizable: true,
		vsync:     true, // see WithVSync
		interp:    true, // see WithInterpolation
		tickRate:  DefaultTickRate,
		fov:       45,
		near:      0.1,
		far:       500,
	}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.tickRate <= 0 {
		cfg.tickRate = DefaultTickRate
	}

	scene := cfg.scene
	if scene == nil {
		scene = NewScene()
	}
	// Rendering is what interpolation is for, so the Engine enables it even on
	// a Scene the caller built headless.
	scene.Interpolate = cfg.interp

	var wOpts []window.Option
	if cfg.fullscreen {
		wOpts = append(wOpts, window.WithFullscreen())
	}
	wOpts = append(wOpts, window.WithResizable(cfg.resizable))

	w, err := window.New(cfg.width, cfg.height, cfg.title, wOpts...)
	if err != nil {
		return nil, err
	}

	r, err := renderer.New(w, cfg.rendererOptions()...)
	if err != nil {
		w.Destroy()
		return nil, err
	}

	e := &Engine{
		Scene:          scene,
		window:         w,
		renderer:       r,
		input:          input.New(w.Handle()),
		game:           g,
		timeScale:      1,
		cameraEye:      mgl32.Vec3{0, 1, 3},
		cameraCenter:   mgl32.Vec3{0, 0, 0},
		cameraUp:       mgl32.Vec3{0, 1, 0},
		fov:            cfg.fov,
		near:           cfg.near,
		far:            cfg.far,
		tickDuration:   time.Second / time.Duration(cfg.tickRate),
		maxCatchUp:     resolveMaxCatchUp(cfg.maxCatchUp, time.Second/time.Duration(cfg.tickRate)),
		maxFrames:      cfg.maxFrames,
		screenshot:     cfg.screenshot,
		fixedFrameTime: resolveFixedFrameTime(cfg.fixedFrameTime),
		quitKey:        cfg.quitKey,
		hasQuitKey:     cfg.hasQuitKey,
		debugKeys:      cfg.debugKeys,
		lightCluster:   lightcluster.New(),
	}

	// Opened before Init so a game that draws during it is already traced.
	if e.trace = openStateTrace(); e.trace != nil {
		r.SetStateTrace(e.trace)
	}
	e.provoke = readProvocations()

	// A fixed clock is only half of a repeatable run: particle spawns draw from
	// a source that is reseeded at every process start, so pin that too.
	if e.fixedFrameTime > 0 {
		// The cursor is the input nobody decides to move. Left live, it makes a
		// capture depend on where the mouse happened to be sitting: two runs of
		// 15-kitchen-sink came out RMS 0.59 apart that way, and 09-water failed
		// the determinism gate intermittently for the same reason.
		e.input.IgnorePointer(true)
		pinSpawnRand(0x5eed)
		log.Printf("Fixed frame time: %v (deterministic run)", e.fixedFrameTime)
	}

	// Celestial billboards are engine-owned meshes, not game assets.
	if e.moonMesh, err = r.CreateDisc(2.0, 24); err != nil {
		e.teardown()
		return nil, err
	}
	if e.sunMesh, err = r.CreateDisc(2.5, 32); err != nil {
		e.teardown()
		return nil, err
	}

	e.fixedUpdate, _ = g.(FixedUpdateGame)
	e.lateUpdate, _ = g.(LateUpdateGame)

	if err := g.Init(e); err != nil {
		e.teardown()
		return nil, err
	}

	return e, nil
}

// Renderer returns the Vulkan renderer.
func (e *Engine) Renderer() *renderer.Renderer { return e.renderer }

// Input returns the input system.
func (e *Engine) Input() *input.Input { return e.input }

// Window returns the underlying window.
func (e *Engine) Window() *window.Window { return e.window }

// Close signals the engine to shut down at the end of the current frame.
func (e *Engine) Close() { e.window.Close() }

// FPS returns a smoothed frames-per-second measurement.
func (e *Engine) FPS() float64 {
	if e.smoothDelta <= 0 {
		return 0
	}
	return 1.0 / e.smoothDelta
}

// FrameCount returns the number of frames rendered so far, counted the same
// way WithMaxFrames counts them.
func (e *Engine) FrameCount() int { return e.frameCount }

// GPUTimings returns the most recent per-pass GPU cost in milliseconds.
//
// Measured with timestamp queries on the GPU's own clock, so the numbers are
// real GPU time whether or not vsync is capping the frame. That is what makes
// them the right tool for deciding where rendering time goes: whole-frame timing
// has to run unlocked to show anything, and even then cannot say which pass to
// look at.
//
// The reading is a couple of frames old — collecting a fresher one would need a
// pipeline flush, which would change what was being measured. Check Valid: it is
// false for the first frames and on devices without timestamp support.
func (e *Engine) GPUTimings() renderer.GPUTimings { return e.renderer.GPUTimings() }

// MeanGPUTimings averages every frame measured so far; see Renderer.MeanGPUTimings.
func (e *Engine) MeanGPUTimings() renderer.GPUTimings { return e.renderer.MeanGPUTimings() }

// LogTimings prints the CPU and GPU breakdowns together, which is the only way
// to read either of them.
//
// A frame that is slow with a large gpuwait is GPU-bound or vsync-paced; the GPU
// table says which pass. A frame that is slow with gpuwait near zero is CPU-bound
// and the GPU table is a distraction. Printing one without the other invites
// exactly the wrong conclusion.
func (e *Engine) LogTimings() {
	c := e.CPUTimings()
	if c.Valid {
		var sum float32
		for p, ms := range c.Phase {
			sum += ms
			log.Printf("cpu %-10s %6.3f ms", CPUPhase(p), ms)
		}
		log.Printf("cpu %-10s %6.3f ms  (phases sum to %.3f)", "FRAME", c.Total, sum)
	}
	e.LogGPUTimings()

	st := e.renderer.Stats()
	log.Printf("draw calls %d  instances %d  triangles %d  grass tiles %d drawn / %d culled",
		st.DrawCalls, st.Instances, st.Triangles, st.GrassTilesDrawn, st.GrassTilesCulled)
}

// LogTimingsTSV prints one tab-separated line of every timing, for collecting
// runs into a table.
//
// Deliberately one line rather than the human-readable block: a benchmark
// comparing twelve scenes wants columns it can align, and a format that survives
// being pasted into a spreadsheet or diffed between commits.
func (e *Engine) LogTimingsTSV(label string) {
	c := e.CPUTimings()
	g := e.MeanGPUTimings()

	var b strings.Builder
	fmt.Fprintf(&b, "BENCH	%s	%d", label, e.FrameCount())
	fmt.Fprintf(&b, "	cpu_total	%.3f", c.Total)
	for p, ms := range c.Phase {
		fmt.Fprintf(&b, "	cpu_%s	%.3f", CPUPhase(p), ms)
	}
	fmt.Fprintf(&b, "	gpu_total	%.3f", g.Total)
	for p, ms := range g.Pass {
		fmt.Fprintf(&b, "	gpu_%s	%.3f", renderer.Pass(p), ms)
	}
	st := e.renderer.Stats()
	fmt.Fprintf(&b, "	n_draws	%d	n_instances	%d	n_triangles	%d	n_grasstiles	%d	n_grassculled	%d",
		st.DrawCalls, st.Instances, st.Triangles, st.GrassTilesDrawn, st.GrassTilesCulled)
	log.Println(b.String())
}

// LogGPUTimings prints one line per pass plus the measured frame total, averaged
// over every frame collected.
//
// The passes are not expected to sum to the total. The GPU overlaps work across
// pass boundaries, and a gap between two passes belongs to neither, so a total
// well above the sum is a bubble worth knowing about rather than an error.
func (e *Engine) LogGPUTimings() {
	t := e.renderer.MeanGPUTimings()
	if !t.Valid {
		if !e.renderer.GPUTimingSupported() {
			log.Println("gpu timings: unsupported on this device")
		} else {
			log.Println("gpu timings: not ready yet")
		}
		return
	}
	var sum float32
	for p, ms := range t.Pass {
		sum += ms
		log.Printf("gpu %-10s %6.3f ms", renderer.Pass(p), ms)
	}
	log.Printf("gpu %-10s %6.3f ms  (passes sum to %.3f)", "FRAME", t.Total, sum)
}

// Alpha returns how far the current frame sits between the last simulation
// tick and the next, in [0,1). Rendering uses it to blend transforms; games
// need it only for their own interpolation of non-Transform state.
func (e *Engine) Alpha() float32 { return e.alpha }

// InterpolatedTransform returns the transform an entity is being drawn at this
// frame — its last two tick transforms blended by Alpha.
//
// Use it for anything that must line up with what is on screen, a camera
// above all. A first-person camera reading the raw Transform sits at the last
// simulated position while the world renders between ticks, so the view lurches
// at the tick rate even though everything else is smooth.
func (e *Engine) InterpolatedTransform(entity ecs.Entity) (Transform, bool) {
	return e.Scene.InterpolatedTransform(entity, e.alpha)
}

// advanceSimulation banks one frame of simulation time and drains it into
// fixed ticks, leaving e.alpha as how far the frame sits past the last one.
//
// Split out of Run because Run needs a window and a GPU, and this is the part
// worth testing: a paused engine that still ticks is the failure this exists to
// prevent, and it is silent. A test that re-implemented the arithmetic instead
// would pass while the loop did the wrong thing.
//
// frameDelta is real time; the scaling to simulation time happens here, and
// the tick step comes from e.tickDuration rather than the caller.
func (e *Engine) advanceSimulation(frameDelta time.Duration) {
	// The clock the shaders read -- grass wind, water waves, the cloud march --
	// advances HERE, with the ticks, and not further down the frame loop where
	// it used to sit. That was below the minimized-window check, which
	// `continue`s: a minimized game went on ticking while its waves stood
	// still, so the two clocks separated by however long the window was down
	// and the sun had moved on a lake that had not. Nothing reads it between
	// here and the render, so where in the frame it moves is unobservable;
	// what matters is that nothing can skip it without skipping the ticks too.
	e.elapsed += float32(frameDelta.Seconds()) * e.timeScale

	// The simulation clock is the real one scaled. Scaling what goes into the
	// accumulator rather than the tick delta is the whole design: a fixed
	// timestep is only fixed if tickDt never moves, and slow motion that
	// shortened the step would change how the integrator behaves and break
	// determinism with it. Half speed is half as many ticks of the same size.
	e.accumulator += time.Duration(float64(frameDelta) * float64(e.timeScale))

	// Bound catch-up work so a long stall cannot spiral. Past the budget the
	// simulation falls behind instead — see WithMaxCatchUp.
	if e.accumulator > e.maxCatchUp {
		e.accumulator = e.maxCatchUp
	}

	step := float32(e.tickDuration.Seconds())
	for e.accumulator >= e.tickDuration {
		e.Scene.Tick(step)
		if e.fixedUpdate != nil {
			e.fixedUpdate.FixedUpdate(e, step)
		}
		e.accumulator -= e.tickDuration
		e.tickCount++
	}

	// Whatever is left over is how far this frame sits past the last tick, in
	// [0,1). Rendering blends by it so motion is smooth between simulation
	// steps rather than stepping at the tick rate. Frozen while paused, which
	// is what holds a paused frame at a consistent pose.
	e.alpha = float32(e.accumulator) / float32(e.tickDuration)
}

// SetTimeScale multiplies the rate of the simulation clock.
//
// 1 is real time. 0 pauses: Scene.Tick and FixedUpdate stop being called,
// animation stops advancing, and the wind and waves stop moving. Values between
// run slow motion, and above 1 runs fast — a difficulty setting, a bullet-time
// effect, or stepping through a bug at a tenth speed.
//
//	func (g *game) Update(e *glyph.Engine, dt float32) {
//	    if e.Input().KeyPressed(input.KeyEscape) {
//	        g.paused = !g.paused
//	        if g.paused {
//	            e.SetTimeScale(0)
//	        } else {
//	            e.SetTimeScale(1)
//	        }
//	    }
//	}
//
// What keeps running is everything a paused game needs to still be a program:
// Update, LateUpdate, and rendering. They receive the real frame delta, not the
// scaled one, so a menu animates and a camera moves at full speed while the
// world behind them is stopped. A game that wants its camera slowed too can
// multiply by TimeScale itself.
//
// Pausing is not something a game can do for itself. Returning early from
// FixedUpdate stops the game's own simulation and none of the engine's: rigid
// bodies keep integrating, character controllers keep moving, the transform
// snapshots interpolation reads keep being taken, and animation keeps playing.
// A game with no physics and no skinned meshes gets away with it by luck.
//
// Negative values are clamped to zero. Running a fixed-timestep simulation
// backwards is not a thing this engine can do — the integrator is not
// reversible — and the useful reading of a negative scale is "stopped".
func (e *Engine) SetTimeScale(s float32) {
	if s < 0 {
		s = 0
	}
	e.timeScale = s
}

// TimeScale returns the current simulation rate; see SetTimeScale.
func (e *Engine) TimeScale() float32 { return e.timeScale }

// Paused reports whether the simulation clock is stopped.
func (e *Engine) Paused() bool { return e.timeScale == 0 }

// Elapsed returns the running wall-clock time in seconds since Run started.
func (e *Engine) Elapsed() float32 { return e.elapsed }

// SetFogDensity sets the environment's fog density (0 disables fog).
//
// A shortcut for the common case. It does nothing when the scene uses a custom
// EnvironmentSource, which owns its own fog — reach through Scene.Env instead.
func (e *Engine) SetFogDensity(d float32) {
	env, ok := e.Scene.Env.(*Environment)
	if !ok || env == nil {
		return
	}
	if env.Fog == nil {
		env.Fog = &Fog{}
	}
	env.Fog.Density = d
}

// FogDensity returns the current fog density.
func (e *Engine) FogDensity() float32 { return e.Scene.Environment().FogDensity }

// SetOverlays sets the overlay render objects drawn on top of the 3D scene.
func (e *Engine) SetOverlays(overlays []renderer.RenderObject) { e.overlays = overlays }

// SetUIOverlays sets the UI panel render objects (alpha-blended, textured 9-slice).
func (e *Engine) SetUIOverlays(objs []renderer.UIRenderObject) { e.uiOverlays = objs }

// UIOverlays returns the current UI panel render objects.
func (e *Engine) UIOverlays() []renderer.UIRenderObject { return e.uiOverlays }

// SetMSDFOverlays sets the MSDF text overlay render objects.
func (e *Engine) SetMSDFOverlays(objs []renderer.RenderObject) { e.msdfOverlays = objs }

// MSDFOverlays returns the current MSDF text overlay render objects.
func (e *Engine) MSDFOverlays() []renderer.RenderObject { return e.msdfOverlays }

// SetCamera sets the camera eye, center, and up vectors used for rendering.
// Both Camera and FPCamera return these three from ViewVectors.
func (e *Engine) SetCamera(eye, center, up mgl32.Vec3) {
	e.cameraEye = eye
	e.cameraCenter = center
	e.cameraUp = up
}

// CameraEye returns the current camera eye position.
func (e *Engine) CameraEye() mgl32.Vec3 { return e.cameraEye }

// reverseZProjection builds the engine's projection matrix: reverse-Z (near maps
// to 1, far to 0) with a flipped Y for Vulkan's clip space. Geometry authored
// for a conventional 0→1 depth range fails the depth test and silently draws
// nothing.
//
// Split out of ViewProjection so the depth range can be checked without a
// device — Aspect() needs a live swapchain, and the questions worth asking about
// reverse-Z are all about near and far.
func reverseZProjection(fovDegrees, aspect, near, far float32) mgl32.Mat4 {
	proj := mgl32.Perspective(mgl32.DegToRad(fovDegrees), aspect, near, far)
	proj[10] = near / (far - near)
	proj[14] = (far * near) / (far - near)
	proj[5] *= -1
	return proj
}

// ViewProjection returns the current view-projection matrix.
func (e *Engine) ViewProjection() mgl32.Mat4 {
	_, _, vp := e.viewProjection()
	return vp
}

// viewProjection returns the two matrices and their product together, for the
// one caller that needs them apart: the light binner works in view space and
// reads the projection's own elements, so handing it the product would make it
// take them apart again and guess at which half was which.
func (e *Engine) viewProjection() (view, proj, vp mgl32.Mat4) {
	proj = reverseZProjection(e.fov, e.renderer.Aspect(), e.near, e.far)
	view = mgl32.LookAtV(e.cameraEye, e.cameraCenter, e.cameraUp)
	return view, proj, proj.Mul4(view)
}

// ScreenRay converts a screen-space position in pixels into a world-space ray.
func (e *Engine) ScreenRay(screenX, screenY float64) (origin, dir mgl32.Vec3) {
	w, h := e.renderer.Extent()
	ndcX := float32(2*screenX/float64(w) - 1)
	ndcY := float32(2*screenY/float64(h) - 1)

	invVP := e.ViewProjection().Inv()

	// Reverse-Z: the near plane is z=1 and the far plane z=0.
	nearW := invVP.Mul4x1(mgl32.Vec4{ndcX, ndcY, 1, 1})
	farW := invVP.Mul4x1(mgl32.Vec4{ndcX, ndcY, 0, 1})

	origin = mgl32.Vec3{nearW[0] / nearW[3], nearW[1] / nearW[3], nearW[2] / nearW[3]}
	far := mgl32.Vec3{farW[0] / farW[3], farW[1] / farW[3], farW[2] / farW[3]}
	return origin, far.Sub(origin).Normalize()
}

// PickEntity casts a ray from a screen-space mouse position into the world and
// returns the nearest collider hit within maxDist.
func (e *Engine) PickEntity(screenX, screenY float64, maxDist float32, exclude ecs.Entity) (RayHit, bool) {
	origin, dir := e.ScreenRay(screenX, screenY)
	return e.Raycast(origin, dir, maxDist, exclude)
}

// Run enters the main loop. Each frame, in order:
//
//  1. poll input
//  2. Game.Update            - frame delta; sample input, latch edges
//  3. Scene.Tick + FixedUpdate - tick delta, zero or more times
//  4. TickAnimations         - frame delta
//  5. Game.LateUpdate        - frame delta; final transforms and poses
//  6. render
//
// It returns when the window closes or the WithMaxFrames budget is spent.
//
// Frame pacing belongs to the presentation engine, not to this loop. With
// vsync on (the default) the swapchain blocks at the refresh rate of whatever
// display the window is on; with it off the loop runs unbounded. There is
// deliberately no software frame limiter here — see WithVSync.
func (e *Engine) Run() {
	prev := time.Now()

	// Deferred so it covers both exits: the frame budget running out and
	// the window closing.
	defer e.captureIfRequested()

	// GLYPHENGINE_TIMING=1 reports on exit without the game having to add a
	// flag, the same way GLYPHENGINE_VALIDATION works: the point of both is to
	// get numbers out of a binary you did not compile.
	switch os.Getenv("GLYPHENGINE_TIMING") {
	case "1":
		defer e.LogTimings()
	case "tsv":
		label := os.Getenv("GLYPHENGINE_BENCH_LABEL")
		if label == "" {
			label = "run"
		}
		defer e.LogTimingsTSV(label)
	}

	if e.provoke.active() {
		log.Printf("glyphengine: determinism provocations are ON; this run's capture is not a reference")
	}

	for !e.window.ShouldClose() {
		e.loopCount++
		e.trace.Begin(e.loopCount)
		e.provoke.apply(e.loopCount, e.renderer)

		frameStart := time.Now()
		frameDelta := frameStart.Sub(prev)
		prev = frameStart
		// A fixed clock replaces the measured delta for everything the
		// simulation reads, and nothing else: the CPU timers below still
		// measure real time, because their job is to report what the frame
		// actually cost. See WithFixedFrameTime.
		if e.fixedFrameTime > 0 {
			frameDelta = e.fixedFrameTime
		}
		// The simulation clock is the real one scaled. Scaling the accumulator
		// rather than the tick delta is the whole design: a fixed timestep is
		// only fixed if tickDt never moves, and slow motion that shortened the
		// step would change how physics integrates and break determinism with
		// it. Half speed means half as many ticks of the same size.
		dt := frameDelta.Seconds()
		if e.smoothDelta <= 0 {
			e.smoothDelta = dt
		} else {
			e.smoothDelta = e.smoothDelta*0.95 + dt*0.05
		}

		e.cpu.begin(CPUPoll)
		e.input.Update()
		e.window.PollEvents()

		// Checked here rather than in the game because it is the harness's job;
		// see WithQuitKey. Sampled in the same place as every other device, so
		// the edge cannot be missed or doubled the way one read from a fixed
		// tick would be.
		if e.hasQuitKey && e.input.KeyPressed(e.quitKey) {
			e.Close()
		}
		if e.debugKeys {
			e.handleDebugKeys()
		}

		if e.window.WasResized() {
			e.renderer.NotifyResize()
			if rg, ok := e.game.(ResizeGame); ok {
				w, h := e.window.GetFramebufferSize()
				rg.OnResize(e, w, h)
			}
		}

		// Per-frame game logic runs BEFORE the fixed ticks, so input sampled
		// this frame is consumed by simulation on the same frame. Unity runs
		// its FixedUpdate first, which costs up to a frame of input latency;
		// this ordering does not.
		e.cpu.begin(CPUUpdate)
		e.game.Update(e, float32(dt))

		// Fixed-timestep simulation. Runs zero or more times per frame — which
		// is exactly why input belongs in Update above, not in here.
		e.cpu.begin(CPUTick)
		e.advanceSimulation(frameDelta)

		// Animation sampling is presentation, not simulation — advance it once
		// per rendered frame rather than per tick. Clamped so a long stall does
		// not lurch poses forward, then scaled: a paused game with a walk cycle
		// still playing is not paused, and slow motion wants the walk slowed to
		// match.
		animDt := float32(dt)
		if animDt > 0.25 {
			animDt = 0.25
		}
		animDt *= e.timeScale
		e.cpu.begin(CPUAnimate)
		e.TickAnimations(animDt)

		// Last hook before rendering: transforms and poses are final, so a
		// camera following its target here is never a tick behind.
		e.cpu.begin(CPULateUpdate)
		if e.lateUpdate != nil {
			e.lateUpdate.LateUpdate(e, float32(dt))
		}

		// Skip rendering while minimized; game logic and networking continue.
		if e.renderer.Minimized() {
			e.cpu.stop()
			if t := e.trace; t != nil {
				t.Int("ticks", e.tickCount)
				t.Int("rendered", e.frameCount)
				t.Str("outcome", "skip-minimized")
				t.End()
			}
			time.Sleep(50 * time.Millisecond)
			continue
		}

		// Scaled for the same reason as animation. This clock reaches the
		// shaders as lighting.Time and drives grass wind, water waves and the
		// cloud march; a paused world whose lake is still rippling has not
		// stopped, and under slow motion unscaled waves would run visibly fast
		// against the characters in front of them.
		//
		// The sun needs no handling here: time of day advances inside
		// Scene.Tick, so it stops when the ticks do.
		e.renderFrame()

		// The fence wait happens inside the renderer, so it is folded in rather
		// than bracketed here, and subtracted from submit so the two do not
		// double-count.
		// DrawFrame is three different things: two waits on the presentation
		// pipeline and the command recording between them. Charging the whole
		// call to the CPU reports a saturated CPU on a frame that is simply
		// waiting for vsync, which is the opposite of the truth.
		wait := e.renderer.LastFenceWait()
		record := e.renderer.LastRecord()
		present := e.renderer.LastPresent()
		e.cpu.add(CPUGPUWait, wait)
		e.cpu.add(CPURecord, record)
		e.cpu.add(CPUPresent, present)
		e.cpu.add(CPUSubmit, -(wait + record + present))

		e.cpu.endFrame(time.Since(frameStart))

		e.frameCount++
		if t := e.trace; t != nil {
			t.Int("rendered", e.frameCount)
			t.End()
		}
		if e.maxFrames > 0 && e.frameCount >= e.maxFrames {
			return
		}
	}
}

// captureIfRequested writes the screenshot asked for by WithScreenshot, if
// any. A capture failure is logged rather than returned: losing a screenshot
// should not look like the program crashed.
func (e *Engine) captureIfRequested() {
	if e.screenshot == "" {
		return
	}
	if err := e.renderer.SaveScreenshot(e.screenshot); err != nil {
		log.Printf("glyphengine: screenshot: %v", err)
		return
	}
	log.Printf("glyphengine: wrote screenshot %s", e.screenshot)
}

// renderFrame builds the draw list and lighting for the current camera and
// day/night state, then submits one frame.
func (e *Engine) renderFrame() {
	view, proj, vp := e.viewProjection()

	// Compute cascade VPs first so buildDrawList can include shadow casters
	// that are outside the camera frustum. The far cascade's frustum is a
	// superset of the near one, so it alone decides shadow-only inclusion.
	// Resolve the environment once for the whole frame. Asking it twice could
	// return different answers -- a custom EnvironmentSource is free to be as
	// stateful as it likes -- and half a frame lit by one sky and half by
	// another is a hard bug to find.
	env := e.Scene.Environment()

	shadowEnabled := env.CastShadows
	var cascadeVPs [renderer.ShadowCascades]mgl32.Mat4
	if shadowEnabled {
		cascadeVPs = renderer.ComputeCascadeVPs(env.SunDir, e.cameraCenter)
	}

	e.cpu.begin(CPUDrawList)
	draws := e.buildDrawList(vp, shadowEnabled, cascadeVPs[renderer.ShadowCascades-1])

	// The sun and moon go in their own list rather than the draw list. There
	// they wrote depth, which rejected the sky pass on those pixels and left
	// the clouds unable to pass in front of them. See createCelestialPipeline.
	var celestials []renderer.RenderObject

	// Celestial billboards fade near the horizon instead of cutting out; the
	// environment decides whether they exist at all.
	if env.DrawSun {
		celestials = append(celestials, e.buildSunObject(vp, env))
	}
	if env.DrawMoon {
		celestials = append(celestials, e.buildMoonObject(vp, env))
	}

	// Project the sun to screen space for the light shafts. It sits at a fixed
	// distance along its direction, the same place the disc is drawn, so the
	// shafts radiate from the disc rather than from a point near it.
	//
	// What leaves here is the strength the pass will DRAW with, edge fade and
	// all, not the strength the game set. Zero means the pass cannot put a
	// pixel on screen, and renderer.recordCommandBuffer reads it that way: a
	// frame with no water runs the whole water pass -- a full scene copy and a
	// second render pass -- for the shafts alone, so every case that could not
	// contribute has to be decided before it gets there.
	var sunScreen [2]float32
	shaftStrength := env.LightShafts
	if shaftStrength > 0 {
		sp := e.cameraEye.Add(mgl32.Vec3{env.SunDiscDir[0], env.SunDiscDir[1], env.SunDiscDir[2]}.Mul(e.celestialDistance()))
		clip := vp.Mul4x1(mgl32.Vec4{sp.X(), sp.Y(), sp.Z(), 1})
		if clip.W() > 0 {
			ndc := mgl32.Vec3{clip.X() / clip.W(), clip.Y() / clip.W(), clip.Z() / clip.W()}
			sunScreen = [2]float32{ndc.X()*0.5 + 0.5, ndc.Y()*0.5 + 0.5}
			shaftStrength *= shaftEdgeFade(sunScreen)
		} else {
			// Behind the camera: there is nothing on screen to radiate from.
			shaftStrength = 0
		}
	}

	// Camera basis for billboard particles.
	camForward := e.cameraCenter.Sub(e.cameraEye).Normalize()
	camRight := camForward.Cross(mgl32.Vec3{0, 1, 0}).Normalize()
	camUp := camRight.Cross(camForward).Normalize()

	// Refreshed into an Engine field rather than allocated fresh each frame:
	// SceneLighting takes a pointer because nil there has to mean "the
	// default" (see renderer.NightGrade), and a per-frame &NightGrade{} would
	// be an allocation in the draw path for a value that almost never moves.
	g := e.Scene.NightGrade()
	e.nightGrade = renderer.NightGrade{
		Strength: g.Strength,
		Tint:     [3]float32{g.Tint.X(), g.Tint.Y(), g.Tint.Z()},
	}
	v := e.Scene.Volumetrics()
	e.volumetrics = renderer.Volumetrics{Anisotropy: v.Anisotropy, Steps: v.Steps}
	p := e.Scene.SkyPalette()
	e.skyPalette = renderer.SkyPalette{
		ZenithDay:       p.ZenithDay,
		HorizonDay:      p.HorizonDay,
		ZenithTwilight:  p.ZenithTwilight,
		HorizonTwilight: p.HorizonTwilight,
		ZenithNight:     p.ZenithNight,
		HorizonNight:    p.HorizonNight,
	}

	lighting := renderer.SceneLighting{
		VP:            vp,
		CameraRight:   [3]float32{camRight.X(), camRight.Y(), camRight.Z()},
		CameraUp:      [3]float32{camUp.X(), camUp.Y(), camUp.Z()},
		SunDir:        env.SunDir,
		SunColor:      env.SunColor,
		PointPos:      e.Scene.pointPos,
		PointRange:    e.Scene.pointRange,
		PointColor:    e.Scene.pointColor,
		Ambient:       env.Ambient,
		SkyColor:      [4]float32{env.ClearColor[0], env.ClearColor[1], env.ClearColor[2], 1},
		InvVP:         vp.Inv(),
		CameraPos:     [3]float32{e.cameraEye.X(), e.cameraEye.Y(), e.cameraEye.Z()},
		Time:          e.elapsed,
		NightFactor:   env.StarFade,
		SunElevation:  env.SunElevation,
		RealSunDir:    env.RealSunDir,
		CascadeVPs:    cascadeVPs,
		ShadowEnabled: shadowEnabled,
		NightGrade:    &e.nightGrade,
		SkyPalette:    &e.skyPalette,
		Volumetrics:   &e.volumetrics,
		FogDensity:    env.FogDensity,
		FogHeight:     env.FogHeight,
		FogBaseHeight: env.FogBaseHeight,
		DrawSky:       env.DrawSky,
		DrawStars:     env.DrawStars,
		MilkyWay:      env.MilkyWay,
		StarDensity:   env.StarDensity,
		CloudSteps:    env.CloudSteps,
		LightShafts:   shaftStrength,
		SunScreenPos:  sunScreen,
		ShaftShape: renderer.LightShaftShape{
			Radius:    env.LightShaftShape.Radius,
			Decay:     env.LightShaftShape.Decay,
			Threshold: env.LightShaftShape.Threshold,
		},
	}

	// Bin the lights for THIS frame's camera and framebuffer. proj came from
	// Aspect() and the extent below comes from the same swapchain, which is
	// the one the frame is about to be recorded against -- so the grid, the
	// matrices and gl_FragCoord cannot be describing different cameras.
	//
	// A resize does not leave the grid stale, not even for a frame. DrawFrame
	// recreates the swapchain in two places and neither of them renders
	// afterwards: on an out-of-date acquire it returns immediately, so the
	// frame that would have used the old extent is never drawn at all, and
	// after present the frame is already recorded. Either way the next
	// renderFrame reads the new extent here before it bins anything. Nothing
	// is resized on the GPU side either -- the grid is a cell COUNT, so only
	// the header's screen scale changes.
	e.cpu.begin(CPUCluster)
	fbWidth, fbHeight := e.renderer.Extent()
	lighting.Lights, lighting.Clusters = e.clusterFrameLights(view, proj, fbWidth, fbHeight)
	lighting.LightFlags = e.lightFlags() | volumetricFlag(lighting.Lights, e.Scene.volumetrics)

	e.cpu.begin(CPUSubmit)
	// Debug text rides in its own channel, appended rather than merged, so a
	// game calling SetMSDFOverlays neither loses its own text nor clobbers this.
	msdf := e.msdfOverlays
	if dbg := e.debugOverlay(); len(dbg) > 0 {
		msdf = append(append([]renderer.RenderObject(nil), msdf...), dbg...)
	}
	// Recorded here rather than earlier so the draw list is the one the
	// recorder is about to walk, sort and all.
	if t := e.trace; t != nil {
		e.traceSimulation(t, view, proj, vp, env)
		traceDrawList(t, "draws", draws)
		traceDrawList(t, "overlays", e.overlays)
		traceDrawList(t, "celestials", celestials)
		traceDrawList(t, "msdf", msdf)
		t.Int("uioverlays", len(e.uiOverlays))
	}
	if err := e.renderer.DrawFrame(draws, e.overlays, celestials, e.uiOverlays, msdf, lighting); err != nil {
		log.Printf("glyphengine: draw error: %v", err)
	}
}

// identityModel fills the Model slot of an instanced draw. Nothing reads it --
// lit_instanced.vert takes the model from its per-instance attribute and the
// fragment stage never read pc.model -- but RenderObject.ViewDepth does, so a
// zero matrix would put every set at the origin if one ever needed sorting.
var identityModel = [16]float32{1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1}

// buildDrawList turns every entity with Transform+MeshRef into a RenderObject
// with a combined view-projection-model matrix.
//
// When shadow mapping is active, objects outside the camera frustum but inside
// the light frustum are included with ShadowOnly set, so they still cast.
func (e *Engine) buildDrawList(vp mgl32.Mat4, shadowEnabled bool, lightVP mgl32.Mat4) []renderer.RenderObject {
	cameraFrustum := ExtractFrustum(vp)
	var lightFrustum Frustum
	if shadowEnabled {
		lightFrustum = ExtractFrustum(lightVP)
	}
	c := e.Scene.C
	interp := e.Scene.Interpolate
	alpha := e.alpha
	draws := e.drawBuf[:0] // reuse backing array
	ecs.Query2(c.Transform, c.MeshRef, func(entity ecs.Entity, t *Transform, mr *MeshRef) {
		if c.Hidden.Has(entity) {
			return
		}

		// Draw where the entity is *between* ticks. Entities with no previous
		// transform — Static geometry, freshly spawned, just teleported — fall
		// through to their current one.
		model := t.ModelMatrix()
		if interp {
			if prev, ok := c.PrevTransform.Get(entity); ok {
				blended := LerpTransform(Transform(*prev), *t, alpha)
				model = blended.ModelMatrix()
			}
		}

		// Frustum cull against the camera; with shadows on, also test the
		// light frustum before discarding.
		shadowOnly := false
		mesh := mr.Mesh
		if mesh.BoundRadius > 0 {
			bc := mesh.BoundCenter
			wc := model.Mul4x1(mgl32.Vec4{bc[0], bc[1], bc[2], 1})
			sx := mgl32.Vec3{model[0], model[1], model[2]}.Len()
			sy := mgl32.Vec3{model[4], model[5], model[6]}.Len()
			sz := mgl32.Vec3{model[8], model[9], model[10]}.Len()
			wr := mesh.BoundRadius * max32(sx, max32(sy, sz))
			if !cameraFrustum.SphereInFrustum(wc[0], wc[1], wc[2], wr) {
				if !shadowEnabled || !lightFrustum.SphereInFrustum(wc[0], wc[1], wc[2], wr) {
					return
				}
				shadowOnly = true
			}
		}

		color := [3]float32{1, 1, 1}
		if col, ok := c.Color.Get(entity); ok {
			color = [3]float32{col.R, col.G, col.B}
		}
		if c.Highlighted.Has(entity) {
			color[0] = min(color[0]+0.25, 1.0)
			color[1] = min(color[1]+0.25, 1.0)
			color[2] = min(color[2]+0.25, 1.0)
		}

		var tex *renderer.Texture
		var terrainMat *renderer.TerrainMaterial
		var pbr *renderer.Material
		if mat, ok := c.MaterialRef.Get(entity); ok {
			tex = mat.Texture
			terrainMat = mat.Terrain
			pbr = mat.PBR
		}

		var water *renderer.WaterParams
		if w, ok := c.Water.Get(entity); ok {
			water = &renderer.WaterParams{
				Amplitude:       w.Options.WaveAmplitude,
				WaveLength:      w.Options.WaveLength,
				AbsorptionDepth: w.Options.AbsorptionDepth,
				RefractStrength: w.Options.RefractStrength,
				WaveNoise:       w.Options.WaveNoise,
			}
		}

		var joints *renderer.JointBuffer
		if sref, ok := c.SkeletonRef.Get(entity); ok {
			if sref.Skinned && sref.JointBuffer != nil {
				joints = sref.JointBuffer
			}
		}

		// Translucency. Alpha at or below zero means the entity has been faded
		// all the way out, which is Hidden by another name -- dropping it here
		// costs a draw call rather than blending a fully transparent object
		// over the frame. At or above one it falls through to the opaque path,
		// so a fade-in can run to 1 and get the cheaper pipeline back without
		// the game special-casing the last frame.
		var alpha float32
		if tr, ok := c.Translucent.Get(entity); ok {
			if tr.Alpha <= 0 {
				return
			}
			if tr.Alpha < 1 {
				alpha = tr.Alpha
			}
		}

		draws = append(draws, renderer.RenderObject{
			Mesh:        mesh,
			Texture:     tex,
			SortID:      uint64(entity),
			MVP:         vp.Mul4(model),
			Model:       model,
			Color:       color,
			Metallic:    mr.Metallic,
			Roughness:   mr.Roughness,
			Emissive:    c.Emissive.Has(entity),
			Alpha:       alpha,
			DoubleSided: c.DoubleSided.Has(entity),
			// A translucent object throwing a solid shadow is what turns a
			// placement preview back into a building, so the decision is made
			// here where the draw is built rather than discovered in a shader.
			// RenderObject.IsTranslucent decides what counts, so this cannot
			// drift from what the blended pass actually draws.
			NoCastShadow: c.NoCastShadow.Has(entity) || alpha > 0,
			ShadowOnly:   shadowOnly,
			Joints:       joints,
			TerrainMat:   terrainMat,
			Water:        water,
			Material:     pbr,
		})
	})

	// Instance sets. They have no Transform -- every placement carries its own
	// model matrix -- so they are a second query rather than a branch inside the
	// first, and they emit one RenderObject each however many placements they
	// hold.
	//
	// Culling is against the set's whole bound. A set with one dome on screen
	// therefore draws all of them, which is the trade this feature makes: the
	// alternative is culling per placement and re-uploading the visible subset
	// every frame, which is the CPU cost the instancing exists to remove. The
	// GPU cost of the off-screen placements is vertex shading a few hundred
	// thousand triangles, which is not where frames go. See
	// docs/agents/instancing.md for the numbers.
	c.InstancedMesh.Each(func(entity ecs.Entity, im *InstancedMesh) {
		if im.Set == nil || im.Set.Count() == 0 || c.Hidden.Has(entity) {
			return
		}

		center, radius := im.Set.Bounds()
		shadowOnly := false
		if radius > 0 && !cameraFrustum.SphereInFrustum(center[0], center[1], center[2], radius) {
			if !shadowEnabled || !lightFrustum.SphereInFrustum(center[0], center[1], center[2], radius) {
				return
			}
			shadowOnly = true
		}

		color := [3]float32{1, 1, 1}
		if col, ok := c.Color.Get(entity); ok {
			color = [3]float32{col.R, col.G, col.B}
		}

		var tex *renderer.Texture
		if mat, ok := c.MaterialRef.Get(entity); ok {
			tex = mat.Texture
		}

		var roughness, metallic float32
		if mr, ok := c.MeshRef.Get(entity); ok {
			roughness, metallic = mr.Roughness, mr.Metallic
		}

		draws = append(draws, renderer.RenderObject{
			Mesh:         im.Set.Mesh,
			Instances:    im.Set,
			SortID:       uint64(entity),
			Texture:      tex,
			Model:        identityModel,
			Color:        color,
			Roughness:    roughness,
			Metallic:     metallic,
			Emissive:     c.Emissive.Has(entity),
			DoubleSided:  c.DoubleSided.Has(entity),
			NoCastShadow: c.NoCastShadow.Has(entity),
			ShadowOnly:   shadowOnly,
		})
	})

	// Opaque geometry groups by pipeline variant then texture, so command
	// recording does far fewer pipeline binds and descriptor switches. It is
	// all depth-tested, so its draw order does not affect the image.
	//
	// Translucent geometry sorts after all of it, and within itself back to
	// front by distance from the eye. That ordering *is* the image: blending is
	// not commutative, so two overlapping ghosts drawn the wrong way round
	// composite the wrong colours. It is redone every frame because it depends
	// on where the camera is, not on what the scene contains.
	//
	// A permutation the query could have produced on its own, forced. See
	// provoke.go, and TestReversedInputSortsToTheSameSequence: with a total
	// order the sorted sequence is the same either way, which is a stronger
	// statement than the capture gate's "the picture did not change".
	if e.provoke.reverse {
		slices.Reverse(draws)
	}

	eye := [3]float32{e.cameraEye.X(), e.cameraEye.Y(), e.cameraEye.Z()}
	sorted := e.sortDraws(draws, eye)

	e.drawBuf = draws // keep the assembly buffer for next frame
	return sorted
}

// drawOrder is one draw's place in the sorted list: everything the comparison
// reads, and the index to gather from.
//
// The draw list is sorted through this rather than in place because a
// RenderObject is 224 bytes, and slices.SortFunc both copies its operands into
// the comparison and moves them on every swap. Measured on 4000 opaque draws
// arriving in map order (BenchmarkDrawSort): sorting the RenderObjects in place
// costs 1095 us, sorting these 24-byte keys and gathering costs 306 us.
//
// It only started to matter when the sort became a total order. Before that
// most pairs compared equal and pdqsort stopped early -- 59 us to produce an
// order that was not one. That is the number the 306 has to be read against.
//
// Field order is load-bearing: written as declared this is exactly 24 bytes,
// and reordering it so the uint64s do not lead pads it to 32.
type drawOrder struct {
	// key is RenderObject.SortKey for an opaque draw, and 1<<63 for a blended
	// one -- SortKey leaves that bit clear so this single compare puts the
	// blended tail after every opaque draw as well as grouping by state.
	key   uint64
	id    uint64
	depth float32
	idx   uint32
}

// sortDraws orders the frame's draws and returns them gathered into the
// engine's second draw buffer.
//
// The comparison is a TOTAL order, and that is the whole reason the recorded
// sequence is a function of the scene rather than of this frame. The list
// arrives in Go map order -- ecs.Query2 and Store.Each range a map[Entity]*T,
// which Go randomises per range statement -- and slices.SortFunc is not
// stable, so anything left equal keeps whichever position the walk happened to
// hand it. Two draws with the same variant and the same set-0 resource used to
// be equal, as did two blended draws at the same distance from the eye, and
// the key's low bits were a descriptor set ADDRESS, so even the groups changed
// places between processes. draws= in the state trace differed between two
// runs of eight of the thirteen scenes the determinism gate covers; the image
// did not, which is why this sat latent until the trace went looking (issue
// #53).
//
// Fixed here rather than by giving the ECS an iteration order: that would put
// a cost on every query in the engine to remove a randomness this sort has to
// absorb anyway. GLYPHENGINE_PROVOKE_DRAW_ORDER=reverse permutes the list on
// purpose, and a stable sort over an ordered input would still have to answer
// for it.
func (e *Engine) sortDraws(draws []renderer.RenderObject, eye [3]float32) []renderer.RenderObject {
	order := e.drawOrderBuf[:0]
	for i := range draws {
		d := &draws[i]
		o := drawOrder{id: d.SortID, idx: uint32(i)}
		if d.IsTranslucent() {
			o.key = 1 << 63
			o.depth = d.ViewDepth(eye)
		} else {
			o.key = d.SortKey()
		}
		order = append(order, o)
	}
	e.drawOrderBuf = order

	slices.SortFunc(order, func(a, b drawOrder) int {
		switch {
		case a.key < b.key:
			return -1
		case a.key > b.key:
			return 1
		}
		// Farther first. Opaque draws all carry depth 0, so this costs them one
		// compare and decides nothing -- their order is the key above.
		switch {
		case a.depth > b.depth:
			return -1
		case a.depth < b.depth:
			return 1
		}
		// Equal depth, or the same variant and resource: the case that would
		// flicker between frames rather than merely differ between runs.
		// Entity ids come from a counter in spawn order, so this is stable
		// across frames and across runs of the same program, and it is an
		// identity the scene chose rather than one the allocator or the map
		// walk did. No two draws reach here equal, so the result is one
		// permutation whatever order the list arrived in.
		switch {
		case a.id < b.id:
			return -1
		case a.id > b.id:
			return 1
		default:
			return 0
		}
	})

	sorted := e.drawSorted[:0]
	for i := range order {
		sorted = append(sorted, draws[order[i].idx])
	}
	// Stored here rather than by the caller: a caller that forgot would hand
	// back a correct list having reallocated the whole gather buffer, which
	// costs three times the sort and shows up as nothing but a slow frame.
	e.drawSorted = sorted
	return sorted
}

// buildBillboard returns a model matrix for a camera-facing quad at pos,
// uniformly scaled.
func (e *Engine) buildBillboard(pos mgl32.Vec3, scale float32) mgl32.Mat4 {
	forward := e.cameraEye.Sub(pos).Normalize()
	right := mgl32.Vec3{0, 1, 0}.Cross(forward).Normalize()
	if right.Len() < 0.001 {
		right = mgl32.Vec3{1, 0, 0}
	}
	up := forward.Cross(right).Normalize()

	right = right.Mul(scale)
	up = up.Mul(scale)
	fwd := forward.Mul(scale)

	// Column-major: col0=right, col1=up, col2=forward. +Z toward the camera
	// keeps the front face visible.
	return mgl32.Mat4{
		right[0], right[1], right[2], 0,
		up[0], up[1], up[2], 0,
		fwd[0], fwd[1], fwd[2], 0,
		pos[0], pos[1], pos[2], 1,
	}
}

// celestialScale makes a celestial body larger near the horizon, approximating
// atmospheric magnification, and smaller at zenith.
func celestialScale(dir [3]float32) float32 {
	elevation := dir[1] // 0 at horizon, ~1 at zenith
	if elevation < 0 {
		elevation = 0
	}
	return 1.0 - 0.45*elevation
}

// celestialTunedDistance is the distance the celestialScale sizes were chosen
// at. Disc scale is expressed relative to it, so moving the billboards does not
// change how large they look.
const celestialTunedDistance = 80.0

// celestialDistance is how far from the camera the sun and moon billboards sit:
// just inside the far clip plane.
//
// They stand in for bodies at infinity, and the depth buffer gives them exactly
// one correct home — the farthest depth still distinguishable from the far plane
// itself. Both bounds are load-bearing:
//
//   - Nearer, and a disc occludes the world. These used to sit at a fixed 80
//     units, which put the sun in front of every piece of terrain further away
//     than that: the disc drew over the mountains it should have been behind.
//   - At the far plane exactly, the sky erases them. The sky draws after all
//     opaque geometry with CompareOpGreaterOrEqual, so a disc sharing the far
//     plane's depth loses that tie and never appears.
//
// Deriving it from e.far rather than hardcoding a distance also means a game
// that shortens its far plane cannot accidentally push the discs outside it.
func (e *Engine) celestialDistance() float32 { return e.far * 0.98 }

// celestialModel returns the billboard transform for a celestial body in
// direction dir, sized so its apparent radius is independent of where
// celestialDistance puts it.
func (e *Engine) celestialModel(dir [3]float32) mgl32.Mat4 {
	dist := e.celestialDistance()
	pos := e.cameraEye.Add(mgl32.Vec3{dir[0], dir[1], dir[2]}.Mul(dist))
	return e.buildBillboard(pos, celestialScale(dir)*dist/celestialTunedDistance)
}

// horizonFade returns a 0–1 multiplier that fades a celestial body as it dips
// below the horizon.
func horizonFade(dir [3]float32) float32 {
	y := dir[1]
	if y >= 0.1 {
		return 1.0
	}
	if y <= -0.15 {
		return 0.0
	}
	return (y + 0.15) / 0.25
}

func (e *Engine) buildSunObject(vp mgl32.Mat4, env EnvironmentState) renderer.RenderObject {
	sd := env.SunDiscDir
	model := e.celestialModel(sd)

	// SunDiscColor already fades with elevation; fading again here would
	// make the sun vanish well before it reaches the horizon.
	sc := env.SunDiscColor

	return renderer.RenderObject{
		Mesh:     e.sunMesh,
		MVP:      vp.Mul4(model),
		Model:    model,
		Color:    sc,
		Emissive: true,
	}
}

func (e *Engine) buildMoonObject(vp mgl32.Mat4, env EnvironmentState) renderer.RenderObject {
	md := env.MoonDiscDir
	model := e.celestialModel(md)

	fade := horizonFade(md)

	// Above 1 for the same reason the sun is: below it the moon cannot cross a
	// bloom threshold at all, so it renders as a flat white disc pasted on the
	// sky rather than as something giving off light.
	//
	// Well under the sun's 5, though. The moon is the brightest thing in a night
	// sky but it is not a sun, and matching them would flatten the difference
	// between the two halves of the cycle. Tuned down from 2.2 alongside the
	// night sky and the cloud lighting: the three have to move together or the
	// moon ends up a hole punched in a dark sky.
	const moonBoost = 1.5

	return renderer.RenderObject{
		Mesh:  e.moonMesh,
		MVP:   vp.Mul4(model),
		Model: model,
		Color: [3]float32{
			0.85 * fade * moonBoost,
			0.88 * fade * moonBoost,
			0.95 * fade * moonBoost,
		},
		Emissive: true,
	}
}

// Destroy runs the game's Shutdown hook, then releases renderer and window
// resources. Call once, after Run returns.
func (e *Engine) Destroy() {
	if sg, ok := e.game.(ShutdownGame); ok {
		sg.Shutdown(e)
	}
	e.teardown()
}

// teardown releases whatever has been created so far. Safe to call from a
// partially constructed Engine.
func (e *Engine) teardown() {
	// Closed before the renderer, which holds the same pointer: writing to a
	// closed file is the one way a diagnostic could take a clean shutdown down.
	if e.trace != nil {
		if e.renderer != nil {
			e.renderer.SetStateTrace(nil)
		}
		if err := e.trace.Close(); err != nil {
			log.Printf("glyphengine: %s: %v", stateTraceEnv, err)
		}
		e.trace = nil
	}
	if e.renderer != nil {
		e.renderer.Destroy()
		e.renderer = nil
	}
	if e.window != nil {
		e.window.Destroy()
		e.window = nil
	}
}

// tonemapCurveNames indexes SetTonemap's curve selector for logging.
var tonemapCurveNames = []string{"identity", "extended Reinhard", "ACES filmic"}

// handleDebugKeys implements WithDebugKeys: F1 toggles bloom, F2 cycles the
// tonemap curve. Both log, because a toggle whose effect you cannot see -- a
// scene with nothing above the bloom threshold, say -- is otherwise
// indistinguishable from a key that did not register.
func (e *Engine) handleDebugKeys() {
	r := e.renderer

	if e.input.KeyPressed(input.KeyF1) {
		intensity, threshold, knee, radius := r.Bloom()
		if threshold <= 0 {
			// Never configured. Use the starting point bloom.md recommends
			// rather than zeroes, which would switch bloom "on" into a
			// threshold of 0 and smear the whole frame.
			threshold, knee, radius = 1.2, 0.2, 1.0
		}
		if intensity > 0 {
			e.debugBloom = intensity
			r.SetBloom(0, threshold, knee, radius)
			log.Println("debug: bloom off")
		} else {
			restore := e.debugBloom
			if restore <= 0 {
				restore = 0.7
			}
			r.SetBloom(restore, threshold, knee, radius)
			log.Printf("debug: bloom on, intensity %.2f threshold %.2f", restore, threshold)
		}
	}

	if e.input.KeyPressed(input.KeyF2) {
		exposure, curve, white := r.Tonemap()
		next := (int(curve) + 1) % len(tonemapCurveNames)
		if white <= 0 {
			white = 6
		}
		r.SetTonemap(exposure, float32(next), white)
		log.Printf("debug: tonemap curve %d, %s", next, tonemapCurveNames[next])
	}
}

// Light-shaft screen-edge fade. See shaftEdgeFade.
const (
	shaftFadeStart = 0.75
	shaftFadeEnd   = 1.35
)

// shaftEdgeFade is how much of the requested shaft strength survives the sun's
// distance from the middle of the frame. 1 at the centre, 0 once the sun is far
// enough outside the frame that nothing it lit is still on screen.
//
// The argument is the sun in UV space; the measure is max(|x-0.5|,|y-0.5|)*2,
// so 1.0 is the sun exactly on the frame edge and the fade is square rather
// than round -- the frame is square-cornered, and a round fade would cut the
// sun off early in the corners where it is still visible.
//
// A hard cutoff at the edge makes the shafts vanish in a single frame as the
// camera turns, which is far more noticeable than their absence, so the fade
// starts before the sun reaches the edge and finishes after it leaves.
//
// It lives here rather than in godray.frag, where it used to: it depends only
// on the sun, so it is one number per frame rather than one per pixel, and
// keeping it on this side is what lets the renderer skip the pass. A frame with
// no water enters the water pass for the shafts alone -- a full scene copy plus
// a second render pass -- and a faded-out sun is exactly the case where that
// buys nothing.
func shaftEdgeFade(sunUV [2]float32) float32 {
	dx := sunUV[0] - 0.5
	if dx < 0 {
		dx = -dx
	}
	dy := sunUV[1] - 0.5
	if dy < 0 {
		dy = -dy
	}
	d := dx
	if dy > d {
		d = dy
	}
	return 1 - smoothstep(shaftFadeStart, shaftFadeEnd, d*2)
}
