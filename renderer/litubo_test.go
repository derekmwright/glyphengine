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
// ShadowData declaration the seven lit fragment shaders share -- and that
// sky.frag and clouds.frag declare a second time, at binding 1 of the cloud
// set, to reach the same buffer: two mat4 cascade matrices, then vec4
// nightGrade as rgb tint and w strength.
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
	packLitUBO(buf, vps, &grade, nil, nil)

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

// TestPackSkyPaletteLayout pins the six endpoints' order and stride against
// the ATM_* indices in shaders/atmosphere.inc.
//
// Order is the whole content of this test, and it is not checkable from the
// engine's own behaviour: swap the day and night pairs and every capture still
// renders a sky, just an inverted one, and swapping zenith with horizon gives
// a plausible-looking gradient the wrong way up. So each endpoint gets a
// distinct value and is asserted where atmSkyPalette indexes it.
//
// The 16-byte stride matters as much as the order. std140 pads an array
// element to a vec4 whether it is declared vec3 or vec4, so packing these
// tightly at 12 bytes would put zenithTwilight's blue where horizonDay's red
// belongs -- a shift that grows down the array and would read as "the sky is
// the wrong colour", not as a layout bug.
//
// Verified to catch a real mistake: packing with a 12-byte stride fails from
// the second endpoint on, and swapping the twilight pair for the night pair in
// SkyPalette.endpoints fails on four of the six.
func TestPackSkyPaletteLayout(t *testing.T) {
	var vps [ShadowCascades]mgl32.Mat4
	pal := SkyPalette{
		ZenithDay:       [3]float32{0.11, 0.12, 0.13},
		HorizonDay:      [3]float32{0.21, 0.22, 0.23},
		ZenithTwilight:  [3]float32{0.31, 0.32, 0.33},
		HorizonTwilight: [3]float32{0.41, 0.42, 0.43},
		ZenithNight:     [3]float32{0.51, 0.52, 0.53},
		HorizonNight:    [3]float32{0.61, 0.62, 0.63},
	}
	buf := make([]byte, litUBOSize)
	packLitUBO(buf, vps, nil, &pal, nil)

	// Index, name, and the value the shader must find there. The names are the
	// ATM_* defines; the indices are what atmSkyPalette subscripts pal with.
	for _, tc := range []struct {
		index int
		name  string
		want  [3]float32
	}{
		{0, "ATM_ZENITH_DAY", pal.ZenithDay},
		{1, "ATM_HORIZON_DAY", pal.HorizonDay},
		{2, "ATM_ZENITH_TWI", pal.ZenithTwilight},
		{3, "ATM_HORIZON_TWI", pal.HorizonTwilight},
		{4, "ATM_ZENITH_NIGHT", pal.ZenithNight},
		{5, "ATM_HORIZON_NIGHT", pal.HorizonNight},
	} {
		off := skyPaletteOffset + tc.index*16
		for c := 0; c < 3; c++ {
			if got := f32At(buf, off+c*4); got != tc.want[c] {
				t.Errorf("skyPalette[%d] (%s) component %d at byte %d = %g, want %g",
					tc.index, tc.name, c, off+c*4, got, tc.want[c])
			}
		}
	}

	// The block must end exactly at the last endpoint's padding word. A
	// mismatch here is a Vulkan descriptor range that disagrees with the
	// shader's block size, which the validation layer catches only sometimes.
	if want := skyPaletteOffset + 6*16; volumetricOffset != want {
		t.Errorf("volumetricOffset = %d, want %d -- the palette must end where the volumetric block begins", volumetricOffset, want)
	}
}

// TestPackLitUBOSkyPaletteDefaultsWhenUnset is the upgrade-safety check for
// the palette, and the reason SceneLighting.SkyPalette is a pointer.
//
// The zero value is not a weaker version of the default here, the way a zero
// NightGrade at least meant "no grade": it is six black colours, so a caller
// driving this package directly with a SceneLighting written before the field
// existed would get a black sky, black fog and black water reflections. Nil
// has to come out as the palette the engine has always drawn.
//
// Verified to catch a real mistake: dropping the nil guard in packLitUBO, so a
// nil palette writes zeros, fails this on all eighteen components.
func TestPackLitUBOSkyPaletteDefaultsWhenUnset(t *testing.T) {
	var vps [ShadowCascades]mgl32.Mat4
	buf := make([]byte, litUBOSize)
	packLitUBO(buf, vps, nil, nil, nil)

	for i, want := range DefaultSkyPalette().endpoints() {
		for c := 0; c < 3; c++ {
			off := skyPaletteOffset + i*16 + c*4
			if got := f32At(buf, off); got != want[c] {
				t.Errorf("nil palette, endpoint %d component %d = %g, want the default %g",
					i, c, got, want[c])
			}
		}
	}
}

