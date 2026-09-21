package glyphengine

import "github.com/derekmwright/glyphengine/renderer"

// SetShadowCoverage sets directional shadow volumes without reallocating maps.
// Call on the frame thread (Init or Update). Invalid input leaves the previous
// setting intact; zero restores the historical defaults. The setting belongs
// to Engine, so swapping Scene preserves it, like other renderer settings.
func (e *Engine) SetShadowCoverage(coverage renderer.ShadowCoverage) error {
	if err := coverage.Validate(); err != nil {
		return err
	}
	e.shadowCoverage = coverage
	return nil
}
