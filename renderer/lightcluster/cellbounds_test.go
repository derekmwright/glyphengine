package lightcluster

import (
	"math"
	"math/rand/v2"
	"testing"

	"github.com/go-gl/mathgl/mgl32"
)

// TestCellBoundsContainTheirFroxel is the invariant the whole refined path
// rests on: the cached box for a cell contains every view-space point the
// mapping puts in that cell. If it does, then a point inside a light and inside
// the frustum is inside its own cell's box, so the sphere-against-box test
// cannot reject the cell that point is in — which is the conservative argument,
// with no reference to how the box was built.
//
// The oracle catches a box that is slightly too small too, but barely: a 10%
// shrink cost it one point per scenario, because it only bites when a sphere
// touches a cell near its edge and nowhere else. This test is the sharp version
// of the same question. Broken on purpose, at the commit that added it, over
// the four parameter sets below (20000 points each):
//
//	axisExtent shrinks the extent 10% towards its centre
//	  -> FAIL, 3962 / 3195 / 2914 / 4003 points outside their own cell's box
//	     (the oracle caught the same change with one point per scenario)
//	sliceDepths drops the near-plane clamp on slice 0
//	  -> FAIL, 4336 / 3626 / 0 / 4336. Slice 0 holds every fragment in front
//	     of its upper boundary, not just the ones the log formula names, so its
//	     box has to start at the near plane. The zero is the parameter set with
//	     SliceStart == Near, where the clamp is a no-op and nothing should
//	     change — which is the part that says the failure is the real one and
//	     not the test flinching at any edit.
//	tileNDC drops the pixel margin
//	  -> PASS: the margin is for the shader's float32 rounding and for MSAA
//	     centroid displacement, neither of which this test reproduces, so it
//	     is not load bearing here and is kept for what pixelMargin says
func TestCellBoundsContainTheirFroxel(t *testing.T) {
	for _, p := range cellBoundsCases() {
		p = p.normalized()
		m := NewMapping(p)
		var fb froxelBounds
		fb.update(p, m)
		if !fb.ok {
			t.Fatalf("%v: no cell bounds for a plain perspective", p.Grid)
		}
		ax, ay := float64(p.Proj[0]), float64(p.Proj[5])
		w, h := float64(p.Width), float64(p.Height)
		logRange := math.Log(float64(p.Far) / float64(p.Near))

		rng := rand.New(rand.NewPCG(41, 43))
		outside := 0
		for i := 0; i < 20000; i++ {
			px := rng.Float64() * w
			py := rng.Float64() * h
			// Log-uniform over the whole rendered depth range, so the near
			// slices get as many samples as the far ones.
			d := float64(p.Near) * math.Exp(rng.Float64()*logRange)
			if i%5 == 0 {
				// Land exactly on froxel boundaries, where a box that stops a
				// shade too early shows up first.
				px = float64(rng.IntN(p.Grid.X+1)) * w / float64(p.Grid.X)
				py = float64(rng.IntN(p.Grid.Y+1)) * h / float64(p.Grid.Y)
				d = math.Exp((float64(rng.IntN(p.Grid.Z+1)) - float64(m.SliceBias)) / float64(m.SliceScale))
				if d < float64(p.Near) || d > float64(p.Far) {
					continue
				}
			}

			x, y, z := m.CellCoords(float32(px), float32(py), float32(d))
			// The view-space point that projects to exactly that fragment.
			v := mgl32.Vec3{
				float32((px/w*2 - 1) * d / ax),
				float32((py/h*2 - 1) * d / ay),
				float32(-d),
			}
			bad := !within(v[0], fb.x[z*p.Grid.X+x]) ||
				!within(v[1], fb.y[z*p.Grid.Y+y]) ||
				!within(v[2], fb.z[z])
			if bad {
				outside++
				if outside <= 3 {
					t.Errorf("%v %dx%d: view point %v at pixel (%.1f,%.1f) depth %.4f is in "+
						"cell (%d,%d,%d) but outside its box x%v y%v z%v",
						p.Grid, p.Width, p.Height, v, px, py, d, x, y, z,
						fb.x[z*p.Grid.X+x], fb.y[z*p.Grid.Y+y], fb.z[z])
				}
			}
		}
		if outside > 0 {
			t.Errorf("%v %dx%d: %d of 20000 points fell outside their own cell's box",
				p.Grid, p.Width, p.Height, outside)
		}
	}
}

