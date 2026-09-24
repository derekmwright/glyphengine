package renderer

import (
	"encoding/binary"
	"fmt"
	"slices"

	"github.com/vkngwrapper/core/v3/core1_0"
)

// AppComputeDesc schedules a compute dispatch on the graphics queue. Reads are
// sampled at set 2 bindings 0..3; Writes are storage images at bindings 4..7.
// Buffers supplies up to four read/write storage buffers at bindings 8..11.
// Calls require the renderer thread, as do application graphics passes.
type AppComputeDesc struct {
	Name    string
	Stage   PassStage
	Comp    []byte
	Reads   []*Texture
	Writes  []*RenderTarget
	Buffers []*StorageBuffer
	Timed   bool
}

// AppCompute shares ordering, timing and lifetime with application graphics work.
type AppCompute struct {
	pass     *AppPass
	desc     AppComputeDesc
	dispatch [3]uint32
}

func (p *AppCompute) SetEnabled(on bool) { p.pass.SetEnabled(on) }

// SetDispatch sets workgroup counts. Any zero axis skips commands while retaining
// the timing edges. Dispatch starts at zero until the application supplies it.
func (p *AppCompute) SetDispatch(x, y, z uint32) { p.dispatch = [3]uint32{x, y, z} }

// SetPushConstants copies the application half of the 256-byte push block.
// The first half contains the scene VP and an identity model matrix.
func (p *AppCompute) SetPushConstants(data []byte) error { return p.pass.SetPushConstants(data) }

func (r *Renderer) validateAppCompute(d AppComputeDesc) error {
	fail := func(field, why string) error { return fmt.Errorf("app compute %q: %s: %s", d.Name, field, why) }
	if d.Stage < StageBeforeScene || d.Stage > StageBeforeTonemap {
		return fail("Stage", "unknown stage")
	}
	if len(d.Comp) < 20 || len(d.Comp)%4 != 0 || binary.LittleEndian.Uint32(d.Comp) != 0x07230203 {
		return fail("Comp", "invalid SPIR-V header or length")
	}
	if len(d.Writes) > 4 {
		return fail("Writes", "at most four outputs")
	}
	if len(d.Buffers) > 4 {
		return fail("Buffers", "at most four buffers")
	}
	for i, b := range d.Buffers {
		if b == nil || b.r != r || b.destroyed {
			return fail("Buffers", "requires live buffers of this renderer")
		}
		if slices.Contains(d.Buffers[:i], b) {
			return fail("Buffers", "duplicate buffer")
		}
	}
	for i, t := range d.Writes {
		if t == nil {
			return fail("Writes", "nil target")
		}
		if t.r != r || t.destroyed {
			return fail("Writes", fmt.Sprintf("target %q is not a live target of this renderer", t.desc.Name))
		}
		if !t.desc.Storage {
			return fail("Writes", fmt.Sprintf("target %q needs Storage", t.desc.Name))
		}
		if slices.Contains(d.Writes[:i], t) {
			return fail("Writes", fmt.Sprintf("duplicate target %q", t.desc.Name))
		}
	}
	if err := r.validateAppReads(d.Reads, d.Stage, false, fail); err != nil {
		return err
	}
	for _, t := range d.Reads {
		if t.target != nil && slices.Contains(d.Writes, t.target) && !t.target.desc.History {
			return fail("Reads", fmt.Sprintf("target %q needs History when also written", t.target.desc.Name))
		}
	}
	return r.validateAppTiming(d.Timed, fail)
}

func (r *Renderer) CreateAppCompute(d AppComputeDesc) (_ *AppCompute, err error) {
	if err = r.validateAppCompute(d); err != nil {
		return nil, err
	}
	if err = r.ensureComputeLayout(); err != nil {
		return nil, fmt.Errorf("app compute %q: input layout: %w", d.Name, err)
	}
	d.Reads, d.Writes = slices.Clone(d.Reads), slices.Clone(d.Writes)
	d.Buffers = slices.Clone(d.Buffers)
	c := &AppCompute{desc: d}
	p := &AppPass{r: r, desc: AppPassDesc{Name: d.Name, Stage: d.Stage, Reads: d.Reads, Timed: d.Timed}, enabled: true, compute: c}
	c.pass = p
	r.appPasses = append(r.appPasses, p)
	defer func() {
		if err != nil {
			r.appPasses = r.appPasses[:len(r.appPasses)-1]
			p.release()
		}
	}()
	if _, err = r.buildAppGraph(); err != nil {
		return nil, err
	}
	p.pipeline, p.layout, err = createAppComputePipeline(r.deviceDriver, d.Comp, r.descriptorSetLayout, r.shadow.descriptorSetLayout, r.computeSetLayout)
	if err != nil {
		return nil, fmt.Errorf("app compute %q: pipeline: %w", d.Name, err)
	}
	p.sets, err = r.allocatePassSets(p, maxFramesInFlight)
	if err != nil {
		return nil, fmt.Errorf("app compute %q: inputs: %w", d.Name, err)
	}
	r.graphDirty = true
	return c, nil
}

func (r *Renderer) DestroyAppCompute(p *AppCompute) {
	if p != nil {
		r.DestroyAppPass(p.pass)
	}
}

