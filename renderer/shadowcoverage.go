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

	// ReceiverDepth is how far from the centre, measured along the light
	// direction either way, a receiver may lie and still be shadowed by this
	// cascade. Casters are not affected: they render from the whole
	// TowardLight..AwayFromLight volume. Zero keeps the whole volume, which is
	// the rule every cascade had before this field.
	//
	// It exists because the depth test cannot hold a fine cascade to nearby
	// receivers once its caster reach is long: a receiver kilometres down-sun
	// projects inside a 90 m cascade's XY bounds whenever it is close to the
	// light axis through the centre, and with 200 km of caster depth its depth
	// is inside too, so the near map is the first match. Bounding the receiver
	// depth hands such a receiver to the next cascade instead, which is the
	// selection a game that filters or blends per tier needs to be able to
	// state. With every tier at the same caster reach the near map does hold
	// the casters on such a receiver's light ray: on 23-shadow-coverage
	// -cascades 3 the frame is byte-identical with the bands and without them
	// (fixed-clock captures, 2026-10-17), so this changes which map is read, not
	// what that scene looks like. The bound is a band in the cascade's own
	// normalised depth, so the shader makes the comparison it was already
	// making against different operands; see ShadowReceiverRange.
	ReceiverDepth float32
}

// ShadowCoverage describes every sun cascade the renderer allocated. A larger
// radius trades resolution for reach; extra caster depth does not change XY
// texel density. The zero value selects DefaultShadowCoverage.
//
// Count is explicit and Cascades is a fixed array rather than a slice. The
// value stays comparable, so the zero value can keep meaning "the defaults" and
// a rejected setter can be told apart from an accepted one with ==, and an
// Engine holds it and hands it to the cascade fit every frame without
// allocating.
type ShadowCoverage struct {
	// Count is how many entries of Cascades are in use. Zero means
	// ShadowCascades, so a coverage written before the count existed means
	// what it meant then. It must equal the renderer's cascade count
	// (WithShadowCascades); the entries past it must be zero.
	Count    int
	Cascades [MaxShadowCascades]ShadowCascadeCoverage
}

// DefaultShadowCoverage is the two historical cascades, 15 and 90 units.
func DefaultShadowCoverage() ShadowCoverage {
	return DefaultShadowCoverageFor(ShadowCascades)
}

// DefaultShadowCoverageFor is the coverage a renderer with n cascades uses
// until the game sets one: the two historical cascades, extended by the factor
// of six that already separates them. Radii are 15, 90, 540 and 3240 units,
// TowardLight equals the radius and AwayFromLight is 1.5 times it, as in the
// first two. n outside [ShadowCascades, MaxShadowCascades] is clamped into it.
//
// It is derived, not measured: nothing tuned 540 or 3240 for any scene. It
// exists so that an engine built with more cascades always draws shadows --
// a frame lit correctly with no shadows in it is a failure nothing reports --
// and a game that cares what its extra tiers cover sets them.
func DefaultShadowCoverageFor(n int) ShadowCoverage {
	n = min(max(n, ShadowCascades), MaxShadowCascades)
	c := ShadowCoverage{Count: n}
	radius := float32(15)
	for i := range n {
		c.Cascades[i] = ShadowCascadeCoverage{Radius: radius, TowardLight: radius, AwayFromLight: radius * 1.5}
		radius *= 6
	}
	return c
}

// CascadeCount is Count with zero read as ShadowCascades.
func (c ShadowCoverage) CascadeCount() int {
	if c.Count == 0 {
		return ShadowCascades
	}
	return c.Count
}

// Validate rejects partial, non-finite and degenerate volumes, a count outside
// [ShadowCascades, MaxShadowCascades], and a volume set past the count -- which
// is what a caller who filled Cascades[2] and forgot Count would otherwise have
// silently ignored. Cascades need not contain one another: the engine culls
// casters against their union.
func (c ShadowCoverage) Validate() error {
	if c == (ShadowCoverage{}) {
		return nil
	}
	n := c.CascadeCount()
	if n < ShadowCascades || n > MaxShadowCascades {
		return fmt.Errorf("shadow coverage: %d cascades outside [%d,%d]", n, ShadowCascades, MaxShadowCascades)
	}
	for i, v := range c.Cascades {
		if i >= n {
			if v != (ShadowCascadeCoverage{}) {
				return fmt.Errorf("shadow cascade %d is set but Count is %d", i, n)
			}
			continue
		}
		for _, value := range []float32{v.Radius, v.TowardLight, v.AwayFromLight, v.TowardLight + v.AwayFromLight} {
			if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) || value <= 0 {
				return fmt.Errorf("shadow cascade %d: distances must be finite and positive", i)
			}
		}
		if v.TowardLight <= 0.1 {
			return fmt.Errorf("shadow cascade %d: TowardLight must exceed the 0.1 near plane", i)
		}
		if r := v.ReceiverDepth; math.IsNaN(float64(r)) || math.IsInf(float64(r), 0) || r < 0 {
			return fmt.Errorf("shadow cascade %d: ReceiverDepth must be finite and not negative", i)
		}
	}
	return nil
}

