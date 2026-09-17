package audio

/*
#include "miniaudio.h"
#include <stdlib.h>
*/
import "C"
import "unsafe"

// soundSlot is one miniaudio sound plus whether it is currently initialized.
//
// The two have to travel together. miniaudio's ma_sound_uninit detaches the
// node from the engine's graph and frees the heap behind it, and calling it
// twice trips its own double-free assert and aborts — on the real-time audio
// thread, from a stack that names the audio callback rather than the code that
// caused it. Every read is equally unsafe: ma_sound_at_end on an uninitialized
// or already-freed slot is reading whatever the allocator has since done with
// that memory.
//
// So this is not a pair of parallel fields on the layer, where a bool can drift
// from the pointer it describes. Every entry point guards on live, and uninit
// clears it, which makes calling uninit twice a no-op instead of a crash — the
// property is enforced here rather than at each call site remembering to check.
type soundSlot struct {
	snd  *C.ma_sound
	live bool
}

// newSoundSlot allocates the C memory for a slot. The sound itself is not
// initialized until init is called.
func newSoundSlot() soundSlot {
	return soundSlot{snd: (*C.ma_sound)(C.calloc(1, C.size_t(unsafe.Sizeof(C.ma_sound{}))))}
}

// init loads a streaming, non-spatialized sound into the slot.
//
// An already-live slot is uninitialized first, so re-initializing over the top
// cannot leak the previous sound's node and heap.
func (s *soundSlot) init(eng *C.ma_engine, path string) bool {
	if s.snd == nil {
		return false
	}
	s.uninit()

	cpath := C.CString(path)
	defer C.free(unsafe.Pointer(cpath))

	flags := C.MA_SOUND_FLAG_STREAM | C.MA_SOUND_FLAG_NO_SPATIALIZATION
	if C.ma_sound_init_from_file(eng, cpath, C.ma_uint32(flags), nil, nil, s.snd) != C.MA_SUCCESS {
		return false
	}
	s.live = true
	return true
}

// uninit releases the sound if it is live, and is a no-op otherwise.
//
// This idempotence is the fix for the crash: the caller no longer has to track
// whether a slot has already been retired, because asking twice is harmless.
func (s *soundSlot) uninit() {
	if !s.live {
		return
	}
	C.ma_sound_uninit(s.snd)
	s.live = false
}

// free releases the slot's C memory. The sound is uninitialized first if it is
// still live, because freeing the memory under a sound miniaudio still has in
// its node graph is the same crash by another route.
func (s *soundSlot) free() {
	s.uninit()
	if s.snd != nil {
		C.free(unsafe.Pointer(s.snd))
		s.snd = nil
	}
}

// atEnd reports whether the sound has played to its end. A slot that is not
// live has not, and is not read.
func (s *soundSlot) atEnd() bool {
	if !s.live {
		return false
	}
	return C.ma_sound_at_end(s.snd) == C.MA_TRUE
}

// isPlaying reports whether the sound is currently playing. A slot that is not
// live is not, and is not read.
func (s *soundSlot) isPlaying() bool {
	if !s.live {
		return false
	}
	return C.ma_sound_is_playing(s.snd) == C.MA_TRUE
}

// fadeOutAndStop ramps the slot to silence over ms and schedules its stop. A
// slot that is not live has nothing to fade.
func (s *soundSlot) fadeOutAndStop(ms int) {
	if !s.live {
		return
	}
	C.ma_sound_set_fade_in_milliseconds(s.snd, -1, 0, C.ma_uint64(ms))
	C.ma_sound_set_stop_time_in_milliseconds(s.snd, C.ma_uint64(ms))
}

// startFadedIn begins playback from silence, ramping to volume over ms.
func (s *soundSlot) startFadedIn(volume float32, ms int) {
	if !s.live {
		return
	}
	C.ma_sound_set_volume(s.snd, 0)
	C.ma_sound_set_fade_in_milliseconds(s.snd, 0, C.float(volume), C.ma_uint64(ms))
	C.ma_sound_start(s.snd)
}

// lengthAndCursor returns the sound's total length and playback position in
// seconds. ok is false for a slot that is not live, which is not the same as a
// zero-length sound.
func (s *soundSlot) lengthAndCursor() (length, cursor float32, ok bool) {
	if !s.live {
		return 0, 0, false
	}
	var l, c C.float
	C.ma_sound_get_length_in_seconds(s.snd, &l)
	C.ma_sound_get_cursor_in_seconds(s.snd, &c)
	return float32(l), float32(c), true
}
