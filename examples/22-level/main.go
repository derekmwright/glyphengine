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
//     ModelMesh.Node cannot answer), and the node's World, which
//     glyph.TransformFromMatrix turns into the engine's Transform. Building 1
//     carries a real rotation together with a non-uniform scale (1, 1.5, 2)
//     on purpose: it is the node that visibly skews if that goes wrong.
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
//	go run ./22-level -reload 20          # swap the level for a fresh load, 20 times
//	go run ./22-level -instanced          # batch repeated static props into InstanceSets
//
// -reload is the Blender iteration story with the file changing underneath it
// taken out: it loads a SECOND copy of the level, spawns it, and only then
// gives the first one back with Renderer.DestroyModel -- the swap happening
// inside one tick, so no frame is ever drawn without a level. That is both the
// only reload a game would ship and the harder case for DestroyModel: at the
// moment of the swap, frames still in flight reference the buffers being
// released. A never-destroyed second model sits beside it as the control.
// Run it under the validation layer (task validate does) -- the layer is what
// would notice a resource freed while a frame still referenced it, and the
// resource counts this asserts on are what notice one never freed at all.
//
// -instanced is issue #71's recipe: turning "several nodes, one doc mesh" --
// which Model.NodeMeshes already surfaces, see above -- into ONE
// renderer.InstanceSet instead of N MeshRef entities, using
// Model.MeshInstances to go the other way, from a doc mesh back to every
// node placing it. WHICH doc meshes qualify is this example's own decision,
// not the engine's (AGENTS.md rule 14): a doc mesh instances as a set only
// when every node sharing it is tagged {"static": true} (a moving node can't
// safely alias one set's fixed placements, and the level's own vocabulary
// already has a word for "never moves") and none of its primitives is
// alphaMode BLEND (InstancedMesh has no blended pipeline -- see
// docs/agents/instancing.md -- so a translucent shared mesh stays on the
// individual path rather than silently losing its translucency). A node
// still tagged {"collider": "box"} keeps its collider through a companion
// entity that carries Transform/Collider/Static and no MeshRef -- the
// InstanceSet already drew its geometry once, in the shared draw call, so a
// second MeshRef here would draw it twice. What that companion entity does
// NOT get back is everything else an ordinary level entity has: no per-
// instance picking, no way for the game to look this one building up and
// drive it, no component beyond the physical box. See
// spawnInstancedGroup/instancedGroupCandidate below and
// docs/agents/instancing.md for the measurement this rule was chosen to
// demonstrate. -instanced and -reload do not combine -- nothing releases an
// InstanceSet's buffer yet (see renderer/instancedmesh.go), so reloading
// under -instanced would leak one per swap; main() refuses the combination
// rather than doing that silently.
//
// Left-drag orbits, scroll zooms, Escape quits.
package main

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"math"
	"os"
	"path/filepath"
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

	// levelPath is a glTF on disk to load instead of the embedded level.glb:
	// the way to look at a file exported from Blender without rebuilding.
	levelPath string

	// instanced turns on -instanced's recipe in spawnLevel: see the package
	// comment and instancedGroupCandidate for the rule.
	instanced bool

	// model and levelEntities are what the current load of the level
	// produced. Nothing reads them after Init with -reload 0; they exist
	// because -reload has to give them back.
	model         *renderer.Model
	levelEntities []glyph.Entity

	// anchor is a SECOND model, loaded once under -reload and never
	// destroyed. It is the control: it must keep drawing exactly the same
	// before, during and after every reload of the model beside it. See
	// loadAnchor.
	anchor *renderer.Model

	// -reload state. See stepReload.
	reloadCycles int
	cyclesLeft   int
	phaseFrame   int
	haveSteady   bool
	steadyCounts renderer.ResourceCounts
	oneLevel     renderer.ResourceCounts
}

// reloadCycleFrames is how many frames -reload draws between swaps.
//
// It has to exceed the frames in flight (two) that a deferred destroy waits
// out, because stepReload's checks are only meaningful once the previous
// cycle release has actually run. Four leaves slack without making the loop
// slow.
const reloadCycleFrames = 4

