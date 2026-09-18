package renderer

import (
	"encoding/binary"
	"math"
	"testing"

	"github.com/go-gl/mathgl/mgl32"
)

func f32At(buf []byte, off int) float32 {
	return math.Float32frombits(binary.LittleEndian.Uint32(buf[off:]))
}

// TestPackLitUBOLayout pins the per-frame lit UBO's byte layout against the
// ShadowData declaration the seven lit fragment shaders share: two mat4
// cascade matrices, then vec4 nightGrade as rgb tint and w strength.
//
// The matrices are checked for column-major order rather than just for being
// present, because that is the half of this that a std140 mistake would break
// silently -- a transposed light VP still produces shadows, just in the wrong
// place, which reads as a bias problem.
//
// Verified to catch a real mistake: writing Strength before Tint (the order
// the Go struct declares them in) makes the tint and strength assertions fail
// immediately rather than passing on a coincidence of similar magnitudes.
func TestPackLitUBOLayout(t *testing.T) {
	buf := make([]byte, litUBOSize)
	var vps [ShadowCascades]mgl32.Mat4
	for c := range vps {
		for i := 0; i < 16; i++ {
			vps[c][i] = float32(c*100 + i)
		}
	}
	grade := NightGrade{Strength: 0.35, Tint: [3]float32{1.5, 0.25, 0.75}}
	packLitUBO(buf, vps, &grade)

	for c := 0; c < ShadowCascades; c++ {
		for i := 0; i < 16; i++ {
			off := c*64 + i*4
			if got, want := f32At(buf, off), float32(c*100+i); got != want {
				t.Fatalf("cascade %d element %d at byte %d = %g, want %g", c, i, off, got, want)
			}
		}
	}

	if got := f32At(buf, nightGradeOffset+0); got != 1.5 {
		t.Errorf("nightGrade.r = %g, want 1.5", got)
	}
	if got := f32At(buf, nightGradeOffset+4); got != 0.25 {
		t.Errorf("nightGrade.g = %g, want 0.25", got)
	}
	if got := f32At(buf, nightGradeOffset+8); got != 0.75 {
		t.Errorf("nightGrade.b = %g, want 0.75", got)
	}
	if got := f32At(buf, nightGradeOffset+12); got != 0.35 {
		t.Errorf("nightGrade.w (strength) = %g, want 0.35", got)
	}
}

// TestPackLitUBODefaultsWhenUnset is the upgrade-safety check, and it is the
// reason SceneLighting.NightGrade is a pointer rather than a value.
//
// A caller driving this package directly fills SceneLighting by hand. One
// written before the field existed leaves it nil, and nil has to come out as
// the grade the engine has always used -- not as the zero NightGrade, whose
// Strength 0 means no night shift at all and would change that game's nights
// on a dependency bump with nobody choosing it.
//
// Verified to catch a real mistake: dropping the nil guard in packLitUBO, so
// a nil grade writes zeros, fails this on every channel. Passing an explicit
// zero-valued grade still writes zeros, which is the "off" case and is
// asserted below so the two cannot be confused.
func TestPackLitUBODefaultsWhenUnset(t *testing.T) {
	var vps [ShadowCascades]mgl32.Mat4
	def := DefaultNightGrade()

	buf := make([]byte, litUBOSize)
	packLitUBO(buf, vps, nil)
	for i, want := range [4]float32{def.Tint[0], def.Tint[1], def.Tint[2], def.Strength} {
		if got := f32At(buf, nightGradeOffset+i*4); got != want {
			t.Errorf("nil grade, component %d = %g, want the default %g", i, got, want)
		}
	}

	// And "off" must still be expressible, or the guard above would have
	// bought upgrade safety by taking away the feature.
	off := make([]byte, litUBOSize)
	packLitUBO(off, vps, &NightGrade{})
	if got := f32At(off, nightGradeOffset+12); got != 0 {
		t.Errorf("explicit zero grade strength = %g, want 0", got)
	}
}

// TestPackLitUBOOverwritesEveryFrame guards the buffer being persistently
// mapped and reused by a frame in flight: a partial write leaves whatever the
// frame two ago put there. Packing over a dirty buffer must leave nothing of
// it behind.
func TestPackLitUBOOverwritesEveryFrame(t *testing.T) {
	buf := make([]byte, litUBOSize)
	for i := range buf {
		buf[i] = 0xAB
	}
	var vps [ShadowCascades]mgl32.Mat4
	packLitUBO(buf, vps, &NightGrade{Strength: 1, Tint: [3]float32{1, 1, 1}})
	for i, b := range buf {
		if b == 0xAB {
			// 0xAB bytes can legitimately appear inside a float, so only a
			// whole untouched word is evidence.
			if i%4 == 0 && i+4 <= len(buf) && buf[i+1] == 0xAB && buf[i+2] == 0xAB && buf[i+3] == 0xAB {
				t.Fatalf("word at byte %d was left unwritten", i)
			}
		}
	}
}
