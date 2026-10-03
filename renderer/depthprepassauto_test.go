package renderer

import (
	"errors"
	"fmt"
	"math"
	"testing"

	"github.com/go-gl/mathgl/mgl32"
	"github.com/vkngwrapper/core/v3/core1_0"
)

// identityMVP is a matrix that leaves object space alone with w = 1, so a box
// given in NDC coordinates projects to itself. Every estimate case below is
// written in NDC for that reason: the arithmetic under test is the rectangle and
// the grid, not the projection, and a perspective matrix in the fixture would
// make a failing case a puzzle about which of the two was wrong.
var identityMVP = [16]float32{
	1, 0, 0, 0,
	0, 1, 0, 0,
	0, 0, 1, 0,
	0, 0, 0, 1,
}

// behindMVP sends w to -z, so a corner at z = 1 lands behind the eye.
var behindMVP = [16]float32{
	1, 0, 0, 0,
	0, 1, 0, 0,
	0, 0, 1, -1,
	0, 0, 0, 0,
}

func ndcBoxDraw(lo, hi [3]float32) RenderObject {
	return RenderObject{Mesh: &Mesh{BoundMin: lo, BoundMax: hi, VertexCount: 8}, MVP: identityMVP}
}

// The estimate on bounds whose answers are arithmetic rather than measured.
//
// The four cases worth naming -- a bound covering the viewport, two covering it,
// one off screen, one half off screen -- plus the two that decide whether the
// quantity is the one the records mean: a half-covered frame
// still reads complexity 1.0, because the denominator is the COVERED area and
// not the viewport, and it is the covered share that falls to 0.5. Normalising
// by the viewport instead was tried first and measured 0.742 against 0.576 on
// the two arms of examples/28-overdraw, 29 % apart where this normalisation puts
// them 3.2x apart; see depthComplexityEstimate.
//
// Verified to fail twice. Dividing by depthComplexityCells squared instead of by
// the covered count -- the viewport normalisation -- reports `half off screen:
// complexity = 0.500, want 1.000`. Returning zero from the estimate reports
// `one bound over the viewport: complexity = 0.000, want 1.000` and five more.
// Leaving the mask uncleared between calls reports `off screen: covered = 1.000,
// want 0.000`, which is the one of the three that would otherwise have been
// invisible: every case passes on a cold mask and only the second call in a
// process sees the stale bits.
func TestDepthComplexityEstimate(t *testing.T) {
	full := ndcBoxDraw([3]float32{-1, -1, 0}, [3]float32{1, 1, 0})
	rightHalf := ndcBoxDraw([3]float32{0, -1, 0}, [3]float32{3, 1, 0})
	offScreen := ndcBoxDraw([3]float32{2, -1, 0}, [3]float32{3, 1, 0})

	skinned := ndcBoxDraw([3]float32{-1, -1, 0}, [3]float32{1, 1, 0})
	skinned.Joints = &JointBuffer{}

	behind := ndcBoxDraw([3]float32{-1, -1, 0}, [3]float32{1, 1, 1})
	behind.MVP = behindMVP

	cases := []struct {
		name            string
		draws           []RenderObject
		complexity      float32
		covered         float32
		complexityDelta float32
	}{
		{"nothing", nil, 0, 0, 0},
		{"one bound over the viewport", []RenderObject{full}, 1, 1, 0},
		{"two bounds over the viewport", []RenderObject{full, full}, 2, 1, 0},
		{"three bounds over the viewport", []RenderObject{full, full, full}, 3, 1, 0},
		{"off screen", []RenderObject{offScreen}, 0, 0, 0},
		{"half off screen", []RenderObject{rightHalf}, 1, 0.5, 0},
		{"viewport plus half", []RenderObject{full, rightHalf}, 1.5, 1, 0},
		// A draw the predicate rejects is not in the sum OR the union: the
		// prepass would not write it, so it is not work the prepass can remove.
		// This is the clause that keeps the estimate and the recorder reading the
		// same set -- a skinned mesh filling the frame would otherwise read as a
		// scene worth prepassing when none of it would be prepassed.
		{"a rejected draw is not counted", []RenderObject{skinned}, 0, 0, 0},
		{"a rejected draw over a counted one", []RenderObject{full, skinned}, 1, 1, 0},
		// Behind the eye: skipped rather than smeared, the same rule the offline
		// measurement uses.
		{"behind the eye", []RenderObject{behind}, 0, 0, 0},
	}
	var mask depthComplexityMask
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			complexity, covered := depthComplexityEstimate(c.draws, &identityMVP, &mask)
			if d := float32(math.Abs(float64(complexity - c.complexity))); d > c.complexityDelta+1e-5 {
				t.Errorf("complexity = %.3f, want %.3f", complexity, c.complexity)
			}
			if d := float32(math.Abs(float64(covered - c.covered))); d > 1e-5 {
				t.Errorf("covered = %.3f, want %.3f", covered, c.covered)
			}
		})
	}
}

