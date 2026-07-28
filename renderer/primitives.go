package renderer

import "math"

// CreateCube creates an indexed cube mesh centered at origin with the given size.
// Each face has 4 vertices (24 total) with per-face normals for correct lighting.
// All vertex colors are white so tint controls the visible color.
func (r *Renderer) CreateCube(size float32) (*Mesh, error) {
	h := size / 2
	w := [3]float32{1, 1, 1} // white

	vertices := []Vertex{
		// Front face (+Z)
		{Pos: [3]float32{-h, -h, h}, Color: w, Normal: [3]float32{0, 0, 1}},
		{Pos: [3]float32{h, -h, h}, Color: w, Normal: [3]float32{0, 0, 1}},
		{Pos: [3]float32{h, h, h}, Color: w, Normal: [3]float32{0, 0, 1}},
		{Pos: [3]float32{-h, h, h}, Color: w, Normal: [3]float32{0, 0, 1}},
		// Back face (-Z)
		{Pos: [3]float32{h, -h, -h}, Color: w, Normal: [3]float32{0, 0, -1}},
		{Pos: [3]float32{-h, -h, -h}, Color: w, Normal: [3]float32{0, 0, -1}},
		{Pos: [3]float32{-h, h, -h}, Color: w, Normal: [3]float32{0, 0, -1}},
		{Pos: [3]float32{h, h, -h}, Color: w, Normal: [3]float32{0, 0, -1}},
		// Top face (+Y)
		{Pos: [3]float32{-h, h, h}, Color: w, Normal: [3]float32{0, 1, 0}},
		{Pos: [3]float32{h, h, h}, Color: w, Normal: [3]float32{0, 1, 0}},
		{Pos: [3]float32{h, h, -h}, Color: w, Normal: [3]float32{0, 1, 0}},
		{Pos: [3]float32{-h, h, -h}, Color: w, Normal: [3]float32{0, 1, 0}},
		// Bottom face (-Y)
		{Pos: [3]float32{-h, -h, -h}, Color: w, Normal: [3]float32{0, -1, 0}},
		{Pos: [3]float32{h, -h, -h}, Color: w, Normal: [3]float32{0, -1, 0}},
		{Pos: [3]float32{h, -h, h}, Color: w, Normal: [3]float32{0, -1, 0}},
		{Pos: [3]float32{-h, -h, h}, Color: w, Normal: [3]float32{0, -1, 0}},
		// Right face (+X)
		{Pos: [3]float32{h, -h, h}, Color: w, Normal: [3]float32{1, 0, 0}},
		{Pos: [3]float32{h, -h, -h}, Color: w, Normal: [3]float32{1, 0, 0}},
		{Pos: [3]float32{h, h, -h}, Color: w, Normal: [3]float32{1, 0, 0}},
		{Pos: [3]float32{h, h, h}, Color: w, Normal: [3]float32{1, 0, 0}},
		// Left face (-X)
		{Pos: [3]float32{-h, -h, -h}, Color: w, Normal: [3]float32{-1, 0, 0}},
		{Pos: [3]float32{-h, -h, h}, Color: w, Normal: [3]float32{-1, 0, 0}},
		{Pos: [3]float32{-h, h, h}, Color: w, Normal: [3]float32{-1, 0, 0}},
		{Pos: [3]float32{-h, h, -h}, Color: w, Normal: [3]float32{-1, 0, 0}},
	}

	// 6 indices per face (2 triangles), CW winding for Vulkan Y-flip
	indices := []uint16{
		0, 2, 1, 0, 3, 2, // Front
		4, 6, 5, 4, 7, 6, // Back
		8, 10, 9, 8, 11, 10, // Top
		12, 14, 13, 12, 15, 14, // Bottom
		16, 18, 17, 16, 19, 18, // Right
		20, 22, 21, 20, 23, 22, // Left
	}

	return r.CreateIndexedMesh(vertices, indices)
}

