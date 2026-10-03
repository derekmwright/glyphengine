package renderer

import (
	"errors"
	"fmt"
	"log"

	"github.com/vkngwrapper/core/v3/common"
	"github.com/vkngwrapper/core/v3/core1_0"
	"github.com/vkngwrapper/extensions/v3/ext_descriptor_indexing"
	"github.com/vkngwrapper/extensions/v3/khr_acceleration_structure"
	"github.com/vkngwrapper/extensions/v3/khr_buffer_device_address"
	"github.com/vkngwrapper/extensions/v3/khr_deferred_host_operations"
	"github.com/vkngwrapper/extensions/v3/khr_driver_properties"
	"github.com/vkngwrapper/extensions/v3/khr_get_physical_device_properties2"
	"github.com/vkngwrapper/extensions/v3/khr_maintenance3"
	"github.com/vkngwrapper/extensions/v3/khr_portability_subset"
	"github.com/vkngwrapper/extensions/v3/khr_ray_query"
	"github.com/vkngwrapper/extensions/v3/khr_shader_float_controls"
	"github.com/vkngwrapper/extensions/v3/khr_spirv_1_4"
)

// Capabilities is what the device actually granted this renderer, fixed once
// during New and read by value afterwards.
//
// It exists because the engine negotiates several things down without saying
// so. MSAA is halved until the device supports the count; anisotropy drops to
// zero; batched ranges fall back from one indirect draw to one draw per range.
// Every one of those produces the same picture, so the engine decides it and
// the game never needs to know -- except that a game choosing between two
// techniques whose results are NOT identical has to make the same kind of
// decision, and before this it had no way to ask. See
// docs/agents/game-loop.md for which side owns which fallback.
//
// A report rather than a set of accessors because it is the input to one
// branch taken once at load time, not a thing to poll: a game reads it after
// New, picks its path, and nothing in the renderer moves it afterwards.
type Capabilities struct {
	// RayQuery reports that this renderer can issue a ray query from a shader.
	//
	// False on every device today: detecting VK_KHR_ray_query's dependency
	// closure is only half of it, and createLogicalDevice enables none of
	// those extensions yet (#159). Reporting the device's answer alone would
	// tell a game it may ray-trace when the extensions are not enabled on the
	// device at all, and the shader would fail at pipeline creation instead of
	// here -- which is the opposite of the point of this struct.
	RayQuery bool

	// MultiDrawIndirect and DrawIndirectFirstInstance are the two features
	// batched mesh ranges need to collapse into a single indirect draw. Both
	// absent is a performance difference and nothing else: the same ranges are
	// drawn, one call each. See SetMeshRangeBatching.
	MultiDrawIndirect         bool
	DrawIndirectFirstInstance bool

	// MSAASamples is the negotiated count -- 1, 2, 4 or 8 -- not the count
	// WithMSAASamples asked for. Every pipeline is built against this one, so a
	// game comparing captures across machines wants to read it rather than
	// assume its request arrived.
	MSAASamples int

	// MaxAnisotropy is the sampler anisotropy the device allows, or 0 when it
	// does not support anisotropic filtering at all.
	MaxAnisotropy float32

	// DepthPrepass reports the mode WithDepthPrepass left this renderer in.
	// Unlike every other field here it is not a negotiation with the device --
	// it is a plain report of a choice the program made, and it is here because
	// the frame it produces is not the frame another mode produces in cost, only
	// in pixels: a game comparing captures or timings across builds wants to read
	// which one it got rather than assume its option arrived.
	//
	// DepthPrepassAuto reports as Auto whatever any individual frame decided.
	// The per-frame decision is RenderStats.PrepassActive, beside the estimate
	// it was taken from; this field is the build.
	DepthPrepass DepthPrepassMode

	// GPUTimestamps reports that the device can timestamp graphics work on the
	// queue this renderer submits to. Without it GPUTimings never becomes
	// valid, and an application pass asking to be Timed is refused with
	// ErrCapabilityUnavailable rather than silently never measured.
	GPUTimestamps bool

	// PipelineStatistics reports that the device supports
	// pipelineStatisticsQuery, which is what WithPipelineStatistics needs to
	// count fragment-shader invocations and post-clip primitives per pass.
	//
	// Reported whether or not this renderer asked for them, because it is a
	// property of the device rather than of the build: a game deciding whether a
	// diagnostic mode exists on this machine reads this, and the accessors say
	// separately whether this build is recording (ErrStatisticsNotEnabled) or the
	// device cannot (ErrCapabilityUnavailable). The feature is enabled on the
	// logical device whenever the device has it, which costs nothing -- a feature
	// bit is permission to create such a query pool, and the pool is what
	// WithPipelineStatistics creates.
	PipelineStatistics bool

	// PortabilitySubset reports that the device implements a subset of Vulkan
	// and VK_KHR_portability_subset was enabled for it -- MoltenVK, in
	// practice. Nothing in the engine behaves differently; it is here because
	// it is the single most useful thing to know when a frame looks wrong on
	// one machine and right on every other.
	PortabilitySubset bool

	// GPUName is the device name Vulkan 1.0 reports, e.g.
	// "AMD Radeon RX 7900 XTX".
	GPUName string

	// DriverName is the driver's own name, e.g. "radv" or "AMD proprietary
	// driver", from VK_KHR_driver_properties. Empty on a device that does not
	// advertise that extension, which core Vulkan 1.0 does not require -- the
	// engine's baseline. Unlike GPUName it is not part of the identity the
	// state trace folds, precisely because it can be absent.
	DriverName string

	// DriverVersion is the vendor's own driver version, and APIVersion the
	// Vulkan version the device supports. Both are packed Vulkan versions.
	DriverVersion uint32
	APIVersion    uint32

	// VendorID and DeviceID are the PCI-style pair that identifies the GPU
	// model independently of how any driver spells its name.
	VendorID uint32
	DeviceID uint32

	// deviceType and cacheUUID are not capabilities a game can act on, but
	// they are part of the device identity the state trace has to notice: the
	// pipeline cache UUID is the field that moves on a driver update without
	// the name or the version necessarily moving with it. They live here, in
	// the same value, so identHash -- and through it the trace's config= field
	// -- is a function of this struct and nothing else.
	deviceType int
	cacheUUID  [16]byte

	// timestampPeriod is nanoseconds per timestamp tick, which only the GPU
	// timer converts with. Here for the same reason: the timer's gate and
	// GPUTimestamps above then come from one negotiation rather than two
	// queries that can disagree.
	timestampPeriod float32
}

