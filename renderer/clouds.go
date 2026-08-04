package renderer

import (
	"fmt"
	"log"
	"unsafe"

	"github.com/vkngwrapper/core/v3/core1_0"
)

// cloudTarget is the half-resolution buffer the cloud raymarch renders into,
// one per swapchain image.
//
// Half resolution because the march is where the frame's time goes: measured on
// 09-water the sky pass costs 1.146 ms with it and 0.008 ms without, so over 99
// percent of "the sky is the top GPU pass" is these steps. Quartering the pixel
// count is the only lever that touches that, and it is available precisely
// because clouds are soft -- upscaling them bilinearly blurs nothing that was
// sharp to begin with.
//
// Per swapchain image for the same reason the HDR and bloom targets are: a frame
// in flight could otherwise be writing the buffer another frame is sampling.
type cloudTarget struct {
	images  []core1_0.Image
	memory  []core1_0.DeviceMemory
	views   []core1_0.ImageView
	sets    []core1_0.DescriptorSet
	fbs     []core1_0.Framebuffer
	sampler core1_0.Sampler

	extent core1_0.Extent2D
}

// cloudBufferCount is how many half-resolution buffers the ping-pong needs.
//
// maxFramesInFlight+1, and the +1 is load-bearing. Frame f writes buf[f%N] and
// reads buf[(f-1)%N]. With N buffers, frame f+N reuses what frame f wrote -- and
// the fence it waits on belongs to frame f+N-maxFramesInFlight, which with
// N = maxFramesInFlight+1 is frame f+1: the last frame that could still have
// been reading it. Two buffers would let a frame overwrite a buffer another
// frame in flight is still sampling.
const cloudBufferCount = maxFramesInFlight + 1

// cloudExtent halves the frame, rounding up so an odd dimension still covers it.
func cloudExtent(full core1_0.Extent2D) core1_0.Extent2D {
	return core1_0.Extent2D{
		Width:  max((full.Width+1)/2, 1),
		Height: max((full.Height+1)/2, 1),
	}
}

func createCloudTargets(
	instanceDriver core1_0.CoreInstanceDriver,
	deviceDriver core1_0.CoreDeviceDriver,
	physicalDevice core1_0.PhysicalDevice,
	descriptorPool core1_0.DescriptorPool,
	texSetLayout core1_0.DescriptorSetLayout,
	renderPass core1_0.RenderPass,
	full core1_0.Extent2D,
	count int,
) (*cloudTarget, error) {
	t := &cloudTarget{extent: cloudExtent(full)}

	// Linear and clamped: this buffer is read at a different resolution than it
	// was written, so the interpolation is the point rather than an accident.
	sampler, _, err := deviceDriver.CreateSampler(nil, core1_0.SamplerCreateInfo{
		MagFilter:    core1_0.FilterLinear,
		MinFilter:    core1_0.FilterLinear,
		AddressModeU: core1_0.SamplerAddressModeClampToEdge,
		AddressModeV: core1_0.SamplerAddressModeClampToEdge,
		AddressModeW: core1_0.SamplerAddressModeClampToEdge,
		MipmapMode:   core1_0.SamplerMipmapModeNearest,
	})
	if err != nil {
		return nil, fmt.Errorf("create cloud sampler: %w", err)
	}
	t.sampler = sampler

	for i := 0; i < count; i++ {
		img, mem, view, err := createOffscreenColor(instanceDriver, deviceDriver, physicalDevice, t.extent)
		if err != nil {
			t.destroy(deviceDriver)
			return nil, fmt.Errorf("create cloud image %d: %w", i, err)
		}
		t.images = append(t.images, img)
		t.memory = append(t.memory, mem)
		t.views = append(t.views, view)

		fb, _, err := deviceDriver.CreateFramebuffer(nil, core1_0.FramebufferCreateInfo{
			RenderPass:  renderPass,
			Attachments: []core1_0.ImageView{view},
			Width:       t.extent.Width,
			Height:      t.extent.Height,
			Layers:      1,
		})
		if err != nil {
			t.destroy(deviceDriver)
			return nil, fmt.Errorf("create cloud framebuffer %d: %w", i, err)
		}
		t.fbs = append(t.fbs, fb)
	}

	layouts := make([]core1_0.DescriptorSetLayout, count)
	for i := range layouts {
		layouts[i] = texSetLayout
	}
	sets, _, err := deviceDriver.AllocateDescriptorSets(core1_0.DescriptorSetAllocateInfo{
		DescriptorPool: descriptorPool,
		SetLayouts:     layouts,
	})
	if err != nil {
		t.destroy(deviceDriver)
		return nil, fmt.Errorf("allocate cloud descriptor sets: %w", err)
	}
	t.sets = sets

	writes := make([]core1_0.WriteDescriptorSet, count)
	for i := range writes {
		writes[i] = core1_0.WriteDescriptorSet{
			DstSet:         sets[i],
			DstBinding:     0,
			DescriptorType: core1_0.DescriptorTypeCombinedImageSampler,
			ImageInfo: []core1_0.DescriptorImageInfo{{
				Sampler:     sampler,
				ImageView:   t.views[i],
				ImageLayout: core1_0.ImageLayoutShaderReadOnlyOptimal,
			}},
		}
	}
	if err := deviceDriver.UpdateDescriptorSets(writes, nil); err != nil {
		t.destroy(deviceDriver)
		return nil, fmt.Errorf("update cloud descriptor sets: %w", err)
	}

	log.Printf("Cloud target: %dx%d (half of %dx%d) x%d",
		t.extent.Width, t.extent.Height, full.Width, full.Height, count)
	return t, nil
}

