package lut

import (
	"math"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	glyph "github.com/derekmwright/glyphengine"
	"github.com/derekmwright/glyphengine/renderer"
	"github.com/derekmwright/glyphengine/shaders/include"
	xsky "github.com/derekmwright/glyphengine/x/sky"
)

// The table is uploaded through renderer.CreateTextureRGBA16F, so this package's
// api dependency on the engine now includes the half-float transfer in both
// directions; see lut.md's seam table.

// luma is Rec. 709 relative luminance, which is what every brightness claim in
// this file and in the GPU gate is measured in. One function so the unit tests
// and the gate cannot disagree about what "brighter" means.
func luma(c [3]float32) float32 {
	return 0.2126*c[0] + 0.7152*c[1] + 0.0722*c[2]
}

// source builds a Sky with no renderer, for the half of this package that is
// arithmetic. New deliberately needs a renderer -- the table and the source are
// two halves that arrive together -- so there is no exported way to do this, and
// a test is the only caller that should want one.
func source(opts Options) *Sky {
	s := &Sky{opts: opts, palette: opts.Palette}
	if s.palette == (glyph.SkyPalette{}) {
		s.palette = glyph.DefaultSkyPalette()
	}
	s.SetTimeOfDay(opts.TimeOfDay)
	return s
}

// ── the keys against x/sky ──

// TestDefaultKeysAreXSkysCycle is the claim that makes this a second sky of the
// same world rather than a different world: at the four keyed hours, the two
// skies put the sun in the same place, light the ground in the same colour and
// fill it with the same ambient.
//
// It is the only place in this package that mentions x/sky, and it is a test
// rather than the package, so nothing here depends on x/sky at run time. The
// package's own doc says why that matters: a slot with one filling is a slot by
// assertion.
//
// Three different strengths of comparison, deliberately:
//
//   - SunDir exactly, to the bit. sunDirAt is x/sky's expression transcribed in
//     the same order, so anything less than equality would mean it is not.
//   - the DIRECTIONAL LIGHT within 1e-6, because this side multiplies a keyed
//     full-strength colour by an elevation ramp where x/sky multiplies a
//     keyframe lookup by the same ramp. The product is the thing a lit surface
//     sees, and it is what has to agree.
//   - Ambient within 1e-6 rather than exactly, because x/sky reaches its key by
//     interpolating to a fraction of exactly 1 and float32's a+(b-a)*1 is not
//     always b.
//
// Verified to fail: changing the sunset key's SunColor green from 0.5 to 0.55
// reports `key 3 (t=0.75): directional light green 0.431 against x/sky's 0.392`.
// Dropping the z tilt from sunDirAt reports every key's SunDir.
func TestDefaultKeysAreXSkysCycle(t *testing.T) {
	for i, k := range DefaultKeys() {
		dn := xsky.DayNight{TimeOfDay: k.TimeOfDay}
		if got, want := k.SunDir, dn.SunDir(); got != want {
			t.Errorf("key %d (t=%g): SunDir %v, x/sky's %v", i, k.TimeOfDay, got, want)
		}

		intensity := sunIntensity(k.SunDir[1])
		light := [3]float32{k.SunColor[0] * intensity, k.SunColor[1] * intensity, k.SunColor[2] * intensity}
		want := dn.SunColor()
		for ch, name := range [3]string{"red", "green", "blue"} {
			if math.Abs(float64(light[ch]-want[ch])) > 1e-6 {
				t.Errorf("key %d (t=%g): directional light %s %.3f against x/sky's %.3f",
					i, k.TimeOfDay, name, light[ch], want[ch])
			}
		}

		amb := dn.AmbientColor()
		for ch, name := range [3]string{"red", "green", "blue"} {
			if math.Abs(float64(k.Ambient[ch]-amb[ch])) > 1e-6 {
				t.Errorf("key %d (t=%g): ambient %s %.4f against x/sky's %.4f",
					i, k.TimeOfDay, name, k.Ambient[ch], amb[ch])
			}
		}
	}
}

// TestTheSunAgreesBetweenTheKeysToo bounds what four keys cost in the one place
// a four-key day could be badly wrong: where the sun is.
//
// Between keys this interpolates the direction and normalizes, where x/sky walks
// the circle, so the two part company by the difference between a chord and an
// arc. It matters because the sun's elevation is what the whole atmosphere is
// driven from -- the palette, the twilight lobe, the fade that takes the
// directional light out -- so a disagreement here is a disagreement about what
// hour it is, which is visible as the two skies reaching sunset at different
// times.
//
// Measured over 2000 times of day with the default keys: the largest
// disagreement is 0.0689 of elevation, 4 degrees of sun altitude, at time 0.191
// and its three mirrors -- the quarter points of the four segments, where a chord
// sags furthest from its arc. The bound below is that number with a little room,
// so a fifth key or a different orbit moves it.
//
// What it is NOT is a disagreement about sunrise and sunset. The keys sit on the
// horizon crossings, so both skies put the sun at elevation zero at exactly 0.25
// and 0.75, and the fade that takes the directional light out is driven from the
// elevation rather than from the clock. The error is in the middle of the night
// and the middle of the morning, where it moves the palette and nothing else.
//
// Verified to fail: dropping the sunrise key, leaving three, reports a maximum
// elevation disagreement of 0.4080 at time 0.307 against the 0.0700 bound -- and
// takes TestStateIsStaticSourcePlusTheDome with it, since 0.25 is then a third of
// the way from midnight to noon rather than sunrise.
func TestTheSunAgreesBetweenTheKeysToo(t *testing.T) {
	keys := DefaultKeys()
	var worst, worstAt float64
	const steps = 2000
	for i := 0; i < steps; i++ {
		tod := float32(i) / steps
		a, b, frac := segmentAt(keys, tod)
		mine := normalize3(lerp3(a.SunDir, b.SunDir, frac))
		their := xsky.DayNight{TimeOfDay: tod}
		theirs := their.SunDir()
		if d := math.Abs(float64(mine[1] - theirs[1])); d > worst {
			worst, worstAt = d, float64(tod)
		}
	}
	t.Logf("largest sun elevation disagreement with x/sky: %.4f, at time of day %.3f", worst, worstAt)
	if worst > 0.070 {
		t.Errorf("largest sun elevation disagreement %.4f at time %.3f, want at most 0.070", worst, worstAt)
	}
}

