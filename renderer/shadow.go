package renderer

import (
	"encoding/binary"
	"fmt"
	"log"
	"math"
	"unsafe"

	"github.com/go-gl/mathgl/mgl32"
	"github.com/vkngwrapper/core/v3/core1_0"

	"github.com/derekmwright/glyphengine/renderer/lightcluster"
)

const ShadowMapSize = 2048
const PointShadowMapSize = 512

// ShadowCascades is the number of sun shadow cascades. Cascade 0 is a small
// high-density region around the camera; later cascades trade texel density
// for reach. Must match cascadeVP[] in the lit fragment shaders.
const ShadowCascades = 2

// cascadeRadii are the orthographic half-extents of each cascade in world
// units, centered on the camera.
var cascadeRadii = [ShadowCascades]float32{15, 90}

// The per-frame uniform block every lit pipeline binds at binding 0 of the
// shadow/light set -- set 1 for the static pipelines, set 2 for the skinned
// ones, where set 1 is already the joint matrices. Declared as ShadowData in
// lit.frag, lit_material.frag, skinned_lit.frag, skinned_lit_material.frag,
// terrain.frag, grass.frag and water.frag; all seven must agree with this.
//
// sky.frag and clouds.frag read the SAME buffer through a different set --
// binding 1 of the cloud descriptor set, see createCloudTargets -- because the
// sky palette below has to be one value for the dome, for the fog distant
// geometry fades into and for the water's reflection of both. They declare the
// members ahead of the one they want purely to land on its offset.
//
// It starts as the cascade matrices and continues with environment values the
// fragment shaders grade with. That second region exists because the push
// constant block is full at its 256-byte guaranteed minimum and there is
// nowhere to put a fifth vec4, and because a look value a game is expected to
// set has no business being a constant compiled into a shader every lit
// surface includes. Add to the tail rather than the middle: each new vec4 is
// one more offset here, one more line in nine shader declarations, and no
// change to anything already at a lower offset.
const (
	cascadeUBOSize   = ShadowCascades * 64 // mat4 cascadeVP[ShadowCascades]
	nightGradeOffset = cascadeUBOSize      // vec4 nightGrade
	skyPaletteOffset = nightGradeOffset + 16
	// vec4 skyPalette[6]. vec4 rather than vec3 because std140 gives an array
	// of either a 16-byte stride, so the padding is there whichever is
	// written and a vec4 says so.
	volumetricOffset = skyPaletteOffset + skyPaletteCount*16 // vec4 volumetric
	litUBOSize       = volumetricOffset + 16
)

// skyPaletteCount is how many endpoints atmSkyPalette blends between: zenith
// and horizon for day, twilight and night.
const skyPaletteCount = 6

// Volumetrics is the scattering medium's look and its sampling: how
// forward-scattering the air is, and how many steps the in-scattering march
// spends crossing it. See volInscatter in shaders/volumetric.inc.
//
// It is not the medium's density -- that is the scene's fog
// (SceneLighting.FogDensity and the height profile beside it), because the
// air that hazes the hills is the air a lamp lights. What is here is the one
// thing the fog does not already say and the one thing the march costs.
//
// SceneLighting carries it as a pointer for the reason NightGrade and
// SkyPalette are pointers: the zero value is a different feature, not a
// weaker one. Steps 0 marches nothing, so a caller driving this package
// directly who has never heard of the field would get no beams at all from
// lights that asked for them, with nothing to say why. Nil means
// DefaultVolumetrics.
type Volumetrics struct {
	// Anisotropy is the Henyey-Greenstein g. 0 scatters equally in every
	// direction; positive scatters forward, so a beam coming toward the eye
	// is brighter than the same beam crossing it; negative scatters back.
	// Clamped to (-1, 1), because the phase function divides by zero at
	// either end.
	Anisotropy float32

	// Steps is how many samples each pixel's march takes. It is a cost knob
	// and a banding knob at once, and it is data for the same reason
	// Sky.CloudSteps is: the right number depends on how deep the scene is
	// and how big a fraction of the frame the beams cover, which the engine
	// cannot know. Zero disables the march entirely.
	Steps int
}

// DefaultVolumetrics is the medium a scene gets by saying nothing.
//
// Anisotropy 0.4 rather than something more dramatic. The forward lobe is
// what makes a beam brighten as the camera swings into it, and at g = 0.7 it
// does that too well: volPhase is 67x stronger straight down the beam than
// across it, so a lamp viewed from the side -- which is most views of most
// street lamps -- almost disappears. At 0.4 the ratio is 5.8x, which still
// reads as a beam that has a direction while leaving the side-on view clearly
// visible. Both ratios are volPhase evaluated at cos(theta) = 1 against
// cos(theta) = 0.
//
// What that does to a capture, measured rather than left as arithmetic: the
// side-on doorway pose below at 1280x720 and the default 32 steps, box
// 600,270,60,60 across a door spot's cone, mean luma added at -volg 0 / 0.2 /
// 0.4 / 0.7 is +7.33 / +7.04 / +6.04 / +3.36. The default keeps 82% of the isotropic brightness when
// looking ACROSS a beam; 0.7 keeps 46%, and a spotlight at night is usually
// there to be seen from the side.
//
//	21-streetlights -volumetric 1 -volg G -camtargetx 21 -camtargety 1.5 \
//	  -camtargetz 0 -camyaw 0 -campitch 0.1 -camdist 9 -camlook 0.6
//
// Steps 32, and this one IS a measurement. `21-streetlights -skylamp
// -volumetric 1 -volsteps N` at 1280x720 under a fixed clock, differenced
// against the same frame with no volumetrics and read by cmd/volumetriccheck
// over the box 610,40,50,120 -- the upward beam where it crosses open sky.
// Mean luma added, and the mean absolute Laplacian of what was added (the
// number that says "grainy", since a fixed-step march through a cone thinner
// than one step lights some pixels and not their neighbours):
//
//	steps   added    grain
//	    8   +6.24    49.82
//	   16   +6.29    37.13
//	   24   +7.25    22.60
//	   32   +7.44    10.10
//	   64   +7.51     5.59
//
// The added light converges: 32 is within 1% of 64 and 16 is 16% short of it,
// because noise plus a concave tonemap loses light rather than just moving it
// about. The grain is what the eye sees, and it is still falling at 64.
//
// 32 rather than 16 because 16 is visibly crosshatched where a cone crosses
// the frame and 32 is not, in the sky at least -- looked at, not inferred from
// the table. It costs 0.46 ms more per frame at 1920x1080 on a scene with
// every light scattering (1.25 ms against 0.79), which is a real price for an
// effect a game opted into deliberately and can opt back out of one field at a
// time. The whole table is in docs/agents/lights.md, so a game that needs the
// time back knows exactly what dropping to 16 buys and costs.
//
// Neither number stops the march being grainy at the apex of a narrow cone,
// where the cone is thinner than a step at any count worth paying for. That is
// the limit of the per-pixel form, and the answer to it is the 3D-texture
// inject/integrate with temporal reprojection, not a bigger number here.
func DefaultVolumetrics() Volumetrics {
	return Volumetrics{Anisotropy: 0.4, Steps: 32}
}

// MaxVolumetricSteps caps Volumetrics.Steps.
//
// The march runs per pixel inside shaders every lit surface and the sky go
// through, so the step count multiplies the whole frame's fragment cost. The
// cap is not a measurement of where quality stops improving -- 32 is already
// past that; see the sweep in docs/agents/lights.md -- it is a bound on what
// one mistyped number can do to a frame, in the same spirit as
// lightcluster.MaxLightsPerCell bounding one fragment's light loop.
const MaxVolumetricSteps = 64

// NightGrade is the scotopic colour grade the lit shaders apply as daylight
// goes -- see atmNightShift in shaders/atmosphere.inc. Strength 0 turns it off
// entirely; Tint is what a fully shifted surface's luminance is multiplied by,
// and is blue-biased because rods are.
//
// It is a render-side value rather than a constant so a game can tune its
// night to taste without vendoring the whole lighting chain. SceneLighting
// carries it as a pointer for one reason: a zero-valued struct would mean
// Strength 0, which means "no night shift at all", and a caller driving this
// package directly who has never heard of the field would silently lose the
// look it had. Nil means DefaultNightGrade.
type NightGrade struct {
	Strength float32
	Tint     [3]float32
}

// DefaultNightGrade is the grade every scene had before it was tunable, and
// the one a caller gets by saying nothing. Changing these numbers changes
// every existing game's nights, which is the whole reason they are reachable
// from outside now.
func DefaultNightGrade() NightGrade {
	return NightGrade{Strength: 0.8, Tint: [3]float32{0.72, 0.86, 1.30}}
}