func cellBoundsCases() []Params {
	_, colony := colonyScene(1, 4, 6, 0.22)
	odd := colony
	odd.Grid = Grid{X: 7, Y: 5, Z: 11}
	odd.Width, odd.Height = 801, 603
	odd.Proj = reverseZProjection(75, 801.0/603.0, 0.25, 120)
	odd.Near, odd.Far = 0.25, 120
	atNear := colony
	atNear.SliceStart = atNear.Near
	wide := colony
	wide.Grid = Grid{X: 32, Y: 18, Z: 48}
	return []Params{colony, odd, atNear, wide}
}

func within(v float32, b [2]float32) bool { return v >= b[0] && v <= b[1] }

// TestCellBoundsCacheKeyedOnProjectionNotView: the extents are in view space
// and must not depend on where the camera is, or the cache would rebuild every
// frame and Build would allocate in the steady state.
func TestCellBoundsCacheKeyedOnProjectionNotView(t *testing.T) {
	_, p := colonyScene(1, 4, 6, 0.22)
	p = p.normalized()
	m := NewMapping(p)
	var fb froxelBounds
	fb.update(p, m)
	first := append([][2]float32(nil), fb.x...)

	moved := p
	moved.View = mgl32.LookAtV(mgl32.Vec3{40, 9, -3}, mgl32.Vec3{1, 2, 3}, mgl32.Vec3{0, 1, 0})
	if n := testing.AllocsPerRun(10, func() { fb.update(moved, NewMapping(moved)) }); n != 0 {
		t.Errorf("moving the camera rebuilt the cell bounds %v times per frame", n)
	}
	for i := range first {
		if fb.x[i] != first[i] {
			t.Fatalf("cell bounds changed when only the view matrix did, at %d", i)
		}
	}

	// A different projection must rebuild them, or a changed field of view
	// would be binned against the old grid.
	zoomed := p
	zoomed.Proj = reverseZProjection(20, 1920.0/1080.0, 0.1, 500)
	fb.update(zoomed, NewMapping(zoomed))
	same := true
	for i := range first {
		if fb.x[i] != first[i] {
			same = false
			break
		}
	}
	if same {
		t.Error("changing the field of view did not rebuild the cell bounds")
	}
}

// TestNonPerspectiveProjectionBinsEverything: without ax and ay there is no way
// to turn a tile edge into a view-space plane, so the cell test must not be
// able to reject anything. Infinite extents express that, and this is the check
// that they really are infinite rather than zero, which would reject
// everything and put holes in every light.
func TestNonPerspectiveProjectionBinsEverything(t *testing.T) {
	_, p := colonyScene(1, 4, 6, 0.22)
	p.Proj[8] = 0.3 // an off-centre frustum: clip.x now depends on view z
	lights := []Light{{Pos: mgl32.Vec3{0, 0.55, 0}, Range: 4}}

	res := New().Build(lights, p)
	if res.Stats.UnboundedLights != 1 {
		t.Fatalf("stats = %+v, want the light to have no screen bound", res.Stats)
	}
	if res.Stats.CellsBinned != res.Stats.CellsTested {
		t.Errorf("binned %d of %d candidate cells; the cell test rejected something "+
			"it has no information to reject", res.Stats.CellsBinned, res.Stats.CellsTested)
	}
	if res.Stats.ScreenWideLights != 1 {
		t.Errorf("ScreenWideLights = %d, want 1", res.Stats.ScreenWideLights)
	}
	checkConsistent(t, res)
}
