package renderer

import (
	"fmt"
	"log"

	core "github.com/vkngwrapper/core/v3"
	"github.com/vkngwrapper/core/v3/common"
	"github.com/vkngwrapper/core/v3/core1_0"
	"github.com/vkngwrapper/extensions/v3/ext_debug_utils"

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
		EnabledExtensionNames: extensions,
		EnabledLayerNames:     layers,
	})
	if err != nil {
		return nil, false, fmt.Errorf("create vulkan instance: %w", err)
	}

	log.Printf("Vulkan instance created for %q", appName)
	return instanceDriver, gotValidation, nil
}
