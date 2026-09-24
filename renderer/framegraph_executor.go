package renderer

import (
	"fmt"

	"github.com/derekmwright/glyphengine/renderer/framegraph"
	"github.com/vkngwrapper/core/v3/core1_0"
)

const (
	graphLegacy      = 0
	graphCopy        = 1
	graphWater       = 2
	graphBloom       = 3
	graphBloomStages = 2*bloomLevels - 1
	graphUILayer     = graphBloom + graphBloomStages
	graphUIGlow      = graphUILayer + 1
	graphTonemap     = graphUIGlow + graphBloomStages
)

type graphImage struct {
	buffers       []core1_0.Buffer
	images        []core1_0.Image
	views         []core1_0.ImageView
	frameInstance bool
	previous      bool
}

type graphNode struct {
	app                 *AppPass
	byFrame             bool
	name                string
	pass                core1_0.RenderPass
	framebuffers        []core1_0.Framebuffer
	extent              core1_0.Extent2D
	clears              []core1_0.ClearValue
	record              func(*graphFrame)
	enabled             func(*graphFrame) bool
	skipped             func(*graphFrame)
	begin, end, resolve Pass // -1 means no edge at this node boundary.
}

// The compiler owns only declarations. Vulkan objects and recording closures
// stay here, with indexed bindings so execution never looks up a cache key.
type frameGraph struct {
	beforeShadows                          int
	lodTimer                               *AppPass
	lodBuffers                             map[framegraph.ResourceID]graphImage
	storage                                map[*StorageBuffer]framegraph.ResourceID
	declarations                           []framegraph.Node
	engine                                 [graphTonemap + 2]int
	targets                                map[*RenderTarget]appGraphTarget
	resolvedDepth                          framegraph.ResourceID
	depthNode                              int
	plan                                   *framegraph.Plan
	nodes                                  []graphNode
	cache                                  map[framegraph.RenderPassKey]core1_0.RenderPass
	images                                 []graphImage
	hdr, depth, copy, color, ui, swapchain framegraph.ResourceID
	bloom, uiBloom                         [bloomLevels]framegraph.ResourceID
	frame                                  graphFrame
}

// Retained on frameGraph: passing a freshly allocated context through a draw
// closure would make the otherwise allocation-free recorder escape each frame.
type graphFrame struct {
	driver                                                  core1_0.DeviceDriver
	cmd                                                     core1_0.CommandBuffer
	imageIndex, frame                                       int
	extent                                                  core1_0.Extent2D
	scratch                                                 *commandScratch
	timer                                                   *gpuTimer
	stats                                                   *RenderStats
	sceneColor                                              *sceneColorTarget
	sceneImage                                              core1_0.Image
	waterPipeline, godRayPipeline, uiPipeline, msdfPipeline core1_0.Pipeline
	pipelineLayout, litPipelineLayout                       core1_0.PipelineLayout
	shadowDS                                                core1_0.DescriptorSet
	draws, msdfOverlays                                     []RenderObject
	uiOverlays                                              []UIRenderObject
	lighting                                                SceneLighting
	ow                                                      overWater
	bloom                                                   bloomPass
	tonemap                                                 tonemapPass
	water, ui                                               bool
}