// SkyPalette is the six colours the atmosphere blends between -- zenith and
// horizon for day, twilight and night. atmSkyPalette in
// shaders/atmosphere.inc mixes night toward day on the daylight curve and then
// toward twilight on the twilight curve, so these are endpoints rather than a
// gradient anyone samples directly.
//
// It is one value for the whole atmosphere, not just for the dome:
// `applyFog` blends distant geometry toward the same horizon colour and water
// reflects the dome, so a game that changed only the sky shader would get a
// violet sky over a landscape still fading into Earth-blue haze. That is what
// made this data rather than constants.
//
// SceneLighting carries it as a pointer for the reason NightGrade is one: a
// zero value here is six black colours, which is a sky nobody wants and which
// a caller driving this package directly would get by never having heard of
// the field. Nil means DefaultSkyPalette.
type SkyPalette struct {
	ZenithDay, HorizonDay           [3]float32
	ZenithTwilight, HorizonTwilight [3]float32
	ZenithNight, HorizonNight       [3]float32
}

// DefaultSkyPalette is Earth's, and is exactly the constants that used to sit
// in atmSkyPalette. Changing these numbers changes the sky of every game that
// has not set its own, which is the whole reason they are reachable now.
//
// The horizon is pale because that is what looking through more atmosphere
// does, but not white: distant geometry fades into this colour, so a
// washed-out horizon washes out the whole landscape with it.
//
// Night is deliberately dark. These are the endpoints the whole scene reaches
// at midnight -- the dome, the fog and the water's reflection all read from
// here -- so lifting them to make the sky legible washes out the entire
// landscape with it. If night needs to be brighter, brighten the moon, not the
// air.
func DefaultSkyPalette() SkyPalette {
	return SkyPalette{
		ZenithDay:  [3]float32{0.13, 0.30, 0.78},
		HorizonDay: [3]float32{0.52, 0.70, 0.93},

		ZenithTwilight:  [3]float32{0.055, 0.085, 0.26},
		HorizonTwilight: [3]float32{0.88, 0.42, 0.22},

		ZenithNight:  [3]float32{0.0014, 0.0017, 0.0060},
		HorizonNight: [3]float32{0.0034, 0.0050, 0.0130},
	}
}

// endpoints returns the palette in the order the shader indexes it, which is
// the order atmosphere.inc's ATM_* defines name. One function so the packing
// and the tests cannot disagree about it.
func (p SkyPalette) endpoints() [skyPaletteCount][3]float32 {
	return [skyPaletteCount][3]float32{
		p.ZenithDay, p.HorizonDay,
		p.ZenithTwilight, p.HorizonTwilight,
		p.ZenithNight, p.HorizonNight,
	}
}

// shadowResources holds all Vulkan resources for the shadow mapping pass.
// Per-frame images/views/framebuffers prevent read-write conflicts between frames in flight.
// The sun shadow map is a 2D array image with one layer per cascade.
type shadowResources struct {
	images       [maxFramesInFlight]core1_0.Image
	memories     [maxFramesInFlight]core1_0.DeviceMemory
	cascadeViews [maxFramesInFlight][ShadowCascades]core1_0.ImageView // per-layer, for framebuffers
	arrayViews   [maxFramesInFlight]core1_0.ImageView                 // 2D array view, for sampling
	format       core1_0.Format
	sampler      core1_0.Sampler
	renderPass   core1_0.RenderPass
	framebuffers [maxFramesInFlight][ShadowCascades]core1_0.Framebuffer

	// Per-frame UBOs for the light VP matrix (persistently mapped)
	lightVPBuffers        [maxFramesInFlight]core1_0.Buffer
	lightVPMemories       [maxFramesInFlight]core1_0.DeviceMemory
	lightVPMapped         [maxFramesInFlight][]byte
	shaderParameterMapped [maxFramesInFlight][]byte // aligned slice of the same UBO allocation

	// Per-frame storage buffers for the clustered light data (points + spots,
	// up to MaxLights), persistently mapped. Three buffers because the GPU
	// contract in shaders/lights.inc is three separate readonly buffers, not
	// one with sub-regions -- LightBuffer (bindings 3), ClusterGrid (4) and
	// LightIndices (5).
	lightBuffers  [maxFramesInFlight]core1_0.Buffer
	lightMemories [maxFramesInFlight]core1_0.DeviceMemory
	lightMapped   [maxFramesInFlight][]byte

	// clusterGrid holds one Cell per froxel and lightIndex the concatenated
	// per-cell light lists; uploadLights rewrites both every frame from what
	// the binner produced. They are zeroed once at creation so the frames
	// before the first upload read empty cells rather than whatever the
	// driver left in host-visible memory.
	clusterGridBuffers  [maxFramesInFlight]core1_0.Buffer
	clusterGridMemories [maxFramesInFlight]core1_0.DeviceMemory
	clusterGridMapped   [maxFramesInFlight][]byte

	lightIndexBuffers  [maxFramesInFlight]core1_0.Buffer
	lightIndexMemories [maxFramesInFlight]core1_0.DeviceMemory
	lightIndexMapped   [maxFramesInFlight][]byte

	// Descriptor set layout: binding 0 = UBO (vertex), binding 1 = shadow sampler (fragment)
	descriptorSetLayout core1_0.DescriptorSetLayout
	// Per-frame descriptor sets, each referencing its own UBO + its own shadow map
	descriptorSets [maxFramesInFlight]core1_0.DescriptorSet

	// Shadow pass pipeline layouts (push constants only, no texture descriptor)
	pipelineLayout        core1_0.PipelineLayout // static shadow: push constants 128B, no sets
	skinnedPipelineLayout core1_0.PipelineLayout // skinned shadow: push constants 128B, set 0 = joints

	pipeline        core1_0.Pipeline // static depth-only
	skinnedPipeline core1_0.Pipeline // skinned depth-only

	// instancedPipeline is the depth-only stage for InstanceSets. Without it
	// instanced geometry silently stops casting: the draw still records, and
	// every instance lands on top of the first one, because shadow.vert takes
	// the model matrix from a push constant the instanced path does not set.
	instancedPipeline core1_0.Pipeline

	// Point light cube shadow map
	cubeImages       [maxFramesInFlight]core1_0.Image
	cubeMemories     [maxFramesInFlight]core1_0.DeviceMemory
	cubeFaceViews    [maxFramesInFlight][6]core1_0.ImageView // per-face 2D views for framebuffers
	cubeSamplerViews [maxFramesInFlight]core1_0.ImageView    // cube view for fragment sampling
	cubeSampler      core1_0.Sampler                         // nearest, no comparison
	cubeFramebuffers [maxFramesInFlight][6]core1_0.Framebuffer
}

