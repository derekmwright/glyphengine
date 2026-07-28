package renderer

import (
	"encoding/binary"
	"fmt"
	"unsafe"

	"github.com/vkngwrapper/core/v3/core1_0"
	"github.com/vkngwrapper/extensions/v3/khr_surface"
)

// queueFamilyIndices tracks the graphics and present queue family indices
// discovered during physical device selection.
type queueFamilyIndices struct {
	graphicsFamily int
	presentFamily  int
	hasGraphics    bool
	hasPresent     bool
}

// isComplete returns true when both graphics and present families have been found.
func (q queueFamilyIndices) isComplete() bool {
	return q.hasGraphics && q.hasPresent
}

// findQueueFamilies enumerates a physical device's queue families and returns
// the first indices that support graphics operations and surface presentation.
func findQueueFamilies(instanceDriver core1_0.CoreInstanceDriver, physicalDevice core1_0.PhysicalDevice, surfaceExt khr_surface.ExtensionDriver, surface khr_surface.Surface) queueFamilyIndices {
	var indices queueFamilyIndices

	families := instanceDriver.GetPhysicalDeviceQueueFamilyProperties(physicalDevice)
	for i, family := range families {
		if family.QueueFlags&core1_0.QueueGraphics != 0 {
			indices.graphicsFamily = i
			indices.hasGraphics = true
		}

		supported, _, err := surfaceExt.GetPhysicalDeviceSurfaceSupport(surface, physicalDevice, i)
		if err == nil && supported {
			indices.presentFamily = i
			indices.hasPresent = true
		}

		if indices.isComplete() {
			break
		}
	}

	return indices
}

// clampInt constrains val to the range [min, max].
func clampInt(val, min, max int) int {
	if val < min {
		return min
	}
	if val > max {
		return max
	}
	return val
}

// bytesToUint32Slice reinterprets a byte slice as a uint32 slice for SPIR-V shader code.
func bytesToUint32Slice(data []byte) []uint32 {
	if len(data) == 0 {
		return nil
	}
	count := len(data) / 4
	result := make([]uint32, count)
	for i := 0; i < count; i++ {
		result[i] = binary.LittleEndian.Uint32(data[i*4 : (i+1)*4])
	}
	return result
}

// sizeOf returns the byte size of a type via unsafe.
func sizeOf[T any]() int {
	var zero T
	return int(unsafe.Sizeof(zero))
}

// findDepthFormat returns a supported depth format, preferring D32_SFLOAT.
func findDepthFormat(instanceDriver core1_0.CoreInstanceDriver, physicalDevice core1_0.PhysicalDevice) (core1_0.Format, error) {
	candidates := []core1_0.Format{
		core1_0.FormatD32SignedFloat,
		core1_0.FormatD32SignedFloatS8UnsignedInt,
		core1_0.FormatD24UnsignedNormalizedS8UnsignedInt,
	}
	for _, format := range candidates {
		props := instanceDriver.GetPhysicalDeviceFormatProperties(physicalDevice, format)
		if props.OptimalTilingFeatures&core1_0.FormatFeatureDepthStencilAttachment != 0 {
			return format, nil
		}
	}
	return 0, fmt.Errorf("failed to find supported depth format")
}

// findMemoryType returns the index of a suitable memory type given requirements and desired properties.
func findMemoryType(instanceDriver core1_0.CoreInstanceDriver, physicalDevice core1_0.PhysicalDevice, typeFilter uint32, properties core1_0.MemoryPropertyFlags) (int, error) {
	memProps := instanceDriver.GetPhysicalDeviceMemoryProperties(physicalDevice)
	for i, mt := range memProps.MemoryTypes {
		if typeFilter&(1<<i) != 0 && mt.PropertyFlags&properties == properties {
			return i, nil
		}
	}
	return 0, fmt.Errorf("failed to find suitable memory type")
}
