package glyphengine

import (
	"sync"
	"testing"

	"github.com/go-gl/mathgl/mgl32"

	"github.com/derekmwright/glyphengine/ecs"
)

// frozenRay is one of the fixed rays the parity test fires at both backends.
type frozenRay struct {
	name    string
	origin  mgl32.Vec3
	dir     mgl32.Vec3
	maxDist float32
	exclude ecs.Entity
}

// frozenBox is one of the fixed overlap queries the parity test runs.
type frozenBox struct {
	name    string
	box     AABB
	exclude ecs.Entity
}

// frozenParityScene builds several hundred colliders on flat terrain: a 20x20
// lattice of static boxes, a cluster of five boxes that deliberately overlap
// each other, and one exactly coincident pair, whose ids it returns lower
// first. Coincidence is what makes the Raycast tie-break observable at all —
// two hits at exactly the same distance.
func frozenParityScene(t *testing.T) (*Scene, ecs.Entity, ecs.Entity) {
	t.Helper()

	s := NewScene()
	s.SetTerrain(flatTerrain(t, 0))

	for i := 0; i < 400; i++ {
		x := float32(i%20)*2 - 20
		z := float32(i/20)*2 - 20
		spawnStaticCollider(s, mgl32.Vec3{x, 0.5, z}, mgl32.Vec3{0.5, 0.5, 0.5})
	}
	// 0.4 apart with 0.75 half-extents: every box in the cluster overlaps its
	// neighbours, so one query box returns five results to put in order.
	for i := 0; i < 5; i++ {
		spawnStaticCollider(s, mgl32.Vec3{30 + float32(i)*0.4, 1, 30}, mgl32.Vec3{0.75, 0.75, 0.75})
	}
	lower := spawnStaticCollider(s, mgl32.Vec3{-30, 1, 30}, mgl32.Vec3{1, 1, 1})
	higher := spawnStaticCollider(s, mgl32.Vec3{-30, 1, 30}, mgl32.Vec3{1, 1, 1})

	s.UpdateSpatialGrid()
	s.RebuildStatics()
	return s, lower, higher
}

func frozenParityQueries(lower ecs.Entity) ([]frozenRay, []frozenBox) {
	rays := []frozenRay{
		// Straight down over a lattice box: the terrain fast path runs first
		// and the box then beats it on distance.
		{name: "down onto a box", origin: mgl32.Vec3{0, 10, 0}, dir: mgl32.Vec3{0, -1, 0}, maxDist: 20},
		// Straight down between the lattice boxes: terrain only.
		{name: "down onto terrain", origin: mgl32.Vec3{1, 10, 1}, dir: mgl32.Vec3{0, -1, 0}, maxDist: 20},
		// Horizontal into the overlapping cluster.
		{name: "into the cluster", origin: mgl32.Vec3{25, 1, 30}, dir: mgl32.Vec3{1, 0, 0}, maxDist: 20},
		// Horizontal into the coincident pair: both are hit at the same
		// distance, so the answer is the tie-break and nothing else.
		{name: "into the coincident pair", origin: mgl32.Vec3{-30, 1, 20}, dir: mgl32.Vec3{0, 0, 1}, maxDist: 20},
		// The same ray with the tie's winner excluded, so the loser answers.
		{name: "coincident pair, winner excluded", origin: mgl32.Vec3{-30, 1, 20}, dir: mgl32.Vec3{0, 0, 1}, maxDist: 20, exclude: lower},
		// Diagonal across the lattice, and a ray that hits nothing.
		{name: "diagonal across the lattice", origin: mgl32.Vec3{-25, 6, -25}, dir: mgl32.Vec3{0.7071, -0.7071, 0}, maxDist: 30},
		{name: "upward into empty air", origin: mgl32.Vec3{0, 40, 0}, dir: mgl32.Vec3{0, 1, 0}, maxDist: 5},
	}
	boxes := []frozenBox{
		{name: "over the cluster", box: AABB{Min: mgl32.Vec3{29, 0, 29}, Max: mgl32.Vec3{33, 2, 31}}},
		{name: "over the lattice", box: AABB{Min: mgl32.Vec3{-1, 0, -1}, Max: mgl32.Vec3{5, 1, 5}}},
		{name: "over the coincident pair", box: AABB{Min: mgl32.Vec3{-31, 0, 29}, Max: mgl32.Vec3{-29, 2, 31}}},
		{name: "over the coincident pair, one excluded", box: AABB{Min: mgl32.Vec3{-31, 0, 29}, Max: mgl32.Vec3{-29, 2, 31}}, exclude: lower},
		{name: "empty ground", box: AABB{Min: mgl32.Vec3{45, 0, 45}, Max: mgl32.Vec3{46, 1, 46}}},
	}
	return rays, boxes
}