// TestDefaultSkyPaletteIsTheShippedSky pins the numbers themselves, because
// they are no longer written down anywhere a compiler checks. atmSkyPalette
// held them as constants until they became data; these are those constants,
// and changing one changes the sky of every game that has not set its own.
//
// Verified to catch a real mistake: perturbing HorizonDay's blue by 0.01 --
// far too small to see in a capture, and exactly the size of an accidental
// edit -- fails here.
func TestDefaultSkyPaletteIsTheShippedSky(t *testing.T) {
	want := [skyPaletteCount][3]float32{
		{0.13, 0.30, 0.78},
		{0.52, 0.70, 0.93},
		{0.055, 0.085, 0.26},
		{0.88, 0.42, 0.22},
		{0.0014, 0.0017, 0.0060},
		{0.0034, 0.0050, 0.0130},
	}
	if got := DefaultSkyPalette().endpoints(); got != want {
		t.Errorf("DefaultSkyPalette() = %v, want the palette atmSkyPalette shipped: %v", got, want)
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
	packLitUBO(buf, vps, nil, nil, nil)
	for i, want := range [4]float32{def.Tint[0], def.Tint[1], def.Tint[2], def.Strength} {
		if got := f32At(buf, nightGradeOffset+i*4); got != want {
			t.Errorf("nil grade, component %d = %g, want the default %g", i, got, want)
		}
	}

	// And "off" must still be expressible, or the guard above would have
	// bought upgrade safety by taking away the feature.
	off := make([]byte, litUBOSize)
	packLitUBO(off, vps, &NightGrade{}, nil, nil)
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
	packLitUBO(buf, vps, &NightGrade{Strength: 1, Tint: [3]float32{1, 1, 1}}, nil, nil)
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

// TestPackVolumetricsLayout pins the volumetric block's offset and contents
// against the `vec4 volumetric` the nine ShadowData declarations end with.
//
// The offset is the half that matters. Everything before it is read by
// shaders that have shipped, so a block written at the wrong offset does not
// fail to draw a beam -- it overwrites a sky palette endpoint with an
// anisotropy, and the symptom is the horizon colour, which nobody would trace
// back to here.
//
// Verified to catch a real mistake: writing the pair at skyPaletteOffset+5*16
// (one endpoint short, the kind of off-by-one this arithmetic invites) fails
// on both components here AND leaves TestPackSkyPaletteLayout's last endpoint
// wrong, which is exactly the pair of failures that says "offset", not
// "value".
func TestPackVolumetricsLayout(t *testing.T) {
	var vps [ShadowCascades]mgl32.Mat4
	buf := make([]byte, litUBOSize)
	packLitUBO(buf, vps, nil, nil, &Volumetrics{Anisotropy: 0.55, Steps: 24})

	if got := f32At(buf, volumetricOffset+0); got != 0.55 {
		t.Errorf("volumetric.x (anisotropy) = %g, want 0.55", got)
	}
	if got := f32At(buf, volumetricOffset+4); got != 24 {
		t.Errorf("volumetric.y (steps) = %g, want 24", got)
	}
	if want := volumetricOffset + 16; litUBOSize != want {
		t.Errorf("litUBOSize = %d, want %d", litUBOSize, want)
	}
	// 256 bytes exactly, which is what nine shader declarations and the
	// descriptor range have to agree on. Spelled as a number because that is
	// the thing a reader checks against the GLSL.
	if litUBOSize != 256 {
		t.Errorf("litUBOSize = %d, want 256", litUBOSize)
	}
}

// TestPackVolumetricsDefaultsAndClamps covers the two ways this block can be
// wrong in a way no capture would explain.
//
// Nil is the upgrade-safety case, the same one NightGrade and SkyPalette have:
// a caller driving this package directly and never having heard of the field
// would write Steps 0, which marches nothing, so a light that asked to scatter
// would silently not and the only evidence would be an absence.
//
// The clamps are the other end. g at exactly +-1 puts a zero in the
// Henyey-Greenstein denominator at one scattering angle, so one direction of
// one pixel's march becomes an infinity and, once multiplied by a zero-length
// step, a NaN that poisons the whole sum. Clamping on this side costs nothing
// per frame; clamping in the shader would cost it per step per pixel.
//
// Verified to catch a real mistake: dropping the nil guard fails the first
// pair, and dropping either clamp fails the matching case with the value
// passed straight through.
func TestPackVolumetricsDefaultsAndClamps(t *testing.T) {
	var vps [ShadowCascades]mgl32.Mat4
	def := DefaultVolumetrics()

	buf := make([]byte, litUBOSize)
	packLitUBO(buf, vps, nil, nil, nil)
	if got := f32At(buf, volumetricOffset+0); got != def.Anisotropy {
		t.Errorf("nil volumetrics anisotropy = %g, want the default %g", got, def.Anisotropy)
	}
	if got, want := f32At(buf, volumetricOffset+4), float32(def.Steps); got != want {
		t.Errorf("nil volumetrics steps = %g, want the default %g", got, want)
	}

	for _, tc := range []struct {
		name      string
		in        Volumetrics
		wantG     float32
		wantSteps float32
	}{
		{"g above 1", Volumetrics{Anisotropy: 4, Steps: 16}, 0.99, 16},
		{"g below -1", Volumetrics{Anisotropy: -4, Steps: 16}, -0.99, 16},
		{"g NaN", Volumetrics{Anisotropy: float32(math.NaN()), Steps: 16}, -0.99, 16},
		{"steps negative", Volumetrics{Anisotropy: 0, Steps: -5}, 0, 0},
		{"steps past the cap", Volumetrics{Anisotropy: 0, Steps: 1000}, 0, MaxVolumetricSteps},
		{"off is expressible", Volumetrics{Anisotropy: 0.4, Steps: 0}, 0.4, 0},
	} {
		b := make([]byte, litUBOSize)
		v := tc.in
		packLitUBO(b, vps, nil, nil, &v)
		if got := f32At(b, volumetricOffset+0); got != tc.wantG {
			t.Errorf("%s: anisotropy = %g, want %g", tc.name, got, tc.wantG)
		}
		if got := f32At(b, volumetricOffset+4); got != tc.wantSteps {
			t.Errorf("%s: steps = %g, want %g", tc.name, got, tc.wantSteps)
		}
	}
}
