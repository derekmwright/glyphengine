package renderer

import (
	"encoding/binary"
	"fmt"
	"math"
	"slices"

	"github.com/vkngwrapper/core/v3/core1_0"
)

type PassStage int

const (
	StageBeforeScene PassStage = iota + 1
	StageAfterScene
	StageBeforeBloom
	StageBeforeTonemap
)

type BlendMode int

const (
	BlendNone BlendMode = iota
	BlendAdditive
	BlendAlpha
)

// AppPassDesc is a graphics node in the renderer's ordered frame graph.
// Reads supplies up to four textures at set 2; SceneColor and SceneDepth expose
// the engine's outputs. All pass and target methods require the renderer thread.
type AppPassDesc struct {
	Name       string
	Stage      PassStage
	Target     *RenderTarget
	Load       bool
	Clear      [4]float32
	Blend      BlendMode
	DepthTest  bool
	Reads      []*Texture
	Vert, Frag []byte
	Fullscreen bool
	Timed      bool
	// Params is the size in bytes of a uniform block private to this pass, at
	// set 2 binding 12, written through SetParams. It must be a multiple of 16
	// and at most AppParamBytes; 0 means the pass has no block, and then its
	// descriptor layout, its pipeline and its per-frame descriptor writes are
	// exactly what they were before the block existed.
	//
	// It is separate from the game's 4096-byte block at set 1 binding 6, which
	// SetShaderParameters replaces whole: a package the game imports cannot
	// claim a slice of that one without the game partitioning bytes by hand.
	Params int
}

type AppPass struct {
	compute            *AppCompute
	r                  *Renderer
	desc               AppPassDesc
	enabled, destroyed bool
	pipeline           core1_0.Pipeline
	layout             core1_0.PipelineLayout
	sets               []core1_0.DescriptorSet
	draws              []RenderObject
	push               [32]float32
	// params is nil for a pass that declared none, and every decision about the
	// block -- which set layout to allocate from, whether to write the
	// descriptor, whether to copy anything per frame -- reads this rather than
	// desc.Params, so there is one answer to it.
	params *appParams
}

func (p *AppPass) SetEnabled(on bool) { p.enabled = on }

// SetDraws retains the caller's slice until replaced. Mesh, Model and Texture
// are consumed at DrawFrame; the other RenderObject fields are ignored.
// Fullscreen passes draw one triangle and cannot accept mesh draws.
func (p *AppPass) SetDraws(draws []RenderObject) { p.draws = draws }

// SetPushConstants copies up to 128 application bytes at offset 128 of the
// fixed 256-byte vertex/fragment push block. Nil clears the application block.
func (p *AppPass) SetPushConstants(data []byte) error {
	if len(data) > 128 || len(data)%16 != 0 {
		return fmt.Errorf("app pass %q: push constants size %d must be a multiple of 16, at most 128", p.desc.Name, len(data))
	}
	clear(p.push[:])
	for i := 0; i < len(data)/4; i++ {
		p.push[i] = math.Float32frombits(binary.LittleEndian.Uint32(data[i*4:]))
	}
	return nil
}

func (r *Renderer) validateAppPass(d AppPassDesc) error {
	fail := func(field, why string) error { return fmt.Errorf("app pass %q: %s: %s", d.Name, field, why) }
	wrap := func(field string, err error) error { return fmt.Errorf("app pass %q: %s: %w", d.Name, field, err) }
	if d.Stage < StageBeforeScene || d.Stage > StageBeforeTonemap {
		return fail("Stage", "unknown stage")
	}
	if d.Blend < BlendNone || d.Blend > BlendAlpha {
		return fail("Blend", "unknown blend mode")
	}
	if err := validateAppParams(d.Params, fail); err != nil {
		return err
	}
	if err := r.validateAppName(d.Name, fail); err != nil {
		return err
	}
	if d.Target == nil {
		if d.Stage != StageBeforeBloom && d.Stage != StageBeforeTonemap {
			return fail("Target", "HDR requires StageBeforeBloom or StageBeforeTonemap")
		}
		if !d.Load {
			return fail("Load", "HDR can only be loaded")
		}
	} else {
		if d.Target.r != r || d.Target.destroyed {
			return fail("Target", "not a live target of this renderer")
		}
		if d.DepthTest && !d.Target.desc.Depth {
			return fail("DepthTest", "Target has no depth attachment")
		}
	}
	if err := r.validateAppReads(d.Reads, d.Stage, d.Target == nil, fail); err != nil {
		return err
	}
	for _, t := range d.Reads {
		if t.target != nil && t.target == d.Target && !t.target.desc.History {
			return fail("Reads", "cannot read Target without History")
		}
	}
	if err := r.validateAppTiming(d.Timed, fail, wrap); err != nil {
		return err
	}
	for _, sh := range []struct {
		name string
		code []byte
	}{{"Vert", d.Vert}, {"Frag", d.Frag}} {
		if len(sh.code) < 20 || len(sh.code)%4 != 0 || binary.LittleEndian.Uint32(sh.code) != 0x07230203 {
			return fail(sh.name, "invalid SPIR-V header or length")
		}
	}
	mesh, err := appVertexInputs(d.Vert)
	if err != nil {
		return fail("Vert", err.Error())
	}
	if mesh == d.Fullscreen {
		return fail("Fullscreen", "must match the vertex shader's location inputs (mesh or fullscreen)")
	}
	return nil
}

