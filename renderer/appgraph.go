package renderer

import (
	"fmt"

	"github.com/derekmwright/glyphengine/renderer/framegraph"
	"github.com/vkngwrapper/core/v3/core1_0"
)

type appGraphTarget struct{ write, read, depth framegraph.ResourceID }

// Shared-image readers include the preceding frame and both graphics shader
// stages. Keeping this dependency fixed also keeps pass pipelines compatible
// when a slot binding changes or a preceding application node is removed.
// Sync-validation measurement: 60 frames of the apppasscheck chain plus water
// reported 240 RAW hazards from application final-layout transitions without
// the outgoing dependency, and 60 from sampling the legacy water output without
// the broad incoming scope. Both counts are zero with these dependencies.
// Scene: 640x360, 4x MSAA, plus a quad at (0,-0.7,0.1) scaled (0.8,0.1,1),
// with WaterParams{WaveLength: 2, AbsorptionDepth: 1}; count RAW messages per
// producer with VK_VALIDATION_FEATURE_ENABLE_SYNCHRONIZATION_VALIDATION_EXT.
// The legacy pass's implicit outgoing dependency ends at bottom-of-pipe, so
// incoming color-output scope alone does not include its final transition.
func appDependency() []core1_0.SubpassDependency {
	return []core1_0.SubpassDependency{{SrcSubpass: core1_0.SubpassExternal, DstSubpass: 0,
		SrcStageMask:  core1_0.PipelineStageAllCommands | core1_0.PipelineStageVertexShader | core1_0.PipelineStageFragmentShader | core1_0.PipelineStageColorAttachmentOutput | core1_0.PipelineStageEarlyFragmentTests | core1_0.PipelineStageLateFragmentTests | core1_0.PipelineStageTransfer,
		DstStageMask:  core1_0.PipelineStageVertexShader | core1_0.PipelineStageFragmentShader | core1_0.PipelineStageColorAttachmentOutput | core1_0.PipelineStageEarlyFragmentTests | core1_0.PipelineStageLateFragmentTests,
		SrcAccessMask: core1_0.AccessMemoryWrite | core1_0.AccessShaderRead | core1_0.AccessColorAttachmentWrite | core1_0.AccessDepthStencilAttachmentWrite | core1_0.AccessTransferRead,
		DstAccessMask: core1_0.AccessShaderRead | core1_0.AccessColorAttachmentRead | core1_0.AccessColorAttachmentWrite | core1_0.AccessDepthStencilAttachmentRead | core1_0.AccessDepthStencilAttachmentWrite},
		{SrcSubpass: 0, DstSubpass: core1_0.SubpassExternal,
			SrcStageMask:  core1_0.PipelineStageColorAttachmentOutput | core1_0.PipelineStageEarlyFragmentTests | core1_0.PipelineStageLateFragmentTests,
			DstStageMask:  core1_0.PipelineStageVertexShader | core1_0.PipelineStageFragmentShader | core1_0.PipelineStageColorAttachmentOutput | core1_0.PipelineStageEarlyFragmentTests | core1_0.PipelineStageLateFragmentTests | core1_0.PipelineStageTransfer,
			SrcAccessMask: core1_0.AccessColorAttachmentWrite | core1_0.AccessDepthStencilAttachmentWrite,
			DstAccessMask: core1_0.AccessShaderRead | core1_0.AccessColorAttachmentRead | core1_0.AccessColorAttachmentWrite | core1_0.AccessDepthStencilAttachmentRead | core1_0.AccessDepthStencilAttachmentWrite | core1_0.AccessTransferRead}}
}

func (r *Renderer) buildAppGraph() (*frameGraph, error) {
	f, err := newFrameGraph(r.msaaSamples, r.depth.format, r.sc.imageFormat, len(r.sc.imageViews), r)
	if err != nil {
		return nil, err
	}
	f.cache = r.frameGraph.cache
	return f, nil
}

func (f *frameGraph) appNode(p *AppPass) int {
	for i := range f.nodes {
		if f.nodes[i].app == p {
			return i
		}
	}
	panic("application pass missing from graph")
}

