package renderer

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/go-gl/mathgl/mgl32"
	"github.com/qmuntal/gltf"
	"github.com/qmuntal/gltf/ext/texturetransform"
)

// TestBlenderFixture parses renderer/testdata/blender/level.glb -- built
// PROCEDURALLY by tools/blender/build_fixture.py and exported by a real,
// installed Blender through tools/blender/export_level.py (see that
// directory's README) -- with the same decoder LoadGLTF uses, and runs the
// package's pure extractors over it, the same way TestLevelGLBLightsAndTransforms
// (renderer/gltflevel_test.go) checks examples/22-level's generator-written
// fixture. No Renderer and no GPU: everything here is *gltf.Document plus
// extractNodes/extractLights/nodeMeshes, which is all any of this needs.
//
// Unlike gltflevel_test.go's fixture, this one was not written by a Go
// program that could simply reuse the same numbers it authored -- it went
// through Blender's actual exporter, including the Z-up -> Y-up axis
// conversion. The expected values below were computed BY HAND from
// tools/blender/build_fixture.py's own Blender-space inputs using the
// conversion this issue measured (see docs/agents/blender-pipeline.md):
// position/scale axes permute as (x,y,z) -> (x,z,-y) [a rotation of -90
// degrees about X], and a rotation about Blender's Z axis becomes the SAME
// angle about glTF's Y axis (Blender's vertical axis maps to glTF's,
// unchanged, since that rotation axis itself maps to +Y under the
// conversion). Every hand-computed number below was then cross-checked
// against this exact file's real, decoded bytes -- not assumed -- which is
// how the Building_Linked and Mirrored sections below found two things a
// naive derivation would have gotten wrong (noted in place).
//
// BREAK/RESTORE VERIFICATION: for every assertion group in this file, the
// function it guards was broken, `go test -run TestBlenderFixture ./renderer`
// was confirmed to fail with the message noted in that group's comment, and
// the source was restored with `git checkout -- <file>` before the next
// group. Nothing here is a check that has never failed.
func TestBlenderFixture(t *testing.T) {
	runBlenderFixtureChecks(t, "testdata/blender/level.glb")
}

// TestBlenderFixtureAltVersion runs the identical checks against a fixture
// built by a DIFFERENT installed Blender version, pointed to by the
// BLENDER_FIXTURE environment variable -- e.g.
//
//	BLENDER_FIXTURE=/scratch/blender42/level.glb go test -run TestBlenderFixtureAltVersion ./renderer
//
// Skips cleanly when unset, which is the normal case: the committed fixture
// (TestBlenderFixture above) always runs, this one only runs when someone
// is deliberately cross-checking a second Blender install, per issue #67's
// "verify across versions" step. Nobody has yet: only 5.0.1 was available
// when this was written, and docs/agents/blender-pipeline.md's "Verified
// against" says so.
func TestBlenderFixtureAltVersion(t *testing.T) {
	path := os.Getenv("BLENDER_FIXTURE")
	if path == "" {
		t.Skip("BLENDER_FIXTURE not set; only the committed fixture (TestBlenderFixture) runs by default")
	}
	runBlenderFixtureChecks(t, path)
}

func runBlenderFixtureChecks(t *testing.T, path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (run tools/blender/build_fixture.py first)", path, err)
	}

	doc := new(gltf.Document)
	if err := gltf.NewDecoder(bytes.NewReader(data)).Decode(doc); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}

	t.Run("Generator", func(t *testing.T) { checkGenerator(t, doc) })
	t.Run("NodesAndExtras", func(t *testing.T) { checkNodesAndExtras(t, doc) })
	t.Run("SharedMeshAndDuplicate", func(t *testing.T) { checkSharedMeshAndDuplicate(t, doc) })
	t.Run("Lights", func(t *testing.T) { checkLights(t, doc) })
	t.Run("TextureTransform", func(t *testing.T) { checkTextureTransform(t, doc) })
	t.Run("AlphaAndDoubleSided", func(t *testing.T) { checkAlphaAndDoubleSided(t, doc) })
	t.Run("Mirrored", func(t *testing.T) { checkMirrored(t, doc) })
	t.Run("ParentChain", func(t *testing.T) { checkParentChain(t, doc) })
	t.Run("LoadedTilingAndAlpha", func(t *testing.T) { checkLoadedTilingAndAlpha(t, doc) })
	t.Run("Instancing", func(t *testing.T) { checkInstancing(t, doc) })
}

