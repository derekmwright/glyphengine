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
// Four opt-in flags put blended effects on either side of the surface, which
// is the case issue #45 was about and the one no other example reaches:
//
//	-plume      an additive flame rising from a stack in the shallows
//	-ghost      a Translucent pane standing at the shoreline
//	-marker     a world-space overlay disc over the water
//	-submerged  a Translucent block on the lake bed, and bubbles above it
//
// The first three straddle eye height, so their lower halves land on water and
// their upper halves on sky; the fourth sits entirely under the surface and has
// to stay behind it. See `task waterblend` and docs/agents/water.md.
//
// Three more flags light the lake at night, which is issue #39's scene:
//
//	-lamps N    warm lamps on piles standing in the water and on the shore
//	-spots N    floodlights on masts in the lake, aimed at their reflections
//	-lampsoff   build every pile and fixture but hand the scene no lights
//
// -lampsoff is the control, and it is what makes `task waterlight` mean
// anything: the pair differs in the lights and in nothing else, so whatever
// the water gains between the two captures is the lamps.
//
// One more flag moves the whole atmosphere to another planet:
//
//	-alien      a violet-and-amber sky palette instead of Earth's
//
// This scene is the one worth doing it in, because the sky, the fogged
// distance and the lake reflecting both are in one frame -- which is the set
// that used to disagree when a game replaced sky.frag to get a sky of its own.
// See `task skypalette` and docs/agents/environment.md.
//
//	go run ./09-water              # windowed
//	go run ./09-water -frames 200  # render 200 frames, then exit
//	go run ./09-water -seed 3      # a different basin
//	go run ./09-water -hud 26      # HUD lines across the waterline; see task hud
//	go run ./09-water -plume -ghost -marker -submerged
//	go run ./09-water -lamps 9 -spots 2 -time 0.02   # lamplight on the lake
//	go run ./09-water -alien -time 0.35              # not Earth's sky
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

	// The camera sits this far above the player's centre; see FPCamera.EyeHeight
	// below. Named because the lamp placement needs the eye's height over the
	// still surface to work out where a reflection lands.
	eyeHeight = 0.7

	// hudLine is what -hud repeats. Mixed case with ascenders, descenders and
	// digits, so a row of it carries enough ink for a mean to be stable, and
	// short enough at scale 20 to stay clear of the right edge at 1280.
	hudLine = "HUD LEGIBILITY 0123456789 ABCDEFGHIJ abcdefghij"

	// ── the blended effects, for issue #45 ──
	//
	// Heights are chosen against the eye, which stands on the shore at 6.46
	// with -seed 1 (ground 4.86, plus a 0.9 half-height and a 0.7 eye offset)
	// while the surface is at 3.0.
	//
	// That is what these numbers are for, and "near the water" is not it. A
	// point above the surface is in front of the water exactly when the ray
	// through it keeps descending to meet the surface -- which is to say when
	// it is BELOW the eye. So each of these spans 6.46: the part under it
	// lands on the lake and vanished before the fix, the part over it lands on
	// sky and never did. That split down the middle of one object is the
	// symptom as reported.
	effectStackTop = waterLevel + 0.6 // 3.6, under the eye; the flame carries it to about 7.8
	effectPaneLow  = waterLevel + 0.2 // 3.2
	effectPaneHigh = waterLevel + 3.6 // 6.6
	effectMarkerY  = waterLevel + 1.0 // 4.0, squarely on the water

	// Where in the lake each effect stands, as a distance from the spawn and a
	// depth of water, rather than a hardcoded Z: -seed moves the shoreline.
	//
	// The distance floor matters as much as the depth. The eye is 3.46 above
	// the surface looking level, and the frame bottom is 25 degrees down, so
	// the nearest water on screen is 3.46/tan(25) = 7.4 units out. A perfectly
	// placed object closer than that is simply below the frame, which is how
	// the first version of the submerged pair came out empty.
	effectShoreDist  = 8.0
	effectShoreDepth = 1.0
	effectDeepDist   = 13.0
	effectDeepDepth  = 2.0

	// One instance buffer holds every emitter, so this is the ceiling for the
	// flame and the bubbles together.
	effectParticles = 640

	// ── lamplight on the lake, issue #39 ──
	//
	// The lamps sit on a grid over one fixed patch of water in front of the
	// spawn: the near edge is past the bottom of the frame (see
	// effectShoreDist for that arithmetic) and the far edge is still well
	// inside a lake of radius 55. More lamps pack the same patch tighter
	// rather than reaching further out, which is what lets `-lamps 400` be a
	// cost measurement of the same piece of water `-lamps 9` is a picture of.
	//
	// The width deliberately runs past the shoreline at the near row, because
	// a lamp BESIDE a lake is as much the reported case as one over it. At
	// -seed 1 that puts one of the nine on dry ground (the bed under it is
	// 3.58 against a waterLevel of 3.0) and the other eight in water from
	// 0.4 to 12 units deep.
	lampNear      = 12.0
	lampFar       = 34.0
	lampHalfWidth = 22.0

	// Above the surface, or above the ground where a pile lands dry. Low
	// enough that the streak on the water is long -- a lamp directly overhead
	// makes a dot, and the streak is the thing worth looking at.
	lampHeight = 2.6

	// Range against height: 14 reaches roughly 13 units out along the surface
	// from a lamp 2.6 above it, so neighbouring pools on the 9-lamp grid
	// overlap slightly and the far water is outside every one of them. That
	// far water is what `task waterlight` requires to be unchanged.
	lampRange = 14.0

	// The floodlights stand on masts out in the lake and aim back at their own
	// reflections; see spawnLamps for why that is the only aim that shows a
	// cone on water at all.
	spotHeight    = 4.0
	spotRange     = 30.0
	spotHalfWidth = 9.0
	spotOut       = 24.0 // from the spawn, toward the middle of the lake
	spotInner     = 0.07 // half-angles, radians
	spotOuter     = 0.14

	// The beam is aimed this fraction of the way from the fixture to its own
	// reflection rather than all the way onto it, so the cone edge lands
	// ACROSS the streak and cuts it. Aimed dead on, the whole streak sits
	// inside the cone and nothing shows the cone at all: widening spotOuter
	// from 0.34 to 0.60 rad then moves the water by 0.15 of a channel, which
	// is a spot light drawing a point light.
	spotAimBias = 1.0
)

