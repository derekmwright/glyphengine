package renderer

import (
	"fmt"

	"github.com/vkngwrapper/core/v3/core1_0"
)

// depthResources holds one depth image per swapchain image so that concurrent
// in-flight frames don't share a single depth buffer.
type depthResources struct {
	images   []core1_0.Image
	memories []core1_0.DeviceMemory
	views    []core1_0.ImageView
	format   core1_0.Format
}

// createDepthResources allocates one device-local depth image per swapchain
// image and creates image views for them.
func createDepthResources(
	instanceDriver core1_0.CoreInstanceDriver,
	deviceDriver core1_0.CoreDeviceDriver,
	physicalDevice core1_0.PhysicalDevice,
	extent core1_0.Extent2D,
	count int,
	samples core1_0.SampleCountFlags,
) (*depthResources, error) {
	format, err := findDepthFormat(instanceDriver, physicalDevice)
	if err != nil {
		return nil, err
	}

	d := &depthResources{
		images:   make([]core1_0.Image, count),
		memories: make([]core1_0.DeviceMemory, count),
		views:    make([]core1_0.ImageView, count),
		format:   format,
	}

	for i := 0; i < count; i++ {
		img, _, err := deviceDriver.CreateImage(nil, core1_0.ImageCreateInfo{
			ImageType: core1_0.ImageType2D,
			Format:    format,
			Extent: core1_0.Extent3D{
				Width:  extent.Width,
				Height: extent.Height,
				Depth:  1,
			},
			MipLevels:     1,
			ArrayLayers:   1,
			Samples:       samples,
			Tiling:        core1_0.ImageTilingOptimal,
			Usage:         core1_0.ImageUsageDepthStencilAttachment,
			SharingMode:   core1_0.SharingModeExclusive,
			InitialLayout: core1_0.ImageLayoutUndefined,
		})
		if err != nil {
			d.destroy(deviceDriver, i)
			return nil, fmt.Errorf("create depth image %d: %w", i, err)
		}
		d.images[i] = img

		memReqs := deviceDriver.GetImageMemoryRequirements(img)
		memTypeIdx, err := findMemoryType(
			instanceDriver, physicalDevice,
			memReqs.MemoryTypeBits,
			core1_0.MemoryPropertyDeviceLocal,
		)
		if err != nil {
			d.destroy(deviceDriver, i+1)
			return nil, err
		}

		mem, _, err := deviceDriver.AllocateMemory(nil, core1_0.MemoryAllocateInfo{
			AllocationSize:  memReqs.Size,
			MemoryTypeIndex: memTypeIdx,
		})
		if err != nil {
			d.destroy(deviceDriver, i+1)
			return nil, fmt.Errorf("allocate depth memory %d: %w", i, err)
		}
		d.memories[i] = mem

		_, err = deviceDriver.BindImageMemory(img, mem, 0)
		if err != nil {
			d.destroy(deviceDriver, i+1)
			return nil, fmt.Errorf("bind depth image memory %d: %w", i, err)
		}

		view, _, err := deviceDriver.CreateImageView(nil, core1_0.ImageViewCreateInfo{
			Image:    img,
			ViewType: core1_0.ImageViewType2D,
			Format:   format,
			SubresourceRange: core1_0.ImageSubresourceRange{
				AspectMask: core1_0.ImageAspectDepth,
				LevelCount: 1,
				LayerCount: 1,
			},
		})
		if err != nil {
			d.destroy(deviceDriver, i+1)
			return nil, fmt.Errorf("create depth image view %d: %w", i, err)
		}
		d.views[i] = view
	}

	return d, nil
}

// destroy releases the first n depth images and their memory.
func (d *depthResources) destroy(deviceDriver core1_0.DeviceDriver, n int) {
	for i := 0; i < n; i++ {
		if d.views[i].Handle() != 0 {
			deviceDriver.DestroyImageView(d.views[i], nil)
		}
		if d.memories[i].Handle() != 0 {
			deviceDriver.FreeMemory(d.memories[i], nil)
		}
		if d.images[i].Handle() != 0 {
			deviceDriver.DestroyImage(d.images[i], nil)
		}
	}
}
