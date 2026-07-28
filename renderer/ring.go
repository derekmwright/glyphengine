package renderer

import "math"

// CreateRing creates a flat annulus (donut) on the XZ plane at Y=0.
// Normals point +Y (up). Winding matches CreatePlane convention.
func (r *Renderer) CreateRing(innerRadius, outerRadius float32, segments int) (*Mesh, error) {
	w := [3]float32{1, 1, 1}
	n := [3]float32{0, 1, 0}

	vertices := make([]Vertex, 0, segments*2)
	for i := 0; i < segments; i++ {
		angle := float64(i) / float64(segments) * 2 * math.Pi
		c := float32(math.Cos(angle))
		s := float32(math.Sin(angle))
		vertices = append(vertices,
			Vertex{Pos: [3]float32{innerRadius * c, 0, innerRadius * s}, Color: w, Normal: n},
			Vertex{Pos: [3]float32{outerRadius * c, 0, outerRadius * s}, Color: w, Normal: n},
		)
	}

	indices := make([]uint16, 0, segments*6)
	for i := 0; i < segments; i++ {
		inner := uint16(i * 2)
		outer := uint16(i*2 + 1)
		nextInner := uint16(((i + 1) % segments) * 2)
		nextOuter := uint16(((i+1)%segments)*2 + 1)
		indices = append(indices,
			inner, outer, nextOuter,
			inner, nextOuter, nextInner,
		)
	}

	return r.CreateIndexedMesh(vertices, indices)
}
