package glyphengine

import (
	"math"
	"strings"
	"sync"
	"testing"
	"time"
)

// gridFromASCII builds a NavGrid from a picture, so a test's obstacles are
// visible in the test rather than assembled by index arithmetic.
//
// '.' is walkable, '#' is blocked. Row 0 is z=0, so the picture reads the way
// the grid is indexed.
func gridFromASCII(t *testing.T, rows ...string) *NavGrid {
	t.Helper()
	if len(rows) == 0 {
		t.Fatal("empty grid")
	}
	w := len(rows[0])
	g := &NavGrid{
		Width:    w,
		Height:   len(rows),
		CellSize: 1,
		Walkable: make([]bool, w*len(rows)),
	}
	for z, row := range rows {
		if len(row) != w {
			t.Fatalf("row %d is %d wide, want %d", z, len(row), w)
		}
		for x, c := range row {
			switch c {
			case '.':
				g.Walkable[z*w+x] = true
			case '#':
			default:
				t.Fatalf("row %d: unexpected %q", z, c)
			}
		}
	}
	return g
}

// cell returns the world coordinate of a grid cell's centre.
func cell(g *NavGrid, gx, gz int) (float32, float32) { return g.GridToWorld(gx, gz) }

// pathCells converts a world-space path back to grid cells.
func pathCells(g *NavGrid, path []Vec2) [][2]int {
	out := make([][2]int, len(path))
	for i, p := range path {
		x, z := g.WorldToGrid(p.X, p.Z)
		out[i] = [2]int{x, z}
	}
	return out
}

func TestFindPathWalksOnlyWalkableCells(t *testing.T) {
	g := gridFromASCII(t,
		"..........",
		"..#####...",
		"..#...#...",
		"..#.#.#...",
		"....#.....",
		"#####.....",
		"..........",
	)

	fx, fz := cell(g, 0, 0)
	tx, tz := cell(g, 9, 6)
	path := g.FindPath(fx, fz, tx, tz, 10000)
	if path == nil {
		t.Fatal("no path found through a grid that has one")
	}

	cells := pathCells(g, path)
	for i, c := range cells {
		if !g.IsWalkable(c[0], c[1]) {
			t.Errorf("step %d at (%d,%d) is not walkable", i, c[0], c[1])
		}
	}

	// Consecutive steps must be adjacent — a path that teleports is not a path.
	for i := 1; i < len(cells); i++ {
		dx := cells[i][0] - cells[i-1][0]
		dz := cells[i][1] - cells[i-1][1]
		if dx < -1 || dx > 1 || dz < -1 || dz > 1 || (dx == 0 && dz == 0) {
			t.Errorf("step %d jumps from (%d,%d) to (%d,%d)",
				i, cells[i-1][0], cells[i-1][1], cells[i][0], cells[i][1])
		}
	}

	if cells[0] != [2]int{0, 0} {
		t.Errorf("path starts at %v, want (0,0)", cells[0])
	}
	if last := cells[len(cells)-1]; last != [2]int{9, 6} {
		t.Errorf("path ends at %v, want (9,6)", last)
	}
}

func TestFindPathReturnsNilWhenWalledOff(t *testing.T) {
	g := gridFromASCII(t,
		"....#....",
		"....#....",
		"....#....",
		"....#....",
		"....#....",
	)
	fx, fz := cell(g, 0, 2)
	tx, tz := cell(g, 8, 2)
	if path := g.FindPath(fx, fz, tx, tz, 10000); path != nil {
		t.Errorf("found a path through a solid wall: %v", pathCells(g, path))
	}
}

// TestNoCornerCutting is the one geometric rule the search has to enforce.
//
// Two blockers meeting at a corner leave a diagonal gap between them. A grid
// path may not slip through it: in a real level those are two walls meeting,
// and a character that takes the diagonal walks through the join. The search
// guards this by requiring both orthogonal neighbours of a diagonal step to be
// walkable.
func TestNoCornerCutting(t *testing.T) {
	// The gap between (1,1) and (2,2) is diagonal only.
	g := gridFromASCII(t,
		"....",
		".#..",
		"..#.",
		"....",
	)

	fx, fz := cell(g, 1, 2)
	tx, tz := cell(g, 2, 1)
	path := g.FindPath(fx, fz, tx, tz, 10000)
	if path == nil {
		t.Fatal("no path at all; expected one going around")
	}

	cells := pathCells(g, path)
	// The illegal move is (1,2) straight to (2,1): a diagonal whose two
	// orthogonal neighbours (2,2) and (1,1) are both blocked.
	for i := 1; i < len(cells); i++ {
		a, b := cells[i-1], cells[i]
		dx, dz := b[0]-a[0], b[1]-a[1]
		if dx != 0 && dz != 0 {
			if !g.IsWalkable(a[0]+dx, a[1]) && !g.IsWalkable(a[0], a[1]+dz) {
				t.Errorf("step %d cuts the corner from (%d,%d) to (%d,%d)",
					i, a[0], a[1], b[0], b[1])
			}
		}
	}
}

