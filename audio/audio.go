package audio

/*
#cgo CFLAGS: -I${SRCDIR}
#cgo windows LDFLAGS: -lole32
#include "miniaudio.h"
#include <stdlib.h>
*/
import "C"
import (
	"fmt"
	"sync"
	"unsafe"
)

const soundPoolSize = 16

// Engine wraps a miniaudio ma_engine for 3D audio playback.
// C structs are allocated in C memory to satisfy Go 1.26 CGo pointer rules.
type Engine struct {
	engine *C.ma_engine
	mu     sync.Mutex

	// Fixed pool of sound slots for one-shot positional sounds.
	pool      *[soundPoolSize]C.ma_sound
	poolInUse [soundPoolSize]bool

	// Streaming music track (looping, no spatialization).
	music   *C.ma_sound
	musicOn bool
	// musicRetiring holds tracks that are fading out and not yet safe to free.
	//
	// A slice rather than one slot: a second transition inside the fade window
	// used to overwrite the pointer, which both leaked the C memory and left
	// miniaudio holding a sound in its node graph that nothing would ever
	// uninitialize. Three seconds is easily long enough for a player to change
	// area twice.
	musicRetiring []retiringSound
	masterVol     float32

	// Ambient layers keyed by ID (e.g. "crickets", "birds").
	ambients map[string]*ambientLayer
}

// Init creates and initializes a new audio engine.
func Init() (*Engine, error) {
	e := &Engine{
		masterVol: 1.0,
		ambients:  make(map[string]*ambientLayer),
		engine:    (*C.ma_engine)(C.calloc(1, C.size_t(unsafe.Sizeof(C.ma_engine{})))),
		pool:      (*[soundPoolSize]C.ma_sound)(C.calloc(C.size_t(soundPoolSize), C.size_t(unsafe.Sizeof(C.ma_sound{})))),
		music:     (*C.ma_sound)(C.calloc(1, C.size_t(unsafe.Sizeof(C.ma_sound{})))),
	}
	result := C.ma_engine_init(nil, e.engine)
	if result != C.MA_SUCCESS {
		C.free(unsafe.Pointer(e.engine))
		C.free(unsafe.Pointer(e.pool))
		C.free(unsafe.Pointer(e.music))
		return nil, fmt.Errorf("ma_engine_init failed: %d", int(result))
	}
	return e, nil
}

// Destroy shuts down the audio engine and frees all resources.
func (e *Engine) Destroy() {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.musicOn {
		C.ma_sound_uninit(e.music)
		e.musicOn = false
	}
	for _, r := range e.musicRetiring {
		C.ma_sound_uninit(r.snd)
		C.free(unsafe.Pointer(r.snd))
	}
	e.musicRetiring = nil
	for _, layer := range e.ambients {
		layer.cleanup()
	}
	e.ambients = nil
	for i := 0; i < soundPoolSize; i++ {
		if e.poolInUse[i] {
			C.ma_sound_uninit(&e.pool[i])
			e.poolInUse[i] = false
		}
	}
	C.ma_engine_uninit(e.engine)
	C.free(unsafe.Pointer(e.engine))
	C.free(unsafe.Pointer(e.pool))
	C.free(unsafe.Pointer(e.music))
}

// recyclePool frees any finished sound slots.
func (e *Engine) recyclePool() {
	for i := 0; i < soundPoolSize; i++ {
		if e.poolInUse[i] && C.ma_sound_at_end(&e.pool[i]) == C.MA_TRUE {
			C.ma_sound_uninit(&e.pool[i])
			e.poolInUse[i] = false
		}
	}
}

// PlaySound plays a one-shot positional sound at world coordinates (x, y, z).
func (e *Engine) PlaySound(path string, x, y, z float32) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.recyclePool()

	// Find a free slot.
	slot := -1
	for i := 0; i < soundPoolSize; i++ {
		if !e.poolInUse[i] {
			slot = i
			break
		}
	}
	if slot < 0 {
		return // all slots busy, drop the sound
	}

	cpath := C.CString(path)
	defer C.free(unsafe.Pointer(cpath))

	flags := C.MA_SOUND_FLAG_DECODE // decode fully into memory for low-latency playback
	result := C.ma_sound_init_from_file(e.engine, cpath, C.ma_uint32(flags), nil, nil, &e.pool[slot])
	if result != C.MA_SUCCESS {
		return
	}
	e.poolInUse[slot] = true

	C.ma_sound_set_position(&e.pool[slot], C.float(x), C.float(y), C.float(z))
	C.ma_sound_start(&e.pool[slot])
}

// musicFadeMs is the default fade duration for music transitions.
const musicFadeMs = 5000

// retiringSound is a track that has been faded out and is waiting for the fade
// to finish before its memory can be released.
type retiringSound struct {
	snd   *C.ma_sound
	timer int // frames remaining
}