// ── the axes and the two copies of the grid ──

// TestAxesInvertTheShadersMapping walks every texel index through bake.go's axis
// and back through the expression skylut.frag uses to turn a value into a
// coordinate, and requires the index to come back.
//
// A half-texel error on any axis is a sky that is slightly the wrong colour
// everywhere -- no seam, no artifact, nothing to look at -- which is why this is
// a test and not an eyeball. The forward expressions below are transcribed from
// the shader; the shader is the thing that cannot be executed here, so this is a
// copy holding a copy, and TestShaderAndBakeAgreeOnTheGrid is what stops the
// sizes drifting under both of them.
//
// Verified to fail: spacing the view axis over lutView instead of lutView-1 --
// the off-by-one-texel this exists for -- reports `view row 1: elevation -0.938
// maps back to texel 0.984` and 62 more rows after it.
func TestAxesInvertTheShadersMapping(t *testing.T) {
	const tol = 1e-3 // texels

	for row := 0; row < lutView; row++ {
		e := viewElevationAt(row)
		vn := 0.5 + 0.5*sign(e)*sqrt(abs(e))
		if back := vn * (lutView - 1); math.Abs(back-float64(row)) > tol {
			t.Errorf("view row %d: elevation %.3f maps back to texel %.3f", row, e, back)
		}
	}
	for slice := 0; slice < lutSun; slice++ {
		s := sunElevationAt(slice)
		sn := 0.5 + 0.5*sign(s)*sqrt(abs(s))
		if back := sn * (lutSun - 1); math.Abs(back-float64(slice)) > tol {
			t.Errorf("sun slice %d: elevation %.3f maps back to texel %.3f", slice, s, back)
		}
	}
	for col := 0; col < lutProx; col++ {
		p := proxAt(col)
		pn := 1 - math.Sqrt(0.5-0.5*float64(p))
		if back := pn * (lutProx - 1); math.Abs(back-float64(col)) > tol {
			t.Errorf("proximity column %d: dot %.4f maps back to texel %.3f", col, p, back)
		}
	}

	// The ends have to be the ends, or the table covers less sky than the shader
	// asks it for and the last texel is stretched over whatever is left.
	for _, c := range []struct {
		name string
		got  float32
		want float32
	}{
		{"view elevation at row 0", viewElevationAt(0), -1},
		{"view elevation at the top row", viewElevationAt(lutView - 1), 1},
		{"sun elevation at slice 0", sunElevationAt(0), -1},
		{"sun elevation at the last slice", sunElevationAt(lutSun - 1), 1},
		{"proximity at column 0", proxAt(0), -1},
		{"proximity at the last column", proxAt(lutProx - 1), 1},
	} {
		if math.Abs(float64(c.got-c.want)) > 1e-6 {
			t.Errorf("%s is %.6f, want %.0f", c.name, c.got, c.want)
		}
	}
}

func sign(v float32) float64 {
	switch {
	case v > 0:
		return 1
	case v < 0:
		return -1
	}
	return 0
}

func abs(v float32) float64  { return math.Abs(float64(v)) }
func sqrt(v float64) float64 { return math.Sqrt(v) }

// TestShaderAndBakeAgreeOnTheGrid reads the three grid constants out of
// skylut.frag and compares them with the ones bake.go bakes with.
//
// They are two copies because the shader's slice arithmetic is written out rather
// than derived from textureSize, and the shader cannot see Go constants. A
// disagreement of one on any axis is not a compile error and not a crash: it is
// a sky sampled off by a fraction of a texel on one axis, which looks like a sky.
//
// There was a fourth, LUT_RANGE, and the check that it is GONE is now part of
// this: the table holds radiance, so a shader that still squared its fetch would
// render a dome that is dark everywhere and black at night, which is a plausible
// enough sky to ship.
//
// Verified to fail: changing LUT_PROX in the shader to 31.0 reports
// `skylut.frag has LUT_PROX = 31, bake.go has 32`. Replacing textureLod with
// texture reports `skylut.frag does not fetch with textureLod; a lookup should ask
// for level 0 rather than depend on the derivative`. Putting `const float
// LUT_RANGE = 3.0;` and `encoded * encoded * LUT_RANGE` back reports both of the
// last two assertions -- `skylut.frag still declares LUT_RANGE; the table holds
// radiance and there is no transfer to undo` and `skylut.frag still squares its
// fetch; the table holds radiance, so the dome would come out dark everywhere` --
// and takes TestCommittedSPIRVMatchesGLSL with it at `skylut.frag.spv is stale
// (4876 bytes committed, 5008 fresh)`.
func TestShaderAndBakeAgreeOnTheGrid(t *testing.T) {
	src, err := readFrag()
	if err != nil {
		t.Fatalf("read skylut.frag: %v", err)
	}
	for _, c := range []struct {
		name string
		want float64
	}{
		{"LUT_VIEW", lutView},
		{"LUT_SUN", lutSun},
		{"LUT_PROX", lutProx},
	} {
		re := regexp.MustCompile(`const float ` + c.name + `\s*=\s*([0-9.]+);`)
		m := re.FindStringSubmatch(src)
		if m == nil {
			t.Errorf("skylut.frag declares no `const float %s`; the grid cannot be checked", c.name)
			continue
		}
		got, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			t.Errorf("skylut.frag's %s is %q, which is not a number", c.name, m[1])
			continue
		}
		if got != c.want {
			t.Errorf("skylut.frag has %s = %g, bake.go has %g", c.name, got, c.want)
		}
	}

	// And the fetch has to be the explicit level. This is a check that the code
	// matches its own comment rather than a check on the picture: measured, an
	// implicit-derivative texture() renders the identical frame (see the comment
	// on the fetch in skylut.frag), so what this holds is that a lookup asks for
	// the level it means instead of depending on a mip chain it does not use.
	if !regexp.MustCompile(`textureLod\(skyLUT`).MatchString(src) {
		t.Error("skylut.frag does not fetch with textureLod; a lookup should ask for level 0 rather than depend on the derivative")
	}

	// And nothing decodes. The table is radiance; the only arithmetic between the
	// fetch and outColor is the slice mix.
	// The declaration, not the word: the comment above the constants names
	// LUT_RANGE to say it is gone, and a check that could not tell those apart
	// would be a check on the prose.
	if regexp.MustCompile(`const\s+float\s+LUT_RANGE`).MatchString(src) {
		t.Error("skylut.frag still declares LUT_RANGE; the table holds radiance and there is no transfer to undo")
	}
	if regexp.MustCompile(`encoded\s*\*\s*encoded`).MatchString(src) {
		t.Error("skylut.frag still squares its fetch; the table holds radiance, so the dome would come out dark everywhere")
	}
}