// createShadowResources creates all resources for the shadow mapping pass.
func createShadowResources(
	instanceDriver core1_0.CoreInstanceDriver,
	deviceDriver core1_0.CoreDeviceDriver,
	sh ShaderSet,
	physicalDevice core1_0.PhysicalDevice,
	descriptorPool core1_0.DescriptorPool,
	jointSetLayout core1_0.DescriptorSetLayout,
) (*shadowResources, error) {
	s := &shadowResources{}
	props, err := instanceDriver.GetPhysicalDeviceProperties(physicalDevice)
	if err != nil {
		return nil, fmt.Errorf("shader parameter limits: %w", err)
	}
	parameterOffset, err := shaderParameterOffset(props.Limits)
	if err != nil {
		return nil, err
	}
	uniformAllocationSize := parameterOffset + ShaderParameterBytes

	// Find depth format
	format, err := findDepthFormat(instanceDriver, physicalDevice)
	if err != nil {
		return nil, fmt.Errorf("shadow depth format: %w", err)
	}
	s.format = format

	// Create per-frame depth array images, one layer per cascade
	// (DepthStencilAttachment + Sampled for reading in main pass)
	for i := 0; i < maxFramesInFlight; i++ {
		s.images[i], _, err = deviceDriver.CreateImage(nil, core1_0.ImageCreateInfo{
			ImageType: core1_0.ImageType2D,
			Format:    format,
			Extent: core1_0.Extent3D{
				Width:  ShadowMapSize,
				Height: ShadowMapSize,
				Depth:  1,
			},
			MipLevels:     1,
			ArrayLayers:   ShadowCascades,
			Samples:       core1_0.Samples1,
			Tiling:        core1_0.ImageTilingOptimal,
			Usage:         core1_0.ImageUsageDepthStencilAttachment | core1_0.ImageUsageSampled,
			SharingMode:   core1_0.SharingModeExclusive,
			InitialLayout: core1_0.ImageLayoutUndefined,
		})
		if err != nil {
			s.destroy(deviceDriver)
			return nil, fmt.Errorf("shadow image %d: %w", i, err)
		}

		memReqs := deviceDriver.GetImageMemoryRequirements(s.images[i])
		memType, err := findMemoryType(instanceDriver, physicalDevice, memReqs.MemoryTypeBits, core1_0.MemoryPropertyDeviceLocal)
		if err != nil {
			s.destroy(deviceDriver)
			return nil, err
		}

		s.memories[i], _, err = deviceDriver.AllocateMemory(nil, core1_0.MemoryAllocateInfo{
			AllocationSize:  memReqs.Size,
			MemoryTypeIndex: memType,
		})
		if err != nil {
			s.destroy(deviceDriver)
			return nil, fmt.Errorf("shadow memory %d: %w", i, err)
		}

		_, err = deviceDriver.BindImageMemory(s.images[i], s.memories[i], 0)
		if err != nil {
			s.destroy(deviceDriver)
			return nil, fmt.Errorf("bind shadow image %d: %w", i, err)
		}

		// Per-cascade layer views for framebuffer attachment
		for c := 0; c < ShadowCascades; c++ {
			s.cascadeViews[i][c], _, err = deviceDriver.CreateImageView(nil, core1_0.ImageViewCreateInfo{
				Image:    s.images[i],
				ViewType: core1_0.ImageViewType2D,
				Format:   format,
				SubresourceRange: core1_0.ImageSubresourceRange{
					AspectMask:     core1_0.ImageAspectDepth,
					LevelCount:     1,
					BaseArrayLayer: c,
					LayerCount:     1,
				},
			})
			if err != nil {
				s.destroy(deviceDriver)
				return nil, fmt.Errorf("shadow cascade view %d/%d: %w", i, c, err)
			}
		}

		// Array view for sampling all cascades in the fragment shader
		s.arrayViews[i], _, err = deviceDriver.CreateImageView(nil, core1_0.ImageViewCreateInfo{
			Image:    s.images[i],
			ViewType: core1_0.ImageViewType2DArray,
			Format:   format,
			SubresourceRange: core1_0.ImageSubresourceRange{
				AspectMask: core1_0.ImageAspectDepth,
				LevelCount: 1,
				LayerCount: ShadowCascades,
			},
		})
		if err != nil {
			s.destroy(deviceDriver)
			return nil, fmt.Errorf("shadow array view %d: %w", i, err)
		}
	}

	// Comparison sampler for PCF (CompareOpLessOrEqual, ClampToBorder white)
	s.sampler, _, err = deviceDriver.CreateSampler(nil, core1_0.SamplerCreateInfo{
		MagFilter:     core1_0.FilterLinear,
		MinFilter:     core1_0.FilterLinear,
		AddressModeU:  core1_0.SamplerAddressModeClampToBorder,
		AddressModeV:  core1_0.SamplerAddressModeClampToBorder,
		AddressModeW:  core1_0.SamplerAddressModeClampToBorder,
		MipmapMode:    core1_0.SamplerMipmapModeNearest,
		CompareEnable: true,
		CompareOp:     core1_0.CompareOpLessOrEqual,
		BorderColor:   core1_0.BorderColorFloatOpaqueWhite,
	})
	if err != nil {
		s.destroy(deviceDriver)
		return nil, fmt.Errorf("shadow sampler: %w", err)
	}

	// Depth-only render pass
	s.renderPass, _, err = deviceDriver.CreateRenderPass(nil, core1_0.RenderPassCreateInfo{
		Attachments: []core1_0.AttachmentDescription{
			{
				Format:         format,
				Samples:        core1_0.Samples1,
				LoadOp:         core1_0.AttachmentLoadOpClear,
				StoreOp:        core1_0.AttachmentStoreOpStore,
				StencilLoadOp:  core1_0.AttachmentLoadOpDontCare,
				StencilStoreOp: core1_0.AttachmentStoreOpDontCare,
				InitialLayout:  core1_0.ImageLayoutUndefined,
				FinalLayout:    core1_0.ImageLayoutDepthStencilReadOnlyOptimal,
			},
		},
		Subpasses: []core1_0.SubpassDescription{
			{
				PipelineBindPoint: core1_0.PipelineBindPointGraphics,
				DepthStencilAttachment: &core1_0.AttachmentReference{
					Attachment: 0,
					Layout:     core1_0.ImageLayoutDepthStencilAttachmentOptimal,
				},
			},
		},
		SubpassDependencies: []core1_0.SubpassDependency{
			{
				// Previous frame's main pass reads must complete before we write.
				SrcSubpass:    core1_0.SubpassExternal,
				DstSubpass:    0,
				SrcStageMask:  core1_0.PipelineStageFragmentShader,
				DstStageMask:  core1_0.PipelineStageEarlyFragmentTests,
				SrcAccessMask: core1_0.AccessShaderRead,
				DstAccessMask: core1_0.AccessDepthStencilAttachmentWrite,
			},
			{
				// Shadow writes must complete before main pass fragment reads.
				SrcSubpass:    0,
				DstSubpass:    core1_0.SubpassExternal,
				SrcStageMask:  core1_0.PipelineStageLateFragmentTests,
				DstStageMask:  core1_0.PipelineStageFragmentShader,
				SrcAccessMask: core1_0.AccessDepthStencilAttachmentWrite,
				DstAccessMask: core1_0.AccessShaderRead,
			},
		},
	})
	if err != nil {
		s.destroy(deviceDriver)
		return nil, fmt.Errorf("shadow render pass: %w", err)
	}

	for i := 0; i < maxFramesInFlight; i++ {
		for c := 0; c < ShadowCascades; c++ {
			s.framebuffers[i][c], _, err = deviceDriver.CreateFramebuffer(nil, core1_0.FramebufferCreateInfo{
				RenderPass:  s.renderPass,
				Attachments: []core1_0.ImageView{s.cascadeViews[i][c]},
				Width:       ShadowMapSize,
				Height:      ShadowMapSize,
				Layers:      1,
			})
			if err != nil {
				s.destroy(deviceDriver)
				return nil, fmt.Errorf("shadow framebuffer %d/%d: %w", i, c, err)
			}
		}
	}

	// Per-frame UBOs (one mat4 light VP per cascade)
	for i := 0; i < maxFramesInFlight; i++ {
		s.lightVPBuffers[i], _, err = deviceDriver.CreateBuffer(nil, core1_0.BufferCreateInfo{
			Size:        uniformAllocationSize,
			Usage:       core1_0.BufferUsageUniformBuffer,
			SharingMode: core1_0.SharingModeExclusive,
		})
		if err != nil {
			s.destroy(deviceDriver)
			return nil, fmt.Errorf("shadow UBO buffer %d: %w", i, err)
		}

		bufMemReqs := deviceDriver.GetBufferMemoryRequirements(s.lightVPBuffers[i])
		bufMemType, err := findMemoryType(instanceDriver, physicalDevice, bufMemReqs.MemoryTypeBits,
			core1_0.MemoryPropertyHostVisible|core1_0.MemoryPropertyHostCoherent)
		if err != nil {
			s.destroy(deviceDriver)
			return nil, err
		}

		s.lightVPMemories[i], _, err = deviceDriver.AllocateMemory(nil, core1_0.MemoryAllocateInfo{
			AllocationSize:  bufMemReqs.Size,
			MemoryTypeIndex: bufMemType,
		})
		if err != nil {
			s.destroy(deviceDriver)
			return nil, fmt.Errorf("shadow UBO memory %d: %w", i, err)
		}

		_, err = deviceDriver.BindBufferMemory(s.lightVPBuffers[i], s.lightVPMemories[i], 0)
		if err != nil {
			s.destroy(deviceDriver)
			return nil, fmt.Errorf("bind shadow UBO %d: %w", i, err)
		}

		ptr, _, err := deviceDriver.MapMemory(s.lightVPMemories[i], 0, uniformAllocationSize, 0)
		if err != nil {
			s.destroy(deviceDriver)
			return nil, fmt.Errorf("map shadow UBO %d: %w", i, err)
		}
		mapped := unsafe.Slice((*byte)(ptr), uniformAllocationSize)
		s.lightVPMapped[i] = mapped[:litUBOSize]
		s.shaderParameterMapped[i] = mapped[parameterOffset:]
		clear(s.shaderParameterMapped[i])
	}

	// Per-frame storage buffers for the clustered light data: LightBuffer,
	// ClusterGrid, LightIndices. Host-visible and persistently mapped, same
	// as the UBOs above -- only the usage and descriptor type differ, because
	// the light array no longer fits the UBO's 16KB minimum guaranteed range
	// once it can hold MaxLights (1024) entries instead of 32.
	newStorageBuffer := func(size int, label string) (core1_0.Buffer, core1_0.DeviceMemory, []byte, error) {
		buf, _, err := deviceDriver.CreateBuffer(nil, core1_0.BufferCreateInfo{
			Size:        size,
			Usage:       core1_0.BufferUsageStorageBuffer,
			SharingMode: core1_0.SharingModeExclusive,
		})
		if err != nil {
			return core1_0.Buffer{}, core1_0.DeviceMemory{}, nil, fmt.Errorf("%s buffer: %w", label, err)
		}

		memReqs := deviceDriver.GetBufferMemoryRequirements(buf)
		memType, err := findMemoryType(instanceDriver, physicalDevice, memReqs.MemoryTypeBits,
			core1_0.MemoryPropertyHostVisible|core1_0.MemoryPropertyHostCoherent)
		if err != nil {
			return core1_0.Buffer{}, core1_0.DeviceMemory{}, nil, err
		}

		mem, _, err := deviceDriver.AllocateMemory(nil, core1_0.MemoryAllocateInfo{
			AllocationSize:  memReqs.Size,
			MemoryTypeIndex: memType,
		})
		if err != nil {
			return core1_0.Buffer{}, core1_0.DeviceMemory{}, nil, fmt.Errorf("%s memory: %w", label, err)
		}

		if _, err := deviceDriver.BindBufferMemory(buf, mem, 0); err != nil {
			return core1_0.Buffer{}, core1_0.DeviceMemory{}, nil, fmt.Errorf("bind %s: %w", label, err)
		}

		ptr, _, err := deviceDriver.MapMemory(mem, 0, size, 0)
		if err != nil {
			return core1_0.Buffer{}, core1_0.DeviceMemory{}, nil, fmt.Errorf("map %s: %w", label, err)
		}
		return buf, mem, unsafe.Slice((*byte)(ptr), size), nil
	}

	for i := 0; i < maxFramesInFlight; i++ {
		var err error
		s.lightBuffers[i], s.lightMemories[i], s.lightMapped[i], err = newStorageBuffer(lightBufferSize, "light buffer")
		if err != nil {
			s.destroy(deviceDriver)
			return nil, fmt.Errorf("frame %d: %w", i, err)
		}

		s.clusterGridBuffers[i], s.clusterGridMemories[i], s.clusterGridMapped[i], err = newStorageBuffer(clusterGridBufferSize, "cluster grid buffer")
		if err != nil {
			s.destroy(deviceDriver)
			return nil, fmt.Errorf("frame %d: %w", i, err)
		}

		s.lightIndexBuffers[i], s.lightIndexMemories[i], s.lightIndexMapped[i], err = newStorageBuffer(lightIndexBufferSize, "light index buffer")
		if err != nil {
			s.destroy(deviceDriver)
			return nil, fmt.Errorf("frame %d: %w", i, err)
		}

		// Mapped host-visible memory is not guaranteed zeroed by the driver,
		// and uploadLights only writes the cells and the index entries the
		// binner filled. An all-zero Cell{0,0} is exactly "empty cell", so
		// zeroing once here is what makes the untouched tail of the index
		// buffer unreachable rather than merely unread.
		clear(s.clusterGridMapped[i])
		clear(s.lightIndexMapped[i])
	}

	// Descriptor set layout: binding 0 = UBO, binding 1 = sun shadow sampler,
	// binding 2 = point cube sampler, bindings 3-5 = the clustered light
	// storage buffers (LightBuffer, ClusterGrid, LightIndices -- see
	// shaders/lights.inc). Binding 3 used to be a point-lights UBO; it is a
	// storage buffer now because the light array no longer fits the UBO's
	// 16KB minimum guaranteed range once it can hold MaxLights (1024) 64-byte
	// entries instead of 32.
	s.descriptorSetLayout, _, err = deviceDriver.CreateDescriptorSetLayout(nil, core1_0.DescriptorSetLayoutCreateInfo{
		Bindings: []core1_0.DescriptorSetLayoutBinding{
			{
				Binding:         0,
				DescriptorType:  core1_0.DescriptorTypeUniformBuffer,
				DescriptorCount: 1,
				StageFlags:      core1_0.StageVertex | core1_0.StageFragment,
			},
			{
				Binding:         1,
				DescriptorType:  core1_0.DescriptorTypeCombinedImageSampler,
				DescriptorCount: 1,
				StageFlags:      core1_0.StageFragment,
			},
			{
				Binding:         2,
				DescriptorType:  core1_0.DescriptorTypeCombinedImageSampler,
				DescriptorCount: 1,
				StageFlags:      core1_0.StageFragment,
			},
			{
				Binding:         3,
				DescriptorType:  core1_0.DescriptorTypeStorageBuffer,
				DescriptorCount: 1,
				StageFlags:      core1_0.StageFragment,
			},
			{
				Binding:         4,
				DescriptorType:  core1_0.DescriptorTypeStorageBuffer,
				DescriptorCount: 1,
				StageFlags:      core1_0.StageFragment,
			},
			{
				Binding:         5,
				DescriptorType:  core1_0.DescriptorTypeStorageBuffer,
				DescriptorCount: 1,
				StageFlags:      core1_0.StageFragment,
			},
			{
				Binding:         6,
				DescriptorType:  core1_0.DescriptorTypeUniformBuffer,
				DescriptorCount: 1,
				StageFlags:      core1_0.StageVertex | core1_0.StageFragment,
			},
			{Binding: 7, DescriptorType: core1_0.DescriptorTypeCombinedImageSampler, DescriptorCount: 1, StageFlags: core1_0.StageVertex | core1_0.StageFragment},
			{Binding: 8, DescriptorType: core1_0.DescriptorTypeCombinedImageSampler, DescriptorCount: 1, StageFlags: core1_0.StageVertex | core1_0.StageFragment},
			{Binding: 9, DescriptorType: core1_0.DescriptorTypeCombinedImageSampler, DescriptorCount: 1, StageFlags: core1_0.StageVertex | core1_0.StageFragment},
			{Binding: 10, DescriptorType: core1_0.DescriptorTypeCombinedImageSampler, DescriptorCount: 1, StageFlags: core1_0.StageVertex | core1_0.StageFragment},
		},
	})
	if err != nil {
		s.destroy(deviceDriver)
		return nil, fmt.Errorf("shadow descriptor set layout: %w", err)
	}

	// Allocate per-frame descriptor sets
	layouts := make([]core1_0.DescriptorSetLayout, maxFramesInFlight)
	for i := range layouts {
		layouts[i] = s.descriptorSetLayout
	}
	sets, _, err := deviceDriver.AllocateDescriptorSets(core1_0.DescriptorSetAllocateInfo{
		DescriptorPool: descriptorPool,
		SetLayouts:     layouts,
	})
	if err != nil {
		s.destroy(deviceDriver)
		return nil, fmt.Errorf("shadow descriptor sets: %w", err)
	}
	copy(s.descriptorSets[:], sets)

	// NOTE: Descriptor set writes are deferred until after cube resources are created (below).

	// Shadow pass pipeline layout: push constants 128B (2x mat4), no descriptor sets
	s.pipelineLayout, _, err = deviceDriver.CreatePipelineLayout(nil, core1_0.PipelineLayoutCreateInfo{
		PushConstantRanges: []core1_0.PushConstantRange{
			{
				StageFlags: core1_0.StageVertex,
				Offset:     0,
				Size:       128, // mvp + model
			},
		},
	})
	if err != nil {
		s.destroy(deviceDriver)
		return nil, fmt.Errorf("shadow pipeline layout: %w", err)
	}

	// Skinned shadow pipeline layout: push constants 128B + set 0 = joint UBO
	s.skinnedPipelineLayout, _, err = deviceDriver.CreatePipelineLayout(nil, core1_0.PipelineLayoutCreateInfo{
		SetLayouts: []core1_0.DescriptorSetLayout{jointSetLayout},
		PushConstantRanges: []core1_0.PushConstantRange{
			{
				StageFlags: core1_0.StageVertex,
				Offset:     0,
				Size:       128,
			},
		},
	})
	if err != nil {
		s.destroy(deviceDriver)
		return nil, fmt.Errorf("skinned shadow pipeline layout: %w", err)
	}

	// Create shadow pipelines
	s.pipeline, err = createShadowPipeline(deviceDriver, sh, s.renderPass, s.pipelineLayout, false)
	if err != nil {
		s.destroy(deviceDriver)
		return nil, fmt.Errorf("shadow pipeline: %w", err)
	}

	s.instancedPipeline, err = createInstancedShadowPipeline(deviceDriver, sh, s.renderPass, s.pipelineLayout)
	if err != nil {
		return nil, err
	}

	s.skinnedPipeline, err = createShadowPipeline(deviceDriver, sh, s.renderPass, s.skinnedPipelineLayout, true)
	if err != nil {
		s.destroy(deviceDriver)
		return nil, fmt.Errorf("skinned shadow pipeline: %w", err)
	}

	// ── Point light cube shadow map ──
	for i := 0; i < maxFramesInFlight; i++ {
		s.cubeImages[i], _, err = deviceDriver.CreateImage(nil, core1_0.ImageCreateInfo{
			Flags:     core1_0.ImageCreateCubeCompatible,
			ImageType: core1_0.ImageType2D,
			Format:    format,
			Extent: core1_0.Extent3D{
				Width:  PointShadowMapSize,
				Height: PointShadowMapSize,
				Depth:  1,
			},
			MipLevels:   1,
			ArrayLayers: 6,
			Samples:     core1_0.Samples1,
			Tiling:      core1_0.ImageTilingOptimal,
			// TransferDst so initCubeShadowLayout can clear it once at startup;
			// see that method for why an unrendered cube map still needs
			// defined contents.
			Usage: core1_0.ImageUsageDepthStencilAttachment |
				core1_0.ImageUsageSampled |
				core1_0.ImageUsageTransferDst,
			SharingMode:   core1_0.SharingModeExclusive,
			InitialLayout: core1_0.ImageLayoutUndefined,
		})
		if err != nil {
			s.destroy(deviceDriver)
			return nil, fmt.Errorf("cube shadow image %d: %w", i, err)
		}

		memReqs := deviceDriver.GetImageMemoryRequirements(s.cubeImages[i])
		memType, err := findMemoryType(instanceDriver, physicalDevice, memReqs.MemoryTypeBits, core1_0.MemoryPropertyDeviceLocal)
		if err != nil {
			s.destroy(deviceDriver)
			return nil, err
		}

		s.cubeMemories[i], _, err = deviceDriver.AllocateMemory(nil, core1_0.MemoryAllocateInfo{
			AllocationSize:  memReqs.Size,
			MemoryTypeIndex: memType,
		})
		if err != nil {
			s.destroy(deviceDriver)
			return nil, fmt.Errorf("cube shadow memory %d: %w", i, err)
		}

		_, err = deviceDriver.BindImageMemory(s.cubeImages[i], s.cubeMemories[i], 0)
		if err != nil {
			s.destroy(deviceDriver)
			return nil, fmt.Errorf("bind cube shadow image %d: %w", i, err)
		}

		// Per-face 2D views for framebuffer attachment
		for face := 0; face < 6; face++ {
			s.cubeFaceViews[i][face], _, err = deviceDriver.CreateImageView(nil, core1_0.ImageViewCreateInfo{
				Image:    s.cubeImages[i],
				ViewType: core1_0.ImageViewType2D,
				Format:   format,
				SubresourceRange: core1_0.ImageSubresourceRange{
					AspectMask:     core1_0.ImageAspectDepth,
					BaseMipLevel:   0,
					LevelCount:     1,
					BaseArrayLayer: face,
					LayerCount:     1,
				},
			})
			if err != nil {
				s.destroy(deviceDriver)
				return nil, fmt.Errorf("cube face view %d/%d: %w", i, face, err)
			}
		}

		// Cube view for sampling in fragment shader
		s.cubeSamplerViews[i], _, err = deviceDriver.CreateImageView(nil, core1_0.ImageViewCreateInfo{
			Image:    s.cubeImages[i],
			ViewType: core1_0.ImageViewTypeCube,
			Format:   format,
			SubresourceRange: core1_0.ImageSubresourceRange{
				AspectMask:     core1_0.ImageAspectDepth,
				BaseMipLevel:   0,
				LevelCount:     1,
				BaseArrayLayer: 0,
				LayerCount:     6,
			},
		})
		if err != nil {
			s.destroy(deviceDriver)
			return nil, fmt.Errorf("cube sampler view %d: %w", i, err)
		}

		// Per-face framebuffers (reuse shadow render pass)
		for face := 0; face < 6; face++ {
			s.cubeFramebuffers[i][face], _, err = deviceDriver.CreateFramebuffer(nil, core1_0.FramebufferCreateInfo{
				RenderPass:  s.renderPass,
				Attachments: []core1_0.ImageView{s.cubeFaceViews[i][face]},
				Width:       PointShadowMapSize,
				Height:      PointShadowMapSize,
				Layers:      1,
			})
			if err != nil {
				s.destroy(deviceDriver)
				return nil, fmt.Errorf("cube framebuffer %d/%d: %w", i, face, err)
			}
		}
	}

	// Cube shadow sampler (nearest, no comparison, clamp-to-edge)
	s.cubeSampler, _, err = deviceDriver.CreateSampler(nil, core1_0.SamplerCreateInfo{
		MagFilter:    core1_0.FilterNearest,
		MinFilter:    core1_0.FilterNearest,
		AddressModeU: core1_0.SamplerAddressModeClampToEdge,
		AddressModeV: core1_0.SamplerAddressModeClampToEdge,
		AddressModeW: core1_0.SamplerAddressModeClampToEdge,
		MipmapMode:   core1_0.SamplerMipmapModeNearest,
	})
	if err != nil {
		s.destroy(deviceDriver)
		return nil, fmt.Errorf("cube shadow sampler: %w", err)
	}

	// Update descriptor sets now that all resources (UBO, sun shadow, cube shadow, light buffers) are ready.
	for i := 0; i < maxFramesInFlight; i++ {
		err = deviceDriver.UpdateDescriptorSets([]core1_0.WriteDescriptorSet{
			{
				DstSet:         s.descriptorSets[i],
				DstBinding:     6,
				DescriptorType: core1_0.DescriptorTypeUniformBuffer,
				BufferInfo:     []core1_0.DescriptorBufferInfo{{Buffer: s.lightVPBuffers[i], Offset: parameterOffset, Range: ShaderParameterBytes}},
			},
			{
				DstSet:         s.descriptorSets[i],
				DstBinding:     0,
				DescriptorType: core1_0.DescriptorTypeUniformBuffer,
				BufferInfo: []core1_0.DescriptorBufferInfo{
					{
						Buffer: s.lightVPBuffers[i],
						Offset: 0,
						Range:  litUBOSize,
					},
				},
			},
			{
				DstSet:         s.descriptorSets[i],
				DstBinding:     1,
				DescriptorType: core1_0.DescriptorTypeCombinedImageSampler,
				ImageInfo: []core1_0.DescriptorImageInfo{
					{
						Sampler:     s.sampler,
						ImageView:   s.arrayViews[i],
						ImageLayout: core1_0.ImageLayoutDepthStencilReadOnlyOptimal,
					},
				},
			},
			{
				DstSet:         s.descriptorSets[i],
				DstBinding:     2,
				DescriptorType: core1_0.DescriptorTypeCombinedImageSampler,
				ImageInfo: []core1_0.DescriptorImageInfo{
					{
						Sampler:     s.cubeSampler,
						ImageView:   s.cubeSamplerViews[i],
						ImageLayout: core1_0.ImageLayoutDepthStencilReadOnlyOptimal,
					},
				},
			},
			{
				DstSet:         s.descriptorSets[i],
				DstBinding:     3,
				DescriptorType: core1_0.DescriptorTypeStorageBuffer,
				BufferInfo: []core1_0.DescriptorBufferInfo{
					{
						Buffer: s.lightBuffers[i],
						Offset: 0,
						Range:  lightBufferSize,
					},
				},
			},
			{
				DstSet:         s.descriptorSets[i],
				DstBinding:     4,
				DescriptorType: core1_0.DescriptorTypeStorageBuffer,
				BufferInfo: []core1_0.DescriptorBufferInfo{
					{
						Buffer: s.clusterGridBuffers[i],
						Offset: 0,
						Range:  clusterGridBufferSize,
					},
				},
			},
			{
				DstSet:         s.descriptorSets[i],
				DstBinding:     5,
				DescriptorType: core1_0.DescriptorTypeStorageBuffer,
				BufferInfo: []core1_0.DescriptorBufferInfo{
					{
						Buffer: s.lightIndexBuffers[i],
						Offset: 0,
						Range:  lightIndexBufferSize,
					},
				},
			},
		}, nil)
		if err != nil {
			s.destroy(deviceDriver)
			return nil, fmt.Errorf("update shadow descriptor set %d: %w", i, err)
		}
	}

	log.Println("Shadow resources created (directional + point cube)")
	return s, nil
}