func (r *Renderer) extendAppGraph(f *frameGraph, g *framegraph.Graph) error {
	f.targets = make(map[*RenderTarget]appGraphTarget)
	for _, t := range r.appTargets {
		format, _ := targetFormat(t.desc.Format)
		d := framegraph.ImageDesc{Name: t.desc.Name, Format: format, Extent: t.desc.extent(), Samples: core1_0.Samples1, Aspect: core1_0.ImageAspectColor, Persistent: true}
		ids := appGraphTarget{write: g.AddImage(d), depth: -1}
		ids.read = ids.write
		if t.desc.History {
			d.Name += " history"
			ids.read = g.AddImage(d)
		}
		if t.desc.Depth {
			d.Name = t.desc.Name + " depth"
			d.Format = r.depth.format
			d.Aspect = core1_0.ImageAspectDepth
			ids.depth = g.AddImage(d)
		}
		f.targets[t] = ids
	}
	if r.depthResolve != nil {
		f.resolvedDepth = g.AddImage(framegraph.ImageDesc{Name: "resolved scene depth", Format: core1_0.FormatR32SignedFloat, Extent: framegraph.Extent{Scale: 1}, Samples: core1_0.Samples1, Aspect: core1_0.ImageAspectColor, Persistent: true})
		// The hand-recorded scene really exits in attachment layout. The next
		// graphics node derives the sampling barrier from that declared state.
		f.declarations[graphLegacy].Uses[1].FinalLayout = core1_0.ImageLayoutDepthStencilAttachmentOptimal
	}
	decl, nodes := f.declarations, f.nodes
	f.declarations = nil
	f.nodes = nil
	appendNode := func(n framegraph.Node, b graphNode) {
		f.declarations = append(f.declarations, n)
		f.nodes = append(f.nodes, b)
	}
	read := func(uses []framegraph.Use, id framegraph.ResourceID) []framegraph.Use {
		for _, u := range uses {
			if u.Resource == id {
				return uses
			}
		}
		return append(uses, framegraph.Use{Resource: id, Access: framegraph.SampledRead, Stages: core1_0.PipelineStageVertexShader | core1_0.PipelineStageFragmentShader})
	}
	slots := func(uses []framegraph.Use, target *RenderTarget) []framegraph.Use {
		for _, t := range r.shaderTargets {
			if t != nil && !t.destroyed && (t != target || t.desc.History) {
				uses = read(uses, f.targets[t].read)
			}
		}
		return uses
	}
	stage := func(stage PassStage) {
		for _, p := range r.appPasses {
			if p.desc.Stage != stage {
				continue
			}
			d := p.desc
			n := framegraph.Node{Name: d.Name, Kind: framegraph.Graphics, Optional: true, Timed: d.Timed, Dependencies: appDependency()}
			color, depth := f.color, f.depth
			if d.Target != nil {
				ids := f.targets[d.Target]
				color, depth = ids.write, ids.depth
			}
			u := framegraph.Use{Resource: color, Access: framegraph.ColorLoadWrite}
			if !d.Load {
				u.Access = framegraph.ColorWrite
				u.Clear = &framegraph.Clear{Color: d.Clear}
				u.Discard = true
			}
			if d.Target == nil && r.msaaSamples != core1_0.Samples1 {
				u.HasResolve = true
				u.ResolveTo = f.hdr
			}
			n.Uses = append(n.Uses, u)
			if d.DepthTest {
				u = framegraph.Use{Resource: depth, Access: framegraph.DepthLoadWrite}
				if d.Target != nil && !d.Load {
					u.Access = framegraph.DepthWrite
					u.Clear = &framegraph.Clear{}
					u.Discard = true
				}
				n.Uses = append(n.Uses, u)
			}
			for _, t := range d.Reads {
				if t == nil || t.destroyed {
					continue
				}
				if t.target != nil && !t.target.destroyed {
					n.Uses = read(n.Uses, f.targets[t.target].read)
				} else if t.scene == r {
					if t.sceneDepth {
						n.Uses = read(n.Uses, f.resolvedDepth)
					} else {
						n.Uses = read(n.Uses, f.hdr)
					}
				}
			}
			n.Uses = slots(n.Uses, d.Target)
			appendNode(n, graphNode{name: d.Name, app: p, byFrame: d.Target != nil, record: p.record, enabled: func(*graphFrame) bool { return p.enabled }, begin: -1, end: -1, resolve: -1})
		}
	}
	// A persistent sampled image may have been written by the preceding
	// submission. Declare that producer even though its final layout is already
	// shader-readable; layout equality alone does not make vertex reads visible.
	var previous []framegraph.Use
	for _, t := range r.appTargets {
		ids := f.targets[t]
		previous = append(previous, framegraph.Use{Resource: ids.write, Access: framegraph.ColorWrite, FinalLayout: core1_0.ImageLayoutShaderReadOnlyOptimal})
		if t.desc.History {
			previous = append(previous, framegraph.Use{Resource: ids.read, Access: framegraph.ColorWrite, FinalLayout: core1_0.ImageLayoutShaderReadOnlyOptimal})
		}
	}
	if len(previous) > 0 {
		appendNode(framegraph.Node{Name: "application previous-frame state", Kind: framegraph.Legacy, Uses: previous}, graphNode{name: "application previous-frame state", begin: -1, end: -1, resolve: -1})
	}
	for i, n := range decl {
		switch i {
		case graphLegacy:
			stage(StageBeforeScene)
			var inputs []framegraph.Use
			for _, t := range r.appTargets {
				inputs = read(inputs, f.targets[t].read)
			}
			if len(inputs) > 0 {
				appendNode(framegraph.Node{Name: "application scene inputs", Kind: framegraph.Transfer, Uses: inputs}, graphNode{name: "application scene inputs", begin: -1, end: -1, resolve: -1})
			}
		case graphCopy:
			stage(StageAfterScene)
		case graphBloom:
			stage(StageBeforeBloom)
		case graphTonemap:
			stage(StageBeforeTonemap)
		}
		f.engine[i] = len(f.nodes)
		if i == graphLegacy || i == graphWater {
			n.Uses = slots(n.Uses, nil)
		}
		appendNode(n, nodes[i])
		if i == graphLegacy && r.depthResolve != nil {
			f.depthNode = len(f.nodes)
			appendNode(framegraph.Node{Name: "scene depth resolve", Kind: framegraph.Graphics, Dependencies: appDependency(), Uses: []framegraph.Use{
				{Resource: f.depth, Access: framegraph.SampledRead}, {Resource: f.resolvedDepth, Access: framegraph.ColorWrite, Discard: true}}},
				graphNode{name: "scene depth resolve", record: r.depthResolve.record, begin: -1, end: -1, resolve: -1})
		}
	}
	// Public Texture() permits sampling through an ordinary RenderObject, outside
	// the pass input list. Declare that resting state even for an unbound target.
	var rest []framegraph.Use
	for _, t := range r.appTargets {
		ids := f.targets[t]
		rest = read(rest, ids.write)
		rest = read(rest, ids.read)
	}
	if r.depthResolve != nil {
		rest = read(rest, f.resolvedDepth)
	}
	if len(rest) > 0 {
		appendNode(framegraph.Node{Name: "application sampled outputs", Kind: framegraph.Legacy, Uses: rest}, graphNode{name: "application sampled outputs", begin: -1, end: -1, resolve: -1})
	}
	return nil
}

