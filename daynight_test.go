package glyphengine

import (
	"math"
	"testing"
)

func lum(c [3]float32) float64 {
	return 0.2126*float64(c[0]) + 0.7152*float64(c[1]) + 0.0722*float64(c[2])
}

// TestCycleIsContinuous is the regression test for the whole class of bug this
// model replaced.
//
// The old cycle keyed the sun's light off the clock and cut it to zero the
// instant TimeOfDay crossed 0.75. Sampled every 1/400th of a cycle, that showed
// up as the sun's luminance falling 0.585 to 0.000 between two adjacent
// samples. On screen it was worse than a flicker: the sunset glow is tinted by
// the sun's colour, so the scattering vanished at the moment it should have
// been strongest, and every shadow in the scene flipped direction as the light
// swapped to the moon.
//
// Nothing here should move more than a few percent of its range in 1/400th of
// a cycle. The curves are steep around sunrise and sunset -- they are supposed
// to be -- but steep is not the same as discontinuous.
func TestCycleIsContinuous(t *testing.T) {
	// The threshold is a fraction of each curve's own range rather than an
	// absolute value, because the curves have very different scales and the
	// interesting property is scale-free: a discontinuity crosses most of its
	// range in a single step, while a merely steep curve spreads the same
	// distance over many. The old sun-light cliff crossed 100% of its range in
	// one step. Everything here should stay well under a fifth.
	const (
		step   = 0.0025
		maxFrc = 0.20
	)

	curves := map[string]func(*DayNight) float64{
		"sun light":     func(d *DayNight) float64 { return lum(d.SunColor()) },
		"sun disc":      func(d *DayNight) float64 { return lum(d.SunDiscColor()) },
		"moon light":    func(d *DayNight) float64 { return lum(d.MoonColor()) },
		"ambient":       func(d *DayNight) float64 { return lum(d.AmbientColor()) },
		"star fade":     func(d *DayNight) float64 { return float64(d.StarVisibility()) },
		"daylight":      func(d *DayNight) float64 { return float64(d.Daylight()) },
		"twilight":      func(d *DayNight) float64 { return float64(d.Twilight()) },
		"primary light": func(d *DayNight) float64 { _, c := d.PrimaryLight(); return lum(c) },
	}

	for name, f := range curves {
		var worst, worstAt float64
		lo, hi := math.Inf(1), math.Inf(-1)

		prev := f(&DayNight{TimeOfDay: 0})
		// Wrap past 1.0 so midnight is covered too.
		for x := step; x <= 1.0+step; x += step {
			cur := f(&DayNight{TimeOfDay: float32(math.Mod(x, 1.0))})
			if d := math.Abs(cur - prev); d > worst {
				worst, worstAt = d, x
			}
			lo, hi = math.Min(lo, cur), math.Max(hi, cur)
			prev = cur
		}

		rng := hi - lo
		if rng < 1e-6 {
			continue // constant curve, nothing to jump
		}
		if frac := worst / rng; frac > maxFrc {
			t.Errorf("%s crosses %.0f%% of its range (%.3f) in one step of %.4f at t=%.4f; want <= %.0f%%",
				name, frac*100, rng, step, worstAt, maxFrc*100)
		}
	}
}

// TestSunKeepsLightingPastTheHorizon pins the specific mistake: the sun's
// contribution has to survive its own sunset, because civil twilight is lit by
// a sun that has already gone down.
func TestSunKeepsLightingPastTheHorizon(t *testing.T) {
	// Find the moment the sun crosses the horizon going down.
	var setAt float32 = -1
	for x := 0.5; x < 1.0; x += 0.0005 {
		if (&DayNight{TimeOfDay: float32(x)}).SunDir()[1] <= 0 {
			setAt = float32(x)
			break
		}
	}
	if setAt < 0 {
		t.Fatal("sun never sets")
	}

	justBefore := lum((&DayNight{TimeOfDay: setAt - 0.002}).SunColor())
	justAfter := lum((&DayNight{TimeOfDay: setAt + 0.002}).SunColor())

	if justAfter == 0 && justBefore > 0 {
		t.Fatalf("sun light cut to zero at the horizon (%.3f -> %.3f)", justBefore, justAfter)
	}
	if ratio := justAfter / justBefore; ratio < 0.5 {
		t.Errorf("sun light fell to %.0f%% across the horizon; want a gradual fade", ratio*100)
	}
}

