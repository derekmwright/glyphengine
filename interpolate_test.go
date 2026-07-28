package glyphengine

import (
	"math"
	"testing"
	"time"

	"github.com/go-gl/mathgl/mgl32"
)

func TestLerpAngleTakesShortestArc(t *testing.T) {
	const eps = 1e-5
	for _, tc := range []struct {
		name       string
		a, b, want float32
	}{
		{"midpoint", 0, 1, 0.5},
		{"negative direction", 1, 0, 0.5},
		// The case plain lerp gets wrong: 179° to -179° is 2° across the wrap,
		// not 358° the long way. Halfway is 180°, i.e. ±π.
		{"across the wrap", math.Pi - 0.05, -math.Pi + 0.05, math.Pi},
		{"across the wrap, reversed", -math.Pi + 0.05, math.Pi - 0.05, -math.Pi},
		{"no movement", 2.5, 2.5, 2.5},
	} {
		got := LerpAngle(tc.a, tc.b, 0.5)
		// Compare as an angle: ±π are the same heading.
		diff := float64(got - tc.want)
		diff = math.Mod(diff+math.Pi, 2*math.Pi)
		if diff < 0 {
			diff += 2 * math.Pi
		}
		diff -= math.Pi
		if math.Abs(diff) > eps {
			t.Errorf("%s: LerpAngle(%.4f, %.4f, 0.5) = %.4f, want %.4f", tc.name, tc.a, tc.b, got, tc.want)
		}
	}
}

func TestLerpAngleNeverTravelsMoreThanPi(t *testing.T) {
	// Whatever the endpoints, a half-step must not move further than half a
	// turn — that is what "shortest arc" means.
	for i := 0; i < 360; i += 7 {
		for j := 0; j < 360; j += 11 {
			a := float32(i) * math.Pi / 180
			b := float32(j) * math.Pi / 180
			mid := LerpAngle(a, b, 0.5)
			if travelled := math.Abs(float64(mid - a)); travelled > math.Pi/2+1e-5 {
				t.Fatalf("LerpAngle(%.0f°, %.0f°, 0.5) moved %.1f°, more than half the shortest arc allows",
					float64(i), float64(j), travelled*180/math.Pi)
			}
		}
	}
}

func TestLerpTransformEndpoints(t *testing.T) {
	a := Transform{Position: mgl32.Vec3{0, 0, 0}, Rotation: mgl32.Vec3{0, 0, 0}, Scale: mgl32.Vec3{1, 1, 1}}
	b := Transform{Position: mgl32.Vec3{10, 4, -2}, Rotation: mgl32.Vec3{0, 1, 0}, Scale: mgl32.Vec3{2, 2, 2}}

	if got := LerpTransform(a, b, 0); got != a {
		t.Errorf("alpha=0 gave %+v, want the start transform", got)
	}
	if got := LerpTransform(a, b, 1); got.Position != b.Position || got.Scale != b.Scale {
		t.Errorf("alpha=1 gave %+v, want the end transform", got)
	}

	mid := LerpTransform(a, b, 0.5)
	if mid.Position != (mgl32.Vec3{5, 2, -1}) {
		t.Errorf("midpoint position %v, want [5 2 -1]", mid.Position)
	}
	if mid.Scale != (mgl32.Vec3{1.5, 1.5, 1.5}) {
		t.Errorf("midpoint scale %v, want [1.5 1.5 1.5]", mid.Scale)
	}
}

