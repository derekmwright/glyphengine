package renderer

import (
	"testing"

	"github.com/vkngwrapper/core/v3/core1_0"
)

// TestFarPlaneDepthStateSurvivesReverseZ pins the depth rule for the layers that
// draw at the far plane after all opaque geometry — the sky dome and the
// starfield.
//
// The starfield shipped with no depth test at all, which let it paint stars over
// the silhouette of any terrain that reached into the sky. The fix was to share
// one depth state between both, so this asserts what that state has to say.
//
// It has teeth: flipping DepthTestEnable back to false, swapping the compare op
// for CompareOpGreater (which rejects a cleared depth buffer everywhere and makes
// the sky vanish entirely), or enabling depth writes all fail it.
func TestFarPlaneDepthStateSurvivesReverseZ(t *testing.T) {
	ds := farPlaneDepthState()

	if !ds.DepthTestEnable {
		t.Error("depth test disabled: these layers draw after opaque geometry and would cover it")
	}
	if ds.DepthWriteEnable {
		t.Error("depth writes enabled: a backdrop must not overwrite the world's depth for the layers after it")
	}
	// Depth is reversed and cleared to 0, and the fullscreen triangle sits at
	// exactly 0, so Greater rejects it on every pixel.
	if ds.DepthCompareOp != core1_0.CompareOpGreaterOrEqual {
		t.Errorf("compare op = %v, want GreaterOrEqual; anything stricter rejects the far plane against itself",
			ds.DepthCompareOp)
	}
}
