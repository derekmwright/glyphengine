package renderer

import (
	"fmt"
	"math"

	"github.com/go-gl/mathgl/mgl32"
)

// ShadowCascadeCoverage describes a camera-centred orthographic shadow volume.
// All distances use the same world units as mesh positions.
type ShadowCascadeCoverage struct {
	Radius        float32 // XY half-extent in light space; resolution is 2*Radius/ShadowMapSize
	TowardLight   float32 // distance from the centre toward the light (near plane is 0.1 beyond this eye)
	AwayFromLight float32 // distance behind the centre, away from the light
}

// ShadowCoverage keeps the two existing maps and their shader ABI. A larger
// radius trades resolution for reach; extra caster depth does not change XY
// texel density. The zero value selects DefaultShadowCoverage.
type ShadowCoverage struct {
	Cascades [ShadowCascades]ShadowCascadeCoverage
}

func DefaultShadowCoverage() ShadowCoverage {
	return ShadowCoverage{Cascades: [ShadowCascades]ShadowCascadeCoverage{
		{Radius: 15, TowardLight: 15, AwayFromLight: 22.5},
		{Radius: 90, TowardLight: 90, AwayFromLight: 135},
	}}
}

// Validate rejects partial, non-finite and degenerate volumes. Cascades need
// not contain one another: the engine culls casters against their union.
func (c ShadowCoverage) Validate() error {
	if c == (ShadowCoverage{}) {
		return nil
	}
	for i, v := range c.Cascades {
		for _, value := range []float32{v.Radius, v.TowardLight, v.AwayFromLight, v.TowardLight + v.AwayFromLight} {
			if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) || value <= 0 {
				return fmt.Errorf("shadow cascade %d: distances must be finite and positive", i)
			}
		}
		if v.TowardLight <= 0.1 {
			return fmt.Errorf("shadow cascade %d: TowardLight must exceed the 0.1 near plane", i)
		}
	}
	return nil
}

// ComputeCascadeVPsWithCoverage uses the same texel snapping as ComputeCascadeVPs.
// The centre, geometry and camera must share a coordinate frame; camera-relative
// consumers must rebase all of them together. Direction must be finite/nonzero.
func ComputeCascadeVPsWithCoverage(sunDir [3]float32, centre mgl32.Vec3, coverage ShadowCoverage) ([ShadowCascades]mgl32.Mat4, error) {
	var result [ShadowCascades]mgl32.Mat4
	if err := coverage.Validate(); err != nil {
		return result, err
	}
	length := mgl32.Vec3(sunDir).Len()
	if length == 0 || math.IsNaN(float64(length)) || math.IsInf(float64(length), 0) {
		return result, fmt.Errorf("shadow direction must be finite and nonzero")
	}
	for _, v := range centre {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return result, fmt.Errorf("shadow centre must be finite")
		}
	}
	if coverage == (ShadowCoverage{}) {
		coverage = DefaultShadowCoverage()
	}
	for i, c := range coverage.Cascades {
		result[i] = computeLightVPCoverage(sunDir, centre, c)
	}
	return result, nil
}
