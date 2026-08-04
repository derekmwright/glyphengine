// Command 12-particles runs three emitters with different behaviour and draws
// them as additive billboards.
//
// Particles are not entities. They are simulated CPU-side by a
// ParticleEmitter, uploaded once per frame as a flat instance buffer, and
// drawn in a single instanced call — none of them touch the ECS, the spatial
// grid, or the draw list. Thousands of short-lived sparks would swamp all
// three for something that never collides and never gets queried.
//
// The three emitters here differ only in configuration:
//
//	fireflies   drift and pulse, and only come out at night (NightOnly)
//	sparks      rise fast from a point and burn out
//	dust        hangs almost still in a wide volume
//
// The blend is additive, so overlapping particles brighten rather than
// occlude, which is why they read as light rather than as objects. That also
// means they do not write depth: a particle behind a pillar is hidden, but two
// particles never fight over which is in front.
//
//	go run ./12-particles              # windowed
//	go run ./12-particles -frames 200  # render 200 frames, then exit
//	go run ./12-particles -time 0.5    # midday, so the fireflies stay home
//
// The camera orbits on its own. Escape quits.
package main

import (
	"flag"
	"log"
	"math"
	"runtime"

	"github.com/go-gl/mathgl/mgl32"

	glyph "github.com/derekmwright/glyphengine"
	"github.com/derekmwright/glyphengine/input"
	"github.com/derekmwright/glyphengine/renderer"
)

func init() {
	// GLFW must be called from the thread that initialized it.
	runtime.LockOSThread()
}

// maxParticles bounds the instance buffer. It is allocated once, so this is a
// ceiling on all emitters together, not a per-emitter budget.
const maxParticles = 4096

// autoOrbitRate circles the camera slowly, in radians per second.
//
// No mouse look: this example is about the three emitters, and camera
// plumbing in front of that is just noise to read past. Orbiting on its own
// also makes -frames screenshots repeatable.
const autoOrbitRate = 0.15

type game struct {
	camera *glyph.Camera

	fireflies *glyph.ParticleEmitter
	sparks    *glyph.ParticleEmitter
	dust      *glyph.ParticleEmitter

	t float32
}

func (g *game) Init(e *glyph.Engine) error {
	r := e.Renderer()

	r.InitParticles(maxParticles)

	ground, err := r.CreatePlane(40, 40)
	if err != nil {
		return err
	}
	ent := e.Spawn()
	e.C.Transform.Set(ent, &glyph.Transform{Scale: mgl32.Vec3{1, 1, 1}})
	e.C.MeshRef.Set(ent, &glyph.MeshRef{Mesh: ground, Roughness: 0.95})
	e.C.Color.Set(ent, &glyph.Color{R: 0.20, G: 0.24, B: 0.20})
	e.C.Static.Set(ent, &glyph.Static{})
	e.C.NoCastShadow.Set(ent, &glyph.NoCastShadow{})

	// Something solid for the particles to move around, and to prove they are
	// depth-tested against the scene rather than pasted over it.
	post, err := r.CreateCube(1.0)
	if err != nil {
		return err
	}
	for i := 0; i < 5; i++ {
		a := float64(i) / 5 * 2 * math.Pi
		p := e.Spawn()
		e.C.Transform.Set(p, &glyph.Transform{
			Position: mgl32.Vec3{float32(math.Cos(a)) * 5, 1.4, float32(math.Sin(a)) * 5},
			Scale:    mgl32.Vec3{0.5, 2.8, 0.5},
		})
		e.C.MeshRef.Set(p, &glyph.MeshRef{Mesh: post, Roughness: 0.85})
		e.C.Color.Set(p, &glyph.Color{R: 0.35, G: 0.32, B: 0.28})
		e.C.Static.Set(p, &glyph.Static{})
	}

	// Fireflies over a wide area. NightOnly in the config means the emitter
	// scales itself down as the sun comes up — the engine passes the night
	// factor into Tick, and the emitter decides what to do with it.
	g.fireflies = glyph.NewParticleEmitter(glyph.FireflyConfig(),
		-9, 9, -9, 9, 0.2, 0.4, 2.6)

	// Sparks from a single point, as if from a fire.
	g.sparks = glyph.NewParticleEmitter(glyph.SparkConfig(),
		-0.4, 0.4, -0.4, 0.4, 0.3, 0.1, 0.5)

	// Dust: the firefly config with the motion and colour flattened, to show
	// the same emitter doing something completely different.
	dustCfg := glyph.FireflyConfig()
	dustCfg.SpawnRate = 40
	dustCfg.MaxParticles = 260
	dustCfg.LifeMin, dustCfg.LifeMax = 8, 14
	dustCfg.SizeMin, dustCfg.SizeMax = 0.02, 0.045
	dustCfg.RMin, dustCfg.RMax = 0.75, 0.85
	dustCfg.GMin, dustCfg.GMax = 0.72, 0.82
	dustCfg.BMin, dustCfg.BMax = 0.60, 0.70
	dustCfg.TurbulenceXZ, dustCfg.TurbulenceY = 0.05, 0.02
	dustCfg.AlphaMax = 0.22
	dustCfg.NightOnly = false
	g.dust = glyph.NewParticleEmitter(dustCfg, -12, 12, -12, 12, 0.4, 0.5, 5.0)

	e.SetTimeOfDay(0.80) // blue hour: fireflies out, geometry still readable
	e.SetDayCycleSpeed(0)

	g.camera = glyph.NewCamera(15)
	g.camera.Pitch = 0.22
	g.camera.Target = mgl32.Vec3{0, 1.6, 0}

	log.Println("12-particles running. Escape quits.")
	return nil
}

