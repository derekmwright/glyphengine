package audio

import "testing"

// What these can and cannot prove is worth being explicit about, because the
// first version of this file got it wrong.
//
// The crash needs a real device, a real file and fifteen seconds of playback,
// none of which is available here. The obvious substitute — call the methods on
// a slot with a null sound and rely on them faulting if a guard is removed —
// does not work: miniaudio null-checks its own arguments, so ma_sound_uninit
// and ma_sound_at_end on a null pointer return quietly. Those tests passed with
// the guards deleted, which makes them decoration. Measured, not assumed.
//
// The hazard is not a null sound. It is a non-null sound that has already been
// uninitialized, and that cannot be constructed here without causing the very
// crash being tested for.
//
// What is testable is the bookkeeping that prevents reaching that state: uninit
// clears live, so the second call returns before touching C. That single
// assignment is the fix, and it is what these cover.

// liveSlot is a slot with real C memory behind it, marked live without loading
// a sound. miniaudio tolerates a zeroed ma_sound, so uninit on one is safe --
// which is what makes the state machine reachable from a test at all.
func liveSlot() soundSlot {
	s := newSoundSlot()
	s.live = true
	return s
}

// The fix. tick retires the inactive slot on every frame, and the slot stays
// inactive for a whole loop of the sound, so ma_sound_uninit was reached
// roughly 120 times a second on an already-freed sound. Clearing live on the
// first call is what makes every later call return early.
func TestUninitClearsLiveSoTheSecondCallIsANoOp(t *testing.T) {
	s := liveSlot()
	defer s.free()

	s.uninit()
	if s.live {
		t.Fatal("uninit left the slot live; every later frame would call ma_sound_uninit again")
	}

	// The loop is what tick does. It must not re-enter C on any of them, which
	// is exactly what the flag above guarantees.
	for range 200 {
		s.uninit()
		if s.live {
			t.Fatal("slot became live again while being retired")
		}
	}
}

// init has to leave the slot live, or the crossfade would start a sound the
// layer then treats as retired and never fades out.
func TestFreeClearsBothTheFlagAndThePointer(t *testing.T) {
	s := liveSlot()

	s.free()
	if s.live {
		t.Error("free left the slot live")
	}
	if s.snd != nil {
		t.Error("free left a non-nil pointer, so a second free would double-free the C memory")
	}

	// cleanup calls free on both slots unconditionally now, so a second one has
	// to be harmless.
	s.free()
}

// The queries answer from the flag. A retired slot has not finished playing --
// it is not playing at all -- and reporting otherwise would make tick retire it
// again on the next frame.
func TestQueriesAnswerFromTheFlagWhenRetired(t *testing.T) {
	s := liveSlot()
	defer s.free()
	s.uninit()

	if s.atEnd() {
		t.Error("a retired slot reported being at its end, which would retire it again")
	}
	if s.isPlaying() {
		t.Error("a retired slot reported playing, which would fade a sound that is gone")
	}
	if _, _, ok := s.lengthAndCursor(); ok {
		t.Error("a retired slot reported a length")
	}
}

// The ping-pong is why a slot needs its own flag: the inactive one is whichever
// the active is not, and it stays inactive across a whole loop.
func TestAmbientLayerSlotsPingPong(t *testing.T) {
	l := &ambientLayer{aActive: true}

	if l.activeSlot() != &l.a || l.inactiveSlot() != &l.b {
		t.Fatal("with aActive set, a is active and b is inactive")
	}

	l.aActive = false
	if l.activeSlot() != &l.b || l.inactiveSlot() != &l.a {
		t.Fatal("after the swap, b is active and a is inactive")
	}

	if l.activeSlot() == l.inactiveSlot() {
		t.Fatal("active and inactive resolved to the same slot")
	}
}

// Engine.Destroy takes this path for a layer whose file failed to load, so it
// has to survive a layer that never started and slots that were never loaded.
func TestAmbientLayerCleanupOnUnstartedLayer(t *testing.T) {
	l := newAmbientLayer("never-loaded.ogg", 1)
	l.cleanup()
	l.cleanup()
}