// lampColor is a warm white lamp, the same hue 21-streetlights puts on its
// ring path, scaled up because water returns a lamp only as a reflection:
// there is no diffuse pool to carry it, so what does not land in the streak
// does not land at all. See the measurement in water.frag.
var lampColor = mgl32.Vec3{1.0, 0.74, 0.50}.Mul(2.0)

// spotColor is 21-streetlights' 2800 K porch bulb at its intensity, unchanged,
// so the two examples do not disagree about what an incandescent fixture is.
var spotColor = mgl32.Vec3{1.0, 0.58, 0.28}.Mul(2.8)

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
	shaftShape glyph.LightShaftShape
	pillars    bool
	pauseAt    int
	hud        int
	bloom      float32
	bloomThres float32

	// Blended effects across the waterline. Off by default, so every other
	// capture of this scene is byte for byte what it was.
	plume     bool
	ghost     bool
	marker    bool
	submerged bool

	// Lamps over the water. Off by default for the same reason.
	lampCount int
	spotCount int
	lampsOff  bool
	// volumetric is SpotLight.Volumetric and PointLight.Volumetric for every
	// light this scene places. 0 is the default, so the stock frame is the one
	// this example has always rendered.
	volumetric float32
	lampPosts  bool

	// An atmosphere that is not Earth's. Off by default, again so nothing
	// else moves.
	alien bool

	flame   *glyph.ParticleEmitter
	bubbles *glyph.ParticleEmitter

	markerMesh  *renderer.Mesh
	markerModel mgl32.Mat4
	overlays    []renderer.RenderObject
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

	// ── blended effects across the waterline ──
	//
	// Everything here is opt-in. Nothing is spawned, no particle system is
	// allocated and no overlay is set unless a flag asked for it, so the
	// default frame is unchanged.
	if err := g.spawnEffects(e, hm, spawnZ); err != nil {
		return err
	}

	// ── lamps over the water ──
	//
	// Also opt-in: with no -lamps and no -spots the scene hands the engine no
	// lights at all, which is the state every other capture of this example
	// was taken in.
	if err := g.spawnLamps(e, hm, spawnZ, spawnY+playerHalfHeight+eyeHeight); err != nil {
		return err
	}

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
			// Zero fields keep the engine's defaults, so passing the struct
			// through whole is the same as not touching it.
			env.Sky.LightShaftShape = g.shaftShape
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

	// A colony on an alien moon, which is what issue #12 was written from.
	// Opt-in, so every other capture of this scene is byte for byte what it
	// was, and set through Scene rather than through the sky shader because
	// these six colours are the whole atmosphere's: the dome overhead, the
	// haze the far shore fades into, and the lake reflecting both. Replacing
	// sky.frag would move only the first of the three.
	//
	// The night pair is kept nearly as dark as the engine's. They are what
	// the fog and the water reach at midnight as well as what the sky does,
	// so a legible night sky bought here is a washed-out landscape --
	// brighten the moon instead. See DefaultSkyPalette.
	if g.alien {
		e.Scene.SetSkyPalette(glyph.SkyPalette{
			ZenithDay:       mgl32.Vec3{0.30, 0.10, 0.62},
			HorizonDay:      mgl32.Vec3{0.95, 0.55, 0.22},
			ZenithTwilight:  mgl32.Vec3{0.18, 0.04, 0.30},
			HorizonTwilight: mgl32.Vec3{0.95, 0.22, 0.30},
			ZenithNight:     mgl32.Vec3{0.0040, 0.0012, 0.0060},
			HorizonNight:    mgl32.Vec3{0.0110, 0.0035, 0.0055},
		})
	}

	g.camera = glyph.NewFPCamera()
	g.camera.EyeHeight = eyeHeight
	g.camera.Yaw = g.yaw // 0 faces back toward the lake
	g.camera.Pitch = g.pitch

	// Off by default, so every other capture of this scene is unchanged.
	if g.bloom > 0 {
		e.Renderer().SetBloom(g.bloom, g.bloomThres, 0.2, 1.0)
	}

	e.Input().SetCursorLocked(true)

	log.Println("09-water running. WASD moves, R toggles refraction, Escape releases the cursor.")
	return nil
}

