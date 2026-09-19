package glyphengine

import (
	"sync"
	"testing"

	"github.com/go-gl/mathgl/mgl32"

	"github.com/derekmwright/glyphengine/ecs"
)

// countingQueryBackend delegates to the scene it was built for — Scene's own
// snapshot-aware implementations, not the exported Scene.Raycast/OverlapAABB
// (which would loop back into this backend) — while counting calls. It
// exists to prove that a given engine code path actually goes through
// Scene.Queries rather than reading the built-in implementation directly;
// a consumer nobody wired through the seam shows up as a permanent zero.
//
// The mutex is here because subtests call it from Camera.ResolveCollision and
// the parallel-movement test drives it from many goroutines at once — the
// counters themselves must not be the shared-scratch violation the interface
// warns callers of a *replacement* backend against introducing.
type countingQueryBackend struct {
	s *Scene

	mu           sync.Mutex
	raycastCalls int
	overlapCalls int
}

func (b *countingQueryBackend) Raycast(origin, dir mgl32.Vec3, maxDist float32, exclude ecs.Entity) (RayHit, bool) {
	b.mu.Lock()
	b.raycastCalls++
	b.mu.Unlock()
	return b.s.raycastBuiltin(origin, dir, maxDist, exclude)
}

func (b *countingQueryBackend) OverlapAABB(box AABB, exclude ecs.Entity) []OverlapResult {
	b.mu.Lock()
	b.overlapCalls++
	b.mu.Unlock()
	return b.s.overlapAABBBuiltin(box, exclude)
}

func (b *countingQueryBackend) counts() (raycasts, overlaps int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.raycastCalls, b.overlapCalls
}

// TestEveryInternalConsumerGoesThroughQueries drives each engine code path
// that calls Scene.Raycast or Scene.OverlapAABB — the list from grepping
// every ".Raycast(" and ".OverlapAABB(" call site in the engine — with a
// counting backend installed, and checks the count that consumer's own kind
// of query went up. A consumer that still called the built-in implementation
// directly instead of going through Scene.Queries would show a permanent
// zero here.
//
// Engine.PickEntity is the one call site not exercised directly: it is
// `return e.Raycast(...)` with Engine embedding *Scene (app.go), so it is the
// identical method call already proven to delegate by every other subtest
// here — exercising it for real needs a windowed Engine, which needs a GPU
// this suite must not use.
//
// Break experiment: reverting Scene.OverlapAABB's dispatch to call
// s.overlapAABBBuiltin unconditionally (dropping the "if s.Queries != nil"
// check) makes the MoveCharacter and Unstick subtests fail with overlapCalls
// == 0. Doing the same to Scene.Raycast makes the IntegrateBodies and
// CameraResolveCollision subtests fail with raycastCalls == 0.
func TestEveryInternalConsumerGoesThroughQueries(t *testing.T) {
	t.Run("IntegrateBodies", func(t *testing.T) {
		s := NewScene() // no Terrain: grounding always falls through to Raycast
		backend := &countingQueryBackend{s: s}
		s.Queries = backend

		spawnBody(s, mgl32.Vec3{0, 5, 0})
		s.Tick(1.0 / 60.0)

		raycasts, overlaps := backend.counts()
		if raycasts == 0 {
			t.Error("IntegrateBodies did not call Raycast through Scene.Queries")
		}
		if overlaps != 0 {
			t.Errorf("IntegrateBodies called OverlapAABB %d times; it never should", overlaps)
		}
	})

	t.Run("MoveCharacter", func(t *testing.T) {
		s := NewScene() // no Terrain, so ground detection's slow path runs Raycast
		backend := &countingQueryBackend{s: s}
		s.Queries = backend

		ch := spawnCharacter(s, mgl32.Vec3{0, 5, 0})
		s.MoveCharacter(ch, MoveIntent{Forward: 1}, 1.0/60.0)

		raycasts, overlaps := backend.counts()
		if raycasts == 0 {
			t.Error("MoveCharacter did not call Raycast through Scene.Queries")
		}
		if overlaps == 0 {
			t.Error("MoveCharacter did not call OverlapAABB through Scene.Queries")
		}
	})

	t.Run("Unstick", func(t *testing.T) {
		s := NewScene()
		backend := &countingQueryBackend{s: s}
		s.Queries = backend

		e := s.Spawn()
		s.C.Transform.Set(e, &Transform{Position: orderBase, Scale: mgl32.Vec3{1, 1, 1}})
		s.C.Collider.Set(e, &Collider{HalfExtents: mgl32.Vec3{0.5, 0.5, 0.5}})
		spawnStaticCollider(s, orderBase.Add(mgl32.Vec3{0.3, 0, 0}), mgl32.Vec3{0.5, 0.5, 0.5})

		s.Unstick(e)

		raycasts, overlaps := backend.counts()
		if overlaps == 0 {
			t.Error("Unstick did not call OverlapAABB through Scene.Queries")
		}
		if raycasts != 0 {
			t.Errorf("Unstick called Raycast %d times; it never should", raycasts)
		}
	})

	t.Run("CameraResolveCollision", func(t *testing.T) {
		s := NewScene()
		backend := &countingQueryBackend{s: s}
		s.Queries = backend

		wall := s.Spawn()
		s.C.Transform.Set(wall, &Transform{Position: mgl32.Vec3{0, 0, -3}, Scale: mgl32.Vec3{1, 1, 1}})
		s.C.Collider.Set(wall, &Collider{HalfExtents: mgl32.Vec3{5, 5, 0.5}})

		cam := NewCamera(10)
		cam.Target = mgl32.Vec3{0, 0, 0}
		cam.Yaw, cam.Pitch = 0, 0 // look straight at -Z, toward the wall

		// ResolveCollision takes a Raycaster, not a *Scene — this is the point
		// of the test: passing the Scene itself is what makes the seam reach
		// camera collision without camera.go knowing Scene.Queries exists.
		cam.ResolveCollision(s, 0, 1.0/60.0)

		raycasts, overlaps := backend.counts()
		if raycasts == 0 {
			t.Error("Camera.ResolveCollision did not call Raycast through Scene.Queries")
		}
		if overlaps != 0 {
			t.Errorf("Camera.ResolveCollision called OverlapAABB %d times; it never should", overlaps)
		}
	})
}

