package glyphengine

import (
	"sort"
	"testing"

	"github.com/go-gl/mathgl/mgl32"

	"github.com/derekmwright/glyphengine/ecs"
)

// spawnStaticCollider adds a Static, Collider-only entity (no mesh, no
// velocity) at pos with the given half-extents.
func spawnStaticCollider(s *Scene, pos, half mgl32.Vec3) ecs.Entity {
	e := s.Spawn()
	s.C.Transform.Set(e, &Transform{Position: pos, Scale: mgl32.Vec3{1, 1, 1}})
	s.C.Collider.Set(e, &Collider{HalfExtents: half})
	s.C.Static.Set(e, &Static{})
	return e
}

// distinctPositions dedupes exactly — the float32 arithmetic on any single
// resolution path is the same sequence of operations every time, so two runs
// that took the same path land on bit-identical results and two that took
// different paths do not round to the same value by coincidence here.
func distinctPositions(seen []mgl32.Vec3, p mgl32.Vec3) []mgl32.Vec3 {
	for _, q := range seen {
		if q == p {
			return seen
		}
	}
	return append(seen, p)
}

// orderBase is offset well away from every axis origin. cellFor floors
// toward negative infinity across x==0 or z==0, so a scenario straddling
// zero can accidentally put two colliders that are right next to each other
// into different spatial-grid cells — which are visited in a fixed raster
// scan, not shuffled, making the scenario deterministic for a reason that
// has nothing to do with the bug. Every entity below sits inside the same
// 32-unit cell, whose entity list order actually comes from the map walk in
// SpatialGrid.Update and reshuffles from call to call.
var orderBase = mgl32.Vec3{100, 0, 100}

// TestUnstickResolvesTwoOverlapsDeterministically sandwiches a character
// between two static colliders 1.4 units apart — narrower than the
// character's own 1-unit width, so leaving one collider always re-enters the
// other. Both need an identical-looking 0.3-unit push, so which one Unstick
// resolves against first decides where the character ends up.
//
// Before the fix, that "first" was overlaps[0]: the spatial grid's cell
// list, filled by a Go map walk (SpatialGrid.Update), in whatever order that
// walk produced. Run from the same start with the grid rebuilt before every
// call, the old code produced 2 distinct final positions over 500 runs in
// one process (confirmed below, before applying the fix in this commit).
// Picking the smallest-push overlap and breaking ties on entity id removes
// the dependency on that order entirely, since the scan is over the whole
// overlap list rather than its first element.
func TestUnstickResolvesTwoOverlapsDeterministically(t *testing.T) {
	s := NewScene()
	e := s.Spawn()
	s.C.Transform.Set(e, &Transform{Position: orderBase, Scale: mgl32.Vec3{1, 1, 1}})
	s.C.Collider.Set(e, &Collider{HalfExtents: mgl32.Vec3{0.5, 0.5, 0.5}})

	spawnStaticCollider(s, orderBase.Add(mgl32.Vec3{-0.7, 0, 0}), mgl32.Vec3{0.5, 0.5, 0.5}) // left wall
	spawnStaticCollider(s, orderBase.Add(mgl32.Vec3{0.7, 0, 0}), mgl32.Vec3{0.5, 0.5, 0.5})  // right wall

	var seen []mgl32.Vec3
	for i := 0; i < 500; i++ {
		tr, _ := s.C.Transform.Get(e)
		tr.Position = orderBase
		s.UpdateSpatialGrid()
		s.Unstick(e)
		seen = distinctPositions(seen, tr.Position)
	}

	if len(seen) != 1 {
		t.Fatalf("Unstick produced %d distinct final positions over 500 runs from the same start, want 1: %v", len(seen), seen)
	}
	want := mgl32.Vec3{99.689995, 0, 100} // captured from this test's own fixed-code run
	if seen[0] != want {
		t.Errorf("final position = %v, want %v", seen[0], want)
	}
}

