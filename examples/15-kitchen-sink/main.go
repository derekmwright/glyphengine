// Command 15-kitchen-sink is everything at once: a rigged character running
// across procedural lit terrain, through instanced grass, under a moving sun,
// with an MSDF text overlay.
//
// The earlier examples each isolate one system so it can be read on its own.
// This one exists to show they compose — and to be the thing you run first,
// before deciding whether the engine is worth your time. Nothing here is new;
// every piece appears alone in an example between 04 and 10:
//
//	terrain     07-terrain    heightmap as collision and geometry at once
//	grass       08-grass      instanced scatter, frustum-culled per tile
//	character   06-skinned    GPU skinning, clip selection, cross-fade
//	text        10-text       one MSDF atlas from caption to headline size
//
// The interesting part is the wiring. A heightmap is the ground for both the
// character controller and the grass scatter, so the character walks on the
// same surface the grass grows from; the animation controller reads the
// velocity the controller produced; and the whole simulation runs on a fixed
// tick while the camera and overlay run per frame.
//
//	go run ./15-kitchen-sink               # windowed, you drive
//	go run ./15-kitchen-sink -demo         # runs a loop by itself
//	go run ./15-kitchen-sink -frames 300   # render 300 frames, then exit
//	go run ./15-kitchen-sink -seed 7       # a different island
//
// WASD moves, mouse orbits, Shift sprints, Space jumps, Escape quits.
package main

