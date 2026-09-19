package renderer

import (
	"bytes"
	"testing"

	"github.com/qmuntal/gltf"
)

// TestResolveAlphaNoMaterial covers a primitive with no material at all
// (materialIdx -1, the same sentinel LoadGLTF's matIdx uses) and an
// out-of-range index -- both answer glTF's own defaults for an absent
// material: opaque, cutoff 0.5 (meaningless for opaque, but still the
// spec's number), alpha 1.
//
// It has teeth: changing resolveAlpha's `cutoff = 0.5` initializer to
// `cutoff = 0` reports "materialIdx=-1: cutoff = 0, want 0.5" (and the same
// for materialIdx=5) -- this is the path that initializer actually guards;
// TestResolveAlphaMaskCutoff's "omitted" subtest does NOT catch this same
// break, since a present material's cutoff comes from
// mat.AlphaCutoffOrDefault() instead. Introduced and reverted to confirm.
func TestResolveAlphaNoMaterial(t *testing.T) {
	doc := &gltf.Document{Materials: []*gltf.Material{{Name: "Unused"}}}

	for _, idx := range []int{-1, 5} {
		mode, cutoff, alpha := resolveAlpha(doc, idx)
		if mode != AlphaModeOpaque {
			t.Errorf("materialIdx=%d: mode = %v, want AlphaModeOpaque", idx, mode)
		}
		if cutoff != 0.5 {
			t.Errorf("materialIdx=%d: cutoff = %v, want 0.5", idx, cutoff)
		}
		if alpha != 1 {
			t.Errorf("materialIdx=%d: alpha = %v, want 1", idx, alpha)
		}
	}
}

