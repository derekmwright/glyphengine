package renderer

import (
	"math"
	"math/bits"

	"github.com/derekmwright/glyphengine/renderer/framegraph"
	"github.com/vkngwrapper/core/v3/core1_0"
)

// depthPrepassNode names the frame-graph declaration that stands for the
// optional depth prepass. Shared rather than repeated, the same way
// shadowCascadeNode is, because the recording fixtures match on it.
const depthPrepassNode = "depth prepass"

// DepthPrepassMode selects whether the depth prepass runs. See
// WithDepthPrepass.
type DepthPrepassMode int

const (
	// DepthPrepassOff is the default. No prepass node, no pipelines, no shader
	// module, and a frame records exactly the stream it records without the
	// option.
	DepthPrepassOff DepthPrepassMode = iota

	// DepthPrepassOn runs the prepass on every frame.
	//
	// It is a bet on the scene, not an improvement: the prepass removes hidden
	// fragments and pays for a second geometry submission over everything that
	// qualifies, whether or not anything is hidden. On the overdraw baseline
	// that cost was 0.816 ms at 3840x2160 and it was the same on the arm with
	// nothing hidden, which is why the unconditional option was removed once
	// already. Use it to measure, and to pin the mode for a capture.
	DepthPrepassOn

	// DepthPrepassAuto runs the prepass on the frames whose estimated depth
	// complexity says it will pay, and not on the others.
	//
	// The estimate is depthComplexityEstimate: the qualifying draws' projected
	// bound areas summed over the viewport's area, in CPU arithmetic over the
	// draw list, with no GPU readback and no frame of latency. The threshold
	// and its hysteresis band are depthPrepassThreshold and
	// depthPrepassHysteresis, both measured.
	//
	// Not the default. This record is evidence that the mechanism is worth
	// having, not that every game's frame should change; see ADR 0013.
	DepthPrepassAuto
)

func (m DepthPrepassMode) String() string {
	switch m {
	case DepthPrepassOff:
		return "off"
	case DepthPrepassOn:
		return "on"
	case DepthPrepassAuto:
		return "auto"
	}
	return "unknown"
}

// DepthPrepassDebug selects what the depth prepass submits, for comparing the
// prepass path against itself. See Renderer.SetDepthPrepassDebug.
type DepthPrepassDebug int

const (
	// DepthPrepassDebugOff is the shipping path: the prepass writes depth for
	// every qualifying draw and the main pass re-tests it with EQUAL.
	DepthPrepassDebugOff DepthPrepassDebug = iota

	// DepthPrepassDebugEmpty keeps the prepass node, its depth clear and the
	// main pass's EQUAL compare, and submits none of the prepass's draws.
	//
	// Every prepassed surface then fails the EQUAL test against a cleared depth
	// buffer and disappears, which is `task prepass`'s control: it has to move a
	// great many pixels. That makes it a check on three things at once -- that
	// the comparator sees a real rendering difference at all, that the depth the
	// main pass tests against is the depth this node wrote, and that the main
	// pass really is comparing EQUAL rather than still comparing Greater. If the
	// equal-compare variants were never bound, this mode would render
	// identically to the shipping one and the gate would say so.
	//
	// It is carried in the shipped engine for the reason LightDebugBruteForce is:
	// the gate that proves the fast path correct needs a slow path to compare it
	// to, and a comparison mode that only exists in a test build is a comparison
	// nobody runs.
	DepthPrepassDebugEmpty
)

// SetDepthPrepassDebug selects what the depth prepass submits. It does nothing
// on a renderer built with DepthPrepassOff.
func (r *Renderer) SetDepthPrepassDebug(mode DepthPrepassDebug) { r.depthPrepass.debug = mode }

// depthPrepassPipelines is the prepass's own two depth-only pipelines plus the
// three main-pass variants that compare EQUAL against what they wrote.
//
// Grouped for the reason materialPipelines is: recordCommandBuffer's parameter
// list is long enough already, and a pipeline transposed with its equal-compare
// twin is a mistake only a missing surface would reveal.
type depthPrepassPipelines struct {
	// depth and depthInstanced write depth only, from PrepassVert and
	// PrepassInstancedVert over the null ShadowFrag.
	depth, depthInstanced core1_0.Pipeline

	// lit, material and instanced are the main pass's twins of pipeline,
	// mat.pipeline and instancedPipeline, with CompareOpEqual and no depth
	// write. Only the draws depthPrepassQualifies accepts are recorded against
	// them, which is exactly the set the prepass wrote.
	lit, material, instanced core1_0.Pipeline

	// layout is litPipelineLayout, reused rather than duplicated: the prepass
	// stages read no descriptor at all, so the only thing they need from a
	// layout is a push-constant range covering the first 128 bytes for the
	// vertex stage, which this one already has. Binding a pipeline whose layout
	// declares sets the shader never touches is not an error, and reusing it
	// means one fewer object with a lifetime to get wrong (AGENTS.md rule 10).
	layout core1_0.PipelineLayout

	// mode is WithDepthPrepass. debug is SetDepthPrepassDebug.
	mode  DepthPrepassMode
	debug DepthPrepassDebug

	// active is this frame's decision, and it is read by three places that have
	// to agree: the prepass recorder, the main pass's pipeline choice and the
	// instanced recorder's. A frame where the recorder submits nothing and the
	// main pass still compares EQUAL renders every qualifying surface against a
	// cleared depth buffer, which is exactly DepthPrepassDebugEmpty -- a useful
	// control and a catastrophic accident.
	//
	// It carries over from the previous frame, which is what the hysteresis
	// band needs: an estimate inside the band keeps whatever the last decision
	// was. Zero value false, so the first frame of a scene sitting inside the
	// band runs without the prepass rather than with it.
	active bool

	// estimate is this frame's depthComplexityEstimate and covered its covered
	// share of the viewport, kept for the stats block whatever the mode, so a
	// game or the bench can see the numbers the decision was made on rather
	// than infer them from the decision.
	estimate float32
	covered  float32
}