// TestAtmosphereIncStillSaysWhatWeCopied holds bake.go's Go copy of the
// atmosphere model against the engine's own GLSL.
//
// bake.go has to reimplement atmDaylight, atmTwilight, atmSkyPalette and
// atmSunGlow because there is no way to evaluate a shader from Go, and a copy of
// a curve is a copy: the engine can change its twilight width, x/sky's dome
// follows it, this table does not, and nothing fails to build. The engine exports
// the include set as an embed.FS precisely so a package outside it can compile
// against the version it runs on -- this reads the same bytes and fails if any of
// the expressions it copied have been reworded.
//
// It is a text match, so it is strict in a way that catches reformatting as well
// as remeasurement. That is the right side to err on: a reformat costs one line
// of this test to acknowledge, and a changed constant costs a sky that disagrees
// with the fog in front of it.
//
// It does NOT cover the gradient and the below-horizon fade, which came from
// x/sky's sky.frag. That file belongs to a sibling package this one deliberately
// does not reach into, so those two expressions are quoted verbatim beside the Go
// that reimplements them in bake.go and nothing automatic holds them. Named here
// rather than left to be discovered.
//
// Verified to fail: changing the expected twilight line to `0.21 : 0.115` reports
// `atmosphere.inc no longer contains "float width = sunY > 0.0 ? 0.21 : 0.115;"`.
func TestAtmosphereIncStillSaysWhatWeCopied(t *testing.T) {
	data, err := include.FS.ReadFile("atmosphere.inc")
	if err != nil {
		t.Fatalf("read the engine's atmosphere.inc: %v", err)
	}
	src := string(data)
	for _, want := range []string{
		// atmDaylight
		"return smoothstep(-0.18, 0.10, sunY);",
		// atmTwilight
		"float width = sunY > 0.0 ? 0.20 : 0.115;",
		"return exp(-y * y);",
		// atmSkyPalette
		"zenith  = mix(pal[ATM_ZENITH_NIGHT].rgb,  pal[ATM_ZENITH_DAY].rgb,  day);",
		"horizon = mix(pal[ATM_HORIZON_NIGHT].rgb, pal[ATM_HORIZON_DAY].rgb, day);",
		"zenith  = mix(zenith,  pal[ATM_ZENITH_TWI].rgb,  twi);",
		"horizon = mix(horizon, pal[ATM_HORIZON_TWI].rgb, twi);",
		// atmSunGlow
		"float prox = max(dot(dir, sunDir), 0.0);",
		"vec3 tint = mix(vec3(1.0, 0.45, 0.16), sunCol, clamp(day, 0.0, 1.0) * 0.6);",
		"float halo = pow(prox, 8.0) * (0.35 + 0.9 * twi);",
		"float wash = pow(prox, 1.6) * twi * 0.55;",
		"float horizonBand = exp(-3.5 * abs(dir.y));",
		"return tint * (halo + wash * horizonBand);",
		// atmSunDirFrom, which skylut.frag calls rather than copies.
		"return vec3(horizontal.x, elevation, horizontal.y);",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("atmosphere.inc no longer contains %q; bake.go's copy of the model is stale", want)
		}
	}
}

// ── the table ──

// table decodes a baked default table into [row][slice][col] colours, so the
// checks below read as claims about the sky rather than about half-float offsets.
//
// renderer.Float16Value rather than a decoder of this package's own: the engine
// owns both directions of the transfer now, and a second copy out here would be
// free to disagree with what Bake encoded with.
func table(t *testing.T, opts Options) [lutView][lutSun][lutProx][3]float32 {
	t.Helper()
	pixels, w, h, err := Bake(opts)
	if err != nil {
		t.Fatalf("bake: %v", err)
	}
	if w != lutSun*lutProx || h != lutView {
		t.Fatalf("baked %dx%d, want %dx%d", w, h, lutSun*lutProx, lutView)
	}
	if len(pixels) != w*h*4 {
		t.Fatalf("baked %d half-floats for %dx%d RGBA, want %d", len(pixels), w, h, w*h*4)
	}
	var out [lutView][lutSun][lutProx][3]float32
	for row := 0; row < lutView; row++ {
		for slice := 0; slice < lutSun; slice++ {
			for col := 0; col < lutProx; col++ {
				at := (row*w + slice*lutProx + col) * 4
				out[row][slice][col] = [3]float32{
					renderer.Float16Value(pixels[at]),
					renderer.Float16Value(pixels[at+1]),
					renderer.Float16Value(pixels[at+2]),
				}
				if a := renderer.Float16Value(pixels[at+3]); a != 1 {
					t.Fatalf("texel (%d,%d,%d) has alpha %g, want 1", row, slice, col, a)
				}
			}
		}
	}
	return out
}

