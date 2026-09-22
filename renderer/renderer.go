package renderer

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"time"
	"unsafe"

	"github.com/vkngwrapper/core/v3/common"
	"github.com/vkngwrapper/core/v3/core1_0"
	"github.com/vkngwrapper/extensions/v3/ext_debug_utils"
	"github.com/vkngwrapper/extensions/v3/khr_surface"
	khr_surface_loader "github.com/vkngwrapper/extensions/v3/khr_surface/loader"
	"github.com/vkngwrapper/extensions/v3/khr_swapchain"

	"github.com/derekmwright/glyphengine/window"
)

// Renderer manages the Vulkan rendering pipeline: instance, device, swapchain,
// render pass, graphics pipelines, and per-frame synchronization.
type Renderer struct {
	win *window.Window

	instanceDriver core1_0.CoreInstanceDriver
	deviceDriver   core1_0.CoreDeviceDriver
	surfaceExt     khr_surface.ExtensionDriver
	swapchainExt   khr_swapchain.ExtensionDriver

	surface        khr_surface.Surface
	physicalDevice core1_0.PhysicalDevice
	indices        queueFamilyIndices

	graphicsQueue core1_0.Queue
	presentQueue  core1_0.Queue

	sc *swapchainDetails
	// lastExtent is the most recent extent a fully successful swapchain
	// build (New or a completed recreateSwapchain) actually produced. Aspect
	// and Extent read this rather than r.sc.extent directly: r.sc is nil
	// while a rebuild attempt has failed and not yet been retried (see
	// recreateSwapchain and issue #86), and a game's own per-frame Update
	// calls those two -- 13-ui does, for its layout -- independently of
	// whether the last DrawFrame succeeded. Answering with the last extent
	// that actually existed is correct rather than merely safe: nothing
	// about the picture has moved just because the next rebuild has not
	// completed yet.
	lastExtent             core1_0.Extent2D
	renderPass             core1_0.RenderPass
	pipelineLayout         core1_0.PipelineLayout // non-lit (stars, overlay, msdf, ui)
	litPipelineLayout      core1_0.PipelineLayout // lit static: set 0=tex, set 1=shadow
	pipeline               core1_0.Pipeline
	litDoubleSidedPipeline core1_0.Pipeline
	// Identical in layout to litPipelineLayout, but a distinct object:
	// createGraphicsPipeline builds one per call.
	litDoubleSidedPipelineLayout core1_0.PipelineLayout
	// The blended lit variants and their layouts; see
	// createTranslucentPipeline. Each carries its own layout for the same
	// reason the lit pair does: createLitVariantPipeline builds one per call.
	translucentPipeline                  core1_0.Pipeline
	translucentPipelineLayout            core1_0.PipelineLayout
	translucentDoubleSidedPipeline       core1_0.Pipeline
	translucentDoubleSidedPipelineLayout core1_0.PipelineLayout
	// The instanced lit variants and their layouts; see
	// createInstancedPipeline.
	instancedPipeline                  core1_0.Pipeline
	instancedPipelineLayout            core1_0.PipelineLayout
	instancedDoubleSidedPipeline       core1_0.Pipeline
	instancedDoubleSidedPipelineLayout core1_0.PipelineLayout
	// instanceSets is every set the renderer has handed out that has not been
	// explicitly given back, so whatever remains can still be freed at
	// teardown. DestroyInstanceSet removes an entry once its own deferred free
	// has actually run -- not at the call that requests it -- the same
	// bookkeeping DestroyModel's release closure keeps for r.meshes/
	// r.textures/r.materials, and for the same reason: the resource is still
	// genuinely alive for the frames still in flight when the call is made.
	instanceSets      []*InstanceSet
	overlayPipeline   core1_0.Pipeline
	starsPipeline     core1_0.Pipeline
	celestialPipeline core1_0.Pipeline
	skyPipeline       core1_0.Pipeline
	// skyVolumetricPipeline draws in-scattering over the pixels the sky
	// covers. Both sky passes use skyPipelineLayout: the non-lit layout plus
	// the shadow/light set at set 1 for directional shadows and the froxel grid. See
	// createSkyVolumetricPipeline and shaders/skyvolumetric.frag.
	skyVolumetricPipeline    core1_0.Pipeline
	skyPipelineLayout        core1_0.PipelineLayout
	uiPipeline               core1_0.Pipeline
	msdfPipeline             core1_0.Pipeline
	jointDescriptorSetLayout core1_0.DescriptorSetLayout
	skinnedPipelineLayout    core1_0.PipelineLayout // skinned: set 0=tex, set 1=joints, set 2=shadow
	skinnedPipeline          core1_0.Pipeline
	// skinnedTranslucentPipeline is the blended twin; see createTranslucentPipeline
	// for why the blended variants exist and translucency.md for what they cover.
	skinnedTranslucentPipeline core1_0.Pipeline
	// Skinned + Material: set 0=material, set 1=joints, set 2=shadow.
	skinnedMaterialPipelineLayout core1_0.PipelineLayout
	skinnedMaterialPipeline       core1_0.Pipeline
	grassPipeline                 core1_0.Pipeline
	grassImpostorPipeline         core1_0.Pipeline
	waterPipeline                 core1_0.Pipeline
	godRayPipeline                core1_0.Pipeline
	waterRenderPass               core1_0.RenderPass
	waterFramebuffers             []core1_0.Framebuffer
	sceneColor                    *sceneColorTarget
	grass                         *GrassSystem
	// waterPlanes is scratch for the per-frame split of blended draws onto
	// either side of the water, kept so a frame that draws water does not
	// allocate to classify against it. See waterorder.go.
	waterPlanes []waterPlane
	// cmdScratch is recordCommandBuffer's reusable descriptor-set, vertex-buffer,
	// clear-value, barrier and push-constant storage -- see commandscratch.go.
	// A value field rather than a pointer: it holds no GPU handle, needs no
	// teardown, and the zero value (every array zeroed) is already correct on
	// the first frame.
	cmdScratch       commandScratch
	shaderParameters [ShaderParameterBytes]byte
	// grassLOD is the distance tuning grass thins, fades and culls by.
	// Defaulted at construction so a zero value never culls grass at zero.
	grassLOD GrassLOD
	// milkyWayTex is an optional equirectangular sky panorama sampled by the
	// star pass; nil leaves the procedural band. See SetMilkyWayTexture.
	milkyWayTex *Texture

	// grassImpostor is the baked billboard atlas; nil until grass is loaded.
	grassImpostor         *grassImpostor
	terrainSetLayout      core1_0.DescriptorSetLayout // set 0 = 4 terrain samplers
	terrainPipelineLayout core1_0.PipelineLayout      // terrain: set 0=4 tex, set 1=shadow
	terrainPipeline       core1_0.Pipeline
	particlePipeline      core1_0.Pipeline
	particles             *ParticleSystem
	shadow                *shadowResources
	framebuffers          []core1_0.Framebuffer
	commandPool           core1_0.CommandPool
	commandBuffers        [maxFramesInFlight]core1_0.CommandBuffer
	sync                  *syncObjects
	depth                 *depthResources
	msaa                  *msaaResources
	msaaSamples           core1_0.SampleCountFlags

	// lastFenceWait is how long the previous DrawFrame blocked on the in-flight
	// fence and on acquiring a swapchain image. The engine folds it into its own
	// CPU breakdown, where it is the number that separates "the GPU is the
	// limit" from "the CPU is".
	lastFenceWait time.Duration

	// lastRecord and lastPresent split what used to be lumped together as
	// "submit". Presenting can block on the presentation engine exactly as
	// acquiring can, so counting it as CPU work reports a busy CPU on a frame
	// that is simply being paced.
	lastRecord  time.Duration
	lastPresent time.Duration

	// hdr is the offscreen colour buffer the scene renders into; the tonemap
	// pass resolves it to the swapchain. See hdr.go.
	hdr                   *hdrTarget
	tonemapSetLayout      core1_0.DescriptorSetLayout
	tonemapPipelineLayout core1_0.PipelineLayout
	tonemapRenderPass     core1_0.RenderPass
	tonemapPipeline       core1_0.Pipeline
	tonemapFramebuffers   []core1_0.Framebuffer

	// Exposure and curve for the tonemap pass; see SetTonemap.
	exposure     float32
	tonemapCurve float32
	tonemapWhite float32

	// clouds is the half-resolution buffer the raymarch renders into; the sky
	// pass composites it. See clouds.go.
	clouds *cloudTarget
	// cloudFrame counts frames for the cloud history ping-pong, and prevVP is
	// the view-projection that frame was rendered with. Both exist only for
	// temporal reprojection.
	cloudFrame      int
	prevVP          [16]float32
	prevCloudTime   float32
	cloudRenderPass core1_0.RenderPass
	cloudPipeline   core1_0.Pipeline

	// bloom is the mip chain the glare passes run through, composited by the
	// tonemap resolve. Off by default; see SetBloom and bloom.go.
	bloom                  *bloomTarget
	bloomDownRenderPass    core1_0.RenderPass
	bloomUpRenderPass      core1_0.RenderPass
	bloomPrefilterPipeline core1_0.Pipeline
	bloomDownPipeline      core1_0.Pipeline
	bloomUpPipeline        core1_0.Pipeline

	bloomIntensity float32
	bloomThreshold float32
	bloomKnee      float32
	bloomRadius    float32

	// The screen-space UI's own HDR layer: a second colour target, a second
	// bloom chain over it, and a fixed-exposure composite onto the swapchain.
	// nil unless WithUIGlowLayer was passed, and everything about the feature
	// is skipped when it is. See uilayer.go.
	uiLayer             *uiLayerTarget
	uiLayerRenderPass   core1_0.RenderPass
	uiLayerUIPipeline   core1_0.Pipeline
	uiLayerMSDFPipeline core1_0.Pipeline
	uiResolvePipeline   core1_0.Pipeline
	uiGlowRequested     bool

	// The UI layer's look. Defaulted at construction rather than left zero,
	// because a zero threshold would make every white label glow the moment a
	// game turned the strength up; see SetUIGlow.
	uiGlowStrength  float32
	uiGlowThreshold float32
	uiGlowKnee      float32
	uiGlowRadius    float32
	uiExposure      float32

	// stats counts what the frame submitted; see stats.go.
	stats RenderStats

	// gpuTimer measures per-pass GPU cost with timestamp queries; see gputimer.go.
	// Non-nil always, but inert when the device cannot timestamp graphics work.
	gpuTimer *gpuTimer

	descriptorSetLayout core1_0.DescriptorSetLayout
	descriptorPool      core1_0.DescriptorPool

	// liveDescriptorSets is how many sets the APPLICATION's resources hold
	// from descriptorPool right now: one per Texture, one per Material, one
	// per TerrainMaterial, maxFramesInFlight per JointBuffer, plus one for the
	// grass impostor atlas while one is baked (issue #87). Kept by hand
	// because Vulkan will not tell us -- there is no query for how much of a
	// pool is spent, and the only signal it offers is the allocation that
	// finally fails.
	//
	// It does NOT count the renderer's own pass sets (shadow, HDR, bloom,
	// clouds, the scene-colour copy, the UI glow layer). Those are allocated
	// in New and rebuilt on resize, so counting them would make this number
	// move when a window is dragged, and what it exists for is the other
	// thing entirely: it sits beside Meshes/Textures/Materials in
	// ResourceCounts, which describe what the application created and can
	// give back, and it is how a leak in that path reads as a number rather
	// than as an out-of-pool-memory error 677 loads later. See
	// freeDescriptorSets, and docs/agents/validation.md for what lives in the
	// pool besides this.
	liveDescriptorSets int

	fallbackTexture *Texture
	textures        []*Texture

	// Material maps: set 0 = albedo/normal/metallic-roughness/occlusion
	// samplers plus a per-material uniform buffer. fallbackNormal is the flat
	// tangent-space normal bound to slots a material leaves unsupplied.
	//
	// Two pipelines for the same reason the lit path has two: without a
	// double-sided variant, a Material on a DoubleSided entity would silently
	// cull its back faces. Each carries its own layout because
	// createLitVariantPipeline builds one per call.
	materialSetLayout                 core1_0.DescriptorSetLayout
	materialPipelineLayout            core1_0.PipelineLayout
	materialPipeline                  core1_0.Pipeline
	materialDoubleSidedPipelineLayout core1_0.PipelineLayout
	materialDoubleSidedPipeline       core1_0.Pipeline
	fallbackNormal                    *Texture
	materials                         []*Material

	// Diagnostic tri-color triangle (see triangle.go). Built lazily on the
	// first DrawTriangle call; nil for programs that never use it.
	trianglePipeline       *core1_0.Pipeline
	trianglePipelineLayout *core1_0.PipelineLayout

	currentFrame       int
	lastPresented      int // swapchain index of the most recent present; see CaptureFrame
	framebufferResized bool

	// trace records what each frame fed the GPU when GLYPHENGINE_STATE_TRACE
	// is set; nil, and free, otherwise. See StateTrace.
	trace *StateTrace

	// provokeSkip makes the next DrawFrame treat its acquire as out of date;
	// see ProvokeSkipNextFrame. False in every run that did not ask for it.
	provokeSkip bool

	meshes        []*Mesh
	jointBuffers  []*JointBuffer
	dynamicMeshes map[*Mesh]*dynamicMesh
	maxAnisotropy float32 // 0 = anisotropic filtering unavailable

	// Reported to Vulkan at instance creation; see WithApplicationName.
	appName    string
	appVersion common.Version

	// vsync selects the swapchain present mode; see WithVSync.
	vsync bool

	// shaders holds the SPIR-V every pipeline is built from; see WithShaders.
	shaders ShaderSet

	// Validation layer plumbing; nil unless validation is enabled.
	validation bool
	debugExt   ext_debug_utils.ExtensionDriver
	debugMsgr  ext_debug_utils.DebugUtilsMessenger

	// initStack holds one teardown step per resource created by New, in
	// creation order. Both the failure path in New and Destroy unwind it in
	// reverse, so there is exactly one place that knows destruction order and
	// the two can never drift apart.
	//
	// Steps read resources through r rather than capturing them, because
	// recreateSwapchain replaces the swapchain, depth, MSAA, and framebuffers
	// after New has already pushed their teardown.
	initStack []func()

	destroyed bool

	deferredDestroys []deferredDestroy
}

