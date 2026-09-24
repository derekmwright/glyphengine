// Command swapchainchurn measures initial swapchain creation without building
// pipelines or rendering. Each iteration owns a fresh window and surface.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"runtime"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/derekmwright/glyphengine/window"
	core "github.com/vkngwrapper/core/v3"
	"github.com/vkngwrapper/core/v3/common"
	"github.com/vkngwrapper/core/v3/core1_0"
	"github.com/vkngwrapper/extensions/v3/ext_debug_utils"
	"github.com/vkngwrapper/extensions/v3/khr_dynamic_rendering"
	"github.com/vkngwrapper/extensions/v3/khr_portability_enumeration"
	"github.com/vkngwrapper/extensions/v3/khr_portability_subset"
	"github.com/vkngwrapper/extensions/v3/khr_surface"
	surface_loader "github.com/vkngwrapper/extensions/v3/khr_surface/loader"
	"github.com/vkngwrapper/extensions/v3/khr_swapchain"
)

func init() { runtime.LockOSThread() }

func main() {
	n := flag.Int("n", 200, "number of window/swapchain lifetimes")
	validate := flag.Bool("validate", false, "require the Khronos validation layer")
	delay := flag.Int("delay", 0, "milliseconds between iterations (after teardown)")
	visible := flag.Bool("visible", false, "show windows instead of hiding them")
	shared := flag.Bool("shared", false, "reuse one Vulkan instance and device")
	flag.Parse()
	if *n < 1 || *delay < 0 || flag.NArg() != 0 {
		log.Fatal("require -n > 0, -delay >= 0, and no positional arguments")
	}
	// Make the comparison explicit even when launched from Task's environment.
	if err := os.Setenv("GLYPHENGINE_BACKGROUND", "0"); err != nil {
		log.Fatal(err)
	}
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
	log.Printf("CHURN n=%d validate=%v delay_ms=%d visible=%v shared=%v", *n, *validate, *delay, *visible, *shared)
	if run(*n, *validate, *delay, *visible, *shared) {
		os.Exit(1)
	}
}

type context struct {
	instance          core1_0.CoreInstanceDriver
	device            core1_0.CoreDeviceDriver
	physical          core1_0.PhysicalDevice
	surfaces          khr_surface.ExtensionDriver
	graphics, present int
	debug             ext_debug_utils.ExtensionDriver
	messenger         ext_debug_utils.DebugUtilsMessenger
	messages          atomic.Int64
}

func (c *context) destroy() {
	if c.device != nil {
		c.device.DestroyDevice(nil)
		c.device = nil
	}
	if c.debug != nil {
		c.debug.DestroyDebugUtilsMessenger(c.messenger, nil)
		c.debug = nil
	}
	if c.instance != nil {
		c.instance.DestroyInstance(nil)
		c.instance = nil
	}
}

func run(n int, validate bool, delay int, visible, shared bool) bool {
	// A shared instance retains function pointers from GLFW's loaded library.
	// Keep that library loaded until AFTER the shared instance is destroyed.
	if shared {
		if err := window.Init(); err != nil {
			log.Print(err)
			return true
		}
		defer window.Terminate()
	}
	c := &context{}
	failures, first, attempts, swapFailures, capFailures := 0, 0, 0, 0, 0
	var firstError string
	var messages int64
	start := time.Now()
	for i := 1; i <= n; i++ {
		if !shared {
			c = &context{}
		}
		stage, attempted, err := iteration(c, i, validate, visible, shared)
		if attempted {
			attempts++
		}
		if err != nil {
			failures++
			if stage == "swapchain" {
				swapFailures++
			}
			if stage == "capabilities" {
				capFailures++
			}
			if first == 0 {
				first, firstError = i, err.Error()
			}
			log.Printf("FAIL iteration=%d stage=%s error=%v", i, stage, err)
		}
		if !shared {
			messages += c.messages.Load()
		}
		if i < n && delay > 0 {
			time.Sleep(time.Duration(delay) * time.Millisecond)
		}
	}
	if shared {
		c.destroy()
		messages = c.messages.Load()
	}
	log.Printf("SUMMARY iterations=%d failures=%d rate=%.6f%% first_failure=%d first_error=%q swapchain_attempts=%d swapchain_failures=%d capability_failures=%d validation_messages=%d elapsed=%s",
		n, failures, 100*float64(failures)/float64(n), first, firstError, attempts, swapFailures, capFailures, messages, time.Since(start))
	return failures != 0 || messages != 0
}

