package glyphengine

import (
	"testing"

	"github.com/go-gl/mathgl/mgl32"

	"github.com/derekmwright/glyphengine/ecs"
)

// TestCustomIntegratorReplacesDefault installs a no-op Integrator and checks
// two things at once: the custom function is called exactly once per Tick,
// and the built-in IntegrateBodies is NOT also called alongside it. The
// second half can only be shown behaviorally, not by an instrumented counter
// on IntegrateBodies itself — a custom no-op integrator does nothing, so if
// gravity is still being applied, IntegrateBodies must be running too.
//
// Break experiment: reverting Tick to call IntegrateBodies(s, dt)
// unconditionally, in addition to s.Integrator, makes this fail: calls stays
// 3 as expected (the custom integrator is still invoked), but the body's Y
// position drops below the spawn height because gravity ran anyway, which
// the last assertion catches.
func TestCustomIntegratorReplacesDefault(t *testing.T) {
	s := NewScene()
	body := spawnBody(s, mgl32.Vec3{0, 10, 0})

	calls := 0
	s.Integrator = func(sc *Scene, dt float32) {
		calls++
		if sc != s {
			t.Errorf("Integrator called with wrong *Scene")
		}
	}

	const ticks = 3
	step(s, ticks)

	if calls != ticks {
		t.Fatalf("custom Integrator called %d times, want %d (once per Tick)", calls, ticks)
	}

	// The custom integrator did nothing, so if the default also ran, gravity
	// would have moved the body. It must not have.
	tr, _ := s.C.Transform.Get(body)
	if tr.Position.Y() != 10 {
		t.Errorf("body moved to Y=%.4f with a no-op Integrator installed; IntegrateBodies must have run too", tr.Position.Y())
	}
}

// TestCustomIntegratorDelegatingToIntegrateBodiesMatchesDefault checks that a
// custom Integrator which simply calls the exported IntegrateBodies
// reproduces the default path bit for bit — the delegation escape hatch the
// issue asks for actually works, not just compiles.
//
// Break experiment: changing the delegating integrator to call
// IntegrateBodies twice (as "integrate then overwrite" — one of the two
// workarounds the issue says are both wrong — would do) makes this fail: the
// delegating scene's body comes to rest at a different Y and with different
// intermediate velocities than the default scene's, because gravity was
// applied twice per tick.
func TestCustomIntegratorDelegatingToIntegrateBodiesMatchesDefault(t *testing.T) {
	const ticks = 90 // long enough to fall, hit the ground, and settle

	def := NewScene()
	def.SetTerrain(flatTerrain(t, 0))
	defBody := spawnBody(def, mgl32.Vec3{1, 8, -2})
	step(def, ticks)

	custom := NewScene()
	custom.SetTerrain(flatTerrain(t, 0))
	custom.Integrator = func(sc *Scene, dt float32) { IntegrateBodies(sc, dt) }
	customBody := spawnBody(custom, mgl32.Vec3{1, 8, -2})
	step(custom, ticks)

	defTr, _ := def.C.Transform.Get(defBody)
	customTr, _ := custom.C.Transform.Get(customBody)
	if defTr.Position != customTr.Position {
		t.Errorf("delegating Integrator diverged: default=%v delegating=%v", defTr.Position, customTr.Position)
	}
	defVel, _ := def.C.Velocity.Get(defBody)
	customVel, _ := custom.C.Velocity.Get(customBody)
	if defVel.Vec != customVel.Vec {
		t.Errorf("delegating Integrator velocity diverged: default=%v delegating=%v", defVel.Vec, customVel.Vec)
	}
}

// TestNewSceneIntegratorCanBeWrapped: the field's doc says NewScene sets it to
// IntegrateBodies, and the reason that is a promise rather than a detail is the
// way a game extends it -- keep whatever was there, call it, then do its own
// part -- which is a nil call if NewScene left the field for Tick's fallback
// to cover. Nothing else here notices: with the default removed from NewScene
// every other test in this file passes, because the fallback does the same
// work. Verified exactly that way; this is the one that fails.
func TestNewSceneIntegratorCanBeWrapped(t *testing.T) {
	s := NewScene()
	s.SetTerrain(flatTerrain(t, 0))
	if s.Integrator == nil {
		t.Fatal("NewScene left Integrator nil; a game wrapping the previous value would call nil")
	}

	prev := s.Integrator
	wrapped := 0
	s.Integrator = func(sc *Scene, dt float32) {
		prev(sc, dt)
		wrapped++
	}
	body := spawnBody(s, mgl32.Vec3{0, 8, 0})
	step(s, 10)

	if wrapped != 10 {
		t.Errorf("the wrapping Integrator ran %d times in 10 ticks", wrapped)
	}
	if tr, _ := s.C.Transform.Get(body); tr.Position.Y() >= 8 {
		t.Errorf("the body is at y=%v after 10 ticks; the wrapped default did not integrate it", tr.Position.Y())
	}
}

