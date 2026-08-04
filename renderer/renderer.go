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

	sc                     *swapchainDetails
	renderPass             core1_0.RenderPass
	pipelineLayout         core1_0.PipelineLayout // non-lit (sky, stars, overlay, msdf, ui)
	litPipelineLayout      core1_0.PipelineLayout // lit static: set 0=tex, set 1=shadow
	pipeline               core1_0.Pipeline
	litDoubleSidedPipeline core1_0.Pipeline
	// Identical in layout to litPipelineLayout, but a distinct object:
	// createGraphicsPipeline builds one per call.
	litDoubleSidedPipelineLayout core1_0.PipelineLayout
	overlayPipeline              core1_0.Pipeline
	starsPipeline                core1_0.Pipeline
	skyPipeline                  core1_0.Pipeline
	uiPipeline                   core1_0.Pipeline
	msdfPipeline                 core1_0.Pipeline
	jointDescriptorSetLayout     core1_0.DescriptorSetLayout
	skinnedPipelineLayout        core1_0.PipelineLayout // skinned: set 0=tex, set 1=joints, set 2=shadow
	skinnedPipeline              core1_0.Pipeline
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
	// grassLOD is the distance tuning grass thins, fades and culls by.
	// Defaulted at construction so a zero value never culls grass at zero.
	grassLOD GrassLOD
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

	// stats counts what the frame submitted; see stats.go.
	stats RenderStats

	// gpuTimer measures per-pass GPU cost with timestamp queries; see gputimer.go.
	// Non-nil always, but inert when the device cannot timestamp graphics work.
	gpuTimer *gpuTimer

	descriptorSetLayout core1_0.DescriptorSetLayout
	descriptorPool      core1_0.DescriptorPool
	fallbackTexture     *Texture
	textures            []*Texture

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
	meshes             []*Mesh
	jointBuffers       []*JointBuffer
	dynamicMeshes      map[*Mesh]*dynamicMesh
	maxAnisotropy      float32 // 0 = anisotropic filtering unavailable

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
	// Reads r.sc at unwind time: recreateSwapchain replaces it on resize.
	r.onInit(func() {
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
	r.onInit(func() { r.depth.destroy(r.deviceDriver, len(r.depth.views)) })

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

	// Non-lit pipeline layout (set 0 = texture only) for sky, stars, overlay, msdf, ui
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

	r.overlayPipeline, err = createOverlayPipeline(r.deviceDriver, r.shaders, r.renderPass, r.pipelineLayout, r.sc.extent, r.msaaSamples)
	if err != nil {
		return nil, fmt.Errorf("renderer: create overlay pipeline: %w", err)
	}
	r.onInit(func() { r.deviceDriver.DestroyPipeline(r.overlayPipeline, nil) })

	r.starsPipeline, err = createStarsPipeline(r.deviceDriver, r.shaders, r.renderPass, r.pipelineLayout, r.sc.extent, r.msaaSamples)
	if err != nil {
		return nil, fmt.Errorf("renderer: create stars pipeline: %w", err)
	}
	r.onInit(func() { r.deviceDriver.DestroyPipeline(r.starsPipeline, nil) })

	r.skyPipeline, err = createSkyPipeline(r.deviceDriver, r.shaders, r.renderPass, r.pipelineLayout, r.sc.extent, r.msaaSamples)
	if err != nil {
		return nil, fmt.Errorf("renderer: create sky pipeline: %w", err)
	}
	r.onInit(func() { r.deviceDriver.DestroyPipeline(r.skyPipeline, nil) })

	r.msdfPipeline, err = createMSDFPipeline(r.deviceDriver, r.shaders, r.renderPass, r.pipelineLayout, r.sc.extent, r.msaaSamples)
	if err != nil {
		return nil, fmt.Errorf("renderer: create MSDF pipeline: %w", err)
	}
	r.onInit(func() { r.deviceDriver.DestroyPipeline(r.msdfPipeline, nil) })

	r.uiPipeline, err = createUIPipeline(r.deviceDriver, r.shaders, r.renderPass, r.pipelineLayout, r.sc.extent, r.msaaSamples)
	if err != nil {
		return nil, fmt.Errorf("renderer: create UI pipeline: %w", err)
	}
	r.onInit(func() { r.deviceDriver.DestroyPipeline(r.uiPipeline, nil) })

	r.skinnedPipelineLayout, err = createSkinnedPipelineLayout(r.deviceDriver, r.descriptorSetLayout, r.jointDescriptorSetLayout, r.shadow.descriptorSetLayout)
	if err != nil {
		return nil, fmt.Errorf("renderer: create skinned pipeline layout: %w", err)
	}
	r.onInit(func() { r.deviceDriver.DestroyPipelineLayout(r.skinnedPipelineLayout, nil) })

	r.skinnedPipeline, err = createSkinnedPipeline(r.deviceDriver, r.shaders, r.shaders.SkinnedLitFrag, r.renderPass, r.skinnedPipelineLayout, r.sc.extent, r.msaaSamples)
	if err != nil {
		return nil, fmt.Errorf("renderer: create skinned pipeline: %w", err)
	}
	r.onInit(func() { r.deviceDriver.DestroyPipeline(r.skinnedPipeline, nil) })

	// Same three sets as the plain skinned pipeline, with the material's layout
	// in place of the single texture at set 0. That is the whole difference, and
	// it is why this needs no fourth descriptor set.
	r.skinnedMaterialPipelineLayout, err = createSkinnedPipelineLayout(r.deviceDriver, r.materialSetLayout, r.jointDescriptorSetLayout, r.shadow.descriptorSetLayout)
	if err != nil {
		return nil, fmt.Errorf("renderer: create skinned material pipeline layout: %w", err)
	}
	r.onInit(func() { r.deviceDriver.DestroyPipelineLayout(r.skinnedMaterialPipelineLayout, nil) })

	r.skinnedMaterialPipeline, err = createSkinnedPipeline(r.deviceDriver, r.shaders, r.shaders.SkinnedLitMaterialFrag, r.renderPass, r.skinnedMaterialPipelineLayout, r.sc.extent, r.msaaSamples)
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
			r.grassImpostor.destroy(r.deviceDriver)
			r.grassImpostor = nil
		}
		if r.grass != nil {
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
		r.descriptorPool, r.descriptorSetLayout, r.sc.extent, len(r.sc.imageViews), r.maxAnisotropy)
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
		r.descriptorPool, r.descriptorSetLayout, r.cloudRenderPass, r.sc.extent, cloudBufferCount)
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

	r.tonemapPipeline, err = createTonemapPipeline(r.deviceDriver, r.shaders, r.tonemapRenderPass, r.tonemapPipelineLayout, r.sc.extent)
	if err != nil {
		return nil, fmt.Errorf("renderer: create tonemap pipeline: %w", err)
	}
	r.onInit(func() { r.deviceDriver.DestroyPipeline(r.tonemapPipeline, nil) })

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
	r.grass = gs

	// Bake the impostor atlas from the meshes just loaded. Doing it here rather
	// than lazily means the cost lands at load with the rest of the flora, and
	// the atlas cannot be out of step with the meshes it stands in for.
	//
	// A bake failure is not fatal: impostors are an optimisation, and a game
	// that cannot have them should still get its grass.
	if err := r.bakeGrassImpostors(grassImpostorCellSize); err != nil {
		log.Printf("Grass impostor bake failed, meshes will be drawn at all distances: %v", err)
	}
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
func (r *Renderer) Aspect() float32 {
	return float32(r.sc.extent.Width) / float32(r.sc.extent.Height)
}

