package renderer

// Which side of the water a blended draw belongs on.
//
// Water is the last thing the frame draws, and it has to be: refraction samples
// what is already on screen, a fragment shader cannot read the attachment it is
// writing, so the scene is copied and the surface drawn against the copy in a
// second pass. Everything blended — translucent meshes, particles, world
// overlays — used to be recorded before that copy, and none of it writes depth.
// So where an effect stood in front of the lake the depth buffer held the lake
// BED, the water passed the depth test and painted over the effect. A flame on
// the shore was cut off dead flat at the waterline: issue #45, reported from
// Vesper III. The effect was still in the copy, and absorbing water swallowed
// it, which is why the symptom is "faint smear" rather than "gone".
//
// Moving all of it after the water is wrong the other way. Something under the
// surface has to be in the frame before the copy or the water refracts a lake
// bed that the object is not part of — and the object then lands on top of the
// lake, unrefracted, at full brightness, which is a louder bug than the one
// being fixed.
//
// So the blended draws split on the water surface:
//
//	a blended draw is BEHIND the water, and stays before the copy, when some
//	water surface's still plane separates it from the eye. Otherwise it is IN
//	FRONT, and is drawn after the water.
//
// "Separates" is the whole rule, and it is what makes an underwater camera fall
// out instead of needing a case of its own. From above the surface the things
// behind the water are the ones below it; from below the surface they are the
// ones above it. Both are "the eye and the object are on opposite sides".
//
// # What this is not
//
// It is a per-draw answer, not a per-pixel one, and the places it is only
// approximately right are worth knowing:
//
//   - **The surface is its still plane.** Gerstner waves displace it by up to
//     WaveAmplitude, so a draw within that band of the surface can be
//     classified onto the wrong side. It is a static answer for a static
//     object, not a flicker, but a particle drifting through the surface flips
//     within a wave height of where it visually should.
//
//   - **A draw is one point.** The bound centre decides for the whole mesh, so
//     a tall pane standing half in the water goes wholly one way. Particles are
//     split per instance, which is as fine as the buffer goes, but one billboard
//     that straddles the surface is still all or nothing.
//
//   - **The footprint is a disc.** The surface mesh's bounding sphere covers the
//     lake in XZ; a lake with a concave shore has parts of that disc over dry
//     land. Being tested there only changes the answer for a draw that is below
//     the waterline over dry land, which is a draw inside the terrain.
//
//   - **Several bodies are tested independently.** A draw is behind the water if
//     ANY surface whose disc contains it separates it from the eye. With lakes
//     at different heights — a tarn above a lake — a draw between the two levels
//     is behind the upper surface and in front of the lower, and it goes before
//     the copy. That is the conservative choice: being refracted by a lake you
//     are not in is a wrong tint, being swallowed by one is a missing object.
//
//   - **Nothing consults the far side of the surface.** A draw above the water
//     and beyond the far shore is "in front", and is drawn after the water. It
//     is still depth-tested against the opaque scene, so the hill in front of it
//     still hides it; the water cannot, because water does not write depth.
//
// The split only exists on frames that contain water. Without it every blended
// draw takes exactly the path it took before this file existed, in exactly the
// order it took, which is what keeps water-free scenes byte for byte identical.

// waterPlane is one water surface reduced to what the split needs: the world Y
// of its still plane and a disc in XZ that covers it.
//
// Both are read off the surface MESH rather than off WaterParams, so they
// cannot disagree with the geometry that is drawn. WaterMesh puts every vertex
// at Level, which makes the bounding sphere's centre exactly the still plane
// and its radius the reach of the lake in XZ. A field on WaterParams would be a
// second copy of Level for the two of them to drift apart on.
type waterPlane struct {
	level  float32
	cx, cz float32

	// radius is the footprint in XZ. Zero means the mesh carries no bounds, and
	// the plane is then treated as unbounded — conservative in the direction
	// that keeps a submerged draw refracted.
	radius float32
}

// appendWaterPlanes reduces the frame's water draws to their still surfaces,
// reusing dst's storage so a frame that draws water does not allocate for it.
//
// The draws it accepts are exactly the ones recordWaterPass draws, so
// len(result) > 0 and hasWater(draws) cannot disagree: a surface counted here
// but not drawn would push blended geometry behind water that is not there.
func appendWaterPlanes(dst []waterPlane, draws []RenderObject) []waterPlane {
	for i := range draws {
		d := &draws[i]
		if d.Water == nil || d.ShadowOnly {
			continue
		}
		cx, cy, cz := d.worldCenter()
		dst = append(dst, waterPlane{
			level:  cy,
			cx:     cx,
			cz:     cz,
			radius: d.Mesh.BoundRadius * d.modelScale(),
		})
	}
	return dst
}

// blendSplit answers "which side of the frame's water is this?" for one frame.
//
// It is built once per frame in DrawFrame, because two things need the same
// answer: the command recorder, which decides where each blended draw is
// recorded, and the particle instance buffer, which has to be partitioned
// before it is uploaded. Computing it twice would let the two drift.
type blendSplit struct {
	planes []waterPlane
	eyeY   float32
}

// active reports whether the frame has water to split against. When it does
// not, the whole split collapses to "record everything where it always was".
func (s blendSplit) active() bool { return len(s.planes) > 0 }

// behind reports whether a point is on the far side of some water surface from
// the eye.
//
// The eye exactly on a surface is counted as above it. That case is a camera
// sitting on the waterline, where "behind the water" is meaningless for half a
// wave either way; picking a side beats a branch that produces neither.
func (s blendSplit) behind(x, y, z float32) bool {
	for i := range s.planes {
		p := &s.planes[i]
		if p.radius > 0 {
			dx, dz := x-p.cx, z-p.cz
			if dx*dx+dz*dz > p.radius*p.radius {
				continue
			}
		}
		if (y < p.level) != (s.eyeY < p.level) {
			return true
		}
	}
	return false
}

// keep reports whether a blended draw at this point belongs to the half being
// recorded: over is true for the pass after the water, false for the one before
// the refraction copy.
//
// With no water in the frame everything belongs to the pre-copy half, which is
// the pass that has always drawn it.
func (s blendSplit) keep(over bool, x, y, z float32) bool {
	if !s.active() {
		return !over
	}
	return s.behind(x, y, z) != over
}
