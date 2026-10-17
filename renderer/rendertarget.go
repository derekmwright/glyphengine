package renderer

import (
	"fmt"
	"math"
	"math/bits"
	"slices"

	"github.com/derekmwright/glyphengine/renderer/framegraph"
	"github.com/vkngwrapper/core/v3/core1_0"
)

// TargetFormat selects a floating-point colour attachment.
type TargetFormat int

const (
	TargetR16F TargetFormat = iota + 1
	TargetRG16F
	TargetRGBA16F
	TargetR32F
	TargetRGBA32F
)

// TargetFilter selects the minification and magnification filter.
type TargetFilter int

const (
	FilterNearest TargetFilter = iota
	FilterLinear
)

// TargetWrap selects addressing on the target's U and V axes.
type TargetWrap int

const (
	WrapClampToEdge TargetWrap = iota
	WrapRepeat
)

// RenderTargetDesc specifies either a fixed extent or a positive swapchain scale.
type RenderTargetDesc struct {
	Name          string
	Format        TargetFormat
	Filter        TargetFilter // min/mag; defaults to nearest
	Wrap          TargetWrap   // U/V addressing; defaults to clamp-to-edge
	Scale         float32
	Width, Height uint32
	Depth         bool
	History       bool
	Storage       bool // allow compute storage-image writes
	// MipLevels is the length of the target's mip chain. Zero or one is a
	// single level, the default and what every target cost before chains
	// existed. A longer chain is validated against the extent at creation: at
	// most one level per halving of the longer side, down to 1x1. A
	// swapchain-relative target whose extent shrinks on resize keeps as many
	// levels as the new extent has, and Levels reports the count it got.
	MipLevels int
}

// LevelRange selects the mip levels [Base, Base+Count) of a render target.
// The zero value is the whole chain.
type LevelRange struct{ Base, Count int }

// RenderTarget is renderer-owned. Its texture pointer survives resize and swaps
// its underlying readable instance at frame boundaries. Use only on the renderer thread.
type RenderTarget struct {
	r         *Renderer
	desc      RenderTargetDesc
	texture   Texture
	color     *appImages
	depth     *appImages
	id        uint32
	destroyed bool
	// instance is the colour image texture currently shows, so a ranged read
	// binds the same frame's copy the whole-chain view would have.
	instance int
	// ranges are the level ranges passes read through views of their own.
	// The target owns those views, rebuilds them with its images, and
	// releases them with its images, so a pass holds no view that can outlive
	// the image under it.
	ranges []LevelRange
}

func (t *RenderTarget) Texture() *Texture { return &t.texture }
func (t *RenderTarget) Extent() (w, h uint32) {
	if t.color != nil {
		return uint32(t.color.extent.Width), uint32(t.color.extent.Height)
	}
	return 0, 0
}

// Levels is the number of mip levels the target's images have now: one for
// a target that asked for none, and for a swapchain-relative chain possibly
// fewer than it asked for after a resize to a smaller extent.
func (t *RenderTarget) Levels() int {
	if t.color == nil {
		return 0
	}
	return max(t.color.levels, 1)
}

// LevelExtent is level l's extent: the base extent halved l times, each side
// floored and held at one. A dispatch over a level sizes its workgroups from
// this rather than from Extent, which is level 0's. A level the target does
// not have reports 0, 0.
func (t *RenderTarget) LevelExtent(l int) (w, h uint32) {
	if t.color == nil || l < 0 || l >= t.color.levels {
		return 0, 0
	}
	return levelExtent(t.color.extent, l)
}

func levelExtent(e core1_0.Extent2D, l int) (w, h uint32) {
	return uint32(max(e.Width>>l, 1)), uint32(max(e.Height>>l, 1))
}

// fullChain is the number of levels a chain over w x h has, down to 1x1.
func fullChain(w, h int) int { return bits.Len(uint(max(w, h, 1))) }

