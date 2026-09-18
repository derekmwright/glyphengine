package lightcluster

import (
	"math"
	"math/rand/v2"
	"testing"

	"github.com/go-gl/mathgl/mgl32"
)

// reverseZProjection is app.go's, copied rather than imported.
//
// The root package needs cgo and a Vulkan loader, and this package must build
// and test on a CI box with neither; importing it would also be a cycle once
// the renderer calls the binner. Copied code can drift, so the tests below
// check the two properties the binner actually depends on (clip w is the
// positive view depth, and the depth planes are where Near and Far say) rather
// than that the elements match.
func reverseZProjection(fovDegrees, aspect, near, far float32) mgl32.Mat4 {
	proj := mgl32.Perspective(mgl32.DegToRad(fovDegrees), aspect, near, far)
	proj[10] = near / (far - near)
	proj[14] = (far * near) / (far - near)
	proj[5] *= -1
	return proj
}

// TestClipWIsPositiveViewDepth pins the fact the whole depth slicing rests on:
// the shader can get its view depth from 1.0/gl_FragCoord.w without knowing
// anything about near, far or the reverse-Z mapping.
//
// It also checks the reverse-Z mapping itself, so that a change to
// reverseZProjection that moved depth back to 0->1 would be caught here rather
// than by a black screen.
func TestClipWIsPositiveViewDepth(t *testing.T) {
	for _, tc := range []struct {
		fov, aspect, near, far float32
	}{
		{60, 16.0 / 9.0, 0.1, 500},
		{90, 1, 0.01, 10},
		{35, 21.0 / 9.0, 1, 10000},
	} {
		proj := reverseZProjection(tc.fov, tc.aspect, tc.near, tc.far)

		if proj[11] != -1 {
			t.Fatalf("proj[11] = %v, want -1: clip w is not the negated view z", proj[11])
		}
		rng := rand.New(rand.NewPCG(1, 2))
		for i := 0; i < 1000; i++ {
			depth := tc.near + rng.Float32()*(tc.far-tc.near)
			v := mgl32.Vec4{
				(rng.Float32()*2 - 1) * 100,
				(rng.Float32()*2 - 1) * 100,
				-depth,
				1,
			}
			clip := proj.Mul4x1(v)
			if clip[3] != depth {
				t.Fatalf("clip w = %v, want view depth %v", clip[3], depth)
			}
			if got := 1 / (1 / clip[3]); math.Abs(float64(got-depth)) > float64(depth)*1e-6 {
				t.Fatalf("1/(1/w) = %v, want %v", got, depth)
			}
		}

		// Reverse-Z: the near plane is z=1 and the far plane z=0.
		atDepth := func(d float32) float32 {
			clip := proj.Mul4x1(mgl32.Vec4{0, 0, -d, 1})
			return clip[2] / clip[3]
		}
		if z := atDepth(tc.near); math.Abs(float64(z-1)) > 1e-5 {
			t.Errorf("ndc z at near = %v, want 1", z)
		}
		if z := atDepth(tc.far); math.Abs(float64(z)) > 1e-5 {
			t.Errorf("ndc z at far = %v, want 0", z)
		}
		mid := tc.near + (tc.far-tc.near)/2
		if atDepth(mid) >= atDepth(tc.near) || atDepth(mid) <= atDepth(tc.far) {
			t.Errorf("ndc z is not decreasing with depth: %v %v %v",
				atDepth(tc.near), atDepth(mid), atDepth(tc.far))
		}
	}
}