func (g *game) Init(e *glyph.Engine) error {
	if g.reloadCycles > 0 {
		if err := g.loadAnchor(e); err != nil {
			return err
		}
	}
	spawnTarget, err := g.loadLevel(e)
	if err != nil {
		return err
	}

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

	spots, points := lightsFromModel(g.model)
	log.Printf("22-level running: %d nodes, %d meshes, %d spot lights, %d point lights",
		len(g.model.Nodes), len(g.model.Meshes), len(spots), len(points))
	return nil
}

// openLevel opens the level file this run was pointed at.
func (g *game) openLevel(r *renderer.Renderer) (*renderer.Model, error) {
	var levelFS fs.FS = assetsFS
	name := "assets/level.glb"
	if g.levelPath != "" {
		// A .gltf keeps its .bin and its textures beside it, so the file's own
		// directory is the filesystem it has to be opened from.
		levelFS, name = os.DirFS(filepath.Dir(g.levelPath)), filepath.Base(g.levelPath)
	}
	model, err := r.LoadGLTF(levelFS, name)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", name, err)
	}
	return model, nil
}

// loadLevel is Init's level half, split out so -reload can run it again. With
// -reload 0 it runs exactly once and nothing about this example changed.
func (g *game) loadLevel(e *glyph.Engine) (mgl32.Vec3, error) {
	model, err := g.openLevel(e.Renderer())
	if err != nil {
		return mgl32.Vec3{}, err
	}
	g.model = model

	spawnTarget, entities := spawnLevel(e, model, g.instanced)
	g.levelEntities = entities
	e.RebuildStatics()

	spots, points := lightsFromModel(model)
	e.Scene.SetSpotLights(spots)
	e.Scene.SetPointLights(points)

	return spawnTarget, nil
}

// loadAnchor loads the -reload control: a second Model from the same file, one
// of whose primitives is drawn for the whole run and never destroyed.
//
// It exists to answer the one question the reloading model cannot answer about
// itself -- does releasing one model disturb another? DestroyModel goes
// through DestroyMesh and DestroyTexture, which deregister from the renderer's
// own r.meshes/r.textures tracking lists by SCANNING them for a pointer match,
// and a bug there (removing the wrong entry, leaving a stale one behind for
// Renderer.Destroy to free a second time) shows up as some OTHER model's
// buffers going missing, not as the destroyed one's.
//
// It has to be DRAWN to be a control, which is not automatic. The first
// version of this spawned a whole second copy of the level 34 units to one
// side, and the state trace showed the frame's draw count unchanged at 7 --
// every entity of it was frustum-culled, so it proved nothing. One primitive,
// parked above the spawn point the camera is already aimed at, is in frame on
// every run, and `task reload` asserts the count went UP for exactly this
// reason.
func (g *game) loadAnchor(e *glyph.Engine) error {
	model, err := g.openLevel(e.Renderer())
	if err != nil {
		return fmt.Errorf("anchor: %w", err)
	}
	g.anchor = model
	if len(model.Meshes) == 0 {
		return fmt.Errorf("anchor: the level carries no primitives to use as one")
	}

	// The last primitive: a prop or a light fixture in both level files this
	// example is run against, rather than the ground slab primitive 0 is.
	ent := e.Spawn()
	tr := glyph.Transform{Position: mgl32.Vec3{0, 9, 8}, Scale: mgl32.Vec3{1, 1, 1}}
	e.C.Transform.Set(ent, &tr)
	spawnPrimitive(e, ent, model.Meshes[len(model.Meshes)-1])
	return nil
}

// releaseLevel gives one load of the level back: the entities that draw it
// first, then the model.
//
// That order is the contract, not a preference. DestroyModel nils every
// ModelMesh's Mesh, Texture and Material, so an entity still holding a MeshRef
// to one of them would be drawing a handle nothing owns; see
// docs/agents/models.md for what that actually costs. Despawning first is how
// a game avoids it. The model's own release is then safe against the frames
// already submitted, because DestroyModel defers it rather than freeing now.
func (g *game) releaseLevel(e *glyph.Engine, model *renderer.Model, entities []glyph.Entity) {
	for _, ent := range entities {
		e.Scene.Despawn(ent)
	}
	e.RebuildStatics()
	e.Renderer().DestroyModel(model)
}

