package renderer

import "testing"

// The interior fill is opt-in, and the way it signals "not set" is the part
// that can break quietly. Zero is a legitimate fill — a fully transparent
// interior leaves just the frame — and so is one, which is what a modal dialog
// needs. So neither can double as the unset marker, and the shader branches on
// a negative opacity instead.
//
// Get that wrong and a panel that asked for nothing renders with a transparent
// interior rather than the derived one, which reads as "the panel background
// disappeared" and points at the wrong file.

func TestPanelFillUnsetSignalsDerive(t *testing.T) {
	var pc [64]float32
	packUIFill(&pc, nil)

	if pc[43] >= 0 {
		t.Errorf("fill opacity is %v, want negative so ui.frag derives the interior", pc[43])
	}
}

func TestPanelFillReachesThePushConstants(t *testing.T) {
	var pc [64]float32
	packUIFill(&pc, &PanelFill{Color: [3]float32{0.1, 0.2, 0.3}, Opacity: 0.85})

	got := [4]float32{pc[40], pc[41], pc[42], pc[43]}
	want := [4]float32{0.1, 0.2, 0.3, 0.85}
	if got != want {
		t.Errorf("fill landed as %v, want %v", got, want)
	}
}

// Both ends of the opacity range have to survive, because both are the reason
// the field exists: the derived interior was capped at 0.7 of the border
// opacity, so a panel could never be made opaque at all.
func TestPanelFillOpacityEndsAreExpressible(t *testing.T) {
	for _, tc := range []struct {
		name    string
		opacity float32
	}{
		{"fully transparent", 0},
		{"fully opaque", 1},
	} {
		var pc [64]float32
		packUIFill(&pc, &PanelFill{Color: [3]float32{0, 0, 0}, Opacity: tc.opacity})

		if pc[43] != tc.opacity {
			t.Errorf("%s: opacity landed as %v, want %v", tc.name, pc[43], tc.opacity)
		}
		if pc[43] < 0 {
			t.Errorf("%s: opacity is negative, which ui.frag reads as unset", tc.name)
		}
	}
}

// The fill occupies the vec4 after params, and params carries the texture-mode
// flag at 36. Overlapping them would make a textured icon draw with a panel
// interior, or a panel take the straight-texture branch.
func TestPanelFillDoesNotOverlapTextureMode(t *testing.T) {
	var pc [64]float32
	pc[36] = 1 // straight texture mode
	packUIFill(&pc, &PanelFill{Color: [3]float32{1, 1, 1}, Opacity: 1})

	if pc[36] != 1 {
		t.Errorf("packUIFill clobbered the texture-mode flag at 36: %v", pc[36])
	}
	for i := 37; i <= 39; i++ {
		if pc[i] != 0 {
			t.Errorf("packUIFill wrote into params at %d: %v", i, pc[i])
		}
	}
}
