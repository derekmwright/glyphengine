// Command 02-cube is the first example that uses the engine rather than the
// renderer directly: a Game, a Scene, entities, and an orbit camera.
//
// It shows the shape every GlyphEngine game has:
//
//   - implement Game (Init builds the scene, Update runs per frame),
//   - spawn entities and attach components (Transform, MeshRef, Color),
//   - drive the camera each frame from Update.
//
// The sun moves: the scene runs a two-minute day/night cycle, so lighting,
// ambient, sky color, and the sun and moon billboards all change while you
// watch. Nothing is loaded from disk — the cube and ground are engine
// primitives.
//
//	go run ./02-cube              # windowed
//	go run ./02-cube -frames 60   # render 60 frames, then exit (CI smoke test)
//
// Left-drag orbits, scroll zooms, Escape quits.
package main

import (
	"flag"
	"log"
	"runtime"

	"github.com/go-gl/mathgl/mgl32"

	glyph "github.com/derekmwright/glyphengine"
	"github.com/derekmwright/glyphengine/ecs"
	"github.com/derekmwright/glyphengine/input"
)

func init() {
	// GLFW must be called from the thread that initialized it.
	runtime.LockOSThread()
}

type game struct {
	camera *glyph.Camera
	cube   ecs.Entity
}

func (g *game) Init(e *glyph.Engine) error {
	r := e.Renderer()

	// A ground plane so the cube has something to cast a shadow onto.
	ground, err := r.CreatePlane(40, 40)
	if err != nil {
		return err
	}
	groundEnt := e.Spawn()
	e.C.Transform.Set(groundEnt, &glyph.Transform{Scale: mgl32.Vec3{1, 1, 1}})
	e.C.MeshRef.Set(groundEnt, &glyph.MeshRef{Mesh: ground, Roughness: 0.9})
	e.C.Color.Set(groundEnt, &glyph.Color{R: 0.35, G: 0.42, B: 0.30})

	// The cube.
	cube, err := r.CreateCube(1.0)
	if err != nil {
		return err
	}
	cubeEnt := e.Spawn()
	e.C.Transform.Set(cubeEnt, &glyph.Transform{
		Position: mgl32.Vec3{0, 1, 0},
		Scale:    mgl32.Vec3{1, 1, 1},
	})
	e.C.MeshRef.Set(cubeEnt, &glyph.MeshRef{Mesh: cube, Metallic: 0.1, Roughness: 0.4})
	e.C.Color.Set(cubeEnt, &glyph.Color{R: 0.85, G: 0.45, B: 0.25})
	g.cube = cubeEnt

	// A two-minute day, starting mid-morning so the sun is up and casting.
	e.SetDayCycleSpeed(1.0 / 120.0)
	e.SetTimeOfDay(0.35)

	g.camera = glyph.NewCamera(8)
	g.camera.Target = mgl32.Vec3{0, 1, 0}

	log.Println("02-cube running. Left-drag orbits, scroll zooms, Escape quits.")
	return nil
}

func (g *game) Update(e *glyph.Engine, dt float32) {
	in := e.Input()
	if in.KeyPressed(input.KeyEscape) {
		e.Close()
	}

	// Spin the cube. dt-scaled, so the rotation rate is frame-rate independent.
	if t, ok := e.C.Transform.Get(g.cube); ok {
		t.Rotation[1] += 0.8 * dt
	}

	g.camera.Update(in)
	g.camera.ResolveCollision(e.Scene, 0, dt)
	e.SetCamera(g.camera.ViewVectors())
}

func main() {
	width := flag.Int("width", 1280, "window width")
	height := flag.Int("height", 720, "window height")
	fullscreen := flag.Bool("fullscreen", false, "run fullscreen on the primary monitor")
	frames := flag.Int("frames", 0, "render N frames then exit (0 = run until closed)")
	shot := flag.String("screenshot", "", "write a PNG of the last frame to this path")
	flag.Parse()

	opts := []glyph.Option{
		glyph.WithTitle("GlyphEngine - 02 Cube"),
		glyph.WithWindowSize(*width, *height),
		glyph.WithMSAA(4),
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

	e.Run()
	log.Printf("rendered %d frames", e.FrameCount())
}
