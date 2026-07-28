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

const (
	gridSize    = 129   // heightmap resolution per side
	worldSize   = 200.0 // world units per side
	heightScale = 14.0  // peak height in world units

	playerHalfHeight = 0.9
)

type game struct {
	camera *glyph.FPCamera
	player ecs.Entity
	seed   int64

	// Sampled in Update, consumed in FixedUpdate. jumpQueued latches the
	// edge-triggered jump across frames that run no tick.
	intent     glyph.MoveIntent
	jumpQueued bool
}

func (g *game) Init(e *glyph.Engine) error {
	// ── terrain ──
	heights := generateHeights(gridSize, gridSize, g.seed)
	hm, err := glyph.NewHeightmap(
		gridSize, gridSize,
		worldSize, worldSize,
		-worldSize/2, -worldSize/2,
		heights,
	)
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
	spawnX, spawnZ := float32(0), float32(0)
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
	e.SetFogDensity(0.006)

	g.camera = glyph.NewFPCamera()
	g.camera.EyeHeight = 0.7

	e.Input().SetCursorLocked(true)

	log.Printf("07-terrain running: %dx%d heightmap over %.0fx%.0f units. Escape releases the cursor.",
		gridSize, gridSize, worldSize, worldSize)
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

func generateHeights(gridW, gridH int, seed int64) []float32 {
	heights := make([]float32, gridW*gridH)
	for iz := 0; iz < gridH; iz++ {
		for ix := 0; ix < gridW; ix++ {
			// Normalized [0,1] grid coordinates.
			u := float64(ix) / float64(gridW-1)
			v := float64(iz) / float64(gridH-1)

			h := fbm(u*4, v*4, seed)

			// Radial falloff so the terrain is an island: the edges drop to
			// zero, which also keeps the player away from the heightmap
			// boundary where HeightAt stops returning ground.
			dx, dz := u-0.5, v-0.5
			d := math.Sqrt(dx*dx+dz*dz) * 2 // 0 at center, 1 at edge midpoints
			falloff := 1 - smoothstep(clamp((d-0.35)/0.5, 0, 1))

			heights[iz*gridW+ix] = float32(h * falloff * heightScale)
		}
	}
	return heights
}

// hash returns a deterministic pseudo-random value in [0,1) for a lattice point.
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

// valueNoise interpolates the hashed lattice with a smoothstep falloff.
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

// fbm sums octaves of value noise at halving amplitude and doubling frequency.
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
	seed := flag.Int64("seed", 1, "terrain generation seed")
	flag.Parse()

	opts := []glyph.Option{
		glyph.WithTitle("GlyphEngine - 07 Terrain"),
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

	e, err := glyph.New(&game{seed: *seed}, opts...)
	if err != nil {
		log.Fatalf("create engine: %v", err)
	}
	defer e.Destroy()

	e.Run()
	log.Printf("rendered %d frames", e.FrameCount())
}
