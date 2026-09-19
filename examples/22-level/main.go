// Command 22-level loads a small settlement from ONE glTF file and shows the
// per-node placement pattern issue #66 exists for: a level authored in an
// editor -- here, a file shaped like what Blender's exporter actually
// produces, see examples/22-level/gen -- carries its own node graph, and a
// game spawns one entity per mesh node at that node's own World transform
// rather than treating the file as a single prop.
//
// What it reads off the file, per node:
//
//   - Geometry and placement: Model.NodeMeshes(i) for which primitives a
//     node instances (several nodes here share ONE doc mesh -- the four
//     buildings, the four lamp posts -- which is exactly the case
//     ModelMesh.Node cannot answer), and the node's World, decomposed into
//     the engine's Transform. Building 1 carries a real rotation together
//     with a non-uniform scale (1, 1.5, 2) on purpose: get the decomposition
//     order wrong and it is the one node in the scene that visibly skews.
//   - extras: `{"static": true, "collider": "box", "floors": N}` on the
//     ground and buildings, `{"spawn": "player"}` on an otherwise-empty
//     node. The engine never looks inside Model.Nodes[i].Extras -- this file
//     defines what those keys mean and reads them itself.
//   - KHR_lights_punctual: every lamp post's spot light and the plaza's
//     point light, fed to Scene.SetSpotLights/SetPointLights after this file
//     picks an intensity -> Color scale and a range (see intensityScale and
//     defaultLampRange below) -- the engine's lights carry no photometric
//     unit and no notion of "unbounded", so a game has to decide both.
//
// It runs at night, same as 21-streetlights, so the lamp posts are doing the
// work rather than competing with daylight.
//
//	go run ./22-level                    # windowed, drag-orbit camera
//	go run ./22-level -frames 200         # render 200 frames, then exit
//	go run ./22-level -screenshot out.png # capture the last frame
//
// Left-drag orbits, scroll zooms, Escape quits.
package main

import (
	"embed"
	"encoding/json"
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
	// intensityScale converts a glTF punctual light's raw candela/lux
	// Intensity into the engine's unitless Color multiplier.
	// glyphengine.PointLight/SpotLight carry no photometric unit at all
	// (docs/agents/lights.md) -- intensity rides entirely in Color, exactly
	// as a hand-placed light already works, so loading a level means
	// deciding how bright "54351 candela" should look. This divisor is
	// chosen, not derived: it puts a real Blender 1000 W spot (54351.4 cd,
	// see gen/main.go's package comment for where that number comes from)
	// at Color scale ~2.72, close to 21-streetlights' hand-tuned porch
	// light (its spotColor is scaled by 2.8). A different art style wants a
	// different number; there is no physically correct one to check this
	// against.
	intensityScale = 20000.0

	// defaultLampRange is the finite range this example supplies for every
	// light whose ModelLight.Range is 0. That is the NORMAL case for a
	// Blender-authored level, not a corner case: Blender's glTF exporter
	// never writes "range" at all, so every light in this file (and in any
	// other Blender export) arrives unbounded. glyphengine's lights have no
	// notion of "unbounded", so a game MUST pick a number; this one is
	// chosen visually for this scene's scale (lamp posts roughly 20m apart)
	// rather than derived from intensity.
	defaultLampRange = 14.0
)

// nodeTags is this EXAMPLE's own vocabulary for node extras -- the engine
// never interprets Model.Nodes[i].Extras (AGENTS.md rule 14), so what
// "static", "collider" and "spawn" mean is entirely up to whatever reads
// them, which here is this struct.
type nodeTags struct {
	Static   bool    `json:"static"`
	Collider string  `json:"collider"`
	Floors   float64 `json:"floors"`
	Spawn    string  `json:"spawn"`
}

type game struct {
	camera *glyph.Camera

	camDist, camPitch, camYaw, camLook float32
}

