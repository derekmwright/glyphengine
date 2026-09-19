package glyphengine

import (
	"testing"
	"time"
)

// These drive Engine.advanceSimulation, which is the loop's own code, and
// assert on Scene.TickCount and on FixedUpdate actually being called.
//
// The first version of this file did not. It re-implemented the accumulator
// arithmetic in the test and checked that, which passed happily with the
// scaling deleted from app.go — it was testing the copy rather than the engine.
// That is why advanceSimulation exists as a method: Run needs a window and a
// GPU, so the part worth testing had to come out of it.
//
// The failure being guarded is silent. A game that pauses by returning early
// from FixedUpdate has stopped its own simulation and none of the engine's, and
// nothing errors while a crate keeps sliding behind the menu.

// countingGame records how many times FixedUpdate ran.
type countingGame struct{ fixed int }

func (g *countingGame) Init(*Engine) error      { return nil }
func (g *countingGame) Update(*Engine, float32) {}
func (g *countingGame) FixedUpdate(_ *Engine, _ float32) {
	g.fixed++
}

func testEngine() (*Engine, *countingGame) {
	g := &countingGame{}
	e := &Engine{
		Scene:        NewScene(),
		timeScale:    1,
		tickDuration: time.Second / time.Duration(DefaultTickRate),
		maxCatchUp:   250 * time.Millisecond,
		fixedUpdate:  g,
	}
	return e, g
}

// run drives whole frames through the engine's own step.
func run(e *Engine, frames int, frameDelta time.Duration) {
	for i := 0; i < frames; i++ {
		e.advanceSimulation(frameDelta)
	}
}

func TestTimeScaleZeroStopsTheSimulation(t *testing.T) {
	// Control first: at real time the same run has to tick, or the assertions
	// below pass on a loop that never ran.
	ctl, ctlGame := testEngine()
	run(ctl, 120, 16*time.Millisecond)
	if ctl.Scene.TickCount() == 0 || ctlGame.fixed == 0 {
		t.Fatal("control: nothing ticked at scale 1, so this test proves nothing")
	}

	e, game := testEngine()
	e.SetTimeScale(0)
	run(e, 120, 16*time.Millisecond)

	if got := e.Scene.TickCount(); got != 0 {
		t.Errorf("paused engine ran %d scene ticks; physics and controllers should be stopped", got)
	}
	if game.fixed != 0 {
		t.Errorf("paused engine called FixedUpdate %d times", game.fixed)
	}
}

// Half speed is half as many ticks of the same size, not the same number of
// shorter ticks. A fixed timestep is only fixed if the tick delta never moves;
// a shortened step would change how the integrator behaves and break
// determinism with it.
func TestTimeScaleHalvesTickCountNotTickSize(t *testing.T) {
	const frames = 600
	const frameDelta = 16 * time.Millisecond

	full, _ := testEngine()
	run(full, frames, frameDelta)

	half, _ := testEngine()
	half.SetTimeScale(0.5)
	run(half, frames, frameDelta)

	fullTicks, halfTicks := int(full.Scene.TickCount()), int(half.Scene.TickCount())
	if d := fullTicks - halfTicks*2; d < -1 || d > 1 {
		t.Errorf("half speed ran %d ticks against %d at full; want about half", halfTicks, fullTicks)
	}

	// The tick delta itself is untouched, which is the property that keeps the
	// simulation deterministic under slow motion.
	if half.tickDuration != full.tickDuration {
		t.Errorf("time scale changed the tick duration: %v against %v", half.tickDuration, full.tickDuration)
	}
}

func TestTimeScaleAboveOneRunsFaster(t *testing.T) {
	const frames = 300
	const frameDelta = 16 * time.Millisecond

	full, _ := testEngine()
	run(full, frames, frameDelta)

	fast, _ := testEngine()
	fast.SetTimeScale(3)
	run(fast, frames, frameDelta)

	fullTicks, fastTicks := int(full.Scene.TickCount()), int(fast.Scene.TickCount())
	if fastTicks <= fullTicks {
		t.Errorf("scale 3 ran %d ticks, not more than %d at scale 1", fastTicks, fullTicks)
	}
	if d := fastTicks - fullTicks*3; d < -2 || d > 2 {
		t.Errorf("scale 3 ran %d ticks against %d at full; want about triple", fastTicks, fullTicks)
	}
}