// TestViewFrustumPlanesAreNearAndFar checks that the planes the cull uses come
// out where Near and Far say, with no assumption about which of the two depth
// planes reverse-Z makes the near one.
func TestViewFrustumPlanesAreNearAndFar(t *testing.T) {
	const near, far = 0.1, 500
	proj := reverseZProjection(60, 16.0/9.0, near, far)
	planes := viewFrustumPlanes(proj)

	// On the view axis, a point at depth d is inside every plane iff
	// near <= d <= far.
	inside := func(d float32) bool {
		return sphereInFrustum(&planes, mgl32.Vec3{0, 0, -d}, 0)
	}
	for _, tc := range []struct {
		d    float32
		want bool
	}{
		{0.05, false}, {near * 1.001, true}, {1, true}, {499, true},
		{far * 1.001, false}, {5000, false}, {-1, false},
	} {
		if got := inside(tc.d); got != tc.want {
			t.Errorf("depth %v inside frustum = %v, want %v", tc.d, got, tc.want)
		}
	}

	// And a sphere straddling a plane is kept.
	if !sphereInFrustum(&planes, mgl32.Vec3{0, 0, -0.05}, 0.2) {
		t.Error("a sphere straddling the near plane was culled")
	}
	if !sphereInFrustum(&planes, mgl32.Vec3{0, 0, 1}, 2) {
		t.Error("a sphere behind the eye but reaching past the near plane was culled")
	}
	if sphereInFrustum(&planes, mgl32.Vec3{0, 0, 1}, 0.5) {
		t.Error("a sphere entirely behind the eye was kept")
	}
}

// scenario is a camera plus a light set for the oracle to chew on.
type scenario struct {
	name   string
	params Params
	lights []Light
}

func viewMatrix(eye, center mgl32.Vec3) mgl32.Mat4 {
	return mgl32.LookAtV(eye, center, mgl32.Vec3{0, 1, 0})
}

// unproject turns normalized device coordinates plus a view depth back into a
// world point, which is how the oracle samples points that are certainly inside
// the frustum.
func unproject(p Params, ndcX, ndcY, depth float32) mgl32.Vec3 {
	view := mgl32.Vec3{ndcX * depth / p.Proj[0], ndcY * depth / p.Proj[5], -depth}
	inv := p.View.Inv()
	return inv.Mul4x1(view.Vec4(1)).Vec3()
}

// project does what the GPU does: transform to clip space, divide, and land on
// a framebuffer pixel. gl_FragCoord.xy is this, and gl_FragCoord.w is 1/depth.
func project(vp mgl32.Mat4, p Params, w mgl32.Vec3) (px, py, depth float32, visible bool) {
	clip := vp.Mul4x1(w.Vec4(1))
	if !(clip[3] > 0) {
		return 0, 0, 0, false
	}
	depth = clip[3]
	px = (clip[0]/depth*0.5 + 0.5) * float32(p.Width)
	py = (clip[1]/depth*0.5 + 0.5) * float32(p.Height)
	visible = px >= 0 && px <= float32(p.Width) && py >= 0 && py <= float32(p.Height) &&
		depth >= p.Near && depth <= p.Far
	return px, py, depth, visible
}

// lit is the reference light test: exactly what the shader's attenuation and
// cone factor make non-zero, and nothing else.
func lit(l Light, pt mgl32.Vec3) bool {
	d := pt.Sub(l.Pos)
	dist := d.Len()
	if !(dist <= l.Range) {
		return false
	}
	if l.Dir.LenSqr() < 0.999 {
		return true // point light
	}
	if dist < 1e-6 {
		return true // at the light itself, the cone has no direction to test
	}
	return d.Mul(1/dist).Dot(l.Dir) >= l.CosOuter
}

func randomUnit(rng *rand.Rand) mgl32.Vec3 {
	for {
		v := mgl32.Vec3{
			float32(rng.NormFloat64()),
			float32(rng.NormFloat64()),
			float32(rng.NormFloat64()),
		}
		if l := v.Len(); l > 1e-3 {
			return v.Mul(1 / l)
		}
	}
}

