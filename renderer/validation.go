package renderer

import (
	"log"
	"os"
	"strconv"

	core1_0 "github.com/vkngwrapper/core/v3/core1_0"
	"github.com/vkngwrapper/extensions/v3/ext_debug_utils"
)

// validationLayerName is the Khronos validation layer, shipped with the Vulkan
// SDK. It is not present on machines that only have a runtime installed.
const validationLayerName = "VK_LAYER_KHRONOS_validation"

// validationEnvVar force-enables validation without a code change, so
// `GLYPHENGINE_VALIDATION=1 task example:02-cube` works on any example.
// Setting it to 0 force-disables, overriding WithValidation.
const validationEnvVar = "GLYPHENGINE_VALIDATION"

// validationSetting resolves whether validation should be on, combining the
// WithValidation option with the environment override.
//
// Validation is off by default. It costs real frame time and, more importantly,
// a missing SDK would otherwise turn every shipped game into a warning at
// startup.
func validationSetting(optValue bool) bool {
	if raw, ok := os.LookupEnv(validationEnvVar); ok {
		if v, err := strconv.ParseBool(raw); err == nil {
			return v
		}
		log.Printf("%s=%q is not a boolean; ignoring", validationEnvVar, raw)
	}
	return optValue
}

// resolveValidation reports whether validation can actually be enabled, and
// logs why not when it cannot. A machine with only the Vulkan runtime has no
// validation layer, which must degrade to a warning rather than a failure —
// otherwise a debug build refuses to start on a player's machine.
func resolveValidation(driver core1_0.GlobalDriver) bool {
	layers, _, err := driver.AvailableLayers()
	if err != nil {
		log.Printf("validation requested but layer enumeration failed (%v); continuing without it", err)
		return false
	}
	if _, ok := layers[validationLayerName]; !ok {
		log.Printf("validation requested but %s is not installed; install the Vulkan SDK. Continuing without it.", validationLayerName)
		return false
	}

	exts, _, err := driver.AvailableExtensions()
	if err != nil {
		log.Printf("validation requested but extension enumeration failed (%v); continuing without it", err)
		return false
	}
	if _, ok := exts[ext_debug_utils.ExtensionName]; !ok {
		log.Printf("validation requested but %s is unavailable; continuing without it", ext_debug_utils.ExtensionName)
		return false
	}

	return true
}

// createDebugMessenger wires the validation layer's output into the Go logger.
// Without a messenger the layer's messages go to a platform-specific default
// (OutputDebugString on Windows) that a terminal never sees, which reads as
// "validation is not working".
func createDebugMessenger(instanceDriver core1_0.CoreInstanceDriver) (ext_debug_utils.ExtensionDriver, ext_debug_utils.DebugUtilsMessenger, error) {
	var noMessenger ext_debug_utils.DebugUtilsMessenger

	debugExt := ext_debug_utils.CreateExtensionDriverFromCoreDriver(instanceDriver)
	if debugExt == nil {
		log.Printf("validation enabled but %s driver unavailable; layer output will not be captured", ext_debug_utils.ExtensionName)
		return nil, noMessenger, nil
	}

	messenger, _, err := debugExt.CreateDebugUtilsMessenger(nil, ext_debug_utils.DebugUtilsMessengerCreateInfo{
		// Verbose and Info are deliberately excluded: they are overwhelmingly
		// per-frame chatter, and anything actionable arrives as a warning.
		MessageSeverity: ext_debug_utils.SeverityWarning | ext_debug_utils.SeverityError,
		MessageType: ext_debug_utils.TypeGeneral |
			ext_debug_utils.TypeValidation |
			ext_debug_utils.TypePerformance,
		UserCallback: logValidationMessage,
	})
	if err != nil {
		return nil, noMessenger, err
	}
	return debugExt, messenger, nil
}

// logValidationMessage formats one validation message. Returning false tells
// the layer to let the offending call proceed, which is what you want: the
// message is the point, not aborting the program.
func logValidationMessage(
	msgType ext_debug_utils.DebugUtilsMessageTypeFlags,
	severity ext_debug_utils.DebugUtilsMessageSeverityFlags,
	data *ext_debug_utils.DebugUtilsMessengerCallbackData,
) bool {
	label := "VULKAN WARNING"
	if severity&ext_debug_utils.SeverityError != 0 {
		label = "VULKAN ERROR"
	}

	name := data.MessageIDName
	if name == "" {
		name = "unnamed"
	}
	log.Printf("%s [%s] %s: %s", label, msgType, name, data.Message)

	// Objects carry the handles involved, which is usually what turns "some
	// image view is wrong" into "this image view is wrong".
	for _, obj := range data.Objects {
		if obj.ObjectName != "" {
			log.Printf("    object: %s %q (handle 0x%x)", obj.ObjectType, obj.ObjectName, obj.ObjectHandle)
		} else {
			log.Printf("    object: %s (handle 0x%x)", obj.ObjectType, obj.ObjectHandle)
		}
	}

	return false
}