// createOffscreenColor allocates one half-float colour target usable as both an
// attachment and a sampled texture.
func createOffscreenColor(
	instanceDriver core1_0.CoreInstanceDriver,
	deviceDriver core1_0.CoreDeviceDriver,
	physicalDevice core1_0.PhysicalDevice,
	extent core1_0.Extent2D,
) (core1_0.Image, core1_0.DeviceMemory, core1_0.ImageView, error) {
	var (
		zi core1_0.Image
		zm core1_0.DeviceMemory
		zv core1_0.ImageView
	)
	img, _, err := deviceDriver.CreateImage(nil, core1_0.ImageCreateInfo{
		ImageType:   core1_0.ImageType2D,
		Format:      hdrFormat,
		Extent:      core1_0.Extent3D{Width: extent.Width, Height: extent.Height, Depth: 1},
		MipLevels:   1,
		ArrayLayers: 1,
		Samples:     core1_0.Samples1,
		Tiling:      core1_0.ImageTilingOptimal,
		// TransferDst only for the one-time clear that gives the buffer a
		// defined layout before the first frame samples it as history.
		Usage:         core1_0.ImageUsageColorAttachment | core1_0.ImageUsageSampled | core1_0.ImageUsageTransferDst,
		SharingMode:   core1_0.SharingModeExclusive,
		InitialLayout: core1_0.ImageLayoutUndefined,
	})
	if err != nil {
		return zi, zm, zv, err
	}

	reqs := deviceDriver.GetImageMemoryRequirements(img)
	memType, err := findMemoryType(instanceDriver, physicalDevice, reqs.MemoryTypeBits, core1_0.MemoryPropertyDeviceLocal)
	if err != nil {
		deviceDriver.DestroyImage(img, nil)
		return zi, zm, zv, err
	}
	mem, _, err := deviceDriver.AllocateMemory(nil, core1_0.MemoryAllocateInfo{
		AllocationSize:  reqs.Size,
		MemoryTypeIndex: memType,
	})
	if err != nil {
		deviceDriver.DestroyImage(img, nil)
		return zi, zm, zv, err
	}
	if _, err := deviceDriver.BindImageMemory(img, mem, 0); err != nil {
		deviceDriver.FreeMemory(mem, nil)
		deviceDriver.DestroyImage(img, nil)
		return zi, zm, zv, err
	}

	view, _, err := deviceDriver.CreateImageView(nil, core1_0.ImageViewCreateInfo{
		Image:    img,
		ViewType: core1_0.ImageViewType2D,
		Format:   hdrFormat,
		SubresourceRange: core1_0.ImageSubresourceRange{
			AspectMask: core1_0.ImageAspectColor,
			LevelCount: 1,
			LayerCount: 1,
		},
	})
	if err != nil {
		deviceDriver.FreeMemory(mem, nil)
		deviceDriver.DestroyImage(img, nil)
		return zi, zm, zv, err
	}
	return img, mem, view, nil
}

func (t *cloudTarget) destroy(deviceDriver core1_0.DeviceDriver) {
	if t == nil {
		return
	}
	for _, fb := range t.fbs {
		deviceDriver.DestroyFramebuffer(fb, nil)
	}
	for _, v := range t.views {
		deviceDriver.DestroyImageView(v, nil)
	}
	for _, m := range t.memory {
		deviceDriver.FreeMemory(m, nil)
	}
	for _, i := range t.images {
		deviceDriver.DestroyImage(i, nil)
	}
	if t.sampler.Handle() != 0 {
		deviceDriver.DestroySampler(t.sampler, nil)
	}
	t.fbs, t.views, t.memory, t.images, t.sets = nil, nil, nil, nil, nil
	t.sampler = core1_0.Sampler{}
}

