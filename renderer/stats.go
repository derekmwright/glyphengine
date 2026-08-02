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
}

// reset zeroes the counters at the start of a frame.
func (s *RenderStats) reset() { *s = RenderStats{} }

// addDraw records one draw call of n instances over a mesh with the given index
// or vertex count.
func (s *RenderStats) addDraw(instances, indexCount, vertexCount int) {
	if instances < 1 {
		instances = 1
	}
	verts := indexCount
	if verts == 0 {
		verts = vertexCount
	}
	s.DrawCalls++
	s.Instances += instances
	s.Triangles += instances * (verts / 3)
}

// Stats returns the counters for the most recently recorded frame.
//
// Recorded rather than presented: these come from command buffer recording, so
// they describe what the frame asked the GPU to do, not what survived early-Z.
// Overdraw is deliberately not here — measuring it needs a GPU query the engine
// does not run, and a guessed number would be worse than none.
func (r *Renderer) Stats() RenderStats { return r.stats }