// retireMusic fades the current track out and moves it to the retiring list.
// The caller is left with a fresh, empty e.music.
func (e *Engine) retireMusic() {
	C.ma_sound_set_fade_in_milliseconds(e.music, -1, 0, C.ma_uint64(musicFadeMs))
	C.ma_sound_set_stop_time_in_milliseconds(e.music, C.ma_uint64(musicFadeMs))

	e.musicRetiring = append(e.musicRetiring, retiringSound{
		snd:   e.music,
		timer: int(musicFadeMs/16) + 1,
	})
	e.music = (*C.ma_sound)(C.calloc(1, C.size_t(unsafe.Sizeof(C.ma_sound{}))))
}

// PlayMusic starts a looping, non-spatialized music track with a fade-in.
// Stops any currently playing music first (with crossfade).
func (e *Engine) PlayMusic(path string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Fade the previous track out onto the retiring list, so the new one can
	// start immediately and the two overlap for the length of the crossfade.
	if e.musicOn {
		e.retireMusic()
	}

	cpath := C.CString(path)
	defer C.free(unsafe.Pointer(cpath))

	flags := C.MA_SOUND_FLAG_STREAM | C.MA_SOUND_FLAG_NO_SPATIALIZATION
	result := C.ma_sound_init_from_file(e.engine, cpath, C.ma_uint32(flags), nil, nil, e.music)
	if result != C.MA_SUCCESS {
		return fmt.Errorf("ma_sound_init_from_file (music) failed: %d", int(result))
	}
	e.musicOn = true

	C.ma_sound_set_looping(e.music, C.MA_TRUE)
	C.ma_sound_start(e.music)
	return nil
}

// StopMusic fades out and stops the currently playing music track.
func (e *Engine) StopMusic() {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.musicOn {
		e.musicOn = false
		e.retireMusic()
	}
}

// recycleMusicOld releases retiring tracks whose fade has finished.
//
// Every entry is independent, so overlapping transitions each get their full
// fade rather than the newest one evicting the rest.
func (e *Engine) recycleMusicOld() {
	kept := e.musicRetiring[:0]
	for _, r := range e.musicRetiring {
		r.timer--
		if r.timer > 0 {
			kept = append(kept, r)
			continue
		}
		C.ma_sound_uninit(r.snd)
		C.free(unsafe.Pointer(r.snd))
	}
	e.musicRetiring = kept
}

// UpdateListener sets the 3D listener position and orientation from the camera.
// Call once per frame after computing view vectors.
func (e *Engine) UpdateListener(px, py, pz, fx, fy, fz, ux, uy, uz float32) {
	e.mu.Lock()
	defer e.mu.Unlock()

	C.ma_engine_listener_set_position(e.engine, 0, C.float(px), C.float(py), C.float(pz))
	C.ma_engine_listener_set_direction(e.engine, 0, C.float(fx), C.float(fy), C.float(fz))
	C.ma_engine_listener_set_world_up(e.engine, 0, C.float(ux), C.float(uy), C.float(uz))

	// Recycle finished one-shots and faded-out music each frame.
	e.recyclePool()
	e.recycleMusicOld()
	e.tickAmbients()
}

// SetMasterVolume sets the global volume (0.0 = silent, 1.0 = full).
func (e *Engine) SetMasterVolume(vol float32) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.masterVol = vol
	C.ma_engine_set_volume(e.engine, C.float(vol))
}

// PlayAmbient starts a looping ambient layer with the given ID. If a layer with
// that ID is already playing, this is a no-op. The layer crossfades its own tail
// into its head for seamless looping. Volume is 0.0–1.0.
func (e *Engine) PlayAmbient(id, path string, volume float32) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if _, exists := e.ambients[id]; exists {
		return nil
	}
	layer := newAmbientLayer(path, volume)
	if !layer.start(e.engine) {
		layer.cleanup()
		return fmt.Errorf("ambient %q: failed to start %s", id, path)
	}
	e.ambients[id] = layer
	return nil
}

// StopAmbient fades out and removes the ambient layer with the given ID.
func (e *Engine) StopAmbient(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if layer, ok := e.ambients[id]; ok {
		layer.stop()
	}
}

// StopAllAmbient fades out and removes all ambient layers.
func (e *Engine) StopAllAmbient() {
	e.mu.Lock()
	defer e.mu.Unlock()

	for _, layer := range e.ambients {
		layer.stop()
	}
}

// tickAmbients advances all ambient layers and removes finished ones.
// Called under mu lock from UpdateListener.
func (e *Engine) tickAmbients() {
	for id, layer := range e.ambients {
		if !layer.tick(e.engine) {
			delete(e.ambients, id)
		}
	}
}