// Capabilities returns what the device granted this renderer. The result is a
// copy; mutating it changes nothing.
func (r *Renderer) Capabilities() Capabilities { return r.caps }

// ErrCapabilityUnavailable is wrapped by every constructor that needs an
// optional device capability and did not get it. The capability's name is in
// the message; errors.Is answers the branch:
//
//	p, err := r.CreateAppPass(desc)
//	if errors.Is(err, renderer.ErrCapabilityUnavailable) {
//		desc.Timed = false // measure it on a machine that can
//		p, err = r.CreateAppPass(desc)
//	}
//
// It is deliberately not returned for a fallback the engine takes on its own.
// Batched ranges without multiDrawIndirect draw the same geometry one call at
// a time and say so in Capabilities; nothing refuses, because nothing the
// caller asked for is missing.
var ErrCapabilityUnavailable = errors.New("capability unavailable")

// unavailable names the capability a constructor needed and could not get. The
// name is in the message as well as the sentinel so a log line says which one
// without the caller having to branch to find out.
func unavailable(capability string) error {
	return fmt.Errorf("%w: %s", ErrCapabilityUnavailable, capability)
}

// identHash folds the GPU and driver identity for the state trace's config=
// field.
//
// A driver update, or a different GPU picked on a hybrid machine, changes what
// every shader compiles to while leaving the simulation and the draw sequence
// identical, so two runs that straddled one would otherwise diff clean. Folded
// from Capabilities rather than from the driver's answers a second time: the
// report a game reads and the identity the trace compares are then the same
// values, and a field whose source drifts away from one shows up as the other
// disagreeing. TestCapabilitiesHashAgreesWithTrace holds that.
//
// GPUName is in and DriverName is not, because VendorID and DeviceID already
// pin the model and DriverName is empty on any device without
// VK_KHR_driver_properties -- a field that is sometimes absent cannot carry a
// difference.
func (c Capabilities) identHash() Hasher {
	return NewHash.
		Bytes([]byte(c.GPUName)).
		Uint64(uint64(c.DriverVersion)).
		Uint64(uint64(c.APIVersion)).
		Int(int(c.VendorID)).
		Int(int(c.DeviceID)).
		Int(c.deviceType).
		Bytes(c.cacheUUID[:])
}