// relLevels are the linear values the relative encoding error is reported above.
//
// Three levels is what the eight-bit transfer needed: its error was
// 2*(0.5/255)/sqrt(v/3), which grew without bound as the value fell, so one
// figure would have said more about the darkest texel in the table than about the
// encoding. Half-float's error is relative by construction and the three levels
// now read the same, which is the whole point of the change -- they are kept
// because reporting all three is what shows that.
var relLevels = [3]float32{0.002, 0.01, 0.1}

// awayFromTheSun is proximity column 0, dot(dir, sunDir) = -1: the one column
// where the scattering lobes contribute exactly nothing, because both are powers
// of max(prox, 0). It is where "the gradient" means the gradient.
const awayFromTheSun = 0

// gradientLadder is the elevations the gradient is read at, from just above the
// horizon to near the zenith.
//
// A ladder of elevations rather than every row, because the view axis is a
// signed square: rows near the horizon are a thousandth of elevation apart, and
// the colour difference across one of them is small enough that it was below a
// single step of the eight-bit encoding this table used to carry. That is no
// longer the reason -- half-float resolves a relative 0.05 percent everywhere, so
// adjacent rows now differ in the direction the model says -- but the ladder is
// kept: what it checks is the gradient over the sky, and 64 rows of which 30 sit
// in the first tenth of the climb is a check on the axis spacing instead.
//
// In degrees, roughly: 1.1, 2.9, 5.7, 11.5, 20.5, 30, 44.4, 64.2.
var gradientLadder = [...]float32{0.02, 0.05, 0.10, 0.20, 0.35, 0.50, 0.70, 0.90}

// rowFor is the shader's view-elevation mapping, rounded to a texel: the row
// whose own elevation is nearest e.
func rowFor(e float32) int {
	vn := 0.5 + 0.5*sign(e)*sqrt(abs(e))
	return int(math.Round(vn * (lutView - 1)))
}

// TestTheGradientFallsFromHorizonToZenith is the direction the model says, read
// out of the bytes the GPU will sample.
//
// Looking directly away from the sun, luminance falls from the horizon to the
// zenith at every hour. That is not a general fact about skies -- Earth's day
// palette is pale at the rim because that is where the light path through the air
// is longest -- it is a fact about this palette, and it is the claim `task
// xskylut` checks in pixels. Checking it here as well is not duplication: this
// sees all 32 hours with no tonemap, no bloom and no camera in the way, and the
// gate sees what a frame actually does with them.
//
// Two different assertions, split where there is light to assert about:
//
//   - in the daylit half, every rung of the ladder is darker than the one below
//     it. Measured, the smallest whole-ladder drop in that half is 0.0375, at a
//     sun elevation of -0.176 where the dome is nearly all night palette already.
//   - below that, only the whole-ladder drop, and only that it is positive. The
//     midnight dome is 0.005 of luminance at the horizon and 0.002 at the
//     zenith, five and two bytes out of 255: the direction is all that survives
//     the encoding, and a floor on the size of it would be a floor on how dark
//     the palette is allowed to make night.
//
// Verified to fail: swapping the gradient's two endpoints reports
// `sun elevation -1.000: the horizon-to-zenith drop is -0.00261; there is no
// gradient at all` for all 32 hours, and at the hours with light in them also
// `sun elevation -0.176: luminance rises from 0.0165 at elevation 0.02 to 0.0189
// at elevation 0.05`. A table of one repeated texel -- the GPU gate's break --
// fails the drop instead, at exactly zero.
func TestTheGradientFallsFromHorizonToZenith(t *testing.T) {
	tab := table(t, DefaultOptions())

	smallestDaylitDrop, smallestDaylitAt := float32(math.MaxFloat32), float32(0)
	for slice := 0; slice < lutSun; slice++ {
		s := sunElevationAt(slice)
		read := func(e float32) float32 { return luma(tab[rowFor(e)][slice][awayFromTheSun]) }
		daylit := s >= -0.2

		smallestStep := float32(math.MaxFloat32)
		for i := 0; i+1 < len(gradientLadder); i++ {
			lo, hi := read(gradientLadder[i]), read(gradientLadder[i+1])
			if step := lo - hi; step < smallestStep {
				smallestStep = step
			}
			if daylit && hi > lo {
				t.Errorf("sun elevation %+.3f: luminance rises from %.4f at elevation %.2f to %.4f at elevation %.2f",
					s, lo, gradientLadder[i], hi, gradientLadder[i+1])
				break
			}
		}
		drop := read(gradientLadder[0]) - read(gradientLadder[len(gradientLadder)-1])
		if drop <= 0 {
			t.Errorf("sun elevation %+.3f: the horizon-to-zenith drop is %.5f; there is no gradient at all", s, drop)
		}
		// 0.03 because the smallest measured in the daylit half is 0.0375, at a
		// sun elevation of -0.176. Above the horizon they are all above 0.33.
		if daylit && drop < 0.03 {
			t.Errorf("sun elevation %+.3f: the horizon-to-zenith drop is only %.4f in the daylit half", s, drop)
		}
		if daylit && drop < smallestDaylitDrop {
			smallestDaylitDrop, smallestDaylitAt = drop, s
		}
		if slice == 0 || slice == lutSun/2 || slice == lutSun-1 {
			t.Logf("sun elevation %+.3f: horizon %.4f, zenith %.4f, drop %.4f, smallest rung %.4f",
				s, read(gradientLadder[0]), read(gradientLadder[len(gradientLadder)-1]), drop, smallestStep)
		}
	}
	// Logged rather than only asserted, because this is the number the floor
	// above is set from and the next person to change the palette needs it.
	t.Logf("smallest horizon-to-zenith drop in the daylit half: %.4f, at sun elevation %+.3f",
		smallestDaylitDrop, smallestDaylitAt)
}