// nodeIndexByName returns the index of the first doc.Nodes entry named
// name, or -1. Local to this file because gltflevel_test.go's fixture has
// no duplicate names to worry about; this fixture does (two nodes named
// "Prop", one per collection instance -- see checkInstancing), so callers
// here are deliberately explicit about which one they mean rather than
// leaning on this helper for those cases.
func nodeIndexByName(doc *gltf.Document, name string) int {
	for i, n := range doc.Nodes {
		if n != nil && n.Name == name {
			return i
		}
	}
	return -1
}

func materialByName(doc *gltf.Document, name string) *gltf.Material {
	for _, m := range doc.Materials {
		if m.Name == name {
			return m
		}
	}
	return nil
}

// checkGenerator asserts doc.Asset.Generator names Blender's own exporter --
// the one fact in this file that proves real Blender wrote these bytes
// rather than a Go program shaped like one.
//
// BROKEN: changed the substring check to require "glyphengine" instead of
// "Blender". FAILED with: "Generator = \"Khronos glTF Blender I/O
// v5.0.21\", want it to contain \"glyphengine\"" -- confirming the check
// reads the real field rather than a hardcoded pass. Restored with
// `git checkout -- renderer/gltfblender_test.go`.
func checkGenerator(t *testing.T, doc *gltf.Document) {
	if !strings.Contains(doc.Asset.Generator, "Blender") {
		t.Errorf("Generator = %q, want it to contain %q", doc.Asset.Generator, "Blender")
	}
}

// checkNodesAndExtras asserts the node count, and that extras round-trip
// with all three JSON types the issue calls out (bool, string, number) on
// Building, the {"spawn":"player"} tag on the spawn empty, and that Ground
// carries none -- the nil case, which a reader that defaults to an empty
// struct on ANY input (extras present or not) would still pass without
// this specific assertion.
//
// BROKEN: made extractNodes always return a non-nil Extras (marshalExtras
// changed to `return json.RawMessage("{}")` instead of `return nil` when
// extras == nil). FAILED with: "Ground.Extras = {}, want nil". Restored.
func checkNodesAndExtras(t *testing.T, doc *gltf.Document) {
	// 17 nodes: Ground, Building, Building_Linked, GlassPane, CulledPanel,
	// OpenPanel, SpotLamp, PointLamp, spawn, Mirrored, ShearChild,
	// ShearParent, Prop x2 (one per collection instance), PropCollectionInstance
	// x2, GNSourcePoints. Counted from build_fixture.py's own object list,
	// then confirmed against this exact file.
	if len(doc.Nodes) != 17 {
		t.Fatalf("doc has %d nodes, want 17 -- tools/blender/build_fixture.py's object list must have changed without this test being updated", len(doc.Nodes))
	}

	nodes := extractNodes(doc)

	groundIdx := nodeIndexByName(doc, "Ground")
	buildingIdx := nodeIndexByName(doc, "Building")
	spawnIdx := nodeIndexByName(doc, "spawn")
	if groundIdx < 0 || buildingIdx < 0 || spawnIdx < 0 {
		t.Fatal("Ground, Building or spawn node not found by name")
	}

	if nodes[groundIdx].Extras != nil {
		t.Errorf("Ground.Extras = %s, want nil", nodes[groundIdx].Extras)
	}

	var tags struct {
		Collider string  `json:"collider"`
		Static   bool    `json:"static"`
		Floors   float64 `json:"floors"`
	}
	if err := json.Unmarshal(nodes[buildingIdx].Extras, &tags); err != nil {
		t.Fatalf("unmarshal Building.Extras: %v", err)
	}
	if tags.Collider != "box" || !tags.Static || tags.Floors != 3 {
		t.Errorf("Building.Extras decoded to %+v, want {Collider:box Static:true Floors:3}", tags)
	}

	var spawnTag struct {
		Spawn string `json:"spawn"`
	}
	if err := json.Unmarshal(nodes[spawnIdx].Extras, &spawnTag); err != nil {
		t.Fatalf("unmarshal spawn.Extras: %v", err)
	}
	if spawnTag.Spawn != "player" {
		t.Errorf("spawn.Extras = %s, want spawn=player", nodes[spawnIdx].Extras)
	}
}

