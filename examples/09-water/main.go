// Command 09-water fills a basin with animated water and lets you walk into it.
//
// The surface is built from the same heightmap the terrain is, so the two
// cannot disagree about where the shoreline is. Every vertex carries the still
// depth of the water beneath it, baked at build time, and that one number does
// most of the work:
//
//   - waves shrink to nothing as the water shallows, so the surface meets the
//     shore instead of cutting through it
//   - the colour absorbs from ShallowColor toward DeepColor with depth
//   - refraction fades out in the shallows, which is what stops the shoreline
//     smearing into the lake
//
// Refraction samples the opaque scene, so water draws in a second pass after
// everything else. Press R to toggle it and watch the lake bed stop rippling.
//
//	go run ./09-water              # windowed
//	go run ./09-water -frames 200  # render 200 frames, then exit
//	go run ./09-water -seed 3      # a different basin
//
// WASD moves, mouse looks, Shift runs, Space jumps, R toggles refraction,
// Escape releases the cursor.
package main

import (
	"flag"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"log"
	"math"
	"os"
	"runtime"

	"github.com/go-gl/mathgl/mgl32"

	glyph "github.com/derekmwright/glyphengine"
	"github.com/derekmwright/glyphengine/ecs"
	"github.com/derekmwright/glyphengine/input"
	"github.com/derekmwright/glyphengine/renderer"
)

func init() {
	// GLFW must be called from the thread that initialized it.
	runtime.LockOSThread()
}

const (
	gridSize    = 161
	worldSize   = 200.0
	heightScale = 15.0

	// The basin is carved out of the island after the noise, so the lake has a
	// definite bed rather than whatever the noise happened to leave low.
	lakeRadius = 55.0
	lakeDepth  = 14.0
	waterLevel = 3.0

	playerHalfHeight = 0.9
)

type game struct {
	camera *glyph.FPCamera
	player ecs.Entity
	water  glyph.Entity
	seed   int64

	intent     glyph.MoveIntent
	jumpQueued bool
	refract    bool
	pitch      float32
	tod        float32
	clouds     int
	stars      float64
	milkyway   string
	band       float64
	fogHeight  float32
	yaw        float32
	shafts     float32
	pillars    bool
}

