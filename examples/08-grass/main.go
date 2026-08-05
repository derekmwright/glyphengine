// Command 08-grass scatters instanced grass across procedural terrain and
// walks a character through it.
//
// Grass is not entities. Hundreds of thousands of blades as ECS entities would
// swamp the draw list and the spatial grid for geometry that never moves and
// never interacts. Instead InitGrass scatters instances deterministically from
// the heightmap, uploads them once as per-instance data, and the renderer
// draws each variant in a handful of instanced calls, frustum-culled per tile.
//
// Placement is a hash of the tile coordinate, so it is stable across runs and
// across machines without storing anything: the same terrain always grows the
// same grass. A density mask carves it away where a game needs bare ground —
// paths, buildings, clearings.
//
//	go run ./08-grass              # windowed
//	go run ./08-grass -frames 150  # render 150 frames, then exit
//	go run ./08-grass -seed 3      # a different island
//
// WASD moves, mouse looks, Shift runs, Escape releases the cursor.
package main

import (
	"embed"
	"flag"
	"log"
	"math"
	"os"
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
	gridSize    = 129
	worldSize   = 160.0
	heightScale = 9.0

	playerHalfHeight = 0.9
)

type game struct {
	grassDist float32
	grassThin float32
	grassImp  float32
	timeOfDay float32
	stars     float64
	camera    *glyph.FPCamera
	player    glyph.Entity
	seed      int64

	intent     glyph.MoveIntent
	jumpQueued bool
}

func (g *game) Init(e *glyph.Engine) error {
	// Dump the baked impostor atlas when asked. An impostor that is framed
	// wrong or cut off is invisible in a field of grass and obvious in the
	// atlas, so being able to look at the artifact matters more than the flag
	// costs.
	defer func() {
		if d := os.Getenv("GLYPH_DUMP_IMPOSTOR"); d != "" {
			dumpImpostorAtlas(e, d)
		}
	}()

	// Grass distance tuning, so the LOD knobs can be swept without editing the
	// engine -- which is the entire point of GrassLOD being configurable.
	if g.grassDist > 0 || g.grassThin > 0 || g.grassImp > 0 {
		lod := e.Renderer().GrassLOD()
		if g.grassDist > 0 {
			lod.MaxDistance = g.grassDist
			lod.FadeStart = g.grassDist * 0.625
		}
		if g.grassThin > 0 {
			lod.ThinMin = g.grassThin
		}
		if g.grassImp > 0 {
			lod.ImpostorDistance = g.grassImp
		}
		e.Renderer().SetGrassLOD(lod)
	}

	r := e.Renderer()

	// ── terrain ──
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
	// A density mask thins or clears grass per cell — the same mechanism a
	// game uses for paths, clearings, and building footprints. Here it only
	// clears the player's own footprint, so they stand in the grass rather
	// than on a bald patch.
	mask := renderer.NewDensityMask(-worldSize/2, -worldSize/2, worldSize, worldSize, 1.0)
	mask.ClearCircle(0, 0, 1.5, 1.5)

	// Weights bias the scatter: mostly common grass, some wispy for variety.
	r.InitGrass(assetsFS, hm, hm.OriginX, hm.OriginZ, hm.WorldW, hm.WorldD,
		[]renderer.GrassModelSpec{
			{Path: "assets/flora/Grass_Common_Short.gltf", Weight: 40},
			{Path: "assets/flora/Grass_Common_Tall.gltf", Weight: 26},
			{Path: "assets/flora/Grass_Wispy_Short.gltf", Weight: 20},
			{Path: "assets/flora/Grass_Wispy_Tall.gltf", Weight: 14},
		}, mask)

	// ── player ──
	spawnY, _ := hm.HeightAt(0, 0)
	g.player = e.Spawn()
	e.C.Transform.Set(g.player, &glyph.Transform{
		Position: mgl32.Vec3{0, spawnY + playerHalfHeight, 0},
		Scale:    mgl32.Vec3{1, 1, 1},
	})
	e.C.Collider.Set(g.player, &glyph.Collider{HalfExtents: mgl32.Vec3{0.4, playerHalfHeight, 0.4}})
	e.C.Velocity.Set(g.player, &glyph.Velocity{})
	cc := glyph.NewCharacterController()
	e.C.CharacterController.Set(g.player, &cc)

	e.SetDayCycleSpeed(1.0 / 300.0)
	// Default is a low sun for long shadows across the grass. It is a flag
	// because half of what goes wrong in this scene only shows up at night,
	// and waiting out the day cycle to see it is not a debugging loop.
	e.SetTimeOfDay(g.timeOfDay)
	if env, ok := e.Scene.Env.(*glyph.Environment); ok && env.Sky != nil {
		env.Sky.StarDensity = float32(g.stars)
	}
	e.SetFogDensity(0.008)

	g.camera = glyph.NewFPCamera()
	g.camera.EyeHeight = 0.7
	e.Input().SetCursorLocked(true)

	log.Println("08-grass running. WASD moves, Shift runs, Escape releases the cursor.")
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

	g.camera.Update(in)

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
		if in.KeyPressed(input.KeySpace) {
			g.jumpQueued = true
		}
	}
}

