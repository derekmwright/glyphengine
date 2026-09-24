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
	if d.Stage < StageBeforeScene || d.Stage > StageBeforeTonemap {
		return fail("Stage", "unknown stage")
	}
	if d.Blend < BlendNone || d.Blend > BlendAlpha {
		return fail("Blend", "unknown blend mode")
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
	if err := r.validateAppTiming(d.Timed, fail); err != nil {
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
func (r *Renderer) validateAppTiming(timed bool, fail func(string, string) error) error {
	if timed {
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
	if err = r.ensureAppLayout(); err != nil {
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
	f, e := r.buildAppGraph()
	if e != nil {
		return nil, e
	}
	index := f.appNode(p)
	rp := f.pipelineFormats(index)
	p.pipeline, p.layout, err = createAppPipeline(r.deviceDriver, d, rp, r.descriptorSetLayout, r.shadow.descriptorSetLayout, r.appSetLayout, f.plan.Steps[index].RenderPass.Samples)
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
	if p.pipeline.Handle() != 0 {
		d.DestroyPipeline(p.pipeline, nil)
		p.pipeline = core1_0.Pipeline{}
	}
	if p.layout.Handle() != 0 {
		d.DestroyPipelineLayout(p.layout, nil)
		p.layout = core1_0.PipelineLayout{}
	}
}

func (r *Renderer) ensureAppLayout() error {
	if r.appSetLayout.Handle() != 0 {
		return nil
	}
	bindings := make([]core1_0.DescriptorSetLayoutBinding, 4)
	for i := range bindings {
		bindings[i] = core1_0.DescriptorSetLayoutBinding{Binding: i, DescriptorType: core1_0.DescriptorTypeCombinedImageSampler, DescriptorCount: 1, StageFlags: core1_0.StageVertex | core1_0.StageFragment}
	}
	l, _, err := r.deviceDriver.CreateDescriptorSetLayout(nil, core1_0.DescriptorSetLayoutCreateInfo{Bindings: bindings})
	r.appSetLayout = l
	return err
}

func (r *Renderer) allocatePassSets(p *AppPass, n int) ([]core1_0.DescriptorSet, error) {
	l := make([]core1_0.DescriptorSetLayout, n)
	for i := range l {
		l[i] = r.appSetLayout
		if p != nil && p.compute != nil {
			l[i] = r.computeSetLayout
		}
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
			d.CmdDrawIndexed(c.cmd, draw.Mesh.IndexCount, 1, 0, 0, 0)
		} else {
			d.CmdDraw(c.cmd, draw.Mesh.VertexCount, 1, 0, 0)
		}
	}
}

func (p *AppPass) releaseSets() {
	freeSets(p.r.deviceDriver, p.sets)
	p.sets = nil
}