func newFrameGraph(samples core1_0.SampleCountFlags, depthFormat, swapchainFormat core1_0.Format, instances int, owner ...*Renderer) (*frameGraph, error) {
	f := &frameGraph{cache: make(map[framegraph.RenderPassKey]core1_0.RenderPass)}
	g := framegraph.New()
	image := func(name string, scale float32) framegraph.ImageDesc {
		return framegraph.ImageDesc{Name: name, Format: hdrFormat, Extent: framegraph.Extent{Scale: scale},
			Samples: core1_0.Samples1, Aspect: core1_0.ImageAspectColor, Instances: instances}
	}
	d := image("HDR", 1)
	f.hdr = g.AddImage(d)
	d = image("depth", 1)
	d.Format, d.Aspect, d.Samples = depthFormat, core1_0.ImageAspectDepth, samples
	f.depth = g.AddImage(d)
	d = image("scene colour", 1)
	d.Persistent, d.Instances = true, 1
	f.copy = g.AddImage(d)
	f.color = f.hdr
	msaa := samples != core1_0.Samples1
	if msaa {
		d = image("MSAA colour", 1)
		d.Samples = samples
		f.color = g.AddImage(d)
	}
	legacy := []framegraph.Use{{Resource: f.color, Access: framegraph.ColorWrite, HasResolve: msaa, ResolveTo: f.hdr},
		{Resource: f.depth, Access: framegraph.DepthWrite}}
	for _, name := range []string{"sun shadow maps", "point shadow maps"} {
		d = image(name, 1)
		d.Format, d.Aspect = depthFormat, core1_0.ImageAspectDepth
		d.Imported, d.InitialLayout = true, core1_0.ImageLayoutShaderReadOnlyOptimal
		legacy = append(legacy, framegraph.Use{Resource: g.AddImage(d), Access: framegraph.SampledRead})
	}
	add := func(n framegraph.Node, record func(*graphFrame)) int {
		id := len(f.nodes)
		f.declarations = append(f.declarations, n)
		f.nodes = append(f.nodes, graphNode{name: n.Name, record: record, begin: -1, end: -1, resolve: -1})
		return int(id)
	}
	add(framegraph.Node{Name: "legacy scene", Kind: framegraph.Legacy, Uses: legacy}, nil)
	// Water needs the finished scene as a texture, so the tail begins by copying
	// it. The copy is the only way a fragment shader can read what is already on
	// screen: the alternative, an input attachment, can only read the pixel being
	// written, and refraction is precisely a read of a *different* pixel. Copying
	// the HDR image rather than the swapchain is what keeps refraction working --
	// nothing has been tonemapped into the swapchain at this point in the frame.
	//
	// The LightShafts arm of the predicate is why a shafts-and-no-water frame
	// enters the group too: godray.frag samples the same copy the water refracts
	// through. LightShafts arrives already faded (see SceneLighting), so a frame
	// that could not draw a shaft pixel reads zero here and pays for neither.
	add(framegraph.Node{Name: "scene colour copy", Kind: framegraph.Transfer, OptionalGroup: 1, Timed: true,
		Uses: []framegraph.Use{{Resource: f.hdr, Access: framegraph.TransferSrc}, {Resource: f.copy, Access: framegraph.TransferDst, Discard: true}}},
		func(c *graphFrame) {
			layers := core1_0.ImageSubresourceLayers{AspectMask: core1_0.ImageAspectColor, LayerCount: 1}
			c.scratch.copyImage(c.driver, c.cmd, c.sceneImage, core1_0.ImageLayoutTransferSrcOptimal,
				c.sceneColor.image, core1_0.ImageLayoutTransferDstOptimal, core1_0.ImageCopy{
					SrcSubresource: layers, DstSubresource: layers,
					Extent: core1_0.Extent3D{Width: c.extent.Width, Height: c.extent.Height, Depth: 1}})
		})
	f.nodes[graphCopy].begin = PassWater
	f.nodes[graphCopy].enabled = func(c *graphFrame) bool { return c.water }
	order := []framegraph.ResourceID{f.color, f.depth}
	if msaa {
		order = append(order, f.hdr)
	}
	// No barrier puts the HDR image back after the copy. With MSAA the water
	// pass declares its resolve target Undefined and rewrites it wholesale;
	// without MSAA it loads from TransferSrc, matching the copy. Either way the
	// render pass performs the transition, which is the compiler's rule for
	// attachments and also the only legal option: a barrier to Undefined is not
	// a transition Vulkan accepts. Every attachment loads rather than clears
	// because the opaque scene and its depth are already there and must
	// survive -- water composites over one and is occluded by the other.
	//
	// The blended draws after the surface are issue #45. Water writes no depth,
	// so they depth-test against the opaque scene exactly as they did in the
	// scene pass and composite over the surface rather than under it; blendSplit
	// decides which draws come here and which stay before the copy (waterorder.go).
	add(framegraph.Node{Name: "water", Kind: framegraph.Graphics, OptionalGroup: 1, Timed: true,
		Dependencies: sceneEntryDependency(), AttachmentOrder: order,
		Uses: []framegraph.Use{{Resource: f.color, Access: framegraph.ColorLoadWrite, HasResolve: msaa, ResolveTo: f.hdr},
			{Resource: f.depth, Access: framegraph.DepthLoadWrite}, {Resource: f.copy, Access: framegraph.SampledRead}}},
		func(c *graphFrame) {
			recordWaterDraws(c.driver, c.stats, c.cmd, c.waterPipeline, c.godRayPipeline,
				c.pipelineLayout, c.litPipelineLayout, c.extent, c.draws, c.lighting, c.sceneColor,
				c.shadowDS, c.ow, c.timer, c.scratch)
		})
	f.nodes[graphWater].enabled = f.nodes[graphCopy].enabled
	// The water pass resolves its MSAA colour on the way out, the same as the
	// scene pass, and gets the same treatment: a bracket of its own around
	// CmdEndRenderPass. It was briefly charged to PassOverWater instead, on the
	// argument that otherwise 0.021 ms belonged to nobody -- true, and the wrong
	// cure, because it made "overwater" read 0.021 ms on a lake with nothing in
	// front of it. PassWater + PassOverWater + PassWaterResolve is what PassWater
	// alone used to be.
	f.nodes[graphWater].resolve = PassWaterResolve
	f.nodes[graphWater].skipped = func(c *graphFrame) {
		c.timer.end(c.driver, c.cmd, c.frame, PassWater)
		for _, p := range [...]Pass{PassShafts, PassOverWater} {
			c.timer.begin(c.driver, c.cmd, c.frame, p)
			c.timer.end(c.driver, c.cmd, c.frame, p)
		}
	}
	// A bloom chain is one node per stage: the prefilter and each downsample
	// overwrite their level and so may discard it, while each upsample adds
	// into a level the downsample already wrote and so must load it -- which is
	// why an upsample's target arrives in ShaderReadOnly, having been a sampled
	// source a moment ago, rather than undefined. The derived dependency pair
	// orders the attachment accesses and exposes the final layout transition
	// to the next sampler. The shared cloud/downsample pass lost 60 RAW reports
	// over 60 fixed 02-cube frames when its exit dependency was added; the old
	// incoming dependency alone did not make that transition visible.
	chain := func(name string, source framegraph.ResourceID, ids *[bloomLevels]framegraph.ResourceID, ui bool, pass Pass) {
		first := len(f.nodes)
		for level := range bloomLevels {
			d := image(fmt.Sprintf("%s %d", name, level), 1/float32(uint32(1)<<uint(level+1)))
			d.Persistent = true
			ids[level] = g.AddImage(d)
		}
		stage := func(level int, up bool, src, dst framegraph.ResourceID) {
			access := framegraph.ColorWrite
			if up {
				access = framegraph.ColorLoadWrite
			}
			id := add(framegraph.Node{Name: fmt.Sprintf("%s %s %d", name, map[bool]string{false: "down", true: "up"}[up], level),
				Kind: framegraph.Graphics, Optional: true, Timed: true,
				Uses: []framegraph.Use{{Resource: src, Access: framegraph.SampledRead}, {Resource: dst, Access: access, Discard: !up}}},
				func(c *graphFrame) {
					b := c.bloom
					if ui {
						b = c.tonemap.ui.bloom
					}
					recordBloomStage(c.driver, c.cmd, b, level, up, c.scratch)
				})
			f.nodes[id].enabled = func(c *graphFrame) bool {
				if ui {
					return c.ui && c.tonemap.ui.bloom.enabled
				}
				return c.bloom.enabled
			}
		}
		for level := range bloomLevels {
			src := source
			if level > 0 {
				src = ids[level-1]
			}
			stage(level, false, src, ids[level])
		}
		for level := bloomLevels - 2; level >= 0; level-- {
			stage(level, true, ids[level+1], ids[level])
		}
		f.nodes[first].begin, f.nodes[len(f.nodes)-1].end = pass, pass
	}
	chain("bloom", f.hdr, &f.bloom, false, PassBloom)
	d = image("UI layer", 1)
	d.Persistent = true
	f.ui = g.AddImage(d)
	// LoadOp Clear rather than the bloom stages' DontCare, and the clear value
	// is (0,0,0,0) rather than anything opaque. That is the whole premultiplied
	// alpha contract in one attachment: the layer starts as "no coverage
	// anywhere", the UI accumulates premultiplied "over" into it, and whatever
	// is still at alpha zero composites as the scene showing through untouched.
	// A DontCare load would leave the previous frame's HUD under this one, and
	// an opaque clear would paint a black rectangle over the whole scene.
	//
	// The compiler includes the previous composite/prefilter reads in the
	// incoming dependency and exposes this pass's writes on exit. 13-ui -glow on,
	// 60 fixed frames: the old incoming-only override produced 60 RAW reports;
	// deriving both dependencies produces none. The draw closure is
	// recordUIComposite with a different destination and premultiplied output, the same
	// function as the swapchain path so panels, nine-slice fills, texture mode
	// and text keep behaving identically whichever one is on.
	add(framegraph.Node{Name: "UI layer", Kind: framegraph.Graphics, Optional: true, Timed: true,
		Uses: []framegraph.Use{{Resource: f.ui, Access: framegraph.ColorWrite, Clear: &framegraph.Clear{}, Discard: true}}},
		func(c *graphFrame) {
			p := c.tonemap.ui
			recordUIComposite(c.driver, c.stats, c.cmd, p.uiPipeline, p.msdfPipeline, p.layout,
				c.extent, c.uiOverlays, c.msdfOverlays, c.ow.fallback, true, c.scratch)
		})
	f.nodes[graphUILayer].begin, f.nodes[graphUILayer].end = PassUILayer, PassUILayer
	f.nodes[graphUILayer].enabled = func(c *graphFrame) bool { return c.ui }
	chain("UI glow", f.ui, &f.uiBloom, true, PassUIGlow)
	d = image("swapchain", 1)
	d.Format, d.Imported = swapchainFormat, true
	f.swapchain = g.AddImage(d)
	// Single sample and no depth: MSAA was already resolved into the HDR
	// target, and a fullscreen triangle has nothing to depth-test against.
	// The swapchain attachment discards because every pixel is written.
	// 02-cube/60 fixed frames: the former sampling-only entry produced 60 WAW
	// reports at the DONT_CARE load; derived ColorAttachmentWrite access removes them.
	//
	// Screen-space UI is composited inside this pass, after the resolve, in
	// its own timer interval adjacent to the tonemap's. With the glow layer on,
	// the composite REPLACES the direct UI draws with one fullscreen triangle of
	// the finished layer rather than stacking on them: drawing both would put
	// the UI on screen twice, which reads as the HUD having gained contrast
	// rather than as a double draw.
	add(framegraph.Node{Name: "tonemap and composite", Kind: framegraph.Graphics, Timed: true,
		Uses: []framegraph.Use{{Resource: f.hdr, Access: framegraph.SampledRead}, {Resource: f.bloom[0], Access: framegraph.SampledRead},
			{Resource: f.ui, Access: framegraph.SampledRead}, {Resource: f.uiBloom[0], Access: framegraph.SampledRead},
			{Resource: f.swapchain, Access: framegraph.ColorWrite, Discard: true}}},
		func(c *graphFrame) {
			recordTonemapDraw(c.driver, c.cmd, c.tonemap, c.tonemap.layout, c.extent, c.scratch)
			c.timer.end(c.driver, c.cmd, c.frame, PassTonemap)
			c.timer.begin(c.driver, c.cmd, c.frame, PassComposite)
			if c.ui {
				recordUIResolve(c.driver, c.cmd, c.tonemap.ui, c.extent, c.scratch)
			} else {
				recordUIComposite(c.driver, c.stats, c.cmd, c.uiPipeline, c.msdfPipeline, c.pipelineLayout,
					c.extent, c.uiOverlays, c.msdfOverlays, c.ow.fallback, false, c.scratch)
			}
			c.timer.end(c.driver, c.cmd, c.frame, PassComposite)
		})
	f.nodes[graphTonemap].begin = PassTonemap
	add(framegraph.Node{Name: "present", Kind: framegraph.Legacy, Uses: []framegraph.Use{{Resource: f.swapchain, Access: framegraph.Present}}}, nil)
	for i := range f.engine {
		f.engine[i] = i
	}
	f.depthNode = -1
	if len(owner) > 0 {
		if err := owner[0].extendAppGraph(f, g); err != nil {
			return nil, err
		}
	}
	for _, n := range f.declarations {
		g.AddNode(n)
	}
	var err error
	f.plan, err = g.Build()
	if err != nil {
		return nil, err
	}
	f.images = make([]graphImage, len(f.plan.Resources))
	for i, step := range f.plan.Steps {
		if step.RenderPass == nil {
			continue
		}
		// Only attachments that actually clear need clear values in the command.
		// Bloom, water and tonemap historically send none.
		for j, a := range step.RenderPass.Attachments {
			if a.LoadOp == core1_0.AttachmentLoadOpClear {
				for len(f.nodes[i].clears) <= j {
					f.nodes[i].clears = append(f.nodes[i].clears, core1_0.ClearValueFloat{})
				}
				if a.SubpassLayout == core1_0.ImageLayoutDepthStencilAttachmentOptimal {
					f.nodes[i].clears[j] = core1_0.ClearValueDepthStencil{Depth: step.RenderPass.Clears[j].Depth, Stencil: step.RenderPass.Clears[j].Stencil}
				} else {
					f.nodes[i].clears[j] = core1_0.ClearValueFloat(step.RenderPass.Clears[j].Color)
				}
			}
		}
	}
	return f, nil
}

