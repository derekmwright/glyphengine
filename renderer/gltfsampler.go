package renderer

import (
	"github.com/qmuntal/gltf"
	"github.com/vkngwrapper/core/v3/core1_0"
)

// imageWrap is the addressing mode to upload one glTF image with. U and V
// are tracked separately because glTF's sampler carries wrapS and wrapT
// independently, which is also why textureOptions above gained the same
// split (issue #69) rather than reusing the single "address" field every
// non-glTF caller had been content with.
type imageWrap struct {
	u, v core1_0.SamplerAddressMode
}

// repeatWrap is glTF's own default: a texture with no sampler at all, or a
// sampler that leaves wrapS/wrapT unset, means REPEAT on both axes (glTF
// spec section 5.30 -- gltf.WrapRepeat is also gltf.WrappingMode's zero
// value, which is what makes gltfWrapMode's default case correct for
// "unset" and not just for an explicit REPEAT). Also what every one of the
// engine's own texture constructors already defaults to.
var repeatWrap = imageWrap{u: core1_0.SamplerAddressModeRepeat, v: core1_0.SamplerAddressModeRepeat}

// gltfWrapMode converts one axis of a glTF sampler's wrapS/wrapT to the
// engine's address mode. WrapClampToBorder does not exist in glTF (only
// REPEAT, CLAMP_TO_EDGE and MIRRORED_REPEAT are legal wrapS/wrapT values),
// so every case this switch does not name falls to REPEAT -- the correct
// answer for WrapRepeat itself and the safe one for anything a future glTF
// revision might add.
func gltfWrapMode(w gltf.WrappingMode) core1_0.SamplerAddressMode {
	switch w {
	case gltf.WrapClampToEdge:
		return core1_0.SamplerAddressModeClampToEdge
	case gltf.WrapMirroredRepeat:
		return core1_0.SamplerAddressModeMirroredRepeat
	default:
		return core1_0.SamplerAddressModeRepeat
	}
}

// imageWrapModes resolves, for every image index a glTF Texture actually
// references, the wrap mode to upload that image with: the WrapS/WrapT of
// the FIRST glTF Texture (doc.Textures order) naming it via Source, glTF's
// repeat default when that texture carries no Sampler (or names one out of
// range, which is invalid glTF but tolerated the same way extractNodes
// tolerates other malformed documents).
//
// glTF binds a sampler per TEXTURE -- an (image, sampler) pair -- while
// loadGLTFImages uploads one engine Texture per IMAGE (a design that
// predates this issue and stays; widening it to one Texture per glTF
// Texture would touch every caller of ModelMesh.Texture and Material, far
// past what issue #69 asks for). So an image referenced by two glTF
// Textures with different samplers can only honour one here; conflicts
// names which image indices that happened for, so loadGLTFImages can log
// once per image rather than the choice silently depending on
// doc.Textures' iteration order.
func imageWrapModes(doc *gltf.Document) (modes map[int]imageWrap, conflicts map[int]bool) {
	modes = make(map[int]imageWrap)
	conflicts = make(map[int]bool)
	seen := make(map[int]bool)
	for _, tex := range doc.Textures {
		if tex == nil || tex.Source == nil {
			continue
		}
		imgIdx := int(*tex.Source)
		w := repeatWrap
		if tex.Sampler != nil {
			if si := *tex.Sampler; si >= 0 && si < len(doc.Samplers) && doc.Samplers[si] != nil {
				s := doc.Samplers[si]
				w = imageWrap{u: gltfWrapMode(s.WrapS), v: gltfWrapMode(s.WrapT)}
			}
		}
		if !seen[imgIdx] {
			seen[imgIdx] = true
			modes[imgIdx] = w
		} else if modes[imgIdx] != w {
			conflicts[imgIdx] = true
		}
	}
	return modes, conflicts
}