func targetFormat(f TargetFormat) (core1_0.Format, error) {
	switch f {
	case TargetR16F:
		return core1_0.FormatR16SignedFloat, nil
	case TargetRG16F:
		return core1_0.FormatR16G16SignedFloat, nil
	case TargetRGBA16F:
		return hdrFormat, nil
	case TargetR32F:
		return core1_0.FormatR32SignedFloat, nil
	case TargetRGBA32F:
		return core1_0.FormatR32G32B32A32SignedFloat, nil
	default:
		return 0, fmt.Errorf("Format: unknown target format %d", f)
	}
}

func (d RenderTargetDesc) extent() framegraph.Extent {
	return framegraph.Extent{Scale: d.Scale, Fixed: [2]uint32{d.Width, d.Height}}
}

func validateTarget(d RenderTargetDesc) error {
	if _, err := targetFormat(d.Format); err != nil {
		return err
	}
	if d.Filter != FilterNearest && d.Filter != FilterLinear {
		return fmt.Errorf("Filter: unknown target filter %d", d.Filter)
	}
	if d.Wrap != WrapClampToEdge && d.Wrap != WrapRepeat {
		return fmt.Errorf("Wrap: unknown target wrap %d", d.Wrap)
	}
	if d.Width != 0 || d.Height != 0 {
		if d.Width == 0 || d.Height == 0 {
			return fmt.Errorf("Width/Height: both fixed dimensions must be nonzero")
		}
	} else if d.Scale <= 0 || math.IsNaN(float64(d.Scale)) || math.IsInf(float64(d.Scale), 0) {
		return fmt.Errorf("Scale: extent must be positive and finite")
	}
	if d.MipLevels < 0 {
		return fmt.Errorf("MipLevels: %d is negative; zero or one is a single level", d.MipLevels)
	}
	return nil
}

func (r *Renderer) CreateRenderTarget(d RenderTargetDesc) (*RenderTarget, error) {
	if err := validateTarget(d); err != nil {
		return nil, fmt.Errorf("render target %q: %w", d.Name, err)
	}
	if d.Storage || d.Filter == FilterLinear {
		format, _ := targetFormat(d.Format)
		props := r.instanceDriver.GetPhysicalDeviceFormatProperties(r.physicalDevice, format)
		if d.Filter == FilterLinear && props.OptimalTilingFeatures&core1_0.FormatFeatureSampledImageFilterLinear == 0 {
			return nil, fmt.Errorf("render target %q: Filter: format %v does not support linear sampling", d.Name, format)
		}
		if d.Storage && props.OptimalTilingFeatures&core1_0.FormatFeatureStorageImage == 0 {
			return nil, fmt.Errorf("render target %q: format %v does not support storage images", d.Name, format)
		}
	}
	if d.MipLevels > 1 {
		size := d.extent().Size(uint32(r.sc.extent.Width), uint32(r.sc.extent.Height))
		if n := fullChain(int(size[0]), int(size[1])); d.MipLevels > n {
			return nil, fmt.Errorf("render target %q: MipLevels: %d levels exceed the %d a %dx%d chain has", d.Name, d.MipLevels, n, size[0], size[1])
		}
	}
	t := &RenderTarget{r: r, desc: d, id: newResourceID()}
	if err := r.allocateAppTarget(t); err != nil {
		return nil, fmt.Errorf("render target %q: %w", d.Name, err)
	}
	r.appTargets = append(r.appTargets, t)
	r.graphDirty = true
	t.selectTexture(r.currentFrame)
	return t, nil
}

func (t *RenderTarget) selectTexture(frame int) {
	if t.color == nil {
		return
	}
	i := 0
	if t.desc.History {
		i = 1 - frame%2
	}
	t.instance = i
	t.texture = t.color.textures[i]
	t.texture.target = t
	t.texture.id = t.id
}

