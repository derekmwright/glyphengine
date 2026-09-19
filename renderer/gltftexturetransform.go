package renderer

import (
	"log"
	"math"

	"github.com/qmuntal/gltf"
	"github.com/qmuntal/gltf/ext/texturetransform"
)

// uvAffine is the affine part of KHR_texture_transform's 3x3 matrix -- the
// third row is always (0 0 1) for a UV transform, so this carries only the
// six coefficients that matter:
//
//	u' = a*u + b*v + c
//	v' = d*u + e*v + f
//
// Comparable with == because every field is a float64, which is what lets
// applyUVTransform and the material-level "differs" check below compare two
// transforms by value rather than by re-deriving the matrix twice.
type uvAffine struct {
	a, b, c float64
	d, e, f float64
}

// identityUV is "no transform": what every primitive got before this issue,
// and what a primitive whose material carries no KHR_texture_transform (every
// shipped asset today -- see G1 in the issue) still gets.
var identityUV = uvAffine{a: 1, e: 1}

// composeUVTransform builds the affine matrix KHR_texture_transform's spec
// defines as translation * rotation * scale, rotation counter-clockwise in
// UV space (glTF's own U-right, V-down axes). This mirrors the extension
// spec's reference GLSL column for column rather than re-deriving the signs
// from scratch:
//
//	mat3 translation = mat3(1,0,0, 0,1,0, offset.x,offset.y,1);
//	mat3 rotation = mat3(cos(r),sin(r),0, -sin(r),cos(r),0, 0,0,1);
//	mat3 scale = mat3(scale.x,0,0, 0,scale.y,0, 0,0,1);
//	matrix = translation * rotation * scale;
//
// GLSL's mat3(...) fills columns, so expanded against (u,v,1) that is scale
// first, then rotate, then translate:
//
//	su, sv := scale.x*u, scale.y*v
//	ru, rv := cos(r)*su - sin(r)*sv, sin(r)*su + cos(r)*sv
//	u', v' := offset.x + ru, offset.y + rv
//
// Verified against the measured Blender 5.0.1 export in issue #69: an 8x
// Mapping-node scale with no rotation exports as offset (0,-7), scale (8,8),
// which under this formula gives u'=8u, v'=8v-7 -- an 8x tile in both axes
// regardless of the offset's sign, since repeat addressing only cares about
// scale for tile COUNT and uses offset purely as tiling phase. The offset
// being negative is Blender's own V-flip on export (see
// docs/agents/models.md), not something this formula needs to know about.
func composeUVTransform(t texturetransform.TextureTranform) uvAffine {
	scale := t.ScaleOrDefault()
	sin, cos := math.Sincos(t.Rotation)
	return uvAffine{
		a: cos * scale[0], b: -sin * scale[1], c: t.Offset[0],
		d: sin * scale[0], e: cos * scale[1], f: t.Offset[1],
	}
}

// applyUVTransform bakes m into every UV in uvs, in place. identityUV is
// skipped entirely rather than multiplied through: it is wasted work on
// every model that does not use the extension (all of them, before this
// issue, per G1), and "do nothing" needs no argument about float
// multiply-by-one-add-zero being exact when it can just not happen.
func applyUVTransform(uvs [][2]float32, m uvAffine) {
	if m == identityUV {
		return
	}
	for i, uv := range uvs {
		u, v := float64(uv[0]), float64(uv[1])
		uvs[i] = [2]float32{
			float32(m.a*u + m.b*v + m.c),
			float32(m.d*u + m.e*v + m.f),
		}
	}
}

// textureTransform reads one texture reference's own KHR_texture_transform
// out of its Extensions map, and whether it carried one at all.
//
// Trusting that ext[texturetransform.ExtensionName] decodes to
// *texturetransform.TextureTranform (not, say, json.RawMessage) depends on
// this package importing ext/texturetransform for its init() side effect --
// the same premise gltflights.go's documentLights depends on for
// KHR_lights_punctual, proved there by an actual encode/decode round trip
// rather than by reading the package and assuming (see
// TestTextureTransformRoundTrip in the _test.go file for the same proof
// here).
func textureTransform(ext gltf.Extensions) (t texturetransform.TextureTranform, ok bool) {
	if ext == nil {
		return t, false
	}
	raw, present := ext[texturetransform.ExtensionName]
	if !present {
		return t, false
	}
	tt, ok := raw.(*texturetransform.TextureTranform)
	if !ok || tt == nil {
		return t, false
	}
	return *tt, true
}

// sameUVTransform reports whether two decoded KHR_texture_transform values
// describe the same matrix -- Offset, Rotation and Scale only. TexCoord is
// deliberately excluded: it decides whether a candidate is USABLE at all
// (see materialUVTransform's texCoord handling below), not whether two
// usable transforms agree, and *int is comparable by address rather than
// value, which would make two separately-decoded "texCoord: 0"s compare
// unequal for a reason that has nothing to do with the baked matrix.
func sameUVTransform(a, b texturetransform.TextureTranform) bool {
	return a.Offset == b.Offset && a.Rotation == b.Rotation && a.Scale == b.Scale
}

// uvTransformCandidate is one texture slot on a material considered for the
// baked UV transform, named for the log line materialUVTransform's caller
// prints when candidates disagree.
type uvTransformCandidate struct {
	name string
	ext  gltf.Extensions
}