// scrambledQueryBackend answers every query with the same SET of results the
// built-in implementation would, but deliberately violates the #57 order
// contract before returning: OverlapAABB's results come back in descending
// entity id instead of ascending. It is the adversarial fake the issue asks
// for, to find out which engine consumer actually depends on the order
// rather than just asserting that one does.
type scrambledQueryBackend struct{ s *Scene }

func (b *scrambledQueryBackend) Raycast(origin, dir mgl32.Vec3, maxDist float32, exclude ecs.Entity) (RayHit, bool) {
	return b.s.raycastBuiltin(origin, dir, maxDist, exclude)
}

func (b *scrambledQueryBackend) OverlapAABB(box AABB, exclude ecs.Entity) []OverlapResult {
	results := b.s.overlapAABBBuiltin(box, exclude)
	for i, j := 0, len(results)-1; i < j; i, j = i+1, j-1 {
		results[i], results[j] = results[j], results[i]
	}
	return results
}

// TestUnstickToleratesScrambledOverlapOrder runs the same two-wall sandwich
// as TestUnstickResolvesTwoOverlapsDeterministically (physics_order_test.go)
// through scrambledQueryBackend and checks Unstick still lands on the exact
// same golden position.
//
// Finding, not assumed: Unstick's own post-#57 fix scans the whole overlap
// list and picks the smallest push with its own entity-id tie-break (see
// physics.go), so it never reads a positional index into the slice
// OverlapAABB hands back. Reversing that slice's order does not change what
// it computes. The engine's own use of OverlapAABB does not depend on the
// order contract — the contract is still required of a replacement (see
// QueryBackend) because OverlapAABB documents it to callers outside the
// engine, which is exactly the guarantee Unstick itself used to violate
// before #57.
func TestUnstickToleratesScrambledOverlapOrder(t *testing.T) {
	s := NewScene()
	s.Queries = &scrambledQueryBackend{s: s}

	e := s.Spawn()
	s.C.Transform.Set(e, &Transform{Position: orderBase, Scale: mgl32.Vec3{1, 1, 1}})
	s.C.Collider.Set(e, &Collider{HalfExtents: mgl32.Vec3{0.5, 0.5, 0.5}})
	spawnStaticCollider(s, orderBase.Add(mgl32.Vec3{-0.7, 0, 0}), mgl32.Vec3{0.5, 0.5, 0.5})
	spawnStaticCollider(s, orderBase.Add(mgl32.Vec3{0.7, 0, 0}), mgl32.Vec3{0.5, 0.5, 0.5})

	s.Unstick(e)

	tr, _ := s.C.Transform.Get(e)
	want := mgl32.Vec3{99.689995, 0, 100} // same golden value as TestUnstickResolvesTwoOverlapsDeterministically
	if tr.Position != want {
		t.Errorf("Unstick against a scrambled-order backend landed at %v, want %v (unchanged from ascending order)", tr.Position, want)
	}
}