// TestFrozenQueriesMatchBuiltinQueries fires a fixed set of rays and overlap
// boxes at FrozenQueries() and at BuiltinQueries() over the same unchanged
// scene, and requires identical answers — same hit, same results, same order.
// It then asserts the #57 contracts on the frozen answers directly: ascending
// entity ids out of OverlapAABB, and the lower id winning an exact Raycast
// tie.
//
// The direct assertions are the point, not belt and braces. FrozenQueries and
// BuiltinQueries deliberately share one implementation, so a change to the
// ordering rules moves both and the parity half stays green through it.
//
// Break experiment (2026-09-25): reversing the insertion sort in
// overlapAABBFrom (`results[i-1].Entity > entity` to `<`) leaves the parity
// subtest passing — both backends are that one sort — and fails the order
// subtest, which is why the order subtest exists:
//
//	--- FAIL: TestFrozenQueriesMatchBuiltinQueries/order/over_the_cluster
//	    query_frozen_test.go:140: FrozenQueries().OverlapAABB is not ascending
//	    by entity id: result 2 is entity 767 after 768
//
// The ids are checked as strictly increasing. They used to hold each Static
// entity twice — the built-in broad phase visited the moving grid and the
// static grid, and a Static entity is in both — which left only a strict
// decrease to test for; the broad phase now offers each entity once (#141), so
// a repeat is a failure like any other disorder.
//
// Break experiment (2026-09-25): removing the staticWalkProduces skip from
// eachBroadPhaseCandidate brings those duplicates back, and the strict check
// is what notices — the parity subtest stays green through it, since both
// backends are that one broad phase:
//
//	--- FAIL: TestFrozenQueriesMatchBuiltinQueries/order/over_the_cluster
//	    query_frozen_test.go:156: FrozenQueries().OverlapAABB is not strictly
//	    ascending by entity id: result 1 is entity 767 after 767
func TestFrozenQueriesMatchBuiltinQueries(t *testing.T) {
	s, lower, higher := frozenParityScene(t)
	rays, boxes := frozenParityQueries(lower)

	frozen := s.FrozenQueries()
	builtin := s.BuiltinQueries()

	t.Run("parity", func(t *testing.T) {
		for _, r := range rays {
			gotHit, gotOK := frozen.Raycast(r.origin, r.dir, r.maxDist, r.exclude)
			wantHit, wantOK := builtin.Raycast(r.origin, r.dir, r.maxDist, r.exclude)
			if gotOK != wantOK || gotHit != wantHit {
				t.Errorf("Raycast %q: frozen (%v, %v), builtin (%v, %v)", r.name, gotHit, gotOK, wantHit, wantOK)
			}
		}
		for _, b := range boxes {
			got := frozen.OverlapAABB(b.box, b.exclude)
			want := builtin.OverlapAABB(b.box, b.exclude)
			if len(got) != len(want) {
				t.Errorf("OverlapAABB %q: frozen returned %d results, builtin %d", b.name, len(got), len(want))
				continue
			}
			for i := range got {
				if got[i] != want[i] {
					t.Errorf("OverlapAABB %q: result %d is %v frozen, %v builtin", b.name, i, got[i], want[i])
				}
			}
		}
	})

	t.Run("order", func(t *testing.T) {
		for _, b := range boxes {
			b := b
			t.Run(b.name, func(t *testing.T) {
				results := frozen.OverlapAABB(b.box, b.exclude)
				for i := 1; i < len(results); i++ {
					if results[i].Entity <= results[i-1].Entity {
						t.Errorf("FrozenQueries().OverlapAABB is not strictly ascending by entity id: result %d is entity %d after %d",
							i, results[i].Entity, results[i-1].Entity)
					}
				}
			})
		}
	})

	t.Run("tie", func(t *testing.T) {
		r := rays[3] // into the coincident pair
		hit, ok := frozen.Raycast(r.origin, r.dir, r.maxDist, r.exclude)
		if !ok {
			t.Fatal("ray into the coincident pair missed")
		}
		if hit.Entity != lower {
			t.Errorf("exact tie went to entity %d, want the lower id %d (the other is %d)", hit.Entity, lower, higher)
		}
	})

	t.Run("terrain wins its own tie", func(t *testing.T) {
		// A downward ray over a box whose top face is exactly at the terrain
		// height would tie with terrain; terrain's RayHit carries entity 0,
		// which no real entity can undercut.
		flush := spawnStaticCollider(s, mgl32.Vec3{40, -0.5, 40}, mgl32.Vec3{0.5, 0.5, 0.5})
		s.UpdateSpatialGrid()
		s.RebuildStatics()

		refrozen := s.FrozenQueries()
		hit, ok := refrozen.Raycast(mgl32.Vec3{40, 10, 40}, mgl32.Vec3{0, -1, 0}, 20, 0)
		if !ok {
			t.Fatal("downward ray missed both terrain and the flush box")
		}
		if hit.Entity != 0 {
			t.Errorf("terrain lost its tie to entity %d (the flush box is %d)", hit.Entity, flush)
		}
	})
}

