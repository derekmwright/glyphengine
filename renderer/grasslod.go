package renderer

// GrassLOD is how grass thins, fades and culls with distance.
//
// Every field here used to be a package constant, which meant tuning grass for
// a particular game required editing the engine. They are the knobs that matter
// most for grass performance and they are the ones most likely to need moving:
// a dense meadow viewed from a walking camera and a sparse steppe viewed from
// horseback want very different numbers, and neither is the engine's business to
// decide.
//
// Grass is the most expensive thing the renderer draws -- 2.5 of kitchensink's
// 3.1 ms -- so these are also the knobs with the most performance to give. See
// docs/agents/grass.md for what each one costs.
//
//	lod := renderer.DefaultGrassLOD()
//	lod.MaxDistance = 120   // see further
//	lod.ThinMin = 0.2       // but thin harder to pay for it
//	r.SetGrassLOD(lod)
//
// Set it at any time; it takes effect on the next frame. Nothing is rebuilt, so
// this is cheap enough to drive from a settings menu or a quality preset.
type GrassLOD struct {
	// ThinNear is the distance out to which every blade is drawn, and ThinFar
	// where thinning reaches its floor. Between them the fraction of instances
	// submitted falls linearly to ThinMin.
	//
	// This is a CPU-side cull: thinned blades are never submitted, so it saves
	// the whole cost of a blade rather than just its shading. It is also the
	// most visible knob -- thinning too aggressively reads as grass popping in
	// as you walk toward it.
	ThinNear float32
	ThinFar  float32

	// ThinMin is the density floor, as a fraction of full. Zero would clear
	// distant grass entirely, which looks worse than fewer blades: the ground
	// texture underneath is not a substitute for a thin scatter.
	ThinMin float32

	// MaxDistance is the hard cull. Blades past it are discarded in the vertex
	// shader before any work, and tiles entirely past it are never submitted.
	MaxDistance float32

	// FadeStart is where blades begin shrinking toward zero height, reaching
	// nothing at MaxDistance. The fade runs through alpha-to-coverage rather
	// than alpha blending, so distant blades dissolve through MSAA coverage
	// instead of shrinking into shimmering sub-pixel slivers.
	//
	// Keep it meaningfully below MaxDistance. Equal values make grass vanish at
	// a hard ring, which is far more noticeable than the fade it replaces.
	FadeStart float32
}

// DefaultGrassLOD is the engine's tuning: the values that were constants before
// this was configurable, so adopting it changes nothing.
func DefaultGrassLOD() GrassLOD {
	return GrassLOD{
		ThinNear:    30,
		ThinFar:     70,
		ThinMin:     0.35,
		MaxDistance: 80,
		FadeStart:   50,
	}
}

// keepFraction is the share of a tile's instances to submit at a given
// distance. Split out so it can be checked without a device.
func (l GrassLOD) keepFraction(dist float32) float32 {
	if dist <= l.ThinNear {
		return 1
	}
	if dist >= l.ThinFar {
		return l.ThinMin
	}
	span := l.ThinFar - l.ThinNear
	if span <= 0 {
		return l.ThinMin
	}
	t := (dist - l.ThinNear) / span
	return 1 - t*(1-l.ThinMin)
}

// sanitised returns the LOD with any nonsensical combination repaired.
//
// A zero or partly-filled value is a legitimate thing for a caller to construct
// by accident -- GrassLOD{MaxDistance: 200} is an easy and reasonable thing to
// write -- and the failure mode is grass that silently does not draw at all.
// Repairing beats both ignoring it and returning an error from a setter nobody
// will check.
//
// The rule is uniform: a distance at or below zero means "unset, use the
// default". That costs the ability to express "start thinning at the camera"
// with a literal 0, which is the price of a zero value that means the defaults;
// use a small positive number for that.
func (l GrassLOD) sanitised() GrassLOD {
	d := DefaultGrassLOD()
	if l.MaxDistance <= 0 {
		l.MaxDistance = d.MaxDistance
	}
	if l.FadeStart <= 0 || l.FadeStart >= l.MaxDistance {
		// Fade over the last third by default rather than snapping to the
		// engine's 50, which would be wrong for any MaxDistance but 80.
		l.FadeStart = l.MaxDistance * 0.625
	}
	if l.ThinFar <= 0 {
		l.ThinFar = d.ThinFar
	}
	if l.ThinNear <= 0 {
		l.ThinNear = d.ThinNear
	}
	if l.ThinNear > l.ThinFar {
		// A near past the far inverts the ramp, which reads as grass getting
		// denser with distance. Collapse to a step at ThinFar instead.
		l.ThinNear = l.ThinFar
	}
	if l.ThinMin <= 0 || l.ThinMin > 1 {
		l.ThinMin = d.ThinMin
	}
	return l
}

// SetGrassLOD replaces the grass distance tuning. Takes effect next frame.
func (r *Renderer) SetGrassLOD(l GrassLOD) { r.grassLOD = l.sanitised() }

// GrassLOD returns the current grass distance tuning.
func (r *Renderer) GrassLOD() GrassLOD { return r.grassLOD }