// checkSharedMeshAndDuplicate asserts Building and Building_Linked --
// Alt-D's Python equivalent, object.copy() sharing the mesh datablock, per
// build_fixture.py's build_building() -- share ONE doc mesh, that
// Model.NodeMeshes returns the SAME primitive for both nodes, and that
// Building_Linked's World matches the hand-computed TRS.
//
// The rotation-about-Z-becomes-rotation-about-Y and the axis permutation on
// scale are the two facts in this file's header comment that this
// specifically exercises: build_fixture.py authored Blender rotation_euler
// = (0,0,30deg) and scale = (1.5, 2.0, 1.0); this asserts the exported
// node's Local is Translate(10,2,0) * RotY(30deg) * Scale(1.5, 1.0, 2.0) --
// scale's Y and Z swapped, matching the axis permutation, while the
// rotation stays the SAME 30 degrees, just about Y instead of Z. Both were
// cross-checked against this file's actual decoded rotation quaternion
// (0, 0.258819, 0, 0.965926) and scale ([1.5, 1, 2]) before being written
// here, rather than trusted from the derivation alone.
//
// BROKEN: changed nodeMeshes to `return []int{0, 1, 2}` unconditionally
// (ignoring which node was asked for). FAILED with: "Building NodeMeshes =
// [0 1 2], Building_Linked NodeMeshes = [0 1 2], want equal single-element
// slices". Restored.
func checkSharedMeshAndDuplicate(t *testing.T, doc *gltf.Document) {
	nodes := extractNodes(doc)
	meshes := make([]ModelMesh, len(doc.Meshes))
	for i := range meshes {
		meshes[i].DocMesh = i
	}

	buildingIdx := nodeIndexByName(doc, "Building")
	dupIdx := nodeIndexByName(doc, "Building_Linked")
	if buildingIdx < 0 || dupIdx < 0 {
		t.Fatal("Building or Building_Linked node not found")
	}
	if doc.Nodes[buildingIdx].Mesh == nil || doc.Nodes[dupIdx].Mesh == nil {
		t.Fatal("Building or Building_Linked carries no mesh")
	}
	if *doc.Nodes[buildingIdx].Mesh != *doc.Nodes[dupIdx].Mesh {
		t.Errorf("Building mesh %d != Building_Linked mesh %d, want the same doc mesh (Alt-D shares the datablock)",
			*doc.Nodes[buildingIdx].Mesh, *doc.Nodes[dupIdx].Mesh)
	}

	gotBuilding := nodeMeshes(nodes, meshes, buildingIdx)
	gotDup := nodeMeshes(nodes, meshes, dupIdx)
	if len(gotBuilding) != 1 || len(gotDup) != 1 || gotBuilding[0] != gotDup[0] {
		t.Errorf("Building NodeMeshes = %v, Building_Linked NodeMeshes = %v, want equal single-element slices", gotBuilding, gotDup)
	}

	rot := mgl32.QuatRotate(mgl32.DegToRad(30), mgl32.Vec3{0, 1, 0})
	wantWorld := mgl32.Translate3D(10, 2, 0).Mul4(rot.Mat4()).Mul4(mgl32.Scale3D(1.5, 1.0, 2.0))
	if !mat4Close(nodes[dupIdx].World, wantWorld, 1e-3) {
		t.Errorf("Building_Linked.World = %v, want %v", nodes[dupIdx].World, wantWorld)
	}
}