// CreatePlane creates an indexed plane on the XZ plane (Y=0) centered at origin.
// All vertex colors are white so tint controls the visible color.
func (r *Renderer) CreatePlane(width, depth float32) (*Mesh, error) {
	hw := width / 2
	hd := depth / 2
	w := [3]float32{1, 1, 1}
	up := [3]float32{0, 1, 0}

	vertices := []Vertex{
		{Pos: [3]float32{-hw, 0, -hd}, Color: w, Normal: up},
		{Pos: [3]float32{hw, 0, -hd}, Color: w, Normal: up},
		{Pos: [3]float32{hw, 0, hd}, Color: w, Normal: up},
		{Pos: [3]float32{-hw, 0, hd}, Color: w, Normal: up},
	}

	// CCW winding when viewed from +Y
	indices := []uint16{0, 1, 2, 2, 3, 0}

	return r.CreateIndexedMesh(vertices, indices)
}

// CreateDisc creates a flat disc on the XY plane using a triangle fan.
// All vertex colors are white, normals point +Z. CW winding for Vulkan.
func (r *Renderer) CreateDisc(radius float32, segments int) (*Mesh, error) {
	w := [3]float32{1, 1, 1}
	n := [3]float32{0, 0, 1}

	// Center vertex + ring vertices
	vertices := make([]Vertex, 0, segments+1)
	vertices = append(vertices, Vertex{Pos: [3]float32{0, 0, 0}, Color: w, Normal: n})
	for i := 0; i < segments; i++ {
		angle := float64(i) / float64(segments) * 2 * math.Pi
		x := radius * float32(math.Cos(angle))
		y := radius * float32(math.Sin(angle))
		vertices = append(vertices, Vertex{Pos: [3]float32{x, y, 0}, Color: w, Normal: n})
	}

	// Triangle fan with CW winding: center, i+1, i (so front face is +Z)
	indices := make([]uint16, 0, segments*3)
	for i := 1; i <= segments; i++ {
		next := i%segments + 1
		indices = append(indices, 0, uint16(next), uint16(i))
	}

	return r.CreateIndexedMesh(vertices, indices)
}

// CreateCylinder creates a cylinder centered on the Y axis with bottom at Y=0
// and top at Y=height. Side quads have outward normals; top/bottom caps have
// +Y/-Y normals. All vertex colors are white. CW winding for Vulkan.
func (r *Renderer) CreateCylinder(radius, height float32, segments int) (*Mesh, error) {
	w := [3]float32{1, 1, 1}

	vertices := make([]Vertex, 0, segments*4+2*(segments+1))
	indices := make([]uint16, 0, segments*6+2*segments*3)

	// Side quads.
	for i := 0; i < segments; i++ {
		a0 := float64(i) / float64(segments) * 2 * math.Pi
		a1 := float64(i+1) / float64(segments) * 2 * math.Pi

		c0, s0 := float32(math.Cos(a0)), float32(math.Sin(a0))
		c1, s1 := float32(math.Cos(a1)), float32(math.Sin(a1))
		n0 := [3]float32{c0, 0, s0}
		n1 := [3]float32{c1, 0, s1}

		base := uint16(len(vertices))
		vertices = append(vertices,
			Vertex{Pos: [3]float32{radius * c0, 0, radius * s0}, Color: w, Normal: n0},
			Vertex{Pos: [3]float32{radius * c1, 0, radius * s1}, Color: w, Normal: n1},
			Vertex{Pos: [3]float32{radius * c1, height, radius * s1}, Color: w, Normal: n1},
			Vertex{Pos: [3]float32{radius * c0, height, radius * s0}, Color: w, Normal: n0},
		)
		// CW winding for Vulkan Y-flip: viewed from outside, a0 is right
		// and a1 is left, so vertex order is BL=1, BR=0, TR=3, TL=2.
		indices = append(indices, base+1, base+3, base, base+1, base+2, base+3)
	}

	// Top cap (+Y normal).
	{
		up := [3]float32{0, 1, 0}
		center := uint16(len(vertices))
		vertices = append(vertices, Vertex{Pos: [3]float32{0, height, 0}, Color: w, Normal: up})
		for i := 0; i < segments; i++ {
			angle := float64(i) / float64(segments) * 2 * math.Pi
			x := radius * float32(math.Cos(angle))
			z := radius * float32(math.Sin(angle))
			vertices = append(vertices, Vertex{Pos: [3]float32{x, height, z}, Color: w, Normal: up})
		}
		for i := 0; i < segments; i++ {
			next := (i + 1) % segments
			indices = append(indices, center, center+1+uint16(i), center+1+uint16(next))
		}
	}

	// Bottom cap (-Y normal).
	{
		down := [3]float32{0, -1, 0}
		center := uint16(len(vertices))
		vertices = append(vertices, Vertex{Pos: [3]float32{0, 0, 0}, Color: w, Normal: down})
		for i := 0; i < segments; i++ {
			angle := float64(i) / float64(segments) * 2 * math.Pi
			x := radius * float32(math.Cos(angle))
			z := radius * float32(math.Sin(angle))
			vertices = append(vertices, Vertex{Pos: [3]float32{x, 0, z}, Color: w, Normal: down})
		}
		for i := 0; i < segments; i++ {
			next := (i + 1) % segments
			indices = append(indices, center, center+1+uint16(next), center+1+uint16(i))
		}
	}

	return r.CreateIndexedMesh(vertices, indices)
}