// DestroyRenderTarget detaches references immediately and retires GPU objects
// after frames in flight. Passes writing the target are destroyed with it;
// readers and slots subsequently see the fallback texture.
func (r *Renderer) DestroyRenderTarget(t *RenderTarget) {
	if t == nil || t.r != r || t.destroyed {
		return
	}
	t.destroyed = true
	t.texture.destroyed = true
	for _, p := range slices.Clone(r.appPasses) {
		if p.desc.Target == t || (p.compute != nil && slices.Contains(p.compute.desc.Writes, t)) {
			r.DestroyAppPass(p)
		}
	}
	r.appTargets = slices.DeleteFunc(r.appTargets, func(x *RenderTarget) bool { return x == t })
	r.graphDirty = true
	// Rebuild Go-side graph bindings before retiring the target views used by
	// preceding submissions. The existing deferred queue owns GPU lifetime.
	r.retiredTargets = append(r.retiredTargets, t)
}

func (r *Renderer) allocateAppTarget(t *RenderTarget) error {
	size := t.desc.extent().Size(uint32(r.sc.extent.Width), uint32(r.sc.extent.Height))
	e := core1_0.Extent2D{Width: int(size[0]), Height: int(size[1])}
	n := 1
	if t.desc.History {
		n = 2
	}
	f, _ := targetFormat(t.desc.Format)
	// A swapchain-relative chain keeps the levels the new extent has room
	// for. A pass that selects a level the shorter chain lacks is refused when
	// the rebuild validates it; see validateAppLevels.
	levels := min(max(t.desc.MipLevels, 1), fullChain(e.Width, e.Height))
	var err error
	t.color, err = r.newAppImages(f, core1_0.ImageAspectColor, e, n, appImageOptions{
		sampled: true, storage: t.desc.Storage, filter: t.desc.Filter, wrap: t.desc.Wrap, levels: levels,
	})
	if err != nil {
		return err
	}
	for _, rg := range t.ranges {
		if rg.Base+rg.Count > levels {
			// No view: the rebuild refuses the pass that names it.
			t.color.rangeViews = append(t.color.rangeViews, nil)
			continue
		}
		if err = r.addRangeViews(t, rg); err != nil {
			t.color.destroy(r.deviceDriver)
			t.color = nil
			return err
		}
	}
	if t.desc.Depth {
		t.depth, err = r.newAppImages(r.depth.format, core1_0.ImageAspectDepth, e, n, appImageOptions{})
		if err != nil {
			t.color.destroy(r.deviceDriver)
			t.color = nil
			return err
		}
	}
	t.selectTexture(r.currentFrame)
	return nil
}

// appImages owns sets before views before images before memory, including on
// partially completed construction. It is also used by the resolved scene depth.
type appImages struct {
	images   []core1_0.Image
	views    []core1_0.ImageView
	memory   []core1_0.DeviceMemory
	sets     []core1_0.DescriptorSet
	textures []Texture
	sampler  core1_0.Sampler
	extent   core1_0.Extent2D
	// levels is the chain length, one for every image that is not a chain.
	// The fields below are nil for a single-level image, which therefore
	// creates exactly the objects it did before chains existed: views[i] is
	// its attachment, storage and sampled view all at once.
	levels int
	// chain[i] spans every level of image i, for sampling with hardware LOD.
	chain []core1_0.ImageView
	// levelViews[i][l] is image i's level-l storage view. Level 0 is views[i],
	// which already is that view, so it is not created or destroyed twice.
	levelViews [][]core1_0.ImageView
	// rangeViews[k][i] is image i's view of the owning target's ranges[k],
	// nil where a rebuilt chain no longer has those levels.
	rangeViews [][]core1_0.ImageView
}

// levelView is image i's storage view of level l.
func (t *appImages) levelView(i, l int) core1_0.ImageView {
	if l == 0 {
		return t.views[i]
	}
	return t.levelViews[i][l]
}

type appImageOptions struct {
	sampled, storage bool
	filter           TargetFilter
	wrap             TargetWrap
	levels           int
}