// checkLights asserts the spot and point lights build_lights() authors:
// build_fixture.py places SpotLamp at Blender (0,0,5) with identity
// rotation -- a Blender light with no rotation already aims down its local
// -Z, which in Blender's Z-up scene IS straight down, so no rig is needed
// in Blender itself. What the exporter DOES write is the axis-conversion
// rotation (-90 degrees about X) on the node, which is why the raw glTF
// rotation is non-identity even though nothing in Blender rotated this
// light -- see the header comment.
//
// The 54351.4 cd / 5435.1 cd and 0.42/0.60 cone numbers are not independent
// facts about this fixture: build_fixture.py's spot is 1000 W / spot_size
// 1.2 rad / blend 0.3, and the point is 100 W, the SAME inputs
// examples/22-level/gen/main.go used to arrive at the identical measured
// numbers (see that file's package comment) -- this is a second, real-
// Blender confirmation of the same physical-units conversion, not a copy.
//
// BROKEN: flipped the sign in lightWorldPosDir's direction (`{0, 0, 1}`
// instead of `{0, 0, -1}` as the local aim axis). FAILED with:
// "SpotLamp world dir = [0 1 -1.19209275e-07], want [0 -1 0] (straight
// down)". Restored.
func checkLights(t *testing.T, doc *gltf.Document) {
	nodes := extractNodes(doc)
	lights := extractLights(doc, nodes)
	if len(lights) != 2 {
		t.Fatalf("got %d lights, want 2 (SpotLamp, PointLamp)", len(lights))
	}

	spotIdx := nodeIndexByName(doc, "SpotLamp")
	pointIdx := nodeIndexByName(doc, "PointLamp")
	if spotIdx < 0 || pointIdx < 0 {
		t.Fatal("SpotLamp or PointLamp node not found")
	}

	var spot, point *ModelLight
	for i := range lights {
		switch lights[i].Node {
		case spotIdx:
			spot = &lights[i]
		case pointIdx:
			point = &lights[i]
		}
	}
	if spot == nil || point == nil {
		t.Fatal("light not attached to SpotLamp or PointLamp as expected")
	}

	if spot.Kind != LightKindSpot {
		t.Errorf("SpotLamp kind = %v, want spot", spot.Kind)
	}
	if closeF(spot.Intensity, 54351.4, 1) == false {
		t.Errorf("SpotLamp Intensity = %v, want 54351.4 +/- 1 (measured Blender 1000W spot, SPEC mode)", spot.Intensity)
	}
	if closeF(spot.InnerCone, 0.42, 0.01) == false || closeF(spot.OuterCone, 0.60, 0.01) == false {
		t.Errorf("SpotLamp cones = [%v %v], want [0.42 0.60] (spot_size 1.2, blend 0.3)", spot.InnerCone, spot.OuterCone)
	}
	if spot.Range != 0 {
		t.Errorf("SpotLamp Range = %v, want 0 (Blender's exporter never writes range)", spot.Range)
	}
	pos, dir := lightWorldPosDir(nodes, *spot)
	wantPos := mgl32.Vec3{0, 5, 0}
	wantDir := mgl32.Vec3{0, -1, 0}
	if !vec3Close(pos, wantPos, 1e-4) {
		t.Errorf("SpotLamp world pos = %v, want %v", pos, wantPos)
	}
	if !vec3Close(dir, wantDir, 1e-4) {
		t.Errorf("SpotLamp world dir = %v, want %v (straight down)", dir, wantDir)
	}

	if point.Kind != LightKindPoint {
		t.Errorf("PointLamp kind = %v, want point", point.Kind)
	}
	if closeF(point.Intensity, 5435.1, 1) == false {
		t.Errorf("PointLamp Intensity = %v, want 5435.1 +/- 1 (measured Blender 100W point, SPEC mode)", point.Intensity)
	}
}

func closeF(got, want, tol float32) bool {
	d := got - want
	if d < 0 {
		d = -d
	}
	return d <= tol
}

// checkTextureTransform is a RAW-DOCUMENT finding for issue #69, asserted
// directly on *gltf.Document because renderer.LoadGLTF does not read
// KHR_texture_transform yet -- this is ground truth for that issue, not a
// claim about engine behaviour today.
//
// Ground's material carries a Mapping node scaled (8,8) feeding its Image
// Texture (build_ground()). The exporter turns that into
// KHR_texture_transform with scale [8,8] -- expected, and required by issue
// #67. What was NOT expected going in, and only found by reading this
// file's actual bytes: offset comes out [0,-7], not [0,0]. The reason is
// glTF's V axis running the opposite way from Blender's UV V (glTF textures
// are sampled top-down, Blender's bottom-up) -- the exporter's V-flip
// composes with an 8x scale as offset_v = 1 - scale_v = 1 - 8 = -7. A #69
// implementation that reads `scale` and ignores `offset` gets the tiling
// FREQUENCY right and the tiling POSITION wrong. See
// docs/agents/blender-pipeline.md.
//
// BROKEN: changed the expected scale to [4,4]. FAILED with: "Ground
// baseColorTexture KHR_texture_transform scale = [8 8], want [4 4]".
// Restored.
func checkTextureTransform(t *testing.T, doc *gltf.Document) {
	mat := materialByName(doc, "Ground")
	if mat == nil || mat.PBRMetallicRoughness == nil || mat.PBRMetallicRoughness.BaseColorTexture == nil {
		t.Fatal("Ground material has no baseColorTexture")
	}
	raw, ok := mat.PBRMetallicRoughness.BaseColorTexture.Extensions[texturetransform.ExtensionName]
	if !ok {
		t.Fatal("Ground baseColorTexture carries no KHR_texture_transform")
	}
	tt, ok := raw.(*texturetransform.TextureTranform)
	if !ok {
		t.Fatalf("Ground baseColorTexture KHR_texture_transform decoded to %T, not *texturetransform.TextureTranform", raw)
	}
	if tt.Scale != [2]float64{8, 8} {
		t.Errorf("Ground baseColorTexture KHR_texture_transform scale = %v, want [8 8]", tt.Scale)
	}
	if !closeF64(tt.Offset[0], 0, 1e-6) || !closeF64(tt.Offset[1], -7, 1e-6) {
		t.Errorf("Ground baseColorTexture KHR_texture_transform offset = %v, want [0 -7] (glTF's V-flip composed with an 8x scale -- see comment above)", tt.Offset)
	}
}

