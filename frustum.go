package glyphengine

import (
	"github.com/go-gl/mathgl/mgl32"

	"github.com/derekmwright/glyphengine/renderer"
)

// Frustum holds the six clip planes extracted from a view-projection matrix.
// The implementation lives in the renderer package so command recording can
// cull GPU work (grass tiles, point-light shadow faces) without importing engine.
type Frustum = renderer.Frustum

// ExtractFrustum extracts 6 normalized clip planes from a view-projection
// matrix using the Gribb-Hartmann method.
func ExtractFrustum(vp mgl32.Mat4) Frustum {
	return renderer.ExtractFrustum(vp)
}

func max32(a, b float32) float32 {
	if a > b {
		return a
	}
	return b
}