// An instance set contributes its whole-set bound once, which is the limitation
// the page and the ADR record rather than a thing to fix here. Pinned so that
// "once" is a decision and not an accident: a change that started summing the
// placements would move the threshold's meaning without moving the threshold.
func TestDepthComplexityEstimateCountsAnInstanceSetOnce(t *testing.T) {
	mesh := &Mesh{BoundMin: [3]float32{-1, -1, -1}, BoundMax: [3]float32{1, 1, 1}, VertexCount: 8}
	// A set whose bound sphere's box is exactly the viewport in NDC under the
	// identity. 1000 placements inside it read the same as one.
	set := &InstanceSet{Mesh: mesh, count: 1000, boundCenter: [3]float32{0, 0, 0}, boundRadius: 1}
	draws := []RenderObject{{Mesh: mesh, Instances: set, MVP: identityMVP}}
	var mask depthComplexityMask
	complexity, covered := depthComplexityEstimate(draws, &identityMVP, &mask)
	if math.Abs(float64(complexity-1)) > 1e-5 || math.Abs(float64(covered-1)) > 1e-5 {
		t.Errorf("a 1000-instance set reads complexity %.3f covered %.3f, want 1.000 and 1.000", complexity, covered)
	}
}

// offlineDepthComplexity is examples/28-overdraw's onScreenRatio, independently
// written from its description rather than called: the point of the comparison
// is that two implementations of the same definition agree, and importing the
// example's code would make it one implementation compared with itself.
//
// onScreenRatio and not that example's overlapRatio, which clamps a bound that
// has left the frame onto an edge cell instead of dropping it. The engine drops
// it, because a draw off the side of the screen rasterises nothing; the example
// keeps the clamping in overlapRatio because its number is the one every record
// of that scene quotes. The two differ by 5.8 % on the grazing arm and not at
// all on the overhead one, which is a gap the tolerance below has no business
// absorbing.
//
// Takes the cell count, because the grid resolution is the only difference
// between this and the function under test that is allowed to matter.
func offlineDepthComplexity(boxes [][2][3]float32, vp *[16]float32, cells int) float64 {
	hits := make([]int, cells*cells)
	span := func(lo, hi float32) (int, int) {
		n := float32(cells)
		first := int(math.Ceil(float64((lo*0.5+0.5)*n - 0.5)))
		last := int(math.Floor(float64((hi*0.5+0.5)*n - 0.5)))
		if last < first {
			first = int(math.Round(float64(((lo+hi)*0.25+0.5)*n - 0.5)))
			last = first
		}
		first = max(0, min(cells-1, first))
		last = max(0, min(cells-1, last))
		if last < first {
			last = first
		}
		return first, last
	}
	for _, b := range boxes {
		lo, hi := b[0], b[1]
		minX, minY := float32(math.MaxFloat32), float32(math.MaxFloat32)
		maxX, maxY := -float32(math.MaxFloat32), -float32(math.MaxFloat32)
		behind := false
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
			cx := vp[0]*x + vp[4]*y + vp[8]*z + vp[12]
			cy := vp[1]*x + vp[5]*y + vp[9]*z + vp[13]
			cw := vp[3]*x + vp[7]*y + vp[11]*z + vp[15]
			if cw <= 0 {
				behind = true
				break
			}
			minX, maxX = min(minX, cx/cw), max(maxX, cx/cw)
			minY, maxY = min(minY, cy/cw), max(maxY, cy/cw)
		}
		if behind {
			continue
		}
		if maxX < -1 || minX > 1 || maxY < -1 || minY > 1 {
			continue
		}
		x0, x1 := span(max(minX, -1), min(maxX, 1))
		y0, y1 := span(max(minY, -1), min(maxY, 1))
		for y := y0; y <= y1; y++ {
			for x := x0; x <= x1; x++ {
				hits[y*cells+x]++
			}
		}
	}
	covered, total := 0, 0
	for _, n := range hits {
		if n > 0 {
			covered++
			total += n
		}
	}
	if covered == 0 {
		return 0
	}
	return float64(total) / float64(covered)
}