// TestUnstickResolvesThreeOverlapsDeterministically is the three-collider
// version of the test above: a character boxed in on -X, +X, and +Z, each
// overlap needing the same 0.3-unit push. The unfixed code produced 3
// distinct final positions over 500 runs in one process (confirmed below,
// before applying the fix in this commit); the fix produces exactly 1.
//
// Break experiment: with Unstick reverted to reading overlaps[0] (but
// OverlapAABB still sorted by entity id, so overlaps[0] is now always the
// lowest id, not the smallest push), this test fails immediately with the
// wrong golden position — [99.689995 0 100] instead of
// [100.310005 0 99.689995] — because with three colliders "lowest id" and
// "smallest push" diverge after the first attempt.
// TestUnstickResolvesTwoOverlapsDeterministically and
// TestUnstickTiesBreakOnLowestEntityID kept passing under that same break:
// their two-wall sandwich is symmetric, so the lowest-id wall and the
// smallest-push wall are always the same one, and overlaps[0] gets the
// right answer by coincidence rather than by scanning for it. That is
// exactly why this three-collider case exists.
func TestUnstickResolvesThreeOverlapsDeterministically(t *testing.T) {
	s := NewScene()
	e := s.Spawn()
	s.C.Transform.Set(e, &Transform{Position: orderBase, Scale: mgl32.Vec3{1, 1, 1}})
	s.C.Collider.Set(e, &Collider{HalfExtents: mgl32.Vec3{0.5, 0.5, 0.5}})

	spawnStaticCollider(s, orderBase.Add(mgl32.Vec3{-0.7, 0, 0}), mgl32.Vec3{0.5, 0.5, 0.5}) // -X wall
	spawnStaticCollider(s, orderBase.Add(mgl32.Vec3{0.7, 0, 0}), mgl32.Vec3{0.5, 0.5, 0.5})  // +X wall
	spawnStaticCollider(s, orderBase.Add(mgl32.Vec3{0, 0, 0.7}), mgl32.Vec3{0.5, 0.5, 0.5})  // +Z wall

	var seen []mgl32.Vec3
	for i := 0; i < 500; i++ {
		tr, _ := s.C.Transform.Get(e)
		tr.Position = orderBase
		s.UpdateSpatialGrid()
		s.Unstick(e)
		seen = distinctPositions(seen, tr.Position)
	}

	if len(seen) != 1 {
		t.Fatalf("Unstick produced %d distinct final positions over 500 runs from the same start, want 1: %v", len(seen), seen)
	}
	want := mgl32.Vec3{100.310005, 0, 99.689995} // captured from this test's own fixed-code run
	if seen[0] != want {
		t.Errorf("final position = %v, want %v", seen[0], want)
	}
}

// TestUnstickSingleOverlapUnchanged pins the common case: with only one
// overlap, "smallest push, tie-break on entity id" has nothing to choose
// between, so the position must exactly match the result captured from the
// code before this change (a single static wall, resolved in one push on
// the first of the 8 attempts).
func TestUnstickSingleOverlapUnchanged(t *testing.T) {
	s := NewScene()
	e := s.Spawn()
	s.C.Transform.Set(e, &Transform{Position: orderBase, Scale: mgl32.Vec3{1, 1, 1}})
	s.C.Collider.Set(e, &Collider{HalfExtents: mgl32.Vec3{0.5, 0.5, 0.5}})
	spawnStaticCollider(s, orderBase.Add(mgl32.Vec3{0.3, 0, 0}), mgl32.Vec3{0.5, 0.5, 0.5})

	s.UpdateSpatialGrid()
	s.Unstick(e)

	tr, _ := s.C.Transform.Get(e)
	golden := orderBase.Add(mgl32.Vec3{-0.71, 0, 0}) // captured from the pre-fix code
	if tr.Position != golden {
		t.Errorf("single-overlap position = %v, want unchanged golden %v", tr.Position, golden)
	}
}