func (g *game) Init(e *glyph.Engine) error {
	r := e.Renderer()
	model, err := r.LoadGLTF(assetsFS, "assets/level.glb")
	if err != nil {
		return fmt.Errorf("load level.glb: %w", err)
	}

	spawnTarget := spawnLevel(e, model)
	e.RebuildStatics()

	spots, points := lightsFromModel(model)
	e.Scene.SetSpotLights(spots)
	e.Scene.SetPointLights(points)

	// Night, so the lamps are the light -- the same setup 21-streetlights
	// uses, for the same reason: a warm pool under each fixture reads
	// clearly against a dim sky, where at midday it would not read at all.
	env := glyph.DefaultEnvironment()
	env.Sky.CloudSteps = glyph.CloudsOff
	env.Sky.MilkyWay = 0.5
	env.Fog = &glyph.Fog{Density: 0.006, Height: 6}
	e.Scene.Env = env
	e.SetTimeOfDay(0.03)
	e.SetDayCycleSpeed(0)

	g.camera = glyph.NewCamera(g.camDist)
	g.camera.Pitch = g.camPitch
	g.camera.Yaw = g.camYaw
	g.camera.LookOffset = g.camLook
	g.camera.Target = spawnTarget

	log.Printf("22-level running: %d nodes, %d meshes, %d spot lights, %d point lights",
		len(model.Nodes), len(model.Meshes), len(spots), len(points))
	return nil
}

func (g *game) Update(e *glyph.Engine, dt float32) {
	in := e.Input()
	if in.KeyPressed(input.KeyEscape) {
		e.Close()
	}
	g.camera.Update(in)
	g.camera.ResolveCollision(e.Scene, 0, dt)
	e.SetCamera(g.camera.ViewVectors())
}

// spawnLevel is the per-node placement pattern this example exists to show:
// one entity per node that instances a mesh, at that node's own World
// transform, decoded through the node's own extras rather than a naming
// convention. It returns the world position of the node tagged
// {"spawn": "player"} in its extras, 1.6m up (eye height) for the camera to
// target -- falling back to the origin, logged, if the level carries none.
func spawnLevel(e *glyph.Engine, model *renderer.Model) mgl32.Vec3 {
	spawnTarget := mgl32.Vec3{0, 1.6, 0}
	foundSpawn := false

	for i := range model.Nodes {
		node := model.Nodes[i]

		var tags nodeTags
		if len(node.Extras) > 0 {
			if err := json.Unmarshal(node.Extras, &tags); err != nil {
				log.Printf("level: node %q: malformed extras, ignoring: %v", node.Name, err)
			}
		}
		if tags.Spawn == "player" {
			pos := node.World.Mul4x1(mgl32.Vec4{0, 0, 0, 1}).Vec3()
			spawnTarget = mgl32.Vec3{pos.X(), pos.Y() + 1.6, pos.Z()}
			foundSpawn = true
		}

		meshIdxs := model.NodeMeshes(i)
		if len(meshIdxs) == 0 {
			continue // an empty node: a light socket, or the spawn marker above
		}

		pos, rot, scale := decomposeWorld(node.World)
		transform := &glyph.Transform{Position: pos, Rotation: rot, Scale: scale}

		ent := e.Spawn()
		e.C.Transform.Set(ent, transform)
		spawnPrimitive(e, ent, model.Meshes[meshIdxs[0]])

		// A doc mesh CAN split into more than one primitive (one per
		// material); level.glb never does, but a level file in general
		// might, and spawning every one of them at the same node transform
		// keeps this correct rather than silently dropping geometry. They
		// ride as their own entities since MeshRef only ever holds one mesh.
		for _, mi := range meshIdxs[1:] {
			extra := e.Spawn()
			e.C.Transform.Set(extra, transform)
			spawnPrimitive(e, extra, model.Meshes[mi])
		}

		if tags.Static {
			e.C.Static.Set(ent, &glyph.Static{})
		}
		if tags.Collider == "box" {
			if half, ok := meshesLocalHalfExtent(model, meshIdxs); ok {
				e.C.Collider.Set(ent, &glyph.Collider{HalfExtents: half})
			}
		}
		if tags.Floors > 0 {
			// Proves extras carries more than strings and bools through to
			// a game -- floors is not otherwise used by anything here.
			log.Printf("level: %q has %d floors (from extras, engine does not use this)", node.Name, int(tags.Floors))
		}
	}

	if !foundSpawn {
		log.Printf("level: no node with extras {\"spawn\":\"player\"} found; camera targets the origin")
	}
	return spawnTarget
}

