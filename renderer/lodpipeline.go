package renderer

import "github.com/vkngwrapper/core/v3/core1_0"

func (r *Renderer) createLODPipeline(kind int) (core1_0.Pipeline, core1_0.PipelineLayout, error) {
	vert, frag := r.shaders.LitLODVert, r.shaders.LitLODFrag
	cull := core1_0.CullModeBack
	bindings := []core1_0.VertexInputBindingDescription{vertexBindingDescription(), instanceBindingDescription()}
	attrs := instanceAttributeDescriptions()
	if kind > 0 {
		cull = 0
	}
	if kind == 2 {
		vert, frag = r.shaders.ImpostorVert, r.shaders.ImpostorFrag
		bindings = bindings[1:]
		attrs = attrs[len(vertexAttributeDescriptions()):]
	}
	return createLitVariantPipelineWithInput(r.deviceDriver, vert, frag, "LOD", r.sceneFormats, r.sc.extent, r.descriptorSetLayout, r.shadow.descriptorSetLayout, r.msaaSamples, cull, false, bindings, attrs, true)
}
