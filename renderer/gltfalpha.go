package renderer

import "github.com/qmuntal/gltf"

// AlphaMode is glTF's material.alphaMode: how a primitive's alpha is meant to
// be used by whatever draws it. The engine surfaces this as data and takes no
// action on it (issue #68) -- whether a BLEND primitive becomes a
// glyphengine.Translucent entity, and whether MASK gets a cutout shader, is
// entirely the game's call. See docs/agents/translucency.md for the matching
// engine feature and docs/agents/models.md for what MASK cannot do yet.
type AlphaMode int

const (
	// AlphaModeOpaque ignores alpha entirely. This is glTF's own default
	// when a material omits alphaMode, and it is AlphaMode's zero value on
	// purpose: every ModelMesh built before this field existed drew opaque,
	// and every primitive with no material at all still does.
	AlphaModeOpaque AlphaMode = iota
	// AlphaModeMask alpha-tests against AlphaCutoff: below the cutoff
	// nothing is drawn, at or above it the surface is fully opaque.
	// Reported for completeness only -- there is no alpha-tested (cutout)
	// path in the engine's lit pipelines today (docs/agents/models.md), so
	// nothing currently honours this; a MASK primitive draws exactly like
	// AlphaModeOpaque unless a game builds its own cutout handling.
	AlphaModeMask
	// AlphaModeBlend blends by alpha, back to front. glyphengine's own
	// Translucent component (docs/agents/translucency.md) is the matching
	// engine feature -- the engine does not assign it automatically; a game
	// decides which BLEND meshes become Translucent entities.
	AlphaModeBlend
)

// String names the mode the way glTF's own "alphaMode" field spells it, the
// same convention ModelLightKind.String uses for KHR_lights_punctual's
// "type" (see gltflights.go).
func (m AlphaMode) String() string {
	switch m {
	case AlphaModeMask:
		return "MASK"
	case AlphaModeBlend:
		return "BLEND"
	default:
		return "OPAQUE"
	}
}

// resolveAlpha reads a glTF material's alpha behaviour: AlphaMode, the
// cutoff AlphaModeMask tests against (glTF's own default of 0.5 when the
// document omits it -- meaningful only for MASK), and the base colour
// factor's alpha component. ModelMesh.BaseColor cannot carry that last one:
// it stays [3]float32 on purpose, since widening it would break every
// existing caller that already treats it as three floats (issue #68).
//
// A pure function of the document, the same shape as resolveMaterial just
// above it in gltf.go, so it is testable without a GPU. materialIdx < 0 or
// out of range -- a primitive with no material at all -- answers with the
// same defaults glTF itself defines for an absent material: opaque, cutoff
// 0.5, alpha 1.
func resolveAlpha(doc *gltf.Document, materialIdx int) (mode AlphaMode, cutoff float32, baseAlpha float32) {
	cutoff = 0.5
	baseAlpha = 1
	if materialIdx < 0 || materialIdx >= len(doc.Materials) {
		return mode, cutoff, baseAlpha
	}
	mat := doc.Materials[materialIdx]
	if mat == nil {
		return mode, cutoff, baseAlpha
	}
	mode = alphaModeFromGLTF(mat.AlphaMode)
	cutoff = float32(mat.AlphaCutoffOrDefault())
	if pbr := mat.PBRMetallicRoughness; pbr != nil && pbr.BaseColorFactor != nil {
		baseAlpha = float32(pbr.BaseColorFactor[3])
	}
	return mode, cutoff, baseAlpha
}

// alphaModeFromGLTF maps qmuntal/gltf's own AlphaMode to the engine's. Split
// out from resolveAlpha so a test can hit every case -- including an
// unrecognized value, which glTF forbids but a hand-authored or buggy
// document can still contain -- without building a whole document around it.
func alphaModeFromGLTF(m gltf.AlphaMode) AlphaMode {
	switch m {
	case gltf.AlphaMask:
		return AlphaModeMask
	case gltf.AlphaBlend:
		return AlphaModeBlend
	default:
		return AlphaModeOpaque
	}
}
