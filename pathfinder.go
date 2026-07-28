package glyphengine

import "sync"

// PathRequest is a queued pathfinding request.
type PathRequest struct {
	ID       uint64
	From, To Vec2
	Callback func([]Vec2)
}

// PathFinder processes path requests in an amortized queue, computing up to
// maxPerTick paths per Tick so a burst of requests spreads its CPU cost across
// frames instead of spiking one.
//
// Request and Cancel are safe to call from any goroutine — AI systems commonly
// request paths off the tick. Tick itself is not: it invokes callbacks that
// typically write ECS components, so it must run on the goroutine that owns
// the world. Scene.Tick calls it for you.
type PathFinder struct {
	grid       *NavGrid
	maxPerTick int
	maxNodes   int // A* node budget per path

	mu     sync.Mutex
	queue  []PathRequest
	nextID uint64
}

// NewPathFinder creates a PathFinder backed by the given NavGrid.
func NewPathFinder(grid *NavGrid, maxPerTick int) *PathFinder {
	return &PathFinder{
		grid:       grid,
		maxPerTick: maxPerTick,
		maxNodes:   2000,
	}
}

// Request enqueues a path request and returns its ID. The callback runs on a
// future Tick with the resulting path, which is nil when no path exists.
func (pf *PathFinder) Request(from, to Vec2, callback func([]Vec2)) uint64 {
	pf.mu.Lock()
	defer pf.mu.Unlock()

	pf.nextID++
	pf.queue = append(pf.queue, PathRequest{
		ID:       pf.nextID,
		From:     from,
		To:       to,
		Callback: callback,
	})
	return pf.nextID
}

// Cancel removes a pending request by ID. It is a no-op if the request has
// already been processed.
func (pf *PathFinder) Cancel(id uint64) {
	pf.mu.Lock()
	defer pf.mu.Unlock()

	for i := range pf.queue {
		if pf.queue[i].ID == id {
			pf.queue = append(pf.queue[:i], pf.queue[i+1:]...)
			return
		}
	}
}

// Tick processes up to maxPerTick queued path requests, invoking each
// callback on the calling goroutine.
func (pf *PathFinder) Tick() {
	// Detach the batch under the lock, then solve outside it: A* is the
	// expensive part and callbacks may re-Request, which would deadlock.
	pf.mu.Lock()
	n := pf.maxPerTick
	if n > len(pf.queue) {
		n = len(pf.queue)
	}
	if n == 0 {
		pf.mu.Unlock()
		return
	}
	batch := make([]PathRequest, n)
	copy(batch, pf.queue[:n])
	pf.queue = pf.queue[n:]
	pf.mu.Unlock()

	for _, req := range batch {
		path := pf.grid.FindPath(req.From.X, req.From.Z, req.To.X, req.To.Z, pf.maxNodes)
		if path != nil {
			path = pf.grid.SmoothPath(path)
		}
		req.Callback(path)
	}
}

// Pending returns the number of queued path requests.
func (pf *PathFinder) Pending() int {
	pf.mu.Lock()
	defer pf.mu.Unlock()
	return len(pf.queue)
}
