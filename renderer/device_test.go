package renderer

import (
	"errors"
	"github.com/vkngwrapper/core/v3/core1_0"
	"github.com/vkngwrapper/extensions/v3/khr_dynamic_rendering"
	properties2 "github.com/vkngwrapper/extensions/v3/khr_get_physical_device_properties2"
	"strings"
	"testing"
)

type dynamicFeatureQuery struct {
	properties2.ExtensionDriver
	supported bool
	err       error
}

func (q dynamicFeatureQuery) GetPhysicalDeviceFeatures2(_ core1_0.PhysicalDevice, out *properties2.PhysicalDeviceFeatures2) error {
	out.NextOutData.Next.(*khr_dynamic_rendering.PhysicalDeviceDynamicRenderingFeatures).DynamicRendering = q.supported
	return q.err
}
func TestDynamicRenderingRequired(t *testing.T) {
	names := []string{"VK_KHR_swapchain", "VK_KHR_dynamic_rendering", "VK_KHR_depth_stencil_resolve", "VK_KHR_create_renderpass2", "VK_KHR_multiview", "VK_KHR_maintenance2"}
	available := map[string]*core1_0.ExtensionProperties{}
	for _, name := range names {
		available[name] = &core1_0.ExtensionProperties{}
	}
	props := &core1_0.PhysicalDeviceProperties{DriverName: "test driver"}
	got, err := dynamicRenderingExtensions(available, props)
	if err != nil || len(got) != len(names) {
		t.Fatalf("dependency closure: %v %v", got, err)
	}
	for _, name := range names {
		delete(available, name)
		_, err := dynamicRenderingExtensions(available, props)
		if err == nil || !strings.Contains(err.Error(), name) || !strings.Contains(err.Error(), props.DriverName) {
			t.Fatalf("missing %s: %v", name, err)
		}
		available[name] = &core1_0.ExtensionProperties{}
	}
	for _, query := range []properties2.ExtensionDriver{nil, dynamicFeatureQuery{}} {
		err := requireDynamicRendering(query, core1_0.PhysicalDevice{}, props.DriverName)
		if err == nil || !strings.Contains(err.Error(), "VK_KHR_dynamic_rendering") || !strings.Contains(err.Error(), props.DriverName) {
			t.Fatalf("unsupported feature: %v", err)
		}
	}
	sentinel := errors.New("query failed")
	if err := requireDynamicRendering(dynamicFeatureQuery{err: sentinel}, core1_0.PhysicalDevice{}, props.DriverName); !errors.Is(err, sentinel) {
		t.Fatalf("query error: %v", err)
	}
	if err := requireDynamicRendering(dynamicFeatureQuery{supported: true}, core1_0.PhysicalDevice{}, props.DriverName); err != nil {
		t.Fatal(err)
	}
}