// depthPrepassThreshold is the estimated depth complexity at or above which
// DepthPrepassAuto runs the prepass, and depthPrepassHysteresis is how far
// below it the estimate has to fall before Auto stops.
//
// Both measured, by `task bench -- -scene prepasssweep` at both bench
// resolutions: nine camera placements on examples/28-overdraw between estimate
// 1.15 and 3.13, prepass off and on interleaved inside each cell, three trials,
// 200 frames under the fixed clock, every sample kept, AMD Radeon RX 7900 XTX.
// The net change in gpu_total crosses zero at
//
//	1280x720    estimate 1.849   (bracketed by 1.706 at +0.277 ms and 1.916 at -0.130 ms)
//	3840x2160   estimate 1.201   (bracketed by 1.158 at +0.143 ms and 1.359 at -0.528 ms)
//
// and the threshold is the HIGHER crossing plus the sweep's scatter at its
// bracketing cell: 0.331 ms of within-mode scatter against a -1.938 ms slope per
// unit of estimate is 0.171 of estimate, so 1.849 + 0.171 = 2.02. Below the
// higher crossing, Auto would enable a prepass at a complexity where 720p still
// loses, which is the clause three earlier mechanisms died on.
//
// # Why one number and not one per resolution
//
// The saving is per fragment and the cost is a second geometry submission, so
// the excess over 1.0 at which the two balance ought to scale as C/pixels. It
// does not. The two crossings give C = 782,438 and C = 1,667,174, a factor of
// 2.13: the 4K-derived C predicts a 720p crossing of 2.809 against 1.849
// measured, which is 5.6x that cell's scatter, and even the geometric-mean
// compromise misses 720p by 2.3x. A resolution-aware threshold was the
// preferred outcome and the measurement refused it.
//
// The reason is visible in the sweep and is worth knowing before anyone tries
// again: the break-even depends on how much of the VIEWPORT the qualifying
// geometry covers as well as on how deep it is stacked, because the hidden
// fragments are covered x pixels x (complexity - 1). On this ladder coverage
// runs 0.22 to 0.98, which is why the 4K nets are not monotone in the estimate
// -- the saving grows again at estimates 1.52 and 1.71, where coverage is 0.98
// and 0.77, after shrinking at 1.92 where it is 0.65. A threshold on the
// estimate alone cannot separate those; RenderStats.PrepassCovered is reported
// so a second gate on coverage can be measured if a real scene needs one.
//
// # What one number costs
//
// At 3840x2160 the prepass pays from estimate 1.201 up, so a threshold of 2.02
// declines where it would have won: the measured cells at 1.359, 1.518, 1.710
// and 1.922 forgo 0.528, 1.163, 1.233 and 0.446 ms, up to 10.6 % of an 11.6 ms
// frame. That is the price of a threshold that cannot be wrong at 720p, and it
// is recorded in ADR 0013 rather than hidden in this constant.
//
// The band is one scatter, and it is close to the same number at both
// resolutions -- 0.171 of estimate at 720p and 0.177 at 4K -- which is what
// makes a single 0.18 honest. It puts the off edge at 1.84, within a thousandth
// of the 720p break-even, so Auto stops at break-even and starts one scatter
// above it.
const (
	depthPrepassThreshold  float32 = 2.02
	depthPrepassHysteresis float32 = 0.18
)

// DepthPrepassThreshold returns the estimated depth complexity at or above which
// DepthPrepassAuto runs the prepass, and the hysteresis band below it within
// which the previous frame's decision stands.
//
// Exported because RenderStats.PrepassEstimate cannot be interpreted without
// them. A game reading an estimate of 2.4 has no way to know whether that is a
// scene Auto will act on, and a gate checking that Auto decided correctly on a
// given scene would otherwise have to hard-code a number that the sweep is
// expected to move -- which is how a gate stops testing the thing it names. The
// values are measured; see the constants.
func DepthPrepassThreshold() (threshold, hysteresis float32) {
	return depthPrepassThreshold, depthPrepassHysteresis
}

// decide sets active from this frame's estimate and returns it.
//
// The band is asymmetric on purpose: the prepass starts when the estimate
// reaches the threshold and stops only when it falls a band BELOW it, so a
// scene hovering on the threshold keeps whichever mode it already had instead
// of alternating. Alternating would be worse than either choice -- the mode
// changes which pipelines the main pass binds, so a toggle costs a pipeline
// switch on every qualifying draw and makes two consecutive frames
// incomparable.
//
// Deterministic under the fixed clock, as a function of the scene's SEQUENCE
// rather than of one frame: the stored active flag is the only state, it starts
// false, and the estimate is arithmetic over the draw list. Two runs of the same
// build over the same scene take the same decisions in the same frames, which is
// what `task determinism` gates.
func (p *depthPrepassPipelines) decide(estimate, covered float32) bool {
	p.estimate = estimate
	p.covered = covered
	switch p.mode {
	case DepthPrepassOn:
		p.active = true
	case DepthPrepassAuto:
		switch {
		case estimate >= depthPrepassThreshold:
			p.active = true
		case estimate < depthPrepassThreshold-depthPrepassHysteresis:
			p.active = false
		}
	default:
		p.active = false
	}
	return p.active
}

