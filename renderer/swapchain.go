package renderer

import (
	"log"

	"github.com/vkngwrapper/core/v3/core1_0"
	"github.com/vkngwrapper/extensions/v3/khr_surface"
	"github.com/vkngwrapper/extensions/v3/khr_swapchain"
)

// swapchainDetails holds the swapchain handle and its associated images/views.
type swapchainDetails struct {
	swapchain   khr_swapchain.Swapchain
	images      []core1_0.Image
	imageViews  []core1_0.ImageView
	imageFormat core1_0.Format
	extent      core1_0.Extent2D
}

// chooseSwapSurfaceFormat picks B8G8R8A8 sRGB if available, otherwise the first format.
func chooseSwapSurfaceFormat(formats []khr_surface.SurfaceFormat) khr_surface.SurfaceFormat {
	for _, f := range formats {
		if f.Format == core1_0.FormatB8G8R8A8SRGB && f.ColorSpace == khr_surface.ColorSpaceSRGBNonlinear {
			return f
		}
	}
	return formats[0]
}

// chooseSwapPresentMode picks a present mode for the requested vsync setting.
//
// This is the engine's frame pacing. FIFO blocks the presentation queue at the
// refresh rate of whichever display the surface is on — correct per-monitor,
// costing no CPU, and impossible to get wrong. Nothing else should be limiting
// frame rate on top of it.
//
// FIFO Relaxed is deliberately never selected: it caused device-lost failures
// on some AMD drivers.
func chooseSwapPresentMode(modes []khr_surface.PresentMode, vsync bool) khr_surface.PresentMode {
	var prefer []khr_surface.PresentMode
	if vsync {
		// FIFO is the only mode Vulkan guarantees exists, so this always hits.
		prefer = []khr_surface.PresentMode{khr_surface.PresentModeFIFO}
	} else {
		// Mailbox renders unbounded without tearing by replacing the queued
		// image; Immediate is the tearing fallback where Mailbox is absent.
		prefer = []khr_surface.PresentMode{
			khr_surface.PresentModeMailbox,
			khr_surface.PresentModeImmediate,
		}
	}
	for _, want := range prefer {
		for _, m := range modes {
			if m == want {
				return m
			}
		}
	}
	// Guaranteed available by the spec.
	return khr_surface.PresentModeFIFO
}

// chooseSwapExtent returns the swapchain extent, using the surface's current
// extent or clamping the requested size to the surface's min/max.
func chooseSwapExtent(capabilities *khr_surface.SurfaceCapabilities, width, height int) core1_0.Extent2D {
	// If current extent is not the special value, use it
	if capabilities.CurrentExtent.Width != -1 {
		return capabilities.CurrentExtent
	}
	return core1_0.Extent2D{
		Width:  clampInt(width, capabilities.MinImageExtent.Width, capabilities.MaxImageExtent.Width),
		Height: clampInt(height, capabilities.MinImageExtent.Height, capabilities.MaxImageExtent.Height),
	}
}

// createSwapchain queries surface capabilities and creates a swapchain with
// associated image views. Returns the swapchain details and extension driver.
func createSwapchain(
	deviceDriver core1_0.CoreDeviceDriver,
	surfaceExt khr_surface.ExtensionDriver,
	surface khr_surface.Surface,
	physicalDevice core1_0.PhysicalDevice,
	indices queueFamilyIndices,
	width, height int,
	vsync bool,
) (*swapchainDetails, khr_swapchain.ExtensionDriver, error) {
	capabilities, _, err := surfaceExt.GetPhysicalDeviceSurfaceCapabilities(surface, physicalDevice)
	if err != nil {
		return nil, nil, err
	}

	formats, _, err := surfaceExt.GetPhysicalDeviceSurfaceFormats(surface, physicalDevice)
	if err != nil {
		return nil, nil, err
	}

	modes, _, err := surfaceExt.GetPhysicalDeviceSurfacePresentModes(surface, physicalDevice)
	if err != nil {
		return nil, nil, err
	}

	log.Printf("Available present modes: %v", modes)
	surfaceFormat := chooseSwapSurfaceFormat(formats)
	presentMode := chooseSwapPresentMode(modes, vsync)
	extent := chooseSwapExtent(capabilities, width, height)

	imageCount := capabilities.MinImageCount + 1
	if capabilities.MaxImageCount > 0 && imageCount > capabilities.MaxImageCount {
		imageCount = capabilities.MaxImageCount
	}

	createInfo := khr_swapchain.SwapchainCreateInfo{
		Surface:          surface,
		MinImageCount:    imageCount,
		ImageFormat:      surfaceFormat.Format,
		ImageColorSpace:  surfaceFormat.ColorSpace,
		ImageExtent:      extent,
		ImageArrayLayers: 1,
		ImageUsage:       core1_0.ImageUsageColorAttachment,
		PreTransform:     capabilities.CurrentTransform,
		CompositeAlpha:   khr_surface.CompositeAlphaOpaque,
		PresentMode:      presentMode,
		Clipped:          true,
	}

	if indices.graphicsFamily != indices.presentFamily {
		createInfo.ImageSharingMode = core1_0.SharingModeConcurrent
		createInfo.QueueFamilyIndices = []int{indices.graphicsFamily, indices.presentFamily}
	} else {
		createInfo.ImageSharingMode = core1_0.SharingModeExclusive
	}

	swapchainExt := khr_swapchain.CreateExtensionDriverFromCoreDriver(deviceDriver)
	if swapchainExt == nil {
		return nil, nil, err
	}

	swapchain, _, err := swapchainExt.CreateSwapchain(nil, createInfo)
	if err != nil {
		return nil, nil, err
	}

	images, _, err := swapchainExt.GetSwapchainImages(swapchain)
	if err != nil {
		return nil, nil, err
	}

	imageViews := make([]core1_0.ImageView, len(images))
	for i, img := range images {
		iv, _, err := deviceDriver.CreateImageView(nil, core1_0.ImageViewCreateInfo{
			Image:    img,
			ViewType: core1_0.ImageViewType2D,
			Format:   surfaceFormat.Format,
			Components: core1_0.ComponentMapping{
				R: core1_0.ComponentSwizzleIdentity,
				G: core1_0.ComponentSwizzleIdentity,
				B: core1_0.ComponentSwizzleIdentity,
				A: core1_0.ComponentSwizzleIdentity,
			},
			SubresourceRange: core1_0.ImageSubresourceRange{
				AspectMask:     core1_0.ImageAspectColor,
				BaseMipLevel:   0,
				LevelCount:     1,
				BaseArrayLayer: 0,
				LayerCount:     1,
			},
		})
		if err != nil {
			return nil, nil, err
		}
		imageViews[i] = iv
	}

	log.Printf("Swapchain created: format=%v, presentMode=%v, extent=%dx%d, images=%d",
		surfaceFormat.Format, presentMode, extent.Width, extent.Height, len(images))

	return &swapchainDetails{
		swapchain:   swapchain,
		images:      images,
		imageViews:  imageViews,
		imageFormat: surfaceFormat.Format,
		extent:      extent,
	}, swapchainExt, nil
}
