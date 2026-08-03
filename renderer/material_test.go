package renderer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unsafe"

	"github.com/qmuntal/gltf"
)

// TestMaterialUniformMatchesShaderBlock asserts the Go struct written into the
// per-material uniform buffer is the size the shader's MaterialBlock declares.
//
// Nothing else notices a mismatch. A shorter Go struct leaves the tail of the
// buffer uninitialised, so the shader reads whatever the allocation happened to
// contain and the map flags come out arbitrary; a longer one writes past what the
// descriptor's Range covers. Neither errors, and both look like a material that
// was configured wrong.
//
// It has teeth: adding a field to materialUniform without changing
// materialUniformSize fails it, and so does changing the constant alone.
func TestMaterialUniformMatchesShaderBlock(t *testing.T) {
	if got := int(unsafe.Sizeof(materialUniform{})); got != materialUniformSize {
		t.Errorf("sizeof(materialUniform) = %d, materialUniformSize = %d", got, materialUniformSize)
	}

	// And that the shader still declares the same two vec4s. This catches the
	// other direction — editing lit_material.frag's MaterialBlock without
	// touching Go, which the size check above cannot see.
	src, err := os.ReadFile(filepath.Join("..", "shaders", "lit_material.frag"))
	if err != nil {
		t.Skipf("shader source unavailable: %v", err)
	}
	block := regexp.MustCompile(`(?s)uniform MaterialBlock \{(.*?)\}`).FindSubmatch(src)
	if block == nil {
		t.Fatal("lit_material.frag no longer declares a MaterialBlock; the Go mirror is now unanchored")
	}
	// std140 rounds every member up to 16 bytes, so counting vec4s is enough.
	members := strings.Count(string(block[1]), "vec4")
	if want := materialUniformSize / 16; members != want {
		t.Errorf("MaterialBlock declares %d vec4 members, Go mirror expects %d", members, want)
	}
}

// TestNewMaterialUniformFlagsFollowTheMaps checks the flags that decide whether
// the shader samples each map.
//
// Every slot always has a descriptor bound — an unsupplied one gets a neutral
// 1x1 — so these flags are the only thing distinguishing "no normal map" from
// "a flat normal map". Getting one wrong costs a texture fetch and a tangent
// frame for nothing, or skips a map the caller did supply.
//
// It has teeth: swapping any two of the three Maps components fails it, and
// dropping the FlipGreen negation fails the flip case.
func TestNewMaterialUniformFlagsFollowTheMaps(t *testing.T) {
	tex := &Texture{} // only identity matters here, never sampled

	t.Run("no maps", func(t *testing.T) {
		u := newMaterialUniform(MaterialOptions{Albedo: tex})
		for i, name := range []string{"normal", "metallic-roughness", "occlusion"} {
			if u.Maps[i] != 0 {
				t.Errorf("%s flag = %g with no such map, want 0", name, u.Maps[i])
			}
		}
		// Unset scale and strength default to one, not zero: a zero normal scale
		// would flatten every map and a zero strength would disable occlusion.
		if u.Maps[3] != 1 {
			t.Errorf("occlusion strength = %g, want 1 when unset", u.Maps[3])
		}
		if u.Scale[0] != 1 || u.Scale[1] != 1 {
			t.Errorf("normal scale = %v, want (1,1) when unset", u.Scale[:2])
		}
	})

	t.Run("each map sets only its own flag", func(t *testing.T) {
		cases := []struct {
			name  string
			opts  MaterialOptions
			index int
		}{
			{"normal", MaterialOptions{Normal: tex}, 0},
			{"metallic-roughness", MaterialOptions{MetallicRoughness: tex}, 1},
			{"occlusion", MaterialOptions{Occlusion: tex}, 2},
		}
		for _, c := range cases {
			u := newMaterialUniform(c.opts)
			for i := 0; i < 3; i++ {
				want := float32(0)
				if i == c.index {
					want = 1
				}
				if u.Maps[i] != want {
					t.Errorf("%s map: Maps[%d] = %g, want %g", c.name, i, u.Maps[i], want)
				}
			}
		}
	})

	t.Run("flip green negates only y", func(t *testing.T) {
		u := newMaterialUniform(MaterialOptions{Normal: tex, NormalScale: 2, FlipGreen: true})
		if u.Scale[0] != 2 {
			t.Errorf("x scale = %g, want +2; flipping green must not touch x", u.Scale[0])
		}
		if u.Scale[1] != -2 {
			t.Errorf("y scale = %g, want -2", u.Scale[1])
		}

		plain := newMaterialUniform(MaterialOptions{Normal: tex, NormalScale: 2})
		if plain.Scale[1] != 2 {
			t.Errorf("y scale = %g without FlipGreen, want +2", plain.Scale[1])
		}
	})
}