func (g *game) FixedUpdate(e *glyph.Engine, dt float32) {
	intent := g.intent
	intent.Jump = g.jumpQueued
	g.jumpQueued = false
	e.MoveCharacter(g.player, intent, dt)
}

func (g *game) LateUpdate(e *glyph.Engine, _ float32) {
	if t, ok := e.InterpolatedTransform(g.player); ok {
		g.camera.Follow(&t)
	}
	e.SetCamera(g.camera.ViewVectors())
}

// tint keeps the terrain readable under the grass: earth where grass will
// cover it, rock on slopes too steep for anything to grow.
func tint(height float32, normal [3]float32) [3]float32 {
	if slope := 1 - normal[1]; slope > 0.5 {
		return [3]float32{0.44, 0.41, 0.37}
	}
	g := 0.34 + height/heightScale*0.10
	return [3]float32{0.22, g, 0.18}
}

// ─────────────────────── procedural heightmap ───────────────────────

func generateHeights(gridW, gridH int, seed int64) []float32 {
	heights := make([]float32, gridW*gridH)
	for iz := 0; iz < gridH; iz++ {
		for ix := 0; ix < gridW; ix++ {
			u := float64(ix) / float64(gridW-1)
			v := float64(iz) / float64(gridH-1)
			h := fbm(u*3, v*3, seed)

			dx, dz := u-0.5, v-0.5
			d := math.Sqrt(dx*dx+dz*dz) * 2
			falloff := 1 - smoothstep(clamp((d-0.4)/0.5, 0, 1))

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
	v00, v10 := hash(ix, iy, seed), hash(ix+1, iy, seed)
	v01, v11 := hash(ix, iy+1, seed), hash(ix+1, iy+1, seed)
	sx, sy := smoothstep(xf), smoothstep(yf)
	top := v00 + (v10-v00)*sx
	bot := v01 + (v11-v01)*sx
	return top + (bot-top)*sy
}

func fbm(x, y float64, seed int64) float64 {
	amp, freq, sum, norm := 1.0, 1.0, 0.0, 0.0
	for i := 0; i < 5; i++ {
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
	shot := flag.String("screenshot", "", "write a PNG of the last frame to this path")
	seed := flag.Int64("seed", 1, "terrain generation seed")
	gputime := flag.Bool("gputime", false, "print mean per-pass GPU timings on exit")
	msaa := flag.Int("msaa", 4, "MSAA sample count (1 disables it)")
	grassDist := flag.Float64("grassdist", 0, "grass cull distance (0 = engine default 80)")
	grassThin := flag.Float64("grassthin", 0, "grass density floor 0..1 (0 = engine default 0.35)")
	grassImp := flag.Float64("grassimpostor", 0, "distance past which grass becomes billboards (0 = meshes everywhere)")
	timeOfDay := flag.Float64("timeofday", 0.28, "starting time of day: 0 = midnight, 0.5 = noon")
	stars := flag.Float64("stars", 1.0, "star density multiplier (0 = none)")
	flag.Parse()

	opts := []glyph.Option{
		glyph.WithTitle("GlyphEngine - 08 Grass"),
		glyph.WithDebugKeys(),
		glyph.WithWindowSize(*width, *height),
		glyph.WithMSAA(*msaa),
		glyph.WithProjection(55, 0.1, 600),
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

	e, err := glyph.New(&game{seed: *seed, grassDist: float32(*grassDist), grassThin: float32(*grassThin), grassImp: float32(*grassImp), timeOfDay: float32(*timeOfDay), stars: *stars}, opts...)
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
