package glyphengine

import (
	"encoding/json"
	"math"
	"os"
	"testing"

	"github.com/go-gl/mathgl/mgl32"

	"github.com/derekmwright/glyphengine/ecs"
	"github.com/derekmwright/glyphengine/renderer"
)

// levelTags is this TEST's own vocabulary for node extras, the same shape
// examples/22-level defines for itself. The engine never looks inside
// ModelNode.Extras (AGENTS.md rule 14), so what "static" and "collider" mean
// is decided here, not by the loader.
type levelTags struct {
	Static   bool   `json:"static"`
	Collider string `json:"collider"`
}

// TestLevelGLTFToSceneCollidersWithoutDevice is the test issue #75 exists to
// make possible: a level file that a real Blender wrote goes all the way to a
// queryable Scene with NO Vulkan device anywhere in it.
//
// file -> renderer.ReadGLTF -> TransformFromMatrix per mesh node -> entities
// with Transform, a box Collider sized from ModelMesh.Verts, and Static from
// the node's own extras -> Scene.Raycast and Scene.OverlapAABB finding the
// building where Blender put it.
//
// Before ReadGLTF the only door into a level was Renderer.LoadGLTF, so every
// step above needed a GPU and this test could not be written at all -- which
// is the actual gap, not the missing function: a dedicated server, a navmesh
// baker or a CI level validator wants exactly this chain and none of the
// pixels.
//
// The expected numbers are NOT read back from the file. Building_Linked's
// placement -- Translate(10, 2, 0) * RotY(30 degrees) * Scale(1.5, 1, 2) -- is
// hand-computed from tools/blender/build_fixture.py's own Blender-space
// inputs through the axis conversion documented in
// docs/agents/blender-pipeline.md, and is the same expectation
// renderer/gltfblender_test.go's checkSharedMeshAndDuplicate asserts on the
// raw document. This asserts the same placement survives all the way into a
// physics query.
//
// BROKEN: made marshalExtras return nil for every node, so the read carried no
// extras. FAILED with: "spawned 0 static entities, want 2 (Building,
// Building_Linked)" -- the whole chain this test is about, from the node's own
// application data to a queryable collider, runs through that one value.
// Restored with `git checkout -- renderer/gltf.go`.
func TestLevelGLTFToSceneCollidersWithoutDevice(t *testing.T) {
	model, err := renderer.ReadGLTF(os.DirFS("renderer/testdata/blender"), "level.glb")
	if err != nil {
		t.Fatalf("ReadGLTF: %v", err)
	}

	scene := NewScene()
	byName := make(map[string]ecs.Entity)
	inexact := make(map[string]bool)
	statics := 0

	for i := range model.Nodes {
		node := model.Nodes[i]
		meshIdxs := model.NodeMeshes(i)
		if len(meshIdxs) == 0 {
			continue // an empty node: a lamp socket, the spawn marker
		}

		transform, exact := TransformFromMatrix(node.World)
		if !exact {
			inexact[node.Name] = true
		}

		ent := scene.Spawn()
		own := transform
		scene.C.Transform.Set(ent, &own)
		byName[node.Name] = ent

		var tags levelTags
		if len(node.Extras) > 0 {
			if err := json.Unmarshal(node.Extras, &tags); err != nil {
				t.Fatalf("node %q: extras %s: %v", node.Name, node.Extras, err)
			}
		}
		if tags.Static {
			scene.C.Static.Set(ent, &Static{})
			statics++
		}
		if tags.Collider == "box" {
			half, ok := halfExtentFromVerts(model, meshIdxs)
			if !ok {
				t.Fatalf("node %q: tagged collider=box but its primitives retained no vertices", node.Name)
			}
			scene.C.Collider.Set(ent, &Collider{HalfExtents: half})
		}
	}
	scene.RebuildStatics()

	// The fixture tags exactly Building and Building_Linked with
	// {"collider":"box","static":true}; nothing else in it carries extras
	// that ask for a collider. If build_fixture.py grows another tagged
	// object this count is the first thing that says so.
	if statics != 2 {
		t.Fatalf("spawned %d static entities, want 2 (Building, Building_Linked)", statics)
	}

	// The two placement facts a previous gate verified by hand against this
	// exact file, asserted here through TransformFromMatrix rather than
	// against the raw matrix: a mirrored object is representable and comes
	// back exact with the mirror on Scale.X, and a rotated child inside a
	// non-uniformly scaled parent is sheared and cannot be.
	mirrored, ok := model.Node("Mirrored")
	if !ok {
		t.Fatal("no Mirrored node")
	}
	mt, mExact := TransformFromMatrix(mirrored.World)
	if !mExact {
		t.Error("Mirrored came back inexact; a mirror IS representable as a negative scale")
	}
	if mt.Scale.X() >= 0 {
		t.Errorf("Mirrored Scale.X = %v, want negative (the mirror lands on X)", mt.Scale.X())
	}
	if !inexact["ShearChild"] {
		t.Error("ShearChild came back exact; a rotated child under a non-uniformly scaled parent is sheared and no Transform can hold that")
	}

	// A ray straight down onto Building. Its node sits at (4, 1, 0) and its
	// mesh is a unit cube, so the collider's top face is at y = 2 and a ray
	// from y = 10 has 8 units to travel. Asserting the DISTANCE and not just
	// "something was hit" is what makes this a statement about where Blender
	// put the building rather than about the ray hitting anything at all.
	wantBuilding, ok := byName["Building"]
	if !ok {
		t.Fatal("no entity spawned for Building")
	}
	hit, hitOK := scene.Raycast(mgl32.Vec3{4, 10, 0}, mgl32.Vec3{0, -1, 0}, 20, 0)
	if !hitOK {
		t.Fatal("ray down the middle of Building missed everything")
	}
	if hit.Entity != wantBuilding {
		t.Errorf("ray hit entity %v, want Building's %v", hit.Entity, wantBuilding)
	}
	if math.Abs(float64(hit.T)-8) > 1e-3 {
		t.Errorf("ray hit at T = %v, want 8 (top of a unit cube whose node is at y = 1)", hit.T)
	}

	// The control, without which the assertion above would also pass if
	// everything in the file had a collider: GlassPane sits at (-4, 1, -4) and
	// carries no extras at all, so nothing was spawned with a collider there
	// and the same ray must find nothing.
	if _, hitOK := scene.Raycast(mgl32.Vec3{-4, 10, -4}, mgl32.Vec3{0, -1, 0}, 20, 0); hitOK {
		t.Error("a ray down onto GlassPane hit something; only the two extras-tagged buildings have colliders")
	}

	// OverlapAABB at Building_Linked's hand-computed position. Its node is at
	// (10, 2, 0) with scale (1.5, 1, 2), and WorldAABB is Position +/-
	// HalfExtents*|Scale|, so a small probe box at that point must find it and
	// only it -- Building, six metres away on X, is well outside.
	wantLinked, ok := byName["Building_Linked"]
	if !ok {
		t.Fatal("no entity spawned for Building_Linked")
	}
	probe := AABB{Min: mgl32.Vec3{9.75, 1.75, -0.25}, Max: mgl32.Vec3{10.25, 2.25, 0.25}}
	results := scene.OverlapAABB(probe, 0)
	if len(results) != 1 || results[0].Entity != wantLinked {
		t.Fatalf("OverlapAABB at Building_Linked returned %v, want exactly Building_Linked's entity %v", results, wantLinked)
	}
	// Its world box must be the one the scale implies, not the unit cube's:
	// a loader that dropped the node's scale would still be found by the
	// probe above, and only this notices.
	if got, want := results[0].Box.Min, (mgl32.Vec3{8.5, 1, -2}); got.Sub(want).Len() > 1e-3 {
		t.Errorf("Building_Linked world AABB Min = %v, want %v (scale 1.5, 1, 2 around (10, 2, 0))", got, want)
	}
	if got, want := results[0].Box.Max, (mgl32.Vec3{11.5, 3, 2}); got.Sub(want).Len() > 1e-3 {
		t.Errorf("Building_Linked world AABB Max = %v, want %v", got, want)
	}
}