// onInit records a teardown step for a resource that was just created
// successfully.
func (r *Renderer) onInit(fn func()) { r.initStack = append(r.initStack, fn) }

// unwindInit runs every recorded teardown step in reverse creation order and
// empties the stack, so calling it twice is harmless.
func (r *Renderer) unwindInit() {
	for i := len(r.initStack) - 1; i >= 0; i-- {
		r.initStack[i]()
	}
	r.initStack = nil
}

// rebuildUndo is the same creation-order-in, reverse-order-out idiom as
// initStack/onInit/unwindInit, scoped to a single recreateSwapchain attempt
// instead of the renderer's whole lifetime.
//
// It cannot BE initStack. initStack's closures already read every
// swapchain-dependent field through r rather than a captured value (rule 10
// in AGENTS.md), precisely so recreateSwapchain can swap r.hdr, r.bloom and
// the rest out from under them without New needing to know a resize would
// happen. Destroy unwinds initStack exactly once, at the end of the
// renderer's life; pushing recreateSwapchain's own per-attempt teardown onto
// it would run every rebuild's teardown again at that point, once per resize
// the renderer ever survived, destroying handles that were already replaced
// or freed and were never leaked in the first place.
type rebuildUndo []func()

// push records a teardown step for a resource this rebuild attempt just
// created successfully.
func (u *rebuildUndo) push(fn func()) { *u = append(*u, fn) }

// unwind runs every recorded step in reverse and empties the stack, undoing
// only what this attempt built. Whatever recreateSwapchain destroyed before
// the attempt started (the previous swapchain, depth, HDR and the rest) is
// already gone and is not this stack's concern.
func (u *rebuildUndo) unwind() {
	s := *u
	for i := len(s) - 1; i >= 0; i-- {
		s[i]()
	}
	*u = nil
}

// deferredDestroy holds a GPU resource destruction callback that must wait
// for all in-flight frames to complete before executing.
type deferredDestroy struct {
	framesLeft int
	fn         func()
}

// Option configures the Renderer at creation time.
type Option func(*Renderer)

// WithApplicationName sets the application name and version reported to Vulkan.
// Driver tools, GPU profilers, and vendor control panels display it, and some
// drivers key per-application optimizations off it, so a shipping game should
// set its own rather than appearing as "GlyphEngine Application".
//
// Build version with common.CreateVersion(major, minor, patch). A zero version
// means 0.1.0.
func WithApplicationName(name string, version common.Version) Option {
	return func(r *Renderer) {
		r.appName = name
		r.appVersion = version
	}
}

// WithValidation enables the Khronos validation layer and routes its output
// through the standard logger. It is off by default: validation costs real
// frame time, and it requires the Vulkan SDK, which players do not have.
//
// When the layer is unavailable this logs a warning and continues without it,
// rather than failing to start.
//
// The GLYPHENGINE_VALIDATION environment variable overrides this either way,
// so validation can be switched on for any already-built binary:
//
//	GLYPHENGINE_VALIDATION=1 ./mygame
func WithValidation(enabled bool) Option {
	return func(r *Renderer) { r.validation = enabled }
}

// WithVSync controls frame pacing by selecting the swapchain present mode.
// It defaults to true.
//
// With vsync on, the presentation engine blocks at the refresh rate of the
// display the window is actually on. That is the correct place for the frame
// limit to live: it costs no CPU, follows the window between monitors, and
// cannot disagree with the hardware.
//
// With vsync off, the renderer draws unbounded — Mailbox where available (no
// tearing, newest frame wins), Immediate otherwise (tearing). Expect a pegged
// GPU and the fans that come with it; this is for benchmarking, profiling, and
// latency-sensitive input, not a general default.
func WithVSync(enabled bool) Option {
	return func(r *Renderer) { r.vsync = enabled }
}

// WithShaders replaces the SPIR-V the renderer builds its pipelines from.
// Fields left nil fall back to the engine's embedded shader for that stage,
// so a game can override one pipeline without supplying all of them.
//
// See ShaderSet for what a replacement has to match.
func WithShaders(set ShaderSet) Option {
	return func(r *Renderer) { r.shaders = set }
}

// Shaders returns the set the renderer builds its pipelines from, as last set
// by WithShaders and filled in from the embedded defaults. Exposed for the same
// reason Bloom and Tonemap are: so a harness can see what is actually in effect
// rather than assuming the option it passed arrived.
func (r *Renderer) Shaders() ShaderSet {
	return r.shaders
}

// WithMSAASamples requests an MSAA sample count (1, 2, 4, or 8). Invalid
// values are ignored; the value is clamped to what the device supports for
// both color and depth once the physical device is selected.
func WithMSAASamples(n int) Option {
	return func(r *Renderer) {
		switch n {
		case 1:
			r.msaaSamples = core1_0.Samples1
		case 2:
			r.msaaSamples = core1_0.Samples2
		case 4:
			r.msaaSamples = core1_0.Samples4
		case 8:
			r.msaaSamples = core1_0.Samples8
		}
	}
}

// WithUIGlowLayer gives the screen-space UI its own HDR layer, so a UI element
// can be brighter than 1 and bloom across the elements around it.
//
// It is an option rather than a setter because the layer is a pair of
// swapchain-sized targets: they are allocated when the renderer is built and
// rebuilt on every resize, which is not a decision that can be made per frame.
// How the glow LOOKS is a per-frame decision and lives in SetUIGlow and
// SetUIExposure.
//
// Off by default, and free when off: no images, no framebuffers, no descriptor
// sets, no pipelines, and nothing extra recorded. A UI element's Glow is
// likewise inert without it -- the swapchain is 8 bits per channel, so with no
// layer there is nowhere for a value above 1 to be.
//
// What it costs when on, at 1280x720 with three swapchain images: 21.09 MiB for
// the layer itself (R16G16B16A16_SFLOAT at full resolution, one image per
// swapchain image) and 7.02 MiB for its five-level bloom chain, 28.12 MiB in
// all. On a Radeon RX 7900 XTX at that size it is +0.094 ms of GPU time per
// frame against the same scene with the layer off, of which the bloom chain is
// 0.063 ms and is skipped entirely by SetUIGlow(0, ...). Full numbers in
// docs/agents/overlay-composite.md.
func WithUIGlowLayer() Option {
	return func(r *Renderer) { r.uiGlowRequested = true }
}

