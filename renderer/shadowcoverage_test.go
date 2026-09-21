package renderer

import (
	"math"
	"testing"

	"github.com/go-gl/mathgl/mgl32"
)

func TestShadowCoverageDefaults(t *testing.T) {
	sun := [3]float32{0.8, 0.6, 0}
	centre := mgl32.Vec3{12, 3, -19}
	for _, coverage := range []ShadowCoverage{{}, DefaultShadowCoverage()} {
		got, err := ComputeCascadeVPsWithCoverage(sun, centre, coverage)
		if err != nil {
			t.Fatal(err)
		}
		if got != ComputeCascadeVPs(sun, centre) {
			t.Fatal("defaults changed")
		}
	}
}

func TestShadowCoverageDistantCasterAndNearResolution(t *testing.T) {
	sun := [3]float32{0.8, 0.6, 0}
	c := DefaultShadowCoverage()
	c.Cascades[0].TowardLight = 1500
	got, err := ComputeCascadeVPsWithCoverage(sun, mgl32.Vec3{}, c)
	if err != nil {
		t.Fatal(err)
	}
	old := ComputeCascadeVPs(sun, mgl32.Vec3{})
	ridge := mgl32.Vec3{800, 600, 0}
	if in, _ := insideClipVolume(old[0], ridge, 0); in {
		t.Fatal("control ridge already inside old map")
	}
	if in, _ := insideClipVolume(got[0], ridge, 0); !in {
		t.Fatal("distant caster clipped")
	}
	for _, row := range []int{0, 1} {
		for col := 0; col < 4; col++ {
			if math.Abs(float64(got[0][col*4+row]-old[0][col*4+row])) > 1e-6 {
				t.Fatal("caster depth changed XY resolution")
			}
		}
	}
	// Camera movement smaller than a texel must not move world-space XY.
	a, err := ComputeCascadeVPsWithCoverage(sun, mgl32.Vec3{0, 0, 0.001}, c)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []int{0, 1} {
		if math.Abs(float64(a[0][12+row]-got[0][12+row])) > 1e-6 {
			t.Fatal("subtexel movement changed snapped XY")
		}
	}
}

func TestShadowCoverageValidation(t *testing.T) {
	for _, value := range []float32{-1, 0, float32(math.NaN()), float32(math.Inf(1))} {
		for field := 0; field < 3; field++ {
			c := DefaultShadowCoverage()
			switch field {
			case 0:
				c.Cascades[0].Radius = value
			case 1:
				c.Cascades[0].TowardLight = value
			case 2:
				c.Cascades[0].AwayFromLight = value
			}
			if c.Validate() == nil {
				t.Fatalf("accepted invalid distance %v", value)
			}
		}
	}
	if _, err := ComputeCascadeVPsWithCoverage([3]float32{}, mgl32.Vec3{}, ShadowCoverage{}); err == nil {
		t.Fatal("accepted zero direction")
	}
}