// halfExtentFromVerts sizes a box collider from a node's primitives, in the
// node's own local space: max(|min|, |max|) per axis, which is symmetric about
// the node's origin rather than about the mesh's own (possibly off-centre)
// box.
//
// The same rule examples/22-level uses, for the same reason: Collider carries
// only HalfExtents and no centre offset (WorldAABB is Position +/-
// HalfExtents*Scale), and an editor naturally puts a building's origin at its
// base rather than its middle. Symmetric-and-conservative is the smallest box
// guaranteed to contain the real one without a field the engine does not have.
func halfExtentFromVerts(model *renderer.Model, meshIdxs []int) (mgl32.Vec3, bool) {
	lo := mgl32.Vec3{math.MaxFloat32, math.MaxFloat32, math.MaxFloat32}
	hi := mgl32.Vec3{-math.MaxFloat32, -math.MaxFloat32, -math.MaxFloat32}
	found := false
	for _, mi := range meshIdxs {
		for _, v := range model.Meshes[mi].Verts {
			for a := 0; a < 3; a++ {
				if v.Pos[a] < lo[a] {
					lo[a] = v.Pos[a]
				}
				if v.Pos[a] > hi[a] {
					hi[a] = v.Pos[a]
				}
			}
			found = true
		}
	}
	if !found {
		return mgl32.Vec3{}, false
	}
	return mgl32.Vec3{
		maxAbs(lo[0], hi[0]),
		maxAbs(lo[1], hi[1]),
		maxAbs(lo[2], hi[2]),
	}, true
}

func maxAbs(a, b float32) float32 {
	if a < 0 {
		a = -a
	}
	if b < 0 {
		b = -b
	}
	if a > b {
		return a
	}
	return b
}
