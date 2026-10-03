package renderer

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/derekmwright/glyphengine/shaders"
	"github.com/vkngwrapper/core/v3/common"
	"github.com/vkngwrapper/core/v3/core1_0"
	"github.com/vkngwrapper/extensions/v3/khr_driver_properties"
	properties2 "github.com/vkngwrapper/extensions/v3/khr_get_physical_device_properties2"
	"github.com/vkngwrapper/extensions/v3/khr_portability_subset"
)

// capsTestInstance is a physical device that answers for itself and nothing
// else. It exists because the interesting device is the one no GPU on hand is:
// one that supports fewer MSAA samples than the engine asks for, has no
// multiDrawIndirect, no anisotropic filtering and no usable timestamps. Every
// such device has been reasoned about and never run against.
type capsTestInstance struct {
	resizeFakeInstanceDriver
	props      core1_0.PhysicalDeviceProperties
	limits     core1_0.PhysicalDeviceLimits
	features   core1_0.PhysicalDeviceFeatures
	families   []*core1_0.QueueFamilyProperties
	extensions []string
}

func (d *capsTestInstance) GetPhysicalDeviceProperties(core1_0.PhysicalDevice) (*core1_0.PhysicalDeviceProperties, error) {
	p := d.props
	p.Limits = &d.limits
	return &p, nil
}
func (d *capsTestInstance) GetPhysicalDeviceFeatures(core1_0.PhysicalDevice) *core1_0.PhysicalDeviceFeatures {
	f := d.features
	return &f
}
func (d *capsTestInstance) GetPhysicalDeviceQueueFamilyProperties(core1_0.PhysicalDevice) []*core1_0.QueueFamilyProperties {
	return d.families
}

// asMap is the same extension set EnumerateDeviceExtensionProperties returns,
// for a check that wants it without going through the driver.
func (d *capsTestInstance) asMap() map[string]*core1_0.ExtensionProperties {
	out, _, _ := d.EnumerateDeviceExtensionProperties(core1_0.PhysicalDevice{})
	return out
}

func (d *capsTestInstance) EnumerateDeviceExtensionProperties(core1_0.PhysicalDevice) (map[string]*core1_0.ExtensionProperties, common.VkResult, error) {
	out := map[string]*core1_0.ExtensionProperties{}
	for _, name := range d.extensions {
		out[name] = &core1_0.ExtensionProperties{}
	}
	return out, core1_0.VKSuccess, nil
}

// driverNameQuery stands in for VK_KHR_driver_properties, the same way
// dynamicFeatureQuery in device_test.go stands in for the dynamic-rendering
// feature query.
type driverNameQuery struct {
	properties2.ExtensionDriver
	name string
	err  error
}

func (q driverNameQuery) GetPhysicalDeviceProperties2(_ core1_0.PhysicalDevice, out *properties2.PhysicalDeviceProperties2) error {
	out.NextOutData.Next.(*khr_driver_properties.PhysicalDeviceDriverProperties).DriverName = q.name
	return q.err
}