// depthPrepassQualifies reports whether d's depth can be written by the prepass
// and then matched by the main pass with a compare of EQUAL.
//
// Deliberately one function rather than a condition written out in the prepass
// recorder and again in the main pass's pipeline choice: the two have to agree
// EXACTLY. A draw the prepass skips but the main pass records with EQUAL tests
// against a cleared depth buffer and vanishes; a draw the prepass writes but the
// main pass records with Greater is merely redundant, and therefore silent. The
// first failure is the one this guards.
//
// What is excluded, and why:
//
//   - Anything a fragment shader can discard. The depth test runs before the
//     shader for a pipeline that writes depth unconditionally, but a discard
//     makes the write conditional on the shader, so the hardware can no longer
//     reject early -- and more to the point, the fragment that the prepass
//     wrote may be one the main pass's shader would have thrown away, which
//     leaves depth claiming a surface that was never shaded. Grass, the LOD
//     fade's coverage and the impostor billboards are all in that class.
//   - Skinned draws. The joint transform happens in the vertex stage against a
//     per-frame joint buffer, so a depth-only twin would have to be a third
//     copy of that stage agreeing with it bit for bit, and a skinned mesh is
//     usually a small part of the frame's fragments anyway.
//   - Double-sided draws. Culling decides which faces write depth, so a prepass
//     pipeline whose cull mode disagreed with the main pass's would leave the
//     back faces out of the depth buffer and the main pass would then reject
//     them. Including them needs a second pair of prepass pipelines; they are
//     out of this first set rather than wrong.
//   - Terrain and water, which have their own pipelines and their own passes.
//     Nothing breaks if they are absent: they still write depth with Greater,
//     and a prepassed mesh in front of them still rejects their fragments.
//   - Translucent draws, which do not write depth at all.
//   - Instance sets drawn indirectly or through a mesh-range batch, and LOD
//     buckets: their draw arguments come from a buffer or from a sub-range
//     list, so the prepass would have to replay the same selection rather than
//     repeat one draw call.
func depthPrepassQualifies(d *RenderObject) bool {
	switch {
	case d.Mesh == nil, d.ShadowOnly:
		return false
	case d.Joints != nil, d.DoubleSided:
		return false
	case d.TerrainMat != nil, d.Water != nil:
		return false
	case d.IsTranslucent():
		return false
	case d.InstancesLOD != nil:
		return false
	}
	if s := d.Instances; s != nil {
		return s.lod == nil && s.batch == nil && s.indirect.Handle() == 0 && s.count > 0
	}
	return true
}

// depthComplexityCells is the resolution of the coverage grid the estimate
// measures the union over, per axis. A multiple of 64, so a row of the union is
// a whole number of uint64 words.
//
// 192 and not less, measured rather than assumed. This number does not converge
// quickly: a bound thinner than a cell still counts as one cell, so a field of
// slivers reads higher the coarser the grid, and on the overdraw baseline's
// overlap arm `28-overdraw -probe` measures 4.547 at 64 cells, 3.552 at 128,
// 3.299 at 192, 3.212 at 256 and 3.137 at 384 for its own clamping variant. For
// the rule this function uses -- a bound off screen contributes nothing -- the
// same probe measures 3.117 at 192 against 3.089 at the 180 that example reports,
// which is 0.9 % apart, and 1.0215 against 1.0222 on the control, 0.07 % apart.
// 128 would have been 8.3 % high.
//
// The threshold is calibrated against THIS grid, so the agreement matters less
// for correctness than for being able to read the two numbers side by side at
// all. 192 of these is 4.5 KiB of mask and at most 576 word-ORs per draw.
const depthComplexityCells = 192

// depthComplexityMask is the coverage grid's union, one bit per cell.
//
// A fixed array rather than a slice, and owned by the Renderer rather than
// allocated per frame, because the estimate runs inside cpu_record on every
// frame the prepass is not Off and this engine's allocation gates are per
// frame. 4.5 KiB, zeroed once per call.
type depthComplexityMask struct {
	rows [depthComplexityCells][depthComplexityCells / 64]uint64
}