func (r *Renderer) validateAppReads(inputs []*Texture, stage PassStage, hdr bool, fail func(string, string) error) error {
	if len(inputs) > 4 {
		return fail("Reads", "at most four inputs")
	}
	for _, t := range inputs {
		if t == nil {
			return fail("Reads", "nil texture")
		}
		if t.destroyed {
			return fail("Reads", "destroyed texture")
		}
		if target := t.target; target != nil {
			if target.r != r || target.destroyed {
				return fail("Reads", "not a live target of this renderer")
			}
		}
		if t.scene != nil {
			if t.scene != r {
				return fail("Reads", "scene texture belongs to another renderer")
			}
			if t.sceneDepth {
				if stage == StageBeforeScene {
					return fail("Reads", "SceneDepth is available after the scene")
				}
			} else if stage == StageBeforeScene || hdr {
				return fail("Reads", "SceneColor requires an own target after the scene")
			}
		}
	}
	return nil
}

// validateAppName rejects a name another live application pass already holds.
//
// RenderStats attributes submitted work per pass by name, and GPU timings are
// reported the same way, so two passes called the same thing give a game two
// indistinguishable rows and no way to tell which effect is the expensive one.
// Rejecting it at creation is the only point where the application still knows
// which pass it meant. Destroying a pass frees its name again.
func (r *Renderer) validateAppName(name string, fail func(string, string) error) error {
	for _, p := range r.appPasses {
		if p.desc.Name == name {
			return fail("Name", "already used by a live application pass")
		}
	}
	return nil
}

// validateAppTiming refuses a timed pass the device cannot time, then enforces
// the shared timing capacity.
//
// The refusal is the one place the engine hands a missing optional capability
// back to the game instead of absorbing it, and the distinction is what it is
// the caller asked for. Batched ranges without multi-draw indirect still draw
// the geometry, so the engine takes that fallback silently and reports it in
// Capabilities. A pass asking to be Timed is asking for a measurement, and on a
// device that cannot timestamp the graphics queue there is no measurement to
// give: the pass would be created, run forever, and never once appear in
// GPUTimings().App. A game would read a frame breakdown with its own effect
// missing from it and conclude the effect was free.
//
// wrap puts the sentinel behind the caller's "app pass %q: Timed:" prefix with
// %w, so errors.Is reaches ErrCapabilityUnavailable through it.
func (r *Renderer) validateAppTiming(timed bool, fail func(string, string) error, wrap func(string, error) error) error {
	if timed {
		if !r.caps.GPUTimestamps {
			return wrap("Timed", unavailable("GPU timestamps"))
		}
		n := 0
		for _, p := range r.appPasses {
			if p.desc.Timed {
				n++
			}
		}
		if n >= maxAppTimings {
			return fail("Timed", "at most sixteen timed passes")
		}
	}
	return nil
}

