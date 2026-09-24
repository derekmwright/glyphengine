// Command 07-terrain generates a heightmap, turns it into a mesh, and lets you
// walk over it in first person.
//
// A Heightmap is two things at once:
//
//   - collision — Scene.SetTerrain makes it the ground. Height lookups are
//     O(1) bilinear samples, so the character controller snaps to the surface
//     without raycasting against terrain triangles.
//   - geometry — TerrainMesh builds vertices and 32-bit indices from the same
//     grid, with normals from the heightmap's own central differences.
//
// Because both come from one grid, what you see and what you stand on cannot
// drift apart.
//
// The terrain is generated at startup from value-noise fBm, so this example
// loads nothing from disk. The noise lives here rather than in the engine:
// terrain generation is a game concern, and every game wants a different one.
//
//	go run ./07-terrain              # windowed
//	go run ./07-terrain -frames 120  # render 120 frames, then exit
//	go run ./07-terrain -seed 7      # a different island
//	go run ./07-terrain -heightmap assets/blender_terrain.heightmap
//	                                  # load a .heightmap from disk instead
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
	"github.com/derekmwright/glyphengine/examples/internal/terrainfield"
	"github.com/derekmwright/glyphengine/input"
)

func init() {
	// GLFW must be called from the thread that initialized it.
	runtime.LockOSThread()
}

const (
	gridSize    = 129   // heightmap resolution per side
	worldSize   = 200.0 // world units per side
	heightScale = 14.0  // peak height in world units

	playerHalfHeight = 0.9
)

type game struct {
	camera        *glyph.FPCamera
	player        ecs.Entity
	seed          int64
	heightmapPath string

	// Sampled in Update, consumed in FixedUpdate. jumpQueued latches the
	// edge-triggered jump across frames that run no tick.
	intent     glyph.MoveIntent
	jumpQueued bool
}

func (g *game) Init(e *glyph.Engine) error {
	// ── terrain ──
	//
	// heightmapPath is empty on every default run (including every existing
	// caller of this example): the procedurally-generated branch below is
	// byte-for-byte what ran before -heightmap existed, unreached and
	// untouched when the flag is not passed. -heightmap loads a .heightmap
	// from disk instead -- cmd/heightmapconv's own output, or anything else
	// LoadHeightmap accepts -- so a converted Blender terrain can be looked
	// at without writing a second example.
	hm, err := terrainfield.Load(g.heightmapPath, g.seed)
	if err != nil {
		return err
	}

	e.SetTerrain(hm)

	mesh, err := e.CreateTerrainMesh(hm, &glyph.TerrainOptions{Tint: tint})
	if err != nil {
		return err
	}
	terrainEnt := e.Spawn()
	e.C.Transform.Set(terrainEnt, &glyph.Transform{Scale: mgl32.Vec3{1, 1, 1}})
	e.C.MeshRef.Set(terrainEnt, &glyph.MeshRef{Mesh: mesh, Roughness: 0.95})

	// ── player ──
	//
	// The procedural terrain is always centered on the world origin, so
	// (0,0) is always on it. A loaded heightmap need not be -- the Blender
	// terrain fixture cmd/heightmapconv converts for docs/agents/terrain-heightmap.md
	// sits at world X~190-200 -- so -heightmap spawns inside the loaded
	// grid's own bounds instead of the hardcoded origin. 20% in from the
	// flat (minX, minZ) corner rather than dead centre: that fixture's peak
	// is tall enough relative to its 10x10 footprint that spawning at the
	// centre puts the camera nose-against its slope: see
	// docs/agents/terrain-heightmap.md's "Through the example" section.
	spawnX, spawnZ := float32(0), float32(0)
	if g.heightmapPath != "" {
		minX, minZ, maxX, maxZ := hm.Bounds()
		spawnX, spawnZ = minX+0.2*(maxX-minX), minZ+0.2*(maxZ-minZ)
	}
	spawnY, _ := hm.HeightAt(spawnX, spawnZ)

	g.player = e.Spawn()
	e.C.Transform.Set(g.player, &glyph.Transform{
		Position: mgl32.Vec3{spawnX, spawnY + playerHalfHeight, spawnZ},
		Scale:    mgl32.Vec3{1, 1, 1},
	})
	e.C.Collider.Set(g.player, &glyph.Collider{
		HalfExtents: mgl32.Vec3{0.4, playerHalfHeight, 0.4},
	})
	e.C.Velocity.Set(g.player, &glyph.Velocity{})
	cc := glyph.NewCharacterController()
	e.C.CharacterController.Set(g.player, &cc)

	e.SetDayCycleSpeed(1.0 / 300.0)
	e.SetTimeOfDay(0.30)
	// Terrain wants to fade into the sky rather than end in a hard edge.
	// 0.006 is tuned for the procedural terrain's 200-unit world; a loaded
	// heightmap can be any size (the Blender fixture is 10 units), so scale
	// density inversely with WorldW rather than fogging a small terrain
	// into the ground within a couple of metres of the camera.
	fogDensity := float32(0.006)
	if g.heightmapPath != "" {
		fogDensity = 0.006 * hm.WorldW / worldSize
	}
	e.SetFogDensity(fogDensity)

	g.camera = glyph.NewFPCamera()
	g.camera.EyeHeight = 0.7
	if g.heightmapPath != "" {
		// Look toward the loaded terrain's own centre from the spawn point,
		// rather than the procedural terrain's implicit default (yaw 0,
		// which happens to face into that terrain because it is centred on
		// the spawn at the world origin) -- a loaded heightmap is not
		// necessarily centred on its spawn point at all. Yaw 0 is "look
		// along -Z" (FPCamera.Forward's own doc); atan2 solves
		// Forward=(dx,dz) normalized for the yaw that produces it.
		minX, minZ, maxX, maxZ := hm.Bounds()
		cx, cz := (minX+maxX)/2, (minZ+maxZ)/2
		dx, dz := cx-spawnX, cz-spawnZ
		g.camera.Yaw = float32(math.Atan2(float64(-dx), float64(-dz)))
		// A modest downward pitch: this fixture's footprint (10x10 units)
		// is small enough relative to FPCamera's default 1.6-unit eye
		// height and the level horizon that a level gaze reads mostly as
		// sky/fog above a thin strip of ground -- pitching down brings the
		// terrain itself into more of the frame.
		g.camera.Pitch = 0.35
	}

	e.Input().SetCursorLocked(true)

	log.Printf("07-terrain running: %dx%d heightmap over %.0fx%.0f units. Escape releases the cursor.",
		hm.GridW, hm.GridH, hm.WorldW, hm.WorldD)
	return nil
}

