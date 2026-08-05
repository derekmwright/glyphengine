package glyphengine

import (
	"testing"
	"time"
)

const fixedFrameTimeEnv = "GLYPHENGINE_FIXED_FRAME_TIME"

func TestResolveFixedFrameTimeDefaultsToTheRealClock(t *testing.T) {
	if got := resolveFixedFrameTime(0); got != 0 {
		t.Errorf("resolveFixedFrameTime(0) = %v, want 0 (use the measured delta)", got)
	}
	if got := resolveFixedFrameTime(20 * time.Millisecond); got != 20*time.Millisecond {
		t.Errorf("resolveFixedFrameTime(20ms) = %v, want 20ms", got)
	}
	// A negative option is nonsense; treat it as unset rather than running the
	// clock backwards.
	if got := resolveFixedFrameTime(-time.Second); got != 0 {
		t.Errorf("resolveFixedFrameTime(-1s) = %v, want 0", got)
	}
}

func TestResolveFixedFrameTimeEnvironmentWins(t *testing.T) {
	t.Setenv(fixedFrameTimeEnv, "16.667ms")
	// The point of the variable is to force determinism on a binary that never
	// asked for it, so it has to beat whatever that binary passed.
	if got := resolveFixedFrameTime(0); got != 16667*time.Microsecond {
		t.Errorf("with env set, resolveFixedFrameTime(0) = %v, want 16.667ms", got)
	}
	if got := resolveFixedFrameTime(20 * time.Millisecond); got != 16667*time.Microsecond {
		t.Errorf("with env set, the option won: got %v, want 16.667ms", got)
	}
}

func TestResolveFixedFrameTimeRejectsBadValues(t *testing.T) {
	// Both of these must fall back rather than produce a zero or negative
	// delta, which would freeze or reverse every animation in the engine.
	for _, v := range []string{"garbage", "60", "0s", "-5ms"} {
		t.Run(v, func(t *testing.T) {
			t.Setenv(fixedFrameTimeEnv, v)
			if got := resolveFixedFrameTime(20 * time.Millisecond); got != 20*time.Millisecond {
				t.Errorf("%q gave %v, want the requested 20ms", v, got)
			}
		})
	}
}

// Pinning the spawn source is the other half of a repeatable run: math/rand's
// global is reseeded at process start, so particle spawns differ every time
// even when the clock does not.
func TestPinSpawnRandRepeats(t *testing.T) {
	pinSpawnRand(0x5eed)
	first := make([]float32, 8)
	for i := range first {
		first[i] = spawnRand.Float32()
	}

	pinSpawnRand(0x5eed)
	for i := range first {
		if got := spawnRand.Float32(); got != first[i] {
			t.Fatalf("draw %d = %v, want %v -- the source did not repeat", i, got, first[i])
		}
	}

	// And a different seed must actually differ, or the pin is hiding a
	// constant rather than fixing a sequence.
	pinSpawnRand(1)
	same := true
	for i := range first {
		if spawnRand.Float32() != first[i] {
			same = false
			break
		}
	}
	if same {
		t.Error("a different seed produced the same sequence")
	}
}
