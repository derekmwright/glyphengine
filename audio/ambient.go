package audio

/*
#include "miniaudio.h"
#include <stdlib.h>
*/
import "C"
import "unsafe"

// ambientFadeMs is the crossfade duration when an ambient layer loops.
const ambientFadeMs = 3000

// ambientLayer manages a single looping ambient sound with seamless tail-to-head
// crossfade. Two sound slots (a/b) ping-pong: when the active slot nears its end,
// the other starts from the beginning with a fade-in while the active fades out.
type ambientLayer struct {
	path   string
	volume float32

	a, b      *C.ma_sound
	aActive   bool // true = a is the current slot, false = b
	started   bool // true after first start
	stopping  bool // fade-out in progress, will be removed after fade
	stopTimer int  // frames remaining before safe to clean up
}

func newAmbientLayer(path string, volume float32) *ambientLayer {
	return &ambientLayer{
		path:    path,
		volume:  volume,
		a:       (*C.ma_sound)(C.calloc(1, C.size_t(unsafe.Sizeof(C.ma_sound{})))),
		b:       (*C.ma_sound)(C.calloc(1, C.size_t(unsafe.Sizeof(C.ma_sound{})))),
		aActive: true,
	}
}

// start initialises and begins playback on the active slot with a fade-in.
func (l *ambientLayer) start(eng *C.ma_engine) bool {
	slot := l.activeSlot()
	cpath := C.CString(l.path)
	defer C.free(unsafe.Pointer(cpath))

	flags := C.MA_SOUND_FLAG_STREAM | C.MA_SOUND_FLAG_NO_SPATIALIZATION
	if C.ma_sound_init_from_file(eng, cpath, C.ma_uint32(flags), nil, nil, slot) != C.MA_SUCCESS {
		return false
	}
	C.ma_sound_set_volume(slot, 0)
	C.ma_sound_set_fade_in_milliseconds(slot, 0, C.float(l.volume), C.ma_uint64(ambientFadeMs))
	C.ma_sound_start(slot)
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

	// Get total length and cursor in seconds.
	var length, cursor C.float
	C.ma_sound_get_length_in_seconds(active, &length)
	C.ma_sound_get_cursor_in_seconds(active, &cursor)

	if length <= 0 {
		return true
	}

	fadeSeconds := C.float(ambientFadeMs) / 1000.0
	remaining := length - cursor

	// When we're within the fade duration of the end, start the crossfade.
	if remaining <= fadeSeconds && remaining > 0 {
		next := l.inactiveSlot()

		cpath := C.CString(l.path)
		defer C.free(unsafe.Pointer(cpath))

		flags := C.MA_SOUND_FLAG_STREAM | C.MA_SOUND_FLAG_NO_SPATIALIZATION
		if C.ma_sound_init_from_file(eng, cpath, C.ma_uint32(flags), nil, nil, next) != C.MA_SUCCESS {
			return true
		}

		// Fade in the new slot.
		C.ma_sound_set_volume(next, 0)
		C.ma_sound_set_fade_in_milliseconds(next, 0, C.float(l.volume), C.ma_uint64(ambientFadeMs))
		C.ma_sound_start(next)

		// Fade out the old slot and schedule stop.
		C.ma_sound_set_fade_in_milliseconds(active, -1, 0, C.ma_uint64(ambientFadeMs))
		C.ma_sound_set_stop_time_in_milliseconds(active, C.ma_uint64(ambientFadeMs))

		// Swap active slot.
		l.aActive = !l.aActive
	}

	// Clean up the inactive slot if it has finished.
	inactive := l.inactiveSlot()
	if C.ma_sound_at_end(inactive) == C.MA_TRUE {
		C.ma_sound_uninit(inactive)
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

	active := l.activeSlot()
	C.ma_sound_set_fade_in_milliseconds(active, -1, 0, C.ma_uint64(ambientFadeMs))
	C.ma_sound_set_stop_time_in_milliseconds(active, C.ma_uint64(ambientFadeMs))

	// Also fade the inactive slot if it's still playing (mid-crossfade stop).
	inactive := l.inactiveSlot()
	if C.ma_sound_is_playing(inactive) == C.MA_TRUE {
		C.ma_sound_set_fade_in_milliseconds(inactive, -1, 0, C.ma_uint64(ambientFadeMs))
		C.ma_sound_set_stop_time_in_milliseconds(inactive, C.ma_uint64(ambientFadeMs))
	}
}

// cleanup uninitialises both slots and frees C memory.
func (l *ambientLayer) cleanup() {
	if l.started {
		C.ma_sound_uninit(l.a)
		C.ma_sound_uninit(l.b)
	}
	C.free(unsafe.Pointer(l.a))
	C.free(unsafe.Pointer(l.b))
	l.a = nil
	l.b = nil
}

func (l *ambientLayer) activeSlot() *C.ma_sound {
	if l.aActive {
		return l.a
	}
	return l.b
}

func (l *ambientLayer) inactiveSlot() *C.ma_sound {
	if l.aActive {
		return l.b
	}
	return l.a
}
