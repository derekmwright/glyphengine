package audio

/*
#include "miniaudio.h"
#include <stdlib.h>
*/
import "C"

// ambientFadeMs is the crossfade duration when an ambient layer loops.
const ambientFadeMs = 3000

// ambientLayer manages a single looping ambient sound with seamless tail-to-head
// crossfade. Two sound slots (a/b) ping-pong: when the active slot nears its end,
// the other starts from the beginning with a fade-in while the active fades out.
//
// Each slot tracks whether it is initialized, because the ping-pong means a slot
// spends most of its life retired. tick used to read and uninitialize the
// inactive slot on every frame regardless: before the first crossfade that was a
// read of memory no sound had ever been loaded into, and after one it was
// ma_sound_uninit on an already-freed sound at frame rate, roughly 120 times a
// second, until the next crossfade came round. miniaudio detects that as a
// double free and aborts from the real-time audio thread. See soundSlot.
type ambientLayer struct {
	path   string
	volume float32

	a, b      soundSlot
	aActive   bool // true = a is the current slot, false = b
	started   bool // true after first start
	stopping  bool // fade-out in progress, will be removed after fade
	stopTimer int  // frames remaining before safe to clean up
}

func newAmbientLayer(path string, volume float32) *ambientLayer {
	return &ambientLayer{
		path:    path,
		volume:  volume,
		a:       newSoundSlot(),
		b:       newSoundSlot(),
		aActive: true,
	}
}

// start initialises and begins playback on the active slot with a fade-in.
func (l *ambientLayer) start(eng *C.ma_engine) bool {
	slot := l.activeSlot()
	if !slot.init(eng, l.path) {
		return false
	}
	slot.startFadedIn(l.volume, ambientFadeMs)
	l.started = true
	return true
}

// tick checks whether the active slot is near its end and triggers the crossfade.
// Returns false if the layer should be removed (stop fade completed).
func (l *ambientLayer) tick(eng *C.ma_engine) bool {
	if l.stopping {
		l.stopTimer--
		if l.stopTimer <= 0 {
			l.cleanup()
			return false
		}
		return true
	}
	if !l.started {
		return true
	}

	active := l.activeSlot()
	length, cursor, ok := active.lengthAndCursor()
	if !ok || length <= 0 {
		return true
	}

	fadeSeconds := float32(ambientFadeMs) / 1000.0
	remaining := length - cursor

	// When we're within the fade duration of the end, start the crossfade.
	if remaining <= fadeSeconds && remaining > 0 {
		next := l.inactiveSlot()

		// init retires whatever the slot held, so a crossfade cannot leak the
		// sound from two loops ago.
		if !next.init(eng, l.path) {
			return true
		}
		next.startFadedIn(l.volume, ambientFadeMs)

		// Fade out the old slot and schedule stop.
		active.fadeOutAndStop(ambientFadeMs)

		// Swap active slot.
		l.aActive = !l.aActive
	}

	// Retire the inactive slot once it has finished. This runs every frame and
	// the slot stays inactive for the whole of the next loop, so it is only
	// safe because uninit is a no-op on a slot that is already retired.
	if inactive := l.inactiveSlot(); inactive.atEnd() {
		inactive.uninit()
	}

	return true
}

// stop begins a fade-out. The layer will be removed after the fade completes.
func (l *ambientLayer) stop() {
	if !l.started || l.stopping {
		return
	}
	l.stopping = true
	l.stopTimer = int(ambientFadeMs/16) + 1

	l.activeSlot().fadeOutAndStop(ambientFadeMs)

	// Also fade the inactive slot if it's still playing (mid-crossfade stop).
	if inactive := l.inactiveSlot(); inactive.isPlaying() {
		inactive.fadeOutAndStop(ambientFadeMs)
	}
}

// cleanup uninitialises both slots and frees C memory.
//
// Unconditional now: free retires a live slot and leaves a retired one alone,
// so it no longer matters whether the layer ever started, whether slot b was
// ever loaded, or whether tick already retired one of them.
func (l *ambientLayer) cleanup() {
	l.a.free()
	l.b.free()
}

func (l *ambientLayer) activeSlot() *soundSlot {
	if l.aActive {
		return &l.a
	}
	return &l.b
}

func (l *ambientLayer) inactiveSlot() *soundSlot {
	if l.aActive {
		return &l.b
	}
	return &l.a
}
