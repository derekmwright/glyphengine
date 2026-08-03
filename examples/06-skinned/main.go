// Command 06-skinned loads a rigged glTF character and drives its animation
// from movement — GPU skinning, clip selection, and cross-fading.
//
// This exercises the whole skeletal path: LoadGLTFSkinned reads the skin,
// joint hierarchy, and inverse bind matrices; TickAnimations samples the
// active clip once per rendered frame, blends when the clip changes, and
// uploads the joint matrices; the skinned pipeline transforms vertices on the
// GPU.
//
// It also shows the locomotion controller the engine deliberately does not
// ship. Choosing a clip is game policy — it depends on your animation set,
// your speed thresholds, and your game's states — so it lives here, in a
// game-side system, rather than in the engine. The engine gives you playback
// (PlayLoop, PlayOnce, cross-fade) and stays out of the decision.
//
//	go run ./06-skinned              # windowed
//	go run ./06-skinned -frames 120  # render 120 frames, then exit
//
// Left-drag orbits, scroll zooms, WASD moves, Shift sprints, Space jumps.
// Pass -demo to walk a circle automatically, which is how the documentation
// screenshot is taken.
package main

import (
	"embed"
	"flag"
	"log"
	"math"
	"runtime"

	"github.com/go-gl/mathgl/mgl32"

	glyph "github.com/derekmwright/glyphengine"
	"github.com/derekmwright/glyphengine/input"
	"github.com/derekmwright/glyphengine/renderer"
)

//go:embed assets
var assetsFS embed.FS

func init() {
	// GLFW must be called from the thread that initialized it.
	runtime.LockOSThread()
}

const playerHalfHeight = 0.9

// Ground speeds each clip was authored to look right at. Playback is scaled by
// actual/nominal so the feet track the ground instead of sliding — the usual
// reason a character looks like it is skating.
const (
	walkClipSpeed   = 2.0
	sprintClipSpeed = 5.0

	idleThreshold   = 0.4 // below this, stand still
	sprintThreshold = 5.5 // above this, use the sprint gait
)

type game struct {
	camera *glyph.Camera
	player glyph.Entity
	model  *renderer.SkinnedModel

	intent     glyph.MoveIntent
	jumpQueued bool

	// One entity per mesh in the model, all sharing a joint buffer.
	parts []glyph.Entity

	demo      bool
	plain     bool
	demoAngle float32

	// Clip indices resolved once at load. FindClip is a case-insensitive
	// linear scan, so looking them up every frame would be wasteful.
	clipIdle, clipWalk, clipSprint, clipJump int
}