// spawnEffects places blended geometry on both sides of the water surface.
//
// This is the scene issue #45 needed and no example had: water and a blended
// effect in the same frame. Water is drawn last because refraction samples the
// finished frame, and nothing blended writes depth, so before the fix every one
// of these was painted over wherever it crossed the lake -- cut off at the
// waterline exactly as reported from Vesper III.
//
// Positions are found from the heightmap rather than hardcoded so that -seed
// keeps working, the same way the player spawn is.
func (g *game) spawnEffects(e *glyph.Engine, hm *glyph.Heightmap, spawnZ float32) error {
	if !g.plume && !g.ghost && !g.marker && !g.submerged {
		return nil
	}
	r := e.Renderer()

	// Walk from the spawn toward the middle of the basin until the bed has
	// dropped a clear margin below the waterline, starting no closer than
	// dist so the result is inside the frame as well as inside the lake.
	find := func(x, dist, depth float32) (z, bed float32) {
		start := spawnZ - dist
		for probe := start; probe > -lakeRadius; probe -= 0.25 {
			if h, ok := hm.HeightAt(x, probe); ok && h < waterLevel-depth {
				return probe, h
			}
		}
		return start, waterLevel - depth
	}

	if g.plume || g.submerged {
		r.InitParticles(effectParticles)
	}

	if g.plume {
		// A stack standing in the shallows with a flame on top. The stack is
		// opaque and writes depth, which is the control: the flame in front of
		// it is blended and does not, and only the blended half ever went
		// missing.
		stackZ, bed := find(-3.0, effectShoreDist, effectShoreDepth)
		stack, err := r.CreateCube(1.0)
		if err != nil {
			return err
		}
		ent := e.Spawn()
		e.C.Transform.Set(ent, &glyph.Transform{
			Position: mgl32.Vec3{-3.0, (bed + effectStackTop) / 2, stackZ},
			Scale:    mgl32.Vec3{0.9, effectStackTop - bed, 0.9},
		})
		e.C.MeshRef.Set(ent, &glyph.MeshRef{Mesh: stack, Roughness: 0.9})
		e.C.Color.Set(ent, &glyph.Color{R: 0.26, G: 0.23, B: 0.21})
		e.C.Static.Set(ent, &glyph.Static{})

		g.flame = glyph.NewParticleEmitter(flameConfig(),
			-3.25, -2.75, stackZ-0.25, stackZ+0.25, effectStackTop, 0, 0.15)
	}

	if g.ghost {
		// A pane at the shoreline. DoubleSided so the far face is there too,
		// which is the case that routes it through the second blended pipeline.
		paneZ, _ := find(3.2, effectShoreDist, effectShoreDepth)
		pane, err := r.CreateCube(1.0)
		if err != nil {
			return err
		}
		ent := e.Spawn()
		e.C.Transform.Set(ent, &glyph.Transform{
			Position: mgl32.Vec3{3.2, (effectPaneLow + effectPaneHigh) / 2, paneZ},
			Scale:    mgl32.Vec3{2.4, effectPaneHigh - effectPaneLow, 0.08},
		})
		e.C.MeshRef.Set(ent, &glyph.MeshRef{Mesh: pane, Roughness: 0.25, Metallic: 0.2})
		e.C.Color.Set(ent, &glyph.Color{R: 0.45, G: 0.9, B: 1.0})
		e.C.DoubleSided.Set(ent, &glyph.DoubleSided{})
		e.C.Translucent.Set(ent, &glyph.Translucent{Alpha: 0.5})
		e.C.Static.Set(ent, &glyph.Static{})
	}

	if g.marker {
		// A world-space overlay: the objective pin a game puts over a thing.
		// It is depth-tested against nothing at all, so "the water drew over
		// it" is unambiguous -- there is no other reason for it to be missing.
		markerZ, _ := find(0, effectShoreDist, effectShoreDepth)
		disc, err := r.CreateDisc(0.55, 24)
		if err != nil {
			return err
		}
		g.markerMesh = disc
		g.markerModel = mgl32.Translate3D(0, effectMarkerY, markerZ)
		g.overlays = []renderer.RenderObject{{Mesh: disc, Color: [3]float32{1.0, 0.35, 0.2}}}
	}

	if g.submerged {
		// Under the surface, and it has to stay there: the water refracts and
		// absorbs what is behind it, so a submerged object drawn after the
		// water would sit on top of the lake at full brightness with no
		// distortion at all. This is the half of the split that must NOT move.
		deepZ, bed := find(0, effectDeepDist, effectDeepDepth)
		block, err := r.CreateCube(1.0)
		if err != nil {
			return err
		}
		// From the bed up to just under the surface. Water absorbs along the
		// travelled path, not the vertical depth, so a slab sitting on a bed
		// three units down is looked at through nine units of water at this
		// grazing angle and reads as nothing at all. Coming up to within half
		// a unit of the surface leaves it clearly submerged and clearly there.
		const top float32 = waterLevel - 0.5
		ent := e.Spawn()
		e.C.Transform.Set(ent, &glyph.Transform{
			Position: mgl32.Vec3{0, (bed + top) / 2, deepZ},
			Scale:    mgl32.Vec3{3.2, top - bed, 3.2},
		})
		e.C.MeshRef.Set(ent, &glyph.MeshRef{Mesh: block, Roughness: 0.4})
		e.C.Color.Set(ent, &glyph.Color{R: 1.0, G: 0.72, B: 0.25})
		e.C.Translucent.Set(ent, &glyph.Translucent{Alpha: 0.6})
		e.C.Static.Set(ent, &glyph.Static{})

		// Bubbles in a column between the block and the camera. The height
		// range is written as a distance to the surface rather than to the
		// bed, so the top of the column is waterLevel-0.7 whatever the bed
		// happens to be under it -- they must never break the surface, or the
		// same emitter would be proving two things at once.
		g.bubbles = glyph.NewParticleEmitter(bubbleConfig(),
			-1.2, 1.2, deepZ+1.6, deepZ+2.8, bed, 0.3, waterLevel-0.7-bed)
	}

	return nil
}

