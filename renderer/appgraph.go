package renderer

import (
	"fmt"

	"github.com/derekmwright/glyphengine/renderer/framegraph"
	"github.com/vkngwrapper/core/v3/core1_0"
)

type appGraphTarget struct{ write, read, depth framegraph.ResourceID }

func (r *Renderer) buildAppGraph() (*frameGraph, error) {
	f, err := newFrameGraph(r.msaaSamples, r.depth.format, r.sc.imageFormat, len(r.sc.imageViews), r)
	if err != nil {
		return nil, err
	}
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
	f.storage = make(map[*StorageBuffer]framegraph.ResourceID)
	for _, b := range r.storageBuffers {
		f.storage[b] = g.AddBuffer(framegraph.BufferDesc{Name: b.desc.Name, Size: b.desc.Size, Persistent: true})
	}
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
	// Ahead of GPU LOD selection, shadows and the scene: every streamed copy
	// is in place before anything in this frame can read it.
	r.appendUploadGraph(f, g, appendNode)
	r.appendGPULODGraph(f, g, appendNode)
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
		for order, p := range r.appPasses {
			if p.desc.Stage != stage {
				continue
			}
			if p.compute != nil {
				r.appendComputeGraph(f, p.compute, order+2, appendNode)
				continue
			}
			d := p.desc
			n := framegraph.Node{Name: d.Name, Kind: framegraph.Graphics, Optional: true, Timed: d.Timed}
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
	var previous, storagePrevious []framegraph.Use
	for _, t := range r.appTargets {
		ids := f.targets[t]
		state := framegraph.Use{Resource: ids.write, Access: framegraph.ColorWrite, FinalLayout: core1_0.ImageLayoutShaderReadOnlyOptimal}
		previous = append(previous, state)
		if t.desc.Storage {
			storage := state
			storage.Access, storage.Stages = framegraph.StorageReadWrite, core1_0.PipelineStageAllCommands
			storagePrevious = append(storagePrevious, storage)
			if t.desc.History {
				storage.Resource = ids.read
				storagePrevious = append(storagePrevious, storage)
			}
		}
		if t.desc.History {
			state.Resource = ids.read
			previous = append(previous, state)
		}
	}
	if len(previous) > 0 {
		appendNode(framegraph.Node{Name: "application previous-frame state", Kind: framegraph.Legacy, Uses: previous}, graphNode{name: "application previous-frame state", begin: -1, end: -1, resolve: -1})
	}
	if len(storagePrevious) > 0 {
		// Join the possible storage producer with the graphics producer and
		// readers from the preceding submission. Both leave sampled layouts.
		appendNode(framegraph.Node{Name: "application previous-frame compute state", Kind: framegraph.Legacy, Optional: true, Uses: storagePrevious}, graphNode{name: "application previous-frame compute state", begin: -1, end: -1, resolve: -1})
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
			appendNode(framegraph.Node{Name: "scene depth resolve", Kind: framegraph.Graphics, Uses: []framegraph.Use{
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
			r.frameGraph = old
			return err
		}
	}
	f.sizeScratch(&r.cmdScratch)
	if r.gpuTimer != nil {
		// The engine's own timed passes ride beside the application's; see
		// engineTimings for the query slots they are budgeted.
		r.gpuTimer.apps = append(append([]*AppPass{}, r.appPasses...), f.uploadTimer)
		if f.lodTimer != nil {
			r.gpuTimer.apps = append(r.gpuTimer.apps, f.lodTimer)
		}
		if r.gpuTimer.appSums == nil {
			r.gpuTimer.appSums = make(map[*AppPass]appTimingSum)
		}
		for _, p := range r.gpuTimer.apps {
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
	for id, b := range f.lodBuffers {
		f.images[id] = b
	}
	for b, id := range f.storage {
		f.images[id] = graphImage{buffers: b.buffers, frameInstance: b.desc.History}
	}
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
			p.sets, err = r.allocatePassSets(p, maxFramesInFlight)
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
	for _, b := range r.storageBuffers {
		b.release()
	}
	r.storageBuffers = nil
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
	if r.computeSetLayout.Handle() != 0 {
		r.deviceDriver.DestroyDescriptorSetLayout(r.computeSetLayout, nil)
		r.computeSetLayout = core1_0.DescriptorSetLayout{}
	}
	if r.appSetLayout.Handle() != 0 {
		r.deviceDriver.DestroyDescriptorSetLayout(r.appSetLayout, nil)
		r.appSetLayout = core1_0.DescriptorSetLayout{}
	}
}
