package lightcluster

import (
	"fmt"
	"math"
	"math/rand/v2"
	"testing"

	"github.com/go-gl/mathgl/mgl32"
)

// streetScene is the distribution the constants in this package are sized
// against: small lights on poles, range 5 to 15 m, scattered over a 400 m
// square, with the camera looking down at 45 degrees from a given height. It is
// a colony at night, which is the case that motivated clustering at all.
//
// Lights are placed from a fixed seed so the occupancy numbers below are
// reproducible; the camera is not moved between runs for the same reason.
func streetScene(n int, cameraHeight float32) ([]Light, Params) {
	rng := rand.New(rand.NewPCG(0xb0a7, uint64(n)))
	lights := make([]Light, n)
	for i := range lights {
		l := Light{
			Pos:   mgl32.Vec3{rng.Float32()*400 - 200, 3 + rng.Float32()*2, rng.Float32()*400 - 200},
			Range: 5 + rng.Float32()*10,
		}
		if i%4 == 0 {
			// A quarter of them are downward spots, which is what a street
			// lamp actually is and what exercises the sector bound.
			l.Dir = mgl32.Vec3{rng.Float32()*0.4 - 0.2, -1, rng.Float32()*0.4 - 0.2}.Normalize()
			l.CosOuter = float32(math.Cos(float64(35+rng.Float32()*20) * math.Pi / 180))
		}
		lights[i] = l
	}
	eye := mgl32.Vec3{0, cameraHeight, 0}
	const s = math.Sqrt2 / 2
	center := eye.Add(mgl32.Vec3{0, -s, -s}.Mul(100))
	p := Params{
		View:   mgl32.LookAtV(eye, center, mgl32.Vec3{0, 1, 0}),
		Proj:   reverseZProjection(60, 1920.0/1080.0, 0.1, 500),
		Width:  1920,
		Height: 1080,
		Near:   0.1,
		Far:    500,
		Grid:   DefaultGrid,
	}
	return lights, p
}

func BenchmarkBuild(b *testing.B) {
	for _, n := range []int{32, 256, 1024, 4096} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			lights, p := streetScene(n, 30)
			builder := New()
			res := builder.Build(lights, p) // warm the buffers, then report the scene
			avg := 0.0
			if res.Stats.NonEmptyCells > 0 {
				avg = float64(res.Stats.TotalCellLights) / float64(res.Stats.NonEmptyCells)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				builder.Build(lights, p)
			}
			b.StopTimer()
			b.ReportMetric(avg, "lights/cell")
			b.ReportMetric(float64(res.Stats.MaxCellDemand), "max-lights/cell")
			b.ReportMetric(float64(res.Stats.IndexCount), "indices")
		})
	}
}

// BenchmarkBuildPathological is the same count of lights placed where they cost
// the most: all of them within a few metres of the camera, so every one widens
// to the whole screen and lands in every tile of its slices. It is not a real
// scene; it is the ceiling, and it is what the cell and index caps have to
// survive without going quadratic or writing out of bounds.
func BenchmarkBuildPathological(b *testing.B) {
	for _, n := range []int{256, 1024} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			rng := rand.New(rand.NewPCG(1, uint64(n)))
			lights := make([]Light, n)
			for i := range lights {
				lights[i] = Light{
					Pos:   mgl32.Vec3{rng.Float32()*4 - 2, 30 + rng.Float32()*4 - 2, rng.Float32()*4 - 2},
					Range: 40 + rng.Float32()*200,
				}
			}
			_, p := streetScene(1, 30)
			builder := New()
			res := builder.Build(lights, p)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				builder.Build(lights, p)
			}
			b.StopTimer()
			b.ReportMetric(float64(res.Stats.MaxCellDemand), "max-lights/cell")
			b.ReportMetric(float64(res.Stats.IndexCount), "indices")
		})
	}
}

