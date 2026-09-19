package renderer

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/go-gl/mathgl/mgl32"
	"github.com/qmuntal/gltf"
)

// TestLevelGLBLightsAndTransforms parses the COMMITTED examples/22-level/
// assets/level.glb -- the same bytes the example loads at runtime, generated
// by examples/22-level/gen -- with the real glTF decoder and runs the pure
// extractors from this package over it, the same way LoadGLTF would, minus
// the GPU upload. This is the one test in the suite that ties the generator,
// the committed file and the extractors together: any of the three drifting
// from the others (a generator change not regenerated, a hand-edited
// level.glb, an extractor change that only the in-memory tests above happen
// to still agree with) fails here even though each piece's own unit tests
// stay green.
//
// The expected node/mesh/light counts and the one light's world position and
// direction are computed BY HAND from examples/22-level/gen/main.go's own
// inputs (see the comments below), not copied from a first run's output.
func TestLevelGLBLightsAndTransforms(t *testing.T) {
	data, err := os.ReadFile("../examples/22-level/assets/level.glb")
	if err != nil {
		t.Fatalf("read level.glb: %v (run `go run ./gen` from examples/22-level first)", err)
	}

	doc := new(gltf.Document)
	if err := gltf.NewDecoder(bytes.NewReader(data)).Decode(doc); err != nil {
		t.Fatalf("decode level.glb: %v", err)
	}

	// 15 nodes: Ground(1) + 4 buildings + 4 lamp posts, each with a child
	// light node (4*2=8) + PlazaLantern(1) + Spawn(1) = 1+4+8+1+1 = 15.
	if len(doc.Nodes) != 15 {
		t.Fatalf("doc has %d nodes, want 15 -- gen/main.go's node list must have changed without this test being updated", len(doc.Nodes))
	}
	// 3 doc meshes: GroundSlab, Building (shared by 4 nodes), LampPost
	// (shared by 4 nodes).
	if len(doc.Meshes) != 3 {
		t.Fatalf("doc has %d meshes, want 3", len(doc.Meshes))
	}

	nodes := extractNodes(doc)
	lights := extractLights(doc, nodes)

	// 5 lights ATTACHED to nodes: one per lamp post (4, all sharing light
	// definition 0) plus the plaza lantern (1, light definition 1) -- even
	// though the document only DEFINES 2 lights. If this read 2, extractLights
	// would be counting definitions instead of node references.
	if len(lights) != 5 {
		t.Fatalf("got %d lights, want 5 (4 lamp posts + 1 plaza lantern, sharing 2 light definitions)", len(lights))
	}
	spotCount, pointCount := 0, 0
	for _, l := range lights {
		switch l.Kind {
		case LightKindSpot:
			spotCount++
			if l.Range != 0 {
				t.Errorf("spot light Range = %v, want 0 (Blender's exporter never writes range -- unbounded is the normal case)", l.Range)
			}
			if l.InnerCone != 0.42 || l.OuterCone != 0.6 {
				t.Errorf("spot cone = [%v %v], want [0.42 0.6] (measured Blender 1000W spot)", l.InnerCone, l.OuterCone)
			}
			if l.Intensity != 54351.4 {
				t.Errorf("spot Intensity = %v, want 54351.4 (measured Blender 1000W spot)", l.Intensity)
			}
		case LightKindPoint:
			pointCount++
		default:
			t.Errorf("unexpected light kind %v", l.Kind)
		}
	}
	if spotCount != 4 {
		t.Errorf("got %d spot lights, want 4 (one per lamp post)", spotCount)
	}
	if pointCount != 1 {
		t.Errorf("got %d point lights, want 1 (the plaza lantern)", pointCount)
	}

	// One light's world position and direction, by hand from gen/main.go's
	// own inputs: lamp post 0 sits at translation (10,0,10) with no
	// rotation of its own, its light CHILD node is offset (0,3,0) further
	// and carries lightNodeRotX = -90deg about X. World is the parent's
	// translation composed with the child's local (T then R, per
	// nodeLocalTransform's T*R*S), so:
	//   pos = (10,0,10) + (0,3,0)                      = (10,3,10)
	//   dir = RotX(-90) applied to glTF's -Z aim axis   = (0,-1,0)
	// -- the same rotation TestExtractNodesOrientationSurvives hand-checks
	// for +90 about X giving (0,1,0); flipping the sign flips the result.
	//
	// It has teeth against real data, not just the synthetic document in
	// gltflights_test.go: aiming down +Z instead of -Z reported "LampPost0
	// light world dir = [0 1 ...], want [0 -1 0]", and reading Local instead
	// of World (dropping LampPost0's own translation from the composition)
	// reported "LampPost0 light world pos = [0 3 0], want [10 3 10]". Both
	// introduced in gltflights.go and reverted to confirm.
	var lamp0Light ModelLight
	found := false
	for i := range doc.Nodes {
		if doc.Nodes[i].Name == "LampPost0_Light" {
			for _, l := range lights {
				if l.Node == i {
					lamp0Light = l
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatal("no light attached to node LampPost0_Light")
	}
	pos, dir := lightWorldPosDir(nodes, lamp0Light)
	wantPos := mgl32.Vec3{10, 3, 10}
	wantDir := mgl32.Vec3{0, -1, 0}
	if !vec3Close(pos, wantPos, 1e-4) {
		t.Errorf("LampPost0 light world pos = %v, want %v", pos, wantPos)
	}
	if !vec3Close(dir, wantDir, 1e-4) {
		t.Errorf("LampPost0 light world dir = %v, want %v (straight down)", dir, wantDir)
	}

	// NodeMeshes: reconstruct the []ModelMesh LoadGLTF would build -- DocMesh
	// alone, in doc mesh index order, which is exactly what this file needs
	// since every one of its 3 doc meshes has exactly one primitive. No
	// Renderer/GPU involved, unlike LoadGLTF itself, because DocMesh's value
	// does not depend on anything the GPU upload computes.
	meshes := make([]ModelMesh, len(doc.Meshes))
	for i := range meshes {
		meshes[i].DocMesh = i
	}
	var buildingNode, lampNode, groundNode int = -1, -1, -1
	for i, gn := range doc.Nodes {
		switch gn.Name {
		case "Building0":
			buildingNode = i
		case "LampPost1":
			lampNode = i
		case "Ground":
			groundNode = i
		}
	}
	if buildingNode < 0 || lampNode < 0 || groundNode < 0 {
		t.Fatal("expected named nodes not found -- gen/main.go's naming must have changed")
	}
	if got := nodeMeshes(nodes, meshes, buildingNode); len(got) != 1 || got[0] != 1 {
		t.Errorf("Building0 NodeMeshes = %v, want [1] (the shared Building doc mesh)", got)
	}
	if got := nodeMeshes(nodes, meshes, lampNode); len(got) != 1 || got[0] != 2 {
		t.Errorf("LampPost1 NodeMeshes = %v, want [2] (the shared LampPost doc mesh)", got)
	}
	if got := nodeMeshes(nodes, meshes, groundNode); len(got) != 1 || got[0] != 0 {
		t.Errorf("Ground NodeMeshes = %v, want [0]", got)
	}

	// Extras: Building1 carries all three types the issue calls out
	// explicitly -- a bool, a string and a number -- and Spawn carries the
	// pattern the example uses to find the camera's start point without
	// matching on a node name.
	var building1, spawn *ModelNode
	for i := range nodes {
		switch doc.Nodes[i].Name {
		case "Building1":
			building1 = &nodes[i]
		case "Spawn":
			spawn = &nodes[i]
		}
	}
	if building1 == nil || spawn == nil {
		t.Fatal("Building1 or Spawn node not found")
	}
	var tags struct {
		Static   bool    `json:"static"`
		Collider string  `json:"collider"`
		Floors   float64 `json:"floors"`
	}
	if err := json.Unmarshal(building1.Extras, &tags); err != nil {
		t.Fatalf("unmarshal Building1.Extras: %v", err)
	}
	if !tags.Static || tags.Collider != "box" || tags.Floors != 1 {
		t.Errorf("Building1.Extras decoded to %+v, want {Static:true Collider:box Floors:1}", tags)
	}
	var spawnTag struct {
		Spawn string `json:"spawn"`
	}
	if err := json.Unmarshal(spawn.Extras, &spawnTag); err != nil {
		t.Fatalf("unmarshal Spawn.Extras: %v", err)
	}
	if spawnTag.Spawn != "player" {
		t.Errorf("Spawn.Extras = %s, want spawn=player", spawn.Extras)
	}

	// Building1's own transform: the measured Alt-D linked-duplicate,
	// verbatim. This is the non-uniform-scale-plus-rotation case the issue
	// asks the level to exercise -- checked here against Local, confirming
	// both that gen/main.go wrote exactly these TRS numbers into the file
	// and that extractNodes carried them through. The quaternion-to-matrix
	// step itself is independently hand-verified elsewhere
	// (TestExtractNodesOrientationSurvives), so reusing mgl32.Quat.Mat4()
	// here -- the same primitive nodeLocalTransform composes with -- is
	// enough to catch this specific transform drifting from what the
	// generator authored. Building1 has no parent, so Local and World
	// coincide.
	rot := mgl32.Quat{W: 0.9553, V: mgl32.Vec3{0, 0.2955, 0}}.Mat4()
	wantLocal := mgl32.Translate3D(6, 1, -2).Mul4(rot).Mul4(mgl32.Scale3D(1, 1.5, 2))
	if !mat4Close(building1.Local, wantLocal, 1e-3) {
		t.Errorf("Building1.Local = %v, want %v", building1.Local, wantLocal)
	}
}