func (g *game) Update(e *glyph.Engine, dt float32) {
	in := e.Input()

	if in.KeyPressed(input.KeyEscape) {
		if in.CursorLocked() {
			in.SetCursorLocked(false)
		} else {
			e.Close()
		}
	}
	if in.MousePressed(input.MouseButtonLeft) && !in.CursorLocked() {
		in.SetCursorLocked(true)
	}

	// Mouse look consumes this frame's mouse delta, so it belongs per-frame.
	g.camera.Update(in)

	// Held keys: overwrite each frame, last sample wins.
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

		// Edge-triggered: latch it, because this frame may run no tick.
		if in.KeyPressed(input.KeySpace) {
			g.jumpQueued = true
		}
	}
}

// FixedUpdate moves the player on the simulation tick, so walking over terrain
// behaves identically regardless of frame rate.
func (g *game) FixedUpdate(e *glyph.Engine, dt float32) {
	intent := g.intent
	intent.Jump = g.jumpQueued
	g.jumpQueued = false

	e.MoveCharacter(g.player, intent, dt)
}

// LateUpdate places the camera after simulation has finished for this frame,
// at the interpolated position so the view is as smooth as the world.
func (g *game) LateUpdate(e *glyph.Engine, _ float32) {
	if t, ok := e.InterpolatedTransform(g.player); ok {
		g.camera.Follow(&t)
	}
	e.SetCamera(g.camera.ViewVectors())
}

// tint colors terrain by height and slope: rock on anything steep, sand at the
// waterline, grass in between, snow on the peaks.
func tint(height float32, normal [3]float32) [3]float32 {
	slope := 1 - normal[1] // 0 = flat, 1 = vertical
	if slope > 0.45 {
		return [3]float32{0.42, 0.40, 0.38}
	}
	switch {
	case height < 0.6:
		return [3]float32{0.76, 0.70, 0.50}
	case height > heightScale*0.72:
		return [3]float32{0.92, 0.93, 0.95}
	default:
		g := 0.42 + height/heightScale*0.12
		return [3]float32{0.24, g, 0.22}
	}
}

// ─────────────────────── procedural heightmap ───────────────────────
//
// Value-noise fBm with an island falloff. Deterministic from the seed, so
// -seed N reproduces the same terrain on every machine.

func main() {
	width := flag.Int("width", 1280, "window width")
	height := flag.Int("height", 720, "window height")
	fullscreen := flag.Bool("fullscreen", false, "run fullscreen on the primary monitor")
	frames := flag.Int("frames", 0, "render N frames then exit (0 = run until closed)")
	seed := flag.Int64("seed", 1, "terrain generation seed")
	shot := flag.String("screenshot", "", "write a PNG of the last frame to this path")
	heightmap := flag.String("heightmap", "", "load a .heightmap from disk instead of generating one procedurally")
	flag.Parse()

	opts := []glyph.Option{
		glyph.WithTitle("GlyphEngine - 07 Terrain"),
		glyph.WithDebugKeys(),
		glyph.WithWindowSize(*width, *height),
		glyph.WithMSAA(4),
		// The terrain spans 200 units; push the far plane out to match.
		glyph.WithProjection(50, 0.1, 800),
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

	e, err := glyph.New(&game{seed: *seed, heightmapPath: *heightmap}, opts...)
	if err != nil {
		log.Fatalf("create engine: %v", err)
	}
	defer e.Destroy()

	e.Run()
	log.Printf("rendered %d frames", e.FrameCount())
}