// overdrawField is the geometry of examples/28-overdraw: a cols x rows grid of
// 4 m patches with about 1.5 m of relief, tiled edge to edge away from the
// camera along +Z, farthest row first.
func overdrawField(cols, rows int) (boxes [][2][3]float32) {
	const patch, relief = float32(4), float32(1.5)
	for n := 0; n < cols*rows; n++ {
		col, row := n%cols, rows-1-n/cols
		// A little relief variation per patch, so the vertical extents are not
		// all identical and the projected rectangles are not all the same shape.
		h := relief * (0.6 + 0.4*float32(math.Sin(float64(n))))
		cx := (float32(col) - float32(cols-1)/2) * patch
		cz := float32(row) * patch
		boxes = append(boxes, [2][3]float32{
			{cx - patch/2, 0, cz - patch/2},
			{cx + patch/2, h, cz + patch/2},
		})
	}
	return boxes
}

// The estimate against the offline measurement, on the geometry and the two
// cameras the records are written about.
//
// This is the estimate's agreement with the offline measurement, and it is here
// rather than only in the example because the example needs a GPU and this is the
// same arithmetic. The example's own run
// repeats it against the engine's reported number on the real draw list, with
// the real meshes and the real MVPs, which is what catches a mistake in how the
// draw list reaches the estimate rather than in the estimate itself.
//
// The tolerance is the two grids' resolutions, 192 against 180. It is NOT a
// noise allowance: both sides are deterministic arithmetic over the same bounds,
// and the only reason they differ at all is that a bound thinner than a cell
// counts as one cell in both and the cells are different sizes. Measured here:
// 0.3 % on the grazing camera and 1.3 % on the overhead one. 3 % has room for
// both, and the mistakes it has to exclude are an order of magnitude larger.
//
// Verified to fail, each with the number that makes it worth excluding:
//
//   - the grid at 64 cells instead of 192: `grazing: estimate 3.774 against the
//     offline 3.571, 5.7 % apart`;
//   - the bounding sphere's box instead of the mesh's: `overhead: estimate 2.393
//     against the offline 1.029, 132.6 % apart` and `grazing: estimate 13.196
//     against the offline 3.571, 269.5 % apart`;
//   - normalising by the viewport: `grazing: estimate 1.500 against the offline
//     3.571, 58.0 % apart`, plus the arm check `complexity 1.500, need at least
//     2.500 -- this camera is no longer the stacked one`.
func TestDepthComplexityEstimateAgreesWithTheOfflineMeasurement(t *testing.T) {
	boxes := overdrawField(32, 32)
	draws := make([]RenderObject, 0, len(boxes))
	// One mesh per patch with the patch's own object-space box and an MVP that
	// is the view-projection times a translation, which is how the engine's draw
	// list arrives: the estimate reads BoundMin/BoundMax through MVP, so a
	// fixture that pre-transformed the boxes would not exercise that path.
	mids := make([][3]float32, len(boxes))
	for i, b := range boxes {
		mid := [3]float32{(b[0][0] + b[1][0]) / 2, (b[0][1] + b[1][1]) / 2, (b[0][2] + b[1][2]) / 2}
		mids[i] = mid
		lo := [3]float32{b[0][0] - mid[0], b[0][1] - mid[1], b[0][2] - mid[2]}
		hi := [3]float32{b[1][0] - mid[0], b[1][1] - mid[1], b[1][2] - mid[2]}
		// The sphere as well as the box, because the estimate has a fallback to
		// the sphere for a mesh with no box and a fixture that left the sphere
		// at zero would make that fallback unmeasurable -- a break that swapped
		// to it would read as "nothing is in frame" rather than as the inflation
		// it really is.
		draws = append(draws, RenderObject{Mesh: &Mesh{
			BoundMin:    lo,
			BoundMax:    hi,
			BoundRadius: float32(math.Sqrt(float64(hi[0]*hi[0] + hi[1]*hi[1] + hi[2]*hi[2]))),
			VertexCount: 8,
		}})
	}

	cams := []struct {
		name            string
		eye, at, up     mgl32.Vec3
		fov, tolerance  float32
		wantAtLeast     float32
		wantAtMostAbove float32
	}{
		{
			// The overlap arm: an eye 2.4 m up looking almost along the field.
			name: "grazing", eye: mgl32.Vec3{0, 2.4, -8}, at: mgl32.Vec3{0, 2.39, 0}, up: mgl32.Vec3{0, 1, 0},
			fov: 50, tolerance: 0.03, wantAtLeast: 2.5,
		},
		{
			// The control: the same field from straight above through a narrow
			// lens, where almost nothing hides anything.
			name: "overhead", eye: mgl32.Vec3{0, 304, 62}, at: mgl32.Vec3{0, 0, 62}, up: mgl32.Vec3{0, 0, -1},
			fov: 12, tolerance: 0.03, wantAtMostAbove: 1.2,
		},
	}
	var mask depthComplexityMask
	for _, cam := range cams {
		t.Run(cam.name, func(t *testing.T) {
			proj := mgl32.Perspective(mgl32.DegToRad(cam.fov), 1280.0/720.0, 0.1, 800)
			vpm := proj.Mul4(mgl32.LookAtV(cam.eye, cam.at, cam.up))
			var vp [16]float32
			copy(vp[:], vpm[:])
			projected := make([][2][3]float32, len(boxes))
			for i := range draws {
				mvp := vpm.Mul4(mgl32.Translate3D(mids[i][0], mids[i][1], mids[i][2]))
				copy(draws[i].MVP[:], mvp[:])
				projected[i] = boxes[i]
			}
			got, covered := depthComplexityEstimate(draws, &vp, &mask)
			want := offlineDepthComplexity(projected, &vp, 180)
			t.Logf("%s: estimate %.3f at %d cells, offline %.3f at 180, covered share %.3f",
				cam.name, got, depthComplexityCells, want, covered)
			if want == 0 {
				t.Fatalf("the offline measurement is zero -- this camera sees nothing and the comparison is vacuous")
			}
			rel := math.Abs(float64(got)-want) / want
			if rel > float64(cam.tolerance) {
				t.Errorf("%s: estimate %.3f against the offline %.3f, %.1f %% apart, tolerance %.1f %%",
					cam.name, got, want, rel*100, cam.tolerance*100)
			}
			// The cameras have to still be the cameras they are named after, or
			// the agreement above is an agreement about two numbers that mean
			// nothing. Same argument the example's own arm floors make.
			if cam.wantAtLeast > 0 && got < cam.wantAtLeast {
				t.Errorf("%s: complexity %.3f, need at least %.3f -- this camera is no longer the stacked one", cam.name, got, cam.wantAtLeast)
			}
			if cam.wantAtMostAbove > 0 && got > cam.wantAtMostAbove {
				t.Errorf("%s: complexity %.3f, need at most %.3f -- this camera is no longer the control", cam.name, got, cam.wantAtMostAbove)
			}
		})
	}
}