// spawnPrimitive sets the render-facing components for one ModelMesh on an
// already-Transform'd entity: the GPU mesh plus its material factors. No
// texture or Material maps exist in this level (see gen/main.go), so Color
// plus MeshRef's own Metallic/Roughness is the whole story -- the same
// untextured path 21-streetlights' door, roof and fixture use.
func spawnPrimitive(e *glyph.Engine, ent glyph.Entity, mm renderer.ModelMesh) {
	e.C.MeshRef.Set(ent, &glyph.MeshRef{Mesh: mm.Mesh, Metallic: mm.Metallic, Roughness: mm.Roughness})
	e.C.Color.Set(ent, &glyph.Color{R: mm.BaseColor[0], G: mm.BaseColor[1], B: mm.BaseColor[2]})
}

// meshesLocalHalfExtent returns a box collider's half-extents in the node's
// own local space: max(|min|, |max|) per axis over every vertex in meshIdxs,
// which is symmetric about the node's origin rather than the mesh's own
// (possibly off-centre) bounding box.
//
// glyphengine.Collider carries only HalfExtents, no centre offset (see
// physics.go's WorldAABB: Position +/- HalfExtents*Scale) -- and this
// level's building and lamp-post meshes are NOT centred on their node's
// origin, since that is where an editor naturally puts a box standing on
// the ground: local Y runs 0..height, not -height/2..height/2. Centring the
// collider on the node's origin with a symmetric half-extent is the
// smallest box that is guaranteed to CONTAIN the real one without an
// offset field the engine does not have. The cost is real: a building's
// collider then reaches as far underground as the building is tall. That is
// harmless in this example -- nothing tests collision against it from
// below -- and is reported honestly rather than worked around by adding an
// offset to Collider, which issue #66 does not ask for.
func meshesLocalHalfExtent(model *renderer.Model, meshIdxs []int) (mgl32.Vec3, bool) {
	min := mgl32.Vec3{math.MaxFloat32, math.MaxFloat32, math.MaxFloat32}
	max := mgl32.Vec3{-math.MaxFloat32, -math.MaxFloat32, -math.MaxFloat32}
	found := false
	for _, mi := range meshIdxs {
		for _, v := range model.Meshes[mi].Verts {
			for a := 0; a < 3; a++ {
				if v.Pos[a] < min[a] {
					min[a] = v.Pos[a]
				}
				if v.Pos[a] > max[a] {
					max[a] = v.Pos[a]
				}
			}
			found = true
		}
	}
	if !found {
		return mgl32.Vec3{}, false
	}
	return mgl32.Vec3{
		maxf(absf(min[0]), absf(max[0])),
		maxf(absf(min[1]), absf(max[1])),
		maxf(absf(min[2]), absf(max[2])),
	}, true
}

// decomposeWorld splits a glTF node's World matrix into the position, Euler
// rotation and scale glyphengine.Transform expects.
//
// This is the classic bug in a per-node spawn loop, which is why
// level.glb's Building1 exists: Transform.ModelMatrix composes
// Translate * RotY(y) * RotX(x) * RotZ(z) * Scale (see its own doc comment)
// -- a specific order -- so reading (rotation, scale) back out of a World
// matrix that carries a rotation TOGETHER WITH a non-uniform scale has to
// remove the scale from the rotation part first, and then extract the Euler
// angles against that SAME Y-X-Z product, or the two disagree and the mesh
// comes out visibly skewed. Building1's scale is (1, 1.5, 2), not uniform,
// specifically so a wrong decomposition here is not academic.
func decomposeWorld(w mgl32.Mat4) (pos, eulerXYZ, scale mgl32.Vec3) {
	pos = mgl32.Vec3{w[12], w[13], w[14]}

	sx, sy, sz := mgl32.Extract3DScale(w)
	scale = mgl32.Vec3{sx, sy, sz}

	// Divide the scale back out of each basis column to leave a pure
	// rotation matrix. r[row][col], matching the hand-derived Y-X-Z product
	// below.
	var r [3][3]float32
	if sx != 0 {
		r[0][0], r[1][0], r[2][0] = w[0]/sx, w[1]/sx, w[2]/sx
	}
	if sy != 0 {
		r[0][1], r[1][1], r[2][1] = w[4]/sy, w[5]/sy, w[6]/sy
	}
	if sz != 0 {
		r[0][2], r[1][2], r[2][2] = w[8]/sz, w[9]/sz, w[10]/sz
	}

	// Ry(y) * Rx(x) * Rz(z), worked out by hand (not borrowed from a
	// library that might assume a different order) so it matches
	// ModelMatrix's own composition exactly:
	//   r[1][2] = -sin(x)
	//   r[1][0] =  cos(x)*sin(z),  r[1][1] = cos(x)*cos(z)
	//   r[0][2] =  cos(x)*sin(y),  r[2][2] = cos(x)*cos(y)
	x := float32(math.Asin(float64(clamp32(-r[1][2], -1, 1))))
	cx := float32(math.Cos(float64(x)))
	var y, z float32
	if cx > 1e-6 {
		z = float32(math.Atan2(float64(r[1][0]), float64(r[1][1])))
		y = float32(math.Atan2(float64(r[0][2]), float64(r[2][2])))
	} else {
		// Gimbal lock (x at +/-90deg): y and z rotate about the same world
		// axis there, so only their sum/difference is determined. None of
		// this level's nodes hit this branch -- it exists so a future one
		// that does gets a defined answer instead of atan2(0,0)'s NaN-ish
		// noise.
		y = float32(math.Atan2(float64(-r[2][0]), float64(r[0][0])))
		z = 0
	}
	return pos, mgl32.Vec3{x, y, z}, scale
}

