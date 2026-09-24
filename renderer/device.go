package renderer

import (
	"fmt"
	"log"

	"github.com/vkngwrapper/core/v3/common"
	"github.com/vkngwrapper/core/v3/core1_0"
	"github.com/vkngwrapper/extensions/v3/khr_create_renderpass2"
	"github.com/vkngwrapper/extensions/v3/khr_depth_stencil_resolve"
	"github.com/vkngwrapper/extensions/v3/khr_dynamic_rendering"
	"github.com/vkngwrapper/extensions/v3/khr_get_physical_device_properties2"
	"github.com/vkngwrapper/extensions/v3/khr_maintenance2"
	"github.com/vkngwrapper/extensions/v3/khr_multiview"
	"github.com/vkngwrapper/extensions/v3/khr_portability_subset"
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
// graphics/present queues and the required dynamic-rendering capabilities.
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

	// VUID-VkDeviceCreateInfo-pProperties-04451: if the physical device
	// supports VK_KHR_portability_subset, it *must* be enabled here. Every
	// MoltenVK device advertises it, and creating a device without it is
	// invalid usage -- an error under validation, undefined without it.
	//
	// Conditional for the same reason the instance opt-in is: a conformant
	// driver does not advertise this, and asking for it there would fail device
	// creation on every machine that works today.
	available, _, err := instanceDriver.EnumerateDeviceExtensionProperties(physicalDevice)
	if err != nil {
		return nil, fmt.Errorf("enumerate device extensions: %w", err)
	}
	props, err := instanceDriver.GetPhysicalDeviceProperties(physicalDevice)
	if err != nil {
		return nil, fmt.Errorf("query driver properties: %w", err)
	}
	deviceExtensions, err := dynamicRenderingExtensions(available, props)
	if err != nil {
		return nil, err
	}
	features2 := khr_get_physical_device_properties2.CreateExtensionDriverFromCoreDriver(instanceDriver)
	if err := requireDynamicRendering(features2, physicalDevice, props.DriverName); err != nil {
		return nil, err
	}
	if _, ok := available[khr_portability_subset.ExtensionName]; ok {
		deviceExtensions = append(deviceExtensions, khr_portability_subset.ExtensionName)
		log.Printf("Portability subset enabled (%s)", khr_portability_subset.ExtensionName)
	}

	deviceDriver, _, err := instanceDriver.CreateDevice(physicalDevice, nil, core1_0.DeviceCreateInfo{
		NextOptions:           common.NextOptions{Next: khr_dynamic_rendering.PhysicalDeviceDynamicRenderingFeatures{DynamicRendering: true}},
		QueueCreateInfos:      queueCreateInfos,
		EnabledExtensionNames: deviceExtensions,
		EnabledFeatures: &core1_0.PhysicalDeviceFeatures{
			SamplerAnisotropy:                 supported.SamplerAnisotropy,
			ShaderStorageImageExtendedFormats: supported.ShaderStorageImageExtendedFormats,
		},
	})
	if err != nil {
		return nil, err
	}

	log.Println("Logical device created")
	return deviceDriver, nil
}

// Keep the Vulkan 1.0 baseline by explicitly enabling the extension dependency
// closure, including the instance-level properties2 extension in createInstance.
func dynamicRenderingExtensions(available map[string]*core1_0.ExtensionProperties, props *core1_0.PhysicalDeviceProperties) ([]string, error) {
	names := []string{khr_swapchain.ExtensionName, khr_dynamic_rendering.ExtensionName, khr_depth_stencil_resolve.ExtensionName, khr_create_renderpass2.ExtensionName, khr_multiview.ExtensionName, khr_maintenance2.ExtensionName}
	for _, name := range names {
		if _, ok := available[name]; !ok {
			return nil, fmt.Errorf("driver %q (%v) requires %s for VK_KHR_dynamic_rendering", props.DriverName, props.DriverVersion, name)
		}
	}
	return names, nil
}
func requireDynamicRendering(features2 khr_get_physical_device_properties2.ExtensionDriver, device core1_0.PhysicalDevice, driverName string) error {
	if features2 == nil {
		return fmt.Errorf("driver %q: VK_KHR_dynamic_rendering requires VK_KHR_get_physical_device_properties2", driverName)
	}
	dynamic := khr_dynamic_rendering.PhysicalDeviceDynamicRenderingFeatures{}
	if err := features2.GetPhysicalDeviceFeatures2(device, &khr_get_physical_device_properties2.PhysicalDeviceFeatures2{NextOutData: common.NextOutData{Next: &dynamic}}); err != nil {
		return fmt.Errorf("driver %q: query VK_KHR_dynamic_rendering: %w", driverName, err)
	}
	if !dynamic.DynamicRendering {
		return fmt.Errorf("driver %q does not support VK_KHR_dynamic_rendering dynamicRendering", driverName)
	}
	return nil
}
