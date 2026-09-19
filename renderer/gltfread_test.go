package renderer

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"testing"
	"testing/fstest"
	"unsafe"

	"github.com/go-gl/mathgl/mgl32"
	"github.com/qmuntal/gltf"
	"github.com/qmuntal/gltf/modeler"
)

// geometryDigest hashes a primitive's decoded geometry as the bytes that
// actually reach the GPU: the Vertex array exactly as CreateIndexedMesh
// uploads it (44 bytes each, no padding -- eleven float32s), then the uint32
// indices.
//
// Hashing the upload bytes rather than comparing field by field is the point:
// a change that moved a UV, flipped a winding, dropped a vertex colour or
// reordered the struct would all be invisible to a spot check on a few
// vertices and all show up here.
func geometryDigest(verts []Vertex, idx []uint32) string {
	h := sha256.New()
	if len(verts) > 0 {
		h.Write(unsafe.Slice((*byte)(unsafe.Pointer(&verts[0])), len(verts)*44))
	}
	if len(idx) > 0 {
		h.Write(unsafe.Slice((*byte)(unsafe.Pointer(&idx[0])), len(idx)*4))
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// goldenPrimitive is one expected primitive of a fixture: its material name,
// its vertex and index counts, and the digest above.
type goldenPrimitive struct {
	name   string
	verts  int
	idx    int
	digest string
}

// blenderLevelGolden and exampleLevelGolden are what the loader produced
// BEFORE issue #75 split LoadGLTF into a read and an upload -- captured on
// origin/main at bdedd33 by running that revision's own decode path
// (resolveUVTransform, then extractPrimitive on a nil *Renderer, then the
// winding reversal) over these two committed files and hashing the result.
//
// They exist because the refactor's whole risk is silent: a read path that
// produces slightly different vertices still loads, still draws, and still
// passes every other test in this package. Nothing else in the suite compares
// decoded geometry against a number from before the change.
//
// If one of these fails after a deliberate change to the decode -- a new
// attribute, a different default, another baked extension -- the fixture's
// geometry really did move, and the number has to be recaptured on the
// commit that moved it rather than edited to match.
var blenderLevelGolden = []goldenPrimitive{
	{"Ground", 4, 6, "afb7abd49fe456072f59bd3caf358f6b"},
	{"Stone", 24, 36, "e7749b56ebe4a1551a87021a87405295"},
	{"Glass", 4, 6, "7d03b4aeea069e79020b9879ca015335"},
	{"Culled", 4, 6, "7d03b4aeea069e79020b9879ca015335"},
	{"Open", 4, 6, "7d03b4aeea069e79020b9879ca015335"},
	{"Mirrored", 24, 36, "3efcb96e84878a85dc31c4f9601ec61a"},
	{"ShearChild", 24, 36, "bf4bfc55d9591d6205bcb0de78f98d8c"},
	{"Prop", 128, 186, "744239436f0f2876d07deac7e0d1ed80"},
	{"Prop", 384, 558, "7a0a28b53558c2afdaee640ba2a073e1"},
}

var exampleLevelGolden = []goldenPrimitive{
	{"Ground", 24, 36, "ce5c13f76358d0b07d02ad2bece398da"},
	{"Stone", 24, 36, "6fa100dcf4ae6d4aad87aa9fc526f666"},
	{"DarkMetal", 24, 36, "d8263a2f7db61267ba5121c35cd5ba1a"},
}

// dirFS opens a committed fixture the way a caller would: an fs.FS rooted at
// the file's own directory, since glTF resolves its buffers and images
// relative to itself.
func dirFS(t *testing.T, dir, name string) (fs.FS, string) {
	t.Helper()
	if _, err := os.Stat(dir + "/" + name); err != nil {
		t.Fatalf("fixture %s/%s: %v", dir, name, err)
	}
	return os.DirFS(dir), name
}

// TestReadGLTFMatchesGoldenGeometry is the evidence that splitting LoadGLTF
// into ReadGLTF + upload (issue #75) did not move a single decoded byte.
//
// BROKEN: deleted the `reverseWinding(g.idx)` call from readPrimitive in
// gltfread.go. FAILED on all twelve primitives, the first being:
// "testdata/blender/level.glb primitive 0 (Ground): digest =
// b40a52f335aa6bdba68454b4420da27a, want afb7abd49fe456072f59bd3caf358f6b
// (captured on origin/main bdedd33)". Restored with
// `git checkout -- renderer/gltfread.go`.
func TestReadGLTFMatchesGoldenGeometry(t *testing.T) {
	for _, tc := range []struct {
		dir, name string
		want      []goldenPrimitive
	}{
		{"testdata/blender", "level.glb", blenderLevelGolden},
		{"../examples/22-level/assets", "level.glb", exampleLevelGolden},
	} {
		label := tc.dir + "/" + tc.name
		fsys, name := dirFS(t, tc.dir, tc.name)
		model, err := ReadGLTF(fsys, name)
		if err != nil {
			t.Fatalf("%s: ReadGLTF: %v", label, err)
		}
		if len(model.Meshes) != len(tc.want) {
			t.Fatalf("%s: %d primitives, want %d", label, len(model.Meshes), len(tc.want))
		}
		for i, want := range tc.want {
			mm := model.Meshes[i]
			if mm.Name != want.name {
				t.Errorf("%s primitive %d: Name = %q, want %q", label, i, mm.Name, want.name)
			}
			if len(mm.Verts) != want.verts || len(mm.Idx) != want.idx {
				t.Errorf("%s primitive %d (%s): %d verts / %d indices, want %d / %d",
					label, i, want.name, len(mm.Verts), len(mm.Idx), want.verts, want.idx)
			}
			if got := geometryDigest(mm.Verts, mm.Idx); got != want.digest {
				t.Errorf("%s primitive %d (%s): digest = %s, want %s (captured on origin/main bdedd33)",
					label, i, want.name, got, want.digest)
			}
		}
	}
}

// TestReadGLTFBlenderFixture reads the real-Blender fixture with no device and
// asserts it carries what gltfblender_test.go's extractor checks assert on the
// raw document -- the same node graph, the same two lights, and the same baked
// tiling and alpha that checkLoadedTilingAndAlpha proves on Ground and
// GlassPane. Those go through resolveUVTransform/extractPrimitive/resolveAlpha
// directly; this is the same facts arriving through the public GPU-free door a
// server or a tool would use.
//
// BROKEN: made readPrimitive pass identityUV to extractPrimitive instead of
// the uv resolveUVTransform returned. FAILED with: "Ground V spans 0..1
// through ReadGLTF, want -7..1 (KHR_texture_transform baked)". Restored with
// `git checkout -- renderer/gltfread.go`.
func TestReadGLTFBlenderFixture(t *testing.T) {
	fsys, name := dirFS(t, "testdata/blender", "level.glb")
	model, err := ReadGLTF(fsys, name)
	if err != nil {
		t.Fatalf("ReadGLTF: %v", err)
	}

	if len(model.Nodes) != 17 {
		t.Fatalf("read %d nodes, want 17 (the same count checkNodesAndExtras asserts)", len(model.Nodes))
	}
	building, ok := model.Node("Building")
	if !ok {
		t.Fatal("no Building node in the read model")
	}
	if len(building.Extras) == 0 {
		t.Error("Building.Extras is empty through ReadGLTF; extras are the reason a server reads a level at all")
	}
	ground, ok := model.Node("Ground")
	if !ok {
		t.Fatal("no Ground node in the read model")
	}
	if ground.Extras != nil {
		t.Errorf("Ground.Extras = %s, want nil", ground.Extras)
	}

	if len(model.Lights) != 2 {
		t.Fatalf("read %d lights, want 2 (SpotLamp, PointLamp)", len(model.Lights))
	}

	// Ground's UVs must already carry the 8x tile and the -7 V offset
	// Blender wrote as KHR_texture_transform -- the bake happens in the read,
	// not in the upload, which is exactly what a tool measuring UV density
	// off a read model depends on.
	gi := model.NodeMeshes(nodeIndexByNameIn(model, "Ground"))
	if len(gi) != 1 {
		t.Fatalf("Ground instances %d primitives, want 1", len(gi))
	}
	minV, maxV := model.Meshes[gi[0]].Verts[0].UV[1], model.Meshes[gi[0]].Verts[0].UV[1]
	for _, v := range model.Meshes[gi[0]].Verts {
		minV, maxV = min(minV, v.UV[1]), max(maxV, v.UV[1])
	}
	if !closeF(minV, -7, 1e-4) || !closeF(maxV, 1, 1e-4) {
		t.Errorf("Ground V spans %v..%v through ReadGLTF, want -7..1 (KHR_texture_transform baked)", minV, maxV)
	}

	// Alpha and double-sidedness are surface data, not GPU data, so they have
	// to survive a read.
	byName := map[string]ModelMesh{}
	for _, mm := range model.Meshes {
		byName[mm.Name] = mm
	}
	if glass := byName["Glass"]; glass.AlphaMode != AlphaModeBlend || !closeF(glass.BaseAlpha, 0.3, 1e-3) {
		t.Errorf("Glass reads as %v alpha %v, want BLEND 0.3", glass.AlphaMode, glass.BaseAlpha)
	}
	if stone := byName["Stone"]; !stone.DoubleSided {
		t.Error("Stone reads DoubleSided = false, want true (Backface Culling was not ticked)")
	}
	if culled := byName["Culled"]; culled.DoubleSided {
		t.Error("Culled reads DoubleSided = true, want false (Backface Culling was ticked)")
	}
}

// nodeIndexByNameIn is nodeIndexByName over a read Model rather than a
// document.
func nodeIndexByNameIn(m *Model, name string) int {
	for i := range m.Nodes {
		if m.Nodes[i].Name == name {
			return i
		}
	}
	return -1
}

// TestReadGLTFHasNoGPUHandles asserts the promise in ReadGLTF's doc comment:
// every GPU handle is nil, on both fixtures, so a caller that forgets it has a
// read model gets a Go nil rather than a driver crash.
//
// BROKEN: added `mm.Texture = &Texture{}` to readPrimitive, just before it
// appends. FAILED once per primitive, starting: "testdata/blender/level.glb
// primitive 0 (Ground): Texture is non-nil on a read model". Restored with
// `git checkout -- renderer/gltfread.go`.
func TestReadGLTFHasNoGPUHandles(t *testing.T) {
	for _, tc := range []struct{ dir, name string }{
		{"testdata/blender", "level.glb"},
		{"../examples/22-level/assets", "level.glb"},
	} {
		label := tc.dir + "/" + tc.name
		fsys, name := dirFS(t, tc.dir, tc.name)
		model, err := ReadGLTF(fsys, name)
		if err != nil {
			t.Fatalf("%s: ReadGLTF: %v", label, err)
		}
		for i := range model.Meshes {
			mm := model.Meshes[i]
			if mm.Mesh != nil {
				t.Errorf("%s primitive %d (%s): Mesh is non-nil on a read model", label, i, mm.Name)
			}
			if mm.Texture != nil {
				t.Errorf("%s primitive %d (%s): Texture is non-nil on a read model", label, i, mm.Name)
			}
			if mm.Material != nil {
				t.Errorf("%s primitive %d (%s): Material is non-nil on a read model", label, i, mm.Name)
			}
		}
	}
}

// brokenImageDoc builds, in memory, a one-triangle glTF whose material binds a
// base colour texture backed by an external file that is NOT an image.
//
// This is the document the image-decoding decision is proved on: a server
// asking a level where the doors are must not be made to decode a texture to
// find out, so ReadGLTF must open this file happily while the step LoadGLTF
// takes on it must fail.
func brokenImageDoc(t *testing.T) fs.FS {
	t.Helper()
	doc := &gltf.Document{Asset: gltf.Asset{Version: "2.0"}}
	posIdx := modeler.WritePosition(doc, [][3]float32{{0, 0, 0}, {1, 0, 0}, {0, 1, 0}})
	uvIdx := modeler.WriteTextureCoord(doc, [][2]float32{{0, 0}, {1, 0}, {0, 1}})
	idxIdx := modeler.WriteIndices(doc, []uint16{0, 1, 2})

	doc.Images = []*gltf.Image{{Name: "broken", URI: "broken.png"}}
	doc.Textures = []*gltf.Texture{{Source: gltf.Index(0)}}
	doc.Materials = []*gltf.Material{{
		Name: "Painted",
		PBRMetallicRoughness: &gltf.PBRMetallicRoughness{
			BaseColorTexture: &gltf.TextureInfo{Index: 0},
		},
	}}
	doc.Meshes = []*gltf.Mesh{{
		Name: "Slab",
		Primitives: []*gltf.Primitive{{
			Attributes: gltf.PrimitiveAttributes{gltf.POSITION: posIdx, gltf.TEXCOORD_0: uvIdx},
			Indices:    gltf.Index(idxIdx),
			Material:   gltf.Index(0),
			Mode:       gltf.PrimitiveTriangles,
		}},
	}}
	doc.Nodes = []*gltf.Node{{Name: "SlabNode", Mesh: gltf.Index(0)}}
	doc.Scenes = []*gltf.Scene{{Nodes: []int{0}}}

	var buf bytes.Buffer
	if err := gltf.NewEncoder(&buf).Encode(doc); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return fstest.MapFS{
		"model.glb":  &fstest.MapFile{Data: buf.Bytes()},
		"broken.png": &fstest.MapFile{Data: []byte("this is not a PNG, and is not meant to be")},
	}
}

// TestReadGLTFDoesNotDecodeImages proves the image-decoding decision rather
// than asserting it: the same file reads clean and fails the decode step.
//
// decodeGLTFImages is the exact call LoadGLTF makes (through
// uploadGLTFImages) before it uploads anything, and it is a pure function, so
// this half of the proof needs no device either -- which is why the claim
// "ReadGLTF would work where LoadGLTF would not" can be checked in `task ci`
// rather than only on a machine with a GPU.
//
// BROKEN: called decodeGLTFImages from openRead -- the read half -- and
// returned its error. FAILED with: "ReadGLTF on a document with garbage image
// bytes: decode image 0 (broken): image: unknown format -- the read must not
// decode images". Restored with `git checkout -- renderer/gltfread.go`.
func TestReadGLTFDoesNotDecodeImages(t *testing.T) {
	fsys := brokenImageDoc(t)

	model, err := ReadGLTF(fsys, "model.glb")
	if err != nil {
		t.Fatalf("ReadGLTF on a document with garbage image bytes: %v -- the read must not decode images", err)
	}
	if len(model.Meshes) != 1 {
		t.Fatalf("read %d primitives, want 1", len(model.Meshes))
	}
	if model.Meshes[0].Name != "Painted" {
		t.Errorf("primitive Name = %q, want %q -- the material was read even though its image was not", model.Meshes[0].Name, "Painted")
	}
	if len(model.Meshes[0].Verts) != 3 {
		t.Errorf("read %d verts, want 3", len(model.Meshes[0].Verts))
	}

	// The other half: the step LoadGLTF takes on the same bytes must fail.
	// Without this the test above only says "ReadGLTF succeeded", which it
	// would also do if the file's image were perfectly valid.
	doc, base, err := openGLTF(fsys, "model.glb")
	if err != nil {
		t.Fatalf("openGLTF: %v", err)
	}
	if _, err := decodeGLTFImages(doc, base); err == nil {
		t.Fatal("decodeGLTFImages accepted garbage image bytes -- this test proves nothing about the read skipping them")
	} else {
		t.Logf("upload-side decode fails as expected: %v", err)
	}
}

// TestReadGLTFExternalImageIsNotEvenRead goes one step further than the decode
// test: it removes the image FILE from the filesystem entirely, so a read that
// so much as opened it would fail with a missing-file error.
//
// This is the property a dedicated server actually needs. A level shipped to a
// server without its textures is the normal case, not a corrupt one.
//
// BROKEN: the same break as TestReadGLTFDoesNotDecodeImages (decodeGLTFImages
// called from openRead). FAILED with: "ReadGLTF with the image file absent:
// read external image 0 (broken.png): open broken.png: file does not exist" --
// note it never reached the decode, which is the stronger claim this test
// makes. Restored with `git checkout -- renderer/gltfread.go`.
func TestReadGLTFExternalImageIsNotEvenRead(t *testing.T) {
	full := brokenImageDoc(t).(fstest.MapFS)
	textureless := fstest.MapFS{"model.glb": full["model.glb"]}

	if _, err := ReadGLTF(textureless, "model.glb"); err != nil {
		t.Fatalf("ReadGLTF with the image file absent: %v", err)
	}

	doc, base, err := openGLTF(textureless, "model.glb")
	if err != nil {
		t.Fatalf("openGLTF: %v", err)
	}
	if _, err := decodeGLTFImages(doc, base); err == nil {
		t.Fatal("decodeGLTFImages succeeded with the image file absent -- this test proves nothing")
	} else if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("decodeGLTFImages error = %v, want one wrapping fs.ErrNotExist", err)
	}
}

// TestReadModelPureMethodsWork walks every Model method that is pure
// arithmetic over what a read carries, on a read model, because "anything that
// would nil-deref on a read Model is a bug to fix or document -- found by
// test, not by inspection" is the only way that claim is worth anything.
//
// Renderer.CombineModel is deliberately absent: it uploads, so it needs a
// device by definition. It is named in docs/agents/models.md as the one thing
// on this list a read model cannot do.
//
// BROKEN: made Model.Bounds return `[3]float32{}, [3]float32{}, false`
// unconditionally. FAILED with: "Bounds on a read model: ok = false, want true
// (the read retains Verts)". Restored with
// `git checkout -- renderer/model.go`.
func TestReadModelPureMethodsWork(t *testing.T) {
	fsys, name := dirFS(t, "testdata/blender", "level.glb")
	model, err := ReadGLTF(fsys, name)
	if err != nil {
		t.Fatalf("ReadGLTF: %v", err)
	}

	minB, maxB, ok := model.Bounds()
	if !ok {
		t.Fatal("Bounds on a read model: ok = false, want true (the read retains Verts)")
	}
	if !(maxB[0] > minB[0] && maxB[1] > minB[1] && maxB[2] > minB[2]) {
		t.Errorf("Bounds on a read model = %v..%v, want a non-degenerate box", minB, maxB)
	}

	spot, ok := model.Node("SpotLamp")
	if !ok {
		t.Fatal("Node(\"SpotLamp\") on a read model: not found")
	}
	if _, ok := model.Node("NoSuchNodeAnywhere"); ok {
		t.Error("Node returned ok for a name the file does not carry")
	}

	groundIdx := nodeIndexByNameIn(model, "Ground")
	gm := model.NodeMeshes(groundIdx)
	if len(gm) != 1 {
		t.Fatalf("NodeMeshes(Ground) = %v, want one primitive", gm)
	}
	if model.NodeMeshes(-1) != nil || model.NodeMeshes(len(model.Nodes)) != nil {
		t.Error("NodeMeshes out of range returned something; it must return nil")
	}

	// NodeInMeshSpace inverts the mesh's owning node's World, which is the
	// call that would panic on a model whose Nodes did not survive the read.
	local := model.NodeInMeshSpace(spot, model.Meshes[gm[0]])
	if local == (mgl32.Mat4{}) {
		t.Error("NodeInMeshSpace returned the zero matrix on a read model")
	}

	for _, l := range model.Lights {
		pos, dir := model.LightWorldPosDir(l)
		if dir.Len() < 0.99 {
			t.Errorf("LightWorldPosDir(%q): dir = %v, want a unit vector", l.Name, dir)
		}
		_ = pos
	}

	// ReleaseGeometry is the one that changes the model, so it goes last.
	model.ReleaseGeometry()
	if _, _, ok := model.Bounds(); ok {
		t.Error("Bounds still answers after ReleaseGeometry on a read model")
	}
	if len(model.Nodes) == 0 {
		t.Error("ReleaseGeometry dropped Nodes; it must leave them alone")
	}
	if _, ok := model.Node("SpotLamp"); !ok {
		t.Error("a node looked up before ReleaseGeometry is gone after it")
	}
}

// TestReadGLTFSkinnedSplitsCleanly checks the skinned read against the one
// skinned asset in the repo: the skeleton, the animations and the armature
// root transform all arrive with no device, and the skinned primitives carry
// their material name but no Verts -- the documented gap (SkinnedVertex is a
// different layout), asserted rather than assumed.
//
// BROKEN: made readGLTFSkinned's pass 1 call readPrimitive with skinned=false.
// FAILED with: "primitive 0 (colormap): Skinned = false, want true",
// "primitive 0: 462 Verts, want 0 -- a skinned primitive decodes to
// SkinnedVertex" and "Bounds answered for a purely skinned read model; it must
// report that it cannot". Restored with
// `git checkout -- renderer/gltfread.go`.
func TestReadGLTFSkinnedSplitsCleanly(t *testing.T) {
	fsys, name := dirFS(t, "../examples/06-skinned/assets", "character.glb")
	sm, err := ReadGLTFSkinned(fsys, name)
	if err != nil {
		t.Fatalf("ReadGLTFSkinned: %v", err)
	}
	if sm.Skeleton == nil || len(sm.Skeleton.Joints) == 0 {
		t.Fatal("ReadGLTFSkinned returned no joints")
	}
	if len(sm.Animations) == 0 {
		t.Fatal("ReadGLTFSkinned returned no animation clips")
	}
	if len(sm.Meshes) != 2 {
		t.Fatalf("read %d primitives, want 2", len(sm.Meshes))
	}
	for i := range sm.Meshes {
		mm := sm.Meshes[i]
		if !mm.Skinned {
			t.Errorf("primitive %d (%s): Skinned = false, want true", i, mm.Name)
		}
		if mm.Name != "colormap" {
			t.Errorf("primitive %d: Name = %q, want %q", i, mm.Name, "colormap")
		}
		if len(mm.Verts) != 0 {
			t.Errorf("primitive %d: %d Verts, want 0 -- a skinned primitive decodes to SkinnedVertex", i, len(mm.Verts))
		}
		if mm.Mesh != nil {
			t.Errorf("primitive %d: Mesh is non-nil on a read model", i)
		}
	}
	// Bounds cannot answer for a purely skinned model, the same as through
	// LoadGLTFSkinned (docs/agents/models.md).
	if _, _, ok := sm.Bounds(); ok {
		t.Error("Bounds answered for a purely skinned read model; it must report that it cannot")
	}
}