// createInstancedShadowPipeline is the depth-only stage for InstanceSets.
//
// It reuses the non-skinned shadow pipeline layout -- the push constants are
// the same 128 bytes, and the instanced vertex stage simply reads the first
// matrix as a view-projection rather than a view-projection-model. Only the
// vertex input state differs.
func createInstancedShadowPipeline(deviceDriver core1_0.DeviceDriver, sh ShaderSet, renderPass core1_0.RenderPass, layout core1_0.PipelineLayout) (core1_0.Pipeline, error) {
	return createShadowPipelineWithInput(deviceDriver, sh, sh.ShadowInstancedVert, renderPass, layout,
		[]core1_0.VertexInputBindingDescription{vertexBindingDescription(), instanceBindingDescription()},
		instanceAttributeDescriptions())
}

// createShadowPipeline creates a depth-only pipeline for the shadow pass.
func createShadowPipeline(deviceDriver core1_0.DeviceDriver, sh ShaderSet, renderPass core1_0.RenderPass, layout core1_0.PipelineLayout, skinned bool) (core1_0.Pipeline, error) {
	if skinned {
		return createShadowPipelineWithInput(deviceDriver, sh, sh.ShadowSkinnedVert, renderPass, layout,
			[]core1_0.VertexInputBindingDescription{skinnedVertexBindingDescription()},
			skinnedVertexAttributeDescriptions())
	}
	return createShadowPipelineWithInput(deviceDriver, sh, sh.ShadowVert, renderPass, layout,
		[]core1_0.VertexInputBindingDescription{vertexBindingDescription()},
		vertexAttributeDescriptions())
}