// TestGridOccupancy is not an assertion so much as the measurement the grid
// size, MaxLightsPerCell and MaxLightIndices are chosen from. Run it with -v.
//
// It does assert the one thing that would make those numbers lies: that the
// scenes it reports on did not themselves overflow, because a capped count
// cannot tell you what the cap should be.
func TestGridOccupancy(t *testing.T) {
	type row struct {
		name                       string
		lights                     []Light
		params                     Params
		avg, indexKB               float64
		maxDemand, wide, nonEmpty  int
		uploaded, culled, overflow int
	}
	var rows []row
	for _, n := range []int{32, 256, 1024, 4096} {
		for _, h := range []float32{30, 2} {
			for _, start := range []float32{0, 0.1} { // DefaultSliceStart, then the near plane
				lights, p := streetScene(n, h)
				p.SliceStart = start
				res := New().Build(lights, p)
				avg := 0.0
				if res.Stats.NonEmptyCells > 0 {
					avg = float64(res.Stats.TotalCellLights) / float64(res.Stats.NonEmptyCells)
				}
				name := fmt.Sprintf("%4d lights, camera %2.0fm, slice start %.1f", n, h, cmpStart(start))
				rows = append(rows, row{
					name: name, lights: lights, params: p,
					avg: avg, indexKB: float64(res.Stats.IndexCount) * 4 / 1024,
					maxDemand: res.Stats.MaxCellDemand, wide: res.Stats.ScreenWideLights,
					nonEmpty: res.Stats.NonEmptyCells, uploaded: res.Stats.Uploaded,
					culled: res.Stats.Culled, overflow: res.Stats.CellsOverflowed,
				})
			}
		}
	}
	t.Logf("%-42s %8s %6s %6s %8s %6s %6s", "scene", "avg/cell", "max", "wide", "nonempty", "up", "idx KB")
	for _, r := range rows {
		t.Logf("%-42s %8.2f %6d %6d %8d %6d %6.0f",
			r.name, r.avg, r.maxDemand, r.wide, r.nonEmpty, r.uploaded, r.indexKB)
		if r.overflow != 0 {
			t.Errorf("%s: %d cells overflowed, so max/cell is a capped number and "+
				"cannot justify MaxLightsPerCell", r.name, r.overflow)
		}
		if r.maxDemand >= MaxLightsPerCell {
			t.Errorf("%s: peak cell demand %d has reached MaxLightsPerCell (%d)",
				r.name, r.maxDemand, MaxLightsPerCell)
		}
	}
}

func cmpStart(s float32) float64 {
	if s == 0 {
		return DefaultSliceStart
	}
	return float64(s)
}

// colonyScene models the consumer game's real camera and light layout, which is
// what makes the whole-screen fallback matter: an orbit camera over a flat-top
// hex grid of lamps, where at the zoom a player builds at, the eye sits INSIDE
// many lamp spheres at once.
//
// Hexes of circumradius 1 (neighbours sqrt(3) = 1.732 apart, 2.598 square units
// each), lamps 0.55 above the ground, an orbit camera looking at the origin, so
// the eye is distance*sin(pitch) above the ground. At distance 6 and pitch 0.22
// that is 1.31 up, and a lamp of range 4 reaches it from 3.93 away: 48.5 square
// units, 19 lamps. That is the case a player spends their time in, not a corner
// of the parameter space. Field of view 45 degrees, the engine default.
func colonyScene(lamps int, lampRange, distance, pitch float32) ([]Light, Params) {
	side := 1
	for side*side < lamps {
		side++
	}
	const rowStep = 1.7320508 // sqrt(3)
	lights := make([]Light, 0, lamps)
	for i := 0; i < lamps; i++ {
		q := float32(i%side - side/2)
		r := float32(i/side - side/2)
		lights = append(lights, Light{
			Pos:   mgl32.Vec3{1.5 * q, 0.55, rowStep * (r + q/2)},
			Range: lampRange,
		})
	}
	eye := mgl32.Vec3{
		0,
		distance * float32(math.Sin(float64(pitch))),
		distance * float32(math.Cos(float64(pitch))),
	}
	p := Params{
		View:   mgl32.LookAtV(eye, mgl32.Vec3{0, 0, 0}, mgl32.Vec3{0, 1, 0}),
		Proj:   reverseZProjection(45, 1920.0/1080.0, 0.1, 500),
		Width:  1920,
		Height: 1080,
		Near:   0.1,
		Far:    500,
		Grid:   DefaultGrid,
	}
	return lights, p
}

type colonyCase struct {
	name            string
	lampRange       float32
	distance, pitch float32
	// wantInside is how many lamps must contain the eye for the scene to still
	// be the thing it was modelled on. The game measured 19, 6 and 42 for
	// these three; zoomed out the eye is above every lamp's reach, which is
	// the case the refinement should NOT be paying for.
	wantInside int
}