// TestMoveCharacterToleratesScrambledOverlapOrder is the same finding against
// MoveCharacter's collision check, which is an OR over OverlapAABB's results
// ("does anything block this axis") rather than a positional read — so
// scrambling the order cannot change whether it decides the character is
// blocked.
func TestMoveCharacterToleratesScrambledOverlapOrder(t *testing.T) {
	s := NewScene()
	s.SetTerrain(flatTerrain(t, 0))
	s.Queries = &scrambledQueryBackend{s: s}

	wall := s.Spawn()
	s.C.Transform.Set(wall, &Transform{Position: mgl32.Vec3{0, 2, -3}, Scale: mgl32.Vec3{1, 1, 1}})
	s.C.Collider.Set(wall, &Collider{HalfExtents: mgl32.Vec3{3, 2, 0.5}})
	s.C.Static.Set(wall, &Static{})
	s.RebuildStatics()

	ch := spawnCharacter(s, mgl32.Vec3{0, 0.9, 0})

	const dt = 1.0 / 60.0
	for i := 0; i < 120; i++ {
		s.UpdateSpatialGrid()
		s.MoveCharacter(ch, MoveIntent{Forward: 1}, dt)
	}

	tr, _ := s.C.Transform.Get(ch)
	if tr.Position.Z() < -2.2 {
		t.Errorf("character passed through the wall to Z=%.3f with a scrambled-order backend", tr.Position.Z())
	}
}

// flippedTieRaycastBackend answers Raycast normally except that, when the
// winner is one of a known coincident pair, it reports the HIGHER of the two
// ids instead of the lower one the #57 contract requires — while staying
// deterministic call to call. It exists to separate two different things "the
// order contract matters" could mean: which specific entity wins a tie,
// versus whether the SAME entity wins every time for the same frozen state.
// See TestMoveCharacterWalkableTestToleratesADifferentTieBreakRule.
type flippedTieRaycastBackend struct {
	s     *Scene
	other ecs.Entity // the higher-id half of the coincident pair to prefer
}

func (b *flippedTieRaycastBackend) Raycast(origin, dir mgl32.Vec3, maxDist float32, exclude ecs.Entity) (RayHit, bool) {
	best, found := b.s.raycastBuiltin(origin, dir, maxDist, exclude)
	if !found || best.Entity == 0 || best.Entity == b.other {
		return best, found
	}
	if wb, ok := b.s.colliderAABB(b.other); ok {
		if dist, normal, hit := wb.Raycast(origin, dir, maxDist); hit && dist == best.T {
			best = RayHit{Entity: b.other, T: dist, Normal: normal, Point: best.Point}
		}
	}
	return best, found
}

func (b *flippedTieRaycastBackend) OverlapAABB(box AABB, exclude ecs.Entity) []OverlapResult {
	return b.s.overlapAABBBuiltin(box, exclude)
}

