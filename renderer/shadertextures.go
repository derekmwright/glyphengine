package renderer

import (
	"fmt"

	"github.com/vkngwrapper/core/v3/core1_0"
)

// ShaderTextureSlots are combined image samplers at light-set bindings 7..10,
// visible in vertex and fragment shaders. Nil bindings use the white fallback.
const ShaderTextureSlots = 4

func (r *Renderer) SetShaderTexture(slot int, t *Texture) error {
	if slot < 0 || slot >= ShaderTextureSlots {
		return fmt.Errorf("shader texture slot: %d outside [0,%d)", slot, ShaderTextureSlots)
	}
	if t != nil && t.destroyed {
		return fmt.Errorf("shader texture: destroyed texture")
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
	if slot < 0 || slot >= ShaderTextureSlots {
		return fmt.Errorf("shader target slot: %d outside [0,%d)", slot, ShaderTextureSlots)
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
	for slot := range ShaderTextureSlots {
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
		if p.desc.Fullscreen {
			continue
		}
		for i := range p.draws {
			tex := p.draws[i].Texture
			if tex != nil && tex.target != nil && tex.target == p.desc.Target && !tex.target.desc.History {
				return fmt.Errorf("app pass %q: Texture: cannot sample Target without History", p.desc.Name)
			}
		}
	}
	return nil
}
