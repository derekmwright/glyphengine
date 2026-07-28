package renderer

import "github.com/vkngwrapper/core/v3/core1_0"

// SkinnedVertex extends Vertex with joint indices and weights for skeletal animation.
// Stride: 68 bytes (Pos[3] + Color[3] + Normal[3] + UV[2] + Joints[4]uint16 + Weights[4]float32).
type SkinnedVertex struct {
	Pos     [3]float32
	Color   [3]float32
	Normal  [3]float32
	UV      [2]float32
	Joints  [4]uint16
	Weights [4]float32
}

func skinnedVertexBindingDescription() core1_0.VertexInputBindingDescription {
	return core1_0.VertexInputBindingDescription{
		Binding:   0,
		Stride:    68,
		InputRate: core1_0.VertexInputRateVertex,
	}
}

func skinnedVertexAttributeDescriptions() []core1_0.VertexInputAttributeDescription {
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
		{
			Location: 4,
			Binding:  0,
			Format:   core1_0.FormatR16G16B16A16UnsignedInt,
			Offset:   44, // Joints
		},
		{
			Location: 5,
			Binding:  0,
			Format:   core1_0.FormatR32G32B32A32SignedFloat,
			Offset:   52, // Weights
		},
	}
}
