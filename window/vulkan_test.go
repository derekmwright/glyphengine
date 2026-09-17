package window

import (
	"testing"
)

// The check this guards is a gate in front of GetVulkanProcAddr, and the whole
// point of it is that the two agree: GLFW returns NULL from the proc-address
// call in exactly the case VulkanSupported reports false. If they can disagree,
// the gate either rejects a machine that would have worked or lets a null
// through to the driver constructor, which is the failure it exists to prevent.
//
// This holds on any machine, with or without a loader — it asserts the relation
// rather than either answer, so it is meaningful on the CI runners that have no
// Vulkan as well as on a developer box that does.
func TestVulkanSupportedAgreesWithTheProcAddress(t *testing.T) {
	requireGLFW(t)

	supported := VulkanSupported()
	procAddr := GetVulkanProcAddr()

	switch {
	case supported && procAddr == nil:
		t.Error("VulkanSupported says yes but GetVulkanProcAddr returned NULL; " +
			"the gate would let a null reach the driver constructor")
	case !supported && procAddr != nil:
		t.Error("VulkanSupported says no but GetVulkanProcAddr returned a pointer; " +
			"the gate would reject a machine that can run")
	}

	t.Logf("vulkan loader present: %v", supported)
}