// depthComplexityEstimate is the mean screen-space depth complexity of the
// geometry the prepass would touch: the summed screen area of the qualifying
// draws' projected bounds over the area at least one of them covers. It also
// returns what share of the viewport that covered area is.
//
// It is the number DepthPrepassAuto decides on, and it is the SAME QUANTITY
// examples/28-overdraw's onScreenRatio computes offline, by the same method --
// project each bound's eight corners, take the NDC rectangle, count the grid
// cells whose centres fall inside it, and divide the total by the number of
// cells at least one rectangle reached. That example's number is the check on
// this one, and because the two are the same algorithm at different grid
// resolutions, a disagreement between them is a bug rather than a difference of
// definition.
//
// onScreenRatio and not that example's overlapRatio, which is the same grid with
// one rule changed: a bound whose projected rectangle has left the viewport is
// clamped onto an edge cell there, and dropped here, because a draw off the side
// of the screen rasterises nothing. The two differ by 5.8 % on the overdraw
// baseline's grazing arm and not at all on its control. overlapRatio keeps its
// clamping because its number is the one every record of that scene quotes.
//
// # Why the union and not the viewport
//
// The obvious cheaper estimate is the summed area over the VIEWPORT's area,
// which needs no grid at all and is O(1) per draw. It was built that way first
// and it does not work, which the probe in that example settles in CPU
// arithmetic: on the overlap arm, where the depth complexity is 3.28 and the
// prepass saves 46 % of the frame at 4K, summed-area-over-viewport reads 0.742;
// on the control, where depth complexity is 1.02 and the prepass costs 0.555 ms
// for nothing, it reads 0.576. The two arms the whole measurement is built
// around are 29 % apart in that number and 3.2x apart in this one, and no
// threshold placed in a 29 % gap would survive a different scene. The reason is
// that a grazing camera's field covers a small part of the screen -- 23 % of it
// on the overlap arm -- so dividing by the whole viewport divides out most of
// the signal.
//
// What the union costs over the viewport version is one OR per covered grid row
// per draw, at most 192 rows of three words. The numerator stays O(1): the cells
// a rectangle covers is the product of its two spans, which is arithmetic, so no
// per-cell work happens for the sum.
//
// # Why the bound rather than the geometry
//
// It is what is already there. A mesh carries an object-space box
// (Mesh.BoundMin, see the comment there on why the SPHERE will not do for an
// area), the draw carries MVP, and eight corners through one matrix is the whole
// cost. The alternative -- the radius the frustum cull in the engine's
// buildDrawList already derives -- was not taken because it lives a module away,
// in a layer a renderer-only program does not run, and this decision has to be
// reachable from the renderer alone, beside the predicate that selects the
// draws it sums over.
//
// # What it does not see
//
//   - Hidden work INSIDE one draw. A single self-overlapping mesh reads as its
//     own bound area over its own bound area, which is 1.0, and a prepass would
//     in fact reject the fragments it hides from itself. The showcase's terrain
//     is many patches, which is the case this sees; one big mesh is the case it
//     misses.
//   - An instance set contributes its whole-set bound ONCE, because that is the
//     only bound a set keeps (InstanceSet.Bounds, a sphere over every
//     placement). A thousand trees filling the screen therefore read as one
//     screenful at complexity 1.0 rather than as the overlap they really are.
//     Under-counting means Auto declines to run on a dense instanced forest that
//     would have benefited; that is the conservative direction, and it is
//     recorded rather than fixed here.
//   - Scale. The ratio says how stacked the geometry is, not how much of it
//     there is, so a frame that is mostly sky with one small self-occluding
//     cluster in it reads as high a complexity as a frame filled with the same
//     cluster. The prepass's cost scales with the geometry it resubmits and its
//     saving with the hidden fragments, and both are small in that frame, so the
//     decision is not wrong so much as uncalibrated for it. The covered share is
//     returned beside the ratio so a caller can see which frame it is looking
//     at.
//
// A box is bigger than the mesh inside it, so this over-states both the sum and
// the union; the threshold is measured against THIS number, not against the
// truth, so the over-statement is in the calibration.
//
// Zero allocations: the mask is the caller's, the sum is arithmetic, and
// nothing here grows a slice.
func depthComplexityEstimate(draws []RenderObject, vp *[16]float32, m *depthComplexityMask) (complexity, coveredShare float32) {
	*m = depthComplexityMask{}
	cellsTotal := 0
	for i := range draws {
		d := &draws[i]
		if !depthPrepassQualifies(d) {
			continue
		}
		var lo, hi [3]float32
		mat := &d.MVP
		if set := d.Instances; set != nil {
			// The set's world-space bound sphere through the camera's VP. Its
			// box, not the sphere's silhouette circle, so this is the same
			// rectangle arithmetic as the per-mesh arm.
			c, r := set.boundCenter, set.boundRadius
			if r <= 0 {
				continue
			}
			lo = [3]float32{c[0] - r, c[1] - r, c[2] - r}
			hi = [3]float32{c[0] + r, c[1] + r, c[2] + r}
			mat = vp
		} else {
			lo, hi = d.Mesh.BoundMin, d.Mesh.BoundMax
			if lo == hi {
				// A mesh built before the box existed, or an empty one. Fall
				// back to the sphere's box rather than contributing nothing:
				// nothing would read as a frame with less in it than there is
				// and quietly hold Auto off, which is the silent failure this
				// engine's pages keep warning about.
				c, r := d.Mesh.BoundCenter, d.Mesh.BoundRadius
				if r <= 0 {
					continue
				}
				lo = [3]float32{c[0] - r, c[1] - r, c[2] - r}
				hi = [3]float32{c[0] + r, c[1] + r, c[2] + r}
			}
		}
		x0, x1, y0, y1, ok := projectedBoundNDC(mat, lo, hi)
		if !ok {
			continue
		}
		cx0, cx1 := ndcCellSpan(x0, x1)
		cy0, cy1 := ndcCellSpan(y0, y1)
		cellsTotal += (cx1 - cx0 + 1) * (cy1 - cy0 + 1)
		// The column mask once, then one OR per row. This is the only part of
		// the estimate that is not O(1) per draw, and it is bounded by the grid
		// rather than by the scene: 192 rows of three words, whatever the draw.
		var colMask [depthComplexityCells / 64]uint64
		for w := range colMask {
			lowBit, highBit := w*64, w*64+63
			if cx1 < lowBit || cx0 > highBit {
				continue
			}
			a, b := max(cx0, lowBit)-lowBit, min(cx1, highBit)-lowBit
			colMask[w] = (^uint64(0) >> uint(63-(b-a))) << uint(a)
		}
		for y := cy0; y <= cy1; y++ {
			for w := range colMask {
				m.rows[y][w] |= colMask[w]
			}
		}
	}
	covered := 0
	for y := range m.rows {
		for w := range m.rows[y] {
			covered += bits.OnesCount64(m.rows[y][w])
		}
	}
	if covered == 0 {
		return 0, 0
	}
	return float32(cellsTotal) / float32(covered), float32(covered) / float32(depthComplexityCells*depthComplexityCells)
}