// Extent returns the swapchain pixel dimensions.
func (r *Renderer) Extent() (int, int) {
	return r.sc.extent.Width, r.sc.extent.Height
}

// NotifyResize flags that the framebuffer was resized so the swapchain is
// recreated before the next frame.
func (r *Renderer) NotifyResize() {
	r.framebufferResized = true
}

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
func (r *Renderer) flushDeferredDestroys() {
	n := 0
	for i := range r.deferredDestroys {
		r.deferredDestroys[i].framesLeft--
		if r.deferredDestroys[i].framesLeft <= 0 {
			r.deferredDestroys[i].fn()
		} else {
			r.deferredDestroys[n] = r.deferredDestroys[i]
			n++
		}
	}
	r.deferredDestroys = r.deferredDestroys[:n]
}

// Minimized returns true when the framebuffer is zero-sized (window minimized).
func (r *Renderer) Minimized() bool {
	w, h := r.win.GetFramebufferSize()
	return w == 0 || h == 0
}

// recreateSwapchain tears down and rebuilds the swapchain, depth buffer, and
// framebuffers after a resize or when the surface becomes out of date.
func (r *Renderer) recreateSwapchain() error {
	// Skip while minimized — caller should poll and retry next frame.
	width, height := r.win.GetFramebufferSize()
	if width == 0 || height == 0 {
		return nil
	}

	r.deviceDriver.DeviceWaitIdle()

	// Destroy old resources
	for _, fb := range r.framebuffers {
		r.deviceDriver.DestroyFramebuffer(fb, nil)
	}
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

	r.depth.destroy(r.deviceDriver, len(r.depth.views))

	if r.msaa != nil {
		r.msaa.destroy(r.deviceDriver, len(r.msaa.views))
	}

	for _, iv := range r.sc.imageViews {
		r.deviceDriver.DestroyImageView(iv, nil)
	}

	r.swapchainExt.DestroySwapchain(r.sc.swapchain, nil)

	// Recreate swapchain, depth, MSAA, framebuffers
	var err error
	r.sc, r.swapchainExt, err = createSwapchain(r.deviceDriver, r.surfaceExt, r.surface, r.physicalDevice, r.indices, width, height, r.vsync)
	if err != nil {
		return err
	}

	r.depth, err = createDepthResources(r.instanceDriver, r.deviceDriver, r.physicalDevice, r.sc.extent, len(r.sc.imageViews), r.msaaSamples)
	if err != nil {
		return err
	}

	if r.msaaSamples != core1_0.Samples1 {
		r.msaa, err = createMSAAResources(r.instanceDriver, r.deviceDriver, r.physicalDevice, r.sc.extent, hdrFormat, r.msaaSamples, len(r.sc.imageViews))
		if err != nil {
			return err
		}
	} else {
		r.msaa = nil
	}

	var msaaViews []core1_0.ImageView
	if r.msaa != nil {
		msaaViews = r.msaa.views
	}

	// The HDR targets are swapchain-sized, so they go with it.
	// Both chains and the sets that point into them are size-dependent, so all
	// three are rebuilt together. The descriptor sets come from the pool, which
	// is not reset here -- so this leaks pool capacity across resizes and is why
	// maxHDRSets and maxBloomSets carry headroom rather than being exact.
	r.bloom.destroy(r.deviceDriver)
	r.hdr.destroy(r.deviceDriver)
	r.hdr, err = createHDRTargets(r.instanceDriver, r.deviceDriver, r.physicalDevice,
		r.descriptorPool, r.descriptorSetLayout, r.sc.extent, len(r.sc.imageViews), r.maxAnisotropy)
	if err != nil {
		return err
	}

	r.bloom, err = createBloomTargets(r.instanceDriver, r.deviceDriver, r.physicalDevice,
		r.descriptorPool, r.descriptorSetLayout, r.bloomDownRenderPass, r.bloomUpRenderPass,
		r.sc.extent, len(r.sc.imageViews))
	if err != nil {
		return err
	}

	r.clouds.destroy(r.deviceDriver)
	r.clouds, err = createCloudTargets(r.instanceDriver, r.deviceDriver, r.physicalDevice,
		r.descriptorPool, r.descriptorSetLayout, r.cloudRenderPass, r.sc.extent, cloudBufferCount)
	if err != nil {
		return err
	}
	// The history chain is meaningless across a resize: the buffers are a
	// different size and were rendered through a different projection. Priming
	// them gives a defined layout, and the reprojection rejects the contents on
	// the next frame anyway because nothing was written at the new size yet.
	if err := r.primeSampledImages(r.clouds.images); err != nil {
		return err
	}

	if err := r.primeBloomLayouts(r.bloom); err != nil {
		return err
	}

	// The resolve's sets name specific views, so they have to be rewritten
	// against the new images. Skipping this leaves the tonemap sampling freed
	// image views, which the validation layer catches and a release build does
	// not.
	if err := writeTonemapSets(r.deviceDriver, r.descriptorPool, r.tonemapSetLayout, r.hdr, r.bloom); err != nil {
		return err
	}

	r.framebuffers, err = createFramebuffers(r.deviceDriver, r.renderPass, r.hdr.views, r.depth.views, msaaViews, r.sc.extent)
	if err != nil {
		return err
	}

	r.tonemapFramebuffers, err = createTonemapFramebuffers(r.deviceDriver, r.tonemapRenderPass, r.sc.imageViews, r.sc.extent)
	if err != nil {
		return err
	}

	if r.sc.captureCapable {
		r.sceneColor, err = createSceneColorTarget(r.instanceDriver, r.deviceDriver, r.physicalDevice,
			r.descriptorPool, r.descriptorSetLayout, r.sc.extent, hdrFormat, r.maxAnisotropy)
		if err != nil {
			return err
		}
		r.waterFramebuffers, err = createWaterFramebuffers(r.deviceDriver, r.waterRenderPass, r.hdr.views, r.depth.views, msaaViews, r.sc.extent)
		if err != nil {
			return err
		}
	}

	log.Printf("Swapchain recreated: %dx%d", r.sc.extent.Width, r.sc.extent.Height)
	return nil
}