// createShadowPipelineWithInput is createShadowPipeline with the vertex stage
// and vertex input state supplied, so the skinned and instanced variants
// differ only in those two things.
func createShadowPipelineWithInput(deviceDriver core1_0.DeviceDriver, sh ShaderSet, vertSpv []byte, renderPass core1_0.RenderPass, layout core1_0.PipelineLayout, bindings []core1_0.VertexInputBindingDescription, attrDescs []core1_0.VertexInputAttributeDescription) (core1_0.Pipeline, error) {
	vertModule, _, err := deviceDriver.CreateShaderModule(nil, core1_0.ShaderModuleCreateInfo{
		Code: bytesToUint32Slice(vertSpv),
	})
	if err != nil {
		return core1_0.Pipeline{}, err
	}
	defer deviceDriver.DestroyShaderModule(vertModule, nil)

	fragModule, _, err := deviceDriver.CreateShaderModule(nil, core1_0.ShaderModuleCreateInfo{
		Code: bytesToUint32Slice(sh.ShadowFrag),
	})
	if err != nil {
		return core1_0.Pipeline{}, err
	}
	defer deviceDriver.DestroyShaderModule(fragModule, nil)

	pipelines, _, err := deviceDriver.CreateGraphicsPipelines(nil, nil, core1_0.GraphicsPipelineCreateInfo{
		Stages: []core1_0.PipelineShaderStageCreateInfo{
			{Stage: core1_0.StageVertex, Module: vertModule, Name: "main"},
			{Stage: core1_0.StageFragment, Module: fragModule, Name: "main"},
		},
		VertexInputState: &core1_0.PipelineVertexInputStateCreateInfo{
			VertexBindingDescriptions:   bindings,
			VertexAttributeDescriptions: attrDescs,
		},
		InputAssemblyState: &core1_0.PipelineInputAssemblyStateCreateInfo{
			Topology: core1_0.PrimitiveTopologyTriangleList,
		},
		ViewportState: &core1_0.PipelineViewportStateCreateInfo{
			Viewports: []core1_0.Viewport{{
				X: 0, Y: 0,
				Width: ShadowMapSize, Height: ShadowMapSize,
				MinDepth: 0, MaxDepth: 1,
			}},
			Scissors: []core1_0.Rect2D{{
				Offset: core1_0.Offset2D{X: 0, Y: 0},
				Extent: core1_0.Extent2D{Width: ShadowMapSize, Height: ShadowMapSize},
			}},
		},
		RasterizationState: &core1_0.PipelineRasterizationStateCreateInfo{
			PolygonMode:             core1_0.PolygonModeFill,
			CullMode:                core1_0.CullModeFront, // Render back faces only; NoCastShadow skips single-sided geometry
			FrontFace:               core1_0.FrontFaceClockwise,
			LineWidth:               1.0,
			DepthBiasEnable:         true,
			DepthBiasConstantFactor: 1.0,
			DepthBiasSlopeFactor:    1.5,
		},
		MultisampleState: &core1_0.PipelineMultisampleStateCreateInfo{
			RasterizationSamples: core1_0.Samples1,
		},
		DepthStencilState: &core1_0.PipelineDepthStencilStateCreateInfo{
			DepthTestEnable:  true,
			DepthWriteEnable: true,
			DepthCompareOp:   core1_0.CompareOpLessOrEqual, // Standard Z for shadow map
		},
		ColorBlendState: &core1_0.PipelineColorBlendStateCreateInfo{},
		DynamicState: &core1_0.PipelineDynamicStateCreateInfo{
			DynamicStates: []core1_0.DynamicState{
				core1_0.DynamicStateViewport,
				core1_0.DynamicStateScissor,
			},
		},
		Layout:     layout,
		RenderPass: renderPass,
		Subpass:    0,
	})
	if err != nil {
		return core1_0.Pipeline{}, err
	}

	return pipelines[0], nil
}