// recordClouds marches the cloud layer into the half-resolution target and
// blends it with the previous frame's result.
//
// Recorded before the scene render pass, in its own pass. The render pass's
// external dependency -- colour writes before fragment reads -- is what orders
// it against the sky draw that samples it, so no explicit barrier is needed.
//
// It runs every frame even when the layer is disabled, because a pass that
// sometimes does not run leaves its target in an undefined layout while the sky
// still binds it every frame. That is the trap the bloom chain and the sky-view
// tables both fell into. With zero steps the shader early-outs to fully
// transmissive and the composite is a no-op, at a cost of a quarter-resolution
// fullscreen triangle.
func (r *Renderer) recordClouds(cmdBuf core1_0.CommandBuffer, lighting SceneLighting) error {
	t := r.clouds
	// Indexed by a frame counter rather than the swapchain image index: the
	// presentation engine is free to hand back indices in any order, and the
	// history chain has to be strictly the previous frame's.
	idx := r.cloudFrame % len(t.fbs)

	if err := r.deviceDriver.CmdBeginRenderPass(cmdBuf, core1_0.SubpassContentsInline, core1_0.RenderPassBeginInfo{
		RenderPass:  r.cloudRenderPass,
		Framebuffer: t.fbs[idx],
		RenderArea:  core1_0.Rect2D{Offset: core1_0.Offset2D{X: 0, Y: 0}, Extent: t.extent},
	}); err != nil {
		return err
	}
	r.deviceDriver.CmdBindPipeline(cmdBuf, core1_0.PipelineBindPointGraphics, r.cloudPipeline)
	r.deviceDriver.CmdSetViewport(cmdBuf, core1_0.Viewport{
		Width: float32(t.extent.Width), Height: float32(t.extent.Height), MinDepth: 0, MaxDepth: 1,
	})
	r.deviceDriver.CmdSetScissor(cmdBuf, core1_0.Rect2D{Offset: core1_0.Offset2D{X: 0, Y: 0}, Extent: t.extent})
	// The previous frame's target, which this pass reprojects and blends.
	prev := (r.cloudFrame + len(t.sets) - 1) % len(t.sets)
	r.deviceDriver.CmdBindDescriptorSets(cmdBuf, core1_0.PipelineBindPointGraphics, r.pipelineLayout, 0,
		[]core1_0.DescriptorSet{t.sets[prev]}, nil)

	// The same packing the sky uses, because the march was lifted out of it and
	// reads the same values from the same offsets.
	var pc [pushConstantSize / 4]float32
	copy(pc[:16], lighting.InvVP[:])
	pc[16] = lighting.CameraPos[0]
	pc[17] = lighting.CameraPos[1]
	pc[18] = lighting.CameraPos[2]
	pc[32] = lighting.Time
	pc[33] = lighting.NightFactor
	pc[34] = float32(lighting.CloudSteps)
	pc[36] = lighting.SunDir[0]
	pc[37] = lighting.SunDir[1]
	pc[38] = lighting.SunDir[2]
	pc[40] = lighting.SunColor[0]
	pc[41] = lighting.SunColor[1]
	pc[42] = lighting.SunColor[2]
	pc[43] = lighting.SunElevation
	// The previous frame's view-projection, in the four vec4 slots the march
	// does not read. See the push block in clouds.frag.
	copy(pc[44:60], r.prevVP[:])
	pc[62] = lighting.RealSunDir[0]
	pc[63] = lighting.RealSunDir[2]
	r.deviceDriver.CmdPushConstants(cmdBuf, r.pipelineLayout, core1_0.StageVertex|core1_0.StageFragment, 0,
		unsafe.Slice((*byte)(unsafe.Pointer(&pc[0])), pushConstantSize))

	r.deviceDriver.CmdDraw(cmdBuf, 3, 1, 0, 0)
	r.deviceDriver.CmdEndRenderPass(cmdBuf)
	return nil
}

// cloudSetFor is the descriptor the sky pass samples the cloud layer through:
// whatever this frame's march just wrote.
func (r *Renderer) cloudSetFor() core1_0.DescriptorSet {
	return r.clouds.sets[r.cloudFrame%len(r.clouds.sets)]
}