// TestResolveAlphaMeasuredBlenderExport builds the exact material shape
// issue #68 measured from a real Blender 5.0.1 export of a Principled BSDF
// with alpha 0.3:
//
//	{"alphaMode": "BLEND", "doubleSided": true,
//	 "pbrMetallicRoughness": {"baseColorFactor": [0.8, 0.8, 0.8, 0.3]}}
//
// -- through the REAL encoder and decoder (gltf.NewEncoder/NewDecoder,
// mirroring roundTrip in gltflights_test.go), not by constructing the
// in-memory struct and reading it back directly, so this also confirms
// qmuntal/gltf's own AlphaMode/BaseColorFactor (un)marshalling behaves the
// way resolveAlpha assumes.
func TestResolveAlphaMeasuredBlenderExport(t *testing.T) {
	doc := &gltf.Document{
		Asset: gltf.Asset{Version: "2.0"},
		Materials: []*gltf.Material{{
			Name:        "glass",
			AlphaMode:   gltf.AlphaBlend,
			DoubleSided: true,
			PBRMetallicRoughness: &gltf.PBRMetallicRoughness{
				BaseColorFactor: &[4]float64{0.8, 0.8, 0.8, 0.3},
			},
		}},
	}

	var buf bytes.Buffer
	if err := gltf.NewEncoder(&buf).Encode(doc); err != nil {
		t.Fatalf("encode: %v", err)
	}
	got := new(gltf.Document)
	if err := gltf.NewDecoder(bytes.NewReader(buf.Bytes())).Decode(got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	mode, cutoff, alpha := resolveAlpha(got, 0)
	if mode != AlphaModeBlend {
		t.Errorf("mode = %v, want AlphaModeBlend", mode)
	}
	if cutoff != 0.5 {
		t.Errorf("cutoff = %v, want 0.5 (glTF default, MASK not in use)", cutoff)
	}
	// float32(0.3) from float64(0.30000001192092896) -- the JSON round trip
	// of Blender's own float32-precision export -- must land back on
	// exactly float32(0.3), not merely "close": both are the same bit
	// pattern once truncated to float32, which is what BaseAlpha promises
	// callers comparing against a literal like 0.3.
	if alpha != float32(0.3) {
		t.Errorf("alpha = %v, want %v", alpha, float32(0.3))
	}
	if !got.Materials[0].DoubleSided {
		t.Error("DoubleSided lost in round trip -- sanity check on the fixture itself")
	}
}

// It has teeth against a wrong-component read: changing resolveAlpha's
// `pbr.BaseColorFactor[3]` to `[0]` makes
// TestResolveAlphaMeasuredBlenderExport report "alpha = 0.8, want 0.3" (this
// material's R and A channels happen to differ, 0.8 vs 0.3, which is what
// catches it) while TestResolveAlphaNoBaseColorFactor keeps passing --
// that one has no factor at all, so the wrong index never gets exercised.
// Introduced and reverted to confirm.

// TestResolveAlphaMaskCutoff checks AlphaModeMask together with an explicit
// AlphaCutoff, and that omitting AlphaCutoff on a present MASK material
// still defaults to 0.5 rather than 0 -- both routed through
// mat.AlphaCutoffOrDefault(), qmuntal/gltf's own default, not through
// resolveAlpha's `cutoff = 0.5` initializer (that one only matters for the
// no-material path; changing it to 0 does NOT fail this test, only
// TestResolveAlphaNoMaterial -- checked directly rather than assumed, since
// a wrong guess here would have shipped a redundant-looking test with the
// wrong test actually covering the claim).
//
// It has teeth against the case it does cover: temporarily swapping
// gltf.AlphaMask/gltf.AlphaBlend in alphaModeFromGLTF's switch (see that
// function's own test) reports both subtests' mode as BLEND instead of
// MASK. Introduced and reverted to confirm.
func TestResolveAlphaMaskCutoff(t *testing.T) {
	t.Run("explicit cutoff", func(t *testing.T) {
		doc := &gltf.Document{Materials: []*gltf.Material{{
			AlphaMode:   gltf.AlphaMask,
			AlphaCutoff: gltf.Float(0.75),
		}}}
		mode, cutoff, _ := resolveAlpha(doc, 0)
		if mode != AlphaModeMask {
			t.Errorf("mode = %v, want AlphaModeMask", mode)
		}
		if cutoff != 0.75 {
			t.Errorf("cutoff = %v, want 0.75", cutoff)
		}
	})

	t.Run("omitted cutoff defaults to 0.5", func(t *testing.T) {
		doc := &gltf.Document{Materials: []*gltf.Material{{AlphaMode: gltf.AlphaMask}}}
		mode, cutoff, _ := resolveAlpha(doc, 0)
		if mode != AlphaModeMask {
			t.Errorf("mode = %v, want AlphaModeMask", mode)
		}
		if cutoff != 0.5 {
			t.Errorf("cutoff = %v, want 0.5", cutoff)
		}
	})
}

// TestResolveAlphaNoBaseColorFactor covers a material with a
// PBRMetallicRoughness block but no BaseColorFactor at all (common: a
// material driven entirely by a base colour TEXTURE) -- glTF's default
// factor is opaque white, so BaseAlpha must be 1, not 0.
func TestResolveAlphaNoBaseColorFactor(t *testing.T) {
	doc := &gltf.Document{Materials: []*gltf.Material{{
		PBRMetallicRoughness: &gltf.PBRMetallicRoughness{},
	}}}
	_, _, alpha := resolveAlpha(doc, 0)
	if alpha != 1 {
		t.Errorf("alpha = %v, want 1 (glTF default baseColorFactor alpha)", alpha)
	}
}

// TestAlphaModeString checks every named constant reads as glTF's own
// "alphaMode" spelling, the convention ModelLightKind.String already uses
// for KHR_lights_punctual's "type" (gltflights.go) -- and that an
// out-of-range AlphaMode (not producible by resolveAlpha, but AlphaMode is
// an exported int a game could construct directly) reads as OPAQUE rather
// than panicking or printing a bare number.
func TestAlphaModeString(t *testing.T) {
	cases := []struct {
		mode AlphaMode
		want string
	}{
		{AlphaModeOpaque, "OPAQUE"},
		{AlphaModeMask, "MASK"},
		{AlphaModeBlend, "BLEND"},
		{AlphaMode(99), "OPAQUE"},
	}
	for _, c := range cases {
		if got := c.mode.String(); got != c.want {
			t.Errorf("AlphaMode(%d).String() = %q, want %q", c.mode, got, c.want)
		}
	}
}

// TestAlphaModeFromGLTF is alphaModeFromGLTF's own coverage, independent of
// resolveAlpha, including a raw AlphaMode value glTF's own enum forbids (3)
// -- the same tolerance the rest of this package gives malformed input
// rather than trusting every document is spec-valid.
//
// It has teeth: swapping the AlphaMask/AlphaBlend cases fails both
// TestResolveAlphaMeasuredBlenderExport (BLEND reads back as
// AlphaModeMask) and TestResolveAlphaMaskCutoff (MASK reads back as
// AlphaModeBlend). Introduced and reverted to confirm.
func TestAlphaModeFromGLTF(t *testing.T) {
	cases := []struct {
		in   gltf.AlphaMode
		want AlphaMode
	}{
		{gltf.AlphaOpaque, AlphaModeOpaque},
		{gltf.AlphaMask, AlphaModeMask},
		{gltf.AlphaBlend, AlphaModeBlend},
		{gltf.AlphaMode(3), AlphaModeOpaque},
	}
	for _, c := range cases {
		if got := alphaModeFromGLTF(c.in); got != c.want {
			t.Errorf("alphaModeFromGLTF(%d) = %v, want %v", c.in, got, c.want)
		}
	}
}
