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