func iteration(c *context, i int, validate, visible, shared bool) (stage string, attempted bool, err error) {
	var opts []window.Option
	if !visible {
		opts = append(opts, window.WithBackground())
	}
	w, err := window.New(1280, 720, "GlyphEngine swapchain churn", opts...)
	if err != nil {
		return "window", false, err
	}
	defer w.Destroy()
	if !shared {
		defer c.destroy()
	}
	if c.instance == nil {
		if err := c.createInstance(w, validate); err != nil {
			return "instance", false, err
		}
	}
	handle, err := w.CreateVulkanSurface(unsafe.Pointer(c.instance.Instance().Handle()))
	if err != nil {
		return "surface", false, err
	}
	surface, err := c.surfaces.CreateSurfaceFromHandle(surface_loader.VkSurfaceKHR(handle))
	if err != nil {
		return "surface", false, err
	}
	defer c.surfaces.DestroySurface(surface, nil)
	if c.device == nil {
		if err := c.createDevice(surface); err != nil {
			return "device", false, err
		}
	}
	ok, result, err := c.surfaces.GetPhysicalDeviceSurfaceSupport(surface, c.physical, c.present)
	if err != nil {
		return "support", false, vkError("vkGetPhysicalDeviceSurfaceSupportKHR", result, err)
	}
	if !ok {
		return "support", false, fmt.Errorf("present queue does not support new surface")
	}
	caps, result, err := c.surfaces.GetPhysicalDeviceSurfaceCapabilities(surface, c.physical)
	log.Printf("CAPS iteration=%d result=%d (%s) capabilities=%+v error=%v", i, result, result, caps, err)
	if err != nil {
		return "capabilities", false, vkError("vkGetPhysicalDeviceSurfaceCapabilitiesKHR", result, err)
	}
	formats, result, err := c.surfaces.GetPhysicalDeviceSurfaceFormats(surface, c.physical)
	if err != nil {
		return "formats", false, vkError("vkGetPhysicalDeviceSurfaceFormatsKHR", result, err)
	}
	modes, result, err := c.surfaces.GetPhysicalDeviceSurfacePresentModes(surface, c.physical)
	if err != nil {
		return "modes", false, vkError("vkGetPhysicalDeviceSurfacePresentModesKHR", result, err)
	}
	if len(formats) == 0 || len(modes) == 0 {
		return "capabilities", false, fmt.Errorf("empty formats or present modes")
	}
	// Mirror renderer/swapchain.go: sRGB preference, FIFO (default vsync),
	// min+1 images, opaque alpha, and TRANSFER_SRC only if advertised.
	format := formats[0]
	for _, f := range formats {
		if f.Format == core1_0.FormatB8G8R8A8SRGB && f.ColorSpace == khr_surface.ColorSpaceSRGBNonlinear {
			format = f
			break
		}
	}
	extent := caps.CurrentExtent
	if extent.Width == -1 {
		width, height := w.GetFramebufferSize()
		extent = core1_0.Extent2D{
			Width:  max(caps.MinImageExtent.Width, min(width, caps.MaxImageExtent.Width)),
			Height: max(caps.MinImageExtent.Height, min(height, caps.MaxImageExtent.Height)),
		}
	}
	usage := core1_0.ImageUsageColorAttachment
	if caps.SupportedUsageFlags&core1_0.ImageUsageTransferSrc != 0 {
		usage |= core1_0.ImageUsageTransferSrc
	}
	count := caps.MinImageCount + 1
	if caps.MaxImageCount > 0 {
		count = min(count, caps.MaxImageCount)
	}
	if extent.Width <= 0 || extent.Height <= 0 || caps.SupportedUsageFlags&core1_0.ImageUsageColorAttachment == 0 || caps.SupportedCompositeAlpha&khr_surface.CompositeAlphaOpaque == 0 {
		return "capabilities", false, fmt.Errorf("unusable surface capabilities: %+v", caps)
	}
	info := khr_swapchain.SwapchainCreateInfo{
		Surface:          surface,
		MinImageCount:    count,
		ImageFormat:      format.Format,
		ImageColorSpace:  format.ColorSpace,
		ImageExtent:      extent,
		ImageArrayLayers: 1,
		ImageUsage:       usage,
		PreTransform:     caps.CurrentTransform,
		CompositeAlpha:   khr_surface.CompositeAlphaOpaque,
		PresentMode:      khr_surface.PresentModeFIFO,
		Clipped:          true,
		ImageSharingMode: core1_0.SharingModeExclusive,
	}
	if c.graphics != c.present {
		info.ImageSharingMode = core1_0.SharingModeConcurrent
		info.QueueFamilyIndices = []int{c.graphics, c.present}
	}
	ext := khr_swapchain.CreateExtensionDriverFromCoreDriver(c.device)
	if ext == nil {
		return "extension", false, fmt.Errorf("VK_KHR_swapchain driver unavailable")
	}
	sc, result, err := ext.CreateSwapchain(nil, info)
	log.Printf("CREATE iteration=%d result=%d (%s) extent=%dx%d format=%v mode=%v usage=%v error=%v", i, result, result, extent.Width, extent.Height, format.Format, info.PresentMode, usage, err)
	if err != nil {
		return "swapchain", true, vkError("vkCreateSwapchainKHR", result, err)
	}
	defer ext.DestroySwapchain(sc, nil)
	images, result, err := ext.GetSwapchainImages(sc)
	if err != nil {
		return "images", true, vkError("vkGetSwapchainImagesKHR", result, err)
	}
	// Match the engine's swapchain image-view lifetimes too, without pipelines.
	for _, img := range images {
		view, result, err := c.device.CreateImageView(nil, core1_0.ImageViewCreateInfo{
			Image:    img,
			ViewType: core1_0.ImageViewType2D,
			Format:   format.Format,
			SubresourceRange: core1_0.ImageSubresourceRange{
				AspectMask: core1_0.ImageAspectColor,
				LevelCount: 1,
				LayerCount: 1,
			},
		})
		if err != nil {
			return "image-view", true, vkError("vkCreateImageView", result, err)
		}
		defer c.device.DestroyImageView(view, nil)
	}
	w.PollEvents()
	return "", true, nil
}