// Interpolation holds where it was while paused, which is what keeps a paused
// frame at one consistent pose rather than jittering between two ticks.
func TestPausingFreezesTheInterpolationAlpha(t *testing.T) {
	e, _ := testEngine()
	run(e, 7, 16*time.Millisecond) // land mid-tick, so alpha is not zero
	if e.alpha == 0 {
		t.Skip("alpha landed exactly on a tick boundary; nothing to freeze")
	}

	frozen := e.alpha
	e.SetTimeScale(0)
	run(e, 60, 16*time.Millisecond)

	if e.alpha != frozen {
		t.Errorf("alpha moved from %v to %v while paused", frozen, e.alpha)
	}
}

// Running a fixed-timestep simulation backwards is not something this engine
// can do; the integrator is not reversible. The useful reading of a negative
// scale is "stopped", and silently running it forwards would be worse.
func TestNegativeTimeScaleClampsToPaused(t *testing.T) {
	e, game := testEngine()
	e.SetTimeScale(-2)

	if got := e.TimeScale(); got != 0 {
		t.Errorf("TimeScale() = %v after setting -2, want 0", got)
	}
	if !e.Paused() {
		t.Error("a negative scale did not report as paused")
	}

	run(e, 120, 16*time.Millisecond)
	if e.Scene.TickCount() != 0 || game.fixed != 0 {
		t.Errorf("a negative scale ran %d ticks and %d fixed updates", e.Scene.TickCount(), game.fixed)
	}
}

func TestDefaultTimeScaleIsRealTime(t *testing.T) {
	e, _ := testEngine()
	if got := e.TimeScale(); got != 1 {
		t.Errorf("a fresh engine has TimeScale() = %v, want 1", got)
	}
	if e.Paused() {
		t.Error("a fresh engine reports paused")
	}
}

// TestShaderClockAdvancesWithTheTicks: the elapsed clock (grass wind, water
// waves, the cloud march) and the tick clock must move together. They used not
// to: elapsed was advanced further down the frame loop, below the
// minimized-window check, which `continue`s -- so a minimized game kept ticking
// while its waves stood still, and the clocks came back apart by however long
// the window had been down.
//
// The advance now lives in advanceSimulation, which is the one thing the loop
// cannot skip without skipping the ticks too, so driving that function is
// driving the behaviour. Verified to fail with the advance moved back out: the
// clock then reads 0 after a second of frames.
func TestShaderClockAdvancesWithTheTicks(t *testing.T) {
	e, _ := testEngine()
	frame := time.Second / 60

	run(e, 60, frame)
	if got := e.Elapsed(); got < 0.99 || got > 1.01 {
		t.Fatalf("after one second of frames the shader clock reads %v, want about 1", got)
	}
	if e.tickCount == 0 {
		t.Fatal("no ticks ran, so agreeing with the tick clock means nothing")
	}

	// Paused: neither clock moves.
	e.SetTimeScale(0)
	before, ticks := e.Elapsed(), e.tickCount
	run(e, 60, frame)
	if e.Elapsed() != before || e.tickCount != ticks {
		t.Fatalf("paused, the clocks moved: elapsed %v -> %v, ticks %d -> %d", before, e.Elapsed(), ticks, e.tickCount)
	}

	// Half speed: both clocks run at half rate, together.
	e.SetTimeScale(0.5)
	before, ticks = e.Elapsed(), e.tickCount
	run(e, 120, frame)
	if got := e.Elapsed() - before; got < 0.99 || got > 1.01 {
		t.Fatalf("two seconds at half speed advanced the shader clock by %v, want about 1", got)
	}
	if got := e.tickCount - ticks; got < 59 || got > 61 {
		t.Fatalf("two seconds at half speed ran %d ticks, want about 60", got)
	}
}
