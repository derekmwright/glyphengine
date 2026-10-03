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
//
// It takes the device's own answers rather than asking for them a second time,
// so the features and extensions it ENABLES are exactly the ones Capabilities
// REPORTS. Those used to be two independent sets of queries, which is how a
// report of something the device was never created with would have gone
// unnoticed.
func createLogicalDevice(instanceDriver core1_0.CoreInstanceDriver, features2 khr_get_physical_device_properties2.ExtensionDriver, physicalDevice core1_0.PhysicalDevice, indices queueFamilyIndices, a deviceAnswers) (core1_0.CoreDeviceDriver, error) {
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

	// Anisotropic filtering is enabled when the GPU supports it; the level the
	// samplers are then given is Capabilities.MaxAnisotropy, negotiated from
	// this same feature bit.
	supported := a.features

	// VUID-VkDeviceCreateInfo-pProperties-04451: if the physical device
	// supports VK_KHR_portability_subset, it *must* be enabled here. Every
	// MoltenVK device advertises it, and creating a device without it is
	// invalid usage -- an error under validation, undefined without it.
	//
	// Conditional for the same reason the instance opt-in is: a conformant
	// driver does not advertise this, and asking for it there would fail device
	// creation on every machine that works today.
	deviceExtensions, err := dynamicRenderingExtensions(a.extensions, a.props)
	if err != nil {
		return nil, err
	}
	if err := requireDynamicRendering(features2, physicalDevice, a.props.DriverName); err != nil {
		return nil, err
	}
	if portabilitySubset(a.extensions) {
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
			MultiDrawIndirect:                 supported.MultiDrawIndirect,
			DrawIndirectFirstInstance:         supported.DrawIndirectFirstInstance,
			// Enabled whenever the device has it, like multi-draw above and
			// unlike the ray-query closure: a feature bit is permission to create
			// a pipeline-statistics query pool, not a pool, so a renderer that
			// never calls WithPipelineStatistics pays nothing for it. Enabling it
			// conditionally on the option would make Capabilities.PipelineStatistics
			// a report about the build rather than about the device, which is the
			// one thing this struct is not.
			PipelineStatisticsQuery: supported.PipelineStatisticsQuery,
		},
	})
	if err != nil {
		return nil, err
	}

	log.Println("Logical device created")
	return deviceDriver, nil
}

// requireDeviceLimits refuses a device that cannot run this renderer at all,
// as opposed to one that runs it with less -- the latter is Capabilities' job.
//
// Both limits are checked here rather than discovered as a pipeline that will
// not create, because a Vulkan result code from vkCreateGraphicsPipelines does
// not say which limit it was.
func requireDeviceLimits(props *core1_0.PhysicalDeviceProperties) error {
	// The push constant block is shared by every pipeline and is already
	// larger than Vulkan's guaranteed 128 bytes.
	if lim := props.Limits.MaxPushConstantsSize; lim < pushConstantSize {
		return fmt.Errorf("renderer: device allows %d bytes of push constants, engine needs %d",
			lim, pushConstantSize)
	}
	log.Printf("Push constants: %d bytes used of %d available", pushConstantSize, props.Limits.MaxPushConstantsSize)

	// The clustered light data (LightBuffer, ClusterGrid, LightIndices) lives
	// in three storage buffer bindings on one fragment-stage descriptor set.
	// Vulkan 1.0 core guarantees at least 4 per stage, so this should never
	// fire -- a silent 0 here would be a device that simply cannot run this
	// renderer.
	if lim := props.Limits.MaxPerStageDescriptorStorageBuffers; lim < lightStorageBuffersPerSet {
		return fmt.Errorf("renderer: device allows %d storage buffers per stage, engine needs %d for clustered lighting",
			lim, lightStorageBuffersPerSet)
	}
	return nil
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