func closeF64(got, want, tol float64) bool {
	d := got - want
	if d < 0 {
		d = -d
	}
	return d <= tol
}

// checkAlphaAndDoubleSided is issue #68's ground truth: alphaMode/doubleSided
// asserted on *gltf.Document directly, since renderer.LoadGLTF does not
// read AlphaMode yet either (it reads DoubleSided already, for the pipeline
// -- commands.go's material pipeline selection -- but not for anything
// build_fixture.py's Glass/Culled/Open materials probe here).
//
// Confirmed by reading the Blender 5.0 install's own exporter source
// (io_scene_gltf2/blender/exp/material/search_node_tree.py): alphaMode is
// read off the Principled BSDF's Alpha socket VALUE, not off the legacy
// Eevee blend_method property that older exporter versions used (that
// file's own comment says so) -- build_fixture.py sets both, so this
// fixture does not depend on which detection path a given Blender version
// takes. doubleSided follows use_backface_culling directly (materials.py's
// __gather_double_sided): ticked -> doubleSided omitted (defaults false),
// unticked -> doubleSided true.
//
// BROKEN: changed the Culled-material assertion to check for `true` instead
// of `false`. FAILED with: "Culled material DoubleSided = false, want
// true". Restored.
func checkAlphaAndDoubleSided(t *testing.T, doc *gltf.Document) {
	glass := materialByName(doc, "Glass")
	if glass == nil {
		t.Fatal("Glass material not found")
	}
	if glass.AlphaMode != gltf.AlphaBlend {
		t.Errorf("Glass material AlphaMode = %v, want BLEND", glass.AlphaMode)
	}
	if glass.PBRMetallicRoughness == nil || glass.PBRMetallicRoughness.BaseColorFactor == nil {
		t.Fatal("Glass material has no baseColorFactor")
	}
	if alpha := glass.PBRMetallicRoughness.BaseColorFactor[3]; !closeF64(alpha, 0.3, 1e-3) {
		t.Errorf("Glass material baseColorFactor alpha = %v, want 0.3", alpha)
	}

	culled := materialByName(doc, "Culled")
	stone := materialByName(doc, "Stone")
	if culled == nil || stone == nil {
		t.Fatal("Culled or Stone material not found")
	}
	if culled.DoubleSided {
		t.Errorf("Culled material DoubleSided = %v, want false (Backface Culling was ticked)", culled.DoubleSided)
	}
	if !stone.DoubleSided {
		t.Errorf("Stone material DoubleSided = %v, want true (Backface Culling was NOT ticked -- the exporter's own default)", stone.DoubleSided)
	}
}