func (g *game) Init(e *glyph.Engine) error {
	r := e.Renderer()

	ground, err := r.CreatePlane(60, 60)
	if err != nil {
		return err
	}
	groundEnt := e.Spawn()
	e.C.Transform.Set(groundEnt, &glyph.Transform{Scale: mgl32.Vec3{1, 1, 1}})
	e.C.MeshRef.Set(groundEnt, &glyph.MeshRef{Mesh: ground, Roughness: 0.95})
	e.C.Color.Set(groundEnt, &glyph.Color{R: 0.31, G: 0.36, B: 0.29})
	e.C.Collider.Set(groundEnt, &glyph.Collider{HalfExtents: mgl32.Vec3{30, 0.05, 30}})
	e.C.Static.Set(groundEnt, &glyph.Static{})
	// The ground receives shadows but casting from a flat plane adds nothing.
	e.C.NoCastShadow.Set(groundEnt, &glyph.NoCastShadow{})

	// Pillars, so the character has something to cast shadows against and a
	// sense of scale while moving.
	pillar, err := r.CreateCube(1.0)
	if err != nil {
		return err
	}
	scale := mgl32.Vec3{1.0, 5, 1.0}
	for i := 0; i < 6; i++ {
		a := float64(i) / 6 * 2 * math.Pi
		ent := e.Spawn()
		e.C.Transform.Set(ent, &glyph.Transform{
			Position: mgl32.Vec3{float32(math.Cos(a)) * 9, scale.Y() / 2, float32(math.Sin(a)) * 9},
			Scale:    scale,
		})
		e.C.MeshRef.Set(ent, &glyph.MeshRef{Mesh: pillar, Roughness: 0.85})
		e.C.Color.Set(ent, &glyph.Color{R: 0.66, G: 0.64, B: 0.60})
		e.C.Collider.Set(ent, &glyph.Collider{HalfExtents: mgl32.Vec3{0.5, 0.5, 0.5}})
		e.C.Static.Set(ent, &glyph.Static{})
	}

	// ── the character ──
	//
	// A woven normal map, generated rather than shipped, so the example carries
	// no extra asset. Its only job is to prove the skinned path reaches the
	// material pipeline: without it the character lights as one smooth surface
	// no matter how it is posed.
	var cloth *renderer.Texture
	if !g.plain {
		cloth, err = r.CreateDataTexture(buildClothNormal(), clothSize, clothSize)
		if err != nil {
			return err
		}
	}

	model, err := r.LoadGLTFSkinned(assetsFS, "assets/character.glb")
	if err != nil {
		return err
	}
	g.model = model

	joints, err := r.CreateJointBuffer()
	if err != nil {
		return err
	}

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

	// A skinned entity needs all three: the model and joint buffer to draw
	// with, and an AnimationState for TickAnimations to advance. Miss any one
	// and the character renders in bind pose with no error.
	for _, m := range model.Meshes {
		ent := g.player
		if len(model.Meshes) > 1 {
			// Multi-mesh models (this one is body + head) need one entity per
			// mesh, each sharing the same joint buffer so they deform together.
			ent = e.Spawn()
			e.C.Transform.Set(ent, &glyph.Transform{Scale: mgl32.Vec3{1, 1, 1}})
		}
		e.C.MeshRef.Set(ent, &glyph.MeshRef{Mesh: m.Mesh, Roughness: 0.8})
		switch {
		case m.Material != nil:
			// The glTF carried its own maps.
			e.C.MaterialRef.Set(ent, &glyph.MaterialRef{PBR: m.Material})
		case m.Texture != nil && cloth != nil:
			// It did not, so give it a generated one. A Material works on a
			// skinned mesh exactly as on a static one: it takes set 0, where the
			// plain texture used to sit, and the joints stay at set 1.
			mat, err := r.CreateMaterial(renderer.MaterialOptions{
				Albedo:      m.Texture,
				Normal:      cloth,
				NormalScale: 0.8,
			})
			if err != nil {
				return err
			}
			e.C.MaterialRef.Set(ent, &glyph.MaterialRef{PBR: mat})
		case m.Texture != nil:
			e.C.MaterialRef.Set(ent, &glyph.MaterialRef{Texture: m.Texture})
		}
		e.C.SkeletonRef.Set(ent, &glyph.SkeletonRef{
			Model:       model,
			JointBuffer: joints,
			Skinned:     true,
		})
		e.C.AnimationState.Set(ent, &glyph.AnimationState{})
		g.parts = append(g.parts, ent)
	}

	// Resolve clip indices once. -1 is safe to pass to PlayLoop, so a model
	// missing a clip animates nothing rather than panicking.
	g.clipIdle = glyph.FindClip(model, "idle")
	g.clipWalk = glyph.FindClip(model, "walk")
	g.clipSprint = glyph.FindClipAny(model, "sprint", "run", "walk")
	g.clipJump = glyph.FindClipAny(model, "jump", "fall")
	log.Printf("clips: idle=%d walk=%d sprint=%d jump=%d (of %d)",
		g.clipIdle, g.clipWalk, g.clipSprint, g.clipJump, len(model.Animations))

	e.SetDayCycleSpeed(1.0 / 300.0)
	e.SetTimeOfDay(0.30)

	// Third person, deliberately: this example exists to show the character
	// deform, and a first-person camera sits inside it.
	g.camera = glyph.NewCamera(6)
	g.camera.Pitch = 0.25
	g.camera.LookOffset = 1.0

	log.Println("06-skinned running. WASD moves, Shift sprints, Space jumps, Escape releases the cursor.")
	return nil
}

func (g *game) Update(e *glyph.Engine, dt float32) {
	in := e.Input()

	if in.KeyPressed(input.KeyEscape) {
		e.Close()
	}

	g.camera.Update(in)

	g.intent = glyph.MoveIntent{Yaw: g.camera.Yaw}
	if g.demo {
		// Walk a slow circle with no input, so the animation is visible
		// without a keyboard — used for the documentation screenshots and to
		// exercise the clip blend in a smoke run.
		g.demoAngle += 0.6 * dt
		g.intent.Forward = 1
		g.intent.Yaw = g.demoAngle
		g.camera.Yaw = g.demoAngle + math.Pi
	} else {
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
		if in.KeyPressed(input.KeySpace) {
			g.jumpQueued = true
		}
	}
}

func (g *game) FixedUpdate(e *glyph.Engine, dt float32) {
	e.UpdateSpatialGrid()

	intent := g.intent
	intent.Jump = g.jumpQueued
	g.jumpQueued = false
	e.MoveCharacter(g.player, intent, dt)

	g.chooseClip(e)
}