// TestMoveCharacterWalkableTestToleratesADifferentTieBreakRule stands a
// character on two exactly coincident static boxes (same trick as
// TestRaycastBreaksExactTiesOnEntityID) behind a backend that breaks the tie
// on the higher id instead of the lower one, and checks the character still
// grounds normally.
//
// Finding: MoveCharacter's walkable-slope test only reads hit.Normal, never
// hit.Entity, and both coincident boxes present the identical top-face
// normal — so which of the two entities wins is invisible to MoveCharacter
// here. What MoveCharacter actually needs is that the SAME answer comes back
// for the SAME frozen state on every call, which flippedTieRaycastBackend
// still provides (it is a pure function of the world, just a different pure
// function than the built-in). A backend whose tie-break were instead
// nondeterministic call to call — not just "a different fixed rule" — would
// make Grounded flicker tick to tick on this exact geometry; that failure
// mode is what the #57 contract guards against, documented loudly on
// QueryBackend rather than re-proven here since it would require injecting
// actual randomness to demonstrate.
func TestMoveCharacterWalkableTestToleratesADifferentTieBreakRule(t *testing.T) {
	s := NewScene()

	base := mgl32.Vec3{0, 0, 0}
	e1 := spawnStaticCollider(s, base, mgl32.Vec3{2, 0.5, 2})
	e2 := spawnStaticCollider(s, base, mgl32.Vec3{2, 0.5, 2})
	higher := e1
	if e2 > higher {
		higher = e2
	}
	s.Queries = &flippedTieRaycastBackend{s: s, other: higher}

	ch := spawnCharacter(s, mgl32.Vec3{0, 5, 0})
	const dt = 1.0 / 60.0
	for i := 0; i < 90; i++ {
		s.UpdateSpatialGrid()
		s.MoveCharacter(ch, MoveIntent{}, dt)
	}

	cc, _ := s.C.CharacterController.Get(ch)
	if !cc.Grounded {
		t.Error("character never grounded on two coincident boxes behind a flipped-tie-break backend")
	}
	// Boxes are 1 unit tall centered on Y=0 (top face at Y=0.5); the
	// character's collider half-height is 0.9, so its rest position is
	// 0.5 + 0.9 + the controller's 1mm ground bias.
	tr, _ := s.C.Transform.Get(ch)
	const wantY = 0.5 + 0.9 + 0.001
	if tr.Position.Y() < wantY-0.05 || tr.Position.Y() > wantY+0.5 {
		t.Errorf("character rest height = %.3f, want it standing on the boxes' top face (~%.3f)", tr.Position.Y(), wantY)
	}
}

// goodCustomQueryBackend is a minimal, independent broadphase replacement —
// no shared mutable state, a fresh slice allocated per OverlapAABB call, and
// it answers from the scene's snapshot-aware built-in implementation, the
// same way a replacement indexing its own copy of collider geometry rebuilt
// once per tick would satisfy the collision-snapshot contract "for free" (see
// QueryBackend's doc comment). It exists to prove a real custom backend can
// sit under MoveCharactersParallel and stay race-clean.
type goodCustomQueryBackend struct{ s *Scene }

func (b *goodCustomQueryBackend) Raycast(origin, dir mgl32.Vec3, maxDist float32, exclude ecs.Entity) (RayHit, bool) {
	return b.s.raycastBuiltin(origin, dir, maxDist, exclude)
}

func (b *goodCustomQueryBackend) OverlapAABB(box AABB, exclude ecs.Entity) []OverlapResult {
	src := b.s.overlapAABBBuiltin(box, exclude)
	out := make([]OverlapResult, len(src)) // fresh per call — no shared scratch
	copy(out, src)
	return out
}

// TestMoveCharactersParallelRaceWithCustomQueryBackend is
// TestMoveCharactersParallelRace (controller_race_test.go) with a custom
// Scene.Queries installed, run under -race. It is the concurrency half of the
// QueryBackend contract: the built-in snapshot machinery is Scene-internal
// (buildCollisionSnapshot / colliderAABB / useCollisionSnapshot), so nothing
// about it changes just because Scene.Queries is set — this only proves that
// routing every character's query through a *different* Go value under
// concurrent load does not itself introduce a race.
func TestMoveCharactersParallelRaceWithCustomQueryBackend(t *testing.T) {
	s := NewScene()
	s.SetTerrain(flatTerrain(t, 0))
	s.Queries = &goodCustomQueryBackend{s: s}

	const count = 64
	batch := make([]MoveBatchEntry, 0, count)
	for i := 0; i < count; i++ {
		x := float32(i%8) * 1.5
		z := float32(i/8) * 1.5
		e := spawnCharacter(s, mgl32.Vec3{x, 0.9, z})
		batch = append(batch, MoveBatchEntry{
			Entity: e,
			Intent: MoveIntent{Forward: 1, Right: float32(i%3) - 1, Yaw: float32(i) * 0.1},
		})
	}
	s.UpdateSpatialGrid()

	const dt = 1.0 / 60.0
	for tick := 0; tick < 20; tick++ {
		s.UpdateSpatialGrid()
		s.MoveCharactersParallel(batch, dt)
	}

	for _, entry := range batch {
		tr, _ := s.C.Transform.Get(entry.Entity)
		if tr.Position.Y() < -1 {
			t.Fatalf("entity %d fell through the terrain to Y=%.3f", entry.Entity, tr.Position.Y())
		}
	}
}