// makeLights builds a light set that hits every case the bound has to survive:
// tiny lights, lights bigger than the whole scene, lights containing the
// camera, lights straddling the near plane, lights behind the eye, lights past
// the far plane, and spots from needle-thin to nearly hemispherical.
func makeLights(rng *rand.Rand, n int, p Params) []Light {
	eye := cameraPosition(p.View)
	fwd := p.View.Inv().Mul4x1(mgl32.Vec4{0, 0, -1, 0}).Vec3()
	lights := make([]Light, 0, n)
	for i := 0; i < n; i++ {
		var pos mgl32.Vec3
		var rng2 float32
		switch i % 8 {
		case 0: // somewhere in the frustum, small
			pos = unproject(p, rng.Float32()*2-1, rng.Float32()*2-1, p.Near+rng.Float32()*60)
			rng2 = 0.05 + rng.Float32()*3
		case 1: // somewhere in the frustum, medium
			pos = unproject(p, rng.Float32()*2-1, rng.Float32()*2-1, p.Near+rng.Float32()*200)
			rng2 = 5 + rng.Float32()*20
		case 2: // huge, covering much of the view
			pos = unproject(p, rng.Float32()*2-1, rng.Float32()*2-1, 10+rng.Float32()*200)
			rng2 = 100 + rng.Float32()*150
		case 3: // containing the camera
			pos = eye.Add(randomUnit(rng).Mul(rng.Float32() * 5))
			rng2 = 6 + rng.Float32()*30
		case 4: // straddling the near plane, off to one side
			pos = eye.Add(fwd.Mul(0.05 + rng.Float32()*0.3)).Add(randomUnit(rng).Mul(rng.Float32() * 8))
			rng2 = 0.5 + rng.Float32()*10
		case 5: // behind the eye, some of them reaching back into view
			pos = eye.Sub(fwd.Mul(1 + rng.Float32()*30))
			rng2 = rng.Float32() * 20
		case 6: // beyond the far plane, some reaching back in
			pos = unproject(p, rng.Float32()*2-1, rng.Float32()*2-1, p.Far*0.95).
				Add(fwd.Mul(rng.Float32() * 100))
			rng2 = rng.Float32() * 60
		case 7: // off to the side of the frustum entirely
			pos = eye.Add(fwd.Mul(10 + rng.Float32()*100)).
				Add(randomUnit(rng).Mul(50 + rng.Float32()*200))
			rng2 = 1 + rng.Float32()*50
		}
		l := Light{Pos: pos, Range: rng2}
		if i%3 == 0 { // a third of them are spots
			l.Dir = randomUnit(rng)
			// Outer half-angle from 3 to 85 degrees, so both the sector-sphere
			// bound and the fall-back-to-range-sphere branch get exercised.
			angle := (3 + rng.Float32()*82) * math.Pi / 180
			l.CosOuter = float32(math.Cos(float64(angle)))
		}
		lights = append(lights, l)
	}
	return lights
}

func oracleScenarios(t testing.TB) []scenario {
	t.Helper()
	rng := rand.New(rand.NewPCG(0x5eed, 0xc1a5))
	var out []scenario
	for _, cam := range []struct {
		name          string
		eye, center   mgl32.Vec3
		w, h          int
		fov           float32
		near, far     float32
		grid          Grid
		sliceStart    float32
		lightCount    int
		aspectFromWxH bool
	}{
		{
			name: "level 16x9x24 1280x720", eye: mgl32.Vec3{0, 2, 0}, center: mgl32.Vec3{20, 2, 3},
			w: 1280, h: 720, fov: 60, near: 0.1, far: 500, grid: DefaultGrid, lightCount: 64,
		},
		{
			name: "looking down 1920x1080", eye: mgl32.Vec3{-30, 30, 40}, center: mgl32.Vec3{0, 0, 0},
			w: 1920, h: 1080, fov: 70, near: 0.1, far: 500, grid: DefaultGrid, lightCount: 64,
		},
		{
			name: "odd grid 7x5x11, odd size 801x603", eye: mgl32.Vec3{5, 1, -8}, center: mgl32.Vec3{-3, 4, 2},
			w: 801, h: 603, fov: 45, near: 0.25, far: 120, grid: Grid{X: 7, Y: 5, Z: 11}, lightCount: 56,
		},
		{
			name: "slice start at near", eye: mgl32.Vec3{0, 0.5, 0}, center: mgl32.Vec3{0, 0.5, -10},
			w: 1024, h: 768, fov: 90, near: 0.05, far: 2000, grid: Grid{X: 20, Y: 12, Z: 32},
			sliceStart: 0.05, lightCount: 56,
		},
	} {
		aspect := float32(cam.w) / float32(cam.h)
		p := Params{
			View:       viewMatrix(cam.eye, cam.center),
			Proj:       reverseZProjection(cam.fov, aspect, cam.near, cam.far),
			Width:      cam.w,
			Height:     cam.h,
			Near:       cam.near,
			Far:        cam.far,
			SliceStart: cam.sliceStart,
			Grid:       cam.grid,
		}
		out = append(out, scenario{name: cam.name, params: p, lights: makeLights(rng, cam.lightCount, p)})
	}
	return out
}