// deviceAnswers is everything the selected physical device said about itself,
// gathered in one place so negotiateCapabilities below is a pure function of
// it and can be unit-tested against a device that offers less than the engine
// asks for -- which is the case no real GPU on hand reproduces.
type deviceAnswers struct {
	props      *core1_0.PhysicalDeviceProperties
	features   *core1_0.PhysicalDeviceFeatures
	families   []*core1_0.QueueFamilyProperties
	extensions map[string]*core1_0.ExtensionProperties

	// driverName is VK_KHR_driver_properties' answer, empty when the device
	// does not advertise the extension.
	driverName string

	graphicsFamily int
}

// queryDevice asks the physical device everything the renderer negotiates
// against, before the logical device exists.
//
// One place, because the same answers are needed three times over -- to build
// Capabilities, to choose the extensions and features createLogicalDevice
// enables, and to size the GPU timer -- and querying them separately is how
// the report and the thing it reports on drift apart.
func queryDevice(instanceDriver core1_0.CoreInstanceDriver, props2 khr_get_physical_device_properties2.ExtensionDriver, device core1_0.PhysicalDevice, graphicsFamily int) (deviceAnswers, error) {
	a := deviceAnswers{graphicsFamily: graphicsFamily}
	props, err := instanceDriver.GetPhysicalDeviceProperties(device)
	if err != nil {
		return a, fmt.Errorf("query device properties: %w", err)
	}
	extensions, _, err := instanceDriver.EnumerateDeviceExtensionProperties(device)
	if err != nil {
		return a, fmt.Errorf("enumerate device extensions: %w", err)
	}
	a.props, a.extensions = props, extensions
	a.features = instanceDriver.GetPhysicalDeviceFeatures(device)
	a.families = instanceDriver.GetPhysicalDeviceQueueFamilyProperties(device)

	// Optional, and asked for only when advertised: VkPhysicalDeviceProperties
	// has no driver name of its own in Vulkan 1.0 -- what vkngwrapper calls
	// DriverName there is deviceName, the GPU -- and chaining an out-structure
	// for an unsupported extension gets an untouched struct back at best.
	if _, ok := extensions[khr_driver_properties.ExtensionName]; ok && props2 != nil {
		driver := khr_driver_properties.PhysicalDeviceDriverProperties{}
		out := khr_get_physical_device_properties2.PhysicalDeviceProperties2{NextOutData: common.NextOutData{Next: &driver}}
		if err := props2.GetPhysicalDeviceProperties2(device, &out); err != nil {
			// Not fatal. The renderer runs without knowing the driver's name,
			// and a device that advertises the extension but fails the query
			// is a device whose report is one string short, not one that
			// cannot render.
			log.Printf("Driver name unavailable: %v", err)
		} else {
			a.driverName = driver.DriverName
		}
	}
	return a, nil
}

// rayQueryExtensions is VK_KHR_ray_query's dependency closure, which has to be
// present in full: a device advertising the ray query extension while missing
// VK_KHR_acceleration_structure cannot trace anything, and enabling a
// dependency's dependant without it fails device creation.
var rayQueryExtensions = []string{
	khr_ray_query.ExtensionName,
	khr_spirv_1_4.ExtensionName,
	khr_shader_float_controls.ExtensionName,
	khr_acceleration_structure.ExtensionName,
	khr_deferred_host_operations.ExtensionName,
	khr_buffer_device_address.ExtensionName,
	ext_descriptor_indexing.ExtensionName,
	khr_maintenance3.ExtensionName,
}

// rayQueryAvailable reports whether the device advertises every extension in
// that closure.
func rayQueryAvailable(extensions map[string]*core1_0.ExtensionProperties) bool {
	for _, name := range rayQueryExtensions {
		if _, ok := extensions[name]; !ok {
			return false
		}
	}
	return true
}

// rayQueryEnabled is whether createLogicalDevice enables the closure above. It
// does not, so the detection has nothing to report yet; see #159 and the
// RayQuery field.
const rayQueryEnabled = false