// projectedBoundNDC is the NDC bounding rectangle of the box lo..hi through m,
// clipped to the viewport, and whether there is one at all.
//
// A corner with w <= 0 is behind the eye and projects to the wrong side of the
// screen, so a box with any such corner contributes NOTHING rather than a
// rectangle smeared across the frame. That is the same rule the offline
// measurement in examples/28-overdraw uses, deliberately, so the two agree on
// the same draws; it costs the estimate the draw the camera is standing inside,
// which is one draw.
func projectedBoundNDC(m *[16]float32, lo, hi [3]float32) (x0, x1, y0, y1 float32, ok bool) {
	x0, y0 = float32(math.MaxFloat32), float32(math.MaxFloat32)
	x1, y1 = -float32(math.MaxFloat32), -float32(math.MaxFloat32)
	for c := 0; c < 8; c++ {
		x, y, z := lo[0], lo[1], lo[2]
		if c&1 != 0 {
			x = hi[0]
		}
		if c&2 != 0 {
			y = hi[1]
		}
		if c&4 != 0 {
			z = hi[2]
		}
		// Column-major, the convention every other [16]float32 matrix in this
		// package carries: m[col*4+row].
		cx := m[0]*x + m[4]*y + m[8]*z + m[12]
		cy := m[1]*x + m[5]*y + m[9]*z + m[13]
		cw := m[3]*x + m[7]*y + m[11]*z + m[15]
		if cw <= 0 {
			return 0, 0, 0, 0, false
		}
		ndcX, ndcY := cx/cw, cy/cw
		x0, x1 = min(x0, ndcX), max(x1, ndcX)
		y0, y1 = min(y0, ndcY), max(y1, ndcY)
	}
	if x1 < -1 || x0 > 1 || y1 < -1 || y0 > 1 {
		return 0, 0, 0, 0, false
	}
	return max(x0, -1), min(x1, 1), max(y0, -1), min(y1, 1), true
}

// ndcCellSpan is the inclusive range of grid cells whose CENTRES fall inside
// [lo, hi] on one axis of NDC.
//
// Centres rather than any overlap at all, which is what examples/28-overdraw's
// ndcSpan does and the reason the two agree: rounding a rectangle outward would
// inflate both the sum and the union, and inflate the union more, because the
// union is the smaller number. A rectangle thinner than one cell still counts as
// one cell -- its nearest -- rather than as none, so a field of slivers is not
// invisible to the sum.
func ndcCellSpan(lo, hi float32) (int, int) {
	const n = float32(depthComplexityCells)
	first := int(math.Ceil(float64((lo*0.5+0.5)*n - 0.5)))
	last := int(math.Floor(float64((hi*0.5+0.5)*n - 0.5)))
	if last < first {
		first = int(math.Round(float64(((lo+hi)*0.25+0.5)*n - 0.5)))
		last = first
	}
	first = max(0, min(depthComplexityCells-1, first))
	last = max(0, min(depthComplexityCells-1, last))
	if last < first {
		last = first
	}
	return first, last
}

// insertDepthPrepass splices the prepass declaration in immediately before the
// hand-recorded scene, and renumbers the node indices that follow it.
//
// Immediately before, rather than at the head of the graph, and that placement
// is load-bearing twice over. An application pass at StageBeforeScene with
// DepthTest declares a depth write of its own; today the scene's own clear wipes
// it, and keeping the prepass after those passes means its clear wipes it too,
// so turning the option on cannot change what such a pass contributes. And the
// barrier the compiler derives for this node's depth write is the edge that
// orders it against everything before it, which is the whole reason the node is
// declared rather than merely recorded.
//
// Splicing rather than adding the declaration in newFrameGraph's own sequence:
// the graph* constants index the declaration list that extendAppGraph walks, so
// prepending there would renumber them and make the switch in that walk name the
// wrong nodes. Doing it afterwards, by node index, is the same operation for a
// graph with an owning Renderer and for the GPU-free fixtures that have none.
func (f *frameGraph) insertDepthPrepass() {
	at := f.engine[graphLegacy]
	// Discard and a clear of 0: reverse-Z's far plane (AGENTS.md rule 5). The
	// node owns the depth clear the scene pass used to own, and bindSceneTargets
	// loads depth instead when this node exists.
	//
	// FinalLayout names the attachment layout this node leaves, which is what
	// stops the compiler restoring depth's resting layout on the way out. That
	// resting layout is the sampled one whenever an application pass reads scene
	// depth, and the round trip out of the attachment layout and straight back
	// into it for the scene pass is two barriers that achieve nothing and one
	// write-after-write hazard. See Use.FinalLayout.
	decl := framegraph.Node{Name: depthPrepassNode, Kind: framegraph.Graphics, Timed: true,
		Uses: []framegraph.Use{{Resource: f.depth, Access: framegraph.DepthWrite,
			Clear: &framegraph.Clear{}, Discard: true,
			FinalLayout: core1_0.ImageLayoutDepthStencilAttachmentOptimal}}}
	node := graphNode{name: depthPrepassNode, record: recordDepthPrepass,
		begin: PassDepthPrepass, end: PassDepthPrepass, resolve: -1}

	f.declarations = append(f.declarations, framegraph.Node{})
	copy(f.declarations[at+1:], f.declarations[at:])
	f.declarations[at] = decl
	f.nodes = append(f.nodes, graphNode{})
	copy(f.nodes[at+1:], f.nodes[at:])
	f.nodes[at] = node

	for i := range f.engine {
		if f.engine[i] >= at {
			f.engine[i]++
		}
	}
	if f.depthNode >= at {
		f.depthNode++
	}
	if f.shadowCascades >= at {
		f.shadowCascades++
	}
	f.prepass = at
}