// TestTwilightPeaksAtTheHorizon checks the warm scattering is strongest as the
// sun sets rather than being switched off by nightfall.
//
// In the old model the glow was multiplied by (1 - starVisibility), so night
// arriving erased the sunset. The two are now independent, and this asserts it:
// twilight has to still be substantial at the point where the stars are already
// coming out.
func TestTwilightPeaksAtTheHorizon(t *testing.T) {
	var peak, peakAt float64
	for x := 0.6; x < 0.95; x += 0.001 {
		if v := float64((&DayNight{TimeOfDay: float32(x)}).Twilight()); v > peak {
			peak, peakAt = v, x
		}
	}
	if peak < 0.95 {
		t.Errorf("twilight peaks at only %.2f; want ~1", peak)
	}

	// The peak should land where the sun is on the horizon.
	if y := (&DayNight{TimeOfDay: float32(peakAt)}).SunDir()[1]; math.Abs(float64(y)) > 0.03 {
		t.Errorf("twilight peaks at sun elevation %.3f; want the horizon", y)
	}

	// And nothing but elevation may feed it. Multiplying by (1 - starVisibility)
	// is the original bug, and the two curves overlap enough that most sample
	// points cannot tell them apart -- at the moment the stars first appear the
	// gated curve still reads 0.73 against a correct 0.77, which no sane
	// threshold separates.
	//
	// So sample where they diverge most, measured rather than guessed: sun
	// elevation -0.117, a quarter of the way into the stars coming out, where
	// the correct curve gives 0.376 and the gated one 0.278. 0.33 sits between
	// them. Reintroducing the multiply drops it to 0.278 and this fires --
	// verified by doing exactly that.
	//
	// This threshold moved down from 0.45-at-half-star-visibility, which the
	// asymmetric curve legitimately fails: narrowing the below-horizon width to
	// 0.115 is what stops the glow lingering into full night, and it is the
	// change that made the sunset look right. The invariant being guarded is
	// unchanged -- twilight is a function of sun elevation alone.
	for x := 0.6; x < 0.95; x += 0.001 {
		dn := &DayNight{TimeOfDay: float32(x)}
		if dn.StarVisibility() > 0.25 {
			if dn.Twilight() < 0.33 {
				t.Errorf("a quarter into nightfall (sun elevation %.3f) twilight is %.3f, want >= 0.33; "+
					"something other than sun elevation is scaling the glow",
					dn.SunDir()[1], dn.Twilight())
			}
			break
		}
	}
}

// TestStarsLagTheSunset checks stars arrive during the blue hour rather than
// the instant the sun touches the horizon.
func TestStarsLagTheSunset(t *testing.T) {
	var setAt float32 = -1
	for x := 0.5; x < 1.0; x += 0.0005 {
		if (&DayNight{TimeOfDay: float32(x)}).SunDir()[1] <= 0 {
			setAt = float32(x)
			break
		}
	}

	if v := (&DayNight{TimeOfDay: setAt}).StarVisibility(); v > 0.25 {
		t.Errorf("stars already %.0f%% visible as the sun touches the horizon", v*100)
	}
	// Fully out well after, but not so late that night has no stars.
	if v := (&DayNight{TimeOfDay: setAt + 0.06}).StarVisibility(); v < 0.9 {
		t.Errorf("stars only %.0f%% visible well after sunset", v*100)
	}
}

// TestPrimaryLightHandoverIsQuiet checks the sun-to-moon swap happens while
// both are dim.
//
// There is one directional light, so the two must trade places. Doing it at a
// clock boundary flipped the light direction by 180 degrees in a single frame
// while it was still bright, and every shadow in the scene snapped with it.
func TestPrimaryLightHandoverIsQuiet(t *testing.T) {
	prevDir, prevCol := (&DayNight{TimeOfDay: 0}).PrimaryLight()
	for x := 0.0005; x <= 1.0; x += 0.0005 {
		dir, col := (&DayNight{TimeOfDay: float32(x)}).PrimaryLight()

		// Dot product falling means the light direction moved; a handover
		// shows up as a large step.
		dot := float64(dir[0]*prevDir[0] + dir[1]*prevDir[1] + dir[2]*prevDir[2])
		if dot < 0.9 {
			bright := math.Max(lum(col), lum(prevCol))
			if bright > 0.02 {
				t.Errorf("light direction jumped at t=%.4f while still at luminance %.3f", x, bright)
			}
		}
		prevDir, prevCol = dir, col
	}
}

