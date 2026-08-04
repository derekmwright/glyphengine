package renderer

import "testing"

// TestGrassLODDefaultsMatchTheOldConstants pins the values that used to be
// package constants.
//
// The point of making them configurable was to change nothing by default, and
// this is the only thing that says so. The numbers are quoted literally rather
// than compared against DefaultGrassLOD, which would pass no matter what the
// defaults became.
//
// It has teeth: changing any default fails it, which is correct -- moving the
// engine's grass tuning should be a deliberate act with a reason attached, not
// a side effect of tidying.
func TestGrassLODDefaultsMatchTheOldConstants(t *testing.T) {
	d := DefaultGrassLOD()
	for _, c := range []struct {
		name string
		got  float32
		want float32
	}{
		{"ThinNear", d.ThinNear, 30},
		{"ThinFar", d.ThinFar, 70},
		{"ThinMin", d.ThinMin, 0.35},
		{"MaxDistance", d.MaxDistance, 80},
		{"FadeStart", d.FadeStart, 50},
	} {
		if c.got != c.want {
			t.Errorf("%s = %g, want %g -- this was a constant and moving it changes every scene", c.name, c.got, c.want)
		}
	}
	if d.MaxDistance != GrassMaxDistance {
		t.Errorf("DefaultGrassLOD().MaxDistance = %g but GrassMaxDistance = %g; the exported constant is documented as the default and worlds are sized by it",
			d.MaxDistance, float32(GrassMaxDistance))
	}
}

// TestGrassKeepFractionShape checks the thinning ramp.
//
// It has teeth: dropping the ThinFar clamp lets the fraction fall below ThinMin
// and eventually negative, which clears distant grass entirely; inverting the
// ramp fails the midpoint.
func TestGrassKeepFractionShape(t *testing.T) {
	l := DefaultGrassLOD()

	if f := l.keepFraction(0); f != 1 {
		t.Errorf("at the camera: %g, want full density", f)
	}
	if f := l.keepFraction(l.ThinNear); f != 1 {
		t.Errorf("at ThinNear: %g, want full density", f)
	}
	if f := l.keepFraction(l.ThinFar); f != l.ThinMin {
		t.Errorf("at ThinFar: %g, want the floor %g", f, l.ThinMin)
	}
	if f := l.keepFraction(1e6); f != l.ThinMin {
		t.Errorf("far past ThinFar: %g, want the floor %g -- never below it, or distant ground goes bare", f, l.ThinMin)
	}

	mid := l.keepFraction((l.ThinNear + l.ThinFar) / 2)
	if want := (1 + l.ThinMin) / 2; mid != want {
		t.Errorf("midpoint: %g, want %g (linear ramp)", mid, want)
	}
}

// TestGrassLODSanitised checks that a partly-filled struct still draws grass.
//
// GrassLOD{MaxDistance: 200} is an easy and reasonable thing for a caller to
// write, and without repair it culls at 200 while thinning to a floor of zero
// and fading from zero -- which is no grass at all. A setter nobody checks the
// error of is the wrong place to be strict.
//
// It has teeth: removing any clamp fails its case.
func TestGrassLODSanitised(t *testing.T) {
	t.Run("zero value is the default", func(t *testing.T) {
		if got := (GrassLOD{}).sanitised(); got != DefaultGrassLOD() {
			t.Errorf("zero GrassLOD sanitised to %+v, want the defaults", got)
		}
	})

	t.Run("only MaxDistance set still fades and thins", func(t *testing.T) {
		got := GrassLOD{MaxDistance: 200}.sanitised()
		if got.MaxDistance != 200 {
			t.Errorf("MaxDistance = %g, want the caller's 200", got.MaxDistance)
		}
		if got.FadeStart <= 0 || got.FadeStart >= 200 {
			t.Errorf("FadeStart = %g, want inside (0, 200) so grass does not vanish at a hard ring", got.FadeStart)
		}
		if got.ThinMin <= 0 {
			t.Errorf("ThinMin = %g, want above zero so distant ground is not bare", got.ThinMin)
		}
	})

	t.Run("fade past the cull is repaired", func(t *testing.T) {
		got := GrassLOD{MaxDistance: 100, FadeStart: 150}.sanitised()
		if got.FadeStart >= got.MaxDistance {
			t.Errorf("FadeStart %g not moved inside MaxDistance %g", got.FadeStart, got.MaxDistance)
		}
	})
}