// Zero allocations per frame, at two scene sizes so a per-draw allocation shows
// up as a difference rather than only as a nonzero.
//
// Verified to fail: a slice of the mask's size allocated per call reports
// `allocs/op: 1 at 64 draws, 1 at 1024`.
//
// The FIRST attempt at that break did not fail, and it is worth recording which:
// `m = new(depthComplexityMask)` passed, because 4.5 KiB that does not escape is
// stack-allocated and never reaches the allocator. So this test does not say
// "the mask is not heap-allocated"; it says "nothing per call escapes", and a
// break has to escape to prove it.
func TestDepthComplexityEstimateAllocates(t *testing.T) {
	var mask depthComplexityMask
	allocs := func(n int) float64 {
		boxes := overdrawField(n, n)
		draws := make([]RenderObject, 0, len(boxes))
		for _, b := range boxes {
			draws = append(draws, ndcBoxDraw(
				[3]float32{b[0][0] / 70, b[0][1] / 70, 0},
				[3]float32{b[1][0] / 70, b[1][1] / 70, 0}))
		}
		depthComplexityEstimate(draws, &identityMVP, &mask) // warm
		return testing.AllocsPerRun(20, func() {
			depthComplexityEstimate(draws, &identityMVP, &mask)
		})
	}
	a, b := allocs(8), allocs(32)
	t.Logf("allocs/op: %v at 64 draws, %v at 1024", a, b)
	if a != 0 || b != 0 {
		t.Errorf("the estimate allocates: %v allocs/op at 64 draws, %v at 1024", a, b)
	}
}