// Reflection here only distinguishes a procedural triangle from a vertex-input
// shader. Vulkan remains responsible for validating the pipeline interface.
func appVertexInputs(code []byte) (bool, error) {
	locations, err := appVertexLocations(code)
	return len(locations) > 0, err
}
func appVertexLocations(code []byte) (map[uint32]bool, error) {
	inputs := map[uint32]bool{}
	decorations := map[uint32]uint32{}
	locations := map[uint32]bool{}
	for off := 20; off < len(code); {
		word := binary.LittleEndian.Uint32(code[off:])
		n := int(word>>16) * 4
		op := word & 0xffff
		if n < 4 || off+n > len(code) {
			return nil, fmt.Errorf("malformed SPIR-V instruction")
		}
		if op == 59 && n >= 16 && binary.LittleEndian.Uint32(code[off+12:]) == 1 {
			inputs[binary.LittleEndian.Uint32(code[off+8:])] = true
		}
		if op == 71 && n >= 16 && binary.LittleEndian.Uint32(code[off+8:]) == 30 {
			decorations[binary.LittleEndian.Uint32(code[off+4:])] = binary.LittleEndian.Uint32(code[off+12:])
		}
		off += n
	}
	for id := range inputs {
		if loc, ok := decorations[id]; ok {
			if loc > 3 {
				return nil, fmt.Errorf("vertex Location %d is outside the engine Vertex layout", loc)
			}
			locations[loc] = true
		}
	}
	return locations, nil
}

func (r *Renderer) CreateAppPass(d AppPassDesc) (_ *AppPass, err error) {
	if err = r.validateAppPass(d); err != nil {
		return nil, err
	}
	if err = r.ensureAppLayout(d.Params > 0); err != nil {
		return nil, err
	}
	d.Reads = slices.Clone(d.Reads)
	p := &AppPass{r: r, desc: d, enabled: true}
	r.appPasses = append(r.appPasses, p)
	defer func() {
		if err != nil {
			r.appPasses = r.appPasses[:len(r.appPasses)-1]
			p.release()
		}
	}()
	if d.Params > 0 {
		// Inside the unwind above, and before the pipeline: appLayoutFor reads
		// p.params to pick the set layout the pipeline is created against.
		if err = r.createAppParams(p, d.Params); err != nil {
			return nil, err
		}
	}
	f, e := r.buildAppGraph()
	if e != nil {
		return nil, e
	}
	index := f.appNode(p)
	rp := f.pipelineFormats(index)
	p.pipeline, p.layout, err = createAppPipeline(r.deviceDriver, d, rp, r.descriptorSetLayout, r.shadow.descriptorSetLayout, r.appLayoutFor(p), f.plan.Steps[index].RenderPass.Samples)
	if err != nil {
		return nil, fmt.Errorf("app pass %q: pipeline: %w", d.Name, err)
	}
	p.sets, err = r.allocatePassSets(p, maxFramesInFlight)
	if err != nil {
		return nil, err
	}
	r.graphDirty = true
	return p, nil
}

func (r *Renderer) DestroyAppPass(p *AppPass) {
	if p == nil || p.r != r || p.destroyed {
		return
	}
	p.destroyed = true
	r.appPasses = slices.DeleteFunc(r.appPasses, func(x *AppPass) bool { return x == p })
	r.graphDirty = true
	if r.gpuTimer != nil {
		delete(r.gpuTimer.appSums, p)
	}
	r.DeferDestroy(p.release)
}

func (p *AppPass) release() {
	d := p.r.deviceDriver
	p.releaseSets()
	p.params.release(d)
	p.params = nil
	if p.pipeline.Handle() != 0 {
		d.DestroyPipeline(p.pipeline, nil)
		p.pipeline = core1_0.Pipeline{}
	}
	if p.layout.Handle() != 0 {
		d.DestroyPipelineLayout(p.layout, nil)
		p.layout = core1_0.PipelineLayout{}
	}
}

// ensureAppLayout creates the graphics pass set-2 layout, in the plain form or
// the one that also declares the private uniform block.
//
// Two layouts rather than one that always declares the block: a pass that asks
// for no Params allocates from a layout byte-identical to the one it always
// had, so its pipeline, its descriptor writes and its recorded stream are
// unchanged. A single layout with an always-declared, sometimes-unwritten
// uniform descriptor would be a pass reading an undefined descriptor the moment
// a shader touched the binding, and would charge the pool for every pass.
func (r *Renderer) ensureAppLayout(params bool) error {
	if params && r.appParamsSetLayout.Handle() != 0 {
		return nil
	}
	if !params && r.appSetLayout.Handle() != 0 {
		return nil
	}
	bindings := make([]core1_0.DescriptorSetLayoutBinding, 4)
	for i := range bindings {
		bindings[i] = core1_0.DescriptorSetLayoutBinding{Binding: i, DescriptorType: core1_0.DescriptorTypeCombinedImageSampler, DescriptorCount: 1, StageFlags: core1_0.StageVertex | core1_0.StageFragment}
	}
	if params {
		bindings = append(bindings, core1_0.DescriptorSetLayoutBinding{Binding: appParamsBinding, DescriptorType: core1_0.DescriptorTypeUniformBuffer, DescriptorCount: 1, StageFlags: core1_0.StageVertex | core1_0.StageFragment})
	}
	l, _, err := r.deviceDriver.CreateDescriptorSetLayout(nil, core1_0.DescriptorSetLayoutCreateInfo{Bindings: bindings})
	if params {
		r.appParamsSetLayout = l
	} else {
		r.appSetLayout = l
	}
	return err
}