// TestTheSunSideIsBrighterAtDawn is the other half of the shape, and the half
// that says the proximity axis is doing anything: with the sun on the horizon,
// the half of the sky it is in is brighter than the half opposite.
//
// A table with no proximity axis would pass the gradient check above perfectly
// and have no sun in it at all, which is why this check is here and why the GPU
// gate renders two captures 180 degrees apart.
//
// Measured on the default palette: 1.0465 toward the sun against 0.2806 away, a
// ratio of 3.73, at a view elevation of 0.274 with the sun on the horizon.
//
// Verified to fail: zeroing atmSunGlow's return reports `with the sun on the
// horizon, the sun's side reads 0.2806 and the far side 0.2806, a ratio of
// 1.00`.
func TestTheSunSideIsBrighterAtDawn(t *testing.T) {
	tab := table(t, DefaultOptions())

	// The slice nearest a sun exactly on the horizon, and an elevation well up
	// the sky so the reading is the halo and the wash rather than the horizon
	// band both sides share.
	slice := lutSun / 2
	row := lutView/2 + lutView/4
	near := luma(tab[row][slice][lutProx-1])
	far := luma(tab[row][slice][awayFromTheSun])
	t.Logf("sun elevation %+.4f, view elevation %+.3f: toward the sun %.4f, away %.4f, ratio %.2f",
		sunElevationAt(slice), viewElevationAt(row), near, far, near/far)
	if near < far+0.05 || near/far < 1.5 {
		t.Errorf("with the sun on the horizon, the sun's side reads %.4f and the far side %.4f, a ratio of %.2f",
			near, far, near/far)
	}
}

// TestTheTableIsTheModel is the encoding's error budget: every texel decoded out
// of the table against the model evaluated at that texel's own coordinates.
//
// Interpolation is not in question here -- a texel is exactly a sample point --
// so what is left is the eight bits, and this is where the number that belongs in
// lut.md comes from. It matters because the alternative to this encoding is a
// format the engine cannot upload (see lut.md), so the error is the price of the
// package rather than a bug, and a price has to be known.
//
// RGBA8 holding sqrt(v/3) measured a worst absolute error of 0.0105 and relative
// errors of 1.3 percent above a tenth of full scale, 6.8 percent above a
// hundredth and 15 percent above two thousandths -- the square transfer and
// nothing else, 2*(0.5/255)/sqrt(v/3), unimprovable by any other curve over 255
// levels. Half-float is 0.049 percent at every level, which is half a step of a
// 10-bit significand (2^-11 = 0.000488) and therefore the format and nothing
// else. The bounds below are those.
//
// The absolute bound has to SCALE with the value now, which is the real
// difference: the eight-bit table was worst in absolute terms at the top of its
// range and worst in relative terms at the bottom, and half-float is relative
// everywhere. 0.0013 is 0.049 percent of the brightest texel the default palette
// reaches (2.505, logged below).
//
// Verified to fail: writing the radiance as renderer.Float16(c[ch]*0.99) -- a
// one-percent scale error, which is the size of mistake a transfer change can
// hide -- reports `worst absolute encoding error 0.025848, want at most 0.0013`
// and `worst relative error 0.0105 above 0.002, want at most 0.0006` at all three
// levels. The measured figures with it in place are 0.000488 relative at every
// level and 0.000976 absolute.
func TestTheTableIsTheModel(t *testing.T) {
	opts := DefaultOptions()
	pal := endpoints(glyph.DefaultSkyPalette())
	tab := table(t, opts)

	var worst float32
	var worstRel [len(relLevels)]float32
	for slice := 0; slice < lutSun; slice++ {
		s := sunElevationAt(slice)
		sunCol := glowColorAt(opts.Keys, s)
		for row := 0; row < lutView; row++ {
			e := viewElevationAt(row)
			for col := 0; col < lutProx; col++ {
				want := domeColor(pal, e, s, proxAt(col), sunCol)
				got := tab[row][slice][col]
				for ch := 0; ch < 3; ch++ {
					d := absf(got[ch] - want[ch])
					if d > worst {
						worst = d
					}
					// Relative error is the one that shows at night, where the
					// whole sky is thousandths and an absolute budget says
					// nothing. Reported at three levels because it grows as the
					// value falls, so one figure would say more about whichever
					// corner of the table is darkest than about the encoding.
					for i, level := range relLevels {
						if want[ch] > level {
							if r := d / want[ch]; r > worstRel[i] {
								worstRel[i] = r
							}
						}
					}
				}
			}
		}
	}
	for i, level := range relLevels {
		t.Logf("half-float table: worst relative error %.6f above %g", worstRel[i], level)
	}
	t.Logf("half-float table: worst absolute error %.6f", worst)
	// Half a step of a 10-bit significand is 2^-11 = 0.000488, so the absolute
	// error cannot exceed that fraction of the brightest texel. 0.0013 is that
	// against the default palette's 2.505.
	if worst > 0.0013 {
		t.Errorf("worst absolute encoding error %.6f, want at most 0.0013", worst)
	}
	// Every level, not just the middle one. With the transfer gone there is no
	// level at which the error is a bound on a chosen curve rather than on the
	// format, which is why all three are asserted now where only one used to be.
	for i, level := range relLevels {
		if worstRel[i] > 0.0006 {
			t.Errorf("worst relative error %.4f above %g, want at most 0.0006", worstRel[i], level)
		}
	}
}