// spawnLamps puts warm point lamps and shore floodlights over the lake.
//
// This is issue #39's scene, and no example had it: water.frag shaded its own
// surface and never consulted the clustered light list, so a lamp beside a
// lake lit the bed and the shore and left the water itself untouched -- no
// streak, no pool, and a surface that took the full night grade in the middle
// of a lit harbour. Nothing could see that, because the only scene with water
// in it had no lights and the only scenes with lights in them had no water.
//
// Placement is found from the heightmap, like the player spawn and the blended
// effects, so -seed keeps working: a pile stands on the bed where the bed is
// under water and on the ground where it is not.
func (g *game) spawnLamps(e *glyph.Engine, hm *glyph.Heightmap, spawnZ, eyeY float32) error {
	if g.lampCount <= 0 && g.spotCount <= 0 {
		return nil
	}
	r := e.Renderer()

	// A unit-height cylinder scaled per pile, rather than one mesh per height.
	// CreateCylinder centres the mesh, so a pile is positioned at its middle.
	var post *renderer.Mesh
	var bulb *renderer.Mesh
	if g.lampPosts {
		var err error
		if post, err = r.CreateCylinder(0.09, 1.0, 8); err != nil {
			return err
		}
		if bulb, err = r.CreateCube(1.0); err != nil {
			return err
		}
	}

	// fixture stands a pile from the bed (or the ground) up to head, with an
	// emissive marker on top, so the light reads as coming from something.
	// The marker is emissive rather than lit: an emissive surface is its own
	// colour at night, which is what a bulb is.
	fixture := func(x, z, head, size float32) {
		if !g.lampPosts {
			return
		}
		base, ok := hm.HeightAt(x, z)
		if !ok {
			base = waterLevel
		}
		p := e.Spawn()
		e.C.Transform.Set(p, &glyph.Transform{
			Position: mgl32.Vec3{x, (base + head) / 2, z},
			Scale:    mgl32.Vec3{1, head - base, 1},
		})
		e.C.MeshRef.Set(p, &glyph.MeshRef{Mesh: post, Roughness: 0.5, Metallic: 0.3})
		e.C.Color.Set(p, &glyph.Color{R: 0.20, G: 0.20, B: 0.22})
		e.C.Static.Set(p, &glyph.Static{})

		b := e.Spawn()
		e.C.Transform.Set(b, &glyph.Transform{Position: mgl32.Vec3{x, head, z}, Scale: mgl32.Vec3{size, size, size}})
		e.C.MeshRef.Set(b, &glyph.MeshRef{Mesh: bulb})
		e.C.Color.Set(b, &glyph.Color{R: 1.0, G: 0.82, B: 0.55})
		e.C.Emissive.Set(b, &glyph.Emissive{})
	}

	// ── lamps on a grid over the water in front of the spawn ──
	lamps := make([]glyph.PointLight, 0, g.lampCount)
	if g.lampCount > 0 {
		cols := int(math.Ceil(math.Sqrt(float64(g.lampCount))))
		rows := (g.lampCount + cols - 1) / cols
		step := 2 * lampHalfWidth / float32(cols)
		for i := 0; i < g.lampCount; i++ {
			col, row := i%cols, i/cols
			v := float32(0.5)
			if rows > 1 {
				v = float32(row) / float32(rows-1)
			}
			// Alternate rows are offset half a column, and the NEAR row is the
			// offset one: a pile dead ahead at the near row is four metres of
			// black post up the middle of the frame, and the picture this
			// example exists to show is behind it.
			x := -lampHalfWidth + step*(float32(col)+0.5)
			if row%2 == 0 {
				x += step * 0.5
			}
			z := spawnZ - (lampNear + (lampFar-lampNear)*v)

			// A pile in the water carries its lamp lampHeight above the
			// SURFACE; one that landed on dry ground carries it lampHeight
			// above the ground. Measuring both from the bed would sink the
			// ones in the deep water and leave the shore ones on stilts.
			head := float32(waterLevel + lampHeight)
			if bed, ok := hm.HeightAt(x, z); ok && bed > waterLevel {
				head = bed + lampHeight
			}
			lamps = append(lamps, glyph.PointLight{
				Pos:   mgl32.Vec3{x, head, z},
				Range: lampRange,
				Color: lampColor,
				// 0 unless -volumetric asked for it, so every capture this
				// example has ever taken is unchanged.
				Volumetric: g.volumetric,
			})
			fixture(x, z, head, 0.22)
		}
	}

	// ── floodlights on masts in the lake, aimed at their own reflections ──
	//
	// Aiming a cone at its own reflection looks like a trick and is the only
	// aim that shows a cone on water at all. A specular surface returns a
	// light to the eye from exactly one place: the point where the half-vector
	// lines up with the normal, which on a flat lake is on the line between
	// the eye and the light's mirror image, a fraction
	// eyeAbove/(eyeAbove+lampAbove) of the way out. Point the beam anywhere
	// else and it lands on water that cannot reflect it toward the camera, so
	// the surface shows nothing however bright the fixture is -- the lit lake
	// BED still shows through the refraction, but that is the terrain shader's
	// doing and not the water's. Aimed here, the cone edge lands across the
	// reflection streak and cuts it, which is what makes `lightSpotFactor` on
	// water something a capture can show.
	spots := make([]glyph.SpotLight, 0, g.spotCount)
	for i := 0; i < g.spotCount; i++ {
		u := float32(0.5)
		if g.spotCount > 1 {
			u = float32(i) / float32(g.spotCount-1)
		}
		x := -spotHalfWidth + 2*spotHalfWidth*u
		z := spawnZ - spotOut
		head := float32(waterLevel + spotHeight)
		if bed, ok := hm.HeightAt(x, z); ok && bed > waterLevel {
			head = bed + spotHeight
		}

		// The reflection point, in the eye's own frame: eyeY is the camera, and
		// both heights are measured from the still surface.
		t := (eyeY - waterLevel) / ((eyeY - waterLevel) + (head - waterLevel))
		glintX := t * x
		glintZ := spawnZ + t*(z-spawnZ)
		aimX := x + spotAimBias*(glintX-x)
		aimZ := z + spotAimBias*(glintZ-z)

		spots = append(spots, glyph.SpotLight{
			Pos:        mgl32.Vec3{x, head, z},
			Dir:        mgl32.Vec3{aimX - x, spotAimBias * (waterLevel - head), aimZ - z},
			Range:      spotRange,
			Color:      spotColor,
			Inner:      spotInner,
			Outer:      spotOuter,
			Volumetric: g.volumetric,
		})
		fixture(x, z, head, 0.18)
	}

	// -lampsoff builds every pile and every emissive marker and then hands the
	// scene nothing, which is the control `task waterlight` differences
	// against. Clearing the sets here rather than skipping the loops above is
	// the point: the two runs draw the same geometry, from the same heightmap
	// probes, in the same order, so the only thing that can move a pixel
	// between them is the light.
	if g.lampsOff {
		return nil
	}
	e.Scene.SetPointLights(lamps)
	e.Scene.SetSpotLights(spots)
	return nil
}