// appLayoutFor is the set-2 layout p's pipeline and descriptor sets use. A nil
// pass is the scene-depth node, a graphics pass with no block of its own.
func (r *Renderer) appLayoutFor(p *AppPass) core1_0.DescriptorSetLayout {
	switch {
	case p == nil:
		return r.appSetLayout
	case p.compute != nil && p.params != nil:
		return r.computeParamsSetLayout
	case p.compute != nil:
		return r.computeSetLayout
	case p.params != nil:
		return r.appParamsSetLayout
	}
	return r.appSetLayout
}

func (r *Renderer) allocatePassSets(p *AppPass, n int) ([]core1_0.DescriptorSet, error) {
	l := make([]core1_0.DescriptorSetLayout, n)
	layout := r.appLayoutFor(p)
	for i := range l {
		l[i] = layout
	}
	s, _, err := r.deviceDriver.AllocateDescriptorSets(core1_0.DescriptorSetAllocateInfo{DescriptorPool: r.descriptorPool, SetLayouts: l})
	return s, err
}

func (p *AppPass) record(c *graphFrame) {
	s := c.scratch
	d := c.driver
	d.CmdBindPipeline(c.cmd, core1_0.PipelineBindPointGraphics, p.pipeline)
	e := c.extent
	if p.desc.Target != nil {
		e = p.desc.Target.color.extent
	}
	s.setViewport(d, c.cmd, core1_0.Viewport{Width: float32(e.Width), Height: float32(e.Height), MinDepth: 0, MaxDepth: 1})
	s.setScissor(d, c.cmd, core1_0.Rect2D{Extent: e})
	s.resetPC()
	copy(s.pc[:16], c.lighting.VP[:])
	copy(s.pc[32:], p.push[:])
	if p.desc.Fullscreen {
		s.pc[16], s.pc[21], s.pc[26], s.pc[31] = 1, 1, 1, 1
		s.bindDescriptorSets(d, c.cmd, core1_0.PipelineBindPointGraphics, p.layout, 0, p.r.fallbackTexture.DescriptorSet, c.shadowDS, p.sets[c.frame])
		s.pushConstants(d, c.cmd, p.layout, core1_0.StageVertex|core1_0.StageFragment)
		d.CmdDraw(c.cmd, 3, 1, 0, 0)
		// One draw of one triangle. A fullscreen pass is submitted work like any
		// other, and reporting it as nothing hid whole postprocess chains.
		c.stats.addAppDraw(c.appSlot, 1, 0, 3)
		return
	}
	for i := range p.draws {
		draw := &p.draws[i]
		if draw.Mesh == nil {
			continue
		}
		copy(s.pc[16:32], draw.Model[:])
		tex := draw.Texture
		if tex == nil || tex.destroyed {
			tex = p.r.fallbackTexture
		}
		s.bindDescriptorSets(d, c.cmd, core1_0.PipelineBindPointGraphics, p.layout, 0, tex.DescriptorSet, c.shadowDS, p.sets[c.frame])
		s.pushConstants(d, c.cmd, p.layout, core1_0.StageVertex|core1_0.StageFragment)
		s.bindVertexBuffers(d, c.cmd, 0, draw.Mesh.vertexBuffer)
		if draw.Mesh.IndexCount > 0 {
			d.CmdBindIndexBuffer(c.cmd, draw.Mesh.indexBuffer, 0, draw.Mesh.indexType)
			d.CmdDrawIndexed(c.cmd, draw.Mesh.IndexCount, 1, draw.Mesh.firstIndex, draw.Mesh.vertexOffset, 0)
		} else {
			d.CmdDraw(c.cmd, draw.Mesh.VertexCount, 1, 0, 0)
		}
		c.stats.addAppDraw(c.appSlot, 1, draw.Mesh.IndexCount, draw.Mesh.VertexCount)
	}
}

func (p *AppPass) releaseSets() {
	freeSets(p.r.deviceDriver, p.sets)
	p.sets = nil
}
