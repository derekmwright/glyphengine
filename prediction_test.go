package glyphengine

import (
	"testing"

	"github.com/go-gl/mathgl/mgl32"

	"github.com/derekmwright/glyphengine/ecs"
)

// obstacleScene builds a world with terrain and static geometry, so replay is
// exercised against real collision rather than an empty plane.
func obstacleScene(t *testing.T) (*Scene, ecs.Entity) {
	t.Helper()
	s := NewScene()
	s.SetTerrain(flatTerrain(t, 0))

	// A wall and a pillar to slide along and bump into.
	wall := s.Spawn()
	s.C.Transform.Set(wall, &Transform{Position: mgl32.Vec3{0, 2, -6}, Scale: mgl32.Vec3{1, 1, 1}})
	s.C.Collider.Set(wall, &Collider{HalfExtents: mgl32.Vec3{5, 2, 0.5}})
	s.C.Static.Set(wall, &Static{})

	pillar := s.Spawn()
	s.C.Transform.Set(pillar, &Transform{Position: mgl32.Vec3{1.5, 2, -3}, Scale: mgl32.Vec3{1, 1, 1}})
	s.C.Collider.Set(pillar, &Collider{HalfExtents: mgl32.Vec3{0.6, 2, 0.6}})
	s.C.Static.Set(pillar, &Static{})

	s.RebuildStatics()

	ch := spawnCharacter(s, mgl32.Vec3{0, 0.9, 0})
	return s, ch
}

// scriptedIntents is a deterministic input sequence that exercises turning,
// strafing, sprinting, jumping, and running into things.
func scriptedIntents(n int) []MoveIntent {
	out := make([]MoveIntent, n)
	for i := range out {
		out[i] = MoveIntent{
			Forward: 1,
			Right:   float32((i/7)%3) - 1,
			Yaw:     float32(i) * 0.03,
			Sprint:  i%11 < 5,
			Jump:    i%23 == 0,
		}
	}
	return out
}

// TestReplayReproducesSimulationExactly is the property client-side prediction
// stands on: rewinding to a snapshot and replaying the same inputs must land
// on exactly the same state, bit for bit. Anything less and a client's
// prediction drifts from the server for reasons neither can explain.
func TestReplayReproducesSimulationExactly(t *testing.T) {
	const dt = 1.0 / 60.0
	intents := scriptedIntents(180)

	s, ch := obstacleScene(t)

	// Authoritative pass: snapshot at the start, then simulate.
	start, ok := s.SnapshotCharacter(ch)
	if !ok {
		t.Fatal("SnapshotCharacter failed on a character controller entity")
	}
	for _, in := range intents {
		s.UpdateSpatialGrid()
		s.MoveCharacter(ch, in, dt)
	}
	want, _ := s.SnapshotCharacter(ch)

	// Rewind and replay the identical inputs.
	if !s.RestoreCharacter(ch, start) {
		t.Fatal("RestoreCharacter failed")
	}
	for _, in := range intents {
		s.UpdateSpatialGrid()
		s.MoveCharacter(ch, in, dt)
	}
	got, _ := s.SnapshotCharacter(ch)

	if got != want {
		t.Errorf("replay diverged:\n  first pass: %+v\n  replay:     %+v", want, got)
	}
	t.Logf("180 ticks replayed exactly: pos=%v vel=%v grounded=%v",
		got.Position, got.Velocity, got.Grounded)
}

// TestReplayFromMidStreamMatches covers the shape reconciliation actually
// uses: the server confirms some tick partway through, and the client replays
// only the inputs after it.
func TestReplayFromMidStreamMatches(t *testing.T) {
	const dt = 1.0 / 60.0
	const confirmedAt = 60
	intents := scriptedIntents(150)

	s, ch := obstacleScene(t)

	var confirmed CharacterState
	for i, in := range intents {
		if i == confirmedAt {
			confirmed, _ = s.SnapshotCharacter(ch)
		}
		s.UpdateSpatialGrid()
		s.MoveCharacter(ch, in, dt)
	}
	want, _ := s.SnapshotCharacter(ch)

	// Rewind to the confirmed tick and replay the unacknowledged tail.
	if !s.RestoreCharacter(ch, confirmed) {
		t.Fatal("RestoreCharacter failed")
	}
	for _, in := range intents[confirmedAt:] {
		s.UpdateSpatialGrid()
		s.MoveCharacter(ch, in, dt)
	}
	got, _ := s.SnapshotCharacter(ch)

	if got != want {
		t.Errorf("mid-stream replay diverged:\n  original: %+v\n  replay:   %+v", want, got)
	}
}

// TestSnapshotCapturesEverythingMoveCharacterWrites guards against the state
// struct falling behind the controller. If MoveCharacter grows a new piece of
// persistent state and CharacterState does not, replay silently drifts — so
// restoring a snapshot must fully erase the effect of intervening simulation.
func TestSnapshotCapturesEverythingMoveCharacterWrites(t *testing.T) {
	const dt = 1.0 / 60.0
	s, ch := obstacleScene(t)

	// Drive it into a non-trivial state: airborne, turned, moving.
	for i := 0; i < 40; i++ {
		s.UpdateSpatialGrid()
		s.MoveCharacter(ch, MoveIntent{Forward: 1, Yaw: 0.9, Sprint: true, Jump: i == 0}, dt)
	}
	snap, _ := s.SnapshotCharacter(ch)

	// Diverge hard: run very different inputs for a while.
	for i := 0; i < 40; i++ {
		s.UpdateSpatialGrid()
		s.MoveCharacter(ch, MoveIntent{Forward: -1, Right: 1, Yaw: -2.0}, dt)
	}

	// Restoring must put it back exactly, so that continuing from here matches
	// continuing from the original snapshot.
	if !s.RestoreCharacter(ch, snap) {
		t.Fatal("RestoreCharacter failed")
	}
	if after, _ := s.SnapshotCharacter(ch); after != snap {
		t.Fatalf("restore did not round-trip:\n  saved:    %+v\n  restored: %+v", snap, after)
	}

	continued := func() CharacterState {
		for i := 0; i < 30; i++ {
			s.UpdateSpatialGrid()
			s.MoveCharacter(ch, MoveIntent{Forward: 1, Yaw: 0.2}, dt)
		}
		st, _ := s.SnapshotCharacter(ch)
		return st
	}
	first := continued()

	s.RestoreCharacter(ch, snap)
	second := continued()

	if first != second {
		t.Errorf("simulation after restore is not reproducible; CharacterState is missing state MoveCharacter depends on:\n  %+v\n  %+v",
			first, second)
	}
}

// TestSnapshotRejectsNonCharacters keeps the narrow contract honest.
func TestSnapshotRejectsNonCharacters(t *testing.T) {
	s := NewScene()

	body := spawnBody(s, mgl32.Vec3{0, 5, 0}) // Transform + Collider + Velocity, no controller
	if _, ok := s.SnapshotCharacter(body); ok {
		t.Error("SnapshotCharacter succeeded on an entity with no CharacterController")
	}
	if s.RestoreCharacter(body, CharacterState{}) {
		t.Error("RestoreCharacter succeeded on an entity with no CharacterController")
	}

	bare := s.Spawn()
	if _, ok := s.SnapshotCharacter(bare); ok {
		t.Error("SnapshotCharacter succeeded on an entity with no components")
	}
}