// dijkstraCost is an independent shortest-path oracle: no heuristic, no
// ordering tricks, just relax every cell until nothing improves.
//
// Comparing A* against a second implementation rather than against a closed
// form is what lets this run on grids with obstacles, where there is no
// formula for the answer — and obstacles are exactly where a bad heuristic
// starts returning longer paths.
func dijkstraCost(g *NavGrid, sx, sz, ex, ez int) float64 {
	const inf = math.MaxFloat64
	dist := make([]float64, g.Width*g.Height)
	for i := range dist {
		dist[i] = inf
	}
	dist[sz*g.Width+sx] = 0

	for changed := true; changed; {
		changed = false
		for z := 0; z < g.Height; z++ {
			for x := 0; x < g.Width; x++ {
				d := dist[z*g.Width+x]
				if d == inf || !g.IsWalkable(x, z) {
					continue
				}
				for dz := -1; dz <= 1; dz++ {
					for dx := -1; dx <= 1; dx++ {
						if dx == 0 && dz == 0 {
							continue
						}
						nx, nz := x+dx, z+dz
						if !g.IsWalkable(nx, nz) {
							continue
						}
						// Same corner rule the search uses, or the oracle
						// would allow routes the search correctly refuses.
						if dx != 0 && dz != 0 {
							if !g.IsWalkable(x+dx, z) || !g.IsWalkable(x, z+dz) {
								continue
							}
						}
						step := 1.0
						if dx != 0 && dz != 0 {
							step = math.Sqrt2
						}
						if nd := d + step; nd < dist[nz*g.Width+nx]-1e-9 {
							dist[nz*g.Width+nx] = nd
							changed = true
						}
					}
				}
			}
		}
	}
	return dist[ez*g.Width+ex]
}