func (o appImageOptions) samplerInfo() core1_0.SamplerCreateInfo {
	filter := core1_0.FilterNearest
	if o.filter == FilterLinear {
		filter = core1_0.FilterLinear
	}
	wrap := core1_0.SamplerAddressModeClampToEdge
	if o.wrap == WrapRepeat {
		wrap = core1_0.SamplerAddressModeRepeat
	}
	info := core1_0.SamplerCreateInfo{MagFilter: filter, MinFilter: filter,
		AddressModeU: wrap, AddressModeV: wrap, AddressModeW: core1_0.SamplerAddressModeClampToEdge}
	if o.levels > 1 {
		// A single-level target keeps MaxLod 0 and the sampler it always had.
		// A chain opens the LOD range to every level, and blends between two
		// levels exactly when the target already filters linearly within one,
		// so Filter means the same thing on both axes of the chain.
		info.MaxLod = float32(o.levels)
		if o.filter == FilterLinear {
			info.MipmapMode = core1_0.SamplerMipmapModeLinear
		}
	}
	return info
}

func (r *Renderer) newAppImages(format core1_0.Format, aspect core1_0.ImageAspectFlags, extent core1_0.Extent2D, count int, opts appImageOptions) (_ *appImages, err error) {
	levels := max(opts.levels, 1)
	t := &appImages{extent: extent, levels: levels}
	defer func() {
		if err != nil {
			t.destroy(r.deviceDriver)
		}
	}()
	usage := core1_0.ImageUsageDepthStencilAttachment | core1_0.ImageUsageTransferDst
	if aspect == core1_0.ImageAspectColor {
		usage = core1_0.ImageUsageColorAttachment | core1_0.ImageUsageTransferDst
	}
	if opts.sampled {
		usage |= core1_0.ImageUsageSampled
	}
	if opts.storage {
		usage |= core1_0.ImageUsageStorage
	}
	for range count {
		var img core1_0.Image
		img, _, err = r.deviceDriver.CreateImage(nil, core1_0.ImageCreateInfo{ImageType: core1_0.ImageType2D, Format: format,
			Extent: core1_0.Extent3D{Width: extent.Width, Height: extent.Height, Depth: 1}, MipLevels: levels, ArrayLayers: 1,
			Samples: core1_0.Samples1, Tiling: core1_0.ImageTilingOptimal, Usage: usage, SharingMode: core1_0.SharingModeExclusive})
		if err != nil {
			return nil, err
		}
		t.images = append(t.images, img)
		req := r.deviceDriver.GetImageMemoryRequirements(img)
		mt, e := findMemoryType(r.instanceDriver, r.physicalDevice, req.MemoryTypeBits, core1_0.MemoryPropertyDeviceLocal)
		if e != nil {
			return nil, e
		}
		mem, _, e := r.deviceDriver.AllocateMemory(nil, core1_0.MemoryAllocateInfo{AllocationSize: req.Size, MemoryTypeIndex: mt})
		if e != nil {
			return nil, e
		}
		t.memory = append(t.memory, mem)
		if _, err = r.deviceDriver.BindImageMemory(img, mem, 0); err != nil {
			return nil, err
		}
		view, _, e := r.deviceDriver.CreateImageView(nil, core1_0.ImageViewCreateInfo{Image: img, ViewType: core1_0.ImageViewType2D, Format: format,
			SubresourceRange: core1_0.ImageSubresourceRange{AspectMask: aspect, LevelCount: 1, LayerCount: 1}})
		if e != nil {
			return nil, e
		}
		t.views = append(t.views, view)
		if levels > 1 {
			if err = r.addChainViews(t, img, format, aspect, opts.storage); err != nil {
				return nil, err
			}
		}
	}
	if opts.sampled {
		t.sampler, _, err = r.deviceDriver.CreateSampler(nil, opts.samplerInfo())
		if err != nil {
			return nil, err
		}
		layouts := make([]core1_0.DescriptorSetLayout, count)
		for i := range layouts {
			layouts[i] = r.descriptorSetLayout
		}
		t.sets, _, err = r.deviceDriver.AllocateDescriptorSets(core1_0.DescriptorSetAllocateInfo{DescriptorPool: r.descriptorPool, SetLayouts: layouts})
		if err != nil {
			return nil, err
		}
		t.textures = make([]Texture, count)
		for i := range count {
			t.textures[i] = Texture{image: t.images[i], view: t.views[i], sampler: t.sampler, DescriptorSet: t.sets[i]}
			if levels > 1 {
				t.textures[i].view = t.chain[i]
			}
			if err = r.writeAppSampler(t.sets[i], 0, &t.textures[i]); err != nil {
				return nil, err
			}
		}
	}
	if aspect&core1_0.ImageAspectDepth != 0 {
		aspect |= depthAspect(format)
	}
	if err = r.primeAppImages(t, aspect, opts.sampled); err != nil {
		return nil, err
	}
	return t, nil
}