// ValidateFor is Validate plus the one thing a coverage cannot know alone:
// that it describes as many cascades as the renderer allocated. Fewer would
// leave a layer the shader selects that no pass drew into for this coverage;
// more would describe a tier no pass draws at all. cascades zero means
// ShadowCascades.
func (c ShadowCoverage) ValidateFor(cascades int) error {
	if err := c.Validate(); err != nil {
		return err
	}
	// The zero value means "the defaults", and there is a default for every
	// count: DefaultShadowCoverageFor.
	if c == (ShadowCoverage{}) {
		return nil
	}
	if cascades == 0 {
		cascades = ShadowCascades
	}
	if n := c.CascadeCount(); n != cascades {
		return fmt.Errorf("shadow coverage describes %d cascades; the renderer allocated %d (WithShadowCascades)", n, cascades)
	}
	return nil
}

// ComputeCascadeVPsWithCoverage uses the same texel snapping as ComputeCascadeVPs.
// The centre, geometry and camera must share a coordinate frame; camera-relative
// consumers must rebase all of them together. Direction must be finite/nonzero.
// Entries past coverage.CascadeCount() are zero.
func ComputeCascadeVPsWithCoverage(sunDir [3]float32, centre mgl32.Vec3, coverage ShadowCoverage) ([MaxShadowCascades]mgl32.Mat4, error) {
	var result [MaxShadowCascades]mgl32.Mat4
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
	for i := range coverage.CascadeCount() {
		result[i] = computeLightVPCoverage(sunDir, centre, coverage.Cascades[i])
	}
	return result, nil
}

// ShadowReceiverRange is the band of one cascade's normalised light depth --
// 0 at its near plane, 1 at its far plane -- a receiver must lie in to select
// that cascade. SceneLighting.CascadeReceivers carries one per cascade and
// ShadowCoverage.ReceiverRanges computes them.
//
// The zero value is the whole volume, [0, 1]: exactly the depth test calcShadow
// always made, so a SceneLighting that never heard of the field shades as it
// did. The shader makes the same two comparisons either way; only their
// operands come from this.
type ShadowReceiverRange struct{ Near, Far float32 }

// bounds is the pair the shader compares against, with the zero value read as
// the whole volume.
func (r ShadowReceiverRange) bounds() (near, far float32) {
	if r == (ShadowReceiverRange{}) {
		return 0, 1
	}
	return r.Near, r.Far
}

// receiverRange converts ReceiverDepth into the cascade's normalised depth. The
// matrix maps view distance d from the light eye to (d - 0.1) / (T + A - 0.1),
// and the centre sits at d = T; see computeLightVPCoverage. Worked in float64
// so each edge is rounded to float32 once.
func (v ShadowCascadeCoverage) receiverRange() ShadowReceiverRange {
	if v.ReceiverDepth == 0 {
		return ShadowReceiverRange{}
	}
	const near = 0.1
	toward, away, depth := float64(v.TowardLight), float64(v.AwayFromLight), float64(v.ReceiverDepth)
	span := toward + away - near
	lo := max((toward-depth-near)/span, 0)
	hi := min((toward+depth-near)/span, 1)
	return ShadowReceiverRange{Near: float32(lo), Far: float32(hi)}
}

// ReceiverRanges is each in-use cascade's ShadowReceiverRange, zero past the
// count and for a cascade whose ReceiverDepth is zero. A renderer-only consumer
// passes it through SceneLighting.CascadeReceivers beside the matrices.
func (c ShadowCoverage) ReceiverRanges() [MaxShadowCascades]ShadowReceiverRange {
	var out [MaxShadowCascades]ShadowReceiverRange
	n := min(c.CascadeCount(), MaxShadowCascades)
	for i := range n {
		out[i] = c.Cascades[i].receiverRange()
	}
	return out
}
