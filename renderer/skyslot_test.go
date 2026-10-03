package renderer

import (
	"reflect"
	"testing"
)

// The sky slot is the one group of ShaderSet stages with no embedded fallback:
// the engine ships no dome, no star field and no cloud march, so nil means
// absent. These two tests are its engine-side contract. x/sky's
// TestShadersFillTheSkySlotAndNothingElse is the other end of the same seam.

// TestSkySlotHasNoEmbeddedFallback is the property the whole arrangement rests
// on. If DefaultShaders ever grows a dome again, or withDefaults starts filling
// one of the three, then nil stops meaning absent and every scene gets a sky
// back whether it asked or not -- which is what this change removed.
//
// It also pins the other direction, because a slot that swallowed its
// neighbours would be the same bug wearing a different hat: every stage NOT in
// the slot must still be filled from the engine's embedded shader.
//
// Verified to fail both ways. Putting any non-nil blob in DefaultShaders'
// SkyFrag reports `DefaultShaders() embeds SkyFrag (1392 bytes); the sky slot
// must be empty` -- the blob used was SkyVertSpv, since there is no dome left in
// the engine to put there, which is itself the point. Doing the same for
// CloudsFrag AND restoring {&s.CloudsFrag, d.CloudsFrag} to withDefaults' list
// adds `withDefaults filled CloudsFrag`; the second half cannot be broken on its
// own, because a list entry that copies a nil default fills nothing.
func TestSkySlotHasNoEmbeddedFallback(t *testing.T) {
	d := DefaultShaders()
	slot := []struct {
		name string
		spv  []byte
	}{
		{"SkyFrag", d.SkyFrag},
		{"StarsFrag", d.StarsFrag},
		{"CloudsFrag", d.CloudsFrag},
	}
	for _, st := range slot {
		if st.spv != nil {
			t.Errorf("DefaultShaders() embeds %s (%d bytes); the sky slot must be empty", st.name, len(st.spv))
		}
	}

	defaulted := ShaderSet{}.withDefaults()
	if defaulted.SkyFrag != nil {
		t.Error("withDefaults filled SkyFrag")
	}
	if defaulted.StarsFrag != nil {
		t.Error("withDefaults filled StarsFrag")
	}
	if defaulted.CloudsFrag != nil {
		t.Error("withDefaults filled CloudsFrag")
	}

	// Everything else has to be filled, or the fallback promise on ShaderSet is
	// broken for the twenty-odd stages that do have one.
	v := reflect.ValueOf(defaulted)
	filled := 0
	for i := 0; i < v.NumField(); i++ {
		name := v.Type().Field(i).Name
		if name == "SkyFrag" || name == "StarsFrag" || name == "CloudsFrag" {
			continue
		}
		if v.Field(i).Bytes() == nil {
			t.Errorf("withDefaults left %s nil, and it is not in the sky slot", name)
			continue
		}
		filled++
	}
	if filled < 20 {
		t.Fatalf("withDefaults filled only %d stages; this test is not looking at the set it thinks it is", filled)
	}
}

// TestEmptySkySlotClearsTheDrawFlags is the half a capture cannot show cheaply:
// an environment that asks for a dome, against a renderer with no shader to draw
// one, must not leave DrawSky set. A flag that survived would be recorded as a
// pipeline bind on a zero handle, which is a device-lost or a validation error
// rather than a missing dome.
//
// No device is involved. A Renderer value with its two pipeline fields set or
// left zero is the whole of what resolveSkySlot reads, which is why it is a
// method.
//
// Verified to fail: returning l unchanged from resolveSkySlot reports
// `empty slot: DrawSky survived` and `dome only: DrawStars survived`.
func TestEmptySkySlotClearsTheDrawFlags(t *testing.T) {
	var h fakeHandles
	asked := SceneLighting{DrawSky: true, DrawStars: true}

	t.Run("empty slot", func(t *testing.T) {
		var r Renderer
		got := r.resolveSkySlot(asked)
		if got.DrawSky {
			t.Error("empty slot: DrawSky survived")
		}
		if got.DrawStars {
			t.Error("empty slot: DrawStars survived")
		}
	})

	t.Run("dome only", func(t *testing.T) {
		var r Renderer
		r.skyPipeline = h.pipeline()
		got := r.resolveSkySlot(asked)
		if !got.DrawSky {
			t.Error("dome only: DrawSky was cleared with a sky pipeline present")
		}
		if got.DrawStars {
			t.Error("dome only: DrawStars survived")
		}
	})

	t.Run("dome and stars", func(t *testing.T) {
		var r Renderer
		r.skyPipeline = h.pipeline()
		r.starsPipeline = h.pipeline()
		got := r.resolveSkySlot(asked)
		if !got.DrawSky || !got.DrawStars {
			t.Errorf("both pipelines present and the flags were cleared: %+v", got)
		}
	})

	// It must not invent a sky either: a source that asked for nothing gets
	// nothing, with every pipeline in place.
	var r Renderer
	r.skyPipeline = h.pipeline()
	r.starsPipeline = h.pipeline()
	if got := r.resolveSkySlot(SceneLighting{}); got.DrawSky || got.DrawStars {
		t.Errorf("a source that asked for no sky got one: %+v", got)
	}
}