// addChainViews appends one more image's whole-chain view and, for a storage
// target, its storage views of levels 1 and up, in the order destroy and the
// failure unwind both expect: whatever was appended is released.
func (r *Renderer) addChainViews(t *appImages, img core1_0.Image, format core1_0.Format, aspect core1_0.ImageAspectFlags, storage bool) error {
	view := func(base, count int) (core1_0.ImageView, error) {
		v, _, err := r.deviceDriver.CreateImageView(nil, core1_0.ImageViewCreateInfo{Image: img, ViewType: core1_0.ImageViewType2D, Format: format,
			SubresourceRange: core1_0.ImageSubresourceRange{AspectMask: aspect, BaseMipLevel: base, LevelCount: count, LayerCount: 1}})
		return v, err
	}
	v, err := view(0, t.levels)
	if err != nil {
		return err
	}
	t.chain = append(t.chain, v)
	if !storage {
		return nil
	}
	t.levelViews = append(t.levelViews, make([]core1_0.ImageView, t.levels))
	i := len(t.levelViews) - 1
	t.levelViews[i][0] = t.views[i]
	for l := 1; l < t.levels; l++ {
		if t.levelViews[i][l], err = view(l, 1); err != nil {
			return err
		}
	}
	return nil
}

// addRangeViews creates one view of rg per image of t's colour and appends
// them as the next entry of rangeViews, so the index of rg in t.ranges is the
// index of its views.
func (r *Renderer) addRangeViews(t *RenderTarget, rg LevelRange) error {
	c := t.color
	format, _ := targetFormat(t.desc.Format)
	views := make([]core1_0.ImageView, len(c.images))
	c.rangeViews = append(c.rangeViews, views)
	for i, img := range c.images {
		v, _, err := r.deviceDriver.CreateImageView(nil, core1_0.ImageViewCreateInfo{Image: img, ViewType: core1_0.ImageViewType2D, Format: format,
			SubresourceRange: core1_0.ImageSubresourceRange{AspectMask: core1_0.ImageAspectColor, BaseMipLevel: rg.Base, LevelCount: rg.Count, LayerCount: 1}})
		if err != nil {
			return err
		}
		views[i] = v
	}
	return nil
}

// rangeView registers rg on t if no pass has named it yet, creating its
// views, and returns its index. A failed creation leaves nothing registered.
func (r *Renderer) rangeView(t *RenderTarget, rg LevelRange) (int, error) {
	if k := slices.Index(t.ranges, rg); k >= 0 {
		return k, nil
	}
	if err := r.addRangeViews(t, rg); err != nil {
		last := t.color.rangeViews[len(t.color.rangeViews)-1]
		t.color.rangeViews = t.color.rangeViews[:len(t.color.rangeViews)-1]
		for _, v := range last {
			if v.Handle() != 0 {
				r.deviceDriver.DestroyImageView(v, nil)
			}
		}
		return 0, err
	}
	t.ranges = append(t.ranges, rg)
	return len(t.ranges) - 1, nil
}

// readView is the view a pass binds for a read of t restricted to rg, in the
// instance t currently shows; the zero range is the texture's own view.
func (t *RenderTarget) readView(rg LevelRange) core1_0.ImageView {
	if rg.Count == 0 {
		return t.texture.view
	}
	k := slices.Index(t.ranges, rg)
	if k < 0 || t.color.rangeViews[k] == nil {
		return core1_0.ImageView{}
	}
	return t.color.rangeViews[k][t.instance]
}

