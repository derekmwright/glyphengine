package renderer

import (
	"encoding/binary"
	"fmt"
	"log"
	"math"
	"unsafe"

	"github.com/go-gl/mathgl/mgl32"
	"github.com/vkngwrapper/core/v3/core1_0"
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

// cascadeUBOSize holds one mat4 light VP per cascade.
const cascadeUBOSize = ShadowCascades * 64

// pointLightsUBOSize is the UBO size for up to 32 unshadowed point lights.
// Layout: int numLights (4B) + 12B padding + 32 * PointLightData (32B each) = 1040 bytes.
const pointLightsUBOSize = 16 + 32*32

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
	lightVPBuffers  [maxFramesInFlight]core1_0.Buffer
	lightVPMemories [maxFramesInFlight]core1_0.DeviceMemory
	lightVPMapped   [maxFramesInFlight][]byte

	// Per-frame UBOs for unshadowed point lights, up to 32 (persistently mapped)
	pointLightsBuffers  [maxFramesInFlight]core1_0.Buffer
	pointLightsMemories [maxFramesInFlight]core1_0.DeviceMemory
	pointLightsMapped   [maxFramesInFlight][]byte

	// Descriptor set layout: binding 0 = UBO (vertex), binding 1 = shadow sampler (fragment)
	descriptorSetLayout core1_0.DescriptorSetLayout
	// Per-frame descriptor sets, each referencing its own UBO + its own shadow map
	descriptorSets [maxFramesInFlight]core1_0.DescriptorSet

	// Shadow pass pipeline layouts (push constants only, no texture descriptor)
	pipelineLayout        core1_0.PipelineLayout // static shadow: push constants 128B, no sets
	skinnedPipelineLayout core1_0.PipelineLayout // skinned shadow: push constants 128B, set 0 = joints

	pipeline        core1_0.Pipeline // static depth-only
	skinnedPipeline core1_0.Pipeline // skinned depth-only

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
			Size:        cascadeUBOSize,
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

		ptr, _, err := deviceDriver.MapMemory(s.lightVPMemories[i], 0, cascadeUBOSize, 0)
		if err != nil {
			s.destroy(deviceDriver)
			return nil, fmt.Errorf("map shadow UBO %d: %w", i, err)
		}
		s.lightVPMapped[i] = unsafe.Slice((*byte)(ptr), cascadeUBOSize)
	}

	// Per-frame UBOs for unshadowed point lights (1040 bytes each)
	for i := 0; i < maxFramesInFlight; i++ {
		s.pointLightsBuffers[i], _, err = deviceDriver.CreateBuffer(nil, core1_0.BufferCreateInfo{
			Size:        pointLightsUBOSize,
			Usage:       core1_0.BufferUsageUniformBuffer,
			SharingMode: core1_0.SharingModeExclusive,
		})
		if err != nil {
			s.destroy(deviceDriver)
			return nil, fmt.Errorf("point lights UBO buffer %d: %w", i, err)
		}

		bufMemReqs := deviceDriver.GetBufferMemoryRequirements(s.pointLightsBuffers[i])
		bufMemType, err := findMemoryType(instanceDriver, physicalDevice, bufMemReqs.MemoryTypeBits,
			core1_0.MemoryPropertyHostVisible|core1_0.MemoryPropertyHostCoherent)
		if err != nil {
			s.destroy(deviceDriver)
			return nil, err
		}

		s.pointLightsMemories[i], _, err = deviceDriver.AllocateMemory(nil, core1_0.MemoryAllocateInfo{
			AllocationSize:  bufMemReqs.Size,
			MemoryTypeIndex: bufMemType,
		})
		if err != nil {
			s.destroy(deviceDriver)
			return nil, fmt.Errorf("point lights UBO memory %d: %w", i, err)
		}

		_, err = deviceDriver.BindBufferMemory(s.pointLightsBuffers[i], s.pointLightsMemories[i], 0)
		if err != nil {
			s.destroy(deviceDriver)
			return nil, fmt.Errorf("bind point lights UBO %d: %w", i, err)
		}

		ptr, _, err := deviceDriver.MapMemory(s.pointLightsMemories[i], 0, pointLightsUBOSize, 0)
		if err != nil {
			s.destroy(deviceDriver)
			return nil, fmt.Errorf("map point lights UBO %d: %w", i, err)
		}
		s.pointLightsMapped[i] = unsafe.Slice((*byte)(ptr), pointLightsUBOSize)
	}

	// Descriptor set layout: binding 0 = UBO, binding 1 = sun shadow sampler, binding 2 = point cube sampler, binding 3 = point lights UBO
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
				DescriptorType:  core1_0.DescriptorTypeUniformBuffer,
				DescriptorCount: 1,
				StageFlags:      core1_0.StageFragment,
			},
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

	// Update descriptor sets now that all resources (UBO, sun shadow, cube shadow) are ready.
	for i := 0; i < maxFramesInFlight; i++ {
		err = deviceDriver.UpdateDescriptorSets([]core1_0.WriteDescriptorSet{
			{
				DstSet:         s.descriptorSets[i],
				DstBinding:     0,
				DescriptorType: core1_0.DescriptorTypeUniformBuffer,
				BufferInfo: []core1_0.DescriptorBufferInfo{
					{
						Buffer: s.lightVPBuffers[i],
						Offset: 0,
						Range:  cascadeUBOSize,
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
				DescriptorType: core1_0.DescriptorTypeUniformBuffer,
				BufferInfo: []core1_0.DescriptorBufferInfo{
					{
						Buffer: s.pointLightsBuffers[i],
						Offset: 0,
						Range:  pointLightsUBOSize,
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

// createShadowPipeline creates a depth-only pipeline for the shadow pass.
func createShadowPipeline(deviceDriver core1_0.DeviceDriver, sh ShaderSet, renderPass core1_0.RenderPass, layout core1_0.PipelineLayout, skinned bool) (core1_0.Pipeline, error) {
	var vertSpv []byte
	if skinned {
		vertSpv = sh.ShadowSkinnedVert
	} else {
		vertSpv = sh.ShadowVert
	}

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

	var bindingDesc core1_0.VertexInputBindingDescription
	var attrDescs []core1_0.VertexInputAttributeDescription
	if skinned {
		bindingDesc = skinnedVertexBindingDescription()
		attrDescs = skinnedVertexAttributeDescriptions()
	} else {
		bindingDesc = vertexBindingDescription()
		attrDescs = vertexAttributeDescriptions()
	}

	pipelines, _, err := deviceDriver.CreateGraphicsPipelines(nil, nil, core1_0.GraphicsPipelineCreateInfo{
		Stages: []core1_0.PipelineShaderStageCreateInfo{
			{Stage: core1_0.StageVertex, Module: vertModule, Name: "main"},
			{Stage: core1_0.StageFragment, Module: fragModule, Name: "main"},
		},
		VertexInputState: &core1_0.PipelineVertexInputStateCreateInfo{
			VertexBindingDescriptions:   []core1_0.VertexInputBindingDescription{bindingDesc},
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

// uploadCascadeVPs writes the per-cascade light VP matrices to the per-frame
// UBO (persistently mapped).
func (s *shadowResources) uploadCascadeVPs(frame int, vps [ShadowCascades]mgl32.Mat4) {
	src := unsafe.Slice((*byte)(unsafe.Pointer(&vps[0][0])), cascadeUBOSize)
	copy(s.lightVPMapped[frame], src)
}

// uploadPointLights serializes the unshadowed point lights directly into the
// per-frame UBO (persistently mapped), matching the GLSL LightBlock layout:
// int numLights (4B) + 12B pad + 32 * {vec4 posRange, vec4 color}.
func (s *shadowResources) uploadPointLights(frame int, lights *[MaxPointLights]PointLightData, count int) {
	buf := s.pointLightsMapped[frame]
	if count > MaxPointLights {
		count = MaxPointLights
	}
	binary.LittleEndian.PutUint32(buf[0:4], uint32(count))
	for i := 0; i < count; i++ {
		off := 16 + i*32
		binary.LittleEndian.PutUint32(buf[off+0:], math.Float32bits(lights[i].Pos[0]))
		binary.LittleEndian.PutUint32(buf[off+4:], math.Float32bits(lights[i].Pos[1]))
		binary.LittleEndian.PutUint32(buf[off+8:], math.Float32bits(lights[i].Pos[2]))
		binary.LittleEndian.PutUint32(buf[off+12:], math.Float32bits(lights[i].Range))
		binary.LittleEndian.PutUint32(buf[off+16:], math.Float32bits(lights[i].Color[0]))
		binary.LittleEndian.PutUint32(buf[off+20:], math.Float32bits(lights[i].Color[1]))
		binary.LittleEndian.PutUint32(buf[off+24:], math.Float32bits(lights[i].Color[2]))
	}
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
	// sunDir points TOWARD the sun. The light shines FROM the sun toward the scene.
	lightDir := mgl32.Vec3{sunDir[0], sunDir[1], sunDir[2]}.Normalize()

	lightUp := mgl32.Vec3{0, 1, 0}
	if math.Abs(float64(lightDir.Dot(lightUp))) > 0.99 {
		lightUp = mgl32.Vec3{0, 0, 1}
	}

	const shadowNear = float32(0.1)
	shadowFar := shadowRadius * 2.5

	// Place the light toward the sun from the scene center, looking back at the scene.
	lightPos := cameraPos.Add(lightDir.Mul(shadowRadius))
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
		if s.pointLightsMapped[i] != nil {
			deviceDriver.UnmapMemory(s.pointLightsMemories[i])
			s.pointLightsMapped[i] = nil
		}
		if s.pointLightsMemories[i].Handle() != 0 {
			deviceDriver.FreeMemory(s.pointLightsMemories[i], nil)
		}
		if s.pointLightsBuffers[i].Handle() != 0 {
			deviceDriver.DestroyBuffer(s.pointLightsBuffers[i], nil)
		}
	}
	for i := 0; i < maxFramesInFlight; i++ {
		if s.lightVPMapped[i] != nil {
			deviceDriver.UnmapMemory(s.lightVPMemories[i])
			s.lightVPMapped[i] = nil
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
