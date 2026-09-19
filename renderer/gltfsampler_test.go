package renderer

import (
	"testing"

	"github.com/qmuntal/gltf"
	"github.com/vkngwrapper/core/v3/core1_0"
)

// TestGltfWrapMode checks every legal glTF wrapping mode plus the zero value
// (WrapRepeat, which is also what an unset wrapS/wrapT decodes to -- glTF's
// own default).
func TestGltfWrapMode(t *testing.T) {
	cases := []struct {
		in   gltf.WrappingMode
		want core1_0.SamplerAddressMode
	}{
		{gltf.WrapRepeat, core1_0.SamplerAddressModeRepeat},
		{gltf.WrapClampToEdge, core1_0.SamplerAddressModeClampToEdge},
		{gltf.WrapMirroredRepeat, core1_0.SamplerAddressModeMirroredRepeat},
	}
	for _, c := range cases {
		if got := gltfWrapMode(c.in); got != c.want {
			t.Errorf("gltfWrapMode(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

// TestImageWrapModesNoSampler covers glTF's own default: a texture with no
// Sampler at all means repeat on both axes (spec section 5.30), the same as
// an explicit REPEAT sampler.
func TestImageWrapModesNoSampler(t *testing.T) {
	doc := &gltf.Document{
		Textures: []*gltf.Texture{{Source: gltf.Index(0)}}, // no Sampler
		Images:   []*gltf.Image{{Name: "img0"}},
	}
	modes, conflicts := imageWrapModes(doc)
	if got := modes[0]; got != repeatWrap {
		t.Errorf("modes[0] = %+v, want repeatWrap", got)
	}
	if len(conflicts) != 0 {
		t.Errorf("conflicts = %v, want empty", conflicts)
	}
}

// TestImageWrapModesFromSampler covers a texture that DOES carry a sampler
// with non-default wrap modes -- clamp on U, mirrored repeat on V, the case
// issue #69 asks to be honoured per axis.
func TestImageWrapModesFromSampler(t *testing.T) {
	doc := &gltf.Document{
		Samplers: []*gltf.Sampler{{WrapS: gltf.WrapClampToEdge, WrapT: gltf.WrapMirroredRepeat}},
		Textures: []*gltf.Texture{{Source: gltf.Index(0), Sampler: gltf.Index(0)}},
		Images:   []*gltf.Image{{Name: "img0"}},
	}
	modes, _ := imageWrapModes(doc)
	got := modes[0]
	if got.u != core1_0.SamplerAddressModeClampToEdge {
		t.Errorf("u = %v, want ClampToEdge", got.u)
	}
	if got.v != core1_0.SamplerAddressModeMirroredRepeat {
		t.Errorf("v = %v, want MirroredRepeat", got.v)
	}
}

// TestImageWrapModesConflict covers the case this engine's one-Texture-
// per-IMAGE design cannot fully honour: the SAME image referenced by two
// glTF Textures with different samplers. The first texture's wrap mode
// (doc.Textures order) wins, and the image index is reported in conflicts
// so the caller can log once.
//
// It has teeth: removing the `else if modes[imgIdx] != w` branch (so
// conflicts is never populated) makes conflicts empty, which the second
// assertion below catches; removing the `!seen[imgIdx]` guard entirely
// (always overwriting modes[imgIdx]) makes the FIRST assertion fail instead,
// since modes[0] would end up as the SECOND texture's clamp mode rather than
// the first's repeat. Both introduced and reverted to confirm.
func TestImageWrapModesConflict(t *testing.T) {
	doc := &gltf.Document{
		Samplers: []*gltf.Sampler{
			{}, // sampler 0: defaults (repeat/repeat)
			{WrapS: gltf.WrapClampToEdge, WrapT: gltf.WrapClampToEdge}, // sampler 1: clamp
		},
		Textures: []*gltf.Texture{
			{Source: gltf.Index(0), Sampler: gltf.Index(0)}, // texture 0: repeat, first
			{Source: gltf.Index(0), Sampler: gltf.Index(1)}, // texture 1: clamp, SAME image
		},
		Images: []*gltf.Image{{Name: "shared"}},
	}
	modes, conflicts := imageWrapModes(doc)
	if got := modes[0]; got != repeatWrap {
		t.Errorf("modes[0] = %+v, want repeatWrap (first texture wins)", got)
	}
	if !conflicts[0] {
		t.Error("conflicts[0] = false, want true")
	}
}

// TestImageWrapModesNoConflictWhenSame checks the common case does not get
// flagged: two textures referencing the same image with the SAME sampler
// (or two different samplers that happen to carry identical wrap modes)
// must not be reported as a conflict.
func TestImageWrapModesNoConflictWhenSame(t *testing.T) {
	doc := &gltf.Document{
		Samplers: []*gltf.Sampler{{WrapS: gltf.WrapMirroredRepeat, WrapT: gltf.WrapMirroredRepeat}},
		Textures: []*gltf.Texture{
			{Source: gltf.Index(0), Sampler: gltf.Index(0)},
			{Source: gltf.Index(0), Sampler: gltf.Index(0)},
		},
		Images: []*gltf.Image{{Name: "shared"}},
	}
	_, conflicts := imageWrapModes(doc)
	if conflicts[0] {
		t.Error("conflicts[0] = true, want false (both textures agree)")
	}
}

// TestImageWrapModesOutOfRangeSampler covers a texture naming a Sampler
// index the document does not have -- invalid glTF, but the same tolerance
// extractNodes gives other malformed input: falls back to repeat rather
// than panicking.
func TestImageWrapModesOutOfRangeSampler(t *testing.T) {
	doc := &gltf.Document{
		Textures: []*gltf.Texture{{Source: gltf.Index(0), Sampler: gltf.Index(5)}},
		Images:   []*gltf.Image{{Name: "img0"}},
	}
	modes, _ := imageWrapModes(doc) // must not panic
	if got := modes[0]; got != repeatWrap {
		t.Errorf("modes[0] = %+v, want repeatWrap (out-of-range sampler tolerated)", got)
	}
}
