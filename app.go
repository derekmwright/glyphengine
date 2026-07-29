package glyphengine

import (
	"log"
	"slices"
	"time"

	"github.com/go-gl/mathgl/mgl32"
	"github.com/vkngwrapper/core/v3/common"

	"github.com/derekmwright/glyphengine/ecs"
	"github.com/derekmwright/glyphengine/input"
	"github.com/derekmwright/glyphengine/renderer"
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
//		g.intent.Forward = axis(e.Input(), input.KeyW, input.KeyS)
//		if e.Input().KeyPressed(input.KeySpace) {
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
	scene      *Scene
	msaa       int
	width      int
	height     int
	title      string
	appName    string
	appVersion common.Version
	fullscreen bool
	resizable  bool
	validation bool
	vsync      bool
	interp     bool
	tickRate   int
	maxCatchUp time.Duration
	maxFrames  int
	screenshot string
	fov        float32
	near, far  float32
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

// WithMaxFrames stops the main loop after n rendered frames. Zero, the
// default, runs until the window closes. This is what makes the engine
// testable on CI and profilable over a fixed workload.
func WithMaxFrames(n int) Option {
	return func(c *config) { c.maxFrames = n }
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
	smoothDelta  float64

	moonMesh *renderer.Mesh
	sunMesh  *renderer.Mesh
	elapsed  float32 // running time counter for shader animation

	drawBuf     []renderer.RenderObject // reused each frame to avoid allocs
	animScratch renderer.AnimScratch    // reused each frame by TickAnimations

	fogDensity float32 // exp² distance fog density (0 disables)

	tickDuration time.Duration
	maxCatchUp   time.Duration
	maxFrames    int
	frameCount   int
	screenshot   string
	alpha        float32 // fraction between the last two ticks; see Alpha
}

// resolveMaxCatchUp applies the default and makes sure the budget can fit at
// least one tick — a smaller budget would starve the simulation completely.
func resolveMaxCatchUp(requested, tickDuration time.Duration) time.Duration {
	if requested <= 0 {
		requested = DefaultMaxCatchUp
	}
	if requested < tickDuration {
		return tickDuration
	}
	return requested
}

// defaultFogDensity gives ~35% fog at the 80-unit grass cull distance and
// ~75% at 150 units, softening the horizon without smothering the midground.
const defaultFogDensity = 0.0075

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

	appName := cfg.appName
	if appName == "" {
		appName = cfg.title
	}
	rOpts := []renderer.Option{
		renderer.WithApplicationName(appName, cfg.appVersion),
		renderer.WithValidation(cfg.validation),
		renderer.WithVSync(cfg.vsync),
	}
	if cfg.msaa != 0 {
		rOpts = append(rOpts, renderer.WithMSAASamples(cfg.msaa))
	}
	r, err := renderer.New(w, rOpts...)
	if err != nil {
		w.Destroy()
		return nil, err
	}

	e := &Engine{
		Scene:        scene,
		window:       w,
		renderer:     r,
		input:        input.New(w.Handle()),
		game:         g,
		cameraEye:    mgl32.Vec3{0, 1, 3},
		cameraCenter: mgl32.Vec3{0, 0, 0},
		cameraUp:     mgl32.Vec3{0, 1, 0},
		fov:          cfg.fov,
		near:         cfg.near,
		far:          cfg.far,
		fogDensity:   defaultFogDensity,
		tickDuration: time.Second / time.Duration(cfg.tickRate),
		maxCatchUp:   resolveMaxCatchUp(cfg.maxCatchUp, time.Second/time.Duration(cfg.tickRate)),
		maxFrames:    cfg.maxFrames,
		screenshot:   cfg.screenshot,
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

// FrameCount returns the number of frames rendered so far.
func (e *Engine) FrameCount() int { return e.frameCount }

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

// Elapsed returns the running wall-clock time in seconds since Run started.
func (e *Engine) Elapsed() float32 { return e.elapsed }

// SetFogDensity overrides the exp² distance fog density (0 disables fog).
func (e *Engine) SetFogDensity(d float32) { e.fogDensity = d }

// FogDensity returns the current fog density.
func (e *Engine) FogDensity() float32 { return e.fogDensity }

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

// ViewProjection returns the current view-projection matrix.
//
// The projection is reverse-Z (near maps to 1, far to 0) with a flipped Y for
// Vulkan's clip space. Geometry authored for a conventional 0→1 depth range
// fails the depth test and silently draws nothing.
func (e *Engine) ViewProjection() mgl32.Mat4 {
	proj := mgl32.Perspective(mgl32.DegToRad(e.fov), e.renderer.Aspect(), e.near, e.far)
	proj[10] = e.near / (e.far - e.near)
	proj[14] = (e.far * e.near) / (e.far - e.near)
	proj[5] *= -1
	view := mgl32.LookAtV(e.cameraEye, e.cameraCenter, e.cameraUp)
	return proj.Mul4(view)
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
	var accumulator time.Duration
	prev := time.Now()
	tickDt := float32(e.tickDuration.Seconds())

	// Deferred so it covers both exits: the frame budget running out and
	// the window closing.
	defer e.captureIfRequested()

	for !e.window.ShouldClose() {
		frameStart := time.Now()
		frameDelta := frameStart.Sub(prev)
		prev = frameStart
		accumulator += frameDelta

		// Bound catch-up work so a long pause cannot spiral. Past the budget
		// the simulation falls behind instead — see WithMaxCatchUp.
		if accumulator > e.maxCatchUp {
			accumulator = e.maxCatchUp
		}

		dt := frameDelta.Seconds()
		if e.smoothDelta <= 0 {
			e.smoothDelta = dt
		} else {
			e.smoothDelta = e.smoothDelta*0.95 + dt*0.05
		}

		e.input.Update()
		e.window.PollEvents()

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
		e.game.Update(e, float32(dt))

		// Fixed-timestep simulation. Runs zero or more times per frame — which
		// is exactly why input belongs in Update above, not in here.
		for accumulator >= e.tickDuration {
			e.Scene.Tick(tickDt)
			if e.fixedUpdate != nil {
				e.fixedUpdate.FixedUpdate(e, tickDt)
			}
			accumulator -= e.tickDuration
		}

		// Whatever time is left over is how far this frame sits past the last
		// tick, in [0,1). Rendering blends by it so motion is smooth between
		// simulation steps rather than stepping at the tick rate.
		e.alpha = float32(accumulator) / float32(e.tickDuration)

		// Animation sampling is presentation, not simulation — advance it once
		// per rendered frame by the real delta. Clamped so a long pause does
		// not lurch poses forward.
		animDt := float32(dt)
		if animDt > 0.25 {
			animDt = 0.25
		}
		e.TickAnimations(animDt)

		// Last hook before rendering: transforms and poses are final, so a
		// camera following its target here is never a tick behind.
		if e.lateUpdate != nil {
			e.lateUpdate.LateUpdate(e, float32(dt))
		}

		// Skip rendering while minimized; game logic and networking continue.
		if e.renderer.Minimized() {
			time.Sleep(50 * time.Millisecond)
			continue
		}

		e.elapsed += float32(frameDelta.Seconds())
		e.renderFrame()

		e.frameCount++
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
	vp := e.ViewProjection()

	// Compute cascade VPs first so buildDrawList can include shadow casters
	// that are outside the camera frustum. The far cascade's frustum is a
	// superset of the near one, so it alone decides shadow-only inclusion.
	dn := &e.Scene.dayNight
	shadowEnabled := dn.SunAboveHorizon()
	var cascadeVPs [renderer.ShadowCascades]mgl32.Mat4
	if shadowEnabled {
		cascadeVPs = renderer.ComputeCascadeVPs(dn.SunDir(), e.cameraCenter)
	}

	draws := e.buildDrawList(vp, shadowEnabled, cascadeVPs[renderer.ShadowCascades-1])

	// Celestial billboards fade near the horizon instead of cutting out.
	if sd := dn.SunDir(); sd[1] > -0.15 {
		draws = append(draws, e.buildSunObject(vp))
	}
	if md := dn.MoonDir(); md[1] > -0.15 {
		draws = append(draws, e.buildMoonObject(vp))
	}

	// One directional light, handed over from sun to moon where both are near
	// zero rather than at a clock boundary -- see DayNight.PrimaryLight.
	sunDir, sunColor := dn.PrimaryLight()

	// Camera basis for billboard particles.
	camForward := e.cameraCenter.Sub(e.cameraEye).Normalize()
	camRight := camForward.Cross(mgl32.Vec3{0, 1, 0}).Normalize()
	camUp := camRight.Cross(camForward).Normalize()

	lighting := renderer.SceneLighting{
		VP:            vp,
		CameraRight:   [3]float32{camRight.X(), camRight.Y(), camRight.Z()},
		CameraUp:      [3]float32{camUp.X(), camUp.Y(), camUp.Z()},
		SunDir:        sunDir,
		SunColor:      sunColor,
		PointPos:      e.Scene.pointPos,
		PointRange:    e.Scene.pointRange,
		PointColor:    e.Scene.pointColor,
		Ambient:       dn.AmbientColor(),
		SkyColor:      dn.SkyColor(),
		InvVP:         vp.Inv(),
		CameraPos:     [3]float32{e.cameraEye.X(), e.cameraEye.Y(), e.cameraEye.Z()},
		Time:          e.elapsed,
		NightFactor:   dn.StarVisibility(),
		SunElevation:  dn.SunDir()[1],
		CascadeVPs:    cascadeVPs,
		ShadowEnabled: shadowEnabled,
		FogDensity:    e.fogDensity,
	}

	pls := e.Scene.pointLights
	if len(pls) > renderer.MaxPointLights {
		pls = pls[:renderer.MaxPointLights]
	}
	lighting.PointLightCount = len(pls)
	for i, pl := range pls {
		lighting.PointLights[i] = renderer.PointLightData{
			Pos:   [3]float32{pl.Pos.X(), pl.Pos.Y(), pl.Pos.Z()},
			Range: pl.Range,
			Color: [3]float32{pl.Color.X(), pl.Color.Y(), pl.Color.Z()},
		}
	}

	if err := e.renderer.DrawFrame(draws, e.overlays, e.uiOverlays, e.msdfOverlays, lighting); err != nil {
		log.Printf("glyphengine: draw error: %v", err)
	}
}

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
		if mat, ok := c.MaterialRef.Get(entity); ok {
			tex = mat.Texture
			terrainMat = mat.Terrain
		}

		var water *renderer.WaterParams
		if w, ok := c.Water.Get(entity); ok {
			water = &renderer.WaterParams{
				Amplitude:       w.Options.WaveAmplitude,
				WaveLength:      w.Options.WaveLength,
				AbsorptionDepth: w.Options.AbsorptionDepth,
				RefractStrength: w.Options.RefractStrength,
			}
		}

		var joints *renderer.JointBuffer
		if sref, ok := c.SkeletonRef.Get(entity); ok {
			if sref.Skinned && sref.JointBuffer != nil {
				joints = sref.JointBuffer
			}
		}

		draws = append(draws, renderer.RenderObject{
			Mesh:         mesh,
			Texture:      tex,
			MVP:          vp.Mul4(model),
			Model:        model,
			Color:        color,
			Metallic:     mr.Metallic,
			Roughness:    mr.Roughness,
			Emissive:     c.Emissive.Has(entity),
			DoubleSided:  c.DoubleSided.Has(entity),
			NoCastShadow: c.NoCastShadow.Has(entity),
			ShadowOnly:   shadowOnly,
			Joints:       joints,
			TerrainMat:   terrainMat,
			Water:        water,
		})
	})

	// Group by pipeline variant then texture so command recording does far
	// fewer pipeline binds and descriptor switches. All lit geometry is opaque
	// and depth-tested, so draw order does not affect the image.
	slices.SortFunc(draws, func(a, b renderer.RenderObject) int {
		ka, kb := a.SortKey(), b.SortKey()
		switch {
		case ka < kb:
			return -1
		case ka > kb:
			return 1
		default:
			return 0
		}
	})

	e.drawBuf = draws // keep the backing array for next frame
	return draws
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

func (e *Engine) buildSunObject(vp mgl32.Mat4) renderer.RenderObject {
	sd := e.Scene.dayNight.SunDir()
	pos := e.cameraEye.Add(mgl32.Vec3{sd[0], sd[1], sd[2]}.Mul(80))
	model := e.buildBillboard(pos, celestialScale(sd))

	// SunDiscColor already fades with elevation; fading again here would
	// make the sun vanish well before it reaches the horizon.
	sc := e.Scene.dayNight.SunDiscColor()

	return renderer.RenderObject{
		Mesh:     e.sunMesh,
		MVP:      vp.Mul4(model),
		Model:    model,
		Color:    sc,
		Emissive: true,
	}
}

func (e *Engine) buildMoonObject(vp mgl32.Mat4) renderer.RenderObject {
	md := e.Scene.dayNight.MoonDir()
	pos := e.cameraEye.Add(mgl32.Vec3{md[0], md[1], md[2]}.Mul(80))
	model := e.buildBillboard(pos, celestialScale(md))

	fade := horizonFade(md)

	return renderer.RenderObject{
		Mesh:     e.moonMesh,
		MVP:      vp.Mul4(model),
		Model:    model,
		Color:    [3]float32{0.85 * fade, 0.88 * fade, 0.95 * fade},
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
	if e.renderer != nil {
		e.renderer.Destroy()
		e.renderer = nil
	}
	if e.window != nil {
		e.window.Destroy()
		e.window = nil
	}
}
