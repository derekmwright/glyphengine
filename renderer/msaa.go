package renderer

import (
	"fmt"

	"github.com/vkngwrapper/core/v3/core1_0"
)

// msaaResources holds one multisample color image per swapchain image,
// used as the actual render target when MSAA is enabled. The swapchain
// images become resolve attachments.
type msaaResources struct {
	images   []core1_0.Image
	memories []core1_0.DeviceMemory
	views    []core1_0.ImageView
}

// createMSAAResources allocates one multisample color image per swapchain
// image and creates image views for them.
func createMSAAResources(
	instanceDriver core1_0.CoreInstanceDriver,
	deviceDriver core1_0.CoreDeviceDriver,
	physicalDevice core1_0.PhysicalDevice,
	extent core1_0.Extent2D,
	format core1_0.Format,
	samples core1_0.SampleCountFlags,
	count int,
) (*msaaResources, error) {
	m := &msaaResources{
		images:   make([]core1_0.Image, count),
		memories: make([]core1_0.DeviceMemory, count),
		views:    make([]core1_0.ImageView, count),
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
			Usage:         core1_0.ImageUsageColorAttachment | core1_0.ImageUsageTransientAttachment,
			SharingMode:   core1_0.SharingModeExclusive,
			InitialLayout: core1_0.ImageLayoutUndefined,
		})
		if err != nil {
			m.destroy(deviceDriver, i)
			return nil, fmt.Errorf("create MSAA image %d: %w", i, err)
		}
		m.images[i] = img

		memReqs := deviceDriver.GetImageMemoryRequirements(img)
		memTypeIdx, err := findMemoryType(
			instanceDriver, physicalDevice,
			memReqs.MemoryTypeBits,
			core1_0.MemoryPropertyDeviceLocal,
		)
		if err != nil {
			m.destroy(deviceDriver, i+1)
			return nil, err
		}

		mem, _, err := deviceDriver.AllocateMemory(nil, core1_0.MemoryAllocateInfo{
			AllocationSize:  memReqs.Size,
			MemoryTypeIndex: memTypeIdx,
		})
		if err != nil {
			m.destroy(deviceDriver, i+1)
			return nil, fmt.Errorf("allocate MSAA memory %d: %w", i, err)
		}
		m.memories[i] = mem

		_, err = deviceDriver.BindImageMemory(img, mem, 0)
		if err != nil {
			m.destroy(deviceDriver, i+1)
			return nil, fmt.Errorf("bind MSAA image memory %d: %w", i, err)
		}

		view, _, err := deviceDriver.CreateImageView(nil, core1_0.ImageViewCreateInfo{
			Image:    img,
			ViewType: core1_0.ImageViewType2D,
			Format:   format,
			SubresourceRange: core1_0.ImageSubresourceRange{
				AspectMask: core1_0.ImageAspectColor,
				LevelCount: 1,
				LayerCount: 1,
			},
		})
		if err != nil {
			m.destroy(deviceDriver, i+1)
			return nil, fmt.Errorf("create MSAA image view %d: %w", i, err)
		}
		m.views[i] = view
	}

	return m, nil
}

// destroy releases the first n MSAA images and their memory.
func (m *msaaResources) destroy(deviceDriver core1_0.DeviceDriver, n int) {
	for i := 0; i < n; i++ {
		if m.views[i].Handle() != 0 {
			deviceDriver.DestroyImageView(m.views[i], nil)
		}
		if m.memories[i].Handle() != 0 {
			deviceDriver.FreeMemory(m.memories[i], nil)
		}
		if m.images[i].Handle() != 0 {
			deviceDriver.DestroyImage(m.images[i], nil)
		}
	}
}