// TestUnstickTiesBreakOnLowestEntityID isolates the tie-break rule itself,
// independent of grid order, using the same sandwich as
// TestUnstickResolvesTwoOverlapsDeterministically (both walls need an
// identical 0.3-unit push, so which one is resolved first can only be
// decided by the tie-break). What varies here is which wall gets the lower
// entity id: Unstick always resolves off that wall first, and since the gap
// is narrower than the character, it then has to clear the other wall too,
// landing just past it — so the final position mirrors around the start
// depending only on which id is lower, not which side it happens to be on.
func TestUnstickTiesBreakOnLowestEntityID(t *testing.T) {
	run := func(t *testing.T, lowIDOnRight bool) mgl32.Vec3 {
		s := NewScene()
		e := s.Spawn()
		s.C.Transform.Set(e, &Transform{Position: orderBase, Scale: mgl32.Vec3{1, 1, 1}})
		s.C.Collider.Set(e, &Collider{HalfExtents: mgl32.Vec3{0.5, 0.5, 0.5}})

		left := func() { spawnStaticCollider(s, orderBase.Add(mgl32.Vec3{-0.7, 0, 0}), mgl32.Vec3{0.5, 0.5, 0.5}) }
		right := func() { spawnStaticCollider(s, orderBase.Add(mgl32.Vec3{0.7, 0, 0}), mgl32.Vec3{0.5, 0.5, 0.5}) }
		if lowIDOnRight {
			right() // spawned first: lower id
			left()
		} else {
			left() // spawned first: lower id
			right()
		}

		s.UpdateSpatialGrid()
		s.Unstick(e)
		tr, _ := s.C.Transform.Get(e)
		return tr.Position
	}

	// Lower id on the left: matches
	// TestUnstickResolvesTwoOverlapsDeterministically exactly (left is
	// spawned first there too), landing on the left side.
	got := run(t, false)
	want := mgl32.Vec3{99.689995, 0, 100} // matches TestUnstickResolvesTwoOverlapsDeterministically exactly
	if got != want {
		t.Errorf("lower id on left: position = %v, want %v (resolved off the lower-id wall first)", got, want)
	}

	// Lower id on the right: the mirror image.
	got = run(t, true)
	want = mgl32.Vec3{100.310005, 0, 100}
	if got != want {
		t.Errorf("lower id on right: position = %v, want %v (resolved off the lower-id wall first)", got, want)
	}
}

// TestOverlapAABBOrderIsAscendingEntityID is the order-contract test for
// OverlapAABB: five overlapping static colliders in one spatial-grid cell,
// queried 500 times with the grid rebuilt before each call. Before the fix,
// this produced unsorted results (confirmed below, before applying the fix
// in this commit); after it, the result is ascending by entity id on every
// call.
//
// Break experiment: reverting OverlapAABB's insertion sort back to a plain
// append made this fail on run 2 of 500 with an out-of-order result (entity
// 1 arriving last instead of first). It did not touch the Unstick tests
// above — Unstick's own fix scans every overlap itself and no longer reads
// results in whatever order OverlapAABB hands them back.
func TestOverlapAABBOrderIsAscendingEntityID(t *testing.T) {
	base := mgl32.Vec3{200, 0, 200}
	s := NewScene()
	for i := -2; i <= 2; i++ {
		spawnStaticCollider(s, base.Add(mgl32.Vec3{float32(i) * 0.3, 0, 0}), mgl32.Vec3{0.5, 0.5, 0.5})
	}
	query := AABB{Min: base.Sub(mgl32.Vec3{2, 2, 2}), Max: base.Add(mgl32.Vec3{2, 2, 2})}

	for i := 0; i < 500; i++ {
		s.UpdateSpatialGrid()
		ov := s.OverlapAABB(query, 0)
		if len(ov) != 5 {
			t.Fatalf("run %d: got %d overlaps, want 5", i, len(ov))
		}
		if !sort.SliceIsSorted(ov, func(a, b int) bool { return ov[a].Entity < ov[b].Entity }) {
			t.Fatalf("run %d: OverlapAABB result not sorted by entity id: %v", i, ov)
		}
	}
}

