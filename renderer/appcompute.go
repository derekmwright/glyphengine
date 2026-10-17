package renderer

import (
	"encoding/binary"
	"fmt"
	"slices"

	"github.com/vkngwrapper/core/v3/core1_0"
)

// AppComputeDesc schedules a compute dispatch on the graphics queue. Reads are
// sampled at set 2 bindings 0..3; Writes are storage images at bindings 4..7.
// Buffers supplies up to four read/write storage buffers at bindings 8..11, and
// Params a private uniform block at binding 12.
// Calls require the renderer thread, as do application graphics passes.
type AppComputeDesc struct {
	Name  string
	Stage PassStage
	Comp  []byte
	Reads []*Texture
	// Reads3D is AppPassDesc.Reads3D for a dispatch: the indices into Reads
	// the shader declares as sampler3D, checked against the textures' shapes
	// at creation in both directions.
	Reads3D []int
	// ReadRanges is AppPassDesc.ReadRanges for a dispatch. It is also what
	// lets a dispatch read one level of a chain it writes another level of:
	// a read of a target the dispatch also writes, without History, must name
	// a range that excludes the written level.
	ReadRanges []LevelRange
	Writes     []*RenderTarget
	// WriteLevels is parallel to Writes and selects the mip level each
	// storage binding writes; an index it does not reach writes level 0. A
	// level a target does not have, and any level but 0 on a single-level
	// target, is refused at creation. Size the dispatch from LevelExtent of
	// the level written, not from Extent.
	WriteLevels []int
	Buffers     []*StorageBuffer
	// ReadsShadows declares that the shader samples the directional cascades at
	// set 1 bindings 0 and 1. It adds the frame-graph edge from the cascade
	// passes to this dispatch, so their depth writes are made visible to the
	// compute reads. The descriptors are bound either way, so a shader sampling
	// them without declaring it reads whatever a neighbouring pass's barriers
	// happened to leave visible. A dispatch that leaves it false records exactly
	// the stream it recorded before.
	ReadsShadows bool
	Timed        bool
	// Params is the size in bytes of a uniform block private to this dispatch,
	// at set 2 binding 12, written through SetParams. The rules are
	// AppPassDesc.Params' rules: a multiple of 16, at most AppParamBytes, and 0
	// for a dispatch that records exactly what it recorded before the block
	// existed.
	Params int
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

// SetParams copies into the dispatch's own uniform block at set 2 binding 12.
// See AppPass.SetParams for the slot discipline and the size rules.
func (p *AppCompute) SetParams(data []byte) error { return p.pass.SetParams(data) }

func (r *Renderer) validateAppCompute(d AppComputeDesc) error {
	fail := func(field, why string) error { return fmt.Errorf("app compute %q: %s: %s", d.Name, field, why) }
	wrap := func(field string, err error) error { return fmt.Errorf("app compute %q: %s: %w", d.Name, field, err) }
	if d.Stage < StageBeforeScene || d.Stage > StageBeforeTonemap {
		return fail("Stage", "unknown stage")
	}
	if err := r.validateAppName(d.Name, fail); err != nil {
		return err
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
	if err := validateAppParams(d.Params, fail); err != nil {
		return err
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
	if err := r.validateAppReads(d.Reads, d.Reads3D, d.Stage, false, fail); err != nil {
		return err
	}
	if err := validateAppLevels(d.Reads, d.ReadRanges, d.Writes, d.WriteLevels, fail); err != nil {
		return err
	}
	if err := validateAppOverlap(d.Reads, d.ReadRanges, d.Writes, d.WriteLevels, fail); err != nil {
		return err
	}
	return r.validateAppTiming(d.Timed, fail, wrap)
}

// validateAppOverlap refuses a read of a target the dispatch also writes,
// unless the read sees the previous frame (History) or its level range
// excludes the level written. Without either, the shader samples the
// subresource it is storing into, which no barrier can order.
func validateAppOverlap(reads []*Texture, ranges []LevelRange, writes []*RenderTarget, writeLevels []int, fail func(string, string) error) error {
	for i, t := range reads {
		target := t.target
		if target == nil || target.desc.History {
			continue
		}
		j := slices.Index(writes, target)
		if j < 0 {
			continue
		}
		if target.Levels() <= 1 {
			return fail("Reads", fmt.Sprintf("target %q needs History when also written", target.desc.Name))
		}
		rg := LevelRange{Count: target.Levels()}
		if i < len(ranges) && ranges[i].Count > 0 {
			rg = ranges[i]
		}
		level := 0
		if j < len(writeLevels) {
			level = writeLevels[j]
		}
		if level >= rg.Base && level < rg.Base+rg.Count {
			return fail("Reads", fmt.Sprintf("index %d reads levels [%d,%d) of target %q, overlapping level %d that Writes index %d writes; give it a ReadRanges entry that excludes that level",
				i, rg.Base, rg.Base+rg.Count, target.desc.Name, level, j))
		}
	}
	return nil
}

// validateAppLevels checks a pass's level declarations against the chains
// the named targets have now. Creation calls it, and so does the rebuild that
// can shorten a swapchain-relative chain, so a pass naming a level that no
// longer exists is refused there rather than binding a view of nothing.
func validateAppLevels(reads []*Texture, ranges []LevelRange, writes []*RenderTarget, writeLevels []int, fail func(string, string) error) error {
	if len(ranges) > len(reads) {
		return fail("ReadRanges", fmt.Sprintf("%d entries for %d reads", len(ranges), len(reads)))
	}
	for i, rg := range ranges {
		if rg == (LevelRange{}) {
			continue
		}
		if rg.Base < 0 || rg.Count <= 0 {
			return fail("ReadRanges", fmt.Sprintf("index %d: {Base %d, Count %d} is not a level range; the zero value is the whole chain", i, rg.Base, rg.Count))
		}
		t := reads[i]
		if t == nil || t.target == nil {
			return fail("ReadRanges", fmt.Sprintf("index %d restricts a texture that is not a render target", i))
		}
		switch n := t.target.Levels(); {
		case n <= 1:
			return fail("ReadRanges", fmt.Sprintf("index %d selects levels [%d,%d) of target %q, which has one level", i, rg.Base, rg.Base+rg.Count, t.target.desc.Name))
		case rg.Base+rg.Count > n:
			return fail("ReadRanges", fmt.Sprintf("index %d selects levels [%d,%d) of target %q, which has %d levels", i, rg.Base, rg.Base+rg.Count, t.target.desc.Name, n))
		}
	}
	if len(writeLevels) > len(writes) {
		return fail("WriteLevels", fmt.Sprintf("%d entries for %d writes", len(writeLevels), len(writes)))
	}
	for i, l := range writeLevels {
		t := writes[i]
		switch n := t.Levels(); {
		case l < 0:
			return fail("WriteLevels", fmt.Sprintf("index %d selects level %d of target %q; levels count from 0", i, l, t.desc.Name))
		case l == 0:
		case n <= 1:
			return fail("WriteLevels", fmt.Sprintf("index %d selects level %d of target %q, which has one level", i, l, t.desc.Name))
		case l >= n:
			return fail("WriteLevels", fmt.Sprintf("index %d selects level %d of target %q, which has %d levels", i, l, t.desc.Name, n))
		}
	}
	return nil
}

// createRangeViews registers every ranged read of a pass on its target. The
// views belong to the target, see RenderTarget.ranges.
func (r *Renderer) createRangeViews(reads []*Texture, ranges []LevelRange) error {
	for i, rg := range ranges {
		if rg.Count > 0 {
			if _, err := r.rangeView(reads[i].target, rg); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *Renderer) CreateAppCompute(d AppComputeDesc) (_ *AppCompute, err error) {
	if err = r.validateAppCompute(d); err != nil {
		return nil, err
	}
	if err = r.ensureComputeLayout(d.Params > 0); err != nil {
		return nil, fmt.Errorf("app compute %q: input layout: %w", d.Name, err)
	}
	if err = r.createRangeViews(d.Reads, d.ReadRanges); err != nil {
		return nil, fmt.Errorf("app compute %q: ReadRanges: %w", d.Name, err)
	}
	d.Reads, d.Writes = slices.Clone(d.Reads), slices.Clone(d.Writes)
	d.Buffers, d.Reads3D = slices.Clone(d.Buffers), slices.Clone(d.Reads3D)
	d.ReadRanges, d.WriteLevels = slices.Clone(d.ReadRanges), slices.Clone(d.WriteLevels)
	c := &AppCompute{desc: d}
	p := &AppPass{r: r, desc: AppPassDesc{Name: d.Name, Stage: d.Stage, Reads: d.Reads, Reads3D: d.Reads3D, ReadRanges: d.ReadRanges, Timed: d.Timed, Params: d.Params}, enabled: true, compute: c}
	c.pass = p
	r.appPasses = append(r.appPasses, p)
	defer func() {
		if err != nil {
			r.appPasses = r.appPasses[:len(r.appPasses)-1]
			p.release()
		}
	}()
	if d.Params > 0 {
		if err = r.createAppParams(p, d.Params); err != nil {
			return nil, err
		}
	}
	if _, err = r.buildAppGraph(); err != nil {
		return nil, err
	}
	p.pipeline, p.layout, err = createAppComputePipeline(r.deviceDriver, d.Comp, r.descriptorSetLayout, r.shadow.descriptorSetLayout, r.appLayoutFor(p))
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

// ensureComputeLayout creates the dispatch set-2 layout, in the plain form or
// the one that also declares the private uniform block. The two-layout
// reasoning is ensureAppLayout's.
func (r *Renderer) ensureComputeLayout(params bool) error {
	if params && r.computeParamsSetLayout.Handle() != 0 {
		return nil
	}
	if !params && r.computeSetLayout.Handle() != 0 {
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
	if params {
		bindings = append(bindings, core1_0.DescriptorSetLayoutBinding{Binding: appParamsBinding, DescriptorType: core1_0.DescriptorTypeUniformBuffer, DescriptorCount: 1, StageFlags: core1_0.StageCompute})
	}
	l, _, err := r.deviceDriver.CreateDescriptorSetLayout(nil, core1_0.DescriptorSetLayoutCreateInfo{Bindings: bindings})
	if err == nil {
		if params {
			r.computeParamsSetLayout = l
		} else {
			r.computeSetLayout = l
		}
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
	// Not a draw and not triangles: a dispatch is its own kind of submitted work,
	// so it stays out of DrawCalls and lands in App.Dispatches only.
	c.stats.addAppDispatch(c.appSlot)
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
		level := 0
		if i < len(p.desc.WriteLevels) {
			level = p.desc.WriteLevels[i]
		}
		r.appInfos[0] = core1_0.DescriptorImageInfo{ImageView: t.color.levelView(instance, level), ImageLayout: core1_0.ImageLayoutGeneral}
		r.appWrites[0] = core1_0.WriteDescriptorSet{DstSet: set, DstBinding: 4 + i, DescriptorType: core1_0.DescriptorTypeStorageImage, ImageInfo: r.appInfos[:1]}
		if err := r.deviceDriver.UpdateDescriptorSets(r.appWrites[:1], nil); err != nil {
			return err
		}
	}
	return nil
}