// CreateCone creates a cone centered on the Y axis with base at Y=0 and apex
// at Y=height. Side triangles have outward normals; bottom cap has -Y normals.
// All vertex colors are white. CW winding for Vulkan.
func (r *Renderer) CreateCone(radius, height float32, segments int) (*Mesh, error) {
	w := [3]float32{1, 1, 1}

	vertices := make([]Vertex, 0, segments*3+(segments+1))
	indices := make([]uint16, 0, segments*6)

	// Side triangles: each segment is a triangle from base edge to apex.
	for i := 0; i < segments; i++ {
		a0 := float64(i) / float64(segments) * 2 * math.Pi
		a1 := float64(i+1) / float64(segments) * 2 * math.Pi

		c0, s0 := float32(math.Cos(a0)), float32(math.Sin(a0))
		c1, s1 := float32(math.Cos(a1)), float32(math.Sin(a1))

		// Outward normal for this face.
		slope := radius / height
		midA := (a0 + a1) / 2
		nx := float32(math.Cos(midA))
		nz := float32(math.Sin(midA))
		nl := float32(math.Sqrt(float64(nx*nx + nz*nz + slope*slope)))
		norm := [3]float32{nx / nl, slope / nl, nz / nl}

		base := uint16(len(vertices))
		vertices = append(vertices,
			Vertex{Pos: [3]float32{radius * c0, 0, radius * s0}, Color: w, Normal: norm},
			Vertex{Pos: [3]float32{radius * c1, 0, radius * s1}, Color: w, Normal: norm},
			Vertex{Pos: [3]float32{0, height, 0}, Color: w, Normal: norm},
		)
		indices = append(indices, base+1, base+2, base)
	}

	// Bottom cap (-Y normal).
	{
		down := [3]float32{0, -1, 0}
		center := uint16(len(vertices))
		vertices = append(vertices, Vertex{Pos: [3]float32{0, 0, 0}, Color: w, Normal: down})
		for i := 0; i < segments; i++ {
			angle := float64(i) / float64(segments) * 2 * math.Pi
			x := radius * float32(math.Cos(angle))
			z := radius * float32(math.Sin(angle))
			vertices = append(vertices, Vertex{Pos: [3]float32{x, 0, z}, Color: w, Normal: down})
		}
		for i := 0; i < segments; i++ {
			next := (i + 1) % segments
			indices = append(indices, center, center+1+uint16(next), center+1+uint16(i))
		}
	}

	return r.CreateIndexedMesh(vertices, indices)
}