func (g *game) Update(e *glyph.Engine, dt float32) {
	g.t += dt

	g.camera.Yaw += autoOrbitRate * dt
	e.SetCamera(g.camera.ViewVectors())

	// The spark source wanders, so the trail it leaves is visible.
	g.sparks.SetPosition(
		float32(math.Cos(float64(g.t)*0.7))*2.5, 0.3,
		float32(math.Sin(float64(g.t)*0.7))*2.5,
	)

	// Emitters take the night factor because some of them care. Fireflies
	// fade out in daylight; dust does not.
	night := e.Scene.StarVisibility()
	g.fireflies.Tick(dt, night)
	g.sparks.Tick(dt, night)
	g.dust.Tick(dt, night)

	// One instance buffer for every emitter. They are drawn in a single call,
	// so there is no reason to keep them apart.
	instances := make([]renderer.ParticleInstance, 0, maxParticles)
	instances = append(instances, g.fireflies.BuildInstances(night)...)
	instances = append(instances, g.sparks.BuildInstances(night)...)
	instances = append(instances, g.dust.BuildInstances(night)...)
	if len(instances) > maxParticles {
		instances = instances[:maxParticles]
	}
	e.Renderer().UpdateParticleInstances(instances)
}

func main() {
	width := flag.Int("width", 1280, "window width")
	height := flag.Int("height", 720, "window height")
	fullscreen := flag.Bool("fullscreen", false, "run fullscreen on the primary monitor")
	frames := flag.Int("frames", 0, "render N frames then exit (0 = run until closed)")
	tod := flag.Float64("time", 0.80, "time of day; fireflies only come out at night")
	shot := flag.String("screenshot", "", "write a PNG of the last frame to this path")
	flag.Parse()

	opts := []glyph.Option{
		glyph.WithTitle("GlyphEngine - 12 Particles"),
		glyph.WithWindowSize(*width, *height),
		glyph.WithMSAA(4),
		glyph.WithQuitKey(input.KeyEscape),
		glyph.WithDebugKeys(),
	}
	if *fullscreen {
		opts = append(opts, glyph.WithFullscreen())
	}
	if *frames > 0 {
		opts = append(opts, glyph.WithMaxFrames(*frames))
	}
	if *shot != "" {
		opts = append(opts, glyph.WithScreenshot(*shot))
	}

	e, err := glyph.New(&game{}, opts...)
	if err != nil {
		log.Fatalf("create engine: %v", err)
	}
	defer e.Destroy()

	e.SetTimeOfDay(float32(*tod))
	e.Run()
	log.Printf("rendered %d frames", e.FrameCount())
}
