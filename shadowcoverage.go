package glyphengine

import (
	"log"

	"github.com/go-gl/mathgl/mgl32"

	"github.com/derekmwright/glyphengine/renderer"
)

// SetShadowCoverage sets directional shadow volumes without reallocating maps.
// Call on the frame thread (Init or Update). Invalid input leaves the previous
// setting intact; zero restores the historical defaults. The setting belongs
// to Engine, so swapping Scene preserves it, like other renderer settings.
//
// The coverage must describe as many cascades as WithShadowCascades
// allocated: a count that disagrees is refused like any other invalid input.
// The zero value is renderer.DefaultShadowCoverageFor that count.
func (e *Engine) SetShadowCoverage(coverage renderer.ShadowCoverage) error {
	if err := coverage.ValidateFor(e.shadowCascadeCount()); err != nil {
		return err
	}
	e.shadowCoverage = coverage
	return nil
}

// shadowCascadeCount is the renderer's cascade count, with zero -- an Engine a
// test assembled without a renderer -- read as the default.
func (e *Engine) shadowCascadeCount() int {
	if e.shadowCascades == 0 {
		return renderer.ShadowCascades
	}
	return e.shadowCascades
}

// fitShadowCascades is the frame's cascade fit: a matrix for each cascade the
// renderer draws, how many that is, and whether directional shadows are on
// once the coverage has been checked against the count. renderFrame culls
// casters against exactly the first count matrices and hands all of them to
// the renderer.
func (e *Engine) fitShadowCascades(castShadows bool, sunDir [3]float32) (vps [renderer.MaxShadowCascades]mgl32.Mat4, cascades int, enabled bool) {
	cascades = e.shadowCascadeCount()
	if !castShadows {
		return vps, cascades, false
	}
	coverage := e.shadowCoverage
	if coverage == (renderer.ShadowCoverage{}) {
		// The zero value is "the defaults" at every count, so an engine built
		// with more cascades draws shadows before the game says what its extra
		// tiers cover. SetShadowCoverage refuses any other mismatched count.
		coverage = renderer.DefaultShadowCoverageFor(cascades)
	}
	vps, err := renderer.ComputeCascadeVPsWithCoverage(sunDir, e.cameraCenter, coverage)
	if err != nil {
		log.Printf("glyphengine: directional shadows disabled this frame: %v", err)
		return vps, cascades, false
	}
	return vps, cascades, true
}