// DrawFrame records and submits one frame: waits for the in-flight fence, acquires
// a swapchain image, records draw commands, submits to the GPU, and presents.
func (r *Renderer) DrawFrame(draws []RenderObject, overlays []RenderObject, uiOverlays []UIRenderObject, msdfOverlays []RenderObject, lighting SceneLighting) error {
	f := r.currentFrame

	waitStart := time.Now()

	// Wait for this in-flight frame's previous submission to finish
	_, err := r.deviceDriver.WaitForFences(true, common.NoTimeout, r.sync.inFlight[f])
	if err != nil {
		return err
	}

	// The fence guarantees this slot's previous submission finished, which makes
	// it both the earliest point its timestamps are readable and the last point
	// before they are reset. Collecting anywhere else needs a stall.
	r.gpuTimer.collect(r.deviceDriver, f)

	// Safe to destroy resources queued for deferred destruction now that
	// a fence has been waited on.
	r.flushDeferredDestroys()

	// Acquire the next swapchain image
	imageIndex, result, err := r.swapchainExt.AcquireNextImage(r.sc.swapchain, common.NoTimeout, &r.sync.imageAvailable[f], nil)
	if err != nil {
		if result == khr_swapchain.VKErrorOutOfDate {
			return r.recreateSwapchain()
		}
		return err
	}

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
	if r.particles != nil {
		r.particles.flushUploads(f)
	}
	r.flushDynamicMeshes(f)

	// Always upload the cascade VP matrices so the fragment shader never reads
	// stale data. When shadows are disabled, the VPs are zero matrices, causing
	// all fragments to project to the shadow map origin where depth=1.0
	// (cleared) → fully lit.
	r.shadow.uploadCascadeVPs(f, lighting.CascadeVPs)
	r.shadow.uploadPointLights(f, &lighting.PointLights, lighting.PointLightCount)

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
		return err
	}
	recordStart := time.Now()
	err = recordCommandBuffer(r.deviceDriver, cmdBuf, r.renderPass, r.framebuffers[imageIndex], r.pipeline, r.litDoubleSidedPipeline, r.overlayPipeline, r.skyPipeline, r.starsPipeline, r.uiPipeline, r.msdfPipeline, r.skinnedPipeline, r.grassPipeline, r.waterPipeline, r.godRayPipeline, r.waterRenderPass, waterFB, r.sceneColor, r.hdr.images[imageIndex],
		func(cb core1_0.CommandBuffer) error { return r.recordClouds(cb, lighting) },
		r.cloudSetFor(),
		r.bloomFor(imageIndex), r.tonemapFor(imageIndex), r.particlePipeline, r.terrainPipeline, r.materialPipelines(), &r.stats, r.pipelineLayout, r.litPipelineLayout, r.skinnedPipelineLayout, r.terrainPipelineLayout, r.sc.extent, draws, overlays, uiOverlays, msdfOverlays, lighting, r.fallbackTexture, r.shadow, r.grass, r.grassLOD, r.grassImpostor, r.grassImpostorPipeline, r.particles, f, r.msaa != nil, r.gpuTimer)
	if err != nil {
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
		return err
	}
	if presentResult == khr_swapchain.VKErrorOutOfDate || presentResult == khr_swapchain.VKSuboptimal || r.framebufferResized {
		r.framebufferResized = false
		return r.recreateSwapchain()
	}

	// Advance the cloud history chain and remember what this frame was rendered
	// with, so the next one can reproject into it.
	r.cloudFrame++
	r.prevVP = lighting.VP

	r.currentFrame = (f + 1) % maxFramesInFlight
	return nil
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
func (r *Renderer) flushAllDeferred() {
	for _, dd := range r.deferredDestroys {
		dd.fn()
	}
	r.deferredDestroys = nil
}