// checkMirrored is a RAW-DOCUMENT finding, not an engine-behaviour claim:
// Mirrored's authored scale in Blender is (-1, 1, 1) (build_mirrored(),
// unapplied on purpose -- Object > Apply > Scale would remove the exact
// thing this probes). This file's actual bytes carry raw scale
// [-1, -1, -1] with rotation [1,0,0,0] (a 180 degree turn about X), NOT the
// naive axis-permuted [-1, 1, 1] with identity rotation the position/scale
// permutation rule elsewhere in this file would predict -- Blender's own
// matrix decomposition (mathutils' Matrix.decompose(), which the exporter
// uses to pull TRS back out of the evaluated world matrix) is free to
// split a reflection between rotation and scale however it likes, since a
// quaternion alone cannot represent a reflection and something has to
// carry the sign. Both decompositions multiply out to the SAME 3x3 linear
// map (verified by hand: Rx(180) * diag(-1,-1,-1) = diag(-1,1,1)), which is
// why this checks the composed World's determinant rather than any single
// TRS field -- the determinant is the one thing every valid decomposition
// of the same mirror agrees on. renderer/testdata/blender/README.md and
// docs/agents/blender-pipeline.md record this so #67's engine-side reader
// (glyphengine.TransformFromMatrix, root package, cannot be called from
// here -- see the package comment) is not surprised by it either: that
// function re-decomposes the matrix its OWN way and always puts the mirror
// on Scale.X, regardless of how the source file encoded it.
//
// BROKEN: inverted the determinant check (`det <= 0` / "want positive"
// instead of `det >= 0` / "want negative"). FAILED with: "Mirrored.World
// determinant = -1, want positive (a mirror)" -- i.e. the determinant IS
// correctly negative, so only the deliberately-inverted assertion failed,
// confirming the check is live rather than vacuously true either way.
// Restored.
func checkMirrored(t *testing.T, doc *gltf.Document) {
	nodes := extractNodes(doc)
	idx := nodeIndexByName(doc, "Mirrored")
	if idx < 0 {
		t.Fatal("Mirrored node not found")
	}
	det := nodes[idx].World.Mat3().Det()
	if det >= 0 {
		t.Errorf("Mirrored.World determinant = %v, want negative (a mirror)", det)
	}

	// Also true for this specific file (see the comment above for why this
	// is incidental rather than the general rule): every authored scale
	// component came out negative.
	s := doc.Nodes[idx].ScaleOrDefault()
	if s[0] >= 0 && s[1] >= 0 && s[2] >= 0 {
		t.Errorf("Mirrored node's authored scale = %v, want at least one negative component", s)
	}
}

// checkParentChain is the one check here that a PARENTED node's World is
// its parent's matrix times its own, in that order, on what Blender wrote.
//
// It was missing, and breaking the order showed it: with resolveWorld
// composing child * parent instead, every other check in this file passed,
// because nothing else here asserts on a node that has a parent -- the lights
// and the buildings are all top level. (The in-memory tests did catch it, so
// the engine was covered; the claim this FILE makes, that the loader agrees
// with real Blender output, was not.) ShearChild is the node that can tell:
// a stretched parent and a rotated child do not commute, which is the whole
// reason it is sheared. The two collection-instance children cannot, since a
// parent that only translates gives the same answer either way round.
//
// By hand from build_fixture.py: the parent is at Blender (-8, -6, 1) with
// scale (1, 1, 3), which is glTF (-8, 1, 6) and (1, 3, 1); the child sits 0.6
// up the parent's Z, glTF (0, 0.6, 0), rotated 45 degrees about X. So
//
//	World = T(-8,1,6) * S(1,3,1) * T(0,0.6,0) * Rx(45)
//
// puts the child at (-8, 1 + 3*0.6, 6) and makes its local Y axis
// S * (0, cos45, sin45) = (0, 2.1213, 0.7071). The wrong order gives
// Rx * S * (0,1,0) = (0, 2.1213, 2.1213) for that axis instead.
//
// Verified to fail that way: "ShearChild world Y axis = [0 2.1213202
// 2.1213205], want [0 2.1213 0.7071]".
func checkParentChain(t *testing.T, doc *gltf.Document) {
	nodes := extractNodes(doc)
	idx := nodeIndexByName(doc, "ShearChild")
	if idx < 0 {
		t.Fatal("ShearChild node not found")
	}
	if p := nodes[idx].Parent; p < 0 || nodes[p].Name != "ShearParent" {
		t.Fatalf("ShearChild's parent is node %d, want ShearParent", p)
	}
	w := nodes[idx].World
	if pos := (mgl32.Vec3{w[12], w[13], w[14]}); !closeF(pos[0], -8, 1e-4) || !closeF(pos[1], 2.8, 1e-4) || !closeF(pos[2], 6, 1e-4) {
		t.Errorf("ShearChild world position = %v, want [-8 2.8 6]", pos)
	}
	if y := (mgl32.Vec3{w[4], w[5], w[6]}); !closeF(y[0], 0, 1e-4) || !closeF(y[1], 2.1213, 1e-3) || !closeF(y[2], 0.7071, 1e-3) {
		t.Errorf("ShearChild world Y axis = %v, want [0 2.1213 0.7071]", y)
	}

	// The collection instances: each child lands where its instancing empty
	// is, which is the part of the chain they CAN vouch for.
	for name, want := range map[string]mgl32.Vec3{
		"PropCollectionInstance":  {8, 0, 6},
		"PropCollectionInstance2": {10, 2, 6},
	} {
		pi := nodeIndexByName(doc, name)
		if pi < 0 {
			t.Fatalf("%s node not found", name)
		}
		found := false
		for i := range nodes {
			if nodes[i].Parent != pi {
				continue
			}
			found = true
			cw := nodes[i].World
			if pos := (mgl32.Vec3{cw[12], cw[13], cw[14]}); pos.Sub(want).Len() > 1e-4 {
				t.Errorf("%s's child is at %v, want %v", name, pos, want)
			}
		}
		if !found {
			t.Errorf("%s has no child node; Blender wrote its instance as one", name)
		}
	}
}