// TestABrightPaletteNeedsNoRefusing is the test that used to be its opposite.
//
// Bake refused a palette whose brightest texel passed 3.0, because the table was
// RGBA8 holding sqrt(v/3) and a clipped table is a flat white patch where the sun
// is -- which reads as a shader bug rather than as an encoding limit. There is no
// ceiling now: half-float reaches 65504, so a palette five times Earth's is a
// bright sky and nothing else, and this is what says the refusal did not survive
// as a leftover guard.
//
// The round trip is asserted at the SAME relative precision the default palette
// gets, which is the claim that matters: a wider table is not useful if the error
// grows with the values in it. It does not -- half-float's error is relative.
//
// Verified to fail: clamping the bake to 3.0 (`if c[ch] > 3 { c[ch] = 3 }` before
// the encode), which is what the old ceiling looked like from the inside, reports
// `five times Earth's palette: texel (22,11,26) red is 3.000, want 3.015` -- the
// first texel over the line, which is the sun's halo starting to flatten into the
// flat white patch the refusal existed to avoid.
func TestABrightPaletteNeedsNoRefusing(t *testing.T) {
	bright := glyph.DefaultSkyPalette()
	bright.ZenithDay = bright.ZenithDay.Mul(5)
	bright.HorizonDay = bright.HorizonDay.Mul(5)
	bright.ZenithTwilight = bright.ZenithTwilight.Mul(5)
	bright.HorizonTwilight = bright.HorizonTwilight.Mul(5)
	bright.ZenithNight = bright.ZenithNight.Mul(5)
	bright.HorizonNight = bright.HorizonNight.Mul(5)
	opts := DefaultOptions()
	opts.Palette = bright

	pal := endpoints(bright)
	tab := table(t, opts)
	var peak, worstRel float32
	for slice := 0; slice < lutSun; slice++ {
		s := sunElevationAt(slice)
		sunCol := glowColorAt(opts.Keys, s)
		for row := 0; row < lutView; row++ {
			e := viewElevationAt(row)
			for col := 0; col < lutProx; col++ {
				want := domeColor(pal, e, s, proxAt(col), sunCol)
				got := tab[row][slice][col]
				for ch := 0; ch < 3; ch++ {
					if want[ch] > peak {
						peak = want[ch]
					}
					if want[ch] > relLevels[0] {
						if r := absf(got[ch]-want[ch]) / want[ch]; r > worstRel {
							worstRel = r
						}
					}
					if absf(got[ch]-want[ch]) > 0.0006*absf(want[ch])+1e-6 {
						t.Fatalf("five times Earth's palette: texel (%d,%d,%d) %s is %.3f, want %.3f",
							row, slice, col, [3]string{"red", "green", "blue"}[ch], got[ch], want[ch])
					}
				}
			}
		}
	}
	t.Logf("five times Earth's palette: brightest texel %.3f, worst relative error %.6f", peak, worstRel)
	if peak < 3 {
		t.Fatalf("the bright palette peaks at %.3f, under the 3.0 the old encoding refused; this case proves nothing", peak)
	}
}

// TestTheDefaultPaletteBrightestTexel pins how bright Earth's sky gets in this
// model, because it is the number the absolute error bound above is set from and
// the number lut.md quotes. A change to the model, the palette defaults or the
// keys that moves it is a change that should be noticed here.
//
// It used to be a HEADROOM test: the same peak against the 3.0 ceiling, with a
// floor at 60 percent of it, because an eight-bit encoding that spent its levels
// on range nothing reached was coarser than it needed to be. Half-float spends no
// levels on range, so there is nothing to be economical about and the peak is
// just a number to record.
func TestTheDefaultPaletteBrightestTexel(t *testing.T) {
	tab := table(t, DefaultOptions())
	var peak float32
	var atSun, atView, atProx float32
	for row := 0; row < lutView; row++ {
		for slice := 0; slice < lutSun; slice++ {
			for col := 0; col < lutProx; col++ {
				for _, v := range tab[row][slice][col] {
					if v > peak {
						peak, atSun, atView, atProx = v, sunElevationAt(slice), viewElevationAt(row), proxAt(col)
					}
				}
			}
		}
	}
	t.Logf("brightest texel %.3f, at sun elevation %+.3f, view elevation %+.3f, proximity %+.3f",
		peak, atSun, atView, atProx)
	// The bound the absolute error budget in TestTheTableIsTheModel is derived
	// from. Wide on purpose: what would make that budget wrong is the peak moving
	// by a factor, not by a hundredth.
	if peak < 2 || peak > 3 {
		t.Errorf("brightest texel %.3f; TestTheTableIsTheModel's absolute bound is derived from 2.505", peak)
	}
}

// ── the source ──