// TestFrozenQueriesIgnoreLaterWrites is the freeze itself: it takes a
// snapshot, then teleports a collider clean out of the query's way and
// despawns another, and requires the frozen backend to keep answering as the
// world was. BuiltinQueries over the same scene is checked to have noticed the
// same writes, so a frozen backend that quietly read live components would
// fail here rather than agree with it.
func TestFrozenQueriesIgnoreLaterWrites(t *testing.T) {
	s := NewScene()
	// Plain colliders, not Static ones: this test deliberately never rebuilds a
	// grid (see below), so the moving grid is the only broad phase it has and
	// the counts below stay about the freeze rather than about which grid
	// produced which candidate.
	spawnCollider := func(pos mgl32.Vec3) ecs.Entity {
		e := s.Spawn()
		s.C.Transform.Set(e, &Transform{Position: pos, Scale: mgl32.Vec3{1, 1, 1}})
		s.C.Collider.Set(e, &Collider{HalfExtents: mgl32.Vec3{1, 1, 1}})
		return e
	}
	mover := spawnCollider(mgl32.Vec3{0, 1, 0})
	doomed := spawnCollider(mgl32.Vec3{4, 1, 0})
	s.UpdateSpatialGrid()

	box := AABB{Min: mgl32.Vec3{-5, 0, -1}, Max: mgl32.Vec3{5, 2, 1}}
	frozen := s.FrozenQueries()
	before := frozen.OverlapAABB(box, 0)
	if len(before) != 2 {
		t.Fatalf("snapshot saw %d colliders in the box, want 2", len(before))
	}
	hitBefore, ok := frozen.Raycast(mgl32.Vec3{0, 10, 0}, mgl32.Vec3{0, -1, 0}, 20, 0)
	if !ok || hitBefore.Entity != mover {
		t.Fatalf("snapshot raycast hit (%v, %v), want entity %d", hitBefore, ok, mover)
	}

	tr, _ := s.C.Transform.Get(mover)
	tr.Position = mgl32.Vec3{0, 1, 40}
	s.World().Despawn(doomed)
	// The spatial grid is deliberately NOT rebuilt here. It is the broad phase,
	// not the snapshot: rebuilding it would drop both entities out of the cells
	// the query visits, and the frozen backend would answer "nothing" for a
	// reason that has nothing to do with the freeze. Rebuilding a grid while a
	// snapshot is in use is what the docs tell a game not to do.

	if got := frozen.OverlapAABB(box, 0); len(got) != 2 || got[0] != before[0] || got[1] != before[1] {
		t.Errorf("frozen OverlapAABB changed after the writes: %v, want %v", got, before)
	}
	if hit, ok := frozen.Raycast(mgl32.Vec3{0, 10, 0}, mgl32.Vec3{0, -1, 0}, 20, 0); !ok || hit != hitBefore {
		t.Errorf("frozen Raycast changed after the writes: (%v, %v), want %v", hit, ok, hitBefore)
	}

	// The control: the live backend must disagree, or the writes above never
	// happened and the assertions are vacuous.
	if got := s.BuiltinQueries().OverlapAABB(box, 0); len(got) != 0 {
		t.Errorf("live OverlapAABB still sees %d colliders in the box; the writes did not land", len(got))
	}
	if hit, ok := s.BuiltinQueries().Raycast(mgl32.Vec3{0, 10, 0}, mgl32.Vec3{0, -1, 0}, 20, 0); ok && hit.Entity == mover {
		t.Error("live Raycast still hits the moved collider; the write did not land")
	}
}