// checkLoadedTilingAndAlpha is what checkTextureTransform and
// checkAlphaAndDoubleSided were written to become. Those two assert on the raw
// document, because when #67 committed this file the loader read neither; this
// asserts on what the loader makes of the same bytes now that it does (#68,
// #69).
//
// Tiling: the ground is a plane with UVs 0..1 and a Mapping node scaled 8x,
// which Blender writes as scale (8,8), offset (0,-7). Baked, u' = 8u and
// v' = 8v - 7, so the primitive's UVs must span exactly 0..8 and -7..1 -- eight
// tiles each way. The -7 is not decoration: a reader that drops the offset
// spans 0..8 on V and draws the same eight tiles shifted by a whole number of
// them, which is invisible here and wrong the moment the scale is not an
// integer. So V's RANGE is asserted, not just its width.
//
// Alpha: the pane Blender was given Principled alpha 0.3 for reports blend and
// 0.3, and a plain building reports opaque, 1, and glTF's default cutoff.
//
// Verified to fail: with applyUVTransform ignoring the offset (c and f), V
// spans 0..8 and this reports "Ground V spans 0..8, want -7..1"; with
// resolveAlpha returning before it reads the material, GlassPane reports
// "GlassPane loads as OPAQUE alpha 1, want blend 0.3".
func checkLoadedTilingAndAlpha(t *testing.T, doc *gltf.Document) {
	meshOf := func(node string) (*gltf.Mesh, int) {
		idx := nodeIndexByName(doc, node)
		if idx < 0 || doc.Nodes[idx].Mesh == nil {
			t.Fatalf("%s: no such mesh node", node)
		}
		m := doc.Meshes[*doc.Nodes[idx].Mesh]
		if len(m.Primitives) == 0 || m.Primitives[0].Material == nil {
			t.Fatalf("%s: mesh has no primitive with a material", node)
		}
		return m, int(*m.Primitives[0].Material)
	}

	ground, groundMat := meshOf("Ground")
	uv := resolveUVTransform("level.glb", doc, map[int]uvAffine{}, groundMat)
	// extractPrimitive never touches its receiver; see
	// TestExtractPrimitiveBakesUVPerCopy.
	var r *Renderer
	verts, _, err := r.extractPrimitive(doc, ground.Primitives[0], uv)
	if err != nil {
		t.Fatalf("extract Ground: %v", err)
	}
	minU, maxU := verts[0].UV[0], verts[0].UV[0]
	minV, maxV := verts[0].UV[1], verts[0].UV[1]
	for _, v := range verts {
		minU, maxU = min(minU, v.UV[0]), max(maxU, v.UV[0])
		minV, maxV = min(minV, v.UV[1]), max(maxV, v.UV[1])
	}
	if !closeF(minU, 0, 1e-4) || !closeF(maxU, 8, 1e-4) {
		t.Errorf("Ground U spans %v..%v, want 0..8", minU, maxU)
	}
	if !closeF(minV, -7, 1e-4) || !closeF(maxV, 1, 1e-4) {
		t.Errorf("Ground V spans %v..%v, want -7..1", minV, maxV)
	}

	_, glassMat := meshOf("GlassPane")
	if mode, _, alpha := resolveAlpha(doc, glassMat); mode != AlphaModeBlend || !closeF(alpha, 0.3, 1e-3) {
		t.Errorf("GlassPane loads as %v alpha %v, want blend 0.3", mode, alpha)
	}
	_, wallMat := meshOf("Building")
	if mode, cutoff, alpha := resolveAlpha(doc, wallMat); mode != AlphaModeOpaque || alpha != 1 || cutoff != 0.5 {
		t.Errorf("Building loads as %v alpha %v cutoff %v, want opaque 1 0.5", mode, alpha, cutoff)
	}
}

