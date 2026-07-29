package renderer

import "testing"

// TestPackLightingPCSendsTheRealSunDirection guards the two push-constant slots
// the atmosphere rebuilds the sun's direction from.
//
// The block is full at 256 bytes, so the sun's direction is split: its elevation
// rides in sunColor.w (pc[43]) and its horizontal pair in fog.zw (pc[62..63]).
// atmSunDirFrom reassembles them. Dropping either write is silent — the shader
// reads zero, the glow snaps to the zenith, and nothing anywhere errors.
//
// It has teeth: deleting the pc[62]/pc[63] assignments in packLightingPC fails
// the horizontal cases, and packing SunDir instead of RealSunDir fails the
// separation case, which is the actual bug this guards (a warm sunset halo
// centred on the midnight moon).
func TestPackLightingPCSendsTheRealSunDirection(t *testing.T) {
	// A sun low in the west while the moon — the primary light — is high in the
	// east: the configuration where the two directions disagree most.
	lighting := SceneLighting{
		SunDir:       [3]float32{-0.3, 0.9, -0.31}, // the moon, lighting the scene
		RealSunDir:   [3]float32{0.6, -0.5, 0.62},  // the sun, below the horizon
		SunElevation: -0.5,
	}

	var pc [64]float32
	packLightingPC(&pc, lighting)

	if got, want := pc[43], lighting.SunElevation; got != want {
		t.Errorf("sunColor.w = %g, want the real sun's elevation %g", got, want)
	}
	if got, want := pc[62], lighting.RealSunDir[0]; got != want {
		t.Errorf("fog.z = %g, want the real sun's x %g", got, want)
	}
	if got, want := pc[63], lighting.RealSunDir[2]; got != want {
		t.Errorf("fog.w = %g, want the real sun's z %g", got, want)
	}

	// The reassembled vector must be the sun, not the body lighting the scene.
	rebuilt := [3]float32{pc[62], pc[43], pc[63]}
	if rebuilt != lighting.RealSunDir {
		t.Errorf("shader would rebuild %v, want the real sun %v", rebuilt, lighting.RealSunDir)
	}
	if rebuilt == lighting.SunDir {
		t.Error("rebuilt direction is the lighting body; the glow would follow the moon")
	}

	// sunDir stays the lighting body: that is what evalLighting and the cloud
	// march want, and moving it would darken every night scene.
	if got := [3]float32{pc[36], pc[37], pc[38]}; got != lighting.SunDir {
		t.Errorf("sunDir = %v, want the lighting body %v", got, lighting.SunDir)
	}
}