// flameConfig is a warm additive plume: fast rise, little gravity, and a life
// long enough to carry it a few units up. Bigger and slower than SparkConfig,
// which is a spark shower rather than a flame.
func flameConfig() *glyph.EmitterConfig {
	return &glyph.EmitterConfig{
		Spawn:        glyph.SpawnContinuous,
		SpawnRate:    110,
		MaxParticles: 380,

		LifeMin: 1.9,
		LifeMax: 2.6,

		SizeMin: 0.30,
		SizeMax: 0.55,

		RMin: 0.95, RMax: 1.0,
		GMin: 0.34, GMax: 0.62,
		BMin: 0.04, BMax: 0.14,

		Move:           glyph.MoveGravity,
		InitialSpeedXZ: 0.22,
		InitialSpeedY:  1.55,
		MaxSpeed:       3.0,
		TurbulenceXZ:   0.5,
		TurbulenceY:    0.2,
		Gravity:        0.32,

		Fade:        glyph.FadePlain,
		FadeInTime:  0.15,
		FadeOutTime: 0.9,
		AlphaMax:    0.55,

		Loop: true,
	}
}

// bubbleConfig is a slow pale drift that bounces off its own bounds, so it
// stays under the surface instead of breaking it.
func bubbleConfig() *glyph.EmitterConfig {
	return &glyph.EmitterConfig{
		Spawn:        glyph.SpawnContinuous,
		SpawnRate:    28,
		MaxParticles: 120,

		LifeMin: 3.0,
		LifeMax: 5.0,

		SizeMin: 0.10,
		SizeMax: 0.20,

		RMin: 0.75, RMax: 0.9,
		GMin: 0.9, GMax: 1.0,
		BMin: 0.9, BMax: 1.0,

		Move:           glyph.MoveDrift,
		InitialSpeedXZ: 0.08,
		InitialSpeedY:  0.35,
		MaxSpeed:       0.7,
		TurbulenceXZ:   0.2,
		TurbulenceY:    0.4,
		BounceAtBounds: true,

		Fade:        glyph.FadePlain,
		FadeInTime:  0.4,
		FadeOutTime: 1.2,
		AlphaMax:    0.8,

		Loop: true,
	}
}

