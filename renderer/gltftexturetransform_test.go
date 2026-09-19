package renderer

import (
	"bytes"
	"math"
	"testing"

	"github.com/qmuntal/gltf"
	"github.com/qmuntal/gltf/ext/texturetransform"
	"github.com/qmuntal/gltf/modeler"
)

// TestTextureTransformRoundTrip proves the premise the rest of this file
// depends on: that ext/texturetransform's init() registers its Unmarshal so
// a TextureInfo's Extensions["KHR_texture_transform"] decodes to
// *texturetransform.TextureTranform, through the REAL encoder and decoder
// (gltf.NewEncoder/NewDecoder) rather than by reading the package and
// assuming -- the same standard TestExtractLightsRoundTrip in
// gltflights_test.go holds KHR_lights_punctual to, and what issue #69
// explicitly calls for ("confirm how it decodes with a real round trip").
//
// The fixture is the exact numbers issue #69 measured from a real Blender
// 5.0.1 export of an 8x8 Mapping-node scale: offset (0,-7), scale (8,8), no
// rotation.
func TestTextureTransformRoundTrip(t *testing.T) {
	doc := &gltf.Document{
		Asset:          gltf.Asset{Version: "2.0"},
		ExtensionsUsed: []string{texturetransform.ExtensionName},
		Materials: []*gltf.Material{{
			Name: "ground",
			PBRMetallicRoughness: &gltf.PBRMetallicRoughness{
				BaseColorTexture: &gltf.TextureInfo{
					Index: 0,
					Extensions: gltf.Extensions{
						texturetransform.ExtensionName: texturetransform.TextureTranform{
							Offset: [2]float64{0, -7},
							Scale:  [2]float64{8, 8},
						},
					},
				},
			},
		}},
		Textures: []*gltf.Texture{{Source: gltf.Index(0)}},
		Images:   []*gltf.Image{{Name: "checker", URI: "checker.png"}},
	}

	var buf bytes.Buffer
	if err := gltf.NewEncoder(&buf).Encode(doc); err != nil {
		t.Fatalf("encode: %v", err)
	}
	got := new(gltf.Document)
	if err := gltf.NewDecoder(bytes.NewReader(buf.Bytes())).Decode(got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Prove the premise directly before trusting materialUVTransform to
	// have used it correctly.
	raw, ok := got.Materials[0].PBRMetallicRoughness.BaseColorTexture.Extensions[texturetransform.ExtensionName]
	if !ok {
		t.Fatal("decoded baseColorTexture lost its KHR_texture_transform extension")
	}
	if _, ok := raw.(*texturetransform.TextureTranform); !ok {
		t.Fatalf("decoded as %T, want *texturetransform.TextureTranform", raw)
	}

	m, found, chosenName, skipped, differing := materialUVTransform(got, 0)
	if !found {
		t.Fatal("found = false, want true")
	}
	if chosenName != "baseColorTexture" {
		t.Errorf("chosenName = %q, want baseColorTexture", chosenName)
	}
	if len(skipped) != 0 || len(differing) != 0 {
		t.Errorf("skipped=%v differing=%v, want both empty (only one map)", skipped, differing)
	}

	// The measured Blender fixture: u'=8u, v'=8v-7. Applied to the plane's
	// authored 0..1 UVs.
	uvs := [][2]float32{{0, 0}, {1, 0}, {0, 1}, {1, 1}}
	applyUVTransform(uvs, m)
	want := [][2]float32{{0, -7}, {8, -7}, {0, 1}, {8, 1}}
	for i := range uvs {
		if uvs[i] != want[i] {
			t.Errorf("uvs[%d] = %v, want %v", i, uvs[i], want[i])
		}
	}
}

// It has teeth against the scale/offset ORDER (translation * rotation *
// scale, not the other way around): multiplying composeUVTransform's c/f
// offset terms by scale as well -- what you would get from scaling the
// offset before translating, i.e. applying scale to translation too --
// reports "uvs[0] = [0 -56], want [0 -7]" against this exact measured
// fixture (offset -7 times scale 8 is -56). Introduced and reverted to
// confirm.

// TestComposeUVTransformRotation checks composeUVTransform's rotation term
// by hand, independent of any Blender-measured fixture -- issue #69 asks for
// KHR_texture_transform's rotation to be applied, and no shipped or scratch
// asset happens to use one with a value this test can cross-check against,
// so this is worked from the spec's own formula:
//
//	u' = cos(r)*scale.x*u - sin(r)*scale.y*v
//	v' = sin(r)*scale.x*u + cos(r)*scale.y*v
//
// at r = 30 degrees, scale (1,1), offset (0,0):
//
//	u=1,v=0 -> u'=cos(30)=0.8660254, v'=sin(30)=0.5
//	u=0,v=1 -> u'=-sin(30)=-0.5,      v'=cos(30)=0.8660254
//
// It has teeth: negating composeUVTransform's `d` term (the sin(r)*scale[0]
// coefficient) flips v' = d*u + e*v wherever d*u is nonzero, which the FIRST
// checked point already catches (u=1,v=0 makes v'=d, so it flips 0.5 to
// -0.5) -- checked by actually breaking it rather than assumed, since a
// first guess here said only the second point would move and that guess
// was wrong. Introduced and reverted to confirm.
func TestComposeUVTransformRotation(t *testing.T) {
	tt := texturetransform.TextureTranform{
		Scale:    texturetransform.DefaultScale,
		Rotation: math.Pi / 6, // 30 degrees
	}
	m := composeUVTransform(tt)

	uvs := [][2]float32{{1, 0}, {0, 1}}
	applyUVTransform(uvs, m)

	const eps = 1e-5
	wantA := [2]float32{0.8660254, 0.5}
	wantB := [2]float32{-0.5, 0.8660254}
	if math.Abs(float64(uvs[0][0]-wantA[0])) > eps || math.Abs(float64(uvs[0][1]-wantA[1])) > eps {
		t.Errorf("(1,0) -> %v, want %v", uvs[0], wantA)
	}
	if math.Abs(float64(uvs[1][0]-wantB[0])) > eps || math.Abs(float64(uvs[1][1]-wantB[1])) > eps {
		t.Errorf("(0,1) -> %v, want %v", uvs[1], wantB)
	}
}

// TestApplyUVTransformIdentitySkipsWork checks the identityUV fast path:
// applyUVTransform must leave uvs completely untouched -- not merely close
// -- when the transform is identity, which is the G1 requirement (no shipped
// asset uses this extension) resting on this exact behaviour.
//
// It has teeth: removing the `if m == identityUV { return }` guard still
// passes this specific test (multiply-by-one-add-zero is exact for these
// values), which is why TestApplyUVTransformIdentityIsReallyANoOp below
// exists as a second, sharper check using a value the naive arithmetic
// actually corrupts.
func TestApplyUVTransformIdentitySkipsWork(t *testing.T) {
	uvs := [][2]float32{{0.25, 0.75}, {-3, 12.5}}
	orig := append([][2]float32(nil), uvs...)
	applyUVTransform(uvs, identityUV)
	for i := range uvs {
		if uvs[i] != orig[i] {
			t.Errorf("uvs[%d] = %v, want unchanged %v", i, uvs[i], orig[i])
		}
	}
}

// TestApplyUVTransformIdentityIsReallyANoOp guards the identity fast path
// with a value where "multiply by 1, add 0" is not float-exact for every
// input: -0.0. float32(1*-0.0 + 0*x + 0) normalizes to +0.0 in Go, which
// differs in sign bit from the original -0.0 even though both compare == 0.
//
// It has teeth: removing applyUVTransform's identity guard makes this test
// report "uvs[0] = [0 0], want unchanged [-0 0]" (the U component's sign bit
// flips) -- confirmed by actually removing the guard and reverting, not by
// reasoning about IEEE754 alone.
func TestApplyUVTransformIdentityIsReallyANoOp(t *testing.T) {
	uvs := [][2]float32{{float32(math.Copysign(0, -1)), 0}}
	applyUVTransform(uvs, identityUV)
	if math.Signbit(float64(uvs[0][0])) != math.Signbit(float64(math.Copysign(0, -1))) {
		t.Errorf("uvs[0][0] sign bit changed: got %v", uvs[0][0])
	}
}

// TestMaterialUVCandidatesOrder pins the fallback order issue #69 specifies:
// base colour, normal, metallic-roughness, occlusion, emissive.
func TestMaterialUVCandidatesOrder(t *testing.T) {
	mat := &gltf.Material{
		PBRMetallicRoughness: &gltf.PBRMetallicRoughness{
			BaseColorTexture:         &gltf.TextureInfo{Index: 0},
			MetallicRoughnessTexture: &gltf.TextureInfo{Index: 1},
		},
		NormalTexture:    &gltf.NormalTexture{Index: gltf.Index(2)},
		OcclusionTexture: &gltf.OcclusionTexture{Index: gltf.Index(3)},
		EmissiveTexture:  &gltf.TextureInfo{Index: 4},
	}
	got := materialUVCandidates(mat)
	want := []string{"baseColorTexture", "normalTexture", "metallicRoughnessTexture", "occlusionTexture", "emissiveTexture"}
	if len(got) != len(want) {
		t.Fatalf("got %d candidates, want %d", len(got), len(want))
	}
	for i, name := range want {
		if got[i].name != name {
			t.Errorf("candidate[%d] = %q, want %q", i, got[i].name, name)
		}
	}
}

// uvTT is a small constructor to keep the tests below to one line per
// candidate.
func uvTT(offsetX, offsetY, scaleX, scaleY float64) gltf.Extensions {
	return gltf.Extensions{
		texturetransform.ExtensionName: &texturetransform.TextureTranform{
			Offset: [2]float64{offsetX, offsetY},
			Scale:  [2]float64{scaleX, scaleY},
		},
	}
}

// TestMaterialUVTransformSharedMappingNode is the synthetic version of what
// issue #69 asks to be measured against a real Blender export: base colour
// and normal sharing ONE Mapping node, so both carry the identical
// transform. found is true, chosenName is baseColorTexture (first in
// order), and differing is empty -- agreeing maps must never be reported as
// disagreeing.
func TestMaterialUVTransformSharedMappingNode(t *testing.T) {
	mat := &gltf.Material{
		Name: "tiles",
		PBRMetallicRoughness: &gltf.PBRMetallicRoughness{
			BaseColorTexture: &gltf.TextureInfo{Index: 0, Extensions: uvTT(0, 0, 4, 4)},
		},
		NormalTexture: &gltf.NormalTexture{Index: gltf.Index(1), Extensions: uvTT(0, 0, 4, 4)},
	}
	doc := &gltf.Document{Materials: []*gltf.Material{mat}}

	_, found, chosenName, skipped, differing := materialUVTransform(doc, 0)
	if !found {
		t.Fatal("found = false, want true")
	}
	if chosenName != "baseColorTexture" {
		t.Errorf("chosenName = %q, want baseColorTexture", chosenName)
	}
	if len(skipped) != 0 {
		t.Errorf("skipped = %v, want empty", skipped)
	}
	if len(differing) != 0 {
		t.Errorf("differing = %v, want empty (both maps share the same transform)", differing)
	}
}

// TestMaterialUVTransformDisagreeingMaps covers the case baking cannot
// satisfy: base colour and normal carry DIFFERENT transforms. The base
// colour's transform still wins (first in fallback order), and normalTexture
// is named in differing so the caller can log it.
//
// It has teeth: swapping materialUVCandidates' base-colour and normal
// entries (normal first) changes chosenName to "normalTexture" and
// differing to ["baseColorTexture"], failing both assertions below.
// Introduced and reverted to confirm.
func TestMaterialUVTransformDisagreeingMaps(t *testing.T) {
	mat := &gltf.Material{
		Name: "mismatched",
		PBRMetallicRoughness: &gltf.PBRMetallicRoughness{
			BaseColorTexture: &gltf.TextureInfo{Index: 0, Extensions: uvTT(0, 0, 8, 8)},
		},
		NormalTexture: &gltf.NormalTexture{Index: gltf.Index(1), Extensions: uvTT(0, 0, 4, 4)},
	}
	doc := &gltf.Document{Materials: []*gltf.Material{mat}}

	m, found, chosenName, _, differing := materialUVTransform(doc, 0)
	if !found {
		t.Fatal("found = false, want true")
	}
	if chosenName != "baseColorTexture" {
		t.Errorf("chosenName = %q, want baseColorTexture", chosenName)
	}
	if len(differing) != 1 || differing[0] != "normalTexture" {
		t.Errorf("differing = %v, want [normalTexture]", differing)
	}
	if m.a != 8 || m.e != 8 {
		t.Errorf("chosen matrix = %+v, want the baseColorTexture 8x scale", m)
	}
}

// TestMaterialUVTransformTexCoordOverride covers the texCoord-override rule:
// a map whose KHR_texture_transform names a texCoord other than 0 is
// reported in skippedTexCoord and never chosen or compared, since this
// engine reads TEXCOORD_0 only. With base colour skipped this way, normal
// (the next candidate) wins instead.
func TestMaterialUVTransformTexCoordOverride(t *testing.T) {
	badTexCoord := 1
	mat := &gltf.Material{
		Name: "atlas",
		PBRMetallicRoughness: &gltf.PBRMetallicRoughness{
			BaseColorTexture: &gltf.TextureInfo{Index: 0, Extensions: gltf.Extensions{
				texturetransform.ExtensionName: &texturetransform.TextureTranform{
					Scale: [2]float64{2, 2}, TexCoord: &badTexCoord,
				},
			}},
		},
		NormalTexture: &gltf.NormalTexture{Index: gltf.Index(1), Extensions: uvTT(0, 0, 4, 4)},
	}
	doc := &gltf.Document{Materials: []*gltf.Material{mat}}

	m, found, chosenName, skipped, differing := materialUVTransform(doc, 0)
	if !found {
		t.Fatal("found = false, want true")
	}
	if len(skipped) != 1 || skipped[0] != "baseColorTexture" {
		t.Errorf("skipped = %v, want [baseColorTexture]", skipped)
	}
	if chosenName != "normalTexture" {
		t.Errorf("chosenName = %q, want normalTexture (base colour skipped)", chosenName)
	}
	if len(differing) != 0 {
		t.Errorf("differing = %v, want empty (only one usable candidate)", differing)
	}
	if m.a != 4 {
		t.Errorf("chosen matrix a = %v, want 4 (normalTexture's scale)", m.a)
	}
}

// TestMaterialUVTransformTexCoordZeroIsHonoured checks the other half of the
// texCoord rule: an EXPLICIT texCoord of 0 (as opposed to an absent field,
// which decodes the same way but is worth covering separately since Go's
// *int zero value is nil, not a pointer to 0) is honoured normally, not
// skipped.
//
// It has teeth: changing the skip condition from `*t.TexCoord != 0` to
// `t.TexCoord != nil` (i.e. skipping ANY explicit texCoord, including 0)
// makes this report found=false. Introduced and reverted to confirm.
func TestMaterialUVTransformTexCoordZeroIsHonoured(t *testing.T) {
	zero := 0
	mat := &gltf.Material{
		PBRMetallicRoughness: &gltf.PBRMetallicRoughness{
			BaseColorTexture: &gltf.TextureInfo{Index: 0, Extensions: gltf.Extensions{
				texturetransform.ExtensionName: &texturetransform.TextureTranform{
					Scale: [2]float64{3, 3}, TexCoord: &zero,
				},
			}},
		},
	}
	doc := &gltf.Document{Materials: []*gltf.Material{mat}}

	m, found, _, skipped, _ := materialUVTransform(doc, 0)
	if !found {
		t.Fatal("found = false, want true (texCoord 0 is not an override)")
	}
	if len(skipped) != 0 {
		t.Errorf("skipped = %v, want empty", skipped)
	}
	if m.a != 3 {
		t.Errorf("matrix a = %v, want 3", m.a)
	}
}

// TestMaterialUVTransformDegenerateExtensionIsIdentity covers a real find
// from G1's asset grep, not a hypothetical: examples/06-skinned and
// examples/15-kitchen-sink's shared character.glb (exported by UnityGLTF)
// DOES carry KHR_texture_transform -- contrary to the brief's assumption
// that no shipped asset uses it -- but as exactly
// `"KHR_texture_transform":{"texCoord":0}` with no offset/scale/rotation
// keys at all, which decodes to the zero value (Offset [0,0], Scale
// DefaultScale [1,1] per the library's own UnmarshalJSON, Rotation 0):
// geometrically identity. found is still true (the extension IS present),
// but the matrix composeUVTransform produces from it must equal identityUV
// exactly, or G1's byte-identical promise for these two examples breaks.
//
// It has teeth: constructing the fixture with Offset [0.001, 0] instead of
// the zero value (simulating the extension NOT being a true no-op) makes
// `m != identityUV` and fails the assertion below -- confirming this test
// would catch it if character.glb's real content were not actually
// identity. Introduced and reverted to confirm.
func TestMaterialUVTransformDegenerateExtensionIsIdentity(t *testing.T) {
	zero := 0
	mat := &gltf.Material{
		Name: "character-human",
		PBRMetallicRoughness: &gltf.PBRMetallicRoughness{
			BaseColorTexture: &gltf.TextureInfo{Index: 0, Extensions: gltf.Extensions{
				texturetransform.ExtensionName: &texturetransform.TextureTranform{
					Scale: texturetransform.DefaultScale, TexCoord: &zero,
				},
			}},
		},
	}
	doc := &gltf.Document{Materials: []*gltf.Material{mat}}

	m, found, _, skipped, differing := materialUVTransform(doc, 0)
	if !found {
		t.Error("found = false, want true (the extension IS present in the file)")
	}
	if len(skipped) != 0 || len(differing) != 0 {
		t.Errorf("skipped=%v differing=%v, want both empty", skipped, differing)
	}
	if m != identityUV {
		t.Errorf("m = %+v, want identityUV -- character.glb's real KHR_texture_transform must be a geometric no-op", m)
	}
}

// TestMaterialUVTransformNoExtension covers the overwhelmingly common case
// (G1: no shipped asset uses KHR_texture_transform) -- a material with
// texture references but no extension on any of them returns found=false
// and identityUV, so applyUVTransform's callers do nothing.
func TestMaterialUVTransformNoExtension(t *testing.T) {
	mat := &gltf.Material{
		PBRMetallicRoughness: &gltf.PBRMetallicRoughness{
			BaseColorTexture: &gltf.TextureInfo{Index: 0},
		},
	}
	doc := &gltf.Document{Materials: []*gltf.Material{mat}}
	m, found, chosenName, skipped, differing := materialUVTransform(doc, 0)
	if found {
		t.Error("found = true, want false")
	}
	if m != identityUV {
		t.Errorf("m = %+v, want identityUV", m)
	}
	// The base colour map still DECIDES -- at the identity -- so it is named;
	// see TestMaterialUVTransformAnUntiledMapIsStillAMap for why that matters.
	if chosenName != "baseColorTexture" || skipped != nil || differing != nil {
		t.Errorf("chosenName=%q skipped=%v differing=%v, want baseColorTexture and nothing else", chosenName, skipped, differing)
	}
}

// TestMaterialUVTransformNoMaterial covers materialIdx < 0 and out of range
// -- the same sentinel LoadGLTF's matIdx uses for "no material".
func TestMaterialUVTransformNoMaterial(t *testing.T) {
	doc := &gltf.Document{Materials: []*gltf.Material{{Name: "Unused"}}}
	for _, idx := range []int{-1, 9} {
		m, found, _, _, _ := materialUVTransform(doc, idx)
		if found {
			t.Errorf("materialIdx=%d: found = true, want false", idx)
		}
		if m != identityUV {
			t.Errorf("materialIdx=%d: m = %+v, want identityUV", idx, m)
		}
	}
}

// TestResolveUVTransformCaches checks resolveUVTransform's caching contract:
// calling it twice for the same materialIdx returns the identical matrix
// (trivially true for a pure function, but the point of the cache map is
// that a shared material's log lines print once per LOAD, not once per
// PRIMITIVE -- this at least confirms the cached value survives a second
// call rather than a bug clobbering the map entry). materialIdx -1 (no
// material) is covered too, since resolveUVTransform special-cases it
// before ever calling materialUVTransform.
func TestResolveUVTransformCaches(t *testing.T) {
	mat := &gltf.Material{
		Name: "tiles",
		PBRMetallicRoughness: &gltf.PBRMetallicRoughness{
			BaseColorTexture: &gltf.TextureInfo{Index: 0, Extensions: uvTT(0, 0, 2, 2)},
		},
	}
	doc := &gltf.Document{Materials: []*gltf.Material{mat}}
	cache := make(map[int]uvAffine)

	first := resolveUVTransform("test.gltf", doc, cache, 0)
	second := resolveUVTransform("test.gltf", doc, cache, 0)
	if first != second {
		t.Errorf("first=%+v second=%+v, want equal", first, second)
	}
	if first.a != 2 {
		t.Errorf("a = %v, want 2", first.a)
	}

	none := resolveUVTransform("test.gltf", doc, cache, -1)
	if none != identityUV {
		t.Errorf("materialIdx -1 = %+v, want identityUV", none)
	}
}

// TestResolveUVTransformDiscriminatesMaterials is the real teeth behind "two
// primitives sharing a mesh get their OWN correct transform": two DIFFERENT
// materialIdx values resolved through ONE shared cache (as LoadGLTF's mesh
// loop does for the whole document) must not cross-contaminate.
//
// It has teeth: replacing resolveUVTransform's `cache[materialIdx]` reads
// and writes with a fixed key (`cache[0]`, simulating a copy-paste bug that
// forgot to key by materialIdx) makes material 1's result read back as
// material 0's 2x scale instead of its own 6x -- caught below. Introduced
// and reverted to confirm.
func TestResolveUVTransformDiscriminatesMaterials(t *testing.T) {
	mat0 := &gltf.Material{PBRMetallicRoughness: &gltf.PBRMetallicRoughness{
		BaseColorTexture: &gltf.TextureInfo{Index: 0, Extensions: uvTT(0, 0, 2, 2)},
	}}
	mat1 := &gltf.Material{PBRMetallicRoughness: &gltf.PBRMetallicRoughness{
		BaseColorTexture: &gltf.TextureInfo{Index: 1, Extensions: uvTT(0, 0, 6, 6)},
	}}
	doc := &gltf.Document{Materials: []*gltf.Material{mat0, mat1}}
	cache := make(map[int]uvAffine)

	m0 := resolveUVTransform("test.gltf", doc, cache, 0)
	m1 := resolveUVTransform("test.gltf", doc, cache, 1)
	if m0.a != 2 {
		t.Errorf("material 0 = %+v, want a=2", m0)
	}
	if m1.a != 6 {
		t.Errorf("material 1 = %+v, want a=6", m1)
	}

	// Re-resolving material 0 after material 1 has to still answer 2, not
	// whatever the cache last stored under the wrong key.
	if again := resolveUVTransform("test.gltf", doc, cache, 0); again.a != 2 {
		t.Errorf("material 0 re-resolved = %+v, want a=2", again)
	}
}

// TestMaterialUVTransformIgnoresTexCoordPointerIdentity: TexCoord is a *int,
// which Go compares by ADDRESS, so two maps that both say "texCoord: 0" hold
// two different pointers. The comparison is between composed matrices, which
// never see the pointer -- this is here so that a future comparison of the
// decoded structs themselves is caught, since that is the obvious way to write
// it and it reports every such material as disagreeing with itself.
//
// Verified to fail that way: with the loop comparing the decoded
// TextureTranform values with ==, differing came back [normalTexture].
func TestMaterialUVTransformIgnoresTexCoordPointerIdentity(t *testing.T) {
	zeroA, zeroB := 0, 0
	mat := &gltf.Material{
		Name: "two pointers to zero",
		PBRMetallicRoughness: &gltf.PBRMetallicRoughness{
			BaseColorTexture: &gltf.TextureInfo{Index: 0, Extensions: gltf.Extensions{
				texturetransform.ExtensionName: &texturetransform.TextureTranform{Scale: [2]float64{2, 2}, TexCoord: &zeroA},
			}},
		},
		NormalTexture: &gltf.NormalTexture{Index: gltf.Index(1), Extensions: gltf.Extensions{
			texturetransform.ExtensionName: &texturetransform.TextureTranform{Scale: [2]float64{2, 2}, TexCoord: &zeroB},
		}},
	}
	doc := &gltf.Document{Materials: []*gltf.Material{mat}}
	m, found, _, skipped, differing := materialUVTransform(doc, 0)
	if !found || m.a != 2 || len(skipped) != 0 || len(differing) != 0 {
		t.Errorf("m=%+v found=%v skipped=%v differing=%v; want the 2x transform, agreed on by both maps", m, found, skipped, differing)
	}
}

// TestExtractPrimitiveBakesUVPerCopy is an end-to-end sanity check for issue
// #69's central safety claim: two primitives split from the SAME doc mesh
// (sharing one TEXCOORD_0 accessor -- the exact shape an exporter that
// splits a mesh by material produces) end up with independently correct
// baked UVs when given different transforms.
//
// extractPrimitive needs no Renderer at all: it is a free function, which is
// what issue #75 made of the *Renderer method that never dereferenced its
// receiver. These tests used to call it on a nil *Renderer to say the same
// thing.
//
// What this does NOT have teeth against, checked rather than assumed:
// making extractPrimitive share modeler.ReadTextureCoord's destination
// buffer across calls (simulating a future "reduce allocations" change)
// does NOT fail this test -- Vertex.UV is a fixed-size array, so
// `vertices[i].UV = uvs[i]` copies it out of the shared buffer before the
// next primitive's call can overwrite it. The real protection against a
// shared/stale transform leaking between primitives is resolveUVTransform's
// cache being keyed correctly by materialIdx, which
// TestResolveUVTransformDiscriminatesMaterials below covers, WITH teeth.
func TestExtractPrimitiveBakesUVPerCopy(t *testing.T) {
	doc := &gltf.Document{}
	posIdx := modeler.WritePosition(doc, [][3]float32{{0, 0, 0}, {1, 0, 0}, {0, 1, 0}})
	uvIdx := modeler.WriteTextureCoord(doc, [][2]float32{{0, 0}, {1, 0}, {0, 1}})
	idxIdx := modeler.WriteIndices(doc, []uint16{0, 1, 2})

	primA := &gltf.Primitive{
		Attributes: gltf.PrimitiveAttributes{gltf.POSITION: posIdx, gltf.TEXCOORD_0: uvIdx},
		Indices:    gltf.Index(idxIdx),
		Mode:       gltf.PrimitiveTriangles,
	}
	primB := &gltf.Primitive{
		Attributes: gltf.PrimitiveAttributes{gltf.POSITION: posIdx, gltf.TEXCOORD_0: uvIdx},
		Indices:    gltf.Index(idxIdx),
		Mode:       gltf.PrimitiveTriangles,
	}

	uvA := composeUVTransform(texturetransform.TextureTranform{Scale: [2]float64{1, 1}})
	uvB := composeUVTransform(texturetransform.TextureTranform{Scale: [2]float64{2, 2}})

	vertsA, _, err := extractPrimitive(doc, primA, uvA)
	if err != nil {
		t.Fatalf("extractPrimitive A: %v", err)
	}
	vertsB, _, err := extractPrimitive(doc, primB, uvB)
	if err != nil {
		t.Fatalf("extractPrimitive B: %v", err)
	}

	if vertsA[1].UV != [2]float32{1, 0} {
		t.Errorf("prim A vertex 1 UV = %v, want [1 0] (scale 1x, untouched)", vertsA[1].UV)
	}
	if vertsB[1].UV != [2]float32{2, 0} {
		t.Errorf("prim B vertex 1 UV = %v, want [2 0] (scale 2x) -- if this reads [1 0], prim A's bake leaked into prim B's shared accessor", vertsB[1].UV)
	}
}

// TestExtractSkinnedPrimitiveBakesUV is extractSkinnedPrimitive's own
// coverage -- issue #69 asks skinned primitives to get "the same treatment
// if they go through a shared UV read; if not, do it in both places and
// test both" -- extractSkinnedPrimitive IS a separate function from
// extractPrimitive (SkinnedVertex is a different layout), so this is the
// "test both" half.
func TestExtractSkinnedPrimitiveBakesUV(t *testing.T) {
	doc := &gltf.Document{}
	posIdx := modeler.WritePosition(doc, [][3]float32{{0, 0, 0}, {1, 0, 0}, {0, 1, 0}})
	uvIdx := modeler.WriteTextureCoord(doc, [][2]float32{{0, 0}, {1, 0}, {0, 1}})
	idxIdx := modeler.WriteIndices(doc, []uint16{0, 1, 2})
	jointsIdx := modeler.WriteJoints(doc, [][4]uint16{{0, 0, 0, 0}, {0, 0, 0, 0}, {0, 0, 0, 0}})
	weightsIdx := modeler.WriteWeights(doc, [][4]float32{{1, 0, 0, 0}, {1, 0, 0, 0}, {1, 0, 0, 0}})

	prim := &gltf.Primitive{
		Attributes: gltf.PrimitiveAttributes{
			gltf.POSITION:   posIdx,
			gltf.TEXCOORD_0: uvIdx,
			gltf.JOINTS_0:   jointsIdx,
			gltf.WEIGHTS_0:  weightsIdx,
		},
		Indices: gltf.Index(idxIdx),
		Mode:    gltf.PrimitiveTriangles,
	}

	uv := composeUVTransform(texturetransform.TextureTranform{Scale: [2]float64{5, 5}, Offset: [2]float64{1, 0}})
	verts, _, err := extractSkinnedPrimitive(doc, prim, uv)
	if err != nil {
		t.Fatalf("extractSkinnedPrimitive: %v", err)
	}
	if verts[1].UV != [2]float32{6, 0} {
		t.Errorf("vertex 1 UV = %v, want [6 0] (5*1+1, 5*0+0)", verts[1].UV)
	}
}

// TestMaterialUVTransformAnUntiledMapIsStillAMap: a map with no
// KHR_texture_transform has a transform, and it is the identity. Leaving such
// maps out of the comparison was wrong in both directions, each of them silent:
//
//   - An untiled base colour over a tiled normal map: the normal's 4x was the
//     only transform found, so it won, and the base colour -- the map everyone
//     looks at -- was tiled 4x by a transform that was never its own.
//   - A tiled base colour over an untiled normal map: the normal was tiled 8x
//     with it and nothing said so.
//
// Base colour, when there is one, decides; whatever disagrees with it is named.
//
// Verified to fail against the first version of materialUVTransform: the first
// case reported "chosen normalTexture, matrix 4x" and the second an empty
// differing list.
func TestMaterialUVTransformAnUntiledMapIsStillAMap(t *testing.T) {
	untiledBase := &gltf.Material{
		Name: "untiled base, tiled normal",
		PBRMetallicRoughness: &gltf.PBRMetallicRoughness{
			BaseColorTexture: &gltf.TextureInfo{Index: 0},
		},
		NormalTexture: &gltf.NormalTexture{Index: gltf.Index(1), Extensions: uvTT(0, 0, 4, 4)},
	}
	tiledBase := &gltf.Material{
		Name: "tiled base, untiled normal",
		PBRMetallicRoughness: &gltf.PBRMetallicRoughness{
			BaseColorTexture: &gltf.TextureInfo{Index: 0, Extensions: uvTT(0, -7, 8, 8)},
		},
		NormalTexture: &gltf.NormalTexture{Index: gltf.Index(1)},
	}
	doc := &gltf.Document{Materials: []*gltf.Material{untiledBase, tiledBase}}

	m, _, chosen, _, differing := materialUVTransform(doc, 0)
	if m != identityUV || chosen != "baseColorTexture" {
		t.Errorf("untiled base: chosen %s, matrix %+v; want baseColorTexture and the identity", chosen, m)
	}
	if len(differing) != 1 || differing[0] != "normalTexture" {
		t.Errorf("untiled base: differing = %v, want [normalTexture]", differing)
	}

	m, _, chosen, _, differing = materialUVTransform(doc, 1)
	if m.a != 8 || m.e != 8 || m.f != -7 || chosen != "baseColorTexture" {
		t.Errorf("tiled base: chosen %s, matrix %+v; want baseColorTexture's 8x and -7", chosen, m)
	}
	if len(differing) != 1 || differing[0] != "normalTexture" {
		t.Errorf("tiled base: differing = %v, want [normalTexture]", differing)
	}
}

// TestMaterialUVTransformComparesMatricesNotSpellings: glTF lets `scale` be
// omitted, meaning (1,1), and an exporter is free to write it on one map and
// leave it off another. Those are one transform spelled two ways, and
// reporting them as a disagreement sends someone looking for a tiling bug that
// is not there.
//
// Verified to fail when the raw fields are compared: differing came back
// [normalTexture].
func TestMaterialUVTransformComparesMatricesNotSpellings(t *testing.T) {
	mat := &gltf.Material{
		Name: "same transform, two spellings",
		PBRMetallicRoughness: &gltf.PBRMetallicRoughness{
			BaseColorTexture: &gltf.TextureInfo{Index: 0, Extensions: gltf.Extensions{
				texturetransform.ExtensionName: &texturetransform.TextureTranform{Offset: [2]float64{0.25, 0}},
			}},
		},
		NormalTexture: &gltf.NormalTexture{Index: gltf.Index(1), Extensions: gltf.Extensions{
			texturetransform.ExtensionName: &texturetransform.TextureTranform{Offset: [2]float64{0.25, 0}, Scale: [2]float64{1, 1}},
		}},
	}
	doc := &gltf.Document{Materials: []*gltf.Material{mat}}
	if _, _, _, _, differing := materialUVTransform(doc, 0); len(differing) != 0 {
		t.Errorf("differing = %v, want none: both maps are an offset of 0.25 at scale 1", differing)
	}
}
