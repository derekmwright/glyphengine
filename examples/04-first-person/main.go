// Command 04-first-person is a walkable scene: a first-person camera, a
// character controller, and pillars you cannot walk through.
//
// It shows the input-to-movement path a game actually uses, and the three
// callbacks it is split across:
//
//	Update      - per frame: read input, latch edges, mouse look
//	FixedUpdate - per tick:  e.MoveCharacter(player, intent, dt)
//	LateUpdate  - per frame: camera follows the final position
//
// MoveIntent is deliberately not a keyboard struct. The same value can come
// from a gamepad, an AI, or a decoded network packet, which is what lets the
// same controller run on a server.
//
// Movement is in FixedUpdate rather than Update so it is deterministic. Run it
// on the frame delta instead and jump height changes with frame rate — about
// 5% higher at 30fps than at 300fps — which is both a fairness problem and
// fatal to any attempt to have a server agree with a client.
//
//	go run ./04-first-person              # windowed
//	go run ./04-first-person -frames 120  # render 120 frames, then exit
//
// WASD moves, mouse looks, Shift runs, Space jumps, Escape releases the
// cursor (press again to quit).
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

// playerHalfHeight is half the capsule height. The controller snaps the
// entity's origin to ground + halfHeight, so this is also the eye offset from
// the ground when standing.
const playerHalfHeight = 0.9

type game struct {
	camera *glyph.FPCamera
	player ecs.Entity

	// intent is sampled in Update and consumed in FixedUpdate. jumpQueued
	// latches the edge-triggered jump so it survives a frame that runs no
	// tick at all.
	intent     glyph.MoveIntent
	jumpQueued bool
}

func (g *game) Init(e *glyph.Engine) error {
	r := e.Renderer()

	// Ground.
	ground, err := r.CreatePlane(80, 80)
	if err != nil {
		return err
	}
	groundEnt := e.Spawn()
	e.C.Transform.Set(groundEnt, &glyph.Transform{Scale: mgl32.Vec3{1, 1, 1}})
	e.C.MeshRef.Set(groundEnt, &glyph.MeshRef{Mesh: ground, Roughness: 0.95})
	e.C.Color.Set(groundEnt, &glyph.Color{R: 0.30, G: 0.36, B: 0.28})
	e.C.Collider.Set(groundEnt, &glyph.Collider{HalfExtents: mgl32.Vec3{40, 0.05, 40}})
	e.C.Static.Set(groundEnt, &glyph.Static{})

	// A ring of pillars to bump into.
	//
	// A Collider is always centered on the entity's Transform position and its
	// half-extents are multiplied by Transform.Scale — there is no local
	// offset. So the visual mesh has to be origin-centered too, which is why
	// this uses a scaled cube rather than CreateCylinder, whose base sits at
	// Y=0 and whose top is at Y=height.
	pillar, err := r.CreateCube(1.0)
	if err != nil {
		return err
	}
	const pillarCount = 8
	pillarScale := mgl32.Vec3{1.2, 4, 1.2}
	for i := 0; i < pillarCount; i++ {
		angle := float64(i) / pillarCount * 2 * math.Pi
		x := float32(math.Cos(angle)) * 10
		z := float32(math.Sin(angle)) * 10

		ent := e.Spawn()
		e.C.Transform.Set(ent, &glyph.Transform{
			Position: mgl32.Vec3{x, pillarScale.Y() / 2, z},
			Scale:    pillarScale,
		})
		e.C.MeshRef.Set(ent, &glyph.MeshRef{Mesh: pillar, Roughness: 0.8})
		e.C.Color.Set(ent, &glyph.Color{R: 0.72, G: 0.70, B: 0.64})
		e.C.Collider.Set(ent, &glyph.Collider{HalfExtents: mgl32.Vec3{0.5, 0.5, 0.5}})
		e.C.Static.Set(ent, &glyph.Static{})
	}

	// The player. A character controller entity needs Transform, Collider,
	// Velocity, and CharacterController — IntegrateBodies skips it because the
	// controller does its own gravity and collision.
	g.player = e.Spawn()
	e.C.Transform.Set(g.player, &glyph.Transform{
		Position: mgl32.Vec3{0, playerHalfHeight, 0},
		Scale:    mgl32.Vec3{1, 1, 1},
	})
	e.C.Collider.Set(g.player, &glyph.Collider{
		HalfExtents: mgl32.Vec3{0.4, playerHalfHeight, 0.4},
	})
	e.C.Velocity.Set(g.player, &glyph.Velocity{})
	cc := glyph.NewCharacterController()
	e.C.CharacterController.Set(g.player, &cc)

	e.RebuildStatics()

	e.SetDayCycleSpeed(1.0 / 240.0)
	e.SetTimeOfDay(0.32)

	g.camera = glyph.NewFPCamera()
	// The controller keeps the entity origin at the collider center, so the
	// eye sits a little above that rather than a full body height.
	g.camera.EyeHeight = 0.7

	// First-person games own the cursor.
	e.Input().SetCursorLocked(true)

	log.Println("04-first-person running. WASD moves, Shift runs, Space jumps, Escape releases the cursor.")
	return nil
}