// Auto's hysteresis: an estimate wandering inside the band keeps the decision it
// had, and crossing either edge changes it.
//
// Why the band is not decoration. The mode decides which pipelines the main pass
// binds for every qualifying draw, so a scene sitting on the threshold without a
// band would alternate, pay a pipeline switch per draw on every frame, and make
// two consecutive frames incomparable to each other -- which is worse than
// either answer.
//
// Verified to fail: replacing decide's two-sided test with the single
// `p.active = estimate >= depthPrepassThreshold` reports `inside the band from
// above: step 1 at estimate 2.720 flipped to false` and `crossing both edges:
// step 3 at estimate 2.800: active = false, want true`.
func TestDepthPrepassAutoHysteresis(t *testing.T) {
	th, band := DepthPrepassThreshold()
	if band <= 0 || band >= th {
		t.Fatalf("threshold %.3f and band %.3f: a band of zero is no hysteresis and a band wider than the threshold has no lower edge", th, band)
	}
	// Three points strictly inside the band, which is where the decision has to
	// be the previous one rather than a function of the estimate.
	inside := []float32{th - band*0.9, th - band*0.5, th - band*0.1}

	t.Run("inside the band from below", func(t *testing.T) {
		p := &depthPrepassPipelines{mode: DepthPrepassAuto}
		if p.decide(th-band-0.01, 1) {
			t.Fatal("below the lower edge the prepass must be off")
		}
		for i, e := range inside {
			if p.decide(e, 1) {
				t.Fatalf("inside the band from below: step %d at estimate %.3f flipped to true", i+1, e)
			}
		}
	})
	t.Run("inside the band from above", func(t *testing.T) {
		p := &depthPrepassPipelines{mode: DepthPrepassAuto}
		if !p.decide(th, 1) {
			t.Fatal("at the threshold the prepass must be on")
		}
		for i, e := range inside {
			if !p.decide(e, 1) {
				t.Fatalf("inside the band from above: step %d at estimate %.3f flipped to false", i+1, e)
			}
		}
	})
	t.Run("crossing both edges", func(t *testing.T) {
		p := &depthPrepassPipelines{mode: DepthPrepassAuto}
		for i, step := range []struct {
			estimate float32
			want     bool
		}{
			{th - band - 0.01, false},
			{th, true},
			{th - band*0.5, true}, // back inside the band: stays on
			{th - band - 0.01, false},
			{th - band*0.5, false}, // back inside the band: stays off
			{th + 1, true},
		} {
			if got := p.decide(step.estimate, 1); got != step.want {
				t.Errorf("step %d at estimate %.3f: active = %v, want %v", i+1, step.estimate, got, step.want)
			}
		}
	})
	t.Run("the first frame of a scene inside the band is off", func(t *testing.T) {
		p := &depthPrepassPipelines{mode: DepthPrepassAuto}
		if p.decide(th-band*0.5, 1) {
			t.Error("a renderer's first frame inside the band ran the prepass: the initial decision has to be a decision, not whatever the zero value happens to be")
		}
	})
	t.Run("On and Off ignore the estimate", func(t *testing.T) {
		on := &depthPrepassPipelines{mode: DepthPrepassOn}
		if !on.decide(0, 0) {
			t.Error("DepthPrepassOn declined at estimate 0")
		}
		off := &depthPrepassPipelines{mode: DepthPrepassOff}
		if off.decide(100, 1) {
			t.Error("DepthPrepassOff ran the prepass at estimate 100")
		}
		// Off still records the estimate it was given, so a renderer that has
		// one can report it; it just never acts on it.
		if off.estimate != 100 {
			t.Errorf("DepthPrepassOff dropped the estimate: %.3f", off.estimate)
		}
	})
}

