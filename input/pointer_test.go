package input

import "testing"

// The bug this covers is invisible on the hardware the engine is developed on.
// Windows and Linux normally report the window and the framebuffer as the same
// size, so a wrong conversion is the identity there and a test that needed a
// Retina panel to fail would never run. These drive the arithmetic directly
// instead, with the scale factors supplied.

func TestScaleToFramebuffer(t *testing.T) {
	cases := []struct {
		name                 string
		x, y                 float64
		winW, winH, fbW, fbH int
		wantX, wantY         float64
	}{
		{
			// The common case, and the reason this went unnoticed.
			name: "1x leaves the cursor alone",
			x:    640, y: 360,
			winW: 1280, winH: 720, fbW: 1280, fbH: 720,
			wantX: 640, wantY: 360,
		},
		{
			// A Retina Mac. Before the fix the ray was cast from the unscaled
			// value, which is this input — half as far from the origin as the
			// pointer actually is.
			name: "2x Retina doubles into framebuffer pixels",
			x:    1200, y: 800,
			winW: 1440, winH: 900, fbW: 2880, fbH: 1800,
			wantX: 2400, wantY: 1600,
		},
		{
			name: "fractional scale",
			x:    100, y: 50,
			winW: 1000, winH: 500, fbW: 1500, fbH: 750,
			wantX: 150, wantY: 75,
		},
		{
			// Some setups scale the axes differently; the conversion is per
			// axis rather than one factor for both.
			name: "non-uniform scale",
			x:    100, y: 100,
			winW: 1000, winH: 1000, fbW: 2000, fbH: 1000,
			wantX: 200, wantY: 100,
		},
		{
			// GLFW reports zero for a minimized window. Scaling by zero would
			// park the cursor at the origin and dividing would be worse.
			name: "minimized window is passed through",
			x:    640, y: 360,
			winW: 0, winH: 0, fbW: 0, fbH: 0,
			wantX: 640, wantY: 360,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotX, gotY := scaleToFramebuffer(tc.x, tc.y, tc.winW, tc.winH, tc.fbW, tc.fbH)
			if gotX != tc.wantX || gotY != tc.wantY {
				t.Errorf("scaleToFramebuffer(%v, %v, %d, %d, %d, %d) = (%v, %v), want (%v, %v)",
					tc.x, tc.y, tc.winW, tc.winH, tc.fbW, tc.fbH, gotX, gotY, tc.wantX, tc.wantY)
			}
		})
	}
}

// The corner the ray is cast from has to survive the conversion, or the fix
// trades an offset in the middle of the screen for one at its edges.
func TestScaleToFramebufferKeepsTheCorners(t *testing.T) {
	const winW, winH, fbW, fbH = 1440, 900, 2880, 1800

	if x, y := scaleToFramebuffer(0, 0, winW, winH, fbW, fbH); x != 0 || y != 0 {
		t.Errorf("top-left moved to (%v, %v), want (0, 0)", x, y)
	}
	x, y := scaleToFramebuffer(winW, winH, winW, winH, fbW, fbH)
	if x != fbW || y != fbH {
		t.Errorf("bottom-right became (%v, %v), want (%d, %d)", x, y, fbW, fbH)
	}
}