func (g *game) Update(e *glyph.Engine, dt float32) {
	in := e.Input()

	// Escape releases the cursor first, then quits — otherwise a locked cursor
	// makes the window impossible to leave without killing the process.
	if in.KeyPressed(input.KeyEscape) {
		if in.CursorLocked() {
			in.SetCursorLocked(false)
		} else {
			e.Close()
		}
	}
	// Clicking in the window takes the cursor back.
	if in.MousePressed(input.MouseButtonLeft) && !in.CursorLocked() {
		in.SetCursorLocked(true)
	}

	// Mouse look is per-frame: it consumes this frame's mouse delta, so it has
	// to run exactly once per frame.
	g.camera.Update(in)

	// Sample movement input into an intent that FixedUpdate will consume.
	// Held keys can simply be overwritten each frame — whatever the last
	// sample said is current.
	g.intent = glyph.MoveIntent{Yaw: g.camera.Yaw}
	if in.CursorLocked() {
		if in.KeyDown(input.KeyW) {
			g.intent.Forward++
		}
		if in.KeyDown(input.KeyS) {
			g.intent.Forward--
		}
		if in.KeyDown(input.KeyD) {
			g.intent.Right++
		}
		if in.KeyDown(input.KeyA) {
			g.intent.Right--
		}
		g.intent.Sprint = in.KeyDown(input.KeyLeftShift)

		// Jump is edge-triggered, so it must be LATCHED rather than sampled.
		// KeyPressed is true for exactly one frame, and a frame may run zero
		// ticks — reading it inside FixedUpdate would silently drop the jump.
		if in.KeyPressed(input.KeySpace) {
			g.jumpQueued = true
		}
	}
}

// FixedUpdate runs on the fixed simulation tick. Movement lives here so it is
// deterministic: the same intent and the same tick delta always produce the
// same result, on this machine, on a 144Hz machine, or on a server.
func (g *game) FixedUpdate(e *glyph.Engine, dt float32) {
	// Broad-phase index for the moving entities. The pillars are Static and
	// already indexed by RebuildStatics.
	e.UpdateSpatialGrid()

	intent := g.intent
	intent.Jump = g.jumpQueued
	g.jumpQueued = false // consume exactly once, however many ticks run

	e.MoveCharacter(g.player, intent, dt)
}

// LateUpdate runs after every tick, so the camera follows the position the
// player actually ended this frame at. Following in Update would render the
// camera one tick behind.
//
// It follows the INTERPOLATED transform, not the raw one. The world is drawn
// blended between ticks; a camera sitting at the last simulated position would
// lurch at 60Hz while everything around it moved smoothly — which in first
// person is the most noticeable place to get this wrong.
func (g *game) LateUpdate(e *glyph.Engine, _ float32) {
	if t, ok := e.InterpolatedTransform(g.player); ok {
		g.camera.Follow(&t)
	}
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
		glyph.WithTitle("GlyphEngine - 04 First Person"),
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