// A frame Auto declined submits none of the prepass's draws, and the stats say
// which way it went.
//
// The node is still in the plan on such a frame and its depth clear still runs --
// the scene pass loads depth whenever the prepass exists, so something has to
// clear it -- so "declined" cannot mean "skip the node". It means this: the
// recorder returns early and `active` is false everywhere the main pass's pipeline
// choice reads it, which is what puts the ordinary Greater variants back.
//
// Verified to fail: deleting `!c.prepass.active ||` from recordDepthPrepass's
// early return reports `auto declined but the prepass submitted 4 draws`. The
// first attempt at this check was the equivalence gate alone, and it did NOT
// catch that break -- a prepass that writes depth the main pass then tests with
// Greater renders the same pixels, so the waste is invisible in an image and only
// a counter can see it.
func TestDepthPrepassInactiveFrameSubmitsNothing(t *testing.T) {
	h := &fakeHandles{}
	draws, qualifying := prepassDrawList(h)
	if qualifying == 0 {
		t.Fatal("the fixture list has nothing to withhold")
	}
	for _, c := range []struct {
		name   string
		active bool
		want   int
	}{
		{"auto declined", false, 0},
		{"auto accepted", true, qualifying},
	} {
		t.Run(c.name, func(t *testing.T) {
			var stats RenderStats
			var scratch commandScratch
			d := &prepassRecorder{}
			fr := &graphFrame{driver: d, cmd: h.commandBuffer(), extent: core1_0.Extent2D{Width: 64, Height: 32},
				scratch: &scratch, stats: &stats, draws: draws,
				prepass: depthPrepassPipelines{depth: h.pipeline(), depthInstanced: h.pipeline(), layout: h.layout(),
					mode: DepthPrepassAuto, active: c.active, estimate: 2.5, covered: 0.4}}
			recordDepthPrepass(fr)
			if stats.PrepassDraws != c.want {
				t.Errorf("%s but the prepass submitted %d draws, want %d", c.name, stats.PrepassDraws, c.want)
			}
			if c.active && len(d.bound) == 0 {
				t.Error("auto accepted and nothing was bound")
			}
			if !c.active && len(d.bound) != 0 {
				t.Errorf("auto declined and %d pipelines were bound", len(d.bound))
			}
			// The estimate and the decision reach the stats either way: a frame
			// that declined is a frame whose reason has to be readable, and the
			// recorder is the only place that can write them (the recorder zeroes
			// the counters at its start, so DrawFrame cannot).
			if stats.PrepassEstimate != 2.5 || stats.PrepassCovered != 0.4 || stats.PrepassActive != c.active {
				t.Errorf("stats report estimate %.3f covered %.3f active %v, want 2.500, 0.400 and %v",
					stats.PrepassEstimate, stats.PrepassCovered, stats.PrepassActive, c.active)
			}
		})
	}
}

// Capabilities reports the mode the renderer was built with, not a bool and not
// the per-frame decision.
//
// Verified to fail: having DepthPrepassAuto stringify as "on" -- the shape a
// bool-era report would have -- reports `DepthPrepassMode(2).String() = "on",
// want "auto"` from TestWithDepthPrepassSetsTheMode, which is where the string
// is pinned.
func TestCapabilitiesReportsTheDepthPrepassMode(t *testing.T) {
	for _, mode := range []DepthPrepassMode{DepthPrepassOff, DepthPrepassOn, DepthPrepassAuto} {
		t.Run(mode.String(), func(t *testing.T) {
			r := &Renderer{depthPrepassMode: mode}
			r.adopt(Capabilities{MSAASamples: 1})
			if got := r.Capabilities().DepthPrepass; got != mode {
				t.Errorf("Capabilities.DepthPrepass = %v, want %v", got, mode)
			}
			// Not part of the device's identity: two builds of the same program
			// differing only in this option must not look like two different
			// GPUs to anything keyed on the ident hash.
			other := &Renderer{depthPrepassMode: DepthPrepassOff}
			other.adopt(Capabilities{MSAASamples: 1})
			if r.deviceIdent != other.deviceIdent {
				t.Errorf("the prepass mode changed the device ident hash: %v against %v", r.deviceIdent, other.deviceIdent)
			}
		})
	}
}