import (
	"embed"
	"flag"
	"fmt"
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

const (
	gridSize  = 129 // heightmap resolution per side
	worldSize = 130.0

	// Grass culls at renderer.GrassMaxDistance (80 units), so an island much
	// wider than that shows a ring of bare ground around a disc of grass. 130
	// units puts the far corners just past the cull distance, where fog
	// finishes the job.
	heightScale = 12.0 // peak height in world units

	// The Kenney character is a stubby 1.8 units tall, and grass blades stand
	// around 1.3 -- so at native scale it wades through the scatter like a
	// cornfield. Scaling the character rather than the grass is the cheaper
	// fix: blade size is baked into grass.vert as a constant, while this is
	// just a transform. The collider and camera scale with it so the physics
	// and framing stay consistent.
	characterScale   = 1.8
	playerHalfHeight = 0.9 * characterScale
	playerHalfWidth  = 0.4 * characterScale
)

// Ground speeds each clip was authored for. Playback is scaled by
// actual/nominal so the feet track the ground instead of skating.
const (
	walkClipSpeed   = 2.0
	sprintClipSpeed = 5.0

	idleThreshold   = 0.4
	sprintThreshold = 5.5
)

type game struct {
	camera *glyph.Camera
	player glyph.Entity
	model  *renderer.SkinnedModel
	seed   int64

	// One entity per mesh in the model, all sharing a joint buffer.
	parts []glyph.Entity

	font *renderer.Font
	text *renderer.MSDFText

	// Sampled in Update, consumed in FixedUpdate. jumpQueued latches the
	// edge-triggered jump across frames that run no tick.
	intent     glyph.MoveIntent
	jumpQueued bool

	demo      bool
	demoAngle float32

	// Clip indices resolved once at load; FindClip is a linear scan.
	clipIdle, clipWalk, clipSprint, clipJump int
}

func (g *game) Init(e *glyph.Engine) error {
	r := e.Renderer()

	// ── terrain ──
	// One heightmap is both the collision surface and the source of the
	// geometry, so what you see and what you stand on cannot drift apart.
	heights := generateHeights(gridSize, gridSize, g.seed)
	hm, err := glyph.NewHeightmap(gridSize, gridSize, worldSize, worldSize,
		-worldSize/2, -worldSize/2, heights)
	if err != nil {
		return err
	}
	e.SetTerrain(hm)

	mesh, err := e.CreateTerrainMesh(hm, &glyph.TerrainOptions{Tint: tint})
	if err != nil {
		return err
	}
	terrain := e.Spawn()
	e.C.Transform.Set(terrain, &glyph.Transform{Scale: mgl32.Vec3{1, 1, 1}})
	e.C.MeshRef.Set(terrain, &glyph.MeshRef{Mesh: mesh, Roughness: 0.95})
	e.C.Static.Set(terrain, &glyph.Static{})

	// ── grass ──
	// Scattered from the same heightmap, so it grows out of the surface the
	// character walks on. The mask clears the spawn point only, so the
	// character starts standing in grass rather than on a bald patch.
	mask := renderer.NewDensityMask(-worldSize/2, -worldSize/2, worldSize, worldSize, 1.0)
	mask.ClearCircle(0, 0, 1.5, 1.5)
	r.InitGrass(assetsFS, hm, hm.OriginX, hm.OriginZ, hm.WorldW, hm.WorldD,
		[]renderer.GrassModelSpec{
			{Path: "assets/flora/Grass_Common_Short.gltf", Weight: 40},
			{Path: "assets/flora/Grass_Common_Tall.gltf", Weight: 26},
			{Path: "assets/flora/Grass_Wispy_Short.gltf", Weight: 20},
			{Path: "assets/flora/Grass_Wispy_Tall.gltf", Weight: 14},
		}, mask)

	// ── character ──
	model, err := r.LoadGLTFSkinned(assetsFS, "assets/character.glb")
	if err != nil {
		return err
	}
	g.model = model

	joints, err := r.CreateJointBuffer()
	if err != nil {
		return err
	}

	spawnY, _ := hm.HeightAt(0, 0)
	g.player = e.Spawn()
	e.C.Transform.Set(g.player, &glyph.Transform{
		Position: mgl32.Vec3{0, spawnY + playerHalfHeight, 0},
		Scale:    mgl32.Vec3{1, 1, 1},
	})
	e.C.Collider.Set(g.player, &glyph.Collider{
		HalfExtents: mgl32.Vec3{playerHalfWidth, playerHalfHeight, playerHalfWidth},
	})
	e.C.Velocity.Set(g.player, &glyph.Velocity{})
	cc := glyph.NewCharacterController()
	e.C.CharacterController.Set(g.player, &cc)

	// A skinned entity needs the model, a joint buffer, and an AnimationState.
	// Miss any one and it renders in bind pose with no error.
	for _, m := range model.Meshes {
		// The visual meshes are their own entities, never the player entity.
		// Keeping them separate means the character can be scaled for looks
		// without that scale reaching the collider, and it is one code path
		// whether the model has one mesh or several. This one is body + head,
		// sharing a joint buffer so the two deform together.
		ent := e.Spawn()
		e.C.Transform.Set(ent, &glyph.Transform{
			Scale: mgl32.Vec3{characterScale, characterScale, characterScale},
		})
		e.C.MeshRef.Set(ent, &glyph.MeshRef{Mesh: m.Mesh, Roughness: 0.8})
		if m.Texture != nil {
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

	g.clipIdle = glyph.FindClip(model, "idle")
	g.clipWalk = glyph.FindClip(model, "walk")
	g.clipSprint = glyph.FindClipAny(model, "sprint", "run", "walk")
	g.clipJump = glyph.FindClipAny(model, "jump", "fall")

	// ── text ──
	font, err := renderer.LoadFont(r, assetsFS,
		"assets/fonts/goregular.json", "assets/fonts/goregular.png")
	if err != nil {
		return err
	}
	g.font = font
	if g.text, err = renderer.NewMSDFText(r, font); err != nil {
		return err
	}

	// ── world ──
	e.SetDayCycleSpeed(1.0 / 300.0)
	e.SetTimeOfDay(0.30) // morning sun: long shadows across the grass
	// Enough haze to hide where the grass stops, not so much that the far side
	// of the island turns into sky.
	e.SetFogDensity(0.0075)

	// Third person, pitched down. Grass blades stand about chest-high on this
	// character, so a level camera buries it in them -- looking down over its
	// shoulder keeps the silhouette clear and shows the terrain falling away
	// behind, which is the whole point of the shot.
	g.camera = glyph.NewCamera(4.4 * characterScale)
	g.camera.Pitch = 0.38
	g.camera.LookOffset = 1.1 * characterScale

	log.Println("15-kitchen-sink running. WASD moves, mouse orbits, Shift sprints, Space jumps, Escape quits.")
	return nil
}

func (g *game) Update(e *glyph.Engine, dt float32) {
	in := e.Input()
	if in.KeyPressed(input.KeyEscape) {
		e.Close()
	}

	// Mouse look consumes this frame's delta, so it belongs per-frame.
	g.camera.Update(in)

	g.intent = glyph.MoveIntent{Yaw: g.camera.Yaw}
	if g.demo {
		// Run a slow circle with no input, so the whole scene is exercised
		// without a keyboard — used for the documentation screenshot and to
		// give the smoke run something to animate.
		g.demoAngle += 0.35 * dt
		g.intent.Forward = 1
		g.intent.Sprint = true
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

		// Edge-triggered: latch it, because this frame may run no tick.
		if in.KeyPressed(input.KeySpace) {
			g.jumpQueued = true
		}
	}
}

// FixedUpdate runs on the simulation tick, so movement is identical at any
// frame rate.
func (g *game) FixedUpdate(e *glyph.Engine, dt float32) {
	e.UpdateSpatialGrid()

	intent := g.intent
	intent.Jump = g.jumpQueued
	g.jumpQueued = false
	e.MoveCharacter(g.player, intent, dt)

	g.chooseClip(e)
}

// chooseClip is the locomotion controller — game policy, not engine behaviour.
// The engine ships playback and cross-fading and leaves the decision here,
// because thresholds and priority order differ per game.
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
		// Airborne beats locomotion. Grounded is the hook for this, rather
		// than inferring it from vertical velocity.
		if cc != nil && !cc.Grounded {
			anim.PlayLoop(g.clipJump, 1.0)
			continue
		}
		switch {
		case hSpeed < idleThreshold:
			anim.PlayLoop(g.clipIdle, 1.0)
		case hSpeed < sprintThreshold:
			anim.PlayLoop(g.clipWalk, clampf(hSpeed/walkClipSpeed, 0.6, 2.0))
		default:
			anim.PlayLoop(g.clipSprint, clampf(hSpeed/sprintClipSpeed, 0.6, 2.0))
		}
	}
}

// LateUpdate runs after simulation, so the camera and the overlay both see the
// final state of the frame.
func (g *game) LateUpdate(e *glyph.Engine, dt float32) {
	if t, ok := e.InterpolatedTransform(g.player); ok {
		// The controller keeps the entity origin at the collider centre; drop
		// the visual mesh to the character's feet.
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

	g.drawOverlay(e)
}

func (g *game) drawOverlay(e *glyph.Engine) {
	w, h := e.Renderer().Extent()
	sw, sh := float32(w), float32(h)

	var speed float32
	if vel, ok := e.C.Velocity.Get(g.player); ok {
		speed = mgl32.Vec2{vel.Vec[0], vel.Vec[2]}.Len()
	}
	var height float32
	if t, ok := e.C.Transform.Get(g.player); ok {
		height = t.Position[1] - playerHalfHeight
	}

	lines := []renderer.TextLine{
		{Text: "GlyphEngine", X: 36, Y: 30, Scale: 44, Color: [3]float32{1, 1, 1}},
		{Text: "terrain + grass + skinning + text", X: 36, Y: 84, Scale: 20,
			Color: [3]float32{0.78, 0.86, 1.0}},

		{Text: fmt.Sprintf("%.0f fps", e.FPS()), X: -36, Y: 30, Scale: 20,
			Color: [3]float32{0.6, 1.0, 0.6}},
		{Text: fmt.Sprintf("%.1f m/s", speed), X: -36, Y: 56, Scale: 20,
			Color: [3]float32{0.85, 0.85, 0.85}},
		{Text: fmt.Sprintf("%.1f m", height), X: -36, Y: 82, Scale: 20,
			Color: [3]float32{0.85, 0.85, 0.85}},

		{Text: "WASD move   mouse orbit   Shift sprint   Space jump   Esc quit",
			X: 36, Y: sh - 46, Scale: 18, Color: [3]float32{0.88, 0.88, 0.88}},
	}
	g.text.SetText(e.Renderer(), lines, sw, sh)
	e.SetMSDFOverlays([]renderer.RenderObject{g.text.RenderObject(sw, sh, 48)})
}

func (g *game) Shutdown(e *glyph.Engine) {
	if g.text != nil {
		g.text.Destroy(e.Renderer())
	}
}

// tint colors terrain by height and slope: rock on anything steep, sand at the
// waterline, grass in between, snow on the peaks.
func tint(height float32, normal [3]float32) [3]float32 {
	slope := 1 - normal[1] // 0 = flat, 1 = vertical
	if slope > 0.45 {
		return [3]float32{0.42, 0.40, 0.38}
	}
	switch {
	case height < 0.5:
		return [3]float32{0.76, 0.70, 0.50}
	case height > heightScale*0.74:
		return [3]float32{0.92, 0.93, 0.95}
	default:
		g := 0.40 + height/heightScale*0.12
		return [3]float32{0.22, g, 0.20}
	}
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

// ─────────────────────── procedural heightmap ───────────────────────
//
// Value-noise fBm with an island falloff, deterministic from the seed. This
// lives in the example rather than the engine on purpose: terrain generation
// is a game concern, and every game wants a different one.

func generateHeights(gridW, gridH int, seed int64) []float32 {
	heights := make([]float32, gridW*gridH)
	for iz := 0; iz < gridH; iz++ {
		for ix := 0; ix < gridW; ix++ {
			u := float64(ix) / float64(gridW-1)
			v := float64(iz) / float64(gridH-1)

			h := fbm(u*4, v*4, seed)

			// Radial falloff makes it an island, which also keeps the player
			// away from the heightmap boundary where HeightAt stops returning
			// ground.
			dx, dz := u-0.5, v-0.5
			d := math.Sqrt(dx*dx+dz*dz) * 2
			falloff := 1 - smoothstep(clamp((d-0.35)/0.5, 0, 1))

			heights[iz*gridW+ix] = float32(h * falloff * heightScale)
		}
	}
	return heights
}

func hash(x, y int, seed int64) float64 {
	n := int64(x)*374761393 + int64(y)*668265263 + seed*1442695040888963407
	n = (n ^ (n >> 13)) * 1274126177
	n = n ^ (n >> 16)
	return float64(n&0x7fffffff) / float64(0x7fffffff)
}

func smoothstep(t float64) float64 { return t * t * (3 - 2*t) }

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func valueNoise(x, y float64, seed int64) float64 {
	xi, yi := math.Floor(x), math.Floor(y)
	xf, yf := x-xi, y-yi
	ix, iy := int(xi), int(yi)

	v00 := hash(ix, iy, seed)
	v10 := hash(ix+1, iy, seed)
	v01 := hash(ix, iy+1, seed)
	v11 := hash(ix+1, iy+1, seed)

	sx, sy := smoothstep(xf), smoothstep(yf)
	top := v00 + (v10-v00)*sx
	bot := v01 + (v11-v01)*sx
	return top + (bot-top)*sy
}

func fbm(x, y float64, seed int64) float64 {
	const octaves = 5
	amp, freq, sum, norm := 1.0, 1.0, 0.0, 0.0
	for i := 0; i < octaves; i++ {
		sum += valueNoise(x*freq, y*freq, seed) * amp
		norm += amp
		amp *= 0.5
		freq *= 2
	}
	return sum / norm
}

func main() {
	width := flag.Int("width", 1280, "window width")
	height := flag.Int("height", 720, "window height")
	fullscreen := flag.Bool("fullscreen", false, "run fullscreen on the primary monitor")
	frames := flag.Int("frames", 0, "render N frames then exit (0 = run until closed)")
	seed := flag.Int64("seed", 4, "terrain generation seed")
	shot := flag.String("screenshot", "", "write a PNG of the last frame to this path")
	demo := flag.Bool("demo", false, "run a loop automatically, no input needed")
	gputime := flag.Bool("gputime", false, "print mean per-pass GPU timings on exit")
	flag.Parse()

	opts := []glyph.Option{
		glyph.WithTitle("GlyphEngine - 15 Kitchen Sink"),
		glyph.WithWindowSize(*width, *height),
		glyph.WithMSAA(4),
		// The terrain spans 160 units; push the far plane out to match.
		glyph.WithProjection(50, 0.1, 700),
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

	e, err := glyph.New(&game{seed: *seed, demo: *demo}, opts...)
	if err != nil {
		log.Fatalf("create engine: %v", err)
	}
	defer e.Destroy()

	e.Run()
	if *gputime {
		e.LogGPUTimings()
	}
	log.Printf("rendered %d frames", e.FrameCount())
}