func (f *frameGraph) renderPass(d core1_0.DeviceDriver, node int) (core1_0.RenderPass, error) {
	desc := f.plan.Steps[node].RenderPass
	key := desc.Key()
	if pass, ok := f.cache[key]; ok {
		f.nodes[node].pass = pass
		return pass, nil
	}
	info := graphRenderPassInfo(desc)
	pass, _, err := d.CreateRenderPass(nil, info)
	if err != nil {
		return core1_0.RenderPass{}, fmt.Errorf("frame graph render pass %s: %w", f.nodes[node].name, err)
	}
	f.cache[key], f.nodes[node].pass = pass, pass
	return pass, nil
}

func graphRenderPassInfo(desc *framegraph.RenderPassDesc) core1_0.RenderPassCreateInfo {
	info := core1_0.RenderPassCreateInfo{SubpassDependencies: desc.Dependencies}
	for _, a := range desc.Attachments {
		info.Attachments = append(info.Attachments, core1_0.AttachmentDescription{
			Format: a.Format, Samples: a.Samples, LoadOp: a.LoadOp, StoreOp: a.StoreOp,
			StencilLoadOp: a.StencilLoadOp, StencilStoreOp: a.StencilStoreOp,
			InitialLayout: a.InitialLayout, FinalLayout: a.FinalLayout})
	}
	sub := core1_0.SubpassDescription{PipelineBindPoint: core1_0.PipelineBindPointGraphics}
	ref := func(i int) core1_0.AttachmentReference {
		if i < 0 {
			return core1_0.AttachmentReference{Attachment: -1}
		}
		return core1_0.AttachmentReference{Attachment: i, Layout: desc.Attachments[i].SubpassLayout}
	}
	resolve := false
	for _, i := range desc.Resolve {
		resolve = resolve || i >= 0
	}
	for i, color := range desc.Color {
		sub.ColorAttachments = append(sub.ColorAttachments, ref(color))
		if resolve {
			sub.ResolveAttachments = append(sub.ResolveAttachments, ref(desc.Resolve[i]))
		}
	}
	if desc.Depth >= 0 {
		depth := ref(desc.Depth)
		sub.DepthStencilAttachment = &depth
	}
	info.Subpasses = []core1_0.SubpassDescription{sub}
	return info
}