func vkError(call string, result common.VkResult, err error) error {
	return fmt.Errorf("%s: VkResult=%d (%s): %w", call, result, result, err)
}

func (c *context) createInstance(w *window.Window, validate bool) error {
	if !window.VulkanSupported() {
		return fmt.Errorf("Vulkan loader unavailable")
	}
	global, err := core.CreateDriverFromProcAddr(window.GetVulkanProcAddr())
	if err != nil {
		return err
	}
	exts := append(w.GetRequiredInstanceExtensions(), "VK_KHR_get_physical_device_properties2")
	available, result, err := global.AvailableExtensions()
	if err != nil {
		return vkError("vkEnumerateInstanceExtensionProperties", result, err)
	}
	var flags core1_0.InstanceCreateFlags
	if _, ok := available[khr_portability_enumeration.ExtensionName]; ok {
		exts = append(exts, khr_portability_enumeration.ExtensionName)
		flags = khr_portability_enumeration.InstanceCreateEnumeratePortability
	}
	var layers []string
	if validate {
		layers = []string{"VK_LAYER_KHRONOS_validation"}
		exts = append(exts, ext_debug_utils.ExtensionName)
	}
	// Same API and extension baseline as renderer.createInstance. A missing
	// requested validation layer is an error here, never a false clean sample.
	c.instance, result, err = global.CreateInstance(nil, core1_0.InstanceCreateInfo{
		ApplicationName:       "GlyphEngine swapchain churn",
		EngineName:            "GlyphEngine",
		ApplicationVersion:    common.CreateVersion(0, 1, 0),
		EngineVersion:         common.CreateVersion(0, 1, 0),
		APIVersion:            common.Vulkan1_0,
		Flags:                 flags,
		EnabledExtensionNames: exts,
		EnabledLayerNames:     layers,
	})
	if err != nil {
		return vkError("vkCreateInstance", result, err)
	}
	c.surfaces = khr_surface.CreateExtensionDriverFromCoreDriver(c.instance)
	if c.surfaces == nil {
		c.destroy()
		return fmt.Errorf("VK_KHR_surface driver unavailable")
	}
	if validate {
		debug := ext_debug_utils.CreateExtensionDriverFromCoreDriver(c.instance)
		if debug == nil {
			c.destroy()
			return fmt.Errorf("debug-utils driver unavailable")
		}
		messenger, result, err := debug.CreateDebugUtilsMessenger(nil, ext_debug_utils.DebugUtilsMessengerCreateInfo{
			MessageSeverity: ext_debug_utils.SeverityWarning | ext_debug_utils.SeverityError,
			MessageType:     ext_debug_utils.TypeGeneral | ext_debug_utils.TypeValidation | ext_debug_utils.TypePerformance,
			UserCallback: func(_ ext_debug_utils.DebugUtilsMessageTypeFlags, severity ext_debug_utils.DebugUtilsMessageSeverityFlags, data *ext_debug_utils.DebugUtilsMessengerCallbackData) bool {
				c.messages.Add(1)
				log.Printf("VULKAN %v: %s: %s", severity, data.MessageIDName, data.Message)
				return false
			},
		})
		if err != nil {
			c.destroy()
			return vkError("vkCreateDebugUtilsMessengerEXT", result, err)
		}
		c.debug, c.messenger = debug, messenger
		log.Print("Vulkan validation layer enabled")
	}
	return nil
}