// New initializes the full Vulkan rendering stack: instance, surface, device,
// swapchain, render pass, pipelines, framebuffers, command buffers, and sync
// objects. Call Destroy on the result.
//
// Every failure is wrapped with the step that produced it, because a bare
// Vulkan result code does not say which of two dozen pipelines failed to
// build.
//
// A failure at any step unwinds everything created before it, in reverse
// order, so New either returns a usable Renderer or leaves nothing behind. The
// same unwind runs in Destroy, which is what keeps creation and destruction
// order from drifting apart.
func New(w *window.Window, opts ...Option) (_ *Renderer, err error) {
	r := &Renderer{
		win:           w,
		msaaSamples:   core1_0.Samples2,
		vsync:         true, // see WithVSync
		lastPresented: -1,
		shaders:       DefaultShaders(),
		dynamicMeshes: make(map[*Mesh]*dynamicMesh),
		grassLOD:      DefaultGrassLOD(),
		// The UI glow layer's defaults, which are the scene bloom's own
		// starting point over an image whose ordinary content tops out at
		// exactly 1. Inert until WithUIGlowLayer allocates the layer.
		uiGlowStrength:  0.7,
		uiGlowThreshold: 1.2,
		uiGlowKnee:      0.2,
		uiGlowRadius:    1.0,
		uiExposure:      1.0,
	}
	for _, o := range opts {
		o(r)
	}
	// An override may set only the stages it cares about.
	r.shaders = r.shaders.withDefaults()

	// Unwind on any failure below. Every `return nil, err` therefore leaves
	// the process in the state it was in before New was called.
	defer func() {
		if err != nil {
			r.unwindInit()
		}
	}()

	// Step 1: Vulkan instance
	instanceDriver, gotValidation, err := createInstance(w, r.appName, r.appVersion, validationSetting(r.validation))
	if err != nil {
		return nil, err
	}
	r.instanceDriver = instanceDriver
	r.validation = gotValidation
	r.onInit(func() { r.instanceDriver.DestroyInstance(nil) })

	// Step 1b: Validation output. Created immediately after the instance so it
	// captures messages from every later step, including the teardown that a
	// failure in one of them triggers.
	if r.validation {
		debugExt, messenger, derr := createDebugMessenger(instanceDriver)
		if derr != nil {
			return nil, fmt.Errorf("renderer: create debug messenger: %w", derr)
		}
		if debugExt != nil {
			r.debugExt, r.debugMsgr = debugExt, messenger
			r.onInit(func() { r.debugExt.DestroyDebugUtilsMessenger(r.debugMsgr, nil) })
		}
	}

	// Step 2: Surface
	r.surfaceExt = khr_surface.CreateExtensionDriverFromCoreDriver(instanceDriver)
	if r.surfaceExt == nil {
		// VK_KHR_surface is not in the instance's extension set. GLFW asks for
		// it via GetRequiredInstanceExtensions, so reaching here means the
		// loader found no presentation-capable ICD — typically a headless
		// machine, or a driver install that left only a software fallback
		// without WSI.
		return nil, errors.New("renderer: VK_KHR_surface unavailable — no presentation-capable Vulkan driver found")
	}

	instanceHandle := unsafe.Pointer(instanceDriver.Instance().Handle())
	surfaceHandle, err := w.CreateVulkanSurface(instanceHandle)
	if err != nil {
		return nil, fmt.Errorf("renderer: create window surface: %w", err)
	}

	r.surface, err = r.surfaceExt.CreateSurfaceFromHandle(
		khr_surface_loader.VkSurfaceKHR(surfaceHandle),
	)
	if err != nil {
		return nil, fmt.Errorf("renderer: wrap surface handle: %w", err)
	}
	log.Println("Vulkan surface created")
	r.onInit(func() { r.surfaceExt.DestroySurface(r.surface, nil) })

	// Step 3: Physical device
	r.physicalDevice, r.indices, err = pickPhysicalDevice(instanceDriver, r.surfaceExt, r.surface)
	if err != nil {
		return nil, fmt.Errorf("renderer: select physical device: %w", err)
	}

	// Clamp the requested MSAA level to what the device supports for both
	// color and depth framebuffers, halving until a supported count is found.
	if props, err := instanceDriver.GetPhysicalDeviceProperties(r.physicalDevice); err == nil {
		// The push constant block is shared by every pipeline and is already
		// larger than Vulkan's guaranteed 128 bytes, so check it rather than
		// discovering the limit as a pipeline that will not create.
		if lim := props.Limits.MaxPushConstantsSize; lim < pushConstantSize {
			return nil, fmt.Errorf("renderer: device allows %d bytes of push constants, engine needs %d",
				lim, pushConstantSize)
		}
		log.Printf("Push constants: %d bytes used of %d available", pushConstantSize, props.Limits.MaxPushConstantsSize)

		// The clustered light data (LightBuffer, ClusterGrid, LightIndices)
		// lives in three storage buffer bindings on one fragment-stage
		// descriptor set. Vulkan 1.0 core guarantees at least 4 per stage, so
		// this should never fire, but it is the same "check the limit rather
		// than discover it as a pipeline that will not create" reasoning as
		// the push constant check above -- a silent 0 here would be a device
		// that simply cannot run this renderer.
		if lim := props.Limits.MaxPerStageDescriptorStorageBuffers; lim < lightStorageBuffersPerSet {
			return nil, fmt.Errorf("renderer: device allows %d storage buffers per stage, engine needs %d for clustered lighting",
				lim, lightStorageBuffersPerSet)
		}
		supported := props.Limits.FramebufferColorSampleCounts & props.Limits.FramebufferDepthSampleCounts
		requested := r.msaaSamples
		for r.msaaSamples > core1_0.Samples1 && supported&r.msaaSamples == 0 {
			r.msaaSamples >>= 1
		}
		if r.msaaSamples != requested {
			log.Printf("MSAA: requested %dx not supported, using %dx", requested, r.msaaSamples)
		} else {
			log.Printf("MSAA: %dx", r.msaaSamples)
		}

		if instanceDriver.GetPhysicalDeviceFeatures(r.physicalDevice).SamplerAnisotropy {
			r.maxAnisotropy = props.Limits.MaxSamplerAnisotropy
			log.Printf("Anisotropic filtering: %gx", r.maxAnisotropy)
		}
	}

	// Step 4: Logical device
	r.deviceDriver, err = createLogicalDevice(instanceDriver, r.physicalDevice, r.indices)
	if err != nil {
		return nil, fmt.Errorf("renderer: create logical device: %w", err)
	}
	r.onInit(func() { r.deviceDriver.DestroyDevice(nil) })

	r.graphicsQueue = r.deviceDriver.GetQueue(r.indices.graphicsFamily, 0)
	r.presentQueue = r.deviceDriver.GetQueue(r.indices.presentFamily, 0)

	// Step 5: Swapchain
	width, height := w.GetFramebufferSize()
	r.sc, r.swapchainExt, err = createSwapchain(r.deviceDriver, r.surfaceExt, r.surface, r.physicalDevice, r.indices, width, height, r.vsync)
	if err != nil {
		return nil, fmt.Errorf("renderer: create swapchain: %w", err)
	}
	r.lastExtent = r.sc.extent
	// Reads r.sc at unwind time: recreateSwapchain replaces it on resize --
	// and, since issue #86, can leave it nil if the renderer's last rebuild
	// attempt failed and the program exits before a later one succeeds. Nil
	// guarded here for the same reason msaa's step below already was.
	r.onInit(func() {
		if r.sc == nil {
			return
		}
		for _, iv := range r.sc.imageViews {
			r.deviceDriver.DestroyImageView(iv, nil)
		}
		r.swapchainExt.DestroySwapchain(r.sc.swapchain, nil)
	})

	// Step 6: Depth buffer
	r.depth, err = createDepthResources(r.instanceDriver, r.deviceDriver, r.physicalDevice, r.sc.extent, len(r.sc.imageViews), r.msaaSamples)
	if err != nil {
		return nil, fmt.Errorf("renderer: create depth resources: %w", err)
	}
	// Guarded for the same reason the swapchain step just above is: a failed
	// rebuild (issue #86) can leave r.depth nil at Destroy time.
	r.onInit(func() {
		if r.depth != nil {
			r.depth.destroy(r.deviceDriver, len(r.depth.views))
		}
	})

	// Step 6b: MSAA color images (when MSAA is enabled). These are the scene
	// pass's colour attachment and resolve into the HDR target, so they carry
	// hdrFormat rather than the swapchain's — a framebuffer's attachments must
	// match the formats its render pass declares.
	if r.msaaSamples != core1_0.Samples1 {
		r.msaa, err = createMSAAResources(r.instanceDriver, r.deviceDriver, r.physicalDevice, r.sc.extent, hdrFormat, r.msaaSamples, len(r.sc.imageViews))
		if err != nil {
			return nil, fmt.Errorf("renderer: create MSAA resources: %w", err)
		}
		// Guarded: recreateSwapchain nils r.msaa when MSAA ends up disabled.
		r.onInit(func() {
			if r.msaa != nil {
				r.msaa.destroy(r.deviceDriver, len(r.msaa.views))
			}
		})
	}

	// Step 7: Descriptor set layout + pool
	r.descriptorSetLayout, err = createDescriptorSetLayout(r.deviceDriver)
	if err != nil {
		return nil, fmt.Errorf("renderer: create descriptor set layout: %w", err)
	}
	r.onInit(func() { r.deviceDriver.DestroyDescriptorSetLayout(r.descriptorSetLayout, nil) })

	r.terrainSetLayout, err = createTerrainDescriptorSetLayout(r.deviceDriver)
	if err != nil {
		return nil, fmt.Errorf("renderer: create terrain descriptor set layout: %w", err)
	}
	r.onInit(func() { r.deviceDriver.DestroyDescriptorSetLayout(r.terrainSetLayout, nil) })

	r.materialSetLayout, err = createMaterialDescriptorSetLayout(r.deviceDriver)
	if err != nil {
		return nil, fmt.Errorf("renderer: create material descriptor set layout: %w", err)
	}
	r.onInit(func() { r.deviceDriver.DestroyDescriptorSetLayout(r.materialSetLayout, nil) })

	r.descriptorPool, err = createDescriptorPool(r.deviceDriver, 512)
	if err != nil {
		return nil, fmt.Errorf("renderer: create descriptor pool: %w", err)
	}
	r.onInit(func() { r.deviceDriver.DestroyDescriptorPool(r.descriptorPool, nil) })

	r.jointDescriptorSetLayout, err = createJointDescriptorSetLayout(r.deviceDriver)
	if err != nil {
		return nil, fmt.Errorf("renderer: create joint descriptor set layout: %w", err)
	}
	r.onInit(func() { r.deviceDriver.DestroyDescriptorSetLayout(r.jointDescriptorSetLayout, nil) })

	// Step 7b: Shadow resources (needs descriptor pool)
	r.shadow, err = createShadowResources(r.instanceDriver, r.deviceDriver, r.shaders, r.physicalDevice, r.descriptorPool, r.jointDescriptorSetLayout)
	if err != nil {
		return nil, fmt.Errorf("renderer: create shadow resources: %w", err)
	}
	r.onInit(func() { r.shadow.destroy(r.deviceDriver) })

	// Step 8: Render pass + pipeline
	r.renderPass, err = createRenderPass(r.deviceDriver, hdrFormat, r.depth.format, r.msaaSamples)
	if err != nil {
		return nil, fmt.Errorf("renderer: create render pass: %w", err)
	}
	r.onInit(func() { r.deviceDriver.DestroyRenderPass(r.renderPass, nil) })

	// Non-lit set-0 layout for stars, overlay, MSDF and UI. The sky's layout
	// adds the shadow/light set below.
	r.pipelineLayout, err = createNonLitPipelineLayout(r.deviceDriver, r.descriptorSetLayout)
	if err != nil {
		return nil, fmt.Errorf("renderer: create non-lit pipeline layout: %w", err)
	}
	r.onInit(func() { r.deviceDriver.DestroyPipelineLayout(r.pipelineLayout, nil) })

	// Lit pipeline layout (set 0 = texture, set 1 = shadow) and lit pipeline
	r.pipeline, r.litPipelineLayout, err = createGraphicsPipeline(r.deviceDriver, r.shaders, r.renderPass, r.sc.extent, r.descriptorSetLayout, r.shadow.descriptorSetLayout, r.msaaSamples)
	if err != nil {
		return nil, fmt.Errorf("renderer: create lit pipeline: %w", err)
	}
	r.onInit(func() {
		r.deviceDriver.DestroyPipeline(r.pipeline, nil)
		r.deviceDriver.DestroyPipelineLayout(r.litPipelineLayout, nil)
	})
	// createGraphicsPipeline builds a pipeline layout per call, so this second
	// call returns its own. Discarding it with _ leaked a VkPipelineLayout for
	// the life of the process — invisible without validation, since nothing
	// else depends on it. Keep it and destroy it.
	r.litDoubleSidedPipeline, r.litDoubleSidedPipelineLayout, err = createGraphicsPipeline(r.deviceDriver, r.shaders, r.renderPass, r.sc.extent, r.descriptorSetLayout, r.shadow.descriptorSetLayout, r.msaaSamples, 0)
	if err != nil {
		return nil, fmt.Errorf("renderer: create double-sided lit pipeline: %w", err)
	}
	r.onInit(func() {
		r.deviceDriver.DestroyPipeline(r.litDoubleSidedPipeline, nil)
		r.deviceDriver.DestroyPipelineLayout(r.litDoubleSidedPipelineLayout, nil)
	})

	// Terrain splat pipeline: set 0 = 4 terrain samplers, set 1 = shadow.
	r.terrainPipeline, r.terrainPipelineLayout, err = createTerrainPipeline(r.deviceDriver, r.shaders, r.renderPass, r.sc.extent, r.terrainSetLayout, r.shadow.descriptorSetLayout, r.msaaSamples)
	if err != nil {
		return nil, fmt.Errorf("renderer: create terrain pipeline: %w", err)
	}
	r.onInit(func() {
		r.deviceDriver.DestroyPipeline(r.terrainPipeline, nil)
		r.deviceDriver.DestroyPipelineLayout(r.terrainPipelineLayout, nil)
	})

	// Material pipeline: set 0 = material (4 maps + a UBO), set 1 = shadow.
	r.materialPipeline, r.materialPipelineLayout, err = createMaterialPipeline(r.deviceDriver, r.shaders, r.renderPass, r.sc.extent, r.materialSetLayout, r.shadow.descriptorSetLayout, r.msaaSamples)
	if err != nil {
		return nil, fmt.Errorf("renderer: create material pipeline: %w", err)
	}
	r.onInit(func() {
		r.deviceDriver.DestroyPipeline(r.materialPipeline, nil)
		r.deviceDriver.DestroyPipelineLayout(r.materialPipelineLayout, nil)
	})

	r.materialDoubleSidedPipeline, r.materialDoubleSidedPipelineLayout, err = createMaterialPipeline(r.deviceDriver, r.shaders, r.renderPass, r.sc.extent, r.materialSetLayout, r.shadow.descriptorSetLayout, r.msaaSamples, 0)
	if err != nil {
		return nil, fmt.Errorf("renderer: create double-sided material pipeline: %w", err)
	}
	r.onInit(func() {
		r.deviceDriver.DestroyPipeline(r.materialDoubleSidedPipeline, nil)
		r.deviceDriver.DestroyPipelineLayout(r.materialDoubleSidedPipelineLayout, nil)
	})

	r.translucentPipeline, r.translucentPipelineLayout, err = createTranslucentPipeline(r.deviceDriver, r.shaders, r.renderPass, r.sc.extent, r.descriptorSetLayout, r.shadow.descriptorSetLayout, r.msaaSamples)
	if err != nil {
		return nil, fmt.Errorf("renderer: create translucent pipeline: %w", err)
	}
	r.onInit(func() {
		r.deviceDriver.DestroyPipeline(r.translucentPipeline, nil)
		r.deviceDriver.DestroyPipelineLayout(r.translucentPipelineLayout, nil)
	})

	r.translucentDoubleSidedPipeline, r.translucentDoubleSidedPipelineLayout, err = createTranslucentPipeline(r.deviceDriver, r.shaders, r.renderPass, r.sc.extent, r.descriptorSetLayout, r.shadow.descriptorSetLayout, r.msaaSamples, 0)
	if err != nil {
		return nil, fmt.Errorf("renderer: create double-sided translucent pipeline: %w", err)
	}
	r.onInit(func() {
		r.deviceDriver.DestroyPipeline(r.translucentDoubleSidedPipeline, nil)
		r.deviceDriver.DestroyPipelineLayout(r.translucentDoubleSidedPipelineLayout, nil)
	})

	r.instancedPipeline, r.instancedPipelineLayout, err = createInstancedPipeline(r.deviceDriver, r.shaders, r.renderPass, r.sc.extent, r.descriptorSetLayout, r.shadow.descriptorSetLayout, r.msaaSamples)
	if err != nil {
		return nil, fmt.Errorf("renderer: create instanced pipeline: %w", err)
	}
	r.onInit(func() {
		r.deviceDriver.DestroyPipeline(r.instancedPipeline, nil)
		r.deviceDriver.DestroyPipelineLayout(r.instancedPipelineLayout, nil)
	})

	r.instancedDoubleSidedPipeline, r.instancedDoubleSidedPipelineLayout, err = createInstancedPipeline(r.deviceDriver, r.shaders, r.renderPass, r.sc.extent, r.descriptorSetLayout, r.shadow.descriptorSetLayout, r.msaaSamples, 0)
	if err != nil {
		return nil, fmt.Errorf("renderer: create double-sided instanced pipeline: %w", err)
	}
	r.onInit(func() {
		r.deviceDriver.DestroyPipeline(r.instancedDoubleSidedPipeline, nil)
		r.deviceDriver.DestroyPipelineLayout(r.instancedDoubleSidedPipelineLayout, nil)
	})

	// Sets are created by the game after New returns, so this frees whatever
	// the list holds at teardown rather than a fixed set. AGENTS.md rule 10:
	// the teardown goes next to the thing that allocates. Only what the game
	// never explicitly gave back reaches this: DestroyInstanceSet removes an
	// entry from r.instanceSets as its own deferred free runs, and this stack
	// unwinds after Destroy's own r.flushAllDeferred(), so a set released
	// through DestroyInstanceSet before Destroy was called is already gone
	// from the list by the time this runs, and s.destroy here would otherwise
	// be a second free of the same handles.
	r.onInit(func() {
		for _, s := range r.instanceSets {
			s.destroy(r.deviceDriver)
		}
		r.instanceSets = nil
	})

	r.overlayPipeline, err = createOverlayPipeline(r.deviceDriver, r.shaders, r.renderPass, r.pipelineLayout, r.sc.extent, r.msaaSamples)
	if err != nil {
		return nil, fmt.Errorf("renderer: create overlay pipeline: %w", err)
	}
	r.onInit(func() { r.deviceDriver.DestroyPipeline(r.overlayPipeline, nil) })

	r.celestialPipeline, err = createCelestialPipeline(r.deviceDriver, r.shaders, r.renderPass, r.pipelineLayout, r.sc.extent, r.msaaSamples)
	if err != nil {
		return nil, fmt.Errorf("renderer: create celestial pipeline: %w", err)
	}
	r.onInit(func() { r.deviceDriver.DestroyPipeline(r.celestialPipeline, nil) })

	r.starsPipeline, err = createStarsPipeline(r.deviceDriver, r.shaders, r.renderPass, r.pipelineLayout, r.sc.extent, r.msaaSamples)
	if err != nil {
		return nil, fmt.Errorf("renderer: create stars pipeline: %w", err)
	}
	r.onInit(func() { r.deviceDriver.DestroyPipeline(r.starsPipeline, nil) })

	// The regular sky and additive volumetric sky share the light resources.
	// Create their layout before either pipeline so teardown unwinds both first.
	r.skyPipelineLayout, err = createSkyPipelineLayout(r.deviceDriver, r.descriptorSetLayout, r.shadow.descriptorSetLayout)
	if err != nil {
		return nil, fmt.Errorf("renderer: create sky pipeline layout: %w", err)
	}
	r.onInit(func() { r.deviceDriver.DestroyPipelineLayout(r.skyPipelineLayout, nil) })

	r.skyPipeline, err = createSkyPipeline(r.deviceDriver, r.shaders, r.renderPass, r.skyPipelineLayout, r.sc.extent, r.msaaSamples)
	if err != nil {
		return nil, fmt.Errorf("renderer: create sky pipeline: %w", err)
	}
	r.onInit(func() { r.deviceDriver.DestroyPipeline(r.skyPipeline, nil) })

	// The in-scattering that fronts the sky -- a beam aimed at the night sky,
	// which is the shot this whole feature exists for. See skyvolumetric.frag
	// for why it is a draw of its own rather than part of the dome.

	r.skyVolumetricPipeline, err = createSkyVolumetricPipeline(r.deviceDriver, r.shaders, r.renderPass, r.skyPipelineLayout, r.sc.extent, r.msaaSamples)
	if err != nil {
		return nil, fmt.Errorf("renderer: create sky volumetric pipeline: %w", err)
	}
	r.onInit(func() { r.deviceDriver.DestroyPipeline(r.skyVolumetricPipeline, nil) })

	r.skinnedPipelineLayout, err = createSkinnedPipelineLayout(r.deviceDriver, r.descriptorSetLayout, r.jointDescriptorSetLayout, r.shadow.descriptorSetLayout)
	if err != nil {
		return nil, fmt.Errorf("renderer: create skinned pipeline layout: %w", err)
	}
	r.onInit(func() { r.deviceDriver.DestroyPipelineLayout(r.skinnedPipelineLayout, nil) })

	r.skinnedPipeline, err = createSkinnedPipeline(r.deviceDriver, r.shaders, r.shaders.SkinnedLitFrag, r.renderPass, r.skinnedPipelineLayout, r.sc.extent, r.msaaSamples, false)
	if err != nil {
		return nil, fmt.Errorf("renderer: create skinned pipeline: %w", err)
	}
	r.onInit(func() { r.deviceDriver.DestroyPipeline(r.skinnedPipeline, nil) })

	// The blended skinned variant. It shares skinnedPipelineLayout, so it needs
	// no layout of its own -- unlike the lit variants, whose layout is built
	// per call by createLitVariantPipeline.
	//
	// There is no double-sided twin, and that is deliberate rather than an
	// omission: the opaque skinned path does not have one either, so adding one
	// here would make a translucent character more capable than a solid one.
	r.skinnedTranslucentPipeline, err = createSkinnedPipeline(r.deviceDriver, r.shaders, r.shaders.SkinnedLitFrag, r.renderPass, r.skinnedPipelineLayout, r.sc.extent, r.msaaSamples, true)
	if err != nil {
		return nil, fmt.Errorf("renderer: create translucent skinned pipeline: %w", err)
	}
	r.onInit(func() { r.deviceDriver.DestroyPipeline(r.skinnedTranslucentPipeline, nil) })

	// Same three sets as the plain skinned pipeline, with the material's layout
	// in place of the single texture at set 0. That is the whole difference, and
	// it is why this needs no fourth descriptor set.
	r.skinnedMaterialPipelineLayout, err = createSkinnedPipelineLayout(r.deviceDriver, r.materialSetLayout, r.jointDescriptorSetLayout, r.shadow.descriptorSetLayout)
	if err != nil {
		return nil, fmt.Errorf("renderer: create skinned material pipeline layout: %w", err)
	}
	r.onInit(func() { r.deviceDriver.DestroyPipelineLayout(r.skinnedMaterialPipelineLayout, nil) })

	r.skinnedMaterialPipeline, err = createSkinnedPipeline(r.deviceDriver, r.shaders, r.shaders.SkinnedLitMaterialFrag, r.renderPass, r.skinnedMaterialPipelineLayout, r.sc.extent, r.msaaSamples, false)
	if err != nil {
		return nil, fmt.Errorf("renderer: create skinned material pipeline: %w", err)
	}
	r.onInit(func() { r.deviceDriver.DestroyPipeline(r.skinnedMaterialPipeline, nil) })

	r.grassImpostorPipeline, err = createGrassImpostorPipeline(r.deviceDriver, r.shaders, r.renderPass, r.litPipelineLayout, r.sc.extent, r.msaaSamples)
	if err != nil {
		return nil, fmt.Errorf("renderer: create grass impostor pipeline: %w", err)
	}
	r.onInit(func() { r.deviceDriver.DestroyPipeline(r.grassImpostorPipeline, nil) })

	r.grassPipeline, err = createGrassPipeline(r.deviceDriver, r.shaders, r.renderPass, r.litPipelineLayout, r.sc.extent, r.msaaSamples)
	if err != nil {
		return nil, fmt.Errorf("renderer: create grass pipeline: %w", err)
	}
	// The GrassSystem itself is created later by InitGrass, if ever.
	r.onInit(func() {
		if r.grassImpostor != nil {
			// Registered here rather than at bake time: the atlas is created
			// from InitGrass, long after New has finished pushing teardown, and
			// it has the same lifetime as the grass it stands in for.
			r.grassImpostor.destroy(r)
			r.grassImpostor = nil
		}
		if r.grass != nil {
			// Only the instance buffers: r.grass.models' meshes and textures
			// are not special-cased here because they are not special --
			// LoadGLTF recorded them in r.meshes/r.textures like any other
			// model's, and the generic sweep just above (which runs before
			// this stack unwinds) has already freed them. models only matters
			// to replaceGrass, which retires a generation this field never
			// reaches: r.grass.
			r.grass.Destroy(r.deviceDriver)
		}
		r.deviceDriver.DestroyPipeline(r.grassPipeline, nil)
	})

	r.waterRenderPass, err = createWaterRenderPass(r.deviceDriver, hdrFormat, r.depth.format, r.msaaSamples)
	if err != nil {
		return nil, fmt.Errorf("renderer: create water render pass: %w", err)
	}
	r.onInit(func() { r.deviceDriver.DestroyRenderPass(r.waterRenderPass, nil) })

	r.waterPipeline, err = createWaterPipeline(r.deviceDriver, r.shaders, r.waterRenderPass, r.litPipelineLayout, r.sc.extent, r.msaaSamples)
	if err != nil {
		return nil, fmt.Errorf("renderer: create water pipeline: %w", err)
	}
	r.onInit(func() { r.deviceDriver.DestroyPipeline(r.waterPipeline, nil) })

	r.godRayPipeline, err = createGodRayPipeline(r.deviceDriver, r.shaders, r.waterRenderPass, r.pipelineLayout, r.sc.extent, r.msaaSamples)
	if err != nil {
		return nil, fmt.Errorf("renderer: create god ray pipeline: %w", err)
	}
	r.onInit(func() { r.deviceDriver.DestroyPipeline(r.godRayPipeline, nil) })

	r.particlePipeline, err = createParticlePipeline(r.deviceDriver, r.shaders, r.renderPass, r.pipelineLayout, r.sc.extent, r.msaaSamples)
	if err != nil {
		return nil, fmt.Errorf("renderer: create particle pipeline: %w", err)
	}
	// Likewise the ParticleSystem, created later by InitParticles.
	r.onInit(func() {
		if r.particles != nil {
			r.particles.Destroy(r.deviceDriver)
		}
		r.deviceDriver.DestroyPipeline(r.particlePipeline, nil)
	})

	// Step 9: Framebuffers + command buffers
	var msaaViews []core1_0.ImageView
	if r.msaa != nil {
		msaaViews = r.msaa.views
	}
	if !hdrSupported(r.instanceDriver, r.physicalDevice) {
		// A mandatory format per the Vulkan spec, so this should be
		// unreachable -- but silently rendering somewhere else would be worse
		// than saying so.
		return nil, fmt.Errorf("renderer: device cannot use R16G16B16A16_SFLOAT as a sampleable colour attachment")
	}
	r.hdr, err = createHDRTargets(r.instanceDriver, r.deviceDriver, r.physicalDevice,
		r.descriptorPool, r.descriptorSetLayout, r.sc.extent, len(r.sc.imageViews), r.maxAnisotropy, "HDR target")
	if err != nil {
		return nil, fmt.Errorf("renderer: create HDR targets: %w", err)
	}
	r.onInit(func() { r.hdr.destroy(r.deviceDriver) })

	// Bloom's render passes come before its targets, because the framebuffers
	// are created alongside the images and need a pass to be compatible with.
	r.bloomDownRenderPass, err = createBloomRenderPass(r.deviceDriver, false)
	if err != nil {
		return nil, fmt.Errorf("renderer: create bloom downsample render pass: %w", err)
	}
	r.onInit(func() { r.deviceDriver.DestroyRenderPass(r.bloomDownRenderPass, nil) })

	r.bloomUpRenderPass, err = createBloomRenderPass(r.deviceDriver, true)
	if err != nil {
		return nil, fmt.Errorf("renderer: create bloom upsample render pass: %w", err)
	}
	r.onInit(func() { r.deviceDriver.DestroyRenderPass(r.bloomUpRenderPass, nil) })

	r.bloom, err = createBloomTargets(r.instanceDriver, r.deviceDriver, r.physicalDevice,
		r.descriptorPool, r.descriptorSetLayout, r.bloomDownRenderPass, r.bloomUpRenderPass,
		r.sc.extent, len(r.sc.imageViews))
	if err != nil {
		return nil, fmt.Errorf("renderer: create bloom targets: %w", err)
	}
	r.onInit(func() { r.bloom.destroy(r.deviceDriver) })

	for _, p := range []struct {
		dst      *core1_0.Pipeline
		frag     []byte
		pass     core1_0.RenderPass
		additive bool
	}{
		{&r.bloomPrefilterPipeline, r.shaders.BloomPrefilterFrag, r.bloomDownRenderPass, false},
		{&r.bloomDownPipeline, r.shaders.BloomDownFrag, r.bloomDownRenderPass, false},
		{&r.bloomUpPipeline, r.shaders.BloomUpFrag, r.bloomUpRenderPass, true},
	} {
		*p.dst, err = createBloomPipeline(r.deviceDriver, r.shaders, p.frag, p.pass, r.pipelineLayout, p.additive)
		if err != nil {
			return nil, fmt.Errorf("renderer: create bloom pipeline: %w", err)
		}
		pipeline := *p.dst
		r.onInit(func() { r.deviceDriver.DestroyPipeline(pipeline, nil) })
	}

	// The cloud pass reuses the bloom downsample pass's shape: one half-float
	// colour attachment, contents discarded, ending sampleable.
	r.cloudRenderPass, err = createBloomRenderPass(r.deviceDriver, false)
	if err != nil {
		return nil, fmt.Errorf("renderer: create cloud render pass: %w", err)
	}
	r.onInit(func() { r.deviceDriver.DestroyRenderPass(r.cloudRenderPass, nil) })

	r.clouds, err = createCloudTargets(r.instanceDriver, r.deviceDriver, r.physicalDevice,
		r.descriptorPool, r.descriptorSetLayout, r.cloudRenderPass, r.sc.extent, cloudBufferCount,
		// The lit pipelines' per-frame UBO, bound at binding 1 of the cloud
		// sets so sky.frag and clouds.frag read the sky palette out of the
		// very buffer applyFog reads it from. createShadowResources runs well
		// before this, and a swapchain rebuild remakes these sets while
		// leaving those buffers alone.
		r.shadow.lightVPBuffers)
	if err != nil {
		return nil, fmt.Errorf("renderer: create cloud targets: %w", err)
	}
	r.onInit(func() { r.clouds.destroy(r.deviceDriver) })

	r.cloudPipeline, err = createBloomPipeline(r.deviceDriver, r.shaders, r.shaders.CloudsFrag,
		r.cloudRenderPass, r.pipelineLayout, false)
	if err != nil {
		return nil, fmt.Errorf("renderer: create cloud pipeline: %w", err)
	}
	r.onInit(func() { r.deviceDriver.DestroyPipeline(r.cloudPipeline, nil) })

	r.tonemapSetLayout, err = createTonemapSetLayout(r.deviceDriver)
	if err != nil {
		return nil, fmt.Errorf("renderer: create tonemap set layout: %w", err)
	}
	r.onInit(func() { r.deviceDriver.DestroyDescriptorSetLayout(r.tonemapSetLayout, nil) })

	r.tonemapPipelineLayout, err = createNonLitPipelineLayout(r.deviceDriver, r.tonemapSetLayout)
	if err != nil {
		return nil, fmt.Errorf("renderer: create tonemap pipeline layout: %w", err)
	}
	r.onInit(func() { r.deviceDriver.DestroyPipelineLayout(r.tonemapPipelineLayout, nil) })

	if err := writeTonemapSets(r.deviceDriver, r.descriptorPool, r.tonemapSetLayout, r.hdr, r.bloom); err != nil {
		return nil, fmt.Errorf("renderer: %w", err)
	}

	r.tonemapRenderPass, err = createTonemapRenderPass(r.deviceDriver, r.sc.imageFormat)
	if err != nil {
		return nil, fmt.Errorf("renderer: create tonemap render pass: %w", err)
	}
	r.onInit(func() { r.deviceDriver.DestroyRenderPass(r.tonemapRenderPass, nil) })

	r.tonemapPipeline, err = createResolvePipeline(r.deviceDriver, r.shaders, r.shaders.TonemapFrag, "Tonemap",
		r.tonemapRenderPass, r.tonemapPipelineLayout, r.sc.extent, false)
	if err != nil {
		return nil, fmt.Errorf("renderer: create tonemap pipeline: %w", err)
	}
	r.onInit(func() { r.deviceDriver.DestroyPipeline(r.tonemapPipeline, nil) })

	// The screen-space overlay pipelines belong to the tonemap pass, not the
	// scene pass, which is why they are built down here rather than with the
	// other pipelines above: a pipeline is tied to the render pass it was
	// created against, and these draw onto the resolved swapchain image.
	//
	// Samples1 rather than r.msaaSamples for the same reason: a pipeline's
	// rasterization sample count has to match the samples of the attachments in
	// the render pass it is created against, and MSAA is resolved into the HDR
	// target well before the tonemap runs, so the swapchain attachment here is
	// single-sampled.
	//
	// What that costs was measured rather than assumed, capturing each example
	// either side of the move under a fixed frame clock:
	//
	//	10-text          2801 px differ, max delta 1/255
	//	15-kitchen-sink  1250 px differ, max delta 1/255
	//	13-ui            6031 px differ, 30 of them above 1/255, max 47
	//	17-input        20396 px differ, 136 of them above 1/255, max 23
	//
	// Text does not move at all -- everything in 10-text is one 8-bit rounding
	// step, because MSDF antialiases in the fragment shader and never depended
	// on the rasterizer's coverage. What does move is UI panel edges: the 30
	// pixels in 13-ui are two border columns of one panel, and the 136 in
	// 17-input are the outlines of the small button rects. A panel edge landing
	// between pixels used to be smoothed by MSAA coverage and is now hard.
	//
	// That is the trade, and it is worth taking: a few hundred pixels of panel
	// border against a HUD that survives water, keeps its colour independent of
	// scene exposure, and stops feeding bloom. Antialiasing a panel edge is the
	// UI shader's job anyway, the way the glyph edge is already the distance
	// field's.
	r.msdfPipeline, err = createMSDFPipeline(r.deviceDriver, r.shaders, r.tonemapRenderPass, r.pipelineLayout, r.sc.extent, core1_0.Samples1, false)
	if err != nil {
		return nil, fmt.Errorf("renderer: create MSDF pipeline: %w", err)
	}
	r.onInit(func() { r.deviceDriver.DestroyPipeline(r.msdfPipeline, nil) })

	r.uiPipeline, err = createUIPipeline(r.deviceDriver, r.shaders, r.tonemapRenderPass, r.pipelineLayout, r.sc.extent, core1_0.Samples1, false)
	if err != nil {
		return nil, fmt.Errorf("renderer: create UI pipeline: %w", err)
	}
	r.onInit(func() { r.deviceDriver.DestroyPipeline(r.uiPipeline, nil) })

	// The UI glow layer, when a game asked for one. Everything here is skipped
	// otherwise: no render pass, no pipelines, no images, no descriptor sets,
	// and nothing recorded per frame.
	//
	// The two overlay pipelines are built a SECOND time, against the layer's
	// render pass rather than the tonemap's. They have to be: a pipeline is tied
	// to the render pass it was created against, and these two passes differ in
	// the only thing that matters for compatibility -- the layer is
	// R16G16B16A16_SFLOAT and the swapchain is B8G8R8A8_SRGB. Binding the
	// tonemap pass's UI pipeline inside the layer pass is a validation error at
	// DRAW time, not at creation, and the frame still presents; see
	// docs/agents/overlay-composite.md.
	if r.uiGlowRequested {
		r.uiLayerRenderPass, err = createUILayerRenderPass(r.deviceDriver)
		if err != nil {
			return nil, fmt.Errorf("renderer: %w", err)
		}
		r.onInit(func() { r.deviceDriver.DestroyRenderPass(r.uiLayerRenderPass, nil) })

		r.uiLayerUIPipeline, err = createUIPipeline(r.deviceDriver, r.shaders, r.uiLayerRenderPass, r.pipelineLayout, r.sc.extent, core1_0.Samples1, true)
		if err != nil {
			return nil, fmt.Errorf("renderer: create UI layer panel pipeline: %w", err)
		}
		r.onInit(func() { r.deviceDriver.DestroyPipeline(r.uiLayerUIPipeline, nil) })

		r.uiLayerMSDFPipeline, err = createMSDFPipeline(r.deviceDriver, r.shaders, r.uiLayerRenderPass, r.pipelineLayout, r.sc.extent, core1_0.Samples1, true)
		if err != nil {
			return nil, fmt.Errorf("renderer: create UI layer text pipeline: %w", err)
		}
		r.onInit(func() { r.deviceDriver.DestroyPipeline(r.uiLayerMSDFPipeline, nil) })

		// Against the TONEMAP render pass, because this is the draw that lands
		// on the swapchain, one command after the scene's own resolve.
		r.uiResolvePipeline, err = createResolvePipeline(r.deviceDriver, r.shaders, r.shaders.UIResolveFrag, "UI resolve",
			r.tonemapRenderPass, r.tonemapPipelineLayout, r.sc.extent, true)
		if err != nil {
			return nil, fmt.Errorf("renderer: create UI resolve pipeline: %w", err)
		}
		r.onInit(func() { r.deviceDriver.DestroyPipeline(r.uiResolvePipeline, nil) })
	}

	// The layer's own targets are created further down, after the command pool:
	// they have to be primed into a legal layout before the first frame binds
	// them, and priming submits a clear.

	// The scene draws into the HDR views; only the tonemap pass touches the
	// swapchain.
	r.framebuffers, err = createFramebuffers(r.deviceDriver, r.renderPass, r.hdr.views, r.depth.views, msaaViews, r.sc.extent)
	if err != nil {
		return nil, fmt.Errorf("renderer: create framebuffers: %w", err)
	}
	r.onInit(func() {
		for _, fb := range r.framebuffers {
			r.deviceDriver.DestroyFramebuffer(fb, nil)
		}
	})

	r.tonemapFramebuffers, err = createTonemapFramebuffers(r.deviceDriver, r.tonemapRenderPass, r.sc.imageViews, r.sc.extent)
	if err != nil {
		return nil, fmt.Errorf("renderer: create tonemap framebuffers: %w", err)
	}
	r.onInit(func() {
		for _, fb := range r.tonemapFramebuffers {
			r.deviceDriver.DestroyFramebuffer(fb, nil)
		}
	})

	// Water pass targets. Refraction needs the presented image as a copy
	// source, so a device that cannot mark its swapchain images TRANSFER_SRC
	// simply does not get refraction — the water shader falls back to alpha
	// blending, which is why this is a warning rather than an error.
	if r.sc.captureCapable {
		r.sceneColor, err = createSceneColorTarget(r.instanceDriver, r.deviceDriver, r.physicalDevice,
			r.descriptorPool, r.descriptorSetLayout, r.sc.extent, hdrFormat, r.maxAnisotropy)
		if err != nil {
			return nil, fmt.Errorf("renderer: create scene color target: %w", err)
		}
		r.waterFramebuffers, err = createWaterFramebuffers(r.deviceDriver, r.waterRenderPass, r.hdr.views, r.depth.views, msaaViews, r.sc.extent)
		if err != nil {
			return nil, fmt.Errorf("renderer: create water framebuffers: %w", err)
		}
	} else {
		log.Println("Swapchain images are not transfer-capable: water refraction disabled")
	}
	r.onInit(func() {
		for _, fb := range r.waterFramebuffers {
			r.deviceDriver.DestroyFramebuffer(fb, nil)
		}
		r.waterFramebuffers = nil
		r.sceneColor.destroy(r.deviceDriver)
		r.sceneColor = nil
	})

	r.gpuTimer, err = newGPUTimer(r.instanceDriver, r.deviceDriver, r.physicalDevice, r.indices.graphicsFamily)
	if err != nil {
		return nil, fmt.Errorf("renderer: create GPU timer: %w", err)
	}
	r.onInit(func() { r.gpuTimer.destroy(r.deviceDriver) })

	r.commandPool, err = createCommandPool(r.deviceDriver, r.indices.graphicsFamily)
	if err != nil {
		return nil, fmt.Errorf("renderer: create command pool: %w", err)
	}
	// Destroying the pool frees the command buffers allocated from it.
	r.onInit(func() { r.deviceDriver.DestroyCommandPool(r.commandPool, nil) })

	// Step 9b: the cube shadow maps are sampled every frame but only rendered
	// when a point light casts, so give them defined contents and a legal
	// layout up front. Needs the command pool, which is why it is here rather
	// than in createShadowResources. No teardown step — this only changes the
	// state of images the shadow resources already own.
	if err = r.shadow.initCubeShadowLayout(r); err != nil {
		return nil, fmt.Errorf("renderer: initialize cube shadow layout: %w", err)
	}

	// Same reason, same place: the bloom chain is bound by the resolve's
	// descriptor set every frame but only written when bloom is on, so it needs
	// a defined layout up front or the validation layer reports every frame.
	// The cloud history is sampled on the very first frame, before any march
	// has written it -- same rule, same place, and the same reason this is here
	// rather than at creation: priming needs the command pool.
	if err = r.primeSampledImages(r.clouds.images); err != nil {
		return nil, fmt.Errorf("renderer: prime cloud layouts: %w", err)
	}

	if err = r.primeBloomLayouts(r.bloom); err != nil {
		return nil, fmt.Errorf("renderer: prime bloom layouts: %w", err)
	}

	// The UI glow layer's images and bloom chain, here rather than beside its
	// render pass and pipelines above for exactly the reason the two priming
	// calls above are here: createUILayerTargets primes what it allocates, and
	// priming submits a clear, which needs the command pool.
	if r.uiGlowRequested {
		r.uiLayer, err = r.createUILayerTargets()
		if err != nil {
			return nil, fmt.Errorf("renderer: create UI glow layer: %w", err)
		}
		// Read through r rather than captured: recreateSwapchain swaps this out
		// on every resize, and a closure holding the old pointer would free an
		// already-freed target and leak the live one. Rule 10.
		r.onInit(func() { r.uiLayer.destroy(r.deviceDriver) })
	}

	cmdBufs, err := createCommandBuffers(r.deviceDriver, r.commandPool, maxFramesInFlight)
	if err != nil {
		return nil, fmt.Errorf("renderer: allocate command buffers: %w", err)
	}
	copy(r.commandBuffers[:], cmdBufs)

	// Step 10: Sync objects
	r.sync, err = createSyncObjects(r.deviceDriver)
	if err != nil {
		return nil, fmt.Errorf("renderer: create sync objects: %w", err)
	}
	r.onInit(func() {
		for i := 0; i < maxFramesInFlight; i++ {
			r.deviceDriver.DestroySemaphore(r.sync.imageAvailable[i], nil)
			r.deviceDriver.DestroySemaphore(r.sync.renderFinished[i], nil)
			r.deviceDriver.DestroyFence(r.sync.inFlight[i], nil)
		}
	})

	// Step 11: Fallback texture (1x1 white, needs command pool for staging upload)
	r.fallbackTexture, err = r.createFallbackTexture()
	if err != nil {
		return nil, fmt.Errorf("renderer: create fallback texture: %w", err)
	}

	// The flat normal a Material binds where the caller supplied none. Created
	// here rather than lazily so it shares the fallback texture's lifetime and
	// the shutdown sweep over r.textures, and because it needs the same command
	// pool for its staging upload.
	r.fallbackNormal, err = r.createFallbackNormalTexture()
	if err != nil {
		return nil, fmt.Errorf("renderer: create fallback normal texture: %w", err)
	}

	log.Println("Renderer initialized successfully")
	return r, nil
}