func (g *game) Init(e *glyph.Engine) error {
	// ── optional sky panorama ──
	if g.milkyway != "" {
		if err := loadMilkyWay(e, g.milkyway); err != nil {
			return err
		}
	}

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

	// ── water ──
	opts := glyph.DefaultWaterOptions(waterLevel)
	opts.Resolution = 200
	if !g.refract {
		opts.RefractStrength = 0
	}

	surface, err := e.CreateWaterMesh(hm, opts)
	if err != nil {
		return err
	}
	g.water = e.Spawn()
	e.C.Transform.Set(g.water, &glyph.Transform{Scale: mgl32.Vec3{1, 1, 1}})
	e.C.MeshRef.Set(g.water, &glyph.MeshRef{Mesh: surface})
	e.C.Water.Set(g.water, &glyph.Water{Options: opts})
	e.C.Static.Set(g.water, &glyph.Static{})

	// Optional hard occluders. Light shafts are built from the gaps between
	// things silhouetted against the sun, and rolling terrain has no gaps --
	// its edge is one smooth curve. A row of pillars gives the effect
	// something to be interrupted by.
	if g.pillars {
		pillar, err := e.Renderer().CreateCube(1.0)
		if err != nil {
			return err
		}
		for i := -3; i <= 3; i++ {
			p := e.Spawn()
			e.C.Transform.Set(p, &glyph.Transform{
				Position: mgl32.Vec3{-16 + float32(i)*0.2, 7, 44 + float32(i)*3.1},
				Scale:    mgl32.Vec3{1.1, 13, 1.1},
			})
			e.C.MeshRef.Set(p, &glyph.MeshRef{Mesh: pillar, Roughness: 0.85})
			e.C.Color.Set(p, &glyph.Color{R: 0.30, G: 0.28, B: 0.26})
			e.C.Static.Set(p, &glyph.Static{})
		}
	}

	// ── player ──
	// Walk outward from the middle of the basin until the ground clears the
	// waterline, and stand just past that. Finding the shore rather than
	// assuming it means -seed keeps working: the noise moves the shoreline
	// around, and a hardcoded spawn ends up either swimming or behind a hill.
	spawnX, spawnZ := float32(0), float32(lakeRadius)
	for d := float32(0); d < worldSize/2; d += 0.5 {
		if h, ok := hm.HeightAt(0, d); ok && h > waterLevel+0.5 {
			spawnZ = d + 2
			break
		}
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
	e.SetTimeOfDay(0.32)
	if g.tod >= 0 {
		// Freeze the clock so a given time of day can be inspected, and
		// screenshots of it are reproducible.
		e.SetTimeOfDay(g.tod)
		e.SetDayCycleSpeed(0)
	}
	e.SetFogDensity(0.005)

	if env, ok := e.Scene.Env.(*glyph.Environment); ok {
		if env.Sky != nil {
			env.Sky.CloudSteps = g.clouds
			env.Sky.StarDensity = float32(g.stars)
			env.Sky.MilkyWay = float32(g.band)
			if g.shafts >= 0 {
				env.Sky.LightShafts = g.shafts
			}
		}
		if g.fogHeight > 0 && env.Fog != nil {
			// Pool the mist on the water rather than spreading it evenly
			// through the air above the island.
			env.Fog.Height = g.fogHeight
			env.Fog.BaseHeight = waterLevel
		}
	}

	g.camera = glyph.NewFPCamera()
	g.camera.EyeHeight = 0.7
	g.camera.Yaw = g.yaw // 0 faces back toward the lake
	g.camera.Pitch = g.pitch
	e.Input().SetCursorLocked(true)

	log.Println("09-water running. WASD moves, R toggles refraction, Escape releases the cursor.")
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

	// Toggling refraction off leaves the waves, the Fresnel reflection, and the
	// depth colouring intact -- only the distortion of the lake bed goes away,
	// which is the clearest way to see what the second pass actually buys.
	if in.KeyPressed(input.KeyR) {
		g.refract = !g.refract
		if w, ok := e.C.Water.Get(g.water); ok {
			if g.refract {
				w.Options.RefractStrength = 1.0
			} else {
				w.Options.RefractStrength = 0
			}
		}
		log.Printf("refraction %v", g.refract)
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

// tint colors terrain by height and slope. The band just above the waterline is
// wet sand, which gives the shore somewhere to arrive rather than grass running
// straight into water.
func tint(height float32, normal [3]float32) [3]float32 {
	slope := 1 - normal[1]
	if slope > 0.45 {
		return [3]float32{0.42, 0.40, 0.38}
	}
	switch {
	case height < waterLevel+0.9:
		return [3]float32{0.74, 0.68, 0.50}
	case height > heightScale*0.72:
		return [3]float32{0.92, 0.93, 0.95}
	default:
		g := 0.40 + height/heightScale*0.14
		return [3]float32{0.22, g, 0.20}
	}
}

// ─────────────────────── procedural heightmap ───────────────────────

func generateHeights(gridW, gridH int, seed int64) []float32 {
	heights := make([]float32, gridW*gridH)
	for iz := 0; iz < gridH; iz++ {
		for ix := 0; ix < gridW; ix++ {
			u := float64(ix) / float64(gridW-1)
			v := float64(iz) / float64(gridH-1)

			h := fbm(u*4, v*4, seed)

			// Island falloff, as in 07-terrain, but bottoming out at 45%
			// rather than 0. The rim has to stay above the waterline or the
			// whole perimeter floods and the lake becomes a sea.
			dx, dz := u-0.5, v-0.5
			d := math.Sqrt(dx*dx+dz*dz) * 2
			falloff := 1 - 0.55*smoothstep(clamp((d-0.35)/0.5, 0, 1))
			hf := float32(h * falloff * heightScale)

			// Carve the basin. Subtracting a smooth bowl rather than clamping
			// to a flat floor keeps the bed uneven, which is what makes the
			// depth-based colour and the refraction visible at all -- over a
			// flat bottom both are constant and the effect reads as a tint.
			wx := (u - 0.5) * worldSize
			wz := (v - 0.5) * worldSize
			r := math.Sqrt(wx*wx+wz*wz) / lakeRadius
			if r < 1 {
				bowl := 1 - smoothstep(clamp(r, 0, 1))
				hf -= float32(bowl * lakeDepth)
			}

			heights[iz*gridW+ix] = hf
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
	seed := flag.Int64("seed", 1, "terrain generation seed")
	refract := flag.Bool("refraction", true, "distort the lake bed through the surface (costs a second render pass)")
	pitch := flag.Float64("pitch", 0, "initial camera pitch in radians; positive looks down")
	msaa := flag.Int("msaa", 4, "MSAA sample count (1 disables it)")
	novsync := flag.Bool("novsync", false, "disable vsync, for measuring frame cost")
	clouds := flag.Int("clouds", glyph.CloudsHigh, "volumetric cloud raymarch steps (0 disables)")
	stars := flag.Float64("stars", 1.0, "star density multiplier (0 = none)")
	milkyway := flag.String("milkyway", "", "equirectangular sky panorama (PNG) to use as the galactic band")
	band := flag.Float64("band", 1, "procedural galactic band strength, 0 to 1")
	fogHeight := flag.Float64("fogheight", 0, "height fog falloff in world units (0 = uniform density)")
	yaw := flag.Float64("yaw", 0, "initial camera yaw in radians")
	shafts := flag.Float64("shafts", -1, "light shaft strength (0 disables; -1 keeps the default)")
	pillars := flag.Bool("pillars", false, "spawn pillars between the spawn point and the setting sun")
	tod := flag.Float64("time", -1, "freeze time of day in [0,1): 0=midnight, 0.25=sunrise, 0.5=noon, 0.75=sunset")
	shot := flag.String("screenshot", "", "write a PNG of the last frame to this path")
	flag.Parse()

	opts := []glyph.Option{
		glyph.WithTitle("GlyphEngine - 09 Water"),
		glyph.WithDebugKeys(),
		glyph.WithWindowSize(*width, *height),
		glyph.WithMSAA(*msaa),
		glyph.WithProjection(50, 0.1, 800),
	}
	if *novsync {
		opts = append(opts, glyph.WithVSync(false))
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

	e, err := glyph.New(&game{seed: *seed, refract: *refract, pitch: float32(*pitch), tod: float32(*tod), clouds: *clouds, stars: *stars, milkyway: *milkyway, band: *band, fogHeight: float32(*fogHeight), yaw: float32(*yaw), shafts: float32(*shafts), pillars: *pillars}, opts...)
	if err != nil {
		log.Fatalf("create engine: %v", err)
	}
	defer e.Destroy()

	e.Run()
	log.Printf("rendered %d frames", e.FrameCount())
}

// loadMilkyWay reads an equirectangular panorama off disk and hands it to the
// star pass. Off disk rather than embedded because the engine ships no sky
// image: they are megabytes and they carry a licence.
func loadMilkyWay(e *glyph.Engine, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	img, err := png.Decode(f)
	if err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	b := img.Bounds()
	rgba := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(rgba, rgba.Bounds(), img, b.Min, draw.Src)

	// The star pass samples a hemi-octahedral map, not the equirect. Binding the
	// panorama directly draws a mirrored, smeared sky rather than failing, so
	// the resample is not optional. A quarter of the source width is about the
	// resolution the original had over the half-sky this covers.
	size := b.Dx() / 4
	if size < 256 {
		size = 256
	}
	skyMap := renderer.EquirectToSkyMap(rgba.Pix, b.Dx(), b.Dy(), size)

	tex, err := e.Renderer().CreateTexture(skyMap, size, size)
	if err != nil {
		return err
	}
	e.Renderer().SetMilkyWayTexture(tex)
	log.Printf("Milky Way panorama: %s (%dx%d equirect -> %dx%d sky map)", path, b.Dx(), b.Dy(), size, size)
	return nil
}
