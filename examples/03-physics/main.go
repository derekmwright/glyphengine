// Command 03-physics shows the engine's physics and spatial queries: colliders,
// gravity integration, ground snapping, and mouse picking by raycast.
//
// Six crates start in the air and fall. IntegrateBodies — which Scene.Tick runs
// for you — applies gravity, finds the ground, and integrates velocity, so the
// only thing this example does per tick is rebuild the spatial grid.
//
// Click a crate to toggle its highlight. Picking is a raycast from the camera
// through the mouse position, which is the same call a game uses for "what am I
// looking at": Engine.PickEntity.
//
// What this is not: a rigid-body solver. IntegrateBodies resolves bodies
// against the ground and the terrain, not against each other, so the crates
// are spawned over distinct footprints rather than stacked. Body-to-body
// response is the character controller's job (see 04-first-person) or a
// game-side system's.
//
//	go run ./03-physics              # windowed
//	go run ./03-physics -frames 120  # render 120 frames, then exit (CI smoke test)
//
// Left-drag orbits, scroll zooms, left-click picks, R resets, Escape quits.
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

// crateStart holds each crate's spawn position so R can drop them again. The
// footprints are kept apart because nothing resolves crate-against-crate.
var crateStart = []mgl32.Vec3{
	{-4.5, 6, -3.0},
	{-1.5, 9, 3.0},
	{1.5, 7, -3.0},
	{4.5, 12, 3.0},
	{-4.5, 14, 3.0},
	{4.5, 17, -3.0},
}

type game struct {
	camera *glyph.Camera
	crates []ecs.Entity
}

func (g *game) Init(e *glyph.Engine) error {
	r := e.Renderer()

	// Ground. It is a Static collider, so it goes in the static grid and is
	// never re-indexed; the crates land on it.
	ground, err := r.CreatePlane(60, 60)
	if err != nil {
		return err
	}
	groundEnt := e.Spawn()
	e.C.Transform.Set(groundEnt, &glyph.Transform{Scale: mgl32.Vec3{1, 1, 1}})
	e.C.MeshRef.Set(groundEnt, &glyph.MeshRef{Mesh: ground, Roughness: 0.9})
	e.C.Color.Set(groundEnt, &glyph.Color{R: 0.32, G: 0.38, B: 0.30})
	// A very flat, very wide box: the collider is what the raycast hits, and
	// the plane mesh itself has no thickness.
	e.C.Collider.Set(groundEnt, &glyph.Collider{HalfExtents: mgl32.Vec3{30, 0.05, 30}})
	e.C.Static.Set(groundEnt, &glyph.Static{})

	// Falling crates.
	crate, err := r.CreateCube(1.0)
	if err != nil {
		return err
	}
	for i, pos := range crateStart {
		ent := e.Spawn()
		e.C.Transform.Set(ent, &glyph.Transform{Position: pos, Scale: mgl32.Vec3{1, 1, 1}})
		e.C.MeshRef.Set(ent, &glyph.MeshRef{Mesh: crate, Roughness: 0.6})
		e.C.Color.Set(ent, &glyph.Color{R: 0.75, G: 0.55 - float32(i)*0.05, B: 0.30})
		// Half-extents match the 1-unit cube: it spans -0.5..0.5 on each axis.
		e.C.Collider.Set(ent, &glyph.Collider{HalfExtents: mgl32.Vec3{0.5, 0.5, 0.5}})
		e.C.Velocity.Set(ent, &glyph.Velocity{})
		g.crates = append(g.crates, ent)
	}

	// Index the static geometry once. Moving entities are re-indexed per tick
	// in Update.
	e.RebuildStatics()

	e.SetDayCycleSpeed(1.0 / 180.0)
	e.SetTimeOfDay(0.40)

	g.camera = glyph.NewCamera(14)
	g.camera.Target = mgl32.Vec3{0, 1.5, 0}
	g.camera.Pitch = 0.35

	log.Println("03-physics running. Left-click picks a crate, R resets, Escape quits.")
	return nil
}

func (g *game) Update(e *glyph.Engine, dt float32) {
	in := e.Input()
	if in.KeyPressed(input.KeyEscape) {
		e.Close()
	}

	// Rebuild the broad-phase index for moving entities. Without this,
	// OverlapAABB and Raycast fall back to a linear scan of every collider —
	// correct, but O(n) per query.
	e.UpdateSpatialGrid()

	if in.KeyPressed(input.KeyR) {
		g.reset(e)
	}

	// Pick on a click that was not a camera drag.
	if g.camera.WasClick(in) {
		mx, my := in.MousePos()
		if hit, ok := e.PickEntity(mx, my, 100, 0); ok {
			if e.C.Highlighted.Has(hit.Entity) {
				e.C.Highlighted.Remove(hit.Entity)
			} else {
				e.C.Highlighted.Set(hit.Entity, &glyph.Highlighted{})
			}
			log.Printf("picked entity %d at %.2f units", hit.Entity, hit.T)
		}
	}

	g.camera.Update(in)
	g.camera.ResolveCollision(e.Scene, 0, dt)
	e.SetCamera(g.camera.ViewVectors())
}

// reset drops the crates back to their spawn positions with zero velocity.
func (g *game) reset(e *glyph.Engine) {
	for i, ent := range g.crates {
		if t, ok := e.C.Transform.Get(ent); ok {
			t.Position = crateStart[i]
			t.Rotation = mgl32.Vec3{}
		}
		if v, ok := e.C.Velocity.Get(ent); ok {
			v.Vec = mgl32.Vec3{}
		}
	}
	log.Println("reset")
}

func main() {
	width := flag.Int("width", 1280, "window width")
	height := flag.Int("height", 720, "window height")
	fullscreen := flag.Bool("fullscreen", false, "run fullscreen on the primary monitor")
	frames := flag.Int("frames", 0, "render N frames then exit (0 = run until closed)")
	shot := flag.String("screenshot", "", "write a PNG of the last frame to this path")
	flag.Parse()

	opts := []glyph.Option{
		glyph.WithTitle("GlyphEngine - 03 Physics"),
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