// FallbackTexture returns the 1x1 white texture used when no texture is assigned.
func (r *Renderer) FallbackTexture() *Texture { return r.fallbackTexture }

// InitGrass loads glTF flora models from fsys and scatters instances across the
// heightmap, weighted by each spec's spawn weight. If densityMask is non-nil,
// flora is thinned/cleared based on the mask values.
//
// A second call REPLACES the grass a previous one built, rather than being
// refused. A game that changes GrassModelSpecs or the density mask at
// runtime, or that calls InitGrass again on a level transition, has no other
// way to give the old flora back -- refusing the call would just move the
// abandonment into the caller, which cannot reach r.grass or r.grassImpostor
// to release them itself. Nothing the previous generation allocated -- the
// atlas, the instance buffers, the flora models -- is dropped: replaceGrass
// defers their release past the frames in flight that may still be drawing
// them, the same guarantee DestroyModel gives an explicitly released Model
// (issue #87; see replaceGrass). Calling InitGrass again with the SAME
// arguments is deliberately a no-op on the picture, because the scatter and
// the bake are both pure functions of their inputs -- a caller does not have
// to guard against a redundant call of its own.
func (r *Renderer) InitGrass(fsys fs.FS, hm GrassHeightmap, originX, originZ, worldW, worldD float32, specs []GrassModelSpec, densityMask *GrassDensityMask) {
	var models []*Model
	var weights []float32
	for _, spec := range specs {
		m, err := r.LoadGLTF(fsys, spec.Path)
		if err != nil {
			log.Printf("Failed to load flora model %s: %v", spec.Path, err)
			continue
		}
		models = append(models, m)
		weights = append(weights, spec.Weight)
	}
	if len(models) == 0 {
		log.Println("Flora: no models loaded, skipping")
		return
	}

	gs, err := CreateGrassFromModels(r, models, weights, hm, originX, originZ, worldW, worldD, densityMask)
	if err != nil {
		log.Printf("Failed to create flora: %v", err)
		return
	}
	// Recorded the way modelResources records a LoadGLTF call: what THIS call
	// created, so a later replaceGrass can release exactly that, exactly once.
	gs.models = models

	// Bake the impostor atlas from the meshes just loaded. Doing it here rather
	// than lazily means the cost lands at load with the rest of the flora, and
	// the atlas cannot be out of step with the meshes it stands in for.
	//
	// A bake failure is not fatal: impostors are an optimisation, and a game
	// that cannot have them should still get its grass. It is also not a
	// reason to keep a previous atlas around -- one baked from a different set
	// of variants would silhouette the wrong meshes, which is worse than
	// having none -- so replaceGrass retires whatever came before
	// unconditionally, whether or not this bake succeeded.
	imp, err := r.bakeGrassImpostors(gs, grassImpostorCellSize)
	if err != nil {
		log.Printf("Grass impostor bake failed, meshes will be drawn at all distances: %v", err)
	}

	r.replaceGrass(gs, imp)
}