// uploadLitUBO writes this frame's cascade matrices and environment values to
// the per-frame UBO (persistently mapped).
func (s *shadowResources) uploadLitUBO(frame int, vps [ShadowCascades]mgl32.Mat4, grade *NightGrade, palette *SkyPalette, vol *Volumetrics) {
	packLitUBO(s.lightVPMapped[frame], vps, grade, palette, vol)
}

// packLitUBO writes the whole block, every frame. Partial writes would leave
// whatever the last frame using this buffer put there, and with two frames in
// flight that is not even the previous frame's value.
//
// grade nil means DefaultNightGrade, palette nil means DefaultSkyPalette and
// vol nil means DefaultVolumetrics -- see NightGrade, SkyPalette and
// Volumetrics for why a caller's zero value must not be read as "no shift",
// "six black colours" or "march nothing".
func packLitUBO(dst []byte, vps [ShadowCascades]mgl32.Mat4, grade *NightGrade, palette *SkyPalette, vol *Volumetrics) {
	src := unsafe.Slice((*byte)(unsafe.Pointer(&vps[0][0])), cascadeUBOSize)
	copy(dst[:cascadeUBOSize], src)

	g := DefaultNightGrade()
	if grade != nil {
		g = *grade
	}
	off := nightGradeOffset
	for i, v := range [4]float32{g.Tint[0], g.Tint[1], g.Tint[2], g.Strength} {
		binary.LittleEndian.PutUint32(dst[off+i*4:], math.Float32bits(v))
	}

	p := DefaultSkyPalette()
	if palette != nil {
		p = *palette
	}
	// The fourth component is std140 padding the shader never reads. It is
	// written anyway rather than skipped, because this buffer is reused by a
	// frame in flight and "unread" is not the same as "safe to leave stale" --
	// see the note above about partial writes.
	for i, c := range p.endpoints() {
		o := skyPaletteOffset + i*16
		for j, v := range [4]float32{c[0], c[1], c[2], 0} {
			binary.LittleEndian.PutUint32(dst[o+j*4:], math.Float32bits(v))
		}
	}

	v := DefaultVolumetrics()
	if vol != nil {
		v = *vol
	}
	// Clamped here rather than in the shader, which would pay for it on every
	// step of every pixel. g at exactly +-1 makes the Henyey-Greenstein
	// denominator zero at one scattering angle, which is an infinity in the
	// middle of a sum -- and a NaN after it meets a zero-length step. 0.99 is
	// far past any value that reads as a look.
	hg := v.Anisotropy
	if !(hg > -0.99) { // also catches NaN
		hg = -0.99
	} else if hg > 0.99 {
		hg = 0.99
	}
	steps := v.Steps
	if steps < 0 {
		steps = 0
	} else if steps > MaxVolumetricSteps {
		steps = MaxVolumetricSteps
	}
	// The step count is sent as a float because it shares a vec4 with the
	// anisotropy and std140 would pad an int to the same four bytes anyway.
	// The shader converts it back with int(), exact for every count this
	// clamp allows. Clamping on this side rather than in the shader is what
	// keeps the loop bound something a reader can look up: a caller that
	// passes a million gets MaxVolumetricSteps, not a frame that never
	// finishes.
	for i, f := range [4]float32{hg, float32(steps), 0, 0} {
		binary.LittleEndian.PutUint32(dst[volumetricOffset+i*4:], math.Float32bits(f))
	}
}