// recordDepthPrepass writes depth for every qualifying opaque draw, individual
// meshes first and instance sets after, so each pipeline is bound once.
//
// The push block is the 128-byte depth-only one the shadow passes use rather than
// the full 256: these two vertex stages read the first matrix and nothing else.
// It goes through pushPrepassConstants rather than pushShadowConstants because
// the stage flags of a push have to name every stage of the layout range they
// overlap; see that method.
func recordDepthPrepass(c *graphFrame) {
	// The estimate and the decision reach the stats here rather than in
	// DrawFrame, because recordCommandBuffer zeroes the counters at its start
	// and anything written before it would be wiped.
	c.stats.PrepassEstimate = c.prepass.estimate
	c.stats.PrepassCovered = c.prepass.covered
	c.stats.PrepassActive = c.prepass.active
	// Auto said no for this frame, or the control withheld the draws. Either
	// way the node is still here and its depth clear still runs -- the scene
	// pass LOADS depth whenever the prepass exists, so something has to clear
	// it, and skipping the node instead would leave the main pass testing
	// against the previous frame's depth.
	//
	// The difference between the two is what the main pass then compares with.
	// Auto-off takes the ordinary Greater pipelines, because active is false
	// everywhere that reads it. The debug control keeps EQUAL and therefore
	// loses every prepassed surface, on purpose.
	if !c.prepass.active || c.prepass.debug == DepthPrepassDebugEmpty {
		return
	}
	viewport := core1_0.Viewport{Width: float32(c.extent.Width), Height: float32(c.extent.Height), MinDepth: 0, MaxDepth: 1}
	scissor := core1_0.Rect2D{Extent: c.extent}

	bound := false
	for i := range c.draws {
		d := &c.draws[i]
		if d.Instances != nil || !depthPrepassQualifies(d) {
			continue
		}
		if !bound {
			c.driver.CmdBindPipeline(c.cmd, core1_0.PipelineBindPointGraphics, c.prepass.depth)
			c.scratch.setViewport(c.driver, c.cmd, viewport)
			c.scratch.setScissor(c.driver, c.cmd, scissor)
			bound = true
		}
		c.scratch.bindVertexBuffers(c.driver, c.cmd, 0, d.Mesh.vertexBuffer)
		copy(c.scratch.shadowPC[:16], d.MVP[:])
		copy(c.scratch.shadowPC[16:32], d.Model[:])
		c.scratch.pushPrepassConstants(c.driver, c.cmd, c.prepass.layout)

		c.stats.addDraw(1, d.Mesh.IndexCount, d.Mesh.VertexCount)
		c.stats.PrepassDraws++
		if d.Mesh.IndexCount > 0 {
			c.driver.CmdBindIndexBuffer(c.cmd, d.Mesh.indexBuffer, 0, d.Mesh.indexType)
			c.driver.CmdDrawIndexed(c.cmd, d.Mesh.IndexCount, 1, d.Mesh.firstIndex, d.Mesh.vertexOffset, 0)
		} else {
			c.driver.CmdDraw(c.cmd, d.Mesh.VertexCount, 1, 0, 0)
		}
	}

	instanced := false
	for i := range c.draws {
		d := &c.draws[i]
		set := d.Instances
		if set == nil || !depthPrepassQualifies(d) {
			continue
		}
		if !instanced {
			c.driver.CmdBindPipeline(c.cmd, core1_0.PipelineBindPointGraphics, c.prepass.depthInstanced)
			c.scratch.setViewport(c.driver, c.cmd, viewport)
			c.scratch.setScissor(c.driver, c.cmd, scissor)
			instanced = true
		}
		c.scratch.bindVertexBuffers(c.driver, c.cmd, 0, set.Mesh.vertexBuffer, set.buffer)
		// The instanced depth stage reads the first matrix as the camera's
		// view-projection and takes the model from its instance attribute, so
		// [16:32) must read as zero -- explicit here for the same reason
		// recordInstancedShadow spells it out.
		c.scratch.shadowPC = [32]float32{}
		copy(c.scratch.shadowPC[:16], c.lighting.VP[:])
		c.scratch.pushPrepassConstants(c.driver, c.cmd, c.prepass.layout)

		c.stats.addInstanceDraw(set, set.Mesh.IndexCount, set.Mesh.VertexCount)
		c.stats.PrepassDraws++
		if set.Mesh.IndexCount > 0 {
			c.driver.CmdBindIndexBuffer(c.cmd, set.Mesh.indexBuffer, 0, set.Mesh.indexType)
			c.driver.CmdDrawIndexed(c.cmd, set.Mesh.IndexCount, set.count, set.Mesh.firstIndex, set.Mesh.vertexOffset, 0)
		} else {
			c.driver.CmdDraw(c.cmd, set.Mesh.VertexCount, set.count, 0, 0)
		}
	}
}