// TestFrozenQueriesUnderConcurrentTransformWrites is the concurrency half of
// the contract, and the reason the snapshot exists: one goroutine writes
// Transforms the way MoveCharacter does while several others query the frozen
// backend. Run under -race (task test:race).
//
// It checks answers as well as the detector: every reader compares against the
// answer captured before the writer started, so a backend reading live
// components would both race and drift.
//
// Break experiment (2026-09-25): swapping frozen for s.BuiltinQueries() in the
// readers — the live backend, since no parallel phase is running — fails
// immediately under -race, inside the query rather than anywhere near the
// test's own bookkeeping:
//
//	WARNING: DATA RACE
//	Read at 0x00c000447860 by goroutine 1175:
//	  glyphengine.WorldAABB()                      physics.go:50
//	  glyphengine.(*Scene).colliderAABBFrom()      physics.go:168
//	  glyphengine.(*Scene).overlapAABBFrom.func1() physics.go:364
//	  ...
//	  glyphengine.builtinQueries.OverlapAABB()     physics.go:255
//	  ...UnderConcurrentTransformWrites.func2()    query_frozen_test.go:317
//	Previous write at 0x00c000447864 by goroutine 1172:
//	  ...UnderConcurrentTransformWrites.func1()    query_frozen_test.go:305
func TestFrozenQueriesUnderConcurrentTransformWrites(t *testing.T) {
	s := NewScene()
	s.SetTerrain(flatTerrain(t, 0))

	const count = 64
	movers := make([]ecs.Entity, 0, count)
	for i := 0; i < count; i++ {
		e := s.Spawn()
		s.C.Transform.Set(e, &Transform{Position: mgl32.Vec3{float32(i%8) * 1.5, 1, float32(i/8) * 1.5}, Scale: mgl32.Vec3{1, 1, 1}})
		s.C.Collider.Set(e, &Collider{HalfExtents: mgl32.Vec3{0.5, 0.5, 0.5}})
		movers = append(movers, e)
	}
	s.UpdateSpatialGrid()

	box := AABB{Min: mgl32.Vec3{-1, 0, -1}, Max: mgl32.Vec3{6, 2, 6}}
	origin := mgl32.Vec3{3, 10, 3}
	down := mgl32.Vec3{0, -1, 0}

	frozen := s.FrozenQueries()
	wantOverlaps := frozen.OverlapAABB(box, 0)
	wantHit, wantOK := frozen.Raycast(origin, down, 20, 0)
	if !wantOK || len(wantOverlaps) == 0 {
		t.Fatalf("fixture queries answered nothing: hit %v, %d overlaps", wantOK, len(wantOverlaps))
	}

	// The engine's own phase enables store locking around concurrent
	// component access; mirror it so this test exercises the same shape.
	s.World().EnableLocking()
	defer s.World().DisableLocking()

	done := make(chan struct{})
	var writer, readersWG sync.WaitGroup

	writer.Add(1)
	go func() {
		defer writer.Done()
		for i := 0; ; i++ {
			select {
			case <-done:
				return
			default:
			}
			for _, e := range movers {
				tr, _ := s.C.Transform.Get(e)
				tr.Position[1] = 1 + float32(i%17)
			}
		}
	}()

	const readers = 4
	errs := make(chan string, readers)
	readersWG.Add(readers)
	for r := 0; r < readers; r++ {
		go func() {
			defer readersWG.Done()
			for i := 0; i < 200; i++ {
				got := frozen.OverlapAABB(box, 0)
				if len(got) != len(wantOverlaps) {
					errs <- "OverlapAABB result count changed while Transforms were being written"
					return
				}
				for j := range got {
					if got[j] != wantOverlaps[j] {
						errs <- "OverlapAABB result changed while Transforms were being written"
						return
					}
				}
				if hit, ok := frozen.Raycast(origin, down, 20, 0); ok != wantOK || hit != wantHit {
					errs <- "Raycast answer changed while Transforms were being written"
					return
				}
			}
		}()
	}

	readersWG.Wait()
	close(done) // the writer runs until the readers are finished with it
	writer.Wait()

	close(errs)
	for msg := range errs {
		t.Error(msg)
	}
}