func (c *context) createDevice(surface khr_surface.Surface) error {
	devices, result, err := c.instance.EnumeratePhysicalDevices()
	if err != nil {
		return vkError("vkEnumeratePhysicalDevices", result, err)
	}
	for _, physical := range devices {
		graphics, present := -1, -1
		for i, q := range c.instance.GetPhysicalDeviceQueueFamilyProperties(physical) {
			if q.QueueFlags&core1_0.QueueGraphics != 0 {
				graphics = i
			}
			ok, _, err := c.surfaces.GetPhysicalDeviceSurfaceSupport(surface, physical, i)
			if err == nil && ok {
				present = i
			}
			if graphics >= 0 && present >= 0 {
				break
			}
		}
		if graphics < 0 || present < 0 {
			continue
		}
		available, _, err := c.instance.EnumerateDeviceExtensionProperties(physical)
		if err != nil {
			return err
		}
		// Match renderer.createLogicalDevice, including the dynamic-rendering
		// dependency closure, despite never creating a graphics pipeline here.
		exts := []string{
			khr_swapchain.ExtensionName, khr_dynamic_rendering.ExtensionName,
			"VK_KHR_depth_stencil_resolve", "VK_KHR_create_renderpass2",
			"VK_KHR_multiview", "VK_KHR_maintenance2",
		}
		usable := true
		for _, name := range exts {
			if _, ok := available[name]; !ok {
				usable = false
			}
		}
		if !usable {
			continue
		}
		if _, ok := available[khr_portability_subset.ExtensionName]; ok {
			exts = append(exts, khr_portability_subset.ExtensionName)
		}
		queues := []core1_0.DeviceQueueCreateInfo{{QueueFamilyIndex: graphics, QueuePriorities: []float32{1}}}
		if graphics != present {
			queues = append(queues, core1_0.DeviceQueueCreateInfo{QueueFamilyIndex: present, QueuePriorities: []float32{1}})
		}
		features := c.instance.GetPhysicalDeviceFeatures(physical)
		device, result, err := c.instance.CreateDevice(physical, nil, core1_0.DeviceCreateInfo{
			NextOptions: common.NextOptions{
				Next: khr_dynamic_rendering.PhysicalDeviceDynamicRenderingFeatures{DynamicRendering: true},
			},
			QueueCreateInfos:      queues,
			EnabledExtensionNames: exts,
			EnabledFeatures: &core1_0.PhysicalDeviceFeatures{
				SamplerAnisotropy:                 features.SamplerAnisotropy,
				ShaderStorageImageExtendedFormats: features.ShaderStorageImageExtendedFormats,
			},
		})
		if err != nil {
			return vkError("vkCreateDevice", result, err)
		}
		c.device, c.physical, c.graphics, c.present = device, physical, graphics, present
		props, err := c.instance.GetPhysicalDeviceProperties(physical)
		if err != nil {
			return err
		}
		log.Printf("DEVICE name=%q driver=%v api=%v graphics=%d present=%d", props.DriverName, props.DriverVersion, props.APIVersion, graphics, present)
		return nil
	}
	return fmt.Errorf("no suitable graphics/present device with engine extensions")
}
