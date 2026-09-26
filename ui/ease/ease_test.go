package ease

import (
	"math"
	"testing"
)

// tol is what float32 arithmetic on these polynomials costs against a value
// worked out by hand in exact decimal. The largest curve here evaluates three
// multiplies and two adds, so anything beyond a few ULPs of 1 is a wrong
// formula rather than rounding.
const tol = 1e-6

// The table is computed by hand from the standard formulas, not read off the
// implementation. That is the whole point of it: a table generated from the
// code under test agrees with any bug the code has.
//
//	linear        t
//	in_quad       t^2
//	out_quad      1 - (1-t)^2
//	in_out_quad   2t^2                    | 1 - (2-2t)^2 / 2
//	in_cubic      t^3
//	out_cubic     1 - (1-t)^3
//	in_out_cubic  4t^3                    | 1 - (2-2t)^3 / 2
//	out_back      1 + c3(t-1)^3 + c1(t-1)^2,  c1 = 1.70158, c3 = c1+1
//
// out_back at 0.25 is 1 - 2.70158(0.421875) + 1.70158(0.5625), which is
// 1 - 1.13972906 + 0.95713875 = 0.81740969; at 0.50 it is
// 1 - 0.33769750 + 0.42539500 = 1.08769750; at 0.75 it is
// 1 - 0.04221219 + 0.10634875 = 1.06413656.
func TestCurvesMatchTheStandardFormulas(t *testing.T) {
	cases := []struct {
		name string
		want [5]float32 // at t = 0, 0.25, 0.5, 0.75, 1
	}{
		{"linear", [5]float32{0, 0.25, 0.5, 0.75, 1}},
		{"in_quad", [5]float32{0, 0.0625, 0.25, 0.5625, 1}},
		{"out_quad", [5]float32{0, 0.4375, 0.75, 0.9375, 1}},
		{"in_out_quad", [5]float32{0, 0.125, 0.5, 0.875, 1}},
		{"in_cubic", [5]float32{0, 0.015625, 0.125, 0.421875, 1}},
		{"out_cubic", [5]float32{0, 0.578125, 0.875, 0.984375, 1}},
		{"in_out_cubic", [5]float32{0, 0.0625, 0.5, 0.9375, 1}},
		{"out_back", [5]float32{0, 0.81740969, 1.08769750, 1.06413656, 1}},
	}

	ts := [5]float32{0, 0.25, 0.5, 0.75, 1}
	for _, tc := range cases {
		f, ok := ByName(tc.name)
		if !ok {
			t.Errorf("ByName(%q) found nothing", tc.name)
			continue
		}
		for i, at := range ts {
			got := f(at)
			if math.Abs(float64(got-tc.want[i])) > tol {
				t.Errorf("%s(%v) = %v, want %v", tc.name, at, got, tc.want[i])
			}
		}
	}
}

// Every curve starts at exactly 0 and ends at exactly 1. Not "within tol":
// these two are what a transition's rest and hidden states are built on, and a
// curve that ends at 0.9999998 leaves a widget permanently a hair off its
// resolved rect, which is a one-pixel seam nobody can find.
func TestEndpointsAreExact(t *testing.T) {
	for _, name := range Names() {
		f, _ := ByName(name)
		if got := f(0); got != 0 {
			t.Errorf("%s(0) = %v, want exactly 0", name, got)
		}
		if got := f(1); got != 1 {
			t.Errorf("%s(1) = %v, want exactly 1", name, got)
		}
	}
}

// OutBack is the one curve that leaves the unit interval, and a caller that
// clamps its output has to know it will. It must go above 1 somewhere in the
// middle and be back on 1 at the end -- a "back" curve that never overshoots is
// just a slow out_cubic, and would pass every endpoint check above.
func TestOutBackOvershootsAndSettles(t *testing.T) {
	var peak float32
	var peakAt float32
	for i := 0; i <= 1000; i++ {
		at := float32(i) / 1000
		if v := OutBack(at); v > peak {
			peak, peakAt = v, at
		}
	}
	if peak <= 1 {
		t.Fatalf("OutBack never exceeded 1 (peak %v at %v); it is not overshooting", peak, peakAt)
	}
	// Penner's constant produces about 10% over, and a value far off that is a
	// different constant rather than a different curve shape.
	if peak < 1.05 || peak > 1.15 {
		t.Errorf("OutBack peaks at %v (t = %v); the standard constant peaks near 1.10", peak, peakAt)
	}
	if peakAt < 0.5 || peakAt > 0.8 {
		t.Errorf("OutBack peaks at t = %v; the standard curve peaks around 0.6", peakAt)
	}
	if OutBack(1) != 1 {
		t.Errorf("OutBack(1) = %v; it must settle back onto its target", OutBack(1))
	}
}

// Everything but out_back stays inside 0..1 across the whole interval. This is
// what lets a transition hand a quad's opacity straight to a render object for
// seven of the eight curves without a clamp in the middle of the draw path.
func TestOnlyOutBackLeavesTheUnitInterval(t *testing.T) {
	for _, name := range Names() {
		if name == "out_back" {
			continue
		}
		f, _ := ByName(name)
		for i := 0; i <= 1000; i++ {
			at := float32(i) / 1000
			if v := f(at); v < 0 || v > 1 {
				t.Errorf("%s(%v) = %v, outside 0..1", name, at, v)
				break
			}
		}
	}
}

// Input outside 0..1 is clamped rather than extrapolated. A transition's
// progress is clamped before it gets here, but an accumulating dt can land a
// hair past 1 on the frame it finishes, and in_cubic extrapolated at 1.0001 is
// a widget momentarily larger than the layout said.
func TestInputIsClamped(t *testing.T) {
	for _, name := range Names() {
		f, _ := ByName(name)
		if got := f(-0.5); got != f(0) {
			t.Errorf("%s(-0.5) = %v, want f(0) = %v", name, got, f(0))
		}
		if got := f(1.5); got != f(1) {
			t.Errorf("%s(1.5) = %v, want f(1) = %v", name, got, f(1))
		}
	}
}

func TestByNameRejectsUnknown(t *testing.T) {
	if f, ok := ByName("out_bounce"); ok || f != nil {
		t.Errorf("ByName(%q) returned (%v, %v), want (nil, false)", "out_bounce", f, ok)
	}
	if f, ok := ByName(""); ok || f != nil {
		t.Errorf("ByName(%q) returned (%v, %v), want (nil, false)", "", f, ok)
	}
}

// Names is what an error message lists, so it has to be the whole set rather
// than a hand-maintained subset that drifts from the lookup.
func TestNamesCoversEveryCurve(t *testing.T) {
	listed := make(map[string]bool, len(Names()))
	for _, n := range Names() {
		if listed[n] {
			t.Errorf("Names() lists %q twice", n)
		}
		listed[n] = true
		if _, ok := ByName(n); !ok {
			t.Errorf("Names() lists %q, which ByName does not know", n)
		}
	}
	for n := range names {
		if !listed[n] {
			t.Errorf("ByName knows %q, which Names() does not list", n)
		}
	}
}