// replaceGrass installs a newly built grass system and impostor atlas,
// retiring whatever InitGrass built before them.
//
// The swap itself is immediate: r.grass and r.grassImpostor name the new
// generation before this returns, so DrawFrame never draws a mix of the two
// and never draws neither. The retirement behind it is not immediate, and
// cannot be -- a frame submitted just before this call may still be reading
// the previous atlas's descriptor set or the previous GrassSystem's instance
// buffers, exactly the hazard DestroyModel's own deferral exists for (see
// docs/agents/models.md). So the whole release goes through DeferDestroy and
// runs only once every frame that could have been in flight at the moment of
// the swap has retired -- nothing in it may run while a set or a framebuffer
// still names the resource it is about to destroy.
//
// Inside the deferred callback: the atlas first (grassImpostor.destroy frees
// its descriptor set before the view and sampler that set names, for the same
// reason DestroyTexture does), then the instance buffers, then the flora
// models -- through DestroyModel itself, reused rather than reimplemented, so
// a leak in a replaced generation's meshes or textures is caught by the same
// machinery and the same tests that already cover a released Model. Nesting
// DestroyModel's own DeferDestroy inside this one is not a bug:
// flushDeferredDestroys and flushAllDeferred both drain to any depth (see
// TestDeferredDestroyQueuedFromInsideAFlushSurvives) -- it just means a
// replaced generation's flora textures and meshes take one extra
// maxFramesInFlight to actually free, which examples/08-grass's -regrow loop
// budgets for the same way examples/22-level's -reload loop already does for
// DestroyModel calling DestroyMaterial.
func (r *Renderer) replaceGrass(gs *GrassSystem, imp *grassImpostor) {
	prevGrass, prevImpostor := r.grass, r.grassImpostor
	r.grass, r.grassImpostor = gs, imp
	if prevGrass == nil && prevImpostor == nil {
		return // first InitGrass call: nothing to retire
	}
	r.DeferDestroy(func() {
		prevImpostor.destroy(r) // nil-safe: a first-call bake failure leaves this nil
		if prevGrass != nil {
			prevGrass.Destroy(r.deviceDriver)
			for _, m := range prevGrass.models {
				r.DestroyModel(m)
			}
		}
	})
}