// uploadLights writes this frame's light data into the three storage buffers
// the fragment shader reads (see shaders/lights.inc): the 64-byte header and
// the GpuLight array, the per-froxel cells, and the cells' light lists.
//
// lights must already be in clusters.Order -- the shader's lights[i] is what
// the cell lists index. Both come from the same Build, so a frame cannot mix
// one frame's grid with another's lights.
//
// Only clusters.Indices is copied, not the whole index buffer: it is already
// cut to the entries the cells point at, and the rest was zeroed at creation
// and is unreachable. At 1024 lights over a dense colony that is 40k entries
// out of 262144.
func (s *shadowResources) uploadLights(frame int, lights []GpuLight, clusters *lightcluster.Result, flags uint32, extent core1_0.Extent2D) {
	buf := s.lightMapped[frame]
	screenW, screenH := float32(extent.Width), float32(extent.Height)

	if clusters == nil {
		// No binning means no grid to trust: last frame's cells point into an
		// index buffer that is about to say something else. Report zero lights
		// and clear the grid rather than light the scene from a stale list.
		packLightHeader(buf[:lightHeaderSize], lightcluster.Mapping{Grid: lightcluster.DefaultGrid}, 0, screenW, screenH, flags)
		clear(s.clusterGridMapped[frame])
		return
	}

	// The binner's own budget is MaxLights, so this only bites if a caller
	// hands over a list it did not bin. Truncating here keeps the header's
	// count and the array that follows it agreeing, which is what stops the
	// shader reading past the end.
	if len(lights) > MaxLights {
		lights = lights[:MaxLights]
	}
	packLightHeader(buf[:lightHeaderSize], clusters.Mapping, uint32(len(lights)), screenW, screenH, flags)
	packLights(buf[lightHeaderSize:], lights)
	packCells(s.clusterGridMapped[frame], clusters.Cells)
	packIndices(s.lightIndexMapped[frame], clusters.Indices)
}

// ComputeCascadeVPs computes one orthographic light-space view-projection
// matrix per cascade, centered on the camera, with texel snapping to reduce
// shadow shimmer. The far cascade's frustum is a superset of the near one, so
// callers can cull shadow casters against the last cascade alone.
func ComputeCascadeVPs(sunDir [3]float32, cameraPos mgl32.Vec3) [ShadowCascades]mgl32.Mat4 {
	var vps [ShadowCascades]mgl32.Mat4
	for c := 0; c < ShadowCascades; c++ {
		vps[c] = computeLightVP(sunDir, cameraPos, cascadeRadii[c])
	}
	return vps
}

// computeLightVP computes an orthographic light-space view-projection matrix
// with the given half-extent radius, centered on the camera position.
func computeLightVP(sunDir [3]float32, cameraPos mgl32.Vec3, shadowRadius float32) mgl32.Mat4 {
	return computeLightVPCoverage(sunDir, cameraPos, ShadowCascadeCoverage{shadowRadius, shadowRadius, shadowRadius * 1.5})
}

func computeLightVPCoverage(sunDir [3]float32, cameraPos mgl32.Vec3, coverage ShadowCascadeCoverage) mgl32.Mat4 {
	shadowRadius := coverage.Radius
	// sunDir points TOWARD the sun. The light shines FROM the sun toward the scene.
	lightDir := mgl32.Vec3{sunDir[0], sunDir[1], sunDir[2]}.Normalize()

	lightUp := mgl32.Vec3{0, 1, 0}
	if math.Abs(float64(lightDir.Dot(lightUp))) > 0.99 {
		lightUp = mgl32.Vec3{0, 0, 1}
	}

	const shadowNear = float32(0.1)
	shadowFar := coverage.TowardLight + coverage.AwayFromLight

	// Place the light toward the sun from the scene center, looking back at the scene.
	lightPos := cameraPos.Add(lightDir.Mul(coverage.TowardLight))
	lightView := mgl32.LookAtV(lightPos, cameraPos, lightUp)

	// Orthographic projection covering the shadow volume.
	// mgl32.Ortho produces OpenGL clip Z [-1,1]. Remap to Vulkan [0,1].
	lightProj := mgl32.Ortho(
		-shadowRadius, shadowRadius,
		-shadowRadius, shadowRadius,
		shadowNear, shadowFar,
	)
	lightProj[10] = lightProj[10] * 0.5
	lightProj[14] = lightProj[14]*0.5 + 0.5

	lightVP := lightProj.Mul4(lightView)

	// Texel snapping: round clip-space XY to shadow map texel boundaries
	// to prevent shadow edges from shimmering as the camera moves.
	// In clip space, X and Y range from -1 to 1, so each texel is 2/ShadowMapSize.
	texelSize := 2.0 / float32(ShadowMapSize)
	shadowOrigin := lightVP.Mul4x1(mgl32.Vec4{0, 0, 0, 1})
	snapX := float32(math.Round(float64(shadowOrigin[0]/texelSize)))*texelSize - shadowOrigin[0]
	snapY := float32(math.Round(float64(shadowOrigin[1]/texelSize)))*texelSize - shadowOrigin[1]

	snap := mgl32.Translate3D(snapX, snapY, 0)
	lightVP = snap.Mul4(lightVP)

	return lightVP
}

// ComputeCubeFaceVP returns the view-projection matrix for one face of a cube shadow map.
// face: 0=+X, 1=-X, 2=+Y, 3=-Y, 4=+Z, 5=-Z
func ComputeCubeFaceVP(lightPos mgl32.Vec3, lightRange float32, face int) mgl32.Mat4 {
	type faceDir struct {
		target, up mgl32.Vec3
	}
	faces := [6]faceDir{
		{mgl32.Vec3{1, 0, 0}, mgl32.Vec3{0, -1, 0}},  // +X
		{mgl32.Vec3{-1, 0, 0}, mgl32.Vec3{0, -1, 0}}, // -X
		{mgl32.Vec3{0, 1, 0}, mgl32.Vec3{0, 0, 1}},   // +Y
		{mgl32.Vec3{0, -1, 0}, mgl32.Vec3{0, 0, -1}}, // -Y
		{mgl32.Vec3{0, 0, 1}, mgl32.Vec3{0, -1, 0}},  // +Z
		{mgl32.Vec3{0, 0, -1}, mgl32.Vec3{0, -1, 0}}, // -Z
	}

	fd := faces[face]
	view := mgl32.LookAtV(lightPos, lightPos.Add(fd.target), fd.up)

	// 90-degree FOV perspective projection, 1:1 aspect
	const near = float32(0.1)
	proj := mgl32.Perspective(mgl32.DegToRad(90.0), 1.0, near, lightRange)

	// Remap Z from [-1,1] to [0,1] for Vulkan (perspective formula).
	// For perspective, proj[11]=-1 (W-divide row), so the full remap is:
	//   new[10] = 0.5*old[10] + 0.5*old[11] = 0.5*old[10] - 0.5
	//   new[14] = 0.5*old[14] + 0.5*old[15] = 0.5*old[14]
	// (The ortho formula is different because ortho has proj[11]=0, proj[15]=1.)
	proj[10] = proj[10]*0.5 - 0.5
	proj[14] = proj[14] * 0.5

	return proj.Mul4(view)
}