// TestNilIntegratorFallsBackToIntegrateBodies builds a Scene the way NewScene
// itself does NOT — a bare struct literal, standing in for a game type that
// embeds Scene by value or otherwise ends up with one that never went through
// NewScene — and confirms gravity still applies. This is the "zero-valued
// Scene literal must not silently lose physics" requirement from the issue:
// nil is "use IntegrateBodies," never "integrate nothing."
//
// Break experiment: changing Tick's dispatch from
// "if s.Integrator != nil { s.Integrator(s, dt) } else { IntegrateBodies(s, dt) }"
// to plain "if s.Integrator != nil { s.Integrator(s, dt) }" (dropping the
// fallback) makes this fail: the body never falls, staying at its spawn
// height instead of settling on the terrain.
func TestNilIntegratorFallsBackToIntegrateBodies(t *testing.T) {
	w := ecs.NewWorld()
	s := &Scene{
		world: w,
		C:     NewComponents(w),
		// Gravity is set because ITS zero value is separately documented,
		// tested behaviour (0 == gravity off, see physics-queries.md) — this
		// test isolates the Integrator field alone.
		Gravity: DefaultGravity,
		// Integrator deliberately left at its zero value (nil).
	}
	s.SetTerrain(flatTerrain(t, 0))

	body := spawnBody(s, mgl32.Vec3{0, 5, 0})
	step(s, 120)

	tr, _ := s.C.Transform.Get(body)
	const wantY = 0.5 + 0.001 // ground + half-height + IntegrateBodies's 1mm bias
	if tr.Position.Y() > wantY+0.01 {
		t.Errorf("body sitting at Y=%.4f with Integrator left nil; want it to have fallen to ~%.4f under the default", tr.Position.Y(), wantY)
	}
}

// TestIntegratorOffOnPurpose is the other half of the nil decision: a game
// that wants NO built-in integration writes a real no-op function, which is
// different from — and, per the field comment, the only supported way to get
// — "off." A Velocity-bearing body must sit exactly where it was spawned.
func TestIntegratorOffOnPurpose(t *testing.T) {
	s := NewScene()
	s.SetTerrain(flatTerrain(t, 0))
	s.Integrator = func(*Scene, float32) {}

	body := spawnBody(s, mgl32.Vec3{3, 9, 3})
	step(s, 120)

	tr, _ := s.C.Transform.Get(body)
	if tr.Position != (mgl32.Vec3{3, 9, 3}) {
		t.Errorf("body moved to %v with integration turned off on purpose, want unchanged at {3 9 3}", tr.Position)
	}
	if vel, _ := s.C.Velocity.Get(body); vel.Vec != (mgl32.Vec3{}) {
		t.Errorf("velocity changed to %v with integration turned off on purpose, want zero", vel.Vec)
	}
}

// TestTickDefaultIntegratorPathIsBitIdenticalToOldTick guards the ordering
// requirement: for the default integrator, Tick's new dispatch must run at
// exactly the point the old, unconditional "IntegrateBodies(s, dt)" call did
// — relative to Env.Advance, PathFinder.Tick, and registered systems. It
// proves this by reimplementing the OLD Tick body inline against a second
// Scene and comparing every Transform against the real, current Tick after N
// ticks.
//
// A system that zeroes every body's Velocity is what makes the position in
// Tick's sequence detectable: integrate-then-zero (the correct order) gives
// every body exactly one tick's fall each tick, so it never accelerates
// beyond gravity*dt; zero-then-integrate would let velocity start
// accumulating across ticks instead, since the system would run before
// gravity had a chance to touch it that tick. Nothing about plain gravity
// alone would show a swap, because IntegrateBodies is the only thing in this
// Tick that touches Transform or Velocity in this scene.
//
// Break experiment (verified): moving the Integrator dispatch in Tick to run
// after the systems loop instead of before it makes this test fail —
// positions diverge starting at tick 2, because the velocity-zeroing system
// then runs before gravity each tick instead of after, and the reference
// Scene (built from the pre-change ordering) falls slower.
func TestTickDefaultIntegratorPathIsBitIdenticalToOldTick(t *testing.T) {
	const dt = 1.0 / 60.0
	const ticks = 50

	build := func() (*Scene, []Entity) {
		s := NewScene()
		s.SetTerrain(flatTerrain(t, 0))
		var bodies []Entity
		bodies = append(bodies, spawnBody(s, mgl32.Vec3{0, 6, 0}))
		bodies = append(bodies, spawnBody(s, mgl32.Vec3{2, 9, -1}))
		ch := spawnCharacter(s, mgl32.Vec3{5, 5, 5})
		bodies = append(bodies, ch)
		s.SetTimeOfDay(0.2)
		s.SetDayCycleSpeed(1.0 / 60.0)
		// Order-sensitive system: see the comment above for why this is what
		// makes a swap of Tick's step order detectable through Transform.
		s.AddSystem(func(sc *Scene, dt float32) {
			sc.C.Velocity.Each(func(_ Entity, v *Velocity) {
				v.Vec = mgl32.Vec3{}
			})
		})
		return s, bodies
	}

	ref, refEntities := build()
	// referenceOldTick reproduces Tick exactly as it read before this change:
	// IntegrateBodies called unconditionally, with no Integrator dispatch.
	referenceOldTick := func(s *Scene, dt float32) {
		s.tickCount++
		if s.Interpolate {
			s.snapshotTransforms()
		}
		if s.Env != nil {
			s.Env.Advance(dt)
		}
		IntegrateBodies(s, dt)
		if s.PathFinder != nil {
			s.PathFinder.Tick()
		}
		for _, sys := range s.systems {
			sys(s, dt)
		}
	}
	for i := 0; i < ticks; i++ {
		referenceOldTick(ref, dt)
	}

	got, gotEntities := build()
	for i := 0; i < ticks; i++ {
		got.Tick(dt)
	}

	for i := range refEntities {
		wantTr, _ := ref.C.Transform.Get(refEntities[i])
		gotTr, _ := got.C.Transform.Get(gotEntities[i])
		if *wantTr != *gotTr {
			t.Errorf("entity %d: Transform diverged from the pre-change Tick behavior:\n  old-shaped Tick: %+v\n  new Tick:        %+v",
				i, *wantTr, *gotTr)
		}
	}
	if ref.TickCount() != got.TickCount() {
		t.Errorf("tick count diverged: old-shaped=%d new=%d", ref.TickCount(), got.TickCount())
	}
}