// InitParticles allocates the GPU particle system with the given max instance count.
func (r *Renderer) InitParticles(maxInstances int) {
	ps, err := CreateParticleSystem(r, maxInstances)
	if err != nil {
		log.Printf("Failed to create particle system: %v", err)
		return
	}
	r.particles = ps
	log.Printf("Particle system initialized (max %d instances)", maxInstances)
}

// UpdateParticleInstances uploads new particle instance data to the GPU.
func (r *Renderer) UpdateParticleInstances(instances []ParticleInstance) {
	if r.particles == nil {
		return
	}
	r.particles.UpdateInstances(r, instances)
}

// Aspect returns the swapchain aspect ratio.
//
// Answers from the last successful build even mid-rebuild, when r.sc is
// momentarily nil -- see lastExtent.
func (r *Renderer) Aspect() float32 {
	return float32(r.lastExtent.Width) / float32(r.lastExtent.Height)
}

// Extent returns the swapchain pixel dimensions.
//
// Answers from the last successful build even mid-rebuild, when r.sc is
// momentarily nil -- see lastExtent.
func (r *Renderer) Extent() (int, int) {
	return r.lastExtent.Width, r.lastExtent.Height
}

// NotifyResize flags that the framebuffer was resized so the swapchain is
// recreated before the next frame.
func (r *Renderer) NotifyResize() {
	r.framebufferResized = true
}

// SetStateTrace attaches the per-frame state trace DrawFrame writes its half of.
// The engine opens it from GLYPHENGINE_STATE_TRACE and owns closing it; nil
// disables tracing, which is the default. See StateTrace.
func (r *Renderer) SetStateTrace(t *StateTrace) { r.trace = t }

// ProvokeSkipNextFrame makes the next DrawFrame treat its acquire as having
// come back out of date: the swapchain is rebuilt and the frame takes whatever
// path a real one takes from there.
//
// A window being shown, moved to another monitor or DPI-scaled produces that at
// a time nobody controls, and it used to cost the frame -- which is one of the
// few shapes issue #40 could have had. Waiting for the window system to hand
// one over is not a way to test it. Drive it instead, from the engine's
// GLYPHENGINE_PROVOKE_SKIP_FRAMES; the capture must not change.
//
// It stands in before the acquire rather than after a failed one because
// asking and being refused leaves no state behind: no semaphore is signalled,
// no image is held. There is nothing for the substitute to undo.
func (r *Renderer) ProvokeSkipNextFrame() { r.provokeSkip = true }

// DeferDestroy queues a destruction callback that will execute after all
// in-flight frames have finished referencing the resource.
func (r *Renderer) DeferDestroy(fn func()) {
	r.deferredDestroys = append(r.deferredDestroys, deferredDestroy{
		framesLeft: maxFramesInFlight,
		fn:         fn,
	})
}

// flushDeferredDestroys ticks down pending destructions and executes any that
// have waited long enough for all in-flight frames to complete.
//
// The queue is detached before the loop so a callback can queue a destroy of
// its own. That is not hypothetical: DestroyModel's callback calls
// DestroyMaterial, which defers the material's uniform buffer in turn. This
// used to compact in place and then truncate to the surviving count, which
// discarded -- not delayed -- anything appended while it ran, and since
// DestroyMaterial has already dropped the material from r.materials by then,
// Renderer.Destroy's shutdown sweep could not find it either. The only symptom
// was a leaked VkBuffer at vkDestroyDevice.
// TestDeferredDestroyQueuedFromInsideAFlushSurvives is that behaviour pinned.
//
// What a callback queues here waits out its own full countdown from the next
// flush, rather than running inside this one.
func (r *Renderer) flushDeferredDestroys() {
	pending := r.deferredDestroys
	r.deferredDestroys = nil

	n := 0
	for i := range pending {
		pending[i].framesLeft--
		if pending[i].framesLeft <= 0 {
			pending[i].fn()
		} else {
			pending[n] = pending[i]
			n++
		}
	}
	r.deferredDestroys = append(pending[:n], r.deferredDestroys...)
}

// Minimized returns true when the framebuffer is zero-sized (window minimized).
func (r *Renderer) Minimized() bool {
	w, h := r.win.GetFramebufferSize()
	return w == 0 || h == 0
}

// recreateSwapchain tears down and rebuilds the swapchain, depth buffer, and
// framebuffers after a resize or when the surface becomes out of date.
//
// A failure at any step past the swapchain itself unwinds everything this
// call built, through rebuildSwapchainTargets's undo stack, and gives the
// swapchain back too -- so a call that fails leaves the renderer with r.sc
// nil rather than a mix of new and stale handles. That is deliberate: the
// "old" resources this function is about to replace were already destroyed
// below before any recreation was attempted (recreateSwapchain has always
// worked that way -- idle, then destroy, then rebuild), so there is no
// half-old state to fall back to on failure, only a half-new one to give up
// cleanly. acquireImage checks for exactly this (r.sc == nil) and retries the
// rebuild instead of dereferencing it; see issue #86.
func (r *Renderer) recreateSwapchain() error {
	// Skip while minimized — caller should poll and retry next frame.
	width, height := r.win.GetFramebufferSize()
	if width == 0 || height == 0 {
		return nil
	}

	// Remembered because not every rebuild is a resize, and one of the things
	// rebuilt below accumulates across frames. See the cloud targets. Zero
	// when r.sc is already nil -- a retry after a previous attempt failed and
	// unwound everything, including the clouds this comparison exists to
	// spare -- which is correct rather than merely safe: with no live clouds
	// to compare against, this rebuild has to make new ones regardless of
	// whether the size actually moved.
	var oldExtent core1_0.Extent2D
	if r.sc != nil {
		oldExtent = r.sc.extent
	}

	r.deviceDriver.DeviceWaitIdle()

	// Destroy old resources. Every step here is rebuilt unconditionally below
	// (clouds and the UI glow layer are the two exceptions, and each guards
	// its own destroy separately), so every field this section touches is set
	// back to nil immediately -- both because that is what makes the section
	// safe to enter a second time with some of them already nil (a retry
	// after a previous attempt unwound partway through) and because it is
	// what keeps Destroy from freeing a handle destroyed here a second time
	// if a step below fails before reaching that field's recreation.
	for _, fb := range r.framebuffers {
		r.deviceDriver.DestroyFramebuffer(fb, nil)
	}
	r.framebuffers = nil
	for _, fb := range r.tonemapFramebuffers {
		r.deviceDriver.DestroyFramebuffer(fb, nil)
	}
	r.tonemapFramebuffers = nil
	for _, fb := range r.waterFramebuffers {
		r.deviceDriver.DestroyFramebuffer(fb, nil)
	}
	r.waterFramebuffers = nil
	r.sceneColor.destroy(r.deviceDriver)
	r.sceneColor = nil
	// The sets come from the pool, which is not reset here -- each target's
	// destroy gives its own back instead (issue #82). Before it did, a
	// rebuild spent pool capacity that never came back, and the sixteenth
	// rebuild of 13-ui -glow on failed to allocate.
	r.bloom.destroy(r.deviceDriver)
	r.bloom = nil
	r.hdr.destroy(r.deviceDriver)
	r.hdr = nil
	if r.depth != nil {
		r.depth.destroy(r.deviceDriver, len(r.depth.views))
		r.depth = nil
	}
	if r.msaa != nil {
		r.msaa.destroy(r.deviceDriver, len(r.msaa.views))
		r.msaa = nil
	}
	if r.sc != nil {
		for _, iv := range r.sc.imageViews {
			r.deviceDriver.DestroyImageView(iv, nil)
		}
		r.swapchainExt.DestroySwapchain(r.sc.swapchain, nil)
		r.sc = nil
	}

	// From here on, anything created is tracked so a later failure in this
	// same call can give it back rather than leaving a half-new renderer
	// behind. See rebuildUndo -- the same onInit/unwindInit shape New uses,
	// scoped to one rebuild attempt instead of the renderer's whole life.
	var undo rebuildUndo

	newSC, newSwapchainExt, err := createSwapchain(r.deviceDriver, r.surfaceExt, r.surface, r.physicalDevice, r.indices, width, height, r.vsync)
	if err != nil {
		// Nothing created yet in this call -- the old swapchain is already
		// gone above, so there is nothing to unwind.
		return fmt.Errorf("renderer: recreate swapchain: %w", err)
	}
	r.sc, r.swapchainExt = newSC, newSwapchainExt
	undo.push(func() {
		for _, iv := range r.sc.imageViews {
			r.deviceDriver.DestroyImageView(iv, nil)
		}
		r.swapchainExt.DestroySwapchain(r.sc.swapchain, nil)
		r.sc = nil
	})

	if err := r.rebuildSwapchainTargets(oldExtent, &undo); err != nil {
		undo.unwind()
		return err
	}

	// Only now, on full success: Aspect and Extent read this rather than
	// r.sc.extent directly, and must keep answering with whatever the last
	// complete rebuild actually produced for as long as this one has not.
	r.lastExtent = r.sc.extent

	log.Printf("Swapchain recreated: %dx%d", r.sc.extent.Width, r.sc.extent.Height)
	return nil
}