func (f *frameGraph) destroyPasses(d core1_0.DeviceDriver) {
	for key, pass := range f.cache {
		d.DestroyRenderPass(pass, nil)
		delete(f.cache, key)
	}
}

func (f *frameGraph) sizeScratch(s *commandScratch) {
	width := 0
	scan := func(bs []framegraph.Barrier) {
		for i := 0; i < len(bs); {
			j := i + 1
			for j < len(bs) && bs[j].SrcStage == bs[i].SrcStage && bs[j].DstStage == bs[i].DstStage {
				j++
			}
			width = max(width, j-i)
			i = j
		}
	}
	for _, step := range f.plan.Steps {
		scan(step.Barriers)
	}
	scan(f.plan.FinalBarriers)
	s.barriers = make([]core1_0.ImageMemoryBarrier, width)
	s.bufferBarriers = make([]core1_0.BufferMemoryBarrier, width)
}

func (f *frameGraph) barriers(c *graphFrame, bs []framegraph.Barrier) error {
	for i := 0; i < len(bs); {
		j := i
		imageCount, bufferCount := 0, 0
		for j < len(bs) && bs[j].SrcStage == bs[i].SrcStage && bs[j].DstStage == bs[i].DstStage {
			b := bs[j]
			if b.Buffer {
				binding := f.images[b.Resource]
				instance := 0
				if binding.frameInstance {
					instance = c.frame % len(binding.buffers)
				}
				c.scratch.bufferBarriers[bufferCount] = core1_0.BufferMemoryBarrier{
					Buffer: binding.buffers[instance], Offset: b.Offset, Size: b.Size,
					SrcQueueFamilyIndex: -1, DstQueueFamilyIndex: -1, SrcAccessMask: b.SrcAccess, DstAccessMask: b.DstAccess}
				bufferCount++
				j++
				continue
			}
			images := f.images[b.Resource].images
			instance := c.imageIndex
			if f.images[b.Resource].frameInstance {
				instance = c.frame % 2
				if f.images[b.Resource].previous {
					instance = 1 - instance
				}
			}
			if len(images) == 1 {
				instance = 0
			}
			desc := f.plan.Resources[b.Resource].Desc
			c.scratch.barriers[imageCount] = core1_0.ImageMemoryBarrier{OldLayout: b.OldLayout, NewLayout: b.NewLayout,
				SrcQueueFamilyIndex: -1, DstQueueFamilyIndex: -1, Image: images[instance],
				SubresourceRange: core1_0.ImageSubresourceRange{AspectMask: desc.Aspect, LevelCount: 1, LayerCount: int(desc.Layers)},
				SrcAccessMask:    b.SrcAccess, DstAccessMask: b.DstAccess}
			imageCount++
			j++
		}
		var buffers []core1_0.BufferMemoryBarrier
		if bufferCount > 0 {
			buffers = c.scratch.bufferBarriers[:bufferCount]
		}
		if err := c.driver.CmdPipelineBarrier(c.cmd, bs[i].SrcStage, bs[i].DstStage, 0, nil, buffers, c.scratch.barriers[:imageCount]); err != nil {
			return err
		}
		i = j
	}
	return nil
}