// TestNewMaterialUniformEmissive checks the term that is allowed to leave the
// 0..1 range.
//
// Emission is the first thing in the engine meant to exceed 1, so the strength
// multiply is the whole point rather than a detail: fold it in wrong and an
// authored glow arrives merely bright, which looks plausible and is impossible
// to spot without the number in front of you. The default of one matters for the
// same reason -- glTF's default strength is 1, and treating an unset field as
// zero would silently delete every emissive material that does not use the
// extension.
//
// It has teeth: dropping the strength multiply fails the scaled case, defaulting
// strength to 0 instead of 1 fails the unset case, and setting the map flag from
// the wrong field fails the flag cases.
func TestNewMaterialUniformEmissive(t *testing.T) {
	tex := &Texture{}

	t.Run("absent by default", func(t *testing.T) {
		u := newMaterialUniform(MaterialOptions{Albedo: tex})
		if u.Emissive != [4]float32{0, 0, 0, 0} {
			t.Errorf("emissive = %v with nothing set, want all zero", u.Emissive)
		}
	})

	t.Run("unset strength means one", func(t *testing.T) {
		u := newMaterialUniform(MaterialOptions{EmissiveFactor: [3]float32{0.5, 0.25, 1}})
		if want := [3]float32{0.5, 0.25, 1}; [3]float32{u.Emissive[0], u.Emissive[1], u.Emissive[2]} != want {
			t.Errorf("emissive rgb = %v, want %v", u.Emissive[:3], want)
		}
	})

	t.Run("strength scales the factor past one", func(t *testing.T) {
		u := newMaterialUniform(MaterialOptions{
			EmissiveFactor:   [3]float32{1, 0.5, 0},
			EmissiveStrength: 8,
		})
		if want := [3]float32{8, 4, 0}; [3]float32{u.Emissive[0], u.Emissive[1], u.Emissive[2]} != want {
			t.Errorf("emissive rgb = %v, want %v", u.Emissive[:3], want)
		}
		if u.Emissive[0] <= 1 {
			t.Errorf("emissive red = %g, want above 1 -- the HDR range is the point", u.Emissive[0])
		}
	})

	t.Run("map flag follows the map, not the factor", func(t *testing.T) {
		withMap := newMaterialUniform(MaterialOptions{Emissive: tex})
		if withMap.Emissive[3] != 1 {
			t.Errorf("map flag = %g with an emissive map, want 1", withMap.Emissive[3])
		}
		factorOnly := newMaterialUniform(MaterialOptions{EmissiveFactor: [3]float32{1, 1, 1}})
		if factorOnly.Emissive[3] != 0 {
			t.Errorf("map flag = %g with a factor but no map, want 0", factorOnly.Emissive[3])
		}
	})

	t.Run("emissive does not disturb the other maps", func(t *testing.T) {
		u := newMaterialUniform(MaterialOptions{Emissive: tex, EmissiveStrength: 4})
		for i, name := range []string{"normal", "metallic-roughness", "occlusion"} {
			if u.Maps[i] != 0 {
				t.Errorf("%s flag = %g, want 0", name, u.Maps[i])
			}
		}
		if u.Scale[0] != 1 || u.Scale[1] != 1 {
			t.Errorf("normal scale = %v, want (1,1)", u.Scale[:2])
		}
	})
}

// TestEmissiveStrength covers KHR_materials_emissive_strength, which arrives as
// raw JSON because the extension is not registered with the decoder.
//
// Every malformed case has to return 1 rather than 0. Returning 0 would make a
// broken extension delete the emission entirely, which reads as "this model has
// no emissive material" rather than as a parse failure -- and glTF's own default
// when the extension is absent is 1, so 1 is both the safe answer and the
// correct one.
//
// It has teeth: returning 0 from any failure path fails the corresponding case,
// and dropping the negative guard fails the last one.
func TestEmissiveStrength(t *testing.T) {
	const key = "KHR_materials_emissive_strength"

	cases := []struct {
		name string
		ext  gltf.Extensions
		want float32
	}{
		{"absent", nil, 1},
		{"other extension only", gltf.Extensions{"KHR_materials_unlit": json.RawMessage(`{}`)}, 1},
		{"present", gltf.Extensions{key: json.RawMessage(`{"emissiveStrength":5.5}`)}, 5.5},
		{"present but empty", gltf.Extensions{key: json.RawMessage(`{}`)}, 1},
		{"malformed json", gltf.Extensions{key: json.RawMessage(`{`)}, 1},
		{"wrong type", gltf.Extensions{key: json.RawMessage(`{"emissiveStrength":"bright"}`)}, 1},
		{"not raw json", gltf.Extensions{key: 5.5}, 1},
		{"zero is honoured", gltf.Extensions{key: json.RawMessage(`{"emissiveStrength":0}`)}, 0},
		{"negative falls back", gltf.Extensions{key: json.RawMessage(`{"emissiveStrength":-2}`)}, 1},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := emissiveStrength(&gltf.Material{Extensions: c.ext}); got != c.want {
				t.Errorf("emissiveStrength = %g, want %g", got, c.want)
			}
		})
	}
}
