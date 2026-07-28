package renderer

import "github.com/vkngwrapper/core/v3/core1_0"

// Vertex holds per-vertex data: 3D position, RGB color, surface normal, and UV coordinates.
// Stride: 44 bytes (3 x vec3 + 1 x vec2 = 11 floats x 4 bytes).
type Vertex struct {
	Pos    [3]float32
	Color  [3]float32
	Normal [3]float32
	UV     [2]float32
}

// vertexBindingDescription returns the binding description for a single
// interleaved vertex buffer with per-vertex input rate.
func vertexBindingDescription() core1_0.VertexInputBindingDescription {
	return core1_0.VertexInputBindingDescription{
		Binding:   0,
		Stride:    44, // sizeof(Vertex)
		InputRate: core1_0.VertexInputRateVertex,
	}
}

// vertexAttributeDescriptions returns attribute descriptions for position
// (location 0, offset 0), color (location 1, offset 12), normal
// (location 2, offset 24), and UV (location 3, offset 36).
func vertexAttributeDescriptions() []core1_0.VertexInputAttributeDescription {
	return []core1_0.VertexInputAttributeDescription{
		{
			Location: 0,
			Binding:  0,
			Format:   core1_0.FormatR32G32B32SignedFloat,
			Offset:   0, // Pos
		},
		{
			Location: 1,
			Binding:  0,
			Format:   core1_0.FormatR32G32B32SignedFloat,
			Offset:   12, // Color
		},
		{
			Location: 2,
			Binding:  0,
			Format:   core1_0.FormatR32G32B32SignedFloat,
			Offset:   24, // Normal
		},
		{
			Location: 3,
			Binding:  0,
			Format:   core1_0.FormatR32G32SignedFloat,
			Offset:   36, // UV
		},
	}
}