// TestPathIsOptimal checks the search returns the shortest route, not merely a
// route.
//
// The heuristic is Euclidean while movement is 8-connected at cost 1 and √2.
// Euclidean never overestimates octile distance, so it is admissible — and for
// this metric it is also consistent, which together with the search re-opening
// nodes on a shorter g means the result is optimal.
//
// Worth recording what this does and does not catch, because it is less than
// the name suggests. Scaling the heuristic by 3 and by 10, giving diagonals the
// wrong cost, and disabling the re-open branch were all tried, and all still
// produced optimal paths: the implementation is robust to each for the reason
// above. What it does catch is a search that returns a suboptimal route at all
// — injecting a one-cell detour fails it immediately.
//
// The oracle is an independent Dijkstra rather than a closed form so this can
// run over obstacle grids, where there is no formula for the answer.
func TestPathIsOptimal(t *testing.T) {
	grids := map[string][]string{
		"open": {
			"..........",
			"..........",
			"..........",
			"..........",
			"..........",
			"..........",
			"..........",
			"..........",
		},
		"detour": {
			"..........",
			"..........",
			".########.",
			".#........",
			".#.#######",
			".#.......#",
			".#########",
			"..........",
		},
		"scattered": {
			"..#.......",
			"..#..##...",
			"..#..##...",
			".....##...",
			"###.......",
			"....#####.",
			".##.......",
			"..........",
		},
	}

	cases := []struct{ ax, az, bx, bz int }{
		{0, 0, 9, 7},
		{0, 0, 9, 0},
		{0, 0, 5, 5},
		{9, 7, 1, 2},
	}

	for name, rows := range grids {
		g := gridFromASCII(t, rows...)
		for _, c := range cases {
			if !g.IsWalkable(c.ax, c.az) || !g.IsWalkable(c.bx, c.bz) {
				continue
			}
			want := dijkstraCost(g, c.ax, c.az, c.bx, c.bz)
			if math.IsInf(want, 1) || want == math.MaxFloat64 {
				continue // unreachable on this grid; covered elsewhere
			}

			fx, fz := cell(g, c.ax, c.az)
			tx, tz := cell(g, c.bx, c.bz)
			path := g.FindPath(fx, fz, tx, tz, 1000000)
			if path == nil {
				t.Errorf("%s (%d,%d)->(%d,%d): no path, oracle says %.3f",
					name, c.ax, c.az, c.bx, c.bz, want)
				continue
			}

			got := pathLengthCells(pathCells(g, path))
			if math.Abs(got-want) > 1e-3 {
				t.Errorf("%s (%d,%d)->(%d,%d): path costs %.3f, shortest is %.3f",
					name, c.ax, c.az, c.bx, c.bz, got, want)
			}
		}
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func pathLengthCells(cells [][2]int) float64 {
	var total float64
	for i := 1; i < len(cells); i++ {
		dx := abs(cells[i][0] - cells[i-1][0])
		dz := abs(cells[i][1] - cells[i-1][1])
		if dx != 0 && dz != 0 {
			total += math.Sqrt2
		} else {
			total += 1
		}
	}
	return total
}

func TestFindPathRejectsBlockedEndpoints(t *testing.T) {
	g := gridFromASCII(t,
		"...",
		".#.",
		"...",
	)
	bx, bz := cell(g, 1, 1)
	ox, oz := cell(g, 0, 0)

	if p := g.FindPath(bx, bz, ox, oz, 1000); p != nil {
		t.Error("path found starting inside a blocked cell")
	}
	if p := g.FindPath(ox, oz, bx, bz, 1000); p != nil {
		t.Error("path found ending inside a blocked cell")
	}
}

func TestFindPathSameCellReturnsSinglePoint(t *testing.T) {
	g := gridFromASCII(t, "...", "...", "...")
	x, z := cell(g, 1, 1)
	path := g.FindPath(x, z, x, z, 1000)
	if len(path) != 1 {
		t.Fatalf("same-cell path has %d points, want 1", len(path))
	}
}

// TestFindPathRespectsNodeBudget checks the search gives up rather than
// exploring an entire large grid, which is what keeps one bad request from
// stalling a frame.
func TestFindPathRespectsNodeBudget(t *testing.T) {
	rows := make([]string, 60)
	for i := range rows {
		rows[i] = strings.Repeat(".", 60)
	}
	g := gridFromASCII(t, rows...)

	fx, fz := cell(g, 0, 0)
	tx, tz := cell(g, 59, 59)

	if p := g.FindPath(fx, fz, tx, tz, 10); p != nil {
		t.Errorf("a 10-node budget produced a path across a 60x60 grid (%d steps)", len(p))
	}
	if p := g.FindPath(fx, fz, tx, tz, 100000); p == nil {
		t.Error("a generous budget found no path on an open grid")
	}
}

// TestSmoothPathKeepsLineOfSight is the property that makes smoothing safe.
//
// Smoothing deletes waypoints, and deleting the wrong one cuts the corner off
// an obstacle the original path went around. Every remaining segment therefore
// has to have clear line of sight, or the character walks through a wall
// between two points that are each individually fine.
func TestSmoothPathKeepsLineOfSight(t *testing.T) {
	g := gridFromASCII(t,
		"............",
		"............",
		"...######...",
		"...#....#...",
		"...#....#...",
		"...######...",
		"............",
		"............",
	)

	fx, fz := cell(g, 0, 0)
	tx, tz := cell(g, 11, 7)
	path := g.FindPath(fx, fz, tx, tz, 100000)
	if path == nil {
		t.Fatal("no path around the obstacle")
	}

	smoothed := g.SmoothPath(path)
	if len(smoothed) > len(path) {
		t.Errorf("smoothing lengthened the path: %d -> %d", len(path), len(smoothed))
	}
	if smoothed[0] != path[0] {
		t.Error("smoothing moved the start")
	}
	if smoothed[len(smoothed)-1] != path[len(path)-1] {
		t.Error("smoothing moved the end")
	}
	for i := 1; i < len(smoothed); i++ {
		if !g.lineOfSight(smoothed[i-1], smoothed[i]) {
			a, b := smoothed[i-1], smoothed[i]
			t.Errorf("smoothed segment %d has no line of sight: (%.1f,%.1f)->(%.1f,%.1f)",
				i, a.X, a.Z, b.X, b.Z)
		}
	}
}

func TestSmoothPathShortensAStraightRun(t *testing.T) {
	g := gridFromASCII(t,
		"..........",
		"..........",
		"..........",
	)
	fx, fz := cell(g, 0, 1)
	tx, tz := cell(g, 9, 1)
	path := g.FindPath(fx, fz, tx, tz, 100000)
	if len(path) < 3 {
		t.Fatalf("expected a multi-step path, got %d", len(path))
	}
	if smoothed := g.SmoothPath(path); len(smoothed) != 2 {
		t.Errorf("a clear straight line smoothed to %d points, want 2", len(smoothed))
	}
}

// ─────────────────────────── async PathFinder ───────────────────────────

func TestPathFinderRespectsPerTickBudget(t *testing.T) {
	g := gridFromASCII(t, "....", "....", "....", "....")
	pf := NewPathFinder(g, 2)

	var done int
	var mu sync.Mutex
	for i := 0; i < 5; i++ {
		fx, fz := cell(g, 0, 0)
		tx, tz := cell(g, 3, 3)
		pf.Request(Vec2{fx, fz}, Vec2{tx, tz}, func([]Vec2) {
			mu.Lock()
			done++
			mu.Unlock()
		})
	}
	if pf.Pending() != 5 {
		t.Fatalf("Pending() = %d after 5 requests", pf.Pending())
	}

	pf.Tick()
	if done != 2 {
		t.Errorf("first tick solved %d requests, want the 2-per-tick budget", done)
	}
	if pf.Pending() != 3 {
		t.Errorf("Pending() = %d after one tick, want 3", pf.Pending())
	}

	pf.Tick()
	pf.Tick()
	if done != 5 {
		t.Errorf("after three ticks %d of 5 requests are done", done)
	}
	if pf.Pending() != 0 {
		t.Errorf("Pending() = %d once drained", pf.Pending())
	}
}

func TestPathFinderCancel(t *testing.T) {
	g := gridFromASCII(t, "..", "..")
	pf := NewPathFinder(g, 10)

	fx, fz := cell(g, 0, 0)
	tx, tz := cell(g, 1, 1)

	fired := false
	id := pf.Request(Vec2{fx, fz}, Vec2{tx, tz}, func([]Vec2) { fired = true })
	pf.Cancel(id)
	pf.Tick()

	if fired {
		t.Error("callback ran for a cancelled request")
	}
	if pf.Pending() != 0 {
		t.Errorf("Pending() = %d after cancelling the only request", pf.Pending())
	}

	// Cancelling something already gone must not panic or disturb the queue.
	pf.Cancel(id)
	pf.Cancel(9999)
}

// TestPathFinderCallbackCanRequest guards the reason Tick copies its batch out
// from under the lock before solving.
//
// Queueing a follow-up path from inside a callback is the natural way to write
// a patrol or a chase. If Tick held the mutex while running callbacks, that
// would deadlock instantly — and the test would hang rather than fail, so it
// runs on a timer.
func TestPathFinderCallbackCanRequest(t *testing.T) {
	g := gridFromASCII(t, "....", "....", "....", "....")
	pf := NewPathFinder(g, 4)

	fx, fz := cell(g, 0, 0)
	tx, tz := cell(g, 3, 3)

	var second bool
	pf.Request(Vec2{fx, fz}, Vec2{tx, tz}, func([]Vec2) {
		pf.Request(Vec2{tx, tz}, Vec2{fx, fz}, func([]Vec2) { second = true })
	})

	done := make(chan struct{})
	go func() {
		pf.Tick() // runs the first callback, which enqueues the second
		pf.Tick() // runs the second
		close(done)
	}()

	select {
	case <-done:
	case <-timeoutAfterSeconds(5):
		t.Fatal("Tick deadlocked: a callback that calls Request must not block")
	}

	if !second {
		t.Error("the follow-up request never ran")
	}
}

// timeoutAfterSeconds is a small helper so the deadlock test above reads as a
// deadlock check rather than as timer plumbing.
func timeoutAfterSeconds(n int) <-chan time.Time {
	return time.After(time.Duration(n) * time.Second)
}