// chooseClip is the locomotion controller — game policy, not engine behaviour.
// Priority order and speed thresholds are exactly the sort of thing that
// differs per game, which is why the engine ships playback and leaves this to
// you.
func (g *game) chooseClip(e *glyph.Engine) {
	vel, ok := e.C.Velocity.Get(g.player)
	if !ok {
		return
	}
	cc, _ := e.C.CharacterController.Get(g.player)

	hSpeed := mgl32.Vec2{vel.Vec[0], vel.Vec[2]}.Len()

	for _, ent := range g.parts {
		anim, ok := e.C.AnimationState.Get(ent)
		if !ok {
			continue
		}

		// Airborne wins over locomotion. CharacterController.Grounded is the
		// hook for this — no need to infer it from vertical velocity.
		if cc != nil && !cc.Grounded {
			anim.PlayLoop(g.clipJump, 1.0)
			continue
		}

		switch {
		case hSpeed < idleThreshold:
			anim.PlayLoop(g.clipIdle, 1.0)
		case hSpeed < sprintThreshold:
			anim.PlayLoop(g.clipWalk, clamp(hSpeed/walkClipSpeed, 0.6, 2.0))
		default:
			anim.PlayLoop(g.clipSprint, clamp(hSpeed/sprintClipSpeed, 0.6, 2.0))
		}
	}
}

// LateUpdate places the camera after simulation, at the interpolated position
// so the view is as smooth as the world.
func (g *game) LateUpdate(e *glyph.Engine, dt float32) {
	if t, ok := e.InterpolatedTransform(g.player); ok {
		// The controller keeps the entity origin at the collider centre, so
		// drop the visual mesh to the character's feet.
		feet := t
		feet.Position[1] -= playerHalfHeight

		for _, ent := range g.parts {
			if pt, ok := e.C.Transform.Get(ent); ok {
				pt.Position = feet.Position
				pt.Rotation = t.Rotation
			}
		}

		g.camera.Target = t.Position
	}
	g.camera.ResolveCollision(e.Scene, g.player, dt)
	e.SetCamera(g.camera.ViewVectors())
}

func clamp(v, lo, hi float32) float32 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func main() {
	width := flag.Int("width", 1280, "window width")
	height := flag.Int("height", 720, "window height")
	fullscreen := flag.Bool("fullscreen", false, "run fullscreen on the primary monitor")
	frames := flag.Int("frames", 0, "render N frames then exit (0 = run until closed)")
	shot := flag.String("screenshot", "", "write a PNG of the last frame to this path")
	demo := flag.Bool("demo", false, "walk a circle automatically, no input needed")
	plain := flag.Bool("plain", false, "skip the generated normal map, for comparison")
	flag.Parse()

	opts := []glyph.Option{
		glyph.WithTitle("GlyphEngine - 06 Skinned"),
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

	e, err := glyph.New(&game{demo: *demo, plain: *plain}, opts...)
	if err != nil {
		log.Fatalf("create engine: %v", err)
	}
	defer e.Destroy()

	e.Run()
	log.Printf("rendered %d frames", e.FrameCount())
}

// clothSize is the edge length of the generated normal map.
const clothSize = 256

// buildClothNormal generates a tangent-space normal map of a plain weave.
//
// Generated rather than shipped so the example stays asset-light, and woven
// rather than something louder because the point is to be legible on a moving
// character: a pattern with a single dominant direction turns into a shimmering
// mess once the surface deforms, while a weave keeps its two axes balanced from
// any angle.
//
// It has to go through CreateDataTexture. Loaded as sRGB colour, 128 decodes to
// 0.216 rather than 0.502, so every normal arrives tilted and the character
// lights subtly wrong with nothing in the log to say so.
func buildClothNormal() []byte {
	pix := make([]byte, clothSize*clothSize*4)
	const threads = 24.0
	for y := 0; y < clothSize; y++ {
		for x := 0; x < clothSize; x++ {
			u := float64(x) / clothSize * threads * 2 * math.Pi
			v := float64(y) / clothSize * threads * 2 * math.Pi

			// Two out-of-phase ripples. The height field is a sum of sines, so
			// its gradient is a sum of cosines -- no finite differencing needed.
			nx := -math.Cos(u) * 0.5
			ny := -math.Cos(v) * 0.5
			nz := 1.0

			l := math.Sqrt(nx*nx + ny*ny + nz*nz)
			i := (y*clothSize + x) * 4
			pix[i+0] = byte((nx/l*0.5 + 0.5) * 255)
			pix[i+1] = byte((ny/l*0.5 + 0.5) * 255)
			pix[i+2] = byte((nz/l*0.5 + 0.5) * 255)
			pix[i+3] = 255
		}
	}
	return pix
}
