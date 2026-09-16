// Command 18-translucent draws blended world geometry: the placement ghost a
// builder-style game needs, and the ordering problem that comes with it.
//
// Three things are on screen:
//
//   - a row of solid habitat domes, opaque, casting shadows;
//   - a translucent ghost of the next one, sliding along the row where it would
//     be placed. It is the same mesh and the same lighting as the solid ones —
//     only Translucent makes it a preview rather than a building.
//   - three overlapping glass panes at different depths, which is where the
//     sort earns its keep. Orbit past them and the near one has to stay in
//     front. If the pass ever stops sorting, they flicker through each other
//     as the camera crosses the plane where their order swaps.
//
// Alpha and Emissive compose, so hold E to switch the ghost to a full-bright
// hologram without it turning solid. Press T to fade everything translucent out
// and back; at zero the engine stops issuing the draw entirely, and at one it
// takes the opaque pipeline again.
//
//	go run ./18-translucent              # windowed
//	go run ./18-translucent -frames 60   # render 60 frames, then exit
//	go run ./18-translucent -alpha 0.15  # a fainter ghost
//
// Left-drag orbits, scroll zooms, E holds the hologram, T toggles the fade,
// Escape quits.
package main

import (
	"flag"
	"log"
	"math"
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

const (
	domes     = 4
	domeGap   = 3.0
	paneCount = 3

	// The row sits right of centre and the panes left of it, so one still frame
	// shows the ghost against the buildings it previews and the overlapping
	// panes without the two piling up on each other.
	domeOriginX = 3.0
	paneOriginX = -7.5

	// In front of the row rather than in it. A preview that slides through the
	// solid domes is a fine thing for a builder to do and a poor thing for an
	// example to show, because for half its travel it is inside one.
	ghostZ = 2.6
)

type game struct {
	camera *glyph.Camera
	ghost  ecs.Entity
	panes  []ecs.Entity

	alpha   float32
	fading  bool
	fade    float32
	elapsed float32
}

func (g *game) Init(e *glyph.Engine) error {
	r := e.Renderer()

	ground, err := r.CreatePlane(60, 60)
	if err != nil {
		return err
	}
	groundEnt := e.Spawn()
	e.C.Transform.Set(groundEnt, &glyph.Transform{Scale: mgl32.Vec3{1, 1, 1}})
	e.C.MeshRef.Set(groundEnt, &glyph.MeshRef{Mesh: ground, Roughness: 0.9})
	e.C.Color.Set(groundEnt, &glyph.Color{R: 0.33, G: 0.40, B: 0.29})
	e.C.Static.Set(groundEnt, &glyph.Static{})

	dome, err := r.CreateCylinder(1.0, 1.6, 24)
	if err != nil {
		return err
	}

	// The built ones. Ordinary opaque entities, nothing new here — they are the
	// reference the ghost has to read as different from.
	for i := 0; i < domes; i++ {
		x := domeOriginX + (float32(i)-float32(domes-1)/2)*domeGap
		ent := e.Spawn()
		e.C.Transform.Set(ent, &glyph.Transform{Position: mgl32.Vec3{x, 0.8, 0}, Scale: mgl32.Vec3{1, 1, 1}})
		e.C.MeshRef.Set(ent, &glyph.MeshRef{Mesh: dome, Metallic: 0.1, Roughness: 0.55})
		e.C.Color.Set(ent, &glyph.Color{R: 0.78, G: 0.74, B: 0.66})
		e.C.Static.Set(ent, &glyph.Static{})
	}

	// The one being placed. Same mesh, same lighting, one component different.
	g.ghost = e.Spawn()
	e.C.Transform.Set(g.ghost, &glyph.Transform{Position: mgl32.Vec3{domeOriginX, 0.8, ghostZ}, Scale: mgl32.Vec3{1, 1, 1}})
	e.C.MeshRef.Set(g.ghost, &glyph.MeshRef{Mesh: dome, Roughness: 0.55})
	e.C.Color.Set(g.ghost, &glyph.Color{R: 0.45, G: 0.85, B: 1.0})
	e.C.Translucent.Set(g.ghost, &glyph.Translucent{Alpha: g.alpha})

	// Overlapping panes, staggered in depth. DoubleSided because a pane seen
	// from behind should still be there — the opaque path would cull it, and so
	// would a blended pipeline that ignored the component.
	pane, err := r.CreateCube(1.0)
	if err != nil {
		return err
	}
	tints := [paneCount][3]float32{{0.9, 0.35, 0.35}, {0.35, 0.9, 0.45}, {0.4, 0.5, 0.95}}
	for i := 0; i < paneCount; i++ {
		ent := e.Spawn()
		e.C.Transform.Set(ent, &glyph.Transform{
			Position: mgl32.Vec3{paneOriginX + float32(i)*1.2, 1.6, 3.2 - float32(i)*1.7},
			Scale:    mgl32.Vec3{2.6, 2.2, 0.06},
		})
		e.C.MeshRef.Set(ent, &glyph.MeshRef{Mesh: pane, Roughness: 0.25, Metallic: 0.2})
		e.C.Color.Set(ent, &glyph.Color{R: tints[i][0], G: tints[i][1], B: tints[i][2]})
		e.C.DoubleSided.Set(ent, &glyph.DoubleSided{})
		e.C.Translucent.Set(ent, &glyph.Translucent{Alpha: 0.45})
		e.C.Static.Set(ent, &glyph.Static{})
		g.panes = append(g.panes, ent)
	}

	e.SetDayCycleSpeed(1.0 / 120.0)
	e.SetTimeOfDay(0.32)

	g.camera = glyph.NewCamera(17)
	g.camera.Target = mgl32.Vec3{-1.5, 1.4, 1}
	g.fade = 1

	log.Println("18-translucent running. Left-drag orbits, E holds the hologram, T fades, Escape quits.")
	return nil
}

func (g *game) Update(e *glyph.Engine, dt float32) {
	in := e.Input()
	if in.KeyPressed(input.KeyEscape) {
		e.Close()
	}
	if in.KeyPressed(input.KeyT) {
		g.fading = !g.fading
	}

	g.elapsed += dt

	// Slide the ghost along the row, the way a cursor would drag it.
	if t, ok := e.C.Transform.Get(g.ghost); ok {
		span := float32(domes-1) * domeGap / 2
		t.Position[0] = domeOriginX + span*float32(math.Sin(float64(g.elapsed)*0.7))
		t.Rotation[1] += 0.4 * dt
	}

	// Emissive and Translucent compose: the ghost goes full-bright without
	// going solid. Held rather than toggled so the difference is easy to see.
	if in.KeyDown(input.KeyE) {
		if !e.C.Emissive.Has(g.ghost) {
			e.C.Emissive.Set(g.ghost, &glyph.Emissive{})
		}
	} else if e.C.Emissive.Has(g.ghost) {
		e.C.Emissive.Remove(g.ghost)
	}

	// Run the fade all the way to both ends, which is where the edges of the
	// feature are: at 0 the engine drops the draw, at 1 it uses the opaque
	// pipeline. Neither needs the game to special-case anything.
	if g.fading {
		g.fade = 0.5 + 0.5*float32(math.Sin(float64(g.elapsed)*1.4))
	} else {
		g.fade = 1
	}
	if tr, ok := e.C.Translucent.Get(g.ghost); ok {
		tr.Alpha = g.alpha * g.fade
	}
	for _, p := range g.panes {
		if tr, ok := e.C.Translucent.Get(p); ok {
			tr.Alpha = 0.45 * g.fade
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
	alpha := flag.Float64("alpha", 0.35, "opacity of the placement ghost, 0 to 1")
	shot := flag.String("screenshot", "", "write a PNG of the last frame to this path")
	flag.Parse()

	opts := []glyph.Option{
		glyph.WithTitle("GlyphEngine - 18 Translucent"),
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

	e, err := glyph.New(&game{alpha: float32(*alpha)}, opts...)
	if err != nil {
		log.Fatalf("create engine: %v", err)
	}
	defer e.Destroy()

	e.Run()
	log.Printf("rendered %d frames", e.FrameCount())
}