// TestInterpolationRecordsPreviousTransform checks the snapshot actually
// happens before simulation mutates anything.
func TestInterpolationRecordsPreviousTransform(t *testing.T) {
	s := NewScene()
	s.Interpolate = true
	s.SetTerrain(flatTerrain(t, 0))

	body := spawnBody(s, mgl32.Vec3{0, 10, 0})
	before, _ := s.C.Transform.Get(body)
	startY := before.Position.Y()

	s.Tick(1.0 / 60.0)

	prev, ok := s.C.PrevTransform.Get(body)
	if !ok {
		t.Fatal("no PrevTransform recorded after a tick with interpolation on")
	}
	if prev.Position.Y() != startY {
		t.Errorf("PrevTransform Y = %.4f, want the pre-tick value %.4f", prev.Position.Y(), startY)
	}
	if after, _ := s.C.Transform.Get(body); after.Position.Y() >= startY {
		t.Errorf("body did not fall: Y = %.4f", after.Position.Y())
	}

	// Halfway between ticks, the drawn position is halfway between the two.
	mid, _ := s.InterpolatedTransform(body, 0.5)
	after, _ := s.C.Transform.Get(body)
	wantY := (prev.Position.Y() + after.Position.Y()) / 2
	if math.Abs(float64(mid.Position.Y()-wantY)) > 1e-5 {
		t.Errorf("interpolated Y at alpha=0.5 = %.6f, want %.6f", mid.Position.Y(), wantY)
	}
}

// TestStaticEntitiesAreNotSnapshotted covers the cost decision: world geometry
// never moves, so paying a copy per tick for it would be the bulk of the work
// in a scene that is mostly level.
func TestStaticEntitiesAreNotSnapshotted(t *testing.T) {
	s := NewScene()
	s.Interpolate = true

	wall := s.Spawn()
	s.C.Transform.Set(wall, &Transform{Position: mgl32.Vec3{0, 1, 0}, Scale: mgl32.Vec3{1, 1, 1}})
	s.C.Static.Set(wall, &Static{})

	s.Tick(1.0 / 60.0)

	if s.C.PrevTransform.Has(wall) {
		t.Error("Static entity got a PrevTransform; it can never need one")
	}
	// It still renders, just without blending.
	if _, ok := s.InterpolatedTransform(wall, 0.5); !ok {
		t.Error("InterpolatedTransform failed for a Static entity")
	}
}

// TestInterpolationOffCostsNothing guards the headless case: a server ticking
// the same Scene should not be paying for a renderer feature.
func TestInterpolationOffCostsNothing(t *testing.T) {
	s := NewScene() // Interpolate defaults to false
	if s.Interpolate {
		t.Fatal("NewScene enabled interpolation; it must default off for headless use")
	}
	s.SetTerrain(flatTerrain(t, 0))
	body := spawnBody(s, mgl32.Vec3{0, 10, 0})

	s.Tick(1.0 / 60.0)

	if s.C.PrevTransform.Has(body) {
		t.Error("PrevTransform recorded with interpolation off")
	}
	// And the drawn transform is simply the current one.
	cur, _ := s.C.Transform.Get(body)
	got, _ := s.InterpolatedTransform(body, 0.5)
	if got != *cur {
		t.Errorf("interpolated transform %+v, want the current transform %+v", got, *cur)
	}
}

// TestTeleportDoesNotSmear is the failure this feature invites: without
// clearing the previous transform, an entity moved across the map is drawn
// sliding the whole way over one frame.
func TestTeleportDoesNotSmear(t *testing.T) {
	s := NewScene()
	s.Interpolate = true
	s.SetTerrain(flatTerrain(t, 0))

	ch := spawnCharacter(s, mgl32.Vec3{0, 0.9, 0})
	s.Tick(1.0 / 60.0)
	s.MoveCharacter(ch, MoveIntent{Forward: 1}, 1.0/60.0)

	// Teleport far away.
	tr, _ := s.C.Transform.Get(ch)
	tr.Position = mgl32.Vec3{500, 0.9, 500}

	// Without clearing, the midpoint would be halfway across the map.
	smeared, _ := s.InterpolatedTransform(ch, 0.5)
	if smeared.Position.X() < 100 {
		t.Fatalf("expected the un-cleared midpoint to be a smear, got %v — test no longer proves anything", smeared.Position)
	}

	s.ClearInterpolation(ch)
	got, _ := s.InterpolatedTransform(ch, 0.5)
	if got.Position != (mgl32.Vec3{500, 0.9, 500}) {
		t.Errorf("after ClearInterpolation the entity renders at %v, want its actual position", got.Position)
	}
}