// createDepthPrepassPipeline builds one of the prepass's depth-only pipelines.
//
// Close to createShadowPipelineWithInput but not it, and the differences are all
// load-bearing: the scene's own sample count rather than Samples1, the scene's
// reverse-Z CompareOpGreater rather than the shadow map's LessOrEqual, back-face
// culling rather than front, and NO DEPTH BIAS. A bias is what a shadow map wants
// and exactly what this must not have -- the main pass compares EQUAL against
// these values, so a biased prepass would reject every surface it wrote.
func createDepthPrepassPipeline(deviceDriver core1_0.DeviceDriver, sh ShaderSet, vertSpv []byte, label string, formats renderingFormats, layout core1_0.PipelineLayout, extent core1_0.Extent2D, samples core1_0.SampleCountFlags, bindings []core1_0.VertexInputBindingDescription, attrs []core1_0.VertexInputAttributeDescription) (core1_0.Pipeline, error) {
	vertModule, _, err := deviceDriver.CreateShaderModule(nil, core1_0.ShaderModuleCreateInfo{Code: bytesToUint32Slice(vertSpv)})
	if err != nil {
		return core1_0.Pipeline{}, err
	}
	defer deviceDriver.DestroyShaderModule(vertModule, nil)

	fragModule, _, err := deviceDriver.CreateShaderModule(nil, core1_0.ShaderModuleCreateInfo{Code: bytesToUint32Slice(sh.ShadowFrag)})
	if err != nil {
		return core1_0.Pipeline{}, err
	}
	defer deviceDriver.DestroyShaderModule(fragModule, nil)

	pipelines, _, err := deviceDriver.CreateGraphicsPipelines(nil, nil, core1_0.GraphicsPipelineCreateInfo{
		Stages: []core1_0.PipelineShaderStageCreateInfo{
			{Stage: core1_0.StageVertex, Module: vertModule, Name: "main"},
			{Stage: core1_0.StageFragment, Module: fragModule, Name: "main"},
		},
		VertexInputState: &core1_0.PipelineVertexInputStateCreateInfo{
			VertexBindingDescriptions:   bindings,
			VertexAttributeDescriptions: attrs,
		},
		InputAssemblyState: &core1_0.PipelineInputAssemblyStateCreateInfo{Topology: core1_0.PrimitiveTopologyTriangleList},
		ViewportState: &core1_0.PipelineViewportStateCreateInfo{
			Viewports: []core1_0.Viewport{{Width: float32(extent.Width), Height: float32(extent.Height), MinDepth: 0, MaxDepth: 1}},
			Scissors:  []core1_0.Rect2D{{Extent: extent}},
		},
		RasterizationState: &core1_0.PipelineRasterizationStateCreateInfo{
			PolygonMode: core1_0.PolygonModeFill,
			CullMode:    core1_0.CullModeBack,
			FrontFace:   core1_0.FrontFaceClockwise,
			LineWidth:   1.0,
		},
		MultisampleState: &core1_0.PipelineMultisampleStateCreateInfo{RasterizationSamples: samples},
		DepthStencilState: &core1_0.PipelineDepthStencilStateCreateInfo{
			DepthTestEnable:  true,
			DepthWriteEnable: true,
			DepthCompareOp:   core1_0.CompareOpGreater,
		},
		// No colour attachment at all, so no blend attachment state either.
		ColorBlendState: &core1_0.PipelineColorBlendStateCreateInfo{},
		DynamicState: &core1_0.PipelineDynamicStateCreateInfo{
			DynamicStates: []core1_0.DynamicState{core1_0.DynamicStateViewport, core1_0.DynamicStateScissor},
		},
		Layout:      layout,
		NextOptions: renderingOptions(formats),
	})
	if err != nil {
		return core1_0.Pipeline{}, err
	}
	return pipelines[0], nil
}

// depthEqualState is the depth state a main-pass variant takes for draws the
// prepass already wrote: test EQUAL, write nothing.
//
// Write nothing because the value is already there and writing it again is pure
// bandwidth. EQUAL rather than GreaterOrEqual because the point is to reject
// every fragment that is not the one the prepass kept -- GreaterOrEqual would
// also admit anything nearer, which after a complete prepass means nothing, but
// it would stop being a statement about the prepass having worked.
func depthEqualState() *core1_0.PipelineDepthStencilStateCreateInfo {
	return &core1_0.PipelineDepthStencilStateCreateInfo{
		DepthTestEnable:  true,
		DepthWriteEnable: false,
		DepthCompareOp:   core1_0.CompareOpEqual,
	}
}

// createDepthEqualPipeline, createDepthEqualMaterialPipeline and
// createDepthEqualInstancedPipeline are the main-pass twins of
// createGraphicsPipeline, createMaterialPipeline and createInstancedPipeline for
// the prepassed set: the same shaders, the same cull mode and the same layout
// shape, differing only in depthEqualState above.
//
// Siblings rather than an extra variadic on the existing three, because every
// one of their existing call sites passes a cull mode positionally and a second
// optional parameter behind it is exactly the kind of transposition those
// functions' comments already warn about.
func createDepthEqualPipeline(deviceDriver core1_0.DeviceDriver, sh ShaderSet, formats renderingFormats, extent core1_0.Extent2D, texSetLayout, shadowSetLayout core1_0.DescriptorSetLayout, samples core1_0.SampleCountFlags) (core1_0.Pipeline, core1_0.PipelineLayout, error) {
	return createLitVariantPipeline(deviceDriver, sh.LitVert, sh.LitFrag, "Graphics depth-equal",
		formats, extent, texSetLayout, shadowSetLayout, samples, core1_0.CullModeBack, false, depthEqualState())
}

