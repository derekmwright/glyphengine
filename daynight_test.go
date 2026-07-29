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

	// And it must not be extinguished as the stars appear. Twilight is a
	// function of sun elevation alone; gating it on nightfall as well is the
	// original bug, and halves it exactly here. The correct model gives ~0.53
	// at the point the stars are half out.
	for x := 0.6; x < 0.95; x += 0.001 {
		dn := &DayNight{TimeOfDay: float32(x)}
		if dn.StarVisibility() > 0.45 && dn.StarVisibility() < 0.55 {
			if dn.Twilight() < 0.45 {
				t.Errorf("at half-star visibility twilight is %.2f; the sunset is being erased by nightfall",
					dn.Twilight())
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