// TestNightIsActuallyDark guards the other end: a night that is merely dim
// reads as an overcast afternoon.
func TestNightIsActuallyDark(t *testing.T) {
	midnight := &DayNight{TimeOfDay: 0}
	noon := &DayNight{TimeOfDay: 0.5}

	nightAmb := lum(midnight.AmbientColor())
	dayAmb := lum(noon.AmbientColor())
	if ratio := nightAmb / dayAmb; ratio > 0.15 {
		t.Errorf("midnight ambient is %.0f%% of noon; want a real night", ratio*100)
	}
	if lum(midnight.SunColor()) > 0.001 {
		t.Errorf("sun still contributing %.4f at midnight", lum(midnight.SunColor()))
	}
	if midnight.Daylight() > 0.001 {
		t.Errorf("daylight is %.3f at midnight", midnight.Daylight())
	}
}

// TestSunDiscExceedsOne pins the fact that the sun disc is an HDR value, and
// that it stays one for as long as it is on screen.
//
// It is the only thing the engine emits above 1 without a material asking for
// it, and everything downstream of the half-float target depends on that: a
// bloom threshold set above 1 selects the sun and nothing else, and a tonemap
// curve has the disc to compress. Clamp SunDiscColor to 1 -- which looks like
// a tidy-up, because on the old 8-bit path the excess was thrown away at the
// framebuffer anyway -- and the sun silently stops being a highlight.
//
// The horizon sweep is the part that earns its keep. An earlier version of this
// test checked midday only, where the horizon fade is 1.0 and the answer is
// trivially yes. It passed while the sun at sunset was reaching 0.81 -- below
// the bloom threshold, and darker than the sky behind it, so the one moment the
// sun should obviously glare was the one moment it could not. The bug was found
// by looking at a sunset, not by running the tests.
//
// It has teeth: dropping the boost to 1.0 fails the peak check, clamping the
// return of SunDiscColor to 1 fails it, and moving the fade window back over
// the horizon to -0.17..0.05 fails the sweep at every elevation below about
// +0.03.
func TestSunDiscExceedsOne(t *testing.T) {
	maxChan := func(c [3]float32) float32 {
		m := c[0]
		for _, v := range c[1:] {
			if v > m {
				m = v
			}
		}
		return m
	}

	// Midday, when the disc is brightest and fully faded in.
	if peak := maxChan((&DayNight{TimeOfDay: 0.5}).SunDiscColor()); peak <= 1.0 {
		t.Errorf("midday sun disc peaks at %.3f, want above 1 -- the HDR target has nothing to carry", peak)
	}

	// Every elevation at which the disc is still on screen has to clear the
	// bloom ramp docs/agents/bloom.md documents: threshold 1.2 with a 0.2 knee,
	// so the ramp runs 1.0 to 1.4. The threshold compares the max channel, not
	// luminance, which matters here because a sunset disc is almost pure red and
	// its luminance is far below its red channel.
	//
	// -0.05 rather than 0 because the disc has width: the sun is still visibly
	// sitting on the horizon when its centre has dropped just below it.
	const (
		visibleElevation = -0.05
		bloomRampTop     = 1.4
	)
	for step := 0; step <= 1000; step++ {
		tod := float32(step) / 1000
		dn := &DayNight{TimeOfDay: tod}
		if dn.SunDir()[1] < visibleElevation {
			continue
		}
		if peak := maxChan(dn.SunDiscColor()); peak < bloomRampTop {
			t.Errorf("t=%.3f: sun at elevation %+.3f is visible but its disc peaks at %.3f, "+
				"below the bloom ramp top of %.1f -- it will not glare",
				tod, dn.SunDir()[1], peak, bloomRampTop)
			break
		}
	}

	// Night is the control: with the sun well below the horizon the disc must
	// fade out entirely, or a bloom threshold picks up a sun that is not there.
	if l := lum((&DayNight{TimeOfDay: 0.0}).SunDiscColor()); l > 0.01 {
		t.Errorf("midnight sun disc luminance = %.4f, want ~0", l)
	}
	if l := lum((&DayNight{TimeOfDay: 0.80}).SunDiscColor()); l > 0.01 {
		t.Errorf("well-past-sunset disc luminance = %.4f, want ~0", l)
	}
}