func createDepthEqualMaterialPipeline(deviceDriver core1_0.DeviceDriver, sh ShaderSet, formats renderingFormats, extent core1_0.Extent2D, materialSetLayout, shadowSetLayout core1_0.DescriptorSetLayout, samples core1_0.SampleCountFlags) (core1_0.Pipeline, core1_0.PipelineLayout, error) {
	return createLitVariantPipeline(deviceDriver, sh.LitVert, sh.LitMaterialFrag, "Material depth-equal",
		formats, extent, materialSetLayout, shadowSetLayout, samples, core1_0.CullModeBack, false, depthEqualState())
}

func createDepthEqualInstancedPipeline(deviceDriver core1_0.DeviceDriver, sh ShaderSet, formats renderingFormats, extent core1_0.Extent2D, texSetLayout, shadowSetLayout core1_0.DescriptorSetLayout, samples core1_0.SampleCountFlags) (core1_0.Pipeline, core1_0.PipelineLayout, error) {
	return createLitVariantPipelineWithInput(deviceDriver, sh.LitInstancedVert, sh.LitFrag, "Instanced depth-equal",
		formats, extent, texSetLayout, shadowSetLayout, samples, core1_0.CullModeBack, false, depthEqualState(),
		[]core1_0.VertexInputBindingDescription{vertexBindingDescription(), instanceBindingDescription()},
		instanceAttributeDescriptions())
}

// createDepthPrepassPipelines builds everything WithDepthPrepass needs and
// pushes each object's teardown as it goes (AGENTS.md rule 10). It is called
// only when the option is on, so a renderer without it creates no shader module,
// no pipeline and no layout for this at all.
func (r *Renderer) createDepthPrepassPipelines() error {
	p := &r.depthPrepass
	p.mode = r.depthPrepassMode
	p.layout = r.litPipelineLayout

	var err error
	p.depth, err = createDepthPrepassPipeline(r.deviceDriver, r.shaders, r.shaders.PrepassVert, "Depth prepass",
		depthOnlyFormats(r.depth.format), p.layout, r.sc.extent, r.msaaSamples,
		[]core1_0.VertexInputBindingDescription{vertexBindingDescription()}, vertexAttributeDescriptions())
	if err != nil {
		return err
	}
	r.onInit(func() { r.deviceDriver.DestroyPipeline(r.depthPrepass.depth, nil) })

	p.depthInstanced, err = createDepthPrepassPipeline(r.deviceDriver, r.shaders, r.shaders.PrepassInstancedVert, "Depth prepass instanced",
		depthOnlyFormats(r.depth.format), p.layout, r.sc.extent, r.msaaSamples,
		[]core1_0.VertexInputBindingDescription{vertexBindingDescription(), instanceBindingDescription()},
		instanceAttributeDescriptions())
	if err != nil {
		return err
	}
	r.onInit(func() { r.deviceDriver.DestroyPipeline(r.depthPrepass.depthInstanced, nil) })

	// Each of the three below builds a pipeline layout of its own, the same way
	// every other createLitVariantPipeline caller does; both have to be given
	// back, which is why the layouts are retained rather than discarded.
	p.lit, r.depthEqualLayout, err = createDepthEqualPipeline(r.deviceDriver, r.shaders, r.sceneFormats, r.sc.extent,
		r.descriptorSetLayout, r.shadow.descriptorSetLayout, r.msaaSamples)
	if err != nil {
		return err
	}
	r.onInit(func() {
		r.deviceDriver.DestroyPipeline(r.depthPrepass.lit, nil)
		r.deviceDriver.DestroyPipelineLayout(r.depthEqualLayout, nil)
	})

	p.material, r.depthEqualMaterialLayout, err = createDepthEqualMaterialPipeline(r.deviceDriver, r.shaders, r.sceneFormats, r.sc.extent,
		r.materialSetLayout, r.shadow.descriptorSetLayout, r.msaaSamples)
	if err != nil {
		return err
	}
	r.onInit(func() {
		r.deviceDriver.DestroyPipeline(r.depthPrepass.material, nil)
		r.deviceDriver.DestroyPipelineLayout(r.depthEqualMaterialLayout, nil)
	})

	p.instanced, r.depthEqualInstancedLayout, err = createDepthEqualInstancedPipeline(r.deviceDriver, r.shaders, r.sceneFormats, r.sc.extent,
		r.descriptorSetLayout, r.shadow.descriptorSetLayout, r.msaaSamples)
	if err != nil {
		return err
	}
	r.onInit(func() {
		r.deviceDriver.DestroyPipeline(r.depthPrepass.instanced, nil)
		r.deviceDriver.DestroyPipelineLayout(r.depthEqualInstancedLayout, nil)
	})
	return nil
}

// depthOnlyFormats is the prepass node's attachment formats: a depth format and
// no colour at all. colorDepthFormats already treats a zero colour format that
// way, and naming it here is what keeps the one caller from reading as a
// mistake.
func depthOnlyFormats(depth core1_0.Format) renderingFormats { return colorDepthFormats(0, depth) }