// samplePoints builds the world points the oracle checks. Two thirds of them
// sit on and just inside the boundary of some light, because that is where a
// bound that is slightly too small stops covering and a uniform sample over the
// frustum would almost never land there.
func samplePoints(rng *rand.Rand, s scenario) []mgl32.Vec3 {
	p := s.params
	pts := make([]mgl32.Vec3, 0, len(s.lights)*18+2000)

	for i := range s.lights {
		l := &s.lights[i]
		for k := 0; k < 6; k++ {
			// Uniformly inside the range sphere.
			t := l.Range * float32(math.Cbrt(rng.Float64()))
			pts = append(pts, l.Pos.Add(randomUnit(rng).Mul(t)))
			// Just inside the range boundary, in a random direction.
			pts = append(pts, l.Pos.Add(randomUnit(rng).Mul(l.Range*0.9999)))
			// Just inside the cone boundary, where a cone bound that is too
			// tight fails first.
			if l.Dir.LenSqr() > 0.5 {
				axis := l.Dir
				perp := axis.Cross(randomUnit(rng))
				if perp.Len() < 1e-4 {
					perp = axis.Cross(mgl32.Vec3{1, 0, 0})
				}
				perp = perp.Normalize()
				a := float32(math.Acos(float64(clamp1(l.CosOuter)))) * 0.999
				dir := axis.Mul(float32(math.Cos(float64(a)))).
					Add(perp.Mul(float32(math.Sin(float64(a)))))
				pts = append(pts, l.Pos.Add(dir.Mul(l.Range*(0.05+0.9*rng.Float32()))))
			} else {
				pts = append(pts, l.Pos.Add(randomUnit(rng).Mul(l.Range*rng.Float32())))
			}
		}
	}

	// Plus a spread over the frustum itself, including its exact edges and the
	// froxel boundaries, where rounding decides the cell.
	m := NewMapping(p)
	for i := 0; i < 2000; i++ {
		var ndcX, ndcY, depth float32
		switch i % 4 {
		case 0:
			ndcX, ndcY = rng.Float32()*2-1, rng.Float32()*2-1
			depth = p.Near * float32(math.Exp(rng.Float64()*math.Log(float64(p.Far/p.Near))))
		case 1: // exactly on a tile boundary
			ndcX = float32(rng.IntN(p.Grid.X+1))/float32(p.Grid.X)*2 - 1
			ndcY = float32(rng.IntN(p.Grid.Y+1))/float32(p.Grid.Y)*2 - 1
			depth = p.Near + rng.Float32()*(p.Far-p.Near)
		case 2: // exactly on a slice boundary
			ndcX, ndcY = rng.Float32()*2-1, rng.Float32()*2-1
			k := float32(rng.IntN(p.Grid.Z + 1))
			depth = float32(math.Exp(float64((k - m.SliceBias) / m.SliceScale)))
		case 3: // the frustum edges and corners
			ndcX, ndcY = float32(rng.IntN(3)-1), float32(rng.IntN(3)-1)
			depth = p.Near * float32(math.Exp(rng.Float64()*math.Log(float64(p.Far/p.Near))))
		}
		if !(depth >= p.Near && depth <= p.Far) {
			continue
		}
		pts = append(pts, unproject(p, ndcX, ndcY, depth))
	}
	return pts
}

func clamp1(v float32) float32 {
	if v > 1 {
		return 1
	}
	if v < -1 {
		return -1
	}
	return v
}