func (r *Renderer) ensureComputeLayout() error {
	if r.computeSetLayout.Handle() != 0 {
		return nil
	}
	bindings := make([]core1_0.DescriptorSetLayoutBinding, 12)
	for i := range bindings {
		kind := core1_0.DescriptorTypeCombinedImageSampler
		if i >= 4 {
			kind = core1_0.DescriptorTypeStorageImage
		}
		if i >= 8 {
			kind = core1_0.DescriptorTypeStorageBuffer
		}
		bindings[i] = core1_0.DescriptorSetLayoutBinding{Binding: i, DescriptorType: kind, DescriptorCount: 1, StageFlags: core1_0.StageCompute}
	}
	l, _, err := r.deviceDriver.CreateDescriptorSetLayout(nil, core1_0.DescriptorSetLayoutCreateInfo{Bindings: bindings})
	if err == nil {
		r.computeSetLayout = l
	}
	return err
}

func createAppComputePipeline(d core1_0.DeviceDriver, code []byte, layouts ...core1_0.DescriptorSetLayout) (core1_0.Pipeline, core1_0.PipelineLayout, error) {
	module, _, err := d.CreateShaderModule(nil, core1_0.ShaderModuleCreateInfo{Code: bytesToUint32Slice(code)})
	if err != nil {
		return core1_0.Pipeline{}, core1_0.PipelineLayout{}, err
	}
	defer d.DestroyShaderModule(module, nil)
	layout, _, err := d.CreatePipelineLayout(nil, core1_0.PipelineLayoutCreateInfo{SetLayouts: layouts,
		PushConstantRanges: []core1_0.PushConstantRange{{StageFlags: core1_0.StageCompute, Size: pushConstantSize}}})
	if err != nil {
		return core1_0.Pipeline{}, core1_0.PipelineLayout{}, err
	}
	pipelines, _, err := d.CreateComputePipelines(nil, nil, core1_0.ComputePipelineCreateInfo{
		Stage: core1_0.PipelineShaderStageCreateInfo{Stage: core1_0.StageCompute, Module: module, Name: "main"}, Layout: layout})
	if err != nil {
		for _, p := range pipelines {
			if p.Handle() != 0 {
				d.DestroyPipeline(p, nil)
			}
		}
		d.DestroyPipelineLayout(layout, nil)
		return core1_0.Pipeline{}, core1_0.PipelineLayout{}, err
	}
	return pipelines[0], layout, nil
}

func (p *AppCompute) active(*graphFrame) bool {
	return p.pass.enabled && p.dispatch[0] != 0 && p.dispatch[1] != 0 && p.dispatch[2] != 0
}

func (p *AppCompute) record(c *graphFrame) {
	s, d, pass := c.scratch, c.driver, p.pass
	d.CmdBindPipeline(c.cmd, core1_0.PipelineBindPointCompute, pass.pipeline)
	s.bindDescriptorSets(d, c.cmd, core1_0.PipelineBindPointCompute, pass.layout, 0, pass.r.fallbackTexture.DescriptorSet, c.shadowDS, pass.sets[c.frame])
	s.resetPC()
	copy(s.pc[:16], c.lighting.VP[:])
	s.pc[16], s.pc[21], s.pc[26], s.pc[31] = 1, 1, 1, 1
	copy(s.pc[32:], pass.push[:])
	s.pushConstants(d, c.cmd, pass.layout, core1_0.StageCompute)
	d.CmdDispatch(c.cmd, int(p.dispatch[0]), int(p.dispatch[1]), int(p.dispatch[2]))
}

func (r *Renderer) flushComputeOutputs(p *AppCompute, set core1_0.DescriptorSet, frame int) error {
	for i, b := range p.desc.Buffers {
		instance := 0
		if b.desc.History {
			instance = frame % 2
		}
		r.appBufferInfos[0] = core1_0.DescriptorBufferInfo{Buffer: b.buffers[instance], Range: b.desc.Size}
		r.appWrites[0] = core1_0.WriteDescriptorSet{DstSet: set, DstBinding: 8 + i, DescriptorType: core1_0.DescriptorTypeStorageBuffer, BufferInfo: r.appBufferInfos[:]}
		if err := r.deviceDriver.UpdateDescriptorSets(r.appWrites[:], nil); err != nil {
			return err
		}
	}
	// Unused storage bindings are not accessed by the shader and need no dummy
	// image. A live writer owns every referenced target until its sets retire.
	for i, t := range p.desc.Writes {
		instance := 0
		if t.desc.History {
			instance = frame % 2
		}
		r.appInfos[0] = core1_0.DescriptorImageInfo{ImageView: t.color.views[instance], ImageLayout: core1_0.ImageLayoutGeneral}
		r.appWrites[0] = core1_0.WriteDescriptorSet{DstSet: set, DstBinding: 4 + i, DescriptorType: core1_0.DescriptorTypeStorageImage, ImageInfo: r.appInfos[:1]}
		if err := r.deviceDriver.UpdateDescriptorSets(r.appWrites[:1], nil); err != nil {
			return err
		}
	}
	return nil
}
