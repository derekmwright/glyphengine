package renderer

import (
	"fmt"

	"github.com/vkngwrapper/core/v3/core1_0"
)

// ShaderParameterBytes is the capacity of the application-owned std140 block.
// Binding 6 of the shadow/light set (set 1 for sky/static lit, set 2 for skinned
// lit) is a uniform buffer visible to vertex and fragment shaders. Existing
// engine bindings and push constants are unchanged. Stock shaders ignore it.
const ShaderParameterBytes = 4096

// SetShaderParameters copies application data for subsequent DrawFrame calls.
// Call on the renderer/frame thread, normally Game.Init or Game.Update. It does
// not write in-flight GPU memory: DrawFrame copies into its slot after waiting
// for the fence. Sky and lit draws see the same snapshot. Data persists across
// frames and swapchain recreation until replaced; nil clears the whole block.
//
// Supply little-endian std140 bytes, padded to a multiple of 16. The unused
// tail is zeroed. Oversized or unaligned input returns an error and leaves the
// previous value intact. The caller owns field packing; no Go struct layout or
// shader reflection is inferred. The renderer owns the buffers and their lifetime.
func (r *Renderer) SetShaderParameters(data []byte) error {
	if len(data) > ShaderParameterBytes || len(data)%16 != 0 {
		return fmt.Errorf("shader parameters: size %d must be a multiple of 16, at most %d", len(data), ShaderParameterBytes)
	}
	clear(r.shaderParameters[:])
	copy(r.shaderParameters[:], data)
	return nil
}

func (r *Renderer) flushShaderParameters(frame int) {
	copy(r.shadow.shaderParameterMapped[frame], r.shaderParameters[:])
}

// The block shares the existing per-frame allocation, not its descriptor range.
// Align the second descriptor's offset to the actual device requirement.
func shaderParameterOffset(limits *core1_0.PhysicalDeviceLimits) (int, error) {
	// The skinned layout has four UBO descriptors across its three sets;
	// at most three are visible to either vertex or fragment stage.
	if limits.MaxUniformBufferRange < ShaderParameterBytes || limits.MaxPerStageDescriptorUniformBuffers < 3 || limits.MaxDescriptorSetUniformBuffers < 4 {
		return 0, fmt.Errorf("shader parameters require uniform range %d, 3 per-stage UBOs and 4 set UBOs; device supports %d, %d, %d", ShaderParameterBytes, limits.MaxUniformBufferRange, limits.MaxPerStageDescriptorUniformBuffers, limits.MaxDescriptorSetUniformBuffers)
	}
	alignment := max(1, limits.MinUniformBufferOffsetAlignment)
	return (litUBOSize + alignment - 1) / alignment * alignment, nil
}
