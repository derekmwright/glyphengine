package renderer

import (
	"fmt"
	"math"
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
}

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
}

func (t *RenderTarget) Texture() *Texture { return &t.texture }
func (t *RenderTarget) Extent() (w, h uint32) {
	if t.color != nil {
		return uint32(t.color.extent.Width), uint32(t.color.extent.Height)
	}
	return 0, 0
}

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
	// Graph framebuffers must retire before their attachment views. The graph
	// rebuild queues those first, then pending targets, after the waited fence.
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
	var err error
	t.color, err = r.newAppImages(f, core1_0.ImageAspectColor, e, n, appImageOptions{
		sampled: true, storage: t.desc.Storage, filter: t.desc.Filter, wrap: t.desc.Wrap,
	})
	if err != nil {
		return err
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
}

type appImageOptions struct {
	sampled, storage bool
	filter           TargetFilter
	wrap             TargetWrap
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
	return core1_0.SamplerCreateInfo{MagFilter: filter, MinFilter: filter,
		AddressModeU: wrap, AddressModeV: wrap, AddressModeW: core1_0.SamplerAddressModeClampToEdge}
}

func (r *Renderer) newAppImages(format core1_0.Format, aspect core1_0.ImageAspectFlags, extent core1_0.Extent2D, count int, opts appImageOptions) (_ *appImages, err error) {
	t := &appImages{extent: extent}
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
			Extent: core1_0.Extent3D{Width: extent.Width, Height: extent.Height, Depth: 1}, MipLevels: 1, ArrayLayers: 1,
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
			if err = r.writeAppSampler(t.sets[i], 0, &t.textures[i]); err != nil {
				return nil, err
			}
		}
	}
	if err = r.primeAppImages(t, aspect, opts.sampled); err != nil {
		return nil, err
	}
	return t, nil
}

func (r *Renderer) primeAppImages(t *appImages, aspect core1_0.ImageAspectFlags, sampled bool) error {
	cmd, err := r.beginSingleTimeCommands()
	if err != nil {
		return err
	}
	sub := core1_0.ImageSubresourceRange{AspectMask: aspect, LevelCount: 1, LayerCount: 1}
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