// TestOracleNoFalseNegatives is gate G3: for a visible world point, every light
// that reaches it is listed in that point's cell.
//
// This is the check the whole package exists to pass, so it is the one most
// worth breaking on purpose. Each of these was applied to build.go alone, with
// the test unchanged, at the commit that added it (misses are per scenario, in
// the order the scenarios run, out of 16189/14578/18229/16276 lit pairs):
//
//	influenceSphere returns radius*0.9
//	  -> FAIL 31/26/15/38. e.g. cell (9,3,13) at pixel (754,297) depth 29.3
//	     omits a point light at (-3.1 1.9 2.2) range 32.4.
//	naive screen rect (projected centre +/- radius/depth) for tangentBounds
//	  -> FAIL 1/19/0/12. Small because it only bites off axis and this grid is
//	     16 tiles wide; on screen it is a seam down one side of a light.
//	bin only the depth slice of the sphere's centre
//	  -> FAIL 13103/12136/14203/15088.
//	drop the "sphere is in front of the near plane" test that widens to the
//	whole screen, leaving tangentBounds' own refusal to catch it
//	  -> PASS. The refusal (z*z <= r*r) catches every sphere that crosses the
//	     eye plane, so the explicit test is defence in depth for a sphere
//	     behind the eye that the frustum cull kept. Both are kept: with the
//	     refusal ALSO removed (den and disc clamped to non-negative instead of
//	     returning false) it is FAIL 14702/13794/17029/15306, so something has
//	     to hold that line.
//	drop the one-pixel margin (pixelMargin = 0)
//	  -> PASS. The margin is for float32-vs-float64 rounding and MSAA centroid
//	     displacement, neither of which this sampling reproduces; it is not
//	     load bearing for the geometry and is kept for what pixelMargin says.
//	use the range sphere for spots instead of the sector sphere
//	  -> PASS, as it must: that bound is strictly larger, only slower.
//
// The exact counts move with the seed. What matters is that a bound that is
// too small in any of the three axes fails loudly, and that the two changes
// that only cost speed do not.
func TestOracleNoFalseNegatives(t *testing.T) {
	b := New()
	rng := rand.New(rand.NewPCG(7, 11))
	totalLitPairs := 0

	for _, s := range oracleScenarios(t) {
		res := b.Build(s.lights, s.params)

		// The oracle is only an oracle if nothing was dropped: with a budget
		// drop or a cell overflow every miss below would have an excuse, and a
		// test with an excuse for every failure is not a test.
		if res.Stats.DroppedOverBudget != 0 || res.Stats.CellsOverflowed != 0 || res.Stats.CellsTruncated != 0 {
			t.Fatalf("%s: scenario is not a clean oracle: %+v", s.name, res.Stats)
		}
		uploaded := map[int32]int{}
		for i, idx := range res.Order {
			uploaded[idx] = i
		}

		vp := s.params.Proj.Mul4(s.params.View)
		m := res.Mapping
		misses := 0
		litPairs := 0
		for _, pt := range samplePoints(rng, s) {
			px, py, depth, visible := project(vp, s.params, pt)
			if !visible {
				continue
			}
			cell := res.Cells[m.CellIndex(px, py, depth)]
			listed := res.Indices[cell.Offset : cell.Offset+cell.Count]

			for i := range s.lights {
				if !lit(s.lights[i], pt) {
					continue
				}
				litPairs++
				pos, ok := uploaded[int32(i)]
				if !ok {
					t.Fatalf("%s: light %d lights a visible point but was culled", s.name, i)
				}
				found := false
				for _, li := range listed {
					if li == uint32(pos) {
						found = true
						break
					}
				}
				if !found {
					misses++
					if misses <= 3 {
						x, y, z := m.CellCoords(px, py, depth)
						t.Errorf("%s: cell (%d,%d,%d) at pixel (%.1f,%.1f) depth %.3f "+
							"omits light %d %+v which lights point %v",
							s.name, x, y, z, px, py, depth, i, s.lights[i], pt)
					}
				}
			}
		}
		if misses > 0 {
			t.Errorf("%s: %d false negatives out of %d lit point-light pairs", s.name, misses, litPairs)
		}
		// Guard against the test quietly checking nothing, which is how a
		// green oracle stops meaning anything: this repo has shipped a
		// prediction test that compared a value to itself and a teardown gate
		// that inspected nothing.
		if litPairs < 2000 {
			t.Errorf("%s: only %d lit pairs sampled, too few to prove anything", s.name, litPairs)
		}
		totalLitPairs += litPairs
	}
	t.Logf("checked %d lit point-light pairs", totalLitPairs)
}