// checkInstancing records what the two instancing probes turned into --
// findings for issue #71, not engine behaviour (the engine reads none of
// this specially; Model.NodeMeshes already handles "several nodes, one doc
// mesh" regardless of how the nodes got that way).
//
// Collection instance: build_collection_instance() creates the SAME
// collection (holding one "Prop" object) instanced by two empties,
// PropCollectionInstance and PropCollectionInstance2. This file's actual
// bytes show that arrives as each instancing empty gaining a CHILD node --
// named "Prop", one per instance -- rather than any instancing extension;
// both children reference the SAME doc mesh, which is exactly the
// Model.NodeMeshes shape (see docs/agents/models.md) the engine already
// has a tool for. EXT_mesh_gpu_instancing is NOT used anywhere in this
// file despite export_gpu_instances being on -- that option recognises
// Blender's own particle/geometry-node "instancer" flag, not plain
// collection instancing or two objects sharing a mesh datablock (Building /
// Building_Linked above), and this fixture triggers neither.
//
// Geometry-nodes scatter: build_geometry_nodes_scatter() scatters the same
// Prop object at 3 points via Mesh to Points -> Instance on Points. Tried
// first WITHOUT a Realize Instances node: the exported GNSourcePoints node
// carried no mesh at all -- "Apply Modifiers" evaluates the modifier's
// output, unrealized instances evaluate to an empty mesh, and the exporter
// omits an empty mesh rather than writing an empty one. With Realize
// Instances added (what ships in this fixture), GNSourcePoints exports as
// ONE baked mesh merging all 3 Cone copies at their scattered positions
// (384 vertices = 3 x Cone's 128) -- not 3 separate nodes, and still no
// EXT_mesh_gpu_instancing.
//
// BROKEN: changed the GNSourcePoints vertex-count assertion to `!= 999`
// (an impossible value, so it always fails) to confirm the accessor is
// actually being read rather than skipped -- FAILED with: "GNSourcePoints
// POSITION accessor count = 384, want 999". Restored to the real assertion
// (`!= 384`).
func checkInstancing(t *testing.T, doc *gltf.Document) {
	inst1 := nodeIndexByName(doc, "PropCollectionInstance")
	inst2 := nodeIndexByName(doc, "PropCollectionInstance2")
	if inst1 < 0 || inst2 < 0 {
		t.Fatal("PropCollectionInstance or PropCollectionInstance2 not found")
	}
	if len(doc.Nodes[inst1].Children) != 1 || len(doc.Nodes[inst2].Children) != 1 {
		t.Fatalf("collection instance nodes have children %v / %v, want exactly one each",
			doc.Nodes[inst1].Children, doc.Nodes[inst2].Children)
	}
	child1 := doc.Nodes[inst1].Children[0]
	child2 := doc.Nodes[inst2].Children[0]
	if doc.Nodes[child1].Name != "Prop" || doc.Nodes[child2].Name != "Prop" {
		t.Errorf("collection instance children named %q / %q, want both %q",
			doc.Nodes[child1].Name, doc.Nodes[child2].Name, "Prop")
	}
	if doc.Nodes[child1].Mesh == nil || doc.Nodes[child2].Mesh == nil || *doc.Nodes[child1].Mesh != *doc.Nodes[child2].Mesh {
		t.Errorf("collection instance children reference different meshes, want the same doc mesh")
	}

	for _, name := range []string{"EXT_mesh_gpu_instancing"} {
		for _, used := range doc.ExtensionsUsed {
			if used == name {
				t.Errorf("extensionsUsed contains %q, want it absent -- this fixture's instancing probes do not trigger it (see comment above)", name)
			}
		}
	}

	gnIdx := nodeIndexByName(doc, "GNSourcePoints")
	if gnIdx < 0 {
		t.Fatal("GNSourcePoints node not found")
	}
	if doc.Nodes[gnIdx].Mesh == nil {
		t.Fatal("GNSourcePoints carries no mesh -- Realize Instances must be missing from tools/blender/build_fixture.py's node group (see comment above)")
	}
	mesh := doc.Meshes[*doc.Nodes[gnIdx].Mesh]
	if len(mesh.Primitives) != 1 {
		t.Fatalf("GNSourcePoints mesh has %d primitives, want 1", len(mesh.Primitives))
	}
	posAccessor, ok := mesh.Primitives[0].Attributes[gltf.POSITION]
	if !ok {
		t.Fatal("GNSourcePoints primitive has no POSITION attribute")
	}
	if got := doc.Accessors[posAccessor].Count; got != 384 {
		t.Errorf("GNSourcePoints POSITION accessor count = %d, want 384 (3 scattered copies of Cone's 128 vertices)", got)
	}
}
