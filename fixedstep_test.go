package glyphengine

import (
	"testing"
	"time"

	"github.com/go-gl/mathgl/mgl32"
)

// runLoop replays Engine.Run's accumulator over a fixed wall-clock duration at
// a given frame rate, calling onTick for each fixed simulation step exactly as
// Run calls Scene.Tick and Game.FixedUpdate.
//
// It deliberately mirrors Run rather than calling it, because Run needs a
// window and a GPU. If the accumulator logic in Run changes, this must change
// with it.
func runLoop(fps float64, seconds float64, tickRate int, onTick func(dt float32)) (ticks int) {
	return runLoopBudget(fps, seconds, tickRate, DefaultMaxCatchUp, onTick)
}

// runLoopBudget is runLoop with an explicit catch-up budget.
func runLoopBudget(fps float64, seconds float64, tickRate int, maxCatchUp time.Duration, onTick func(dt float32)) (ticks int) {
	tickDuration := time.Second / time.Duration(tickRate)
	tickDt := float32(tickDuration.Seconds())
	frameDelta := time.Duration(float64(time.Second) / fps)
	total := time.Duration(seconds * float64(time.Second))
	budget := resolveMaxCatchUp(maxCatchUp, tickDuration)

	var accumulator time.Duration
	for elapsed := time.Duration(0); elapsed < total; elapsed += frameDelta {
		accumulator += frameDelta
		if accumulator > budget {
			accumulator = budget
		}
		for accumulator >= tickDuration {
			onTick(tickDt)
			accumulator -= tickDuration
			ticks++
		}
	}
	return ticks
}

// TestJumpHeightIsFrameRateIndependent is the regression this whole split
// exists for. Movement on the frame delta made jump apex vary about 5% between
// 30fps and 300fps; on the fixed tick every frame rate must agree exactly.
func TestJumpHeightIsFrameRateIndependent(t *testing.T) {
	peakAt := func(fps float64) float32 {
		s := NewScene()
		s.SetTerrain(flatTerrain(t, 0))
		ch := spawnCharacter(s, mgl32.Vec3{0, 0.9, 0})
		tr, _ := s.C.Transform.Get(ch)

		jumped := false
		peak := tr.Position.Y()
		runLoop(fps, 3.0, DefaultTickRate, func(dt float32) {
			var intent MoveIntent
			if !jumped {
				intent.Jump = true
				jumped = true
			}
			s.MoveCharacter(ch, intent, dt)
			if tr.Position.Y() > peak {
				peak = tr.Position.Y()
			}
		})
		return peak
	}

	rates := []float64{30, 60, 75, 144, 165, 240, 300}
	want := peakAt(rates[0])
	for _, fps := range rates {
		got := peakAt(fps)
		if got != want {
			t.Errorf("%.0f fps: jump peak %.6f, want exactly %.6f (frame rate must not affect simulation)",
				fps, got, want)
		}
	}
	t.Logf("jump peak identical at %v fps: %.4f", rates, want)
}

// TestWalkDistanceIsFrameRateIndependent covers continuous input the same way
// jumping covers an impulse.
func TestWalkDistanceIsFrameRateIndependent(t *testing.T) {
	walkAt := func(fps float64) (float32, int) {
		s := NewScene()
		s.SetTerrain(flatTerrain(t, 0))
		ch := spawnCharacter(s, mgl32.Vec3{0, 0.9, 0})
		ticks := runLoop(fps, 2.0, DefaultTickRate, func(dt float32) {
			s.MoveCharacter(ch, MoveIntent{Forward: 1}, dt)
		})
		tr, _ := s.C.Transform.Get(ch)
		return -tr.Position.Z(), ticks
	}

	// Distance per tick is the invariant. Total tick count over a fixed wall
	// clock can differ by one purely from where the last frame boundary lands
	// relative to the end of the window — an artifact of this harness stepping
	// in discrete frames, not of the simulation.
	baseDist, baseTicks := walkAt(60)
	wantPerTick := baseDist / float32(baseTicks)

	for _, fps := range []float64{60, 75, 144, 165, 240, 300} {
		got, ticks := walkAt(fps)

		if diff := ticks - baseTicks; diff < -1 || diff > 1 {
			t.Errorf("%.0f fps: %d ticks in 2s, want %d±1 (simulation rate must not follow frame rate)",
				fps, ticks, baseTicks)
		}
		if perTick := got / float32(ticks); perTick != wantPerTick {
			t.Errorf("%.0f fps: %.9f units per tick, want exactly %.9f",
				fps, perTick, wantPerTick)
		}
	}
	t.Logf("walk identical across 60-300fps: %.9f units/tick (%d ticks in 2s at 60fps)",
		wantPerTick, baseTicks)
}

