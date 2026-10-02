package renderer

// RenderStats counts what a frame actually submitted.
//
// Times say what costs; counts say why. A pass getting slower is either doing
// more work or doing the same work slower, and those want different fixes — the
// timer alone cannot tell them apart, which is how "grass is expensive" stays
// true for a year without anyone knowing whether it draws ten thousand blades or
// a million.
type RenderStats struct {
	// DrawCalls and Instances are submitted work: a single instanced draw of a
	// thousand blades is one call and a thousand instances.
	DrawCalls int
	Instances int

	// Triangles is instances times the mesh's triangle count, so it counts what
	// the vertex stage was asked for rather than what survived clipping.
	Triangles int

	// GrassTilesDrawn and GrassTilesCulled split the flora tiles by whether they
	// passed the distance and frustum tests. The ratio is the useful part: culling
	// almost nothing means the cull is not working, culling almost everything
	// means the scene is mostly out of view.
	GrassTilesDrawn  int
	GrassTilesCulled int

	// ShadowCasters is how many draws the shadow cascades rendered, summed over
	// cascades, so it can exceed the number of objects in the scene.
	ShadowCasters int

	// UploadsSkipped is how many draws this frame named a mesh whose streamed
	// upload had not completed yet, and so were not recorded at all. Steady
	// nonzero means geometry is being published faster than it can land; a
	// spike right after a burst of AllocAsync calls is the normal shape.
	//
	// Written after the recorder rather than by it: the counters are reset at
	// the start of recording, and the draws are dropped before it. See
	// Renderer.dropStreaming.
	UploadsSkipped int

	// App is the application passes' share of the counters above, so scene-only
	// figures are the difference: DrawCalls - App.DrawCalls is what the engine
	// itself submitted.
	App AppStats
}

// AppStats totals what application graphics and compute passes submitted, and
// attributes it per pass.
//
// Application draws used to be absent from RenderStats entirely, which made an
// expensive pass invisible in the counters read beside its own GPU timer: a
// forward-projected caustic atlas submitted one draw of 2,359,296 triangles
// before the scene and toggling it moved nothing. Counting it in the aggregate
// alone would have broken the other direction — a game watching DrawCalls for
// scene regressions cannot tell its own pass apart from the engine's — so both
// are here and the subtraction is exact.
type AppStats struct {
	DrawCalls int
	Instances int
	Triangles int

	// Dispatches counts compute work, which has no triangles and is not a draw:
	// it is deliberately absent from DrawCalls above and from RenderStats.
	Dispatches int

	// Passes is one entry per application pass, in frame-graph order. It is the
	// renderer's own storage, refilled in place every frame rather than rebuilt,
	// because a map or a fresh slice would allocate inside the recorder on every
	// frame of every game that has a single application pass. Read it before the
	// next DrawFrame, or copy what you keep.
	Passes []AppPassStats
}

// Pass returns the counts recorded for the application pass with this name, and
// false when no pass of that name ran. Names are unique: CreateAppPass and
// CreateAppCompute reject a duplicate precisely so this lookup is unambiguous.
func (a AppStats) Pass(name string) (AppPassStats, bool) {
	for _, p := range a.Passes {
		if p.Name == name {
			return p, true
		}
	}
	return AppPassStats{}, false
}

// AppPassStats is one application pass's submitted work, under the name the pass
// was created with. A disabled pass keeps its entry with zeroes rather than
// disappearing, so a HUD line does not move when an effect is switched off.
type AppPassStats struct {
	Name       string
	DrawCalls  int
	Instances  int
	Triangles  int
	Dispatches int
}

// reset zeroes the counters at the start of a frame and relabels the per-pass
// application entries from the graph's pass list.
//
// The slice is reused rather than reallocated: it only grows when the
// application creates more passes than it has ever had, which happens on
// CreateAppPass and not in the recorder. The zero-allocation tests hold this.
func (s *RenderStats) reset(apps []*AppPass) {
	passes := s.App.Passes[:0]
	*s = RenderStats{}
	for _, p := range apps {
		passes = append(passes, AppPassStats{Name: p.desc.Name})
	}
	s.App.Passes = passes
}

// addDraw records one draw call of n instances over a mesh with the given index
// or vertex count.
func (s *RenderStats) addDraw(instances, indexCount, vertexCount int) {
	instances, triangles := drawWork(instances, indexCount, vertexCount)
	s.DrawCalls++
	s.Instances += instances
	s.Triangles += triangles
}

// drawWork normalizes one draw's instance count and resolves its triangle count
// from whichever of the index and vertex counts the draw actually uses.
func drawWork(instances, indexCount, vertexCount int) (int, int) {
	if instances < 1 {
		instances = 1
	}
	verts := indexCount
	if verts == 0 {
		verts = vertexCount
	}
	return instances, instances * (verts / 3)
}

// addAppDraw records one application draw in the aggregate counters and again
// in the application block, charged to the pass at slot.
//
// A fullscreen pass arrives here too, as the one draw and the triangles it
// really submits: the vertex stage was asked for them, and a pass that is
// "only" a fullscreen triangle still shows up in DrawCalls because that is what
// the counters mean everywhere else.
func (s *RenderStats) addAppDraw(slot, instances, indexCount, vertexCount int) {
	instances, triangles := drawWork(instances, indexCount, vertexCount)
	s.DrawCalls++
	s.Instances += instances
	s.Triangles += triangles
	s.App.DrawCalls++
	s.App.Instances += instances
	s.App.Triangles += triangles
	if p := s.appPass(slot); p != nil {
		p.DrawCalls++
		p.Instances += instances
		p.Triangles += triangles
	}
}

// addAppDispatch records one compute dispatch against the pass at slot. A
// dispatch with a zero axis never reaches here: it submits no command, so it
// counts as no work. See AppCompute.active.
func (s *RenderStats) addAppDispatch(slot int) {
	s.App.Dispatches++
	if p := s.appPass(slot); p != nil {
		p.Dispatches++
	}
}

// appPass returns the per-pass entry for slot, or nil when there is none.
//
// Nil rather than a panic or a neighbour's entry: the slot is fixed when the
// frame graph is built and the labels come from the same build, so a mismatch
// means the recorder is running against stats it did not reset. Losing the
// attribution for that frame is the right failure; corrupting another pass's
// counts, or crashing the render thread, is not.
func (s *RenderStats) appPass(slot int) *AppPassStats {
	if slot < 0 || slot >= len(s.App.Passes) {
		return nil
	}
	return &s.App.Passes[slot]
}

// Stats returns the counters for the most recently recorded frame.
//
// Recorded rather than presented: these come from command buffer recording, so
// they describe what the frame asked the GPU to do, not what survived early-Z.
// Overdraw is deliberately not here — measuring it needs a GPU query the engine
// does not run, and a guessed number would be worse than none.
// GPU LOD commands are counted immediately, including empty indirect commands;
// their instance/triangle estimates use the last retired slot's counters.
//
// App.Passes points at the renderer's own storage and is refilled by the next
// DrawFrame; see AppStats.
func (r *Renderer) Stats() RenderStats { return r.stats }

// Indirect commands are counted even when empty. Their work estimate uses the
// last retired frame slot; reading current GPU counts here would serialize frames.
func (s *RenderStats) addInstanceDraw(set *InstanceSet, indices, vertices int) {
	if set.indirect.Handle() == 0 {
		s.addDraw(set.count, indices, vertices)
		return
	}
	if indices > 0 {
		vertices = indices
	}
	s.DrawCalls++
	s.Instances += set.count
	s.Triangles += set.count * (vertices / 3)
}
