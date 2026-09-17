package renderer

import (
	"fmt"
	"log"

	core "github.com/vkngwrapper/core/v3"
	"github.com/vkngwrapper/core/v3/common"
	"github.com/vkngwrapper/core/v3/core1_0"
	"github.com/vkngwrapper/extensions/v3/ext_debug_utils"
	"github.com/vkngwrapper/extensions/v3/khr_portability_enumeration"

	"github.com/derekmwright/glyphengine/window"
)

// defaultApplicationName is reported to Vulkan when the caller does not set one
// with WithApplicationName. Driver tools, GPU profilers, and vendor control
// panels display it, so a real game should override it.
const defaultApplicationName = "GlyphEngine Application"

// createInstance initializes the Vulkan driver from GLFW's proc address and
// creates a Vulkan 1.0 instance with the extensions required by GLFW.
//
// wantValidation asks for the Khronos validation layer. The returned
// gotValidation reports whether it was actually enabled — a machine with only
// the Vulkan runtime has no validation layer, and that must degrade to a
// warning rather than a startup failure.
func createInstance(w *window.Window, appName string, appVersion common.Version, wantValidation bool) (driver core1_0.CoreInstanceDriver, gotValidation bool, err error) {
	procAddr := window.GetVulkanProcAddr()

	globalDriver, err := core.CreateDriverFromProcAddr(procAddr)
	if err != nil {
		return nil, false, fmt.Errorf("load vulkan driver: %w", err)
	}

	extensions := w.GetRequiredInstanceExtensions()
	log.Printf("GLFW required extensions: %v", extensions)

	var layers []string
	if wantValidation && resolveValidation(globalDriver) {
		gotValidation = true
		layers = append(layers, validationLayerName)
		extensions = append(extensions, ext_debug_utils.ExtensionName)
		log.Printf("Vulkan validation layer enabled")
	}

	// Portability drivers -- MoltenVK on macOS -- implement a subset of Vulkan
	// and identify themselves as such, and since Vulkan SDK 1.3.216 the loader
	// hides them unless the application says it can cope with one.
	//
	// The failure this prevents is not a failure here. CreateInstance succeeds
	// either way; what happens without the opt-in is that
	// vkEnumeratePhysicalDevices returns *zero* devices, so it surfaces later
	// at device selection as "no Vulkan-capable GPU found" on a machine with a
	// perfectly good GPU. That is a bad first five minutes for anyone trying
	// the engine on a Mac, and nothing in the message points at the cause.
	//
	// Conditional because requesting an extension the loader does not advertise
	// is itself an instance-creation error.
	//
	// Note that the condition is usually true everywhere, not just on macOS:
	// this is a loader extension, and a Windows machine with a current Vulkan
	// loader advertises it too, so the flag gets set there as well. That is
	// harmless -- the flag only asks that portable devices be *included* in
	// enumeration, and on a machine with none the device list is identical.
	// Measured on Windows: the same device is selected, the device-level
	// portability subset below is correctly not enabled because no conformant
	// driver advertises it, and `task validate` stays silent.
	var flags core1_0.InstanceCreateFlags
	if available, _, err := globalDriver.AvailableExtensions(); err != nil {
		log.Printf("instance extension enumeration failed (%v); continuing without portability enumeration", err)
	} else if _, ok := available[khr_portability_enumeration.ExtensionName]; ok {
		extensions = append(extensions, khr_portability_enumeration.ExtensionName)
		flags |= khr_portability_enumeration.InstanceCreateEnumeratePortability
		log.Printf("Portability enumeration enabled (%s); portable devices will be listed",
			khr_portability_enumeration.ExtensionName)
	}

	if appName == "" {
		appName = defaultApplicationName
	}
	if appVersion == 0 {
		appVersion = common.CreateVersion(0, 1, 0)
	}

	instanceDriver, _, err := globalDriver.CreateInstance(nil, core1_0.InstanceCreateInfo{
		ApplicationName:       appName,
		ApplicationVersion:    appVersion,
		EngineName:            "GlyphEngine",
		EngineVersion:         common.CreateVersion(0, 1, 0),
		APIVersion:            common.Vulkan1_0,
		Flags:                 flags,
		EnabledExtensionNames: extensions,
		EnabledLayerNames:     layers,
	})
	if err != nil {
		return nil, false, fmt.Errorf("create vulkan instance: %w", err)
	}

	log.Printf("Vulkan instance created for %q", appName)
	return instanceDriver, gotValidation, nil
}
