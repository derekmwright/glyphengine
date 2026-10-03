package main

import "testing"

// TestArmsAreWhatTheyAreCalled is the arm labels, checked without a GPU.
//
// The run itself checks the same two numbers and exits non-zero on them, which is
// what makes `task smoke` and `task validate` fail on a drifted camera. This is
// here because the thing it checks is pure geometry -- a ridge, a camera and six
// thousand placements -- so it does not need a device, and a bench scene whose
// arms have quietly become two controls is worth catching in `task ci` rather
// than in the gate that was going to be measured on it.
//
// Verified by breaking it, 2026-10-02: giving the open arm the ridge terrain
// reports "hidden share 0.500, want between 0.00 and 0.02". Both of the control
// cameras this example tried before the crestless ground were caught here, at
// 0.499 looking along the ridge from one end and 0.498 from an eye lifted 34 m
// over the crest -- against 0.500 for the arm they were the control for.
func TestArmsAreWhatTheyAreCalled(t *testing.T) {
	for _, tc := range []struct {
		arm      string
		min, max float64
	}{
		{arm: "ridge", min: 0.25, max: 1},
		{arm: "open", min: 0, max: 0.02},
	} {
		t.Run(tc.arm, func(t *testing.T) {
			g := &game{arm: tc.arm, trees: defaultTrees}
			if err := g.build(); err != nil {
				t.Fatal(err)
			}
			if len(g.placements) != defaultTrees {
				t.Fatalf("%d placements, want %d", len(g.placements), defaultTrees)
			}
			eye, center, _ := g.view(0)
			share := g.hiddenShare(eye)
			t.Logf("%s: eye %v centre %v, hidden share %.3f", tc.arm, eye, center, share)
			if share < tc.min || share > tc.max {
				t.Errorf("hidden share %.3f, want between %.2f and %.2f", share, tc.min, tc.max)
			}
		})
	}
}

// TestPanKeepsTheRidgeArmHidden holds the moving arm, which is the one the
// determinism and validation matrices run: a sweep that wandered off the flank
// would make those runs measure a different scene from the bench's.
func TestPanKeepsTheRidgeArmHidden(t *testing.T) {
	g := &game{arm: "ridge", trees: defaultTrees, pan: true}
	if err := g.build(); err != nil {
		t.Fatal(err)
	}
	for _, at := range []float32{0, 1.5, 3, 4.5, 6} {
		eye, _, _ := g.view(at)
		if share := g.hiddenShare(eye); share < 0.25 {
			t.Errorf("t=%.1f: hidden share %.3f, want at least 0.25", at, share)
		}
	}
}