// negotiateCapabilities reduces what the engine asked for to what the device
// grants. Pure: every input is one of the device's own answers, and the only
// request in it is the MSAA count.
func negotiateCapabilities(a deviceAnswers, requested core1_0.SampleCountFlags) Capabilities {
	samples := requested
	// Halved until the device supports the count for colour AND depth. Both,
	// because every attachment in one render pass has to agree on the sample
	// count and the scene pass has a depth attachment.
	supported := a.props.Limits.FramebufferColorSampleCounts & a.props.Limits.FramebufferDepthSampleCounts
	for samples > core1_0.Samples1 && supported&samples == 0 {
		samples >>= 1
	}

	// Two separate capabilities have to hold for a timestamp, and a device can
	// have one without the other: the limit says timestamps work on graphics
	// and compute queues at all, and timestampValidBits says this particular
	// queue family writes meaningful bits. Checking only the first is how the
	// GPU timer silently returned zeros on hardware that reports support but
	// not on the queue being used.
	timestamps := a.props.Limits.TimestampComputeAndGraphics &&
		a.graphicsFamily < len(a.families) && a.families[a.graphicsFamily].TimestampValidBits != 0

	c := Capabilities{
		RayQuery:                  rayQueryEnabled && rayQueryAvailable(a.extensions),
		MultiDrawIndirect:         a.features.MultiDrawIndirect,
		DrawIndirectFirstInstance: a.features.DrawIndirectFirstInstance,
		MSAASamples:               int(samples),
		GPUTimestamps:             timestamps,
		PipelineStatistics:        a.features.PipelineStatisticsQuery,
		PortabilitySubset:         portabilitySubset(a.extensions),
		GPUName:                   a.props.DriverName, // deviceName; see queryDevice
		DriverName:                a.driverName,
		DriverVersion:             uint32(a.props.DriverVersion),
		APIVersion:                uint32(a.props.APIVersion),
		VendorID:                  a.props.VendorID,
		DeviceID:                  a.props.DeviceID,
		deviceType:                int(a.props.DriverType),
		cacheUUID:                 a.props.PipelineCacheUUID,
		timestampPeriod:           a.props.Limits.TimestampPeriod,
	}
	// Zero means unavailable rather than "no filtering": the limit is a
	// maximum and a device without the feature reports one anyway, so reading
	// it without the feature bit would ask samplers for anisotropy the device
	// never enabled.
	if a.features.SamplerAnisotropy {
		c.MaxAnisotropy = a.props.Limits.MaxSamplerAnisotropy
	}
	return c
}

// portabilitySubset reports whether VK_KHR_portability_subset is advertised,
// which is also the condition createLogicalDevice enables it under -- one
// source, so the report cannot claim a subset device the device was not
// created as.
func portabilitySubset(extensions map[string]*core1_0.ExtensionProperties) bool {
	_, ok := extensions[khr_portability_subset.ExtensionName]
	return ok
}

// adopt records what the device granted: the report a game reads, the sample
// count every pipeline is built with, and the identity the trace folds.
//
// One assignment point on purpose. msaaSamples is the typed copy forty call
// sites pass to pipeline creation, and deviceIdent is folded once here rather
// than per frame; both are derived from the report, so Capabilities cannot
// describe a device the pipelines were not built for.
// DepthPrepass is filled in here rather than in negotiateCapabilities, which is
// pure over the device's own answers: the prepass is a choice the program made
// before New ran, not something the device granted, and folding it into that
// function would make a report about the device partly a report about an option.
// It is deliberately outside identHash for the same reason DriverName is -- it
// is not part of the GPU's identity.
func (r *Renderer) adopt(c Capabilities) {
	c.DepthPrepass = r.depthPrepassMode
	r.caps = c
	r.msaaSamples = core1_0.SampleCountFlags(c.MSAASamples)
	r.deviceIdent = c.identHash()
}

// logCapabilities prints what the device granted against what was asked for.
// Only the sample count has a request to compare against; the rest is the
// device's answer, and worth a line each because every one of them changes a
// capture without changing any code.
func (c Capabilities) logCapabilities(requested core1_0.SampleCountFlags) {
	if core1_0.SampleCountFlags(c.MSAASamples) != requested {
		log.Printf("MSAA: requested %dx not supported, using %dx", requested, c.MSAASamples)
	} else {
		log.Printf("MSAA: %dx", c.MSAASamples)
	}
	if c.MaxAnisotropy > 0 {
		log.Printf("Anisotropic filtering: %gx", c.MaxAnisotropy)
	} else {
		log.Println("Anisotropic filtering unavailable")
	}
	if !c.GPUTimestamps {
		log.Println("GPU timing unavailable: the device does not timestamp graphics work, " +
			"or writes no timestamp bits on the queue this renderer uses")
	}
	if !c.MultiDrawIndirect || !c.DrawIndirectFirstInstance {
		log.Println("Multi-draw indirect unavailable: batched mesh ranges will use one draw per range")
	}
	if !c.PipelineStatistics {
		log.Println("Pipeline statistics unavailable: the device does not support pipelineStatisticsQuery, " +
			"so WithPipelineStatistics has nothing to count with")
	}
	if c.DepthPrepass != DepthPrepassOff {
		log.Printf("Depth prepass: %s", c.DepthPrepass)
	}
	if c.DriverName != "" {
		log.Printf("Driver: %s", c.DriverName)
	}
}