func (r *Renderer) primeAppImages(t *appImages, aspect core1_0.ImageAspectFlags, sampled bool) error {
	cmd, err := r.beginSingleTimeCommands()
	if err != nil {
		return err
	}
	// Every level, so a chain's levels all rest in the sampled layout the
	// frame graph assumes before any pass has written them.
	sub := core1_0.ImageSubresourceRange{AspectMask: aspect, LevelCount: max(t.levels, 1), LayerCount: 1}
	for _, img := range t.images {
		b := core1_0.ImageMemoryBarrier{Image: img, OldLayout: core1_0.ImageLayoutUndefined, NewLayout: core1_0.ImageLayoutTransferDstOptimal,
			SrcQueueFamilyIndex: -1, DstQueueFamilyIndex: -1, SubresourceRange: sub, DstAccessMask: core1_0.AccessTransferWrite}
		if err = r.deviceDriver.CmdPipelineBarrier(cmd, core1_0.PipelineStageTopOfPipe, core1_0.PipelineStageTransfer, 0, nil, nil, []core1_0.ImageMemoryBarrier{b}); err != nil {
			break
		}
		if sampled {
			r.deviceDriver.CmdClearColorImage(cmd, img, core1_0.ImageLayoutTransferDstOptimal, core1_0.ClearValueFloat{}, sub)
		} else {
			r.deviceDriver.CmdClearDepthStencilImage(cmd, img, core1_0.ImageLayoutTransferDstOptimal, &core1_0.ClearValueDepthStencil{}, sub)
		}
		if err != nil {
			break
		}
		b.OldLayout, b.NewLayout = core1_0.ImageLayoutTransferDstOptimal, core1_0.ImageLayoutShaderReadOnlyOptimal
		b.SrcAccessMask, b.DstAccessMask = core1_0.AccessTransferWrite, core1_0.AccessShaderRead
		stage := core1_0.PipelineStageVertexShader | core1_0.PipelineStageFragmentShader | core1_0.PipelineStageComputeShader
		if !sampled {
			b.NewLayout = core1_0.ImageLayoutDepthStencilAttachmentOptimal
			b.DstAccessMask = core1_0.AccessDepthStencilAttachmentRead | core1_0.AccessDepthStencilAttachmentWrite
			stage = core1_0.PipelineStageEarlyFragmentTests | core1_0.PipelineStageLateFragmentTests
		}
		if err = r.deviceDriver.CmdPipelineBarrier(cmd, core1_0.PipelineStageTransfer, stage, 0, nil, nil, []core1_0.ImageMemoryBarrier{b}); err != nil {
			break
		}
	}
	if err != nil {
		r.deviceDriver.FreeCommandBuffers(cmd)
		return err
	}
	return r.endSingleTimeCommands(cmd)
}

func (t *appImages) destroy(d core1_0.DeviceDriver) {
	if t == nil {
		return
	}
	freeSets(d, t.sets)
	if t.sampler.Handle() != 0 {
		d.DestroySampler(t.sampler, nil)
	}
	for _, views := range t.rangeViews {
		for _, v := range views {
			if v.Handle() != 0 {
				d.DestroyImageView(v, nil)
			}
		}
	}
	for _, levels := range t.levelViews {
		// Level 0 is t.views[i], destroyed below.
		for _, v := range levels[1:] {
			if v.Handle() != 0 {
				d.DestroyImageView(v, nil)
			}
		}
	}
	for _, v := range t.chain {
		d.DestroyImageView(v, nil)
	}
	for _, v := range t.views {
		d.DestroyImageView(v, nil)
	}
	for _, i := range t.images {
		d.DestroyImage(i, nil)
	}
	for _, m := range t.memory {
		d.FreeMemory(m, nil)
	}
	*t = appImages{}
}