// stepReload drives the -reload loop, and the SHAPE of it is the point: load
// the new level, spawn it, and only then release the old one, all inside one
// tick.
//
// Doing it the other way round -- release, then load on a later frame -- was
// the first version of this, and watching it run is what condemned it: the
// level vanishes for a few frames every cycle. That is not cosmetic. This loop
// stands in for the real use, an artist re-exporting from Blender and the game
// picking the file up, and a reload that blinks the world out is not a
// reload anyone would ship. Swapping inside one tick is also the harder case
// for DestroyModel, not the easier one: at the moment of the swap the frames
// still in flight reference the OLD buffers, which is exactly what its
// deferral exists for, and what DestroyMesh's and DestroyTexture's immediacy
// would turn into a use-after-free.
//
// The counts are what make this more than a smoke test. CLAUDE.md records a
// teardown check that reported zero leaks because teardown never ran, so
// nothing here is assumed:
//
//   - Deferred must be 0 at the top of each cycle: the PREVIOUS cycle's
//     release actually ran.
//   - Every cycle must start from the same steady counts. This is the one that
//     catches a DestroyModel that does nothing: each cycle loads a whole extra
//     level, so if the old one were never released the counts would climb by
//     one level per cycle and this fires on cycle two.
//   - The counts must not drop in the tick the swap happens: DestroyModel must
//     defer, not free now.
//   - The release has to be worth something at all -- one level's worth of
//     meshes has to be non-zero, or none of the above proves anything.
//
// The loop deliberately ends with the level LOADED rather than tearing it down
// first. A final teardown would make the last few frames draw no level, which
// is the one thing `task reload` asserts never happens, and it would prove
// nothing the per-cycle steady check has not already proved twenty times over.
// Renderer.Destroy takes the live model at shutdown, which is the other path
// worth having under the layer anyway.
func (g *game) stepReload(e *glyph.Engine) {
	r := e.Renderer()
	g.phaseFrame++
	if g.phaseFrame < reloadCycleFrames {
		return
	}
	g.phaseFrame = 0

	steady := r.ResourceCounts()
	if steady.Deferred != 0 {
		log.Fatalf("-reload: %d deferred destroys still queued %d frames after the last swap; the previous release never ran", steady.Deferred, reloadCycleFrames)
	}
	if g.haveSteady && !sameResources(steady, g.steadyCounts) {
		log.Fatalf("-reload: the renderer tracks %+v at the top of this cycle, want %+v -- a reload is accumulating or losing resources", steady, g.steadyCounts)
	}
	g.steadyCounts, g.haveSteady = steady, true

	if g.cyclesLeft == 0 {
		log.Printf("-reload: %d swaps done; one level is %d meshes, %d textures, %d materials, and the renderer tracked the same %d/%d/%d at the top of every cycle",
			g.reloadCycles, g.oneLevel.Meshes, g.oneLevel.Textures, g.oneLevel.Materials,
			steady.Meshes, steady.Textures, steady.Materials)
		e.Close()
		return
	}
	g.cyclesLeft--

	oldModel, oldEntities := g.model, g.levelEntities

	// The swap. New first.
	if _, err := g.loadLevel(e); err != nil {
		log.Fatalf("-reload: reloading the level: %v", err)
	}
	both := r.ResourceCounts()
	if g.oneLevel.Meshes == 0 {
		g.oneLevel = subResources(both, steady)
		if g.oneLevel.Meshes <= 0 {
			log.Fatalf("-reload: loading a second copy of the level added %d meshes; this check proves nothing unless it adds some", g.oneLevel.Meshes)
		}
	}

	// Old second, in the same tick, so no frame is ever drawn without a level.
	g.releaseLevel(e, oldModel, oldEntities)

	if after := r.ResourceCounts(); !sameResources(after, both) {
		log.Fatalf("-reload: DestroyModel freed immediately -- the renderer tracked %+v before it and %+v after, in the same tick that frames in flight still reference those buffers", both, after)
	}
}