// The option's own mapping, including the one that matters: an unrecognised mode
// value must not create pipelines or a node.
func TestWithDepthPrepassSetsTheMode(t *testing.T) {
	for _, mode := range []DepthPrepassMode{DepthPrepassOff, DepthPrepassOn, DepthPrepassAuto} {
		var r Renderer
		WithDepthPrepass(mode)(&r)
		if r.depthPrepassMode != mode {
			t.Errorf("WithDepthPrepass(%v) left the mode %v", mode, r.depthPrepassMode)
		}
	}
	for _, c := range []struct {
		mode DepthPrepassMode
		want string
	}{
		{DepthPrepassOff, "off"},
		{DepthPrepassOn, "on"},
		{DepthPrepassAuto, "auto"},
		{DepthPrepassMode(99), "unknown"},
	} {
		// Pinned because the string is what reaches every log line, every
		// example's report and the capability dump: an Auto build that announced
		// itself as "on" would make a run of the wrong mode indistinguishable
		// from a run of the right one in the record afterwards.
		if got := c.mode.String(); got != c.want {
			t.Errorf("DepthPrepassMode(%d).String() = %q, want %q", c.mode, got, c.want)
		}
	}
}

// A mesh with no box falls back to its bounding sphere rather than contributing
// nothing. Nothing would be the silent failure: a scene of such meshes would read
// as empty and hold Auto off for ever.
func TestDepthComplexityEstimateFallsBackToTheSphere(t *testing.T) {
	// No box (BoundMin == BoundMax), a unit sphere at the origin, which under the
	// identity is exactly the viewport.
	mesh := &Mesh{BoundCenter: [3]float32{0, 0, 0}, BoundRadius: 1, VertexCount: 8}
	draws := []RenderObject{{Mesh: mesh, MVP: identityMVP}}
	var mask depthComplexityMask
	complexity, covered := depthComplexityEstimate(draws, &identityMVP, &mask)
	if math.Abs(float64(complexity-1)) > 1e-5 || math.Abs(float64(covered-1)) > 1e-5 {
		t.Errorf("a box-less mesh reads complexity %.3f covered %.3f, want 1.000 and 1.000", complexity, covered)
	}
	// And a mesh with neither bound contributes nothing at all, rather than a
	// degenerate rectangle at the origin.
	none := []RenderObject{{Mesh: &Mesh{VertexCount: 8}, MVP: identityMVP}}
	if c, cv := depthComplexityEstimate(none, &identityMVP, &mask); c != 0 || cv != 0 {
		t.Errorf("a mesh with no bounds at all reads complexity %.3f covered %.3f, want zero", c, cv)
	}
}