// TestStateIsStaticSourcePlusTheDome pins the per-frame contract: the light and
// the air are the engine's own resolution of them, and the only thing this
// package adds is the one flag that asks for a dome.
//
// Every zero below is load-bearing. DrawStars, DrawSun and DrawMoon false is what
// makes the engine place no bodies, CloudSteps and Cirrus zero is what makes it
// march nothing, LightShafts zero is what keeps a shaft pass from radiating from
// a disc that is not drawn, and a zero SkyPalette is the sentinel for "the
// scene's" -- which is the one thing that lets Scene.SetSkyPalette still reach the
// fog and the water under a dome it cannot reach.
//
// Verified to fail: setting st.DrawStars = true in State reports `DrawStars is
// true; this sky supplies no star shader and places no bodies, so the engine would
// record a draw with no pipeline`.
func TestStateIsStaticSourcePlusTheDome(t *testing.T) {
	opts := DefaultOptions()
	opts.TimeOfDay = 0.25
	s := source(opts)
	st := s.State()

	key := DefaultKeys()[1] // sunrise
	if st.SunDir != key.SunDir {
		t.Errorf("SunDir %v, want the sunrise key's %v", st.SunDir, key.SunDir)
	}
	if st.RealSunDir != key.SunDir {
		t.Errorf("RealSunDir %v, want the sunrise key's %v; with no moon the light and the sun are one vector", st.RealSunDir, key.SunDir)
	}
	if st.SunElevation != key.SunDir[1] {
		t.Errorf("SunElevation %g, want %g", st.SunElevation, key.SunDir[1])
	}
	if st.Ambient != key.Ambient {
		t.Errorf("Ambient %v, want the sunrise key's %v", st.Ambient, key.Ambient)
	}
	if st.FogDensity != glyph.DefaultFogDensity {
		t.Errorf("FogDensity %g, want the default %g", st.FogDensity, glyph.DefaultFogDensity)
	}
	if !st.DrawSky {
		t.Error("DrawSky is false; nothing would ask for the dome")
	}
	if !st.CastShadows {
		t.Error("CastShadows is false with the sun up")
	}
	for _, c := range []struct {
		name string
		on   bool
	}{{"DrawStars", st.DrawStars}, {"DrawSun", st.DrawSun}, {"DrawMoon", st.DrawMoon}} {
		if c.on {
			t.Errorf("%s is true; this sky supplies no star shader and places no bodies, so the engine would record a draw with no pipeline", c.name)
		}
	}
	if st.CloudSteps != 0 || st.Cirrus != 0 {
		t.Errorf("CloudSteps %d, Cirrus %g; this sky supplies no cloud shader", st.CloudSteps, st.Cirrus)
	}
	if st.LightShafts != 0 {
		t.Errorf("LightShafts %g; the shafts radiate from a sun disc this sky does not draw", st.LightShafts)
	}
	if st.SkyPalette != (glyph.SkyPalette{}) || st.NightGrade != (glyph.NightGrade{}) {
		t.Error("SkyPalette or NightGrade is set; the zero value is the sentinel for the scene's, and setting it here takes Scene.SetSkyPalette away from the fog and the water")
	}
}

// TestTheSunGoesOutWhenItSets is the one approximation in the four-key day that
// would be visible as a mistake rather than as a style: a directional light still
// burning from below the horizon.
//
// The fade is keyed on elevation and not on the clock for exactly this, and this
// is what says so. Midnight, and the two hours either side of it, must have no
// directional light and cast no shadows.
//
// Verified to fail: removing the elevation fade from resolve reports `time 0.000:
// the sun is 79 degrees below the horizon and still delivers 1.000/0.600/0.300`
// and `time 0.000: shadows are on with no sun`, for all seven hours.
func TestTheSunGoesOutWhenItSets(t *testing.T) {
	s := source(DefaultOptions())
	for _, tod := range []float32{0.0, 0.05, 0.125, 0.2, 0.8, 0.875, 0.95} {
		s.SetTimeOfDay(tod)
		st := s.State()
		if st.SunElevation > -0.14 {
			t.Fatalf("time %.3f: the sun is at elevation %.3f, which is not below the horizon; this case proves nothing", tod, st.SunElevation)
		}
		if st.SunColor != ([3]float32{}) {
			t.Errorf("time %.3f: the sun is %.0f degrees below the horizon and still delivers %.3f/%.3f/%.3f",
				tod, -math.Asin(float64(st.SunElevation))*180/math.Pi,
				st.SunColor[0], st.SunColor[1], st.SunColor[2])
		}
		if st.CastShadows {
			t.Errorf("time %.3f: shadows are on with no sun", tod)
		}
		// The ambient does not go out, or night would be black rather than dark.
		if luma(st.Ambient) <= 0 {
			t.Errorf("time %.3f: the ambient is black", tod)
		}
	}
}

// TestTheClockWrapsAndAdvancesInSeconds covers the two ways the clock is moved.
//
// The wrap is in SetTimeOfDay rather than left to the caller for the reason
// x/sky's records: a game assigning 1.2 would silently get a clamped endpoint
// instead of the hour it asked for.
//
// Verified to fail: dropping the floor subtraction from SetTimeOfDay reports
// `SetTimeOfDay(1.25) left the clock at 1.250, want 0.250` and two more.
func TestTheClockWrapsAndAdvancesInSeconds(t *testing.T) {
	s := source(DefaultOptions())
	for _, c := range []struct{ set, want float32 }{{1.25, 0.25}, {-0.1, 0.9}, {2.5, 0.5}, {0.75, 0.75}} {
		s.SetTimeOfDay(c.set)
		if math.Abs(float64(s.TimeOfDay()-c.want)) > 1e-6 {
			t.Errorf("SetTimeOfDay(%g) left the clock at %.3f, want %.3f", c.set, s.TimeOfDay(), c.want)
		}
	}

	// Through the seam: Scene.Tick calls Advance on the fixed tick, so a day runs
	// in simulation seconds and a paused scene has a stationary sun.
	s = source(Options{Keys: DefaultKeys(), TimeOfDay: 0.2, Speed: 1.0 / 120})
	scene := glyph.NewScene()
	scene.Env = s
	for i := 0; i < 60; i++ {
		scene.Tick(1.0 / 60)
	}
	if want := float32(0.2 + 1.0/120); math.Abs(float64(s.TimeOfDay()-want)) > 1e-5 {
		t.Errorf("after one simulated second the clock is %.6f, want %.6f", s.TimeOfDay(), want)
	}

	// Speed zero is a sky that does not move, which is what DefaultOptions is.
	frozen := source(DefaultOptions())
	frozen.Advance(10)
	if frozen.TimeOfDay() != 0.25 {
		t.Errorf("a frozen sky moved to %.3f", frozen.TimeOfDay())
	}
}