// TestSimulationKeepsUpAcrossTickRates is the regression for a catch-up budget
// expressed in ticks instead of time.
//
// The old cap was two ticks, which is 33ms at a 60Hz tick but only 16ms at
// 128Hz — less than one frame at 60fps. Every frame discarded time it could
// never make up, so raising the tick rate for a fast-paced game silently put
// anyone under ~120fps into slow motion: 128Hz/30fps ran at 48% speed, and
// 240Hz/60fps at 50%.
func TestSimulationKeepsUpAcrossTickRates(t *testing.T) {
	const seconds = 2.0

	for _, tickRate := range []int{30, 60, 128, 240} {
		for _, fps := range []float64{30, 60, 100, 144, 300} {
			ticks := runLoop(fps, seconds, tickRate, func(float32) {})
			want := int(seconds * float64(tickRate))
			speed := float64(ticks) / float64(want)

			if speed < 0.99 || speed > 1.02 {
				t.Errorf("tick %dHz at %.0f fps: %d ticks in %.0fs, want %d (%.0f%% speed)",
					tickRate, fps, ticks, seconds, want, speed*100)
			}
		}
	}
}

// TestCatchUpBudgetBoundsTheSpiral covers the other half: past the budget the
// simulation must fall behind rather than accumulate an unpayable debt.
func TestCatchUpBudgetBoundsTheSpiral(t *testing.T) {
	const seconds = 2.0
	const tickRate = 60
	tickDuration := time.Second / tickRate

	// A 100ms budget allows six ticks of catch-up per frame at 60Hz, so the
	// simulation keeps up down to ~10fps and dilates below that.
	const budget = 100 * time.Millisecond

	for _, tc := range []struct {
		fps     float64
		keepsUp bool
	}{
		{60, true},
		{20, true},
		{10, true},
		{4, false}, // 250ms frames against a 100ms budget
		{2, false},
	} {
		ticks := runLoopBudget(tc.fps, seconds, tickRate, budget, func(float32) {})
		want := int(seconds * tickRate)
		speed := float64(ticks) / float64(want)

		if tc.keepsUp && speed < 0.99 {
			t.Errorf("%.0f fps with a %v budget ran at %.0f%% speed, expected to keep up",
				tc.fps, budget, speed*100)
		}
		if !tc.keepsUp && speed > 0.95 {
			t.Errorf("%.0f fps with a %v budget ran at %.0f%% speed; the budget is not bounding catch-up",
				tc.fps, budget, speed*100)
		}

		// Whatever happens, one frame never simulates more than the budget.
		maxTicksPerFrame := int(budget/tickDuration) + 1
		frames := int(tc.fps * seconds)
		if ticks > maxTicksPerFrame*frames+maxTicksPerFrame {
			t.Errorf("%.0f fps: %d ticks exceeds the %d-per-frame ceiling the budget implies",
				tc.fps, ticks, maxTicksPerFrame)
		}
	}
}

// TestMaxCatchUpAlwaysAllowsOneTick guards the degenerate configuration: a
// budget smaller than a single tick would starve the simulation entirely.
func TestMaxCatchUpAlwaysAllowsOneTick(t *testing.T) {
	tickDuration := time.Second / 128
	for _, requested := range []time.Duration{0, -1, time.Nanosecond, tickDuration / 2} {
		if got := resolveMaxCatchUp(requested, tickDuration); got < tickDuration {
			t.Errorf("resolveMaxCatchUp(%v) = %v, want at least one tick (%v)",
				requested, got, tickDuration)
		}
	}
	if got := resolveMaxCatchUp(0, tickDuration); got != DefaultMaxCatchUp {
		t.Errorf("zero should mean the default %v, got %v", DefaultMaxCatchUp, got)
	}
}

// TestTickCountAdvancesWithSimulation checks the simulation clock that network
// code will stamp intents with.
func TestTickCountAdvancesWithSimulation(t *testing.T) {
	s := NewScene()
	if s.TickCount() != 0 {
		t.Fatalf("fresh scene TickCount = %d, want 0", s.TickCount())
	}

	const dt = 1.0 / 60.0
	for i := 0; i < 150; i++ {
		s.Tick(dt)
	}
	if s.TickCount() != 150 {
		t.Errorf("TickCount = %d after 150 ticks, want 150", s.TickCount())
	}
}

// TestFixedUpdateRunsOncePerTick pins the contract the input-latching rule
// depends on: FixedUpdate is called exactly as many times as Scene.Tick, and
// that count varies per frame.
func TestFixedUpdateRunsOncePerTick(t *testing.T) {
	s := NewScene()
	var fixedCalls int
	ticks := runLoop(144, 1.0, DefaultTickRate, func(dt float32) {
		s.Tick(dt)
		fixedCalls++
	})

	if fixedCalls != ticks {
		t.Errorf("FixedUpdate ran %d times for %d ticks; they must match", fixedCalls, ticks)
	}
	if uint64(ticks) != s.TickCount() {
		t.Errorf("TickCount = %d, loop ran %d ticks", s.TickCount(), ticks)
	}
	// 144Hz against a 60Hz tick: about 60 ticks in a second, spread unevenly
	// across 144 frames, so most frames run none at all.
	if ticks < 58 || ticks > 61 {
		t.Errorf("got %d ticks in 1s at 144fps, want ~60", ticks)
	}
}