func clamp32(v, lo, hi float32) float32 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func absf(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}

func maxf(a, b float32) float32 {
	if a > b {
		return a
	}
	return b
}

// lightsFromModel converts every KHR_lights_punctual light the file carries
// into the engine's own light types, applying intensityScale and
// defaultLampRange -- see their doc comments for why those numbers and not
// others. Directional lights are not converted: this level has none, and
// the engine's only directional light is the sun (Scene.SetTimeOfDay), which
// is a different mechanism this example does not attempt to unify with a
// glTF directional light.
func lightsFromModel(model *renderer.Model) (spots []glyph.SpotLight, points []glyph.PointLight) {
	for _, l := range model.Lights {
		pos, dir := model.LightWorldPosDir(l)
		rng := l.Range
		if rng == 0 {
			rng = defaultLampRange
		}
		color := mgl32.Vec3{l.Color[0], l.Color[1], l.Color[2]}.Mul(l.Intensity / intensityScale)

		switch l.Kind {
		case renderer.LightKindSpot:
			spots = append(spots, glyph.SpotLight{
				Pos: pos, Dir: dir, Range: rng, Color: color,
				Inner: l.InnerCone, Outer: l.OuterCone,
			})
		case renderer.LightKindPoint:
			points = append(points, glyph.PointLight{Pos: pos, Range: rng, Color: color})
		default:
			log.Printf("level: light %q is %v, which this example does not place", l.Name, l.Kind)
		}
	}
	return spots, points
}

func main() {
	width := flag.Int("width", 1280, "window width")
	height := flag.Int("height", 720, "window height")
	fullscreen := flag.Bool("fullscreen", false, "run fullscreen on the primary monitor")
	frames := flag.Int("frames", 0, "render N frames then exit (0 = run until closed)")
	shot := flag.String("screenshot", "", "write a PNG of the last frame to this path")
	camDist := flag.Float64("camdist", 26, "camera orbit distance from the spawn point")
	camPitch := flag.Float64("campitch", 0.45, "camera pitch in radians")
	camYaw := flag.Float64("camyaw", 3.14159, "camera yaw in radians (pi looks from behind spawn toward the plaza, +Z)")
	camLook := flag.Float64("camlook", 2.5, "how far above the target the camera looks")
	flag.Parse()

	opts := []glyph.Option{
		glyph.WithTitle("GlyphEngine - 22 Level"),
		glyph.WithWindowSize(*width, *height),
		glyph.WithMSAA(4),
		glyph.WithDebugKeys(),
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

	g := &game{
		camDist:  float32(*camDist),
		camPitch: float32(*camPitch),
		camYaw:   float32(*camYaw),
		camLook:  float32(*camLook),
	}
	e, err := glyph.New(g, opts...)
	if err != nil {
		log.Fatalf("create engine: %v", err)
	}
	defer e.Destroy()

	e.Run()
	log.Printf("rendered %d frames", e.FrameCount())
}