func (r *Renderer) replaceAppGraph(deferOld bool) error {
	f, err := r.buildAppGraph()
	if err != nil {
		return err
	}
	old := r.frameGraph
	r.frameGraph = f
	if deferOld {
		if err = r.ensureSceneDepth(); err == nil {
			err = r.bindGraphTargetsMode(false)
		}
		if err != nil {
			f.destroyFramebuffers(r.deviceDriver)
			r.frameGraph = old
			return err
		}
		r.DeferDestroy(func() { old.destroyFramebuffers(r.deviceDriver) })
	}
	f.sizeScratch(&r.cmdScratch)
	if r.gpuTimer != nil {
		r.gpuTimer.apps = r.appPasses
		if r.gpuTimer.appSums == nil {
			r.gpuTimer.appSums = make(map[*AppPass]appTimingSum)
		}
		for _, p := range r.appPasses {
			if p.desc.Timed {
				if _, ok := r.gpuTimer.appSums[p]; !ok {
					r.gpuTimer.appSums[p] = appTimingSum{}
				}
			}
		}
	}
	r.graphDirty = false
	for _, t := range r.retiredTargets {
		r.DeferDestroy(func() { t.color.destroy(r.deviceDriver); t.depth.destroy(r.deviceDriver); t.color, t.depth = nil, nil })
	}
	r.retiredTargets = nil
	return nil
}