var colonyCases = []colonyCase{
	{"r4 d6 p0.22", 4, 6, 0.22, 15},  // minimum zoom and pitch: eye 1.31 up
	{"r4 d9 p0.45", 4, 9, 0.45, 5},   // default play distance: eye 3.91 up
	{"r4 d16 p0.45", 4, 16, 0.45, 0}, // zoomed out: eye 6.96 up, nothing reaches it
	{"r6 d6 p0.22", 6, 6, 0.22, 35},  // the same camera with a longer lamp range
}

// BenchmarkBuildColony is what the froxel cell test was built against, and what
// it has to keep justifying. Measured on a Ryzen 9 5900X with the two versions
// run alternately, three rounds, medians — sequential runs on this machine
// drift by 30%, which is more than the difference being measured:
//
//	scene            CPU before -> after      index entries      peak cell
//	r4 d6 p0.22       315 -> 502 us  (+59%)   61803 -> 41670       57 -> 50
//	r4 d9 p0.45       337 -> 482 us  (+43%)   64746 -> 36339       61 -> 52
//	r4 d16 p0.45      255 -> 357 us  (+40%)   44106 -> 22459       68 -> 59
//	r6 d6 p0.22       654 -> 937 us  (+43%)  138946 -> 90167       94 -> 82
//
// An index entry is one light that every fragment of one cell evaluates, and a
// cell is 120x120 pixels at 1080p, so a third to a half of them going away is
// worth a good deal more than the CPU it costs. The GPU side of that is G2 and
// G5's to confirm; this package cannot see it.
func BenchmarkBuildColony(b *testing.B) {
	for _, c := range colonyCases {
		b.Run(c.name, func(b *testing.B) {
			lights, p := colonyScene(400, c.lampRange, c.distance, c.pitch)
			builder := New()
			res := builder.Build(lights, p)
			avg := 0.0
			if res.Stats.NonEmptyCells > 0 {
				avg = float64(res.Stats.TotalCellLights) / float64(res.Stats.NonEmptyCells)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				builder.Build(lights, p)
			}
			b.StopTimer()
			b.ReportMetric(avg, "lights/cell")
			b.ReportMetric(float64(res.Stats.MaxCellDemand), "max-lights/cell")
			b.ReportMetric(float64(res.Stats.ScreenWideLights), "screenwide")
			b.ReportMetric(float64(res.Stats.IndexCount), "indices")
		})
	}
}

// TestColonyOccupancy is the measurement the froxel refinement has to justify
// itself against, and the check that the scene still reproduces the camera it
// was modelled on. Run it with -v.
func TestColonyOccupancy(t *testing.T) {
	t.Logf("%-14s %6s %5s %10s %8s %6s %6s %6s %8s %8s %9s %9s",
		"camera", "eye up", "in", "eye-inside", "avg/cell", "max", "wide", "unbnd",
		"nonempty", "indices", "tested", "binned")
	for _, c := range colonyCases {
		lights, p := colonyScene(400, c.lampRange, c.distance, c.pitch)
		res := New().Build(lights, p)
		eye := cameraPosition(p.View)
		inside := 0
		for _, l := range lights {
			if l.Pos.Sub(eye).Len() <= l.Range {
				inside++
			}
		}
		avg := 0.0
		if res.Stats.NonEmptyCells > 0 {
			avg = float64(res.Stats.TotalCellLights) / float64(res.Stats.NonEmptyCells)
		}
		t.Logf("%-14s %6.2f %5d %10d %8.2f %6d %6d %6d %8d %8d %9d %9d",
			c.name, eye[1], res.Stats.Uploaded, inside, avg,
			res.Stats.MaxCellDemand, res.Stats.ScreenWideLights, res.Stats.UnboundedLights,
			res.Stats.NonEmptyCells, res.Stats.IndexCount,
			res.Stats.CellsTested, res.Stats.CellsBinned)
		if inside < c.wantInside {
			t.Errorf("%s: only %d lamps contain the eye, want at least %d; this scene "+
				"has stopped reproducing the camera it was built from", c.name, inside, c.wantInside)
		}
	}
}
