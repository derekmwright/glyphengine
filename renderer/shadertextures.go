package renderer

import (
	"fmt"

	"github.com/vkngwrapper/core/v3/core1_0"
)

// ShaderTextureSlots are combined image samplers at light-set bindings 7..10,
// visible in vertex, fragment and compute shaders. Nil bindings use the white fallback.
const ShaderTextureSlots = 4

// ShaderTextureFirstBinding is the first shared application sampler in the
// light descriptor set (set 1, or set 2 in skinned pipelines).
const ShaderTextureFirstBinding = 7
const maxShaderTextureSlots = 32

// WithShaderTextureSlots extends the shared sampler layout at construction.
// Zero keeps the four-slot contract. New rejects counts below four or above
// Capabilities.MaxShaderTextureSlots; it never silently changes shader bindings.
func WithShaderTextureSlots(count int) Option {
	return func(r *Renderer) { r.shaderTextureSlots = count }
}

// ShaderTextureCapacity is the enabled shared sampler count. Slot i is binding
// ShaderTextureFirstBinding+i. Existing slots keep their layout and lifetime.
func (r *Renderer) ShaderTextureCapacity() int {
	if r.shaderTextureSlots == 0 {
		return ShaderTextureSlots
	}
	return r.shaderTextureSlots
}

func shaderTextureLimit(l *core1_0.PhysicalDeviceLimits) int {
	// Reserve the other pipeline samplers: two light maps, five material
	// textures and four pass-local reads. Some pipelines use fewer, but the
	// shared layout must be legal for every supported consumer.
	const reserved = 2 + materialTextureBindings + 4
	return max(0, min(maxShaderTextureSlots, l.MaxPerStageDescriptorSamplers-reserved,
		l.MaxPerStageDescriptorSampledImages-reserved, l.MaxDescriptorSetSamplers-reserved,
		l.MaxDescriptorSetSampledImages-reserved, l.MaxPerStageResources-reserved-16))
}

func (r *Renderer) SetShaderTexture(slot int, t *Texture) error {
	if slot < 0 || slot >= r.ShaderTextureCapacity() {
		return fmt.Errorf("shader texture slot: %d outside [0,%d)", slot, r.ShaderTextureCapacity())
	}
	if t != nil && t.destroyed {
		return fmt.Errorf("shader texture: destroyed texture")
	}
	// The shared slots stay 2D. The engine's own stages -- lit, terrain,
	// water, the sky slot's fillings -- declare them sampler2D, and one slot
	// layout serves every pipeline, so a volume here would be a 3D view
	// behind a 2D declaration in whichever engine shader next read the slot.
	// A volume binds through a pass's Reads, where the pass declares its shape.
	if t.Dimensions() == 3 {
		return fmt.Errorf("shader texture: slot %d: a 3D texture cannot fill a shared slot; the engine's shaders declare sampler2D there, so bind it through AppPassDesc.Reads with Reads3D", slot)
	}
	if t != nil && t.target != nil {
		return r.SetShaderTarget(slot, t.target)
	}
	if r.shaderTextures[slot] == t && r.shaderTargets[slot] == nil {
		return nil
	}
	if r.shaderTargets[slot] != nil {
		r.graphDirty = true
	}
	r.shaderTextures[slot], r.shaderTargets[slot] = t, nil
	return nil
}

func (r *Renderer) SetShaderTarget(slot int, t *RenderTarget) error {
	if slot < 0 || slot >= r.ShaderTextureCapacity() {
		return fmt.Errorf("shader target slot: %d outside [0,%d)", slot, r.ShaderTextureCapacity())
	}
	if t != nil && (t.r != r || t.destroyed) {
		return fmt.Errorf("shader target: not a live target of this renderer")
	}
	if r.shaderTargets[slot] != t || r.shaderTextures[slot] != nil {
		r.graphDirty = true
	}
	r.shaderTargets[slot], r.shaderTextures[slot] = t, nil
	return nil
}