// TestRestoreCharacterClearsInterpolation makes sure prediction corrections do
// not smear, since RestoreCharacter is a teleport by another name.
func TestRestoreCharacterClearsInterpolation(t *testing.T) {
	s := NewScene()
	s.Interpolate = true
	s.SetTerrain(flatTerrain(t, 0))

	ch := spawnCharacter(s, mgl32.Vec3{0, 0.9, 0})
	snap, _ := s.SnapshotCharacter(ch)

	// Simulate away from the snapshot so a PrevTransform exists and differs.
	for i := 0; i < 30; i++ {
		s.Tick(1.0 / 60.0)
		s.MoveCharacter(ch, MoveIntent{Forward: 1, Sprint: true}, 1.0/60.0)
	}
	if !s.C.PrevTransform.Has(ch) {
		t.Fatal("expected a PrevTransform after simulating")
	}

	s.RestoreCharacter(ch, snap)

	if s.C.PrevTransform.Has(ch) {
		t.Error("RestoreCharacter left a stale PrevTransform; corrections would smear")
	}
	got, _ := s.InterpolatedTransform(ch, 0.5)
	if got.Position != snap.Position {
		t.Errorf("after restore the entity renders at %v, want the restored position %v",
			got.Position, snap.Position)
	}
}

// TestInterpolationSmoothsDrawnMotion measures the point of the feature.
//
// It replays Run's accumulator at 144Hz against a 60Hz tick and records where
// a falling body would actually be *drawn* each frame. Without interpolation
// most frames repeat the previous position and the rest jump a whole tick's
// worth; with it, every frame advances by about the same amount.
func TestInterpolationSmoothsDrawnMotion(t *testing.T) {
	drawnDeltas := func(interp bool) []float64 {
		s := NewScene()
		s.Interpolate = interp
		// Constant velocity, no gravity: drawn motion should advance by the
		// same amount every frame. Under acceleration the per-tick step grows
		// on its own, which would inflate the variance whether or not
		// interpolation is working and make this measure nothing.
		s.Gravity = 0
		body := spawnBody(s, mgl32.Vec3{0, 0, 0})
		vel, _ := s.C.Velocity.Get(body)
		vel.Vec = mgl32.Vec3{5, 0, 0}

		tickDuration := time.Second / DefaultTickRate
		frameDelta := time.Second / 144
		var acc time.Duration

		var deltas []float64
		prevDrawn := float32(0)
		first := true
		for i := 0; i < 300; i++ {
			acc += frameDelta
			if max := 2 * tickDuration; acc > max {
				acc = max
			}
			for acc >= tickDuration {
				s.Tick(float32(tickDuration.Seconds()))
				acc -= tickDuration
			}
			alpha := float32(acc) / float32(tickDuration)

			tr, _ := s.InterpolatedTransform(body, alpha)
			if !first {
				deltas = append(deltas, math.Abs(float64(tr.Position.X()-prevDrawn)))
			}
			prevDrawn = tr.Position.X()
			first = false
		}
		return deltas
	}

	// Coefficient of variation: stddev/mean. Perfectly even motion is 0.
	cv := func(xs []float64) float64 {
		var sum float64
		for _, x := range xs {
			sum += x
		}
		mean := sum / float64(len(xs))
		var varsum float64
		for _, x := range xs {
			varsum += (x - mean) * (x - mean)
		}
		return math.Sqrt(varsum/float64(len(xs))) / mean
	}

	off := cv(drawnDeltas(false))
	on := cv(drawnDeltas(true))

	t.Logf("frame-to-frame drawn motion at 144fps / 60Hz tick:")
	t.Logf("  interpolation off: coefficient of variation %.3f", off)
	t.Logf("  interpolation on:  coefficient of variation %.3f", on)

	if off < 0.5 {
		t.Fatalf("expected stepped motion without interpolation (CV %.3f); test proves nothing", off)
	}
	// The residual is float32 precision in alpha: 144 and 60 do not divide
	// evenly, so each frame's blend factor carries a little rounding. A few
	// percent of variation is imperceptible; the stepping it replaced was not.
	if on > 0.10 {
		t.Errorf("interpolated motion is still uneven: CV %.3f, want < 0.10", on)
	}
	if ratio := off / on; ratio < 10 {
		t.Errorf("interpolation only improved smoothness %.1fx (%.3f -> %.3f), want at least 10x",
			ratio, off, on)
	}
}