// TestTangentBoundsBeatsNaiveRect is the trap tangentBounds exists for: off
// axis, the projection of a sphere is not centred on the projection of its
// centre, so centre +/- radius/depth is NOT conservative.
//
// The numbers are worked by hand so a change to tangentBounds cannot quietly
// redefine what it is being compared against: a unit sphere at view (5,0,-10)
// under a projection with proj[0]=1 spans ndc x from tan(atan(0.5)-asin(1/
// sqrt(125))) = 0.39252 to tan(atan(0.5)+asin(1/sqrt(125))) = 0.61752, while
// the naive rect gives 0.4 to 0.6 -- short by 1.9% of the screen width on the
// left and 3.7% on the right, which at 16 tiles is most of a tile at each end.
func TestTangentBoundsBeatsNaiveRect(t *testing.T) {
	const u, z, r = 5.0, -10.0, 1.0
	lo, hi, ok := tangentBounds(1, u, z, r)
	if !ok {
		t.Fatal("tangentBounds refused a sphere in front of the eye")
	}
	wantLo := math.Tan(math.Atan(0.5) - math.Asin(1/math.Sqrt(125)))
	wantHi := math.Tan(math.Atan(0.5) + math.Asin(1/math.Sqrt(125)))
	if math.Abs(lo-wantLo) > 1e-9 || math.Abs(hi-wantHi) > 1e-9 {
		t.Fatalf("tangentBounds = [%v %v], want [%v %v]", lo, hi, wantLo, wantHi)
	}
	naiveLo, naiveHi := u/-z-r/-z, u/-z+r/-z
	if !(lo < naiveLo && hi > naiveHi) {
		t.Fatalf("the naive rect [%v %v] is not inside the exact bound [%v %v]; "+
			"the test is no longer testing the trap", naiveLo, naiveHi, lo, hi)
	}

	// On axis the two agree, which is why the naive version looks right when
	// you test it with the light in front of you.
	lo, hi, _ = tangentBounds(1, 0, z, r)
	if math.Abs(lo+0.1005037815259) > 1e-9 || math.Abs(hi-0.1005037815259) > 1e-9 {
		t.Fatalf("on-axis bound = [%v %v], want +/-tan(asin(0.1))", lo, hi)
	}

	// And it refuses, rather than returning nonsense, when the sphere contains
	// the eye or crosses its plane.
	if _, _, ok := tangentBounds(1, 0, -0.5, 1); ok {
		t.Error("tangentBounds accepted a sphere containing the eye")
	}
	if _, _, ok := tangentBounds(1, 3, -1, 1.5); ok {
		t.Error("tangentBounds accepted a sphere crossing the eye plane")
	}
}

// TestSectorSphereContainsCone checks the spot bound directly: every point of
// the cone is inside the sphere influenceSphere returns. A bound that is too
// small here is exactly a tile-shaped hole at the edge of a spotlight.
func TestSectorSphereContainsCone(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	for _, deg := range []float64{1, 5, 15, 30, 44.9, 45, 59.9, 60, 60.1, 75, 89.9} {
		cos := float32(math.Cos(deg * math.Pi / 180))
		l := Light{
			Pos:      mgl32.Vec3{3, -2, 7},
			Range:    12,
			Dir:      mgl32.Vec3{0.6, 0.8, 0},
			CosOuter: cos,
		}
		c, r := influenceSphere(&l)
		worst := float32(0)
		for i := 0; i < 20000; i++ {
			dir := randomUnit(rng)
			if dir.Dot(l.Dir) < cos {
				continue
			}
			pt := l.Pos.Add(dir.Mul(l.Range * float32(rng.Float64())))
			if d := pt.Sub(c).Len(); d > worst {
				worst = d
			}
		}
		if worst > r*(1+1e-5) {
			t.Errorf("%.1f degrees: a cone point is %v from the bound centre, radius %v", deg, worst, r)
		}
		// And the bound is tight enough to be worth having below 60 degrees.
		if deg < 44 && r > l.Range*0.72 {
			t.Errorf("%.1f degrees: bound radius %v is no better than the range sphere %v", deg, r, l.Range)
		}
	}
}