// initCubeShadowLayout clears the point-light cube shadow maps to "fully lit"
// and moves them into the layout their descriptors advertise.
//
// This is required, not an optimization. The cube maps are created in
// ImageLayoutUndefined, but the descriptor written at setup time declares
// ImageLayoutDepthStencilReadOnlyOptimal and the lit fragment shader samples
// them on every draw. The transition that would reconcile the two happens when
// the cube render pass begins — and that whole pass is skipped unless a point
// light is actually casting (PointRange > 0). A scene with no point light
// therefore sampled an Undefined-layout image on every frame for the life of
// the process: undefined behavior per spec, and six validation errors per
// frame, one per cube face.
//
// Clearing as well as transitioning matters because a transition alone leaves
// uninitialized memory behind. Depth 1.0 is the far plane, which reads as
// "nothing occludes this fragment" — the correct meaning for a light that is
// not casting. Garbage depth would instead paint random shadows.
//
// Must be called after the Renderer's command pool exists, since it submits a
// one-shot command buffer.
func (s *shadowResources) initCubeShadowLayout(r *Renderer) error {
	cmdBuf, err := r.beginSingleTimeCommands()
	if err != nil {
		return err
	}

	cubeRange := core1_0.ImageSubresourceRange{
		AspectMask:     core1_0.ImageAspectDepth,
		BaseMipLevel:   0,
		LevelCount:     1,
		BaseArrayLayer: 0,
		LayerCount:     6,
	}

	toTransferDst := make([]core1_0.ImageMemoryBarrier, 0, maxFramesInFlight)
	toReadOnly := make([]core1_0.ImageMemoryBarrier, 0, maxFramesInFlight)
	for i := 0; i < maxFramesInFlight; i++ {
		toTransferDst = append(toTransferDst, core1_0.ImageMemoryBarrier{
			OldLayout:           core1_0.ImageLayoutUndefined,
			NewLayout:           core1_0.ImageLayoutTransferDstOptimal,
			SrcQueueFamilyIndex: -1,
			DstQueueFamilyIndex: -1,
			Image:               s.cubeImages[i],
			SubresourceRange:    cubeRange,
			SrcAccessMask:       0,
			DstAccessMask:       core1_0.AccessTransferWrite,
		})
		toReadOnly = append(toReadOnly, core1_0.ImageMemoryBarrier{
			OldLayout:           core1_0.ImageLayoutTransferDstOptimal,
			NewLayout:           core1_0.ImageLayoutDepthStencilReadOnlyOptimal,
			SrcQueueFamilyIndex: -1,
			DstQueueFamilyIndex: -1,
			Image:               s.cubeImages[i],
			SubresourceRange:    cubeRange,
			SrcAccessMask:       core1_0.AccessTransferWrite,
			DstAccessMask:       core1_0.AccessShaderRead,
		})
	}

	r.deviceDriver.CmdPipelineBarrier(cmdBuf,
		core1_0.PipelineStageTopOfPipe, core1_0.PipelineStageTransfer,
		0, nil, nil, toTransferDst)

	clear := core1_0.ClearValueDepthStencil{Depth: 1.0, Stencil: 0}
	for i := 0; i < maxFramesInFlight; i++ {
		r.deviceDriver.CmdClearDepthStencilImage(cmdBuf, s.cubeImages[i],
			core1_0.ImageLayoutTransferDstOptimal, &clear, cubeRange)
	}

	r.deviceDriver.CmdPipelineBarrier(cmdBuf,
		core1_0.PipelineStageTransfer, core1_0.PipelineStageFragmentShader,
		0, nil, nil, toReadOnly)

	return r.endSingleTimeCommands(cmdBuf)
}

// destroy releases all shadow mapping resources.
func (s *shadowResources) destroy(deviceDriver core1_0.DeviceDriver) {
	// Cube shadow resources
	if s.cubeSampler.Handle() != 0 {
		deviceDriver.DestroySampler(s.cubeSampler, nil)
	}
	for i := 0; i < maxFramesInFlight; i++ {
		for face := 0; face < 6; face++ {
			if s.cubeFramebuffers[i][face].Handle() != 0 {
				deviceDriver.DestroyFramebuffer(s.cubeFramebuffers[i][face], nil)
			}
			if s.cubeFaceViews[i][face].Handle() != 0 {
				deviceDriver.DestroyImageView(s.cubeFaceViews[i][face], nil)
			}
		}
		if s.cubeSamplerViews[i].Handle() != 0 {
			deviceDriver.DestroyImageView(s.cubeSamplerViews[i], nil)
		}
		if s.cubeMemories[i].Handle() != 0 {
			deviceDriver.FreeMemory(s.cubeMemories[i], nil)
		}
		if s.cubeImages[i].Handle() != 0 {
			deviceDriver.DestroyImage(s.cubeImages[i], nil)
		}
	}
	// Directional shadow resources
	if s.instancedPipeline.Handle() != 0 {
		deviceDriver.DestroyPipeline(s.instancedPipeline, nil)
	}
	if s.skinnedPipeline.Handle() != 0 {
		deviceDriver.DestroyPipeline(s.skinnedPipeline, nil)
	}
	if s.pipeline.Handle() != 0 {
		deviceDriver.DestroyPipeline(s.pipeline, nil)
	}
	if s.skinnedPipelineLayout.Handle() != 0 {
		deviceDriver.DestroyPipelineLayout(s.skinnedPipelineLayout, nil)
	}
	if s.pipelineLayout.Handle() != 0 {
		deviceDriver.DestroyPipelineLayout(s.pipelineLayout, nil)
	}
	if s.descriptorSetLayout.Handle() != 0 {
		deviceDriver.DestroyDescriptorSetLayout(s.descriptorSetLayout, nil)
	}
	for i := 0; i < maxFramesInFlight; i++ {
		if s.lightIndexMapped[i] != nil {
			deviceDriver.UnmapMemory(s.lightIndexMemories[i])
			s.lightIndexMapped[i] = nil
		}
		if s.lightIndexMemories[i].Handle() != 0 {
			deviceDriver.FreeMemory(s.lightIndexMemories[i], nil)
		}
		if s.lightIndexBuffers[i].Handle() != 0 {
			deviceDriver.DestroyBuffer(s.lightIndexBuffers[i], nil)
		}

		if s.clusterGridMapped[i] != nil {
			deviceDriver.UnmapMemory(s.clusterGridMemories[i])
			s.clusterGridMapped[i] = nil
		}
		if s.clusterGridMemories[i].Handle() != 0 {
			deviceDriver.FreeMemory(s.clusterGridMemories[i], nil)
		}
		if s.clusterGridBuffers[i].Handle() != 0 {
			deviceDriver.DestroyBuffer(s.clusterGridBuffers[i], nil)
		}

		if s.lightMapped[i] != nil {
			deviceDriver.UnmapMemory(s.lightMemories[i])
			s.lightMapped[i] = nil
		}
		if s.lightMemories[i].Handle() != 0 {
			deviceDriver.FreeMemory(s.lightMemories[i], nil)
		}
		if s.lightBuffers[i].Handle() != 0 {
			deviceDriver.DestroyBuffer(s.lightBuffers[i], nil)
		}
	}
	for i := 0; i < maxFramesInFlight; i++ {
		if s.lightVPMapped[i] != nil {
			deviceDriver.UnmapMemory(s.lightVPMemories[i])
			s.lightVPMapped[i] = nil
			s.shaderParameterMapped[i] = nil
		}
		if s.lightVPMemories[i].Handle() != 0 {
			deviceDriver.FreeMemory(s.lightVPMemories[i], nil)
		}
		if s.lightVPBuffers[i].Handle() != 0 {
			deviceDriver.DestroyBuffer(s.lightVPBuffers[i], nil)
		}
	}
	for i := 0; i < maxFramesInFlight; i++ {
		for c := 0; c < ShadowCascades; c++ {
			if s.framebuffers[i][c].Handle() != 0 {
				deviceDriver.DestroyFramebuffer(s.framebuffers[i][c], nil)
			}
		}
	}
	if s.renderPass.Handle() != 0 {
		deviceDriver.DestroyRenderPass(s.renderPass, nil)
	}
	if s.sampler.Handle() != 0 {
		deviceDriver.DestroySampler(s.sampler, nil)
	}
	for i := 0; i < maxFramesInFlight; i++ {
		for c := 0; c < ShadowCascades; c++ {
			if s.cascadeViews[i][c].Handle() != 0 {
				deviceDriver.DestroyImageView(s.cascadeViews[i][c], nil)
			}
		}
		if s.arrayViews[i].Handle() != 0 {
			deviceDriver.DestroyImageView(s.arrayViews[i], nil)
		}
		if s.memories[i].Handle() != 0 {
			deviceDriver.FreeMemory(s.memories[i], nil)
		}
		if s.images[i].Handle() != 0 {
			deviceDriver.DestroyImage(s.images[i], nil)
		}
	}
}