// generousDevice is a device that grants everything the engine asks for, as the
// control the frugal cases below are read against.
func generousDevice() *capsTestInstance {
	return &capsTestInstance{
		props: core1_0.PhysicalDeviceProperties{
			DriverType: 2, DriverName: "Test Discrete GPU",
			DriverVersion: 0x00400000, APIVersion: 0x00401000,
			VendorID: 0x1002, DeviceID: 0x744c,
			PipelineCacheUUID: [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
		},
		limits: core1_0.PhysicalDeviceLimits{
			FramebufferColorSampleCounts: core1_0.Samples1 | core1_0.Samples2 | core1_0.Samples4 | core1_0.Samples8,
			FramebufferDepthSampleCounts: core1_0.Samples1 | core1_0.Samples2 | core1_0.Samples4 | core1_0.Samples8,
			MaxSamplerAnisotropy:         16,
			TimestampComputeAndGraphics:  true,
			TimestampPeriod:              0.5,
		},
		features: core1_0.PhysicalDeviceFeatures{
			SamplerAnisotropy: true, MultiDrawIndirect: true, DrawIndirectFirstInstance: true,
			PipelineStatisticsQuery: true,
		},
		families: []*core1_0.QueueFamilyProperties{
			{TimestampValidBits: 0}, {TimestampValidBits: 64},
		},
		extensions: append([]string{khr_driver_properties.ExtensionName}, rayQueryExtensions...),
	}
}

func negotiate(t *testing.T, d *capsTestInstance, props2 properties2.ExtensionDriver, graphicsFamily int, requested core1_0.SampleCountFlags) Capabilities {
	t.Helper()
	a, err := queryDevice(d, props2, core1_0.PhysicalDevice{}, graphicsFamily)
	if err != nil {
		t.Fatal(err)
	}
	return negotiateCapabilities(a, requested)
}

func TestCapabilitiesReportWhatTheDeviceGranted(t *testing.T) {
	// Family 1 is the one with timestamp bits, and family 0 is deliberately a
	// family that has none: a device can report TimestampComputeAndGraphics and
	// still write no bits on the queue this renderer submits to, and reading
	// only the limit is how the GPU timer returned zeros on real hardware.
	d := generousDevice()
	c := negotiate(t, d, driverNameQuery{name: "test driver 1.2"}, 1, core1_0.Samples8)
	if c.MSAASamples != 8 || c.MaxAnisotropy != 16 || !c.MultiDrawIndirect || !c.DrawIndirectFirstInstance ||
		!c.GPUTimestamps || !c.PipelineStatistics || c.PortabilitySubset {
		t.Fatalf("generous device: %+v", c)
	}
	if c.GPUName != "Test Discrete GPU" || c.DriverName != "test driver 1.2" ||
		c.DriverVersion != 0x00400000 || c.APIVersion != 0x00401000 ||
		c.VendorID != 0x1002 || c.DeviceID != 0x744c {
		t.Fatalf("identity: %+v", c)
	}
	if c.timestampPeriod != 0.5 || c.deviceType != 2 || c.cacheUUID[15] != 16 {
		t.Fatalf("hashed-only fields: %+v", c)
	}
	// The whole ray query closure is advertised and the field is still false,
	// because createLogicalDevice enables none of it (#159). A true here would
	// tell a game to write a ray query that cannot be compiled.
	if !rayQueryAvailable(d.asMap()) {
		t.Fatal("the fixture device does not advertise the closure; RayQuery below proves nothing")
	}
	if c.RayQuery {
		t.Fatal("RayQuery true with no extension enabled on the device")
	}

	// A device that offers less than was asked for: 2x MSAA against a request
	// of 8, no multi-draw, no anisotropy, no timestamps on the chosen queue,
	// portability subset, and no driver-properties extension.
	frugal := generousDevice()
	frugal.limits.FramebufferColorSampleCounts = core1_0.Samples1 | core1_0.Samples2 | core1_0.Samples4
	frugal.limits.FramebufferDepthSampleCounts = core1_0.Samples1 | core1_0.Samples2
	frugal.features = core1_0.PhysicalDeviceFeatures{}
	frugal.extensions = []string{khr_portability_subset.ExtensionName}
	c = negotiate(t, frugal, driverNameQuery{name: "never asked"}, 0, core1_0.Samples8)
	if c.MSAASamples != 2 {
		t.Errorf("MSAA negotiated to %dx, want 2x (colour allows 4x, depth only 2x)", c.MSAASamples)
	}
	if c.MaxAnisotropy != 0 {
		t.Errorf("MaxAnisotropy %g without samplerAnisotropy; 0 is how callers read 'unavailable'", c.MaxAnisotropy)
	}
	if c.MultiDrawIndirect || c.DrawIndirectFirstInstance || c.GPUTimestamps || c.RayQuery || c.PipelineStatistics {
		t.Errorf("features granted that the device does not have: %+v", c)
	}
	if !c.PortabilitySubset {
		t.Error("portability subset not reported for a device that advertises it")
	}
	if c.DriverName != "" {
		t.Errorf("DriverName %q from a device without VK_KHR_driver_properties", c.DriverName)
	}

	// A device whose timestamp limit is set but whose chosen queue writes no
	// bits -- family 0 above -- is the half-capability case, and it has to read
	// as unavailable rather than as supported-but-zero.
	half := generousDevice()
	if c := negotiate(t, half, nil, 0, core1_0.Samples1); c.GPUTimestamps {
		t.Error("timestamps reported for a queue family with no valid bits")
	}
	// And a driver-properties query that fails leaves the name empty rather
	// than failing New: a report one string short still renders.
	if c := negotiate(t, half, driverNameQuery{err: errors.New("query failed")}, 1, core1_0.Samples1); c.DriverName != "" {
		t.Errorf("DriverName %q from a failed query", c.DriverName)
	}
}

func TestRayQueryClosureNeedsEveryExtension(t *testing.T) {
	full := map[string]*core1_0.ExtensionProperties{}
	for _, name := range rayQueryExtensions {
		full[name] = &core1_0.ExtensionProperties{}
	}
	if !rayQueryAvailable(full) {
		t.Fatal("full closure rejected")
	}
	for _, name := range rayQueryExtensions {
		delete(full, name)
		if rayQueryAvailable(full) {
			t.Errorf("closure accepted without %s", name)
		}
		full[name] = &core1_0.ExtensionProperties{}
	}
}

// traceFixture is a renderer with just enough swapchain and depth state for
// traceStaticGPUState, carrying whatever the device granted.
func traceFixture(c Capabilities) *Renderer {
	r := &Renderer{
		sc: &swapchainDetails{
			imageFormat:    core1_0.FormatB8G8R8A8SRGB,
			images:         make([]core1_0.Image, 3),
			extent:         core1_0.Extent2D{Width: 1280, Height: 720},
			captureCapable: true,
		},
		depth: &depthResources{format: core1_0.FormatD32SignedFloat},
	}
	r.adopt(c)
	return r
}

var traceConfig = regexp.MustCompile(`config=([0-9a-f]+)`)

// tracedConfig renders the fixture's static GPU state to a real trace file and
// reads the config= field back out of it, rather than calling the hash helpers
// directly: the field a diff compares is the thing under test.
func tracedConfig(t *testing.T, r *Renderer) Hasher {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.trace")
	tr, err := OpenStateTrace(path)
	if err != nil {
		t.Fatal(err)
	}
	tr.Begin(0)
	r.traceStaticGPUState(tr)
	tr.End()
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	m := traceConfig.FindSubmatch(body)
	if m == nil {
		t.Fatalf("no config= field in %q -- this check proves nothing", strings.TrimSpace(string(body)))
	}
	v, err := strconv.ParseUint(string(m[1]), 16, 64)
	if err != nil {
		t.Fatal(err)
	}
	return Hasher(v)
}

// Breaks verified against this check, each one restored afterwards:
//
//   - MSAASamples sourced from the requested count instead of the negotiated
//     one tripped the vacuity guard below ("fixture device granted the
//     request") and failed TestCapabilitiesReportWhatTheDeviceGranted with
//     "MSAA negotiated to 8x, want 2x".
//   - adopt building msaaSamples independently of the report failed the
//     prediction: "config=cc92f1421d3131c6, predicted 4190a970a26b4cf4".
//   - identHash dropping GPUName failed the per-field sweep: "GPUName: config=
//     moved=false, want true".
func TestCapabilitiesHashAgreesWithTrace(t *testing.T) {
	// A device that halves the request, so the report and the request differ:
	// sourcing MSAASamples from the requested count instead of the negotiated
	// one is exactly the drift this has to catch, and against a device that
	// grants what it was asked for it could not.
	d := generousDevice()
	d.limits.FramebufferDepthSampleCounts = core1_0.Samples1 | core1_0.Samples2
	c := negotiate(t, d, driverNameQuery{name: "test driver 1.2"}, 1, core1_0.Samples8)
	if c.MSAASamples == 8 {
		t.Fatal("fixture device granted the request; the agreement below would be vacuous")
	}
	r := traceFixture(c)

	// The device half of config= predicted from the report alone. The swapchain
	// half is the fixture's own, which the test chose. Anything in the report
	// whose source drifts away from what the trace folded shows up here.
	want := NewHash.
		Int(r.Capabilities().MSAASamples).
		Bool(r.msaa != nil).
		Int(int(r.sc.imageFormat)).
		Int(len(r.sc.images)).
		Int(int(r.sc.extent.Width)).
		Int(int(r.sc.extent.Height)).
		Bool(r.sc.captureCapable).
		Int(int(r.depth.format)).
		Int(maxFramesInFlight).
		Bool(r.bloom != nil).
		Uint64(uint64(r.Capabilities().identHash()))
	if got := tracedConfig(t, r); got != want {
		t.Fatalf("config=%x, predicted %x from Capabilities()", got, want)
	}

	// And every identity field the report carries has to reach the field, or a
	// run on a different GPU could diff clean. Each edit is applied to the
	// device's answers and renegotiated, so this measures the path from the
	// driver through the report into the trace rather than the hash helper.
	base := tracedConfig(t, traceFixture(c))
	for _, tc := range []struct {
		field  string
		edit   func(*capsTestInstance)
		driver string
		moves  bool
	}{
		{field: "GPUName", edit: func(d *capsTestInstance) { d.props.DriverName = "Other GPU" }, moves: true},
		{field: "DriverVersion", edit: func(d *capsTestInstance) { d.props.DriverVersion = 0x00400001 }, moves: true},
		{field: "APIVersion", edit: func(d *capsTestInstance) { d.props.APIVersion = 0x00403000 }, moves: true},
		{field: "VendorID", edit: func(d *capsTestInstance) { d.props.VendorID = 0x10de }, moves: true},
		{field: "DeviceID", edit: func(d *capsTestInstance) { d.props.DeviceID = 0x2684 }, moves: true},
		{field: "deviceType", edit: func(d *capsTestInstance) { d.props.DriverType = 1 }, moves: true},
		{field: "cacheUUID", edit: func(d *capsTestInstance) { d.props.PipelineCacheUUID[0] = 99 }, moves: true},
		{field: "MSAASamples", edit: func(d *capsTestInstance) {
			d.limits.FramebufferDepthSampleCounts = core1_0.Samples1
		}, moves: true},
		// Documented exception: DriverName is empty on any device without
		// VK_KHR_driver_properties, and a field that is sometimes absent cannot
		// carry a difference. See Capabilities.identHash.
		{field: "DriverName", driver: "a different driver", moves: false},
	} {
		edited := generousDevice()
		edited.limits.FramebufferDepthSampleCounts = core1_0.Samples1 | core1_0.Samples2
		if tc.edit != nil {
			tc.edit(edited)
		}
		name := "test driver 1.2"
		if tc.driver != "" {
			name = tc.driver
		}
		got := tracedConfig(t, traceFixture(negotiate(t, edited, driverNameQuery{name: name}, 1, core1_0.Samples8)))
		if moved := got != base; moved != tc.moves {
			t.Errorf("%s: config= moved=%v, want %v (%x vs %x)", tc.field, moved, tc.moves, got, base)
		}
	}
}

func TestCapabilitiesIsAValue(t *testing.T) {
	r := traceFixture(negotiate(t, generousDevice(), driverNameQuery{name: "test driver 1.2"}, 1, core1_0.Samples4))
	before := r.Capabilities()
	c := r.Capabilities()
	c.MSAASamples, c.MaxAnisotropy, c.GPUName, c.GPUTimestamps = 1, 0, "forged", false
	c.cacheUUID[0], c.timestampPeriod = 0xff, 0
	if r.Capabilities() != before {
		t.Fatalf("mutating the returned copy changed the renderer's report: %+v", r.Capabilities())
	}
	// And nothing the trace folds moved with it, which is the consequence that
	// matters: a report a game could edit would be a report two runs could
	// disagree about for no reason the device explains.
	if r.deviceIdent != before.identHash() {
		t.Fatal("identity hash moved")
	}
}

// Break verified: deleting the GPUTimestamps check from validateAppTiming made
// both constructors accept the descriptor and this report "<nil>, want
// ErrCapabilityUnavailable" -- which is the silent degradation the refusal
// replaces.
func TestTimedPassNeedsTimestamps(t *testing.T) {
	r := &Renderer{}
	target := &RenderTarget{r: r, desc: RenderTargetDesc{Name: "out", Format: TargetR16F, Scale: 1, Storage: true}}
	target.texture.target = target

	pass := AppPassDesc{Name: "timed", Stage: StageBeforeScene, Target: target, Fullscreen: true,
		Vert: shaders.DepthResolveVertSpv, Frag: shaders.DepthResolveFragSpv, Timed: true}
	compute := AppComputeDesc{Name: "timed compute", Stage: StageBeforeScene, Comp: computeCode(t),
		Writes: []*RenderTarget{target}, Timed: true}

	for _, tc := range []struct {
		what string
		err  error
	}{
		{"app pass", r.validateAppPass(pass)},
		{"app compute", r.validateAppCompute(compute)},
	} {
		if !errors.Is(tc.err, ErrCapabilityUnavailable) {
			t.Fatalf("%s on a device without timestamps: %v, want ErrCapabilityUnavailable", tc.what, tc.err)
		}
		// The name is in the message as well as the sentinel, so a log line
		// says which capability without the caller branching to find out.
		for _, want := range []string{tc.what, "Timed", "GPU timestamps"} {
			if !strings.Contains(tc.err.Error(), want) {
				t.Errorf("%s: %q does not name %q", tc.what, tc.err, want)
			}
		}
	}

	// The same descriptors on a device that can time them are accepted, so the
	// refusal above is about the capability and not about the descriptor.
	r.caps.GPUTimestamps = true
	if err := r.validateAppPass(pass); err != nil {
		t.Fatalf("timed app pass refused on a device with timestamps: %v", err)
	}
	if err := r.validateAppCompute(compute); err != nil {
		t.Fatalf("timed app compute refused on a device with timestamps: %v", err)
	}
	// An untimed pass never consults the capability at all.
	r.caps.GPUTimestamps = false
	pass.Timed, compute.Timed = false, false
	if err := r.validateAppPass(pass); err != nil {
		t.Fatalf("untimed app pass refused: %v", err)
	}
	if err := r.validateAppCompute(compute); err != nil {
		t.Fatalf("untimed app compute refused: %v", err)
	}
}