func (f *frameGraph) executeGraph() error {
	if err := f.executeSteps(f.engine[graphLegacy]+1, len(f.plan.Steps)); err != nil {
		return err
	}
	return f.barriers(&f.frame, f.plan.FinalBarriers)
}
func (f *frameGraph) executeSteps(first, last int) error {
	c := &f.frame
	for _, step := range f.plan.Steps[first:last] {
		n := &f.nodes[step.Node]
		if n.begin >= 0 {
			c.timer.begin(c.driver, c.cmd, c.frame, n.begin)
		}
		if n.app != nil && n.app.desc.Timed {
			c.timer.beginApp(c.driver, c.cmd, c.frame, n.app)
		}
		run := n.enabled == nil || n.enabled(c)
		if run {
			if err := f.barriers(c, step.Barriers); err != nil {
				return err
			}
			if step.RenderPass != nil {
				if err := c.scratch.beginRenderPass(c.driver, c.cmd, core1_0.SubpassContentsInline, n.pass,
					n.framebuffer(c), core1_0.Rect2D{Extent: n.extent}, n.clears...); err != nil {
					return err
				}
			}
			if n.record != nil {
				n.record(c)
			}
		} else if n.skipped != nil {
			n.skipped(c)
		}
		if n.resolve >= 0 {
			c.timer.begin(c.driver, c.cmd, c.frame, n.resolve)
		}
		if run && step.RenderPass != nil {
			c.driver.CmdEndRenderPass(c.cmd)
		}
		if n.resolve >= 0 {
			c.timer.end(c.driver, c.cmd, c.frame, n.resolve)
		}
		if n.app != nil && n.app.desc.Timed {
			c.timer.endApp(c.driver, c.cmd, c.frame, n.app)
		}
		if n.end >= 0 {
			c.timer.end(c.driver, c.cmd, c.frame, n.end)
		}
	}
	return nil
}

func (n *graphNode) framebuffer(c *graphFrame) core1_0.Framebuffer {
	i := c.imageIndex
	if n.byFrame {
		i = c.frame % len(n.framebuffers)
	}
	return n.framebuffers[i]
}