// Scratch slices survive the interface call. All writes are to the just-waited
// frame slot, including when a history target or a scene-depth instance changes.
func (r *Renderer) writeAppSampler(set core1_0.DescriptorSet, binding int, tex *Texture) error {
	if tex == nil || tex.destroyed {
		tex = r.fallbackTexture
	}
	r.appInfos[0] = core1_0.DescriptorImageInfo{Sampler: tex.sampler, ImageView: tex.view, ImageLayout: core1_0.ImageLayoutShaderReadOnlyOptimal}
	r.appWrites[0] = core1_0.WriteDescriptorSet{DstSet: set, DstBinding: binding, DescriptorType: core1_0.DescriptorTypeCombinedImageSampler, ImageInfo: r.appInfos[:1]}
	return r.deviceDriver.UpdateDescriptorSets(r.appWrites[:1], nil)
}

func (r *Renderer) flushShaderTextures(frame int) error {
	for slot := range r.ShaderTextureCapacity() {
		t := r.shaderTextures[slot]
		if target := r.shaderTargets[slot]; target != nil && !target.destroyed {
			t = target.Texture()
		}
		if t == nil || t.destroyed {
			t = r.fallbackTexture
		}
		old := r.shaderSlotImages[frame][slot]
		if old.ImageView.Handle() == t.view.Handle() && old.Sampler.Handle() == t.sampler.Handle() {
			continue
		}
		if err := r.writeAppSampler(r.shadow.descriptorSets[frame], 7+slot, t); err != nil {
			return err
		}
		r.shaderSlotImages[frame][slot] = r.appInfos[0]
	}
	return nil
}

func (r *Renderer) flushAppInputs(p *AppPass, set core1_0.DescriptorSet) error {
	for i := 0; i < 4; i++ {
		t := r.fallbackTexture
		if i < len(p.desc.Reads) {
			t = p.desc.Reads[i]
		}
		if err := r.writeAppSampler(set, i, t); err != nil {
			return err
		}
	}
	return nil
}

func (r *Renderer) prepareAppFrame(frame, imageIndex int) error {
	if r.graphDirty {
		if err := r.replaceAppGraph(true); err != nil {
			return err
		}
	}
	return r.flushAppFrameBindings(frame, imageIndex)
}

func (r *Renderer) flushAppFrameBindings(frame, imageIndex int) error {
	r.prepareSceneColor(imageIndex)
	for _, t := range r.appTargets {
		t.selectTexture(frame)
	}
	if r.depthResolve != nil {
		if err := r.prepareSceneDepth(imageIndex); err != nil {
			return err
		}
	}
	if err := r.flushShaderTextures(frame); err != nil {
		return err
	}
	for _, p := range r.appPasses {
		if p.desc.Fullscreen && len(p.draws) > 0 {
			return fmt.Errorf("app pass %q: Fullscreen cannot have mesh SetDraws", p.desc.Name)
		}
		if err := r.flushAppInputs(p, p.sets[frame]); err != nil {
			return err
		}
		if p.params != nil {
			// Both halves of the block belong to the slot the fence has just
			// released: the bytes the application staged, and the descriptor
			// naming this slot's buffer. The descriptor is rewritten per frame
			// rather than once at creation for the reason the samplers above
			// are -- a resize frees and reallocates every pass's sets, and this
			// is the one path that runs for every slot afterwards.
			p.flushAppParams(frame)
			if err := r.writeAppParams(p, p.sets[frame], frame); err != nil {
				return err
			}
		}
		if p.compute != nil {
			if err := r.flushComputeOutputs(p.compute, p.sets[frame], frame); err != nil {
				return err
			}
			continue
		}
		if p.desc.Fullscreen {
			continue
		}
		for i := range p.draws {
			tex := p.draws[i].Texture
			if tex != nil && tex.target != nil && tex.target == p.desc.Target && !tex.target.desc.History {
				return fmt.Errorf("app pass %q: Texture: cannot sample Target without History", p.desc.Name)
			}
			// Set 0 binding 0 is the draw's own descriptor set, bound as the
			// texture is handed over, so this loop is the one place a volume
			// there can be refused before its 3D view reaches a sampler2D.
			if tex.Dimensions() == 3 {
				return fmt.Errorf("app pass %q: Texture: draw %d has a 3D texture; set 0 binding 0 is sampler2D, so bind a volume through Reads with Reads3D", p.desc.Name, i)
			}
		}
	}
	return nil
}