// rebuildSwapchainTargets is recreateSwapchain's body from the depth buffer
// on -- everything sized by the swapchain that createSwapchain itself is
// not. Split out so it can be driven without a live window or a real Vulkan
// device: see TestRebuildSwapchainTargetsUnwindsOnFailure. The swapchain step
// in recreateSwapchain above cannot join it -- createSwapchain goes through
// khr_swapchain.CreateExtensionDriverFromCoreDriver, which dereferences a
// real device's loaded function table, so a fake driver does not fail it, it
// segfaults. That step is covered on the GPU instead, by task validate's
// GLYPHENGINE_PROVOKE_RECREATE_FRAMES runs.
//
// Every step it adds pushes that resource's teardown onto undo before moving
// on, so a later failure in the same call unwinds this step and everything
// before it -- including the swapchain recreateSwapchain already pushed.
// oldExtent is the swapchain extent before this rebuild, zero if there was no
// live swapchain to read it from; only the cloud targets read it.
func (r *Renderer) rebuildSwapchainTargets(oldExtent core1_0.Extent2D, undo *rebuildUndo) error {
	var err error

	r.depth, err = createDepthResources(r.instanceDriver, r.deviceDriver, r.physicalDevice, r.sc.extent, len(r.sc.imageViews), r.msaaSamples)
	if err != nil {
		return fmt.Errorf("renderer: recreate depth resources: %w", err)
	}
	undo.push(func() { r.depth.destroy(r.deviceDriver, len(r.depth.views)); r.depth = nil })

	if r.msaaSamples != core1_0.Samples1 {
		r.msaa, err = createMSAAResources(r.instanceDriver, r.deviceDriver, r.physicalDevice, r.sc.extent, hdrFormat, r.msaaSamples, len(r.sc.imageViews))
		if err != nil {
			return fmt.Errorf("renderer: recreate MSAA resources: %w", err)
		}
		undo.push(func() { r.msaa.destroy(r.deviceDriver, len(r.msaa.views)); r.msaa = nil })
	}
	// Else: r.msaa is already nil, from recreateSwapchain's teardown section.

	var msaaViews []core1_0.ImageView
	if r.msaa != nil {
		msaaViews = r.msaa.views
	}

	// The HDR and bloom targets are swapchain-sized, so they go with it. Both
	// chains and the sets that point into them are size-dependent, so both
	// are rebuilt together.
	r.hdr, err = createHDRTargets(r.instanceDriver, r.deviceDriver, r.physicalDevice,
		r.descriptorPool, r.descriptorSetLayout, r.sc.extent, len(r.sc.imageViews), r.maxAnisotropy, "HDR target")
	if err != nil {
		return fmt.Errorf("renderer: recreate HDR targets: %w", err)
	}
	undo.push(func() { r.hdr.destroy(r.deviceDriver); r.hdr = nil })

	r.bloom, err = createBloomTargets(r.instanceDriver, r.deviceDriver, r.physicalDevice,
		r.descriptorPool, r.descriptorSetLayout, r.bloomDownRenderPass, r.bloomUpRenderPass,
		r.sc.extent, len(r.sc.imageViews))
	if err != nil {
		return fmt.Errorf("renderer: recreate bloom targets: %w", err)
	}
	undo.push(func() { r.bloom.destroy(r.deviceDriver); r.bloom = nil })

	// The cloud targets are the one thing here that accumulates, and they are
	// sized by the extent and nothing else -- their count is a constant and
	// they hold no swapchain image. So they are rebuilt only when the extent
	// actually moved.
	//
	// The history chain is meaningless across a *resize*: the buffers are a
	// different size and were rendered through a different projection. It is
	// perfectly good across a rebuild that kept the size, and not every rebuild
	// is a resize -- showing a window, moving it to another monitor, a
	// compositor change and a suboptimal present all produce an out-of-date
	// swapchain at the same extent. Throwing the history away on those restarts
	// the temporal blend, and how much that moves the picture depends on how
	// late it happened: measured on 08-grass -timeofday 0.28 -frames 150 under
	// a fixed clock, a rebuild forced on frame 40 moved 1685 pixels and one
	// forced on frame 149 moved 257601 of them (27.95 %, max delta 104).
	// task determinism's rebuild case is what holds this.
	if r.sc.extent != oldExtent {
		r.clouds.destroy(r.deviceDriver)
		r.clouds = nil
		r.clouds, err = createCloudTargets(r.instanceDriver, r.deviceDriver, r.physicalDevice,
			r.descriptorPool, r.descriptorSetLayout, r.cloudRenderPass, r.sc.extent, cloudBufferCount,
			r.shadow.lightVPBuffers)
		if err != nil {
			return fmt.Errorf("renderer: recreate cloud targets: %w", err)
		}
		undo.push(func() { r.clouds.destroy(r.deviceDriver); r.clouds = nil })

		// Priming gives a defined layout, and the reprojection rejects the
		// contents on the next frame anyway because nothing was written at the
		// new size yet.
		if err := r.primeSampledImages(r.clouds.images); err != nil {
			return fmt.Errorf("renderer: prime cloud layouts: %w", err)
		}
	}

	if err := r.primeBloomLayouts(r.bloom); err != nil {
		return fmt.Errorf("renderer: prime bloom layouts: %w", err)
	}

	// The UI glow layer is swapchain-sized too, so it goes with it -- images,
	// bloom chain, framebuffers and the resolve's descriptor sets, which name
	// specific views and would otherwise sample freed ones. Its render pass and
	// its three pipelines survive: the extent reaches them through dynamic
	// viewport and scissor state, exactly as it does for the tonemap pass's.
	if r.uiLayer != nil {
		r.uiLayer.destroy(r.deviceDriver)
		r.uiLayer = nil
		r.uiLayer, err = r.createUILayerTargets()
		if err != nil {
			return fmt.Errorf("renderer: recreate UI glow layer: %w", err)
		}
		undo.push(func() { r.uiLayer.destroy(r.deviceDriver); r.uiLayer = nil })
	}

	// The resolve's sets name specific views, so they have to be rewritten
	// against the new images. Skipping this leaves the tonemap sampling freed
	// image views, which the validation layer catches and a release build does
	// not. Nothing to push here: it writes into r.hdr.tonemapSets, which the
	// HDR target's own teardown above already frees.
	if err := writeTonemapSets(r.deviceDriver, r.descriptorPool, r.tonemapSetLayout, r.hdr, r.bloom); err != nil {
		return fmt.Errorf("renderer: recreate tonemap sets: %w", err)
	}

	r.framebuffers, err = createFramebuffers(r.deviceDriver, r.renderPass, r.hdr.views, r.depth.views, msaaViews, r.sc.extent)
	if err != nil {
		return fmt.Errorf("renderer: recreate framebuffers: %w", err)
	}
	undo.push(func() {
		for _, fb := range r.framebuffers {
			r.deviceDriver.DestroyFramebuffer(fb, nil)
		}
		r.framebuffers = nil
	})

	r.tonemapFramebuffers, err = createTonemapFramebuffers(r.deviceDriver, r.tonemapRenderPass, r.sc.imageViews, r.sc.extent)
	if err != nil {
		return fmt.Errorf("renderer: recreate tonemap framebuffers: %w", err)
	}
	undo.push(func() {
		for _, fb := range r.tonemapFramebuffers {
			r.deviceDriver.DestroyFramebuffer(fb, nil)
		}
		r.tonemapFramebuffers = nil
	})

	if r.sc.captureCapable {
		r.sceneColor, err = createSceneColorTarget(r.instanceDriver, r.deviceDriver, r.physicalDevice,
			r.descriptorPool, r.descriptorSetLayout, r.sc.extent, hdrFormat, r.maxAnisotropy)
		if err != nil {
			return fmt.Errorf("renderer: recreate scene color target: %w", err)
		}
		undo.push(func() { r.sceneColor.destroy(r.deviceDriver); r.sceneColor = nil })

		r.waterFramebuffers, err = createWaterFramebuffers(r.deviceDriver, r.waterRenderPass, r.hdr.views, r.depth.views, msaaViews, r.sc.extent)
		if err != nil {
			return fmt.Errorf("renderer: recreate water framebuffers: %w", err)
		}
		undo.push(func() {
			for _, fb := range r.waterFramebuffers {
				r.deviceDriver.DestroyFramebuffer(fb, nil)
			}
			r.waterFramebuffers = nil
		})
	}

	return nil
}

// acquireImage gets this frame's swapchain image, rebuilding the swapchain and
// asking once more if the presentation engine says the old one is out of date.
// The second return is false when the frame cannot be drawn at all.
//
// Asking again rather than dropping the frame is what keeps a capture
// repeatable. A dropped frame costs the simulation a step the renderer never
// takes, and everything downstream that spans frames -- the cloud history
// above all -- then sits one step behind for the rest of the run. Under a fixed
// clock that turns `-frames 150` into two different pictures depending on
// whether the window system produced an out-of-date acquire, which it does at
// times nobody controls: while a window is being shown, when it moves between
// monitors, when the compositor changes mode.
//
// The one case that still drops the frame is a rebuild that changed the
// extent. Everything the caller built this frame from -- the projection
// matrices, the light binning, the viewport -- came from the old one, so
// drawing it into the new size would stretch it; the next frame is built
// against the new extent and is the first one that can be right. A resize
// changes the picture anyway, so nothing repeatable is lost.
func (r *Renderer) acquireImage(f int) (int, bool, error) {
	// A previous rebuild failed partway through and unwound back to no
	// swapchain at all (see recreateSwapchain); there is nothing to acquire
	// from until a rebuild succeeds, so go straight to one rather than
	// dereferencing the swapchain that is not there. Issue #86.
	if r.sc == nil {
		return r.rebuildAndAcquire(f)
	}

	// A provoked out-of-date acquire takes the same path as a real one, minus
	// the failed call: asking and being refused leaves no state behind, so
	// there is nothing to undo. See ProvokeSkipNextFrame.
	if r.provokeSkip {
		r.provokeSkip = false
		r.trace.Str("provoked", "out-of-date")
		return r.rebuildAndAcquire(f)
	}

	imageIndex, result, err := r.swapchainExt.AcquireNextImage(r.sc.swapchain, common.NoTimeout, &r.sync.imageAvailable[f], nil)
	if err == nil {
		return imageIndex, true, nil
	}
	if result != khr_swapchain.VKErrorOutOfDate {
		return 0, false, err
	}
	return r.rebuildAndAcquire(f)
}

// rebuildAndAcquire recreates the swapchain and acquires from the new one.
func (r *Renderer) rebuildAndAcquire(f int) (int, bool, error) {
	// Zero when there is no swapchain to compare against yet -- either the
	// first call, or a retry after a previous rebuild unwound everything.
	// r.sc.extent != before is then true as soon as the retry succeeds (a
	// real extent is never the zero value while the window is not
	// minimized), which is the right answer: this frame is skipped and the
	// next one is built against whatever the recovered extent actually is,
	// the same as an ordinary resize.
	var before core1_0.Extent2D
	if r.sc != nil {
		before = r.sc.extent
	}
	if err := r.recreateSwapchain(); err != nil {
		return 0, false, err
	}
	// recreateSwapchain returns without rebuilding while the framebuffer is
	// zero-sized, so the swapchain is still the out-of-date one (or, after a
	// previous rebuild failed partway through, simply gone) and asking again
	// would fail the same way or dereference nil. The caller skips minimized
	// frames; this is the window that closes between that check and here.
	if w, h := r.win.GetFramebufferSize(); w == 0 || h == 0 {
		return 0, false, nil
	}
	if r.sc == nil {
		// recreateSwapchain returns nil only when it skipped (the check just
		// above) or when it fully rebuilt r.sc, so this is not reachable --
		// kept because "acquire never dereferences a nil swapchain" is
		// exactly the contract issue #86 asks for, and a defensive check
		// earning its place costs one comparison a rebuild.
		return 0, false, nil
	}
	if r.sc.extent != before {
		return 0, false, nil
	}

	imageIndex, result, err := r.swapchainExt.AcquireNextImage(r.sc.swapchain, common.NoTimeout, &r.sync.imageAvailable[f], nil)
	if err != nil {
		if result == khr_swapchain.VKErrorOutOfDate {
			// Out of date again immediately: the surface is still moving. Let
			// the next frame deal with it rather than looping here.
			return 0, false, nil
		}
		return 0, false, err
	}
	return imageIndex, true, nil
}

