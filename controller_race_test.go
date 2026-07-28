package glyphengine

import (
	"sync"
	"testing"

	"github.com/go-gl/mathgl/mgl32"
)

// TestMoveCharactersParallelRace drives the parallel movement phase with many
// characters packed close enough that their broad-phase queries overlap, so
// every goroutine is reading neighbors that other goroutines are writing.
//
// Run with -race. Without the collision snapshot and store locking this trips
// the detector on Transform and Velocity.
func TestMoveCharactersParallelRace(t *testing.T) {
	s := NewScene()
	s.SetTerrain(flatTerrain(t, 0))

	const count = 64
	batch := make([]MoveBatchEntry, 0, count)
	for i := 0; i < count; i++ {
		// A tight grid: 1.5 units apart, so AABB queries overlap neighbors.
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

	// Every character should have left its spawn point.
	for _, entry := range batch {
		tr, _ := s.C.Transform.Get(entry.Entity)
		if tr.Position.Y() < -1 {
			t.Fatalf("entity %d fell through the terrain to Y=%.3f", entry.Entity, tr.Position.Y())
		}
	}
}

// TestMoveCharactersParallelSamePlayerIsSequential packs the SAME entity into
// one batch twice, which is what happens when a client's frame clock delivers
// two inputs inside one tick window.
//
// Work is partitioned by entity hash precisely so both land on one goroutine
// and apply in order. A positional split would run them concurrently — a data
// race on that entity's own Transform that silently loses a frame of movement.
func TestMoveCharactersParallelSamePlayerIsSequential(t *testing.T) {
	s := NewScene()
	s.SetTerrain(flatTerrain(t, 0))

	ch := spawnCharacter(s, mgl32.Vec3{0, 0.9, 0})

	// Two forward intents for the same entity in one batch, plus filler
	// entities so the batch is wide enough to fan out across workers.
	batch := []MoveBatchEntry{
		{Entity: ch, Intent: MoveIntent{Forward: 1}},
		{Entity: ch, Intent: MoveIntent{Forward: 1}},
	}
	for i := 0; i < 16; i++ {
		e := spawnCharacter(s, mgl32.Vec3{float32(20 + i*3), 0.9, 40})
		batch = append(batch, MoveBatchEntry{Entity: e, Intent: MoveIntent{Forward: 1}})
	}
	s.UpdateSpatialGrid()

	const dt = 1.0 / 60.0
	s.MoveCharactersParallel(batch, dt)

	// Both intents must have applied: one step is walkSpeed*dt toward -Z.
	tr, _ := s.C.Transform.Get(ch)
	cc, _ := s.C.CharacterController.Get(ch)
	oneStep := cc.WalkSpeed * dt
	got := -tr.Position.Z()

	if got < oneStep*1.5 {
		t.Errorf("moved %.4f, want about %.4f (two intents); one was dropped", got, oneStep*2)
	}
}

// TestPathFinderRequestIsConcurrencySafe hammers Request from many goroutines
// while Tick drains the queue. Run with -race.
func TestPathFinderRequestIsConcurrencySafe(t *testing.T) {
	grid := &NavGrid{
		Width:    32,
		Height:   32,
		CellSize: 1,
		OriginX:  0,
		OriginZ:  0,
		Walkable: make([]bool, 32*32),
	}
	for i := range grid.Walkable {
		grid.Walkable[i] = true
	}
	pf := NewPathFinder(grid, 8)

	var wg sync.WaitGroup
	const writers = 8
	const perWriter = 50

	wg.Add(writers)
	for w := 0; w < writers; w++ {
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				pf.Request(Vec2{X: 1, Z: 1}, Vec2{X: 20, Z: 20}, func([]Vec2) {})
			}
		}(w)
	}

	// Drain concurrently with the writers until they are all done.
	writersDone := make(chan struct{})
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for {
			pf.Tick()
			select {
			case <-writersDone:
				return
			default:
			}
		}
	}()

	wg.Wait()
	close(writersDone)
	<-drained

	// Drain the remainder; every request must resolve.
	for pf.Pending() > 0 {
		pf.Tick()
	}
}

// TestPathFinderIDsAreUnique checks that concurrent Requests never hand out a
// duplicate ID, which Cancel relies on.
func TestPathFinderIDsAreUnique(t *testing.T) {
	pf := NewPathFinder(nil, 1)

	const goroutines = 8
	const perGoroutine = 100

	ids := make([][]uint64, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			defer wg.Done()
			local := make([]uint64, 0, perGoroutine)
			for i := 0; i < perGoroutine; i++ {
				local = append(local, pf.Request(Vec2{}, Vec2{}, func([]Vec2) {}))
			}
			ids[g] = local
		}(g)
	}
	wg.Wait()

	seen := make(map[uint64]bool, goroutines*perGoroutine)
	for _, batch := range ids {
		for _, id := range batch {
			if seen[id] {
				t.Fatalf("duplicate path request ID %d", id)
			}
			seen[id] = true
		}
	}
	if len(seen) != goroutines*perGoroutine {
		t.Errorf("got %d unique IDs, want %d", len(seen), goroutines*perGoroutine)
	}
}
