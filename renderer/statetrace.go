package renderer

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"strconv"
	"unsafe"
)

// StateTrace writes one line per frame-loop iteration describing what that
// iteration did and what it handed the GPU.
//
// It exists because a render that differs run to run cannot be found by
// reading code. The two sightings behind issue #40 -- 08-grass at night and
// 12-particles under load -- both produced a single wrong image with no other
// symptom, and every obvious trigger (a fresh binary, GPU contention, the
// spawn RNG, wall-clock HUD text) had already been ruled out one guess at a
// time. A per-frame record that two runs can be diffed answers the only
// question worth asking first: which frame, and which subsystem, went first.
//
// Enable it with GLYPHENGINE_STATE_TRACE=<path>, then diff two runs:
//
//	GLYPHENGINE_STATE_TRACE=a.trace GLYPHENGINE_FIXED_FRAME_TIME=16.667ms go run ./08-grass -frames 150 -screenshot a.png
//	GLYPHENGINE_STATE_TRACE=b.trace GLYPHENGINE_FIXED_FRAME_TIME=16.667ms go run ./08-grass -frames 150 -screenshot b.png
//	diff a.trace b.trace | head
//
// The first differing line names the frame; the first differing field on it
// names the subsystem. A line whose sim= matches but whose grass= differs is a
// renderer-side divergence with an identical simulation behind it, which is a
// very different bug from the other way round.
//
// Every field is a hash rather than the data, because the point is a cheap
// equality test across runs, not a dump. Nothing that varies between processes
// for reasons of its own -- a Vulkan handle, a Go pointer, a wall-clock
// reading -- is ever hashed, or every line would differ and the tool would say
// nothing. That constraint is why the draw-list hash covers model matrices and
// material constants rather than RenderObject wholesale.
//
// Off by default and off in every shipped path: a nil *StateTrace is the
// disabled state and each of its methods returns immediately, so the cost of
// the instrument in a normal run is one nil check per call site. The callers
// guard hash computation on the same nil, so nothing is hashed either.
// TestStateTraceDisabledAllocatesNothing holds that.
type StateTrace struct {
	f    *os.File
	w    *bufio.Writer
	line []byte
}

// OpenStateTrace creates a trace file. A nil *StateTrace is a valid disabled
// trace, so a caller that does not want one passes nil rather than branching.
func OpenStateTrace(path string) (*StateTrace, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("renderer: state trace: %w", err)
	}
	return &StateTrace{f: f, w: bufio.NewWriterSize(f, 1<<16), line: make([]byte, 0, 512)}, nil
}

// Close flushes and closes the trace.
func (t *StateTrace) Close() error {
	if t == nil {
		return nil
	}
	if err := t.w.Flush(); err != nil {
		t.f.Close()
		return err
	}
	return t.f.Close()
}

// Begin starts a record for one iteration of the frame loop, counted including
// iterations that render nothing -- which is the whole point: a loop that
// simulated 151 times and presented 150 is exactly the failure this is looking
// for, and a trace keyed on the rendered count could not show it.
func (t *StateTrace) Begin(loop int) {
	if t == nil {
		return
	}
	t.line = t.line[:0]
	t.Int("loop", loop)
}

// Str appends a string field.
func (t *StateTrace) Str(key, value string) {
	if t == nil {
		return
	}
	t.key(key)
	t.line = append(t.line, value...)
}

// Int appends an integer field.
func (t *StateTrace) Int(key string, value int) {
	if t == nil {
		return
	}
	t.key(key)
	t.line = strconv.AppendInt(t.line, int64(value), 10)
}

// Hash appends a hash field, written in hex so it is obviously not a quantity.
func (t *StateTrace) Hash(key string, h Hasher) {
	if t == nil {
		return
	}
	t.key(key)
	t.line = strconv.AppendUint(t.line, uint64(h), 16)
}

// CountHash appends a count and a hash as one field, e.g. draws=37/9f3c...
// Two runs that disagree on how many of something there were, and two that
// agree on the count but not the contents, are different bugs, and reading
// them off one field beats correlating two.
func (t *StateTrace) CountHash(key string, n int, h Hasher) {
	if t == nil {
		return
	}
	t.key(key)
	t.line = strconv.AppendInt(t.line, int64(n), 10)
	t.line = append(t.line, '/')
	t.line = strconv.AppendUint(t.line, uint64(h), 16)
}

func (t *StateTrace) key(k string) {
	if len(t.line) > 0 {
		t.line = append(t.line, ' ')
	}
	t.line = append(t.line, k...)
	t.line = append(t.line, '=')
}

// End writes the record. Errors are dropped: a diagnostic that takes the run
// down changes the thing it is measuring.
func (t *StateTrace) End() {
	if t == nil {
		return
	}
	t.line = append(t.line, '\n')
	t.w.Write(t.line)
}

// Hasher is a running FNV-1a/64 over whatever a caller feeds it.
//
// FNV rather than something stronger because this is an equality test between
// two runs of the same build, not a signature: collisions would have to be
// engineered, and the hash is on the frame path when the trace is on.
type Hasher uint64

// NewHash is the FNV-1a offset basis, and the value every field's hash starts
// from.
const NewHash Hasher = 14695981039346656037

const fnvPrime64 = 1099511628211

// Byte folds one byte in.
func (h Hasher) Byte(b byte) Hasher {
	return (h ^ Hasher(b)) * fnvPrime64
}

// Bytes folds a byte slice in.
func (h Hasher) Bytes(b []byte) Hasher {
	for _, c := range b {
		h = (h ^ Hasher(c)) * fnvPrime64
	}
	return h
}

// Uint64 folds an integer in, low byte first.
func (h Hasher) Uint64(v uint64) Hasher {
	for i := 0; i < 8; i++ {
		h = (h ^ Hasher(v&0xff)) * fnvPrime64
		v >>= 8
	}
	return h
}

// Int folds an int in.
func (h Hasher) Int(v int) Hasher { return h.Uint64(uint64(v)) }

// Float32 folds a float in by its bit pattern, so -0 and +0 are distinguished
// and a NaN payload is not lost. Two runs that produce different bits produce
// different images often enough that rounding them first would hide the bug.
func (h Hasher) Float32(v float32) Hasher { return h.Uint64(uint64(math.Float32bits(v))) }

// Float32s folds a slice of floats in.
func (h Hasher) Float32s(v []float32) Hasher {
	for _, f := range v {
		h = h.Float32(f)
	}
	return h
}

// Mat4 folds a 4x4 matrix in.
func (h Hasher) Mat4(m [16]float32) Hasher { return h.Float32s(m[:]) }

// Vec3 folds a 3-vector in.
func (h Hasher) Vec3(v [3]float32) Hasher { return h.Float32s(v[:]) }

// Bool folds a flag in.
func (h Hasher) Bool(b bool) Hasher {
	if b {
		return h.Byte(1)
	}
	return h.Byte(0)
}

// HashPOD folds a slice of plain data in by its memory image. Only valid for
// element types that contain no pointers and no padding that is left
// uninitialised -- GrassInstance, ParticleInstance and Vertex qualify.
// A pointer would hash an address, which differs between two runs of the same
// build for reasons that have nothing to do with the image, and a trace whose
// every line differs reports nothing.
func HashPOD[T any](h Hasher, s []T) Hasher {
	if len(s) == 0 {
		return h
	}
	n := len(s) * int(unsafe.Sizeof(s[0]))
	return h.Bytes(unsafe.Slice((*byte)(unsafe.Pointer(&s[0])), n))
}
