package renderer

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unsafe"
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
