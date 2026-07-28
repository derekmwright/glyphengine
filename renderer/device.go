package renderer

import (
	"fmt"
	"log"

	"github.com/vkngwrapper/core/v3/core1_0"
	"github.com/vkngwrapper/extensions/v3/khr_surface"
	"github.com/vkngwrapper/extensions/v3/khr_swapchain"
)

// pickPhysicalDevice selects the first GPU that supports graphics and present
// queue families, the swapchain extension, and at least one surface format/mode.
func pickPhysicalDevice(instanceDriver core1_0.CoreInstanceDriver, surfaceExt khr_surface.ExtensionDriver, surface khr_surface.Surface) (core1_0.PhysicalDevice, queueFamilyIndices, error) {
	devices, _, err := instanceDriver.EnumeratePhysicalDevices()
	if err != nil {
		return core1_0.PhysicalDevice{}, queueFamilyIndices{}, err
	}
	if len(devices) == 0 {
		return core1_0.PhysicalDevice{}, queueFamilyIndices{}, fmt.Errorf("no Vulkan-capable GPU found")
	}

	for _, device := range devices {
		indices := findQueueFamilies(instanceDriver, device, surfaceExt, surface)
		if !indices.isComplete() {
			continue
		}

		// Check for swapchain extension support
		extensions, _, err := instanceDriver.EnumerateDeviceExtensionProperties(device)
		if err != nil {
			continue
		}
		if _, ok := extensions[khr_swapchain.ExtensionName]; !ok {
			continue
		}

		// Check that the surface has at least one format and present mode
		formats, _, err := surfaceExt.GetPhysicalDeviceSurfaceFormats(surface, device)
		if err != nil || len(formats) == 0 {
			continue
		}
		modes, _, err := surfaceExt.GetPhysicalDeviceSurfacePresentModes(surface, device)
		if err != nil || len(modes) == 0 {
			continue
		}

		props, err := instanceDriver.GetPhysicalDeviceProperties(device)
		if err == nil {
			log.Printf("Selected GPU: %s", props.DriverName)
		}
		log.Printf("Queue families - graphics: %d, present: %d", indices.graphicsFamily, indices.presentFamily)
		return device, indices, nil
	}

	return core1_0.PhysicalDevice{}, queueFamilyIndices{}, fmt.Errorf("no suitable GPU found")
}

// createLogicalDevice creates a Vulkan logical device with queues for the
// graphics and present families and the swapchain extension enabled.
func createLogicalDevice(instanceDriver core1_0.CoreInstanceDriver, physicalDevice core1_0.PhysicalDevice, indices queueFamilyIndices) (core1_0.CoreDeviceDriver, error) {
	// Build unique queue family set
	uniqueFamilies := map[int]struct{}{
		indices.graphicsFamily: {},
		indices.presentFamily:  {},
	}

	var queueCreateInfos []core1_0.DeviceQueueCreateInfo
	for family := range uniqueFamilies {
		queueCreateInfos = append(queueCreateInfos, core1_0.DeviceQueueCreateInfo{
			QueueFamilyIndex: family,
			QueuePriorities:  []float32{1.0},
		})
	}

	// Enable anisotropic filtering when the GPU supports it (queried again in
	// Renderer.New to size MaxAnisotropy for texture samplers).
	supported := instanceDriver.GetPhysicalDeviceFeatures(physicalDevice)

	deviceDriver, _, err := instanceDriver.CreateDevice(physicalDevice, nil, core1_0.DeviceCreateInfo{
		QueueCreateInfos:      queueCreateInfos,
		EnabledExtensionNames: []string{khr_swapchain.ExtensionName},
		EnabledFeatures: &core1_0.PhysicalDeviceFeatures{
			SamplerAnisotropy: supported.SamplerAnisotropy,
		},
	})
	if err != nil {
		return nil, err
	}

	log.Println("Logical device created")
	return deviceDriver, nil
}