// Every Vulkan object createDepthPrepassPipelines makes is given back, including
// when a creation call fails part way through.
//
// The prepass is five pipelines and three pipeline layouts, each pushed onto the
// init stack as it is made (AGENTS.md rule 10). A failure at the fourth has to
// leave the first three destroyed, and the one kind of mistake that is invisible
// without this is a teardown closure that destroys the WRONG field -- which
// still balances the count, so the kind-by-kind comparison below is paired with
// the meta-check at the end that proves it can fail at all.
//
// Verified to fail: dropping the DestroyPipelineLayout from the lit twin's
// teardown closure reports `PipelineLayout: created 3 destroyed 2` on the control
// and at ten of the eighteen injection points past it.
func TestDepthPrepassPipelineCreationUnwinds(t *testing.T) {
	create := func(d *resizeFakeDriver) (*Renderer, error) {
		r := newResizeFixture(d, 3)
		r.depthPrepassMode = DepthPrepassAuto
		r.shaders = DefaultShaders()
		r.depth = &depthResources{format: core1_0.FormatD32SignedFloat}
		r.sceneFormats = colorDepthFormats(hdrFormat, core1_0.FormatD32SignedFloat)
		r.litPipelineLayout = d.h.layout()
		r.materialSetLayout = d.h.descriptorSetLayout()
		r.shadow.descriptorSetLayout = d.h.descriptorSetLayout()
		return r, r.createDepthPrepassPipelines()
	}
	balance := func(tb testing.TB, d *resizeFakeDriver) {
		assertBalanced(tb, d)
		for _, kind := range []string{"Pipeline", "PipelineLayout", "ShaderModule"} {
			if d.created[kind] != d.destroyed[kind] {
				tb.Errorf("%s: created %d destroyed %d", kind, d.created[kind], d.destroyed[kind])
			}
		}
	}
	control := newResizeFakeDriver()
	r, err := create(control)
	if err != nil {
		t.Fatal(err)
	}
	// Five pipelines, or this is not testing what it names.
	if control.created["Pipeline"] != 5 {
		t.Fatalf("the prepass created %d pipelines, want 5", control.created["Pipeline"])
	}
	r.unwindInit()
	balance(t, control)

	for _, call := range []string{"CreateShaderModule", "CreatePipelineLayout", "CreateGraphicsPipelines"} {
		if control.calls[call] == 0 {
			t.Fatalf("no sites for %s", call)
		}
		for at := 1; at <= control.calls[call]; at++ {
			t.Run(fmt.Sprintf("%s/%d", call, at), func(t *testing.T) {
				d := newResizeFakeDriver()
				d.failCall, d.failAt = call, at
				r, err := create(d)
				if !errors.Is(err, errInjected) {
					t.Fatalf("error: %v", err)
				}
				r.unwindInit()
				balance(t, d)
			})
		}
		t.Logf("%s: all %d prepass creation sites unwind without leaks", call, control.calls[call])
	}
	for kind, count := range control.created {
		if count == 0 {
			continue
		}
		control.destroyed[kind]--
		captured := &capturingT{TB: t}
		balance(captured, control)
		control.destroyed[kind]++
		if !captured.failed {
			t.Fatalf("balance meta-check missed %s", kind)
		}
	}
	t.Log("balance meta-check detects a missing destroy for every kind the prepass's creation touches")
}

// What the estimate actually costs per frame, because the bench cannot tell.
//
// On the overdraw control arm, cpu_record rose 0.18 to 0.54 ms with Auto on
// against Auto off -- inside that cell's scatter, so clause 2 of the rule
// passes, but positive on all six paired trials, which is the pattern this
// repository's records treat as a real cost rather than noise. cpu_record also
// contains the second geometry submission on the arms where the prepass runs, so
// it cannot separate the two. This can.
//
// 1024 draws is the overdraw baseline's field. The overhead case is its control
// arm, where each patch covers a few grid cells; the grazing case is the overlap
// arm, where the near rows are stretched across most of the grid and the row
// loop does its most work.
func BenchmarkDepthComplexityEstimate(b *testing.B) {
	for _, c := range []struct {
		name  string
		scale float32
	}{
		{"overhead-1024", 1},
		{"grazing-1024", 0.08},
	} {
		b.Run(c.name, func(b *testing.B) {
			boxes := overdrawField(32, 32)
			draws := make([]RenderObject, 0, len(boxes))
			for _, bx := range boxes {
				// Squeezed into NDC so the whole field is on screen, and
				// stretched in y by 1/scale for the grazing case so each box
				// spans many grid rows.
				draws = append(draws, ndcBoxDraw(
					[3]float32{bx[0][0] / 70, bx[0][2]/70*c.scale - 0.5, 0},
					[3]float32{bx[1][0] / 70, bx[1][2]/70*c.scale - 0.5 + 0.1/c.scale, 0}))
			}
			var mask depthComplexityMask
			complexity, covered := depthComplexityEstimate(draws, &identityMVP, &mask)
			b.ReportMetric(float64(complexity), "complexity")
			b.ReportMetric(float64(covered), "covered")
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				depthComplexityEstimate(draws, &identityMVP, &mask)
			}
		})
	}
}