// parallelPinScene builds the scenario TestMoveCharactersParallelPositionsPinned
// replays: twelve characters on flat terrain walking into a static wall, packed
// close enough that they also block each other. It exercises both queries the
// snapshot answers — the grounding raycast and the per-axis overlap check —
// from inside the parallel phase.
func parallelPinScene(t *testing.T) (*Scene, []MoveBatchEntry) {
	t.Helper()

	s := NewScene()
	s.SetTerrain(flatTerrain(t, 0))

	wall := s.Spawn()
	s.C.Transform.Set(wall, &Transform{Position: mgl32.Vec3{0, 1, -4}, Scale: mgl32.Vec3{1, 1, 1}})
	s.C.Collider.Set(wall, &Collider{HalfExtents: mgl32.Vec3{10, 1, 0.5}})
	s.C.Static.Set(wall, &Static{})
	s.RebuildStatics()

	const count = 12
	batch := make([]MoveBatchEntry, 0, count)
	for i := 0; i < count; i++ {
		e := spawnCharacter(s, mgl32.Vec3{float32(i)*1.1 - 6, 0.9, float32(i%3) * 1.1})
		batch = append(batch, MoveBatchEntry{
			Entity: e,
			Intent: MoveIntent{Forward: 1, Right: float32(i%3) - 1, Yaw: float32(i) * 0.17},
		})
	}
	s.UpdateSpatialGrid()
	return s, batch
}

// TestMoveCharactersParallelPositionsPinned pins two seconds of the parallel
// phase to the exact positions the code produced BEFORE the phase was moved
// onto FrozenQueries, so the export cannot quietly change what a server
// simulates. The golden values below were captured from the unchanged phase
// (buildCollisionSnapshot + the useCollisionSnapshot flag) and are compared
// exactly: each character's movement is a fixed sequence of float32 operations
// against a frozen world, so a run that takes the same path is bit-identical.
//
// The pin is also insensitive to the two things that legitimately vary between
// runs, both confirmed by capture: the number of workers
// (GOMAXPROCS=3 matches the default) and the entity ids, which come from a
// process-global counter and differ between a filtered run and a whole-package
// run. Neither moved a single component.
//
// Break experiment (2026-09-25): making buildCollisionSnapshot leave
// useCollisionSnapshot false — the phase freezing a snapshot and then querying
// live components anyway, which is the mistake this refactor could have made —
// fails on six of the twelve characters, the ones with a neighbour close
// enough to matter:
//
//	--- FAIL: TestMoveCharactersParallelPositionsPinned
//	    query_frozen_test.go:420: character 2 ended at
//	    [-1.6459179 0.90099996 -3.0943098], want the pinned
//	    [-1.6459179 0.90099996 -2.4926844]
func TestMoveCharactersParallelPositionsPinned(t *testing.T) {
	want := []mgl32.Vec3{
		{-11.656835, 0.90099996, -4.33692},
		{-6.2534356, 0.90099996, -3.0394583},
		{-1.6459179, 0.90099996, -2.4926844},
		{-9.628695, 0.90099996, -2.15731},
		{-5.414681, 0.90099996, -3.0988915},
		{-0.84860885, 0.90099996, -2.190822},
		{-6.726968, 0.90099996, 1.6426883},
		{-3.0656292, 0.90099996, -1.8484991},
		{-0.8233395, 0.90099996, -3.060295},
		{-1.8357916, 0.90099996, 5.4214296},
		{-2.1399908, 0.90099996, 2.122167},
		{-0.7961212, 0.90099996, -1.382362},
	}

	s, batch := parallelPinScene(t)

	const dt = 1.0 / 60.0
	for tick := 0; tick < 120; tick++ {
		s.UpdateSpatialGrid()
		s.MoveCharactersParallel(batch, dt)
	}

	for i, entry := range batch {
		tr, _ := s.C.Transform.Get(entry.Entity)
		if tr.Position != want[i] {
			t.Errorf("character %d ended at %v, want the pinned %v", i, tr.Position, want[i])
		}
	}
}