// materialUVCandidates lists a material's texture slots that carry a texture
// reference at all, in the fallback order issue #69 specifies: base colour,
// normal, metallic-roughness, occlusion, emissive. Blender writes the SAME
// transform on every map that shares one Mapping node (measured in
// gltftexturetransform_test.go against a real export with base colour +
// normal through one node), which is why "first present, in this order" is
// the right default rather than an arbitrary one.
func materialUVCandidates(mat *gltf.Material) []uvTransformCandidate {
	var out []uvTransformCandidate
	if pbr := mat.PBRMetallicRoughness; pbr != nil {
		if pbr.BaseColorTexture != nil {
			out = append(out, uvTransformCandidate{"baseColorTexture", pbr.BaseColorTexture.Extensions})
		}
	}
	if mat.NormalTexture != nil {
		out = append(out, uvTransformCandidate{"normalTexture", mat.NormalTexture.Extensions})
	}
	if pbr := mat.PBRMetallicRoughness; pbr != nil && pbr.MetallicRoughnessTexture != nil {
		out = append(out, uvTransformCandidate{"metallicRoughnessTexture", pbr.MetallicRoughnessTexture.Extensions})
	}
	if mat.OcclusionTexture != nil {
		out = append(out, uvTransformCandidate{"occlusionTexture", mat.OcclusionTexture.Extensions})
	}
	if mat.EmissiveTexture != nil {
		out = append(out, uvTransformCandidate{"emissiveTexture", mat.EmissiveTexture.Extensions})
	}
	return out
}

// materialUVTransform picks which map's KHR_texture_transform to bake into a
// primitive's vertices for one material, and reports what a caller should
// log about the choice.
//
// Baking can only satisfy one transform per primitive -- the loader has no
// per-material UV matrix in the shaders, the push-constant block is already
// full at 256 bytes (issue #69) -- so this also surfaces two things the
// caller is expected to log, ONCE per material rather than once per
// primitive or (worse) not at all:
//
//   - skippedTexCoord: candidate names whose extension named a texCoord
//     other than 0. The engine reads TEXCOORD_0 only (no second UV set is
//     explicitly out of scope), so honouring that override would bake a
//     transform meant for UV data this loader never reads; these are
//     skipped as candidates entirely rather than chosen or compared.
//   - differing: candidate names (after the above filter) whose transform
//     does not match the CHOSEN one. Reported so a game can be told its
//     tiling is only correct on one map rather than silently wrong on the
//     rest.
//
// found is false when no surviving candidate carries the extension at all --
// the overwhelmingly common case (see G1: no shipped asset uses this
// extension), meaning "leave the UVs alone" (m is identityUV).
//
// A pure function of the document, no GPU needed, per issue #69's "pure
// functions" list. chosenName is which candidate won (for the caller's
// "differs" log line to name it), "" when found is false.
func materialUVTransform(doc *gltf.Document, materialIdx int) (m uvAffine, found bool, chosenName string, skippedTexCoord, differing []string) {
	if materialIdx < 0 || materialIdx >= len(doc.Materials) {
		return identityUV, false, "", nil, nil
	}
	mat := doc.Materials[materialIdx]
	if mat == nil {
		return identityUV, false, "", nil, nil
	}

	var chosenT texturetransform.TextureTranform
	haveChosen := false

	for _, c := range materialUVCandidates(mat) {
		t, ok := textureTransform(c.ext)
		if !ok {
			continue
		}
		if t.TexCoord != nil && *t.TexCoord != 0 {
			skippedTexCoord = append(skippedTexCoord, c.name)
			continue
		}
		if !haveChosen {
			chosenName, chosenT, haveChosen = c.name, t, true
			continue
		}
		if !sameUVTransform(t, chosenT) {
			differing = append(differing, c.name)
		}
	}

	if !haveChosen {
		return identityUV, false, "", skippedTexCoord, nil
	}
	return composeUVTransform(chosenT), true, chosenName, skippedTexCoord, differing
}

// resolveUVTransform resolves materialIdx's baked UV transform through
// cache, logging what materialUVTransform found the FIRST time a given
// materialIdx is seen -- so a material shared by many primitives (the
// ordinary case: an exporter splits a mesh by material, and several
// primitives commonly share one) is resolved, and logged, once per load
// rather than once per primitive. LoadGLTF and LoadGLTFSkinned each pass
// their own fresh cache.
//
// name is the glTF file name, for the same "gltf %q: ..." log style
// LoadGLTF's other log lines use. materialIdx < 0 (no material) is cached
// under that key like any other and resolves to identityUV without ever
// calling materialUVTransform, since a primitive with no material cannot
// have a material-level extension.
func resolveUVTransform(name string, doc *gltf.Document, cache map[int]uvAffine, materialIdx int) uvAffine {
	if m, ok := cache[materialIdx]; ok {
		return m
	}
	if materialIdx < 0 {
		cache[materialIdx] = identityUV
		return identityUV
	}

	m, found, chosenName, skippedTexCoord, differing := materialUVTransform(doc, materialIdx)
	if !found {
		m = identityUV
	}
	matName := ""
	if materialIdx < len(doc.Materials) && doc.Materials[materialIdx] != nil {
		matName = doc.Materials[materialIdx].Name
	}
	for _, s := range skippedTexCoord {
		log.Printf("gltf %q: material %q: %s's KHR_texture_transform targets a texCoord other than 0, which this loader never reads; ignoring that map's transform", name, matName, s)
	}
	if len(differing) > 0 {
		log.Printf("gltf %q: material %q: %v carry a KHR_texture_transform different from %s's, which is the one baked into this primitive's UVs -- those maps will tile/rotate/offset incorrectly", name, matName, differing, chosenName)
	}

	cache[materialIdx] = m
	return m
}