func (g *game) Update(e *glyph.Engine, dt float32) {
	in := e.Input()

	// -pauseat stops the world at a chosen frame. Everything that moves on its
	// own here is driven by a different clock -- the waves and the shoreline
	// foam by the elapsed clock the shaders read, the sun by Scene.Tick -- so
	// two captures taken at different frames after the pause are identical only
	// if SetTimeScale stopped all of them.
	if g.pauseAt > 0 && e.FrameCount() >= g.pauseAt {
		e.SetTimeScale(0)
	}

	// -hud draws a block of identical HUD lines down the frame, crossing the
	// waterline. Every line is the same string on purpose: the lines are then
	// interchangeable, so any difference in what reaches the screen between one
	// row and the next is the renderer's doing and not the text's. `task hud`
	// measures exactly that. See docs/agents/overlay-composite.md.
	for i := 0; i < g.hud; i++ {
		e.Debugf("%s", hudLine)
	}

	// The emitters both feed one instance buffer, and the bubbles go in first
	// on purpose: the submerged half and the above-water half arrive
	// interleaved, so the renderer's split has to sort them out per instance
	// rather than per emitter. It has no idea there were two emitters.
	if g.flame != nil || g.bubbles != nil {
		night := e.Scene.StarVisibility()
		instances := make([]renderer.ParticleInstance, 0, effectParticles)
		if g.bubbles != nil {
			g.bubbles.Tick(dt, night)
			instances = append(instances, g.bubbles.BuildInstances(night)...)
		}
		if g.flame != nil {
			g.flame.Tick(dt, night)
			instances = append(instances, g.flame.BuildInstances(night)...)
		}
		if len(instances) > effectParticles {
			instances = instances[:effectParticles]
		}
		e.Renderer().UpdateParticleInstances(instances)
	}

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

	// The overlay carries a finished MVP, so it has to be rebuilt after the
	// camera moves -- which is here, not in Update.
	if g.markerMesh != nil {
		g.overlays[0].MVP = e.ViewProjection().Mul4(g.markerModel)
		e.SetOverlays(g.overlays)
	}
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
	shaftRadius := flag.Float64("shaftradius", 0, "light shaft reach in screen heights (0 keeps the default, 0.90)")
	shaftDecay := flag.Float64("shaftdecay", 0, "light shaft per-step decay in (0,1] (0 keeps the default, 0.96)")
	shaftLow := flag.Float64("shaftlow", 0, "lower edge of the shafts' brightness window, linear luminance (with -shafthigh; both 0 keeps the default 0.62..0.88)")
	shaftHigh := flag.Float64("shafthigh", 0, "upper edge of the shafts' brightness window")
	pillars := flag.Bool("pillars", false, "spawn pillars between the spawn point and the setting sun")
	pauseAt := flag.Int("pauseat", 0, "pause the simulation at frame N (0 = never); the world should stop dead")
	hud := flag.Int("hud", 0, "draw N identical HUD lines down the frame, for the `task hud` legibility check")
	bloom := flag.Float64("bloom", 0, "bloom intensity (0 disables)")
	bloomThreshold := flag.Float64("bloomthreshold", 1.2, "luminance bloom starts above; bloom.md's starting point is 1.2")
	tod := flag.Float64("time", -1, "freeze time of day in [0,1): 0=midnight, 0.25=sunrise, 0.5=noon, 0.75=sunset")
	shot := flag.String("screenshot", "", "write a PNG of the last frame to this path")
	plume := flag.Bool("plume", false, "additive flame on a stack in the shallows, crossing the waterline")
	ghost := flag.Bool("ghost", false, "translucent pane at the shoreline, crossing the waterline")
	marker := flag.Bool("marker", false, "world-space overlay disc over the water")
	submerged := flag.Bool("submerged", false, "translucent block and bubbles under the surface")
	lamps := flag.Int("lamps", 0, "warm point lamps on piles over the water and the shore (0 = none)")
	spots := flag.Int("spots", 0, "shore floodlights throwing cones across the surface (0 = none)")
	lampsOff := flag.Bool("lampsoff", false, "build the piles and fixtures but hand the scene no lights, as the control for `task waterlight`")
	volumetric := flag.Float64("volumetric", 0, "volumetric scattering intensity on every lamp and floodlight, so the air over the lake carries their light (0 = off, the default)")
	lampPosts := flag.Bool("lampposts", true, "draw the piles and bulb markers under the lamps (off measures the light loop against identical geometry)")
	alien := flag.Bool("alien", false, "a violet-and-amber sky palette instead of Earth's, through Scene.SetSkyPalette")
	lightDebug := flag.String("lightdebug", "", "light debug mode: heatmap or bruteforce (default: off)")
	uiGlow := flag.Bool("uiglow", false, "route the screen-space UI through its own HDR layer; nothing here asks to glow, so `task hud` uses it as the second path the HUD has to survive")
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
	if *uiGlow {
		// Nothing in this example emits, so the layer changes no colour here --
		// which is the point. `task hud` runs every one of its configurations
		// twice, because "the HUD survives water, bloom and the tonemap" is a
		// statement about a path, and there are two of them now.
		opts = append(opts, glyph.WithUIGlow())
	}

	e, err := glyph.New(&game{seed: *seed, refract: *refract, pitch: float32(*pitch), tod: float32(*tod), clouds: *clouds, stars: *stars, milkyway: *milkyway, band: *band, fogHeight: float32(*fogHeight), yaw: float32(*yaw), shafts: float32(*shafts), shaftShape: glyph.LightShaftShape{Radius: float32(*shaftRadius), Decay: float32(*shaftDecay), Threshold: [2]float32{float32(*shaftLow), float32(*shaftHigh)}}, pillars: *pillars, pauseAt: *pauseAt, hud: *hud, bloom: float32(*bloom), bloomThres: float32(*bloomThreshold), plume: *plume, ghost: *ghost, marker: *marker, submerged: *submerged, lampCount: *lamps, spotCount: *spots, lampsOff: *lampsOff, volumetric: float32(*volumetric), lampPosts: *lampPosts, alien: *alien}, opts...)
	if err != nil {
		log.Fatalf("create engine: %v", err)
	}
	defer e.Destroy()

	switch *lightDebug {
	case "heatmap":
		e.SetLightDebugMode(glyph.LightDebugHeatmap)
	case "bruteforce":
		e.SetLightDebugMode(glyph.LightDebugBruteForce)
	case "":
		// default: off
	default:
		log.Fatalf("unknown -lightdebug %q, want heatmap or bruteforce", *lightDebug)
	}

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