func sameResources(a, b renderer.ResourceCounts) bool {
	return a.Meshes == b.Meshes && a.Textures == b.Textures && a.Materials == b.Materials
}

func sumResources(a, b renderer.ResourceCounts) renderer.ResourceCounts {
	return renderer.ResourceCounts{Meshes: a.Meshes + b.Meshes, Textures: a.Textures + b.Textures, Materials: a.Materials + b.Materials}
}

func subResources(a, b renderer.ResourceCounts) renderer.ResourceCounts {
	return renderer.ResourceCounts{Meshes: a.Meshes - b.Meshes, Textures: a.Textures - b.Textures, Materials: a.Materials - b.Materials}
}

func (g *game) Update(e *glyph.Engine, dt float32) {
	in := e.Input()
	if in.KeyPressed(input.KeyEscape) {
		e.Close()
	}
	g.camera.Update(in)
	g.camera.ResolveCollision(e.Scene, 0, dt)
	e.SetCamera(g.camera.ViewVectors())

	if g.reloadCycles > 0 {
		g.stepReload(e)
	}
}

// spawnLevel is the per-node placement pattern this example exists to show:
// one entity per node that instances a mesh, at that node's own World
// transform, decoded through the node's own extras rather than a naming
// convention. It returns the world position of the node tagged
// {"spawn": "player"} in its extras, 1.6m up (eye height) for the camera to
// target -- falling back to the origin, logged, if the level carries none --
// and every entity it spawned.
//
// The entity list is what -reload despawns before handing the model back.
// Tracking it is the level loader's job, not the engine's: nothing in the ECS
// records which entities came from which file, and a game that reloads a level
// has to know which of its entities were the level.
//
// instanced turns on issue #71's recipe: see the package comment and
// instancedGroupCandidate for the rule that decides which doc meshes qualify.
// instancedDocMesh caches that decision per doc mesh (asked once, the first
// time a node naming it is reached, not once per node -- the answer is the
// same for every node sharing a mesh by construction) and spawnedInstanceSet
// records which doc meshes already got their one InstanceSet, so the SECOND
// and later nodes in a group are skipped for the shared draw while still
// getting their own pass through the extras below (a companion collider,
// the floors log line).
func spawnLevel(e *glyph.Engine, model *renderer.Model, instanced bool) (mgl32.Vec3, []glyph.Entity) {
	spawnTarget := mgl32.Vec3{0, 1.6, 0}
	foundSpawn := false
	var entities []glyph.Entity

	instancedDocMesh := map[int]bool{}
	spawnedInstanceSet := map[int]bool{}

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

		if instanced {
			decided, asked := instancedDocMesh[node.Mesh]
			if !asked {
				decided = instancedGroupCandidate(model, node.Mesh)
				instancedDocMesh[node.Mesh] = decided
			}
			if decided {
				if !spawnedInstanceSet[node.Mesh] {
					spawnedInstanceSet[node.Mesh] = true
					entities = append(entities, spawnInstancedGroup(e, model, node.Mesh, meshIdxs)...)
				}
				// The set already drew this node's geometry once, in the
				// shared draw call above -- no per-node MeshRef here, or it
				// would draw twice. A collider is the one thing this node
				// still needs an entity of its own for; see
				// spawnInstancedCollider's doc comment for why.
				if tags.Collider == "box" {
					entities = append(entities, spawnInstancedCollider(e, model, node, meshIdxs))
				}
				if tags.Floors > 0 {
					log.Printf("level: %q has %d floors (from extras, engine does not use this)", node.Name, int(tags.Floors))
				}
				continue
			}
		}

		// The node's World is a matrix and an entity's Transform is position,
		// Euler angles and scale in the engine's own rotation order, so the
		// engine takes it apart. It says so when it cannot: a rotated object
		// inside a non-uniformly scaled parent is sheared in world space, and
		// no Transform can hold that. Applying the parent's scale in the editor
		// before exporting is the fix, and the node's name is what finds it.
		transform, exact := glyph.TransformFromMatrix(node.World)
		if !exact {
			log.Printf("level: node %q is sheared or has a zero scale; it is drawn without that part of its transform", node.Name)
		}
		ent := e.Spawn()
		entities = append(entities, ent)
		e.C.Transform.Set(ent, &transform)
		spawnPrimitive(e, ent, model.Meshes[meshIdxs[0]])

		// A doc mesh CAN split into more than one primitive (one per
		// material); level.glb never does, but a level file in general
		// might, and spawning every one of them at the same node transform
		// keeps this correct rather than silently dropping geometry. They
		// ride as their own entities since MeshRef only ever holds one mesh.
		//
		// Each gets its OWN Transform. The component store keeps the pointer it
		// is handed, so giving two entities the same one makes them one object
		// as far as anything that moves or interpolates them is concerned.
		for _, mi := range meshIdxs[1:] {
			extra := e.Spawn()
			entities = append(entities, extra)
			own := transform
			e.C.Transform.Set(extra, &own)
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
	return spawnTarget, entities
}

// instancedGroupCandidate is -instanced's whole rule, in one place: does
// every node instancing docMesh get merged into one InstanceSet, or does the
// doc mesh stay on the ordinary per-node path?
//
//   - Shared: model.MeshInstances(docMesh) has to name more than one node, or
//     there is nothing to batch.
//   - Every node sharing it is tagged {"static": true}. A set's placements
//     only change through Renderer.UpdateInstanceSet, which nothing here
//     drives per frame, so a node that might move cannot safely be one of
//     them -- and "static" is already this level's own word for "never
//     moves" (glyph.Static's doc comment), not a second vocabulary invented
//     for this. A doc mesh shared by a mix of static and non-static nodes
//     stays entirely on the individual path rather than splitting the group,
//     which would need its own rule for something neither fixture needs.
//   - None of the doc mesh's primitives is alphaMode BLEND.
//     docs/agents/instancing.md is explicit that InstancedMesh has no
//     blended pipeline and stays opaque rather than losing the placements,
//     so a level that instanced a glass pane would silently lose its
//     translucency; excluding it here keeps that failure from happening
//     rather than documenting it after the fact.
//
// What this does NOT gate on: shear. MeshInstance.Model is a full 4x4
// matrix, not a Position/Rotation/Scale triple, so a sheared node's World
// carries across to an InstanceSet placement exactly -- unlike the
// individual path just above, which has to fit it through
// glyph.TransformFromMatrix and drops the shear when that comes back
// inexact. Neither level file this example loads has a sheared node sharing
// a mesh with anything else, so this is not exercised by a render here, but
// it is why no exactness check appears in this function: TransformFromMatrix
// is only reached below, for a collider companion entity's OWN Transform,
// and a sheared collider-tagged instanced node loses exactly the part of its
// transform the individual path always has.
func instancedGroupCandidate(model *renderer.Model, docMesh int) bool {
	nodes := model.MeshInstances(docMesh)
	if len(nodes) < 2 {
		return false
	}
	for _, ni := range nodes {
		var tags nodeTags
		if len(model.Nodes[ni].Extras) == 0 {
			return false
		}
		if err := json.Unmarshal(model.Nodes[ni].Extras, &tags); err != nil || !tags.Static {
			return false
		}
	}
	for _, mi := range model.NodeMeshes(nodes[0]) {
		if model.Meshes[mi].AlphaMode == renderer.AlphaModeBlend {
			return false
		}
	}
	return true
}

// spawnInstancedGroup builds and spawns one InstanceSet per primitive
// docMesh split into (level.glb never splits one, but the individual path
// above handles it and this mirrors that rather than silently dropping
// geometry a level file in general might have), with one MeshInstance per
// node in model.MeshInstances(docMesh).
//
// The placement matrix is the node's raw World, not a Transform run back
// through ModelMatrix -- see instancedGroupCandidate's doc comment on why
// that is the more faithful choice here, not a shortcut. Tint is left at
// white: MeshInstance.Tint varies ONE placement from its neighbours, which
// nothing about this level's extras asks for, and Color on the entity (set
// below from the primitive's own BaseColor, the same source spawnPrimitive
// reads) already tints the whole set identically to how each node would
// have been tinted individually, since every node in a candidate group
// shares the one doc mesh and therefore the one material.
func spawnInstancedGroup(e *glyph.Engine, model *renderer.Model, docMesh int, meshIdxs []int) []glyph.Entity {
	nodes := model.MeshInstances(docMesh)
	entities := make([]glyph.Entity, 0, len(meshIdxs))
	for _, mi := range meshIdxs {
		mm := model.Meshes[mi]
		placements := make([]renderer.MeshInstance, len(nodes))
		for j, ni := range nodes {
			var m [16]float32
			copy(m[:], model.Nodes[ni].World[:])
			placements[j] = renderer.MeshInstance{Model: m, Tint: [4]float32{1, 1, 1, 1}}
		}

		set, err := e.Renderer().CreateInstanceSet(mm.Mesh, len(placements), placements)
		if err != nil {
			log.Fatalf("level: -instanced: create instance set for doc mesh %d: %v", docMesh, err)
		}
		ent := e.Spawn()
		e.C.InstancedMesh.Set(ent, &glyph.InstancedMesh{Set: set})
		e.C.MeshRef.Set(ent, &glyph.MeshRef{Mesh: mm.Mesh, Metallic: mm.Metallic, Roughness: mm.Roughness})
		e.C.Color.Set(ent, &glyph.Color{R: mm.BaseColor[0], G: mm.BaseColor[1], B: mm.BaseColor[2]})
		if mm.DoubleSided {
			e.C.DoubleSided.Set(ent, &glyph.DoubleSided{})
		}
		entities = append(entities, ent)
	}
	return entities
}

// spawnInstancedCollider gives an instanced node's physical presence back.
//
// An InstancedMesh entity carries no per-node Transform -- every placement's
// matrix lives in the InstanceSet instead -- and Scene.RebuildStatics reads
// Transform, Collider and Static off one entity (scene.go), none of which an
// InstanceSet has anywhere to put. This companion entity is that: Transform
// (through the same glyph.TransformFromMatrix the individual path uses, so a
// sheared collider-tagged node loses exactly the part of its transform the
// individual path always has -- see instancedGroupCandidate), Collider and
// Static, and deliberately no MeshRef, since the InstanceSet already drew
// this node's geometry once and a MeshRef here would draw it a second time
// on top of itself.
//
// What it does NOT get back: picking, or any other per-instance component a
// game might want to hang off one building rather than the whole set. A
// physical obstacle is all this recipe restores; docs/agents/instancing.md
// records that as the cost, not something this function works around.
func spawnInstancedCollider(e *glyph.Engine, model *renderer.Model, node renderer.ModelNode, meshIdxs []int) glyph.Entity {
	transform, exact := glyph.TransformFromMatrix(node.World)
	if !exact {
		log.Printf("level: node %q is sheared or has a zero scale; its collider is drawn without that part of its transform", node.Name)
	}
	ent := e.Spawn()
	e.C.Transform.Set(ent, &transform)
	e.C.Static.Set(ent, &glyph.Static{})
	if half, ok := meshesLocalHalfExtent(model, meshIdxs); ok {
		e.C.Collider.Set(ent, &glyph.Collider{HalfExtents: half})
	}
	return ent
}

// spawnPrimitive sets the render-facing components for one ModelMesh on an
// already-Transform'd entity: the GPU mesh plus its material factors. No
// texture or Material maps exist in the BUILT-IN level (see gen/main.go), so
// Color plus MeshRef's own Metallic/Roughness carries it there -- the same
// untextured path 21-streetlights' door, roof and fixture use.
//
// A glTF loaded with -level can carry a base colour texture (and, per
// docs/agents/material-maps.md, normal/metallic-roughness/occlusion maps),
// which this now draws too -- MaterialRef.PBR when LoadGLTF built a
// Material (issue #69's baked KHR_texture_transform lands in mm.Mesh's own
// vertex UVs either way, so nothing texture-specific is needed here beyond
// binding it), MaterialRef.Texture when it only found a plain base colour.
// The built-in level uses neither, so this branch never fires for it.
//
// AlphaMode/BaseAlpha (issue #68) are surfaced by the engine as pure data --
// LoadGLTF does not decide what a BLEND primitive becomes, docs/agents/models.md
// is explicit that whether it becomes Translucent is the game's call, not the
// engine's -- and this is the level format's own answer: a BLEND material
// (an artist's glass, water, foliage card) gets glyphengine's Translucent
// component, with BaseAlpha (the base colour factor's alpha) as its opacity.
// The built-in level's three materials are all plain opaque PBR (gen/main.go),
// so this branch never fires for it and its render stays byte-identical;
// it fires for the first time on a real Blender export with a glass object
// (see the package comment's -level example).
func spawnPrimitive(e *glyph.Engine, ent glyph.Entity, mm renderer.ModelMesh) {
	e.C.MeshRef.Set(ent, &glyph.MeshRef{Mesh: mm.Mesh, Metallic: mm.Metallic, Roughness: mm.Roughness})
	e.C.Color.Set(ent, &glyph.Color{R: mm.BaseColor[0], G: mm.BaseColor[1], B: mm.BaseColor[2]})
	if mm.DoubleSided {
		e.C.DoubleSided.Set(ent, &glyph.DoubleSided{})
	}
	if mm.AlphaMode == renderer.AlphaModeBlend {
		e.C.Translucent.Set(ent, &glyph.Translucent{Alpha: mm.BaseAlpha})
	}
	if mm.Material != nil || mm.Texture != nil {
		e.C.MaterialRef.Set(ent, &glyph.MaterialRef{PBR: mm.Material, Texture: mm.Texture})
	}
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
	level := flag.String("level", "", "a .glb or .gltf on disk to load instead of the built-in level, e.g. one exported from Blender")
	reload := flag.Int("reload", 0, "load, draw, destroy and reload the level N times, then exit; asserts the renderer's live resource counts return to their baseline (exercises Renderer.DestroyModel)")
	instanced := flag.Bool("instanced", false, "batch every doc mesh shared by several static nodes into one renderer.InstanceSet instead of one entity per node (issue #71; see the package comment for the rule)")
	flag.Parse()

	if *instanced && *reload > 0 {
		// Nothing releases an InstanceSet's GPU buffer yet
		// (renderer/instancedmesh.go has no DestroyInstanceSet), so a level
		// reloaded under -instanced would leak one set's worth of memory per
		// swap. Refusing the combination is the honest failure; leaking
		// silently for twenty cycles is not.
		log.Fatalf("-instanced and -reload do not combine: reloading would leak an InstanceSet's buffer every swap")
	}

	opts := []glyph.Option{
		glyph.WithTitle("GlyphEngine - 22 Level"),
		glyph.WithWindowSize(*width, *height),
		glyph.WithMSAA(4),
		glyph.WithDebugKeys(),
	}
	if *fullscreen {
		opts = append(opts, glyph.WithFullscreen())
	}
	// -reload closes the window itself when its last cycle finishes, and each
	// cycle takes reloadCycleFrames frames, so a -frames
	// cap smaller than that would end the run mid-loop with every check still
	// unmade -- looking exactly like a pass. `task validate` runs every
	// example with -frames 30, so this is not hypothetical. A cap is still
	// applied as a backstop in case the loop itself never terminates.
	switch {
	case *reload > 0:
		needed := (*reload + 3) * reloadCycleFrames
		if *frames > 0 && *frames < needed {
			log.Printf("-reload %d needs about %d frames; ignoring -frames %d, which would cut the loop short", *reload, needed, *frames)
		}
		opts = append(opts, glyph.WithMaxFrames(needed))
	case *frames > 0:
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

		levelPath:    *level,
		reloadCycles: *reload,
		cyclesLeft:   *reload,
		instanced:    *instanced,
	}
	e, err := glyph.New(g, opts...)
	if err != nil {
		log.Fatalf("create engine: %v", err)
	}
	defer e.Destroy()

	e.Run()
	log.Printf("rendered %d frames", e.FrameCount())
}