func (r *Renderer) bindAppGraphImages() {
	f := r.frameGraph
	for _, t := range r.appTargets {
		ids := f.targets[t]
		f.images[ids.write] = graphImage{images: t.color.images, views: t.color.views, frameInstance: t.desc.History}
		if t.desc.History {
			f.images[ids.read] = graphImage{images: t.color.images, views: t.color.views, frameInstance: true, previous: true}
		}
		if t.depth != nil {
			f.images[ids.depth] = graphImage{images: t.depth.images, views: t.depth.views, frameInstance: t.desc.History}
		}
	}
	if r.depthResolve != nil && r.depthResolve.color != nil {
		f.images[f.resolvedDepth] = graphImage{images: r.depthResolve.color.images, views: r.depthResolve.color.views}
	}
}

func (r *Renderer) releaseAppResizeTargets() {
	for _, p := range r.appPasses {
		p.releaseSets()
	}
	// The device is idle during resize. Detach every light-set slot before
	// retiring views; each waited slot is filled with the replacement on draw.
	if r.fallbackTexture != nil {
		for frame := range maxFramesInFlight {
			for slot := range ShaderTextureSlots {
				_ = r.writeAppSampler(r.shadow.descriptorSets[frame], 7+slot, r.fallbackTexture)
			}
		}
		clear(r.shaderSlotImages[:])
	}

	if r.depthResolve != nil {
		r.depthResolve.releaseTargets()
	}
	for _, t := range r.appTargets {
		if t.desc.Width == 0 {
			t.color.destroy(r.deviceDriver)
			t.depth.destroy(r.deviceDriver)
			t.color, t.depth = nil, nil
		}
	}
}

func (r *Renderer) rebuildAppTargets(undo *rebuildUndo) error {
	for _, p := range r.appPasses {
		if len(p.sets) == 0 {
			var err error
			p.sets, err = r.allocateAppSets(maxFramesInFlight)
			if err != nil {
				return err
			}
			undo.push(p.releaseSets)
		}
	}

	for _, t := range r.appTargets {
		if t.color != nil {
			continue
		}
		if err := r.allocateAppTarget(t); err != nil {
			return fmt.Errorf("recreate application target %q: %w", t.desc.Name, err)
		}
		undo.push(func() { t.color.destroy(r.deviceDriver); t.depth.destroy(r.deviceDriver); t.color, t.depth = nil, nil })
	}
	if r.depthResolve != nil {
		if err := r.ensureSceneDepth(); err != nil {
			return err
		}
		undo.push(func() { r.depthResolve.releaseTargets() })
	}
	// Descriptors that named old relative targets are rebuilt while idle,
	// then refreshed for the acquired image in the ordinary waited-slot path.
	if len(r.appTargets) > 0 || len(r.appPasses) > 0 || r.depthResolve != nil {
		undo.push(func() {
			for _, p := range r.appPasses {
				p.releaseSets()
			}
		})
		if r.fallbackTexture != nil {
			for frame := range maxFramesInFlight {
				if err := r.flushAppFrameBindings(frame, 0); err != nil {
					return err
				}
			}
			for _, t := range r.appTargets {
				t.selectTexture(r.currentFrame)
			}
		}
	}
	return nil
}

func (r *Renderer) destroyAppResources() {
	for _, p := range r.appPasses {
		p.release()
	}
	r.appPasses = nil
	if r.depthResolve != nil {
		r.depthResolve.destroy()
	}
	for _, ts := range [][]*RenderTarget{r.appTargets, r.retiredTargets} {
		for _, t := range ts {
			t.color.destroy(r.deviceDriver)
			t.depth.destroy(r.deviceDriver)
		}
	}
	r.appTargets, r.retiredTargets = nil, nil
	if r.appSetLayout.Handle() != 0 {
		r.deviceDriver.DestroyDescriptorSetLayout(r.appSetLayout, nil)
		r.appSetLayout = core1_0.DescriptorSetLayout{}
	}
}

func (f *frameGraph) destroyFramebuffers(d core1_0.DeviceDriver) {
	for i := range f.nodes {
		n := &f.nodes[i]
		for _, fb := range n.framebuffers {
			d.DestroyFramebuffer(fb, nil)
		}
		n.framebuffers = nil
	}
}