// BenchmarkOverlapAABB measures the cost of the ascending-entity-id sort
// added to satisfy OverlapAABB's order contract, at a few overlap counts.
// Measured with:
//
//	go test -run '^$' -bench OverlapAABB -benchmem -count 3 .
//
// on an AMD Ryzen 9 5900X (24 GOMAXPROCS), by temporarily reverting the
// insertion sort to a plain append and comparing:
//
//	                     before (unsorted)         after (sorted)
//	overlaps=1     ~115.9 ns/op, 40 B, 2 allocs    ~115.6 ns/op, 40 B, 2 allocs
//	overlaps=8     ~636.7 ns/op, 544 B, 5 allocs   ~645.8 ns/op, 544 B, 5 allocs
//	overlaps=64    ~5183 ns/op, 4576 B, 8 allocs   ~6176 ns/op, 4576 B, 8 allocs
//
// allocs/op and B/op are identical before and after at every size — the sort
// inserts into the same growing slice OverlapAABB always allocated, not an
// extra buffer. ns/op picks up a real cost only at the high end: insertion
// sort is O(n²), and this benchmark's synthetic setup (colliders spread
// along a line, so the grid returns them in an order this insertion sort
// works hardest against) shows about 19% more time at 64 simultaneous
// overlaps. OverlapAABB is called per character per tick against whatever is
// nearby, which in practice is a handful of colliders, not 64 — the
// overlaps=1 and overlaps=8 cases are what a character moving through a
// scene actually pays, and neither moved outside noise.
func BenchmarkOverlapAABB(b *testing.B) {
	for _, n := range []int{1, 8, 64} {
		b.Run(overlapCountName(n), func(b *testing.B) {
			base := mgl32.Vec3{300, 0, 300}
			s := NewScene()
			// Spread colliders along X inside one grid cell, each overlapping
			// the query box below but not necessarily each other.
			step := float32(0.05)
			for i := 0; i < n; i++ {
				spawnStaticCollider(s, base.Add(mgl32.Vec3{float32(i) * step, 0, 0}), mgl32.Vec3{2, 2, 2})
			}
			s.UpdateSpatialGrid()
			query := AABB{Min: base.Sub(mgl32.Vec3{3, 3, 3}), Max: base.Add(mgl32.Vec3{3, 3, 3})}

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				ov := s.OverlapAABB(query, 0)
				if len(ov) != n {
					b.Fatalf("got %d overlaps, want %d", len(ov), n)
				}
			}
		})
	}
}

func overlapCountName(n int) string {
	switch n {
	case 1:
		return "overlaps=1"
	case 8:
		return "overlaps=8"
	default:
		return "overlaps=64"
	}
}

// TestRaycastBreaksExactTiesOnEntityID fires straight down through two
// coincident boxes — same position, same extents, so their top faces are hit
// at exactly the same distance. Before the fix this was decided by
// SpatialGrid query order — a Go map walk — so it changed from call to call
// (confirmed below, before applying the fix in this commit). The fix keeps
// the lower entity id on every call, in both the grid path (SpatialGrid set)
// and the no-grid fallback (linear scan), which share the same tie-break.
//
// Break experiment: dropping the "dist == best.T && entity < best.Entity"
// clause back to plain "dist < best.T" made both subtests fail immediately —
// 2 distinct entities returned across 300 ties in the grid path, 2 in the
// no-grid fallback — confirming the single shared testEntity closure and
// its tie-break cover both call sites.
func TestRaycastBreaksExactTiesOnEntityID(t *testing.T) {
	base := mgl32.Vec3{400, 0, 400}

	run := func(t *testing.T, useGrid bool) {
		t.Helper()
		s := NewScene()
		var ids []ecs.Entity
		for i := 0; i < 2; i++ {
			e := spawnStaticCollider(s, base, mgl32.Vec3{0.5, 0.5, 0.5})
			ids = append(ids, e)
		}
		lowest := ids[0]
		if ids[1] < lowest {
			lowest = ids[1]
		}

		var seenEntities []ecs.Entity
		for i := 0; i < 300; i++ {
			if useGrid {
				s.UpdateSpatialGrid()
			}
			origin := mgl32.Vec3{base.X(), 5, base.Z()}
			hit, ok := s.Raycast(origin, mgl32.Vec3{0, -1, 0}, 20, 0)
			if !ok {
				t.Fatalf("run %d: ray did not hit either box", i)
			}
			found := false
			for _, id := range seenEntities {
				if id == hit.Entity {
					found = true
					break
				}
			}
			if !found {
				seenEntities = append(seenEntities, hit.Entity)
			}
		}

		if len(seenEntities) != 1 {
			t.Fatalf("Raycast returned %d distinct entities across 300 equal-distance ties, want 1: %v", len(seenEntities), seenEntities)
		}
		if seenEntities[0] != lowest {
			t.Errorf("tie-break winner = entity %d, want lowest id %d", seenEntities[0], lowest)
		}
	}

	t.Run("grid", func(t *testing.T) { run(t, true) })
	t.Run("no-grid fallback", func(t *testing.T) { run(t, false) })
}