// DrawFrame records and submits one frame: waits for the in-flight fence, acquires
// a swapchain image, records draw commands, submits to the GPU, and presents.
func (r *Renderer) DrawFrame(draws []RenderObject, overlays []RenderObject, celestials []RenderObject, uiOverlays []UIRenderObject, msdfOverlays []RenderObject, lighting SceneLighting) error {
	f := r.currentFrame

	if t := r.trace; t != nil {
		t.Int("slot", f)
		// r.sc is nil here only when a previous frame's rebuild failed
		// partway through (see recreateSwapchain) and acquireImage below has
		// not yet retried it -- skip the two fields that name it rather than
		// dereference it for a trace line.
		if r.sc != nil {
			t.Int("w", r.sc.extent.Width)
			t.Int("h", r.sc.extent.Height)
		}
		t.Int("cloudframe", r.cloudFrame)
	}

	waitStart := time.Now()

	// Wait for this in-flight frame's previous submission to finish
	_, err := r.deviceDriver.WaitForFences(true, common.NoTimeout, r.sync.inFlight[f])
	if err != nil {
		r.trace.Str("outcome", "fence-error")
		return err
	}

	// The fence guarantees this slot's previous submission finished, which makes
	// it both the earliest point its timestamps are readable and the last point
	// before they are reset. Collecting anywhere else needs a stall.
	r.gpuTimer.collect(r.deviceDriver, f)

	// Safe to destroy resources queued for deferred destruction now that
	// a fence has been waited on.
	r.flushDeferredDestroys()

	imageIndex, drawable, err := r.acquireImage(f)
	if err != nil {
		r.trace.Str("outcome", "acquire-error")
		return err
	}
	if !drawable {
		// Nothing is recorded, submitted or presented on this path, so the
		// frame the engine simulated is never drawn. Naming it in the trace is
		// the point: an iteration that ends here and one that ends in a present
		// look identical from outside.
		r.trace.Str("outcome", "skip-resized")
		return nil
	}
	r.trace.Int("image", imageIndex)

	// Both the fence and the acquire are waits on the presentation pipeline, so
	// they count together: with vsync on it is the acquire that blocks.
	r.lastFenceWait = time.Since(waitStart)

	// Only reset the fence after a successful acquire
	_, err = r.deviceDriver.ResetFences(r.sync.inFlight[f])
	if err != nil {
		return err
	}

	// Copy staged joint matrices, particle instances, and dynamic mesh data
	// into this frame's buffers now that its fence has signaled (the GPU is
	// done reading them).
	r.flushJointUploads(f)

	// Which side of the water each blended draw is on, worked out once. The
	// recorder needs it to decide where to record, and the particle instance
	// buffer has to be partitioned on the same answer BEFORE it is uploaded --
	// two computations of the same thing could disagree, and the failure would
	// be a few particles in the wrong pass, which nothing would flag.
	//
	// Guarded on sceneColor because that is what the water pass is guarded on:
	// a device whose swapchain images are not transfer-capable draws no water
	// at all, so there is nothing for blended geometry to be in front of.
	var split blendSplit
	if r.sceneColor != nil {
		r.waterPlanes = appendWaterPlanes(r.waterPlanes[:0], draws)
		split = blendSplit{planes: r.waterPlanes, eyeY: lighting.CameraPos[1]}
	}
	if r.particles != nil {
		r.particles.splitAtWater(split)
		r.particles.flushUploads(f)
	}
	r.flushDynamicMeshes(f)

	// Hashed after the flushes, so what is recorded is what this frame's slot
	// actually holds rather than what was staged -- a dirty flag that failed to
	// fire is precisely the kind of drift worth catching, and hashing the
	// staging copy instead would hide it.
	if t := r.trace; t != nil {
		r.traceStreamedBuffers(t)
		traceLighting(t, lighting)
	}

	// Always upload the cascade VP matrices so the fragment shader never reads
	// stale data. When shadows are disabled, the VPs are zero matrices, causing
	// all fragments to project to the shadow map origin where depth=1.0
	// (cleared) → fully lit.
	r.shadow.uploadLitUBO(f, lighting.CascadeVPs, lighting.NightGrade, lighting.SkyPalette, lighting.Volumetrics)
	r.flushShaderParameters(f)
	if t := r.trace; t != nil {
		t.Hash("shaderparams", NewHash.Bytes(r.shadow.shaderParameterMapped[f]))
	}
	r.shadow.uploadLights(f, lighting.Lights, lighting.Clusters, lighting.LightFlags, r.sc.extent)

	// The water pass is optional: a device without TRANSFER_SRC on its
	// swapchain images cannot supply the refraction source, and scenes with no
	// water never begin the pass at all.
	var waterFB core1_0.Framebuffer
	if r.sceneColor != nil && imageIndex < len(r.waterFramebuffers) {
		waterFB = r.waterFramebuffers[imageIndex]
	}

	// Reset and record this frame's command buffer
	cmdBuf := r.commandBuffers[f]
	_, err = r.deviceDriver.ResetCommandBuffer(cmdBuf, 0)
	if err != nil {
		r.trace.Str("outcome", "reset-error")
		return err
	}
	recordStart := time.Now()
	err = recordCommandBuffer(r.deviceDriver, cmdBuf, r.renderPass, r.framebuffers[imageIndex], r.pipeline, r.litDoubleSidedPipeline, r.translucentPipeline, r.translucentDoubleSidedPipeline, r.skinnedTranslucentPipeline, r.instancedPipeline, r.instancedDoubleSidedPipeline, r.overlayPipeline, r.skyPipeline, r.skyVolumetricPipeline, r.starsPipeline, r.celestialPipeline, r.uiPipeline, r.msdfPipeline, r.skinnedPipeline, r.grassPipeline, r.waterPipeline, r.godRayPipeline, r.waterRenderPass, waterFB, r.sceneColor, r.hdr.images[imageIndex],
		func(cb core1_0.CommandBuffer) error { return r.recordClouds(cb, lighting, f) },
		r.cloudSetFor(f),
		r.bloomFor(imageIndex), r.tonemapFor(imageIndex), r.particlePipeline, r.terrainPipeline, r.materialPipelines(), &r.stats, r.pipelineLayout, r.skyPipelineLayout, r.litPipelineLayout, r.skinnedPipelineLayout, r.terrainPipelineLayout, r.sc.extent, draws, overlays, celestials, uiOverlays, msdfOverlays, lighting, split, r.fallbackTexture, r.milkyWayTex, r.shadow, r.grass, r.grassLOD, r.grassImpostor, r.grassImpostorPipeline, r.particles, f, r.msaa != nil, r.gpuTimer, r.trace, &r.cmdScratch)
	if err != nil {
		r.trace.Str("outcome", "record-error")
		return err
	}

	// Submit
	fence := r.sync.inFlight[f]
	r.lastRecord = time.Since(recordStart)

	_, err = r.deviceDriver.QueueSubmit(r.graphicsQueue, &fence, core1_0.SubmitInfo{
		WaitSemaphores:   []core1_0.Semaphore{r.sync.imageAvailable[f]},
		WaitDstStageMask: []core1_0.PipelineStageFlags{core1_0.PipelineStageColorAttachmentOutput},
		CommandBuffers:   []core1_0.CommandBuffer{cmdBuf},
		SignalSemaphores: []core1_0.Semaphore{r.sync.renderFinished[f]},
	})
	if err != nil {
		r.trace.Str("outcome", "submit-error")
		return err
	}

	// Present
	r.lastPresented = imageIndex
	presentStart := time.Now()
	presentResult, err := r.swapchainExt.QueuePresent(r.presentQueue, khr_swapchain.PresentInfo{
		WaitSemaphores: []core1_0.Semaphore{r.sync.renderFinished[f]},
		Swapchains:     []khr_swapchain.Swapchain{r.sc.swapchain},
		ImageIndices:   []int{imageIndex},
	})
	r.lastPresent = time.Since(presentStart)
	if err != nil && presentResult != khr_swapchain.VKErrorOutOfDate {
		r.trace.Str("outcome", "present-error")
		return err
	}
	// Advance the cloud history chain and remember what this frame was rendered
	// with, so the next one can reproject into it.
	//
	// Above the rebuild check rather than below it. This frame was recorded,
	// submitted and presented whatever the present result said, so its
	// bookkeeping is due either way. Skipping it on a suboptimal present left
	// the next frame marching clouds into the buffer it was supposed to be
	// reading as history, and reprojecting with the view-projection of the
	// frame before last -- a broken chain rather than merely a different one.
	r.cloudFrame++
	r.prevVP = lighting.VP
	r.prevCloudTime = lighting.Time
	r.currentFrame = (f + 1) % maxFramesInFlight

	if presentResult == khr_swapchain.VKErrorOutOfDate || presentResult == khr_swapchain.VKSuboptimal || r.framebufferResized {
		// The frame WAS drawn and presented here, unlike the acquire path, so
		// the trace distinguishes the two rather than calling both "recreate".
		r.trace.Str("outcome", "present-recreate")
		r.framebufferResized = false
		return r.recreateSwapchain()
	}

	r.trace.Str("outcome", "present")
	return nil
}

// traceStreamedBuffers records what this frame's slot holds in each buffer the
// renderer streams: the particle instances, the dynamic meshes, and the order
// the dynamic-mesh map was walked in.
//
// The map order is traced as a field of its own rather than folded into the
// contents hash. Go randomises map iteration per range, so if that order ever
// reached the image the contents hash alone would say the data matched while
// the picture did not -- which is the exact shape of bug this is hunting.
func (r *Renderer) traceStreamedBuffers(t *StateTrace) {
	if r.particles != nil {
		t.CountHash("particles", len(r.particles.staging), HashPOD(NewHash, r.particles.staging))
		t.Int("particlesbehind", r.particles.behind)
	}
	// Contents combined with XOR so it does not depend on the walk order, and
	// the walk order hashed separately so it is visible either way.
	var contents Hasher
	order := NewHash
	n := 0
	for m, dm := range r.dynamicMeshes {
		h := HashPOD(HashPOD(NewHash, dm.stagingV), dm.stagingI)
		h = h.Int(m.VertexCount).Int(m.IndexCount)
		contents ^= h
		order = order.Uint64(uint64(h))
		n++
	}
	t.CountHash("dynmesh", n, contents)
	t.Hash("dynmeshorder", order)

	// Renderer state a game -- or a stray keypress under WithDebugKeys, which
	// toggles bloom and cycles the tonemap curve -- can move at any time.
	// Nothing else in a line would show it: the draw lists and the lighting
	// would be identical and the whole frame would come out different, which
	// is the worst kind of divergence to be handed with no field pointing at
	// it.
	t.Hash("post", NewHash.
		Float32(r.exposure).Float32(r.tonemapCurve).Float32(r.tonemapWhite).
		Float32(r.bloomIntensity).Float32(r.bloomThreshold).
		Float32(r.bloomKnee).Float32(r.bloomRadius))
	t.Hash("grasslod", NewHash.
		Float32(r.grassLOD.ThinNear).Float32(r.grassLOD.ThinFar).
		Float32(r.grassLOD.ThinMin).Float32(r.grassLOD.MaxDistance).
		Float32(r.grassLOD.FadeStart).Float32(r.grassLOD.ImpostorDistance))
}

// traceLighting records the per-frame uniform state: the shadow cascades and
// the clustered-light binning.
//
// Both are derived from the camera and the sun, which the engine already
// hashes, so a divergence here with an identical cam= and sky= means the
// derivation itself moved -- a binner reading a different extent, a cascade
// fit that saw a different centre. Worth a field of its own for that reason:
// it is the difference between "the simulation moved" and "the same simulation
// produced different GPU state".
func traceLighting(t *StateTrace, l SceneLighting) {
	h := NewHash.Bool(l.ShadowEnabled)
	for c := range l.CascadeVPs {
		h = h.Float32s(l.CascadeVPs[c][:])
	}
	t.Hash("cascades", h)

	lh := NewHash.Uint64(uint64(l.LightFlags))
	lh = HashPOD(lh, l.Lights)
	n := 0
	if l.Clusters != nil {
		lh = HashPOD(lh, l.Clusters.Order)
		lh = HashPOD(lh, l.Clusters.Cells)
		lh = HashPOD(lh, l.Clusters.Indices)
		n = len(l.Clusters.Order)
	}
	t.CountHash("lights", n, lh)
}

// Destroy waits for the GPU to idle, releases everything the application
// created through the Renderer (textures, meshes, the lazy triangle pipeline),
// then unwinds the init stack recorded by New.
//
// Teardown order lives in exactly one place — the order the steps were pushed
// in New — so it cannot drift away from creation order the way a hand-written
// reverse listing does.
//
// Safe to call more than once.
func (r *Renderer) Destroy() {
	if r.destroyed {
		return
	}
	r.destroyed = true

	if r.deviceDriver == nil {
		// New failed before the logical device existed and already unwound.
		return
	}
	r.deviceDriver.DeviceWaitIdle()

	// Flush deferred destroys now that the GPU is idle.
	r.flushAllDeferred()

	// Application-owned resources, which are created after New and so are not
	// on the init stack.
	r.destroyTrianglePipeline()

	// Hand the tracking lists off before destroying anything. The Destroy*
	// methods deregister as they go, and mutating a slice while ranging over it
	// shifts the backing array under the loop index -- which silently skips
	// every second entry and leaks it.
	textures, meshes, joints := r.textures, r.meshes, r.jointBuffers
	materials := r.materials
	r.textures, r.meshes, r.jointBuffers, r.materials = nil, nil, nil, nil
	// Materials before textures: a material holds views and samplers the
	// textures own, and its descriptor set has to stop referencing them first.
	for _, m := range materials {
		r.DestroyMaterial(m)
	}
	for _, t := range textures {
		r.DestroyTexture(t)
	}
	for _, m := range meshes {
		r.DestroyMesh(m)
	}
	// Joint buffers are swept for the same reason meshes are: an application
	// that loads a skinned model and never explicitly releases it should still
	// shut down clean.
	for _, jb := range joints {
		r.DestroyJointBuffer(jb)
	}

	// Dynamic meshes queue their buffer destruction via DeferDestroy; the GPU
	// is already idle, so run anything queued during the loop above.
	r.flushAllDeferred()

	r.unwindInit()

	log.Println("Renderer destroyed")
}

// flushAllDeferred runs every pending deferred destroy immediately, ignoring
// the frame countdown. Only valid once the GPU is known to be idle.
//
// It drains until the queue is empty rather than making one pass, for the same
// reason flushDeferredDestroys detaches: a callback can queue another (see
// DestroyModel), and a single pass leaves that one behind for the validation
// layer to report as a leak at vkDestroyDevice. Destroy calls this twice, which
// covered exactly one level of nesting by accident; this covers any depth on
// purpose.
func (r *Renderer) flushAllDeferred() {
	for len(r.deferredDestroys) > 0 {
		pending := r.deferredDestroys
		r.deferredDestroys = nil
		for _, dd := range pending {
			dd.fn()
		}
	}
}
