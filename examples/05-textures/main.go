// Command 05-textures loads a texture from an embedded filesystem and applies
// it to geometry.
//
// This is the first example that loads anything. Every loader in the engine
// takes an fs.FS rather than a path, which means the same call works against
// an embed.FS baked into the binary, an os.DirFS pointing at a mod directory,
// a zip, or a test fixture — and it means the working directory never decides
// whether your game finds its assets.
//
//	//go:embed assets
//	var assetsFS embed.FS
//
//	tex, err := r.LoadTexture(assetsFS, "assets/crate.png")
//
// Pass -disk to load the identical asset through os.DirFS instead, which is
// the same call with a different filesystem. Nothing else changes.
//
//	go run ./05-textures              # texture from the embedded FS
//	go run ./05-textures -disk        # same texture, read from disk
//	go run ./05-textures -frames 60   # render 60 frames, then exit
//
// The texture itself is generated procedurally rather than sourced, so the
// repository carries no third-party art. See assets/README.md.
package main

import (
	"embed"
	"flag"
	"io/fs"
	"log"
	"math"
	"os"
	"runtime"

	"github.com/go-gl/mathgl/mgl32"

	glyph "github.com/derekmwright/glyphengine"
	"github.com/derekmwright/glyphengine/input"
)

//go:embed assets
var assetsFS embed.FS

func init() {
	// GLFW must be called from the thread that initialized it.
	runtime.LockOSThread()
}

type game struct {
	camera *glyph.Camera
	assets fs.FS
	crates []crate
}

type crate struct {
	entity glyph.Entity
	spin   float32
}

func (g *game) Init(e *glyph.Engine) error {
	r := e.Renderer()

	// One call, one filesystem. Whether that filesystem is compiled into the
	// binary or read from disk is the caller's business, not the engine's.
	tex, err := r.LoadTexture(g.assets, "assets/crate.png")
	if err != nil {
		return err
	}

	ground, err := r.CreatePlane(40, 40)
	if err != nil {
		return err
	}
	groundEnt := e.Spawn()
	e.C.Transform.Set(groundEnt, &glyph.Transform{Scale: mgl32.Vec3{1, 1, 1}})
	e.C.MeshRef.Set(groundEnt, &glyph.MeshRef{Mesh: ground, Roughness: 0.9})
	e.C.Color.Set(groundEnt, &glyph.Color{R: 0.30, G: 0.34, B: 0.28})
	e.C.Static.Set(groundEnt, &glyph.Static{})

	cube, err := r.CreateCube(1.0)
	if err != nil {
		return err
	}

	// A ring of textured crates, each spinning at its own rate so the texture
	// is visible on every face.
	const count = 6
	for i := 0; i < count; i++ {
		angle := float64(i) / count * 2 * math.Pi
		ent := e.Spawn()
		e.C.Transform.Set(ent, &glyph.Transform{
			Position: mgl32.Vec3{float32(math.Cos(angle)) * 4, 1, float32(math.Sin(angle)) * 4},
			Scale:    mgl32.Vec3{1.4, 1.4, 1.4},
		})
		e.C.MeshRef.Set(ent, &glyph.MeshRef{Mesh: cube, Roughness: 0.7})
		// MaterialRef is what routes an entity through the textured pipeline.
		// Without it the mesh draws with its vertex colors instead.
		e.C.MaterialRef.Set(ent, &glyph.MaterialRef{Texture: tex})
		g.crates = append(g.crates, crate{entity: ent, spin: 0.3 + float32(i)*0.15})
	}

	e.SetDayCycleSpeed(1.0 / 180.0)
	e.SetTimeOfDay(0.38)

	g.camera = glyph.NewCamera(11)
	g.camera.Target = mgl32.Vec3{0, 1, 0}
	g.camera.Pitch = 0.25

	log.Println("05-textures running. Left-drag orbits, scroll zooms, Escape quits.")
	return nil
}

func (g *game) Update(e *glyph.Engine, dt float32) {
	in := e.Input()
	if in.KeyPressed(input.KeyEscape) {
		e.Close()
	}

	for _, c := range g.crates {
		if t, ok := e.C.Transform.Get(c.entity); ok {
			t.Rotation[1] += c.spin * dt
			t.Rotation[0] += c.spin * 0.4 * dt
		}
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
	disk := flag.Bool("disk", false, "load assets from disk instead of the embedded FS")
	shot := flag.String("screenshot", "", "write a PNG of the last frame to this path")
	flag.Parse()

	g := &game{assets: assetsFS}
	if *disk {
		// The engine cannot tell the difference, which is the point.
		g.assets = os.DirFS(".")
		log.Println("loading assets from disk via os.DirFS")
	}

	opts := []glyph.Option{
		glyph.WithTitle("GlyphEngine - 05 Textures"),
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

	e, err := glyph.New(g, opts...)
	if err != nil {
		log.Fatalf("create engine: %v", err)
	}
	defer e.Destroy()

	e.Run()
	log.Printf("rendered %d frames", e.FrameCount())
}