// TestStateAndAdvanceAllocateNothing is the per-frame cost of the seam on this
// side of it. State is called once per rendered frame and Advance once per fixed
// tick, so an allocation in either is paid by every frame of every game.
//
// The one thing with something to prove is State's delegation: it builds a
// glyphengine.StaticSource literal holding pointers into the Sky, and a literal
// that escaped would be an allocation per frame bought for a comment about not
// duplicating the engine's rules.
//
// Verified to fail: a package-level `var escapeSink *glyph.StaticSource` assigned
// from State's literal reports `1 allocations per frame resolving the environment`
// for all three shapes, and leaves Advance at zero -- which is the shape of that
// mistake.
func TestStateAndAdvanceAllocateNothing(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  glyph.EnvironmentSource
	}{
		{"frozen at sunrise", source(DefaultOptions())},
		{"a moving clock", source(Options{Keys: DefaultKeys(), TimeOfDay: 0.4, Speed: 1.0 / 120, Fog: &glyph.Fog{Density: 0.01}})},
		{"one key, no fog", source(Options{Keys: DefaultKeys()[1:2]})},
	} {
		if n := testing.AllocsPerRun(200, func() { _ = tc.src.State() }); n != 0 {
			t.Errorf("%s: %v allocations per frame resolving the environment", tc.name, n)
		}
		if n := testing.AllocsPerRun(200, func() { tc.src.Advance(1.0 / 60) }); n != 0 {
			t.Errorf("%s: %v allocations per tick advancing", tc.name, n)
		}
	}
}

// TestOptionsAreValidated covers the key list, since a malformed one is the only
// way to get an error out of New that is not about the renderer or the palette,
// and an unsorted list would otherwise resolve to a silently wrong hour.
func TestOptionsAreValidated(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts Options
	}{
		{"no keys", Options{}},
		{"a key outside [0,1)", Options{Keys: []Key{{TimeOfDay: 1.5, SunDir: [3]float32{0, 1, 0}}}}},
		{"keys out of order", Options{Keys: []Key{
			{TimeOfDay: 0.5, SunDir: [3]float32{0, 1, 0}},
			{TimeOfDay: 0.25, SunDir: [3]float32{1, 0, 0}},
		}}},
		{"a zero sun direction", Options{Keys: []Key{{TimeOfDay: 0.25}}}},
	} {
		if _, _, _, err := Bake(tc.opts); err == nil {
			t.Errorf("%s was accepted", tc.name)
		}
	}
	if err := DefaultOptions().validate(); err != nil {
		t.Errorf("DefaultOptions is invalid: %v", err)
	}
}

// ── the slot ──

// TestShadersFillOnlyTheDome is the other half of the two halves: a source that
// asks for a dome with no shader to draw one is a frame that silently has no sky,
// and a package that quietly replaced a lit stage would change every surface in
// the frame.
//
// It also pins the two stages this sky leaves nil. That is not an omission to be
// tidied up later: nil is what makes the engine build no star or cloud pipeline
// and record no draw, which is most of what choosing this sky buys.
//
// Verified to fail: starting Shaders() from renderer.DefaultShaders() names every
// stage the engine embeds, beginning `Shaders() set TriangleVert (1504 bytes); it
// must leave every stage but the dome alone`. Setting a cloud stage in Fill reports
// `Fill set CloudsFrag; this sky supplies no cloud march, and a nil stage is what
// turns the engine's cloud pass off`.
func TestShadersFillOnlyTheDome(t *testing.T) {
	got := Shaders()
	if len(got.SkyFrag) == 0 {
		t.Error("SkyFrag is nil; the sky slot is not filled")
	}
	if got.StarsFrag != nil {
		t.Error("Fill set StarsFrag; this sky has no stars, and a nil stage is what turns the engine's star draw off")
	}
	if got.CloudsFrag != nil {
		t.Error("Fill set CloudsFrag; this sky supplies no cloud march, and a nil stage is what turns the engine's cloud pass off")
	}

	v := reflect.ValueOf(got)
	for i := 0; i < v.NumField(); i++ {
		if name := v.Type().Field(i).Name; name != "SkyFrag" {
			if b := v.Field(i).Bytes(); b != nil {
				t.Errorf("Shaders() set %s (%d bytes); it must leave every stage but the dome alone", name, len(b))
			}
		}
	}

	// And the engine's defaults have to leave the slot empty, or none of the
	// above matters: a dome would arrive from the engine and this package would
	// be decoration.
	d := renderer.DefaultShaders()
	if d.SkyFrag != nil || d.StarsFrag != nil || d.CloudsFrag != nil {
		t.Errorf("renderer.DefaultShaders() fills the sky slot: SkyFrag %d, StarsFrag %d, CloudsFrag %d bytes",
			len(d.SkyFrag), len(d.StarsFrag), len(d.CloudsFrag))
	}

	// Fill leaves a dome the caller already set, so a game with its own dome and
	// this package's source is a supported arrangement.
	mine := []byte("my dome")
	if got := Fill(renderer.ShaderSet{SkyFrag: mine}); string(got.SkyFrag) != string(mine) {
		t.Error("Fill overwrote a SkyFrag the caller had already set")
	}

	// The slot the table binds to has to be one the renderer has, and the shader
	// has to name the binding that slot becomes: light-set binding 7+n.
	if ShaderTextureSlot < 0 || ShaderTextureSlot >= renderer.ShaderTextureSlots {
		t.Fatalf("ShaderTextureSlot is %d, outside the renderer's [0,%d)", ShaderTextureSlot, renderer.ShaderTextureSlots)
	}
	src, err := readFrag()
	if err != nil {
		t.Fatalf("read skylut.frag: %v", err)
	}
	want := "layout(set = 1, binding = " + strconv.Itoa(7+ShaderTextureSlot) + ") uniform sampler2D skyLUT;"
	if !strings.Contains(src, want) {
		t.Errorf("skylut.frag does not declare %q; ShaderTextureSlot %d is binding %d",
			want, ShaderTextureSlot, 7+ShaderTextureSlot)
	}
}

// readFrag reads the GLSL beside this test. Two tests compare the shader's text
// with Go constants, because the shader is the one thing in this package that
// cannot be executed from a test and its copies of the grid and the binding are
// exactly the kind that drift in silence.
func readFrag() (string, error) {
	b, err := os.ReadFile("skylut.frag")
	return string(b), err
}
