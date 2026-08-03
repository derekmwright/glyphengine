package renderer

import (
	"log"
	"math"
	"slices"
	"unsafe"

	"github.com/go-gl/mathgl/mgl32"
	"github.com/vkngwrapper/core/v3/core1_0"
)

// MaxPointLights is the maximum number of unshadowed point lights supported.
const MaxPointLights = 32

// PointLightData matches the GLSL struct layout: vec4 posRange + vec4 color = 32 bytes.
type PointLightData struct {
	Pos   [3]float32
	Range float32
	Color [3]float32
	Pad   float32
}

// SceneLighting holds global lighting parameters passed via push constants.
type SceneLighting struct {
	SunDir      [3]float32  // direction toward the sun (normalized)
	SunColor    [3]float32  // sun RGB
	PointPos    [3]float32  // point light world position
	PointRange  float32     // point light falloff range
	PointColor  [3]float32  // point light RGB
	Ambient     [3]float32  // ambient RGB
	SkyColor    [4]float32  // RGBA clear color for the sky
	InvVP       [16]float32 // inverse view-projection for stars
	CameraPos   [3]float32  // camera eye position for stars
	Time        float32     // elapsed time for star twinkling
	NightFactor float32     // 0=day, 1=night for star visibility
	// SunElevation is the real sun's height, independent of which body is
	// currently the scene's directional light. The atmosphere is derived from
	// it, so it must not follow the sun/moon handover -- the moon rides high
	// exactly when the sky should be darkest, and driving the palette from the
	// light direction paints a noon sky at midnight.
	SunElevation float32

	// RealSunDir is the same value as a direction rather than just a height,
	// for the parts of the atmosphere that need to know *where* the sun is and
	// not only how high: the scattering halo and the sunset wash.
	//
	// Its y is SunElevation, so only x and z have to be sent; see the layout
	// note above pushConstantSize. Feeding those from SunDir instead is what
	// used to paint an orange sunset halo around the midnight moon.
	RealSunDir [3]float32

	// DrawSky draws the procedural dome; when false the frame keeps its clear
	// colour. DrawStars adds the star layer.
	DrawSky   bool
	DrawStars bool

	// CloudSteps is the volumetric cloud sample count; zero draws none.
	CloudSteps int

	// LightShafts is the god-ray strength; zero disables them. SunScreenPos is
	// where the sun lands in UV space, which is what the effect radiates from.
	LightShafts  float32
	SunScreenPos [2]float32

	// FogHeight is the altitude over which fog density falls to 1/e. Zero
	// selects the uniform-density falloff instead.
	FogHeight float32
	// FogBaseHeight is the world Y at which density equals FogDensity.
	FogBaseHeight float32

	VP            [16]float32                // camera view-projection matrix (for instanced grass)
	CameraRight   [3]float32                 // camera right vector (for billboard particles)
	CameraUp      [3]float32                 // camera up vector (for billboard particles)
	CascadeVPs    [ShadowCascades]mgl32.Mat4 // per-cascade light view-projections for shadow mapping
	ShadowEnabled bool                       // true when the sun is above the horizon

	PointLights     [MaxPointLights]PointLightData // unshadowed point lights
	PointLightCount int                            // number of active unshadowed point lights (0..32)

	FogDensity float32 // exp² distance fog density (0 disables fog)
}

// RenderObject pairs a mesh with its MVP matrix, model matrix, tint color, and optional texture for drawing.
type RenderObject struct {
	Mesh         *Mesh
	Texture      *Texture
	MVP          [16]float32
	Model        [16]float32
	Color        [3]float32
	Metallic     float32          // 0 = dielectric, 1 = metal
	Roughness    float32          // 0 = mirror, 1 = matte (default 0.5)
	Emissive     bool             // bypass lighting in lit shader (tint.w = 1.0)
	DoubleSided  bool             // render both front and back faces (no culling)
	NoCastShadow bool             // skip this object in shadow pass (receives shadows only)
	ShadowOnly   bool             // in light frustum but not camera frustum — shadow pass only
	Joints       *JointBuffer     // non-nil = skinned mesh
	TerrainMat   *TerrainMaterial // non-nil = render via the terrain splat pipeline
	Water        *WaterParams     // non-nil = render via the water pipeline

	// Material, when set, renders via the material pipeline: the lit path plus
	// normal, metallic-roughness, and occlusion maps. It supersedes Texture,
	// whose job the material's own albedo slot takes over.
	//
	// Ignored on skinned draws. The skinned pipeline spends set 1 on joint
	// matrices, so a material's set 0 has nowhere to sit alongside them without
	// a fourth descriptor set and a skinned material shader.
	Material *Material
}

// WaterParams are the per-surface constants the water shader needs. They
// mirror the fields of the engine's WaterOptions that the shader reads; the
// rest are baked into the mesh at build time.
type WaterParams struct {
	Amplitude       float32
	WaveLength      float32
	AbsorptionDepth float32
	RefractStrength float32
	WaveNoise       float32
}

// SortKey groups draws to minimize state switches in the main pass: pipeline
// variant (skinned / double-sided / material) first, then the descriptor the
// draw binds at set 0.
func (d *RenderObject) SortKey() uint64 {
	var key uint64
	if d.Joints != nil {
		key |= 1 << 63
	}
	if d.DoubleSided {
		key |= 1 << 62
	}
	// Material draws use a different pipeline from plain textured ones, so they
	// have to sort apart from them rather than interleave by descriptor handle.
	if d.Material != nil && d.Joints == nil {
		key |= 1 << 61
		key |= uint64(uintptr(d.Material.DescriptorSet.Handle())) & (1<<61 - 1)
		return key
	}
	if d.Texture != nil {
		key |= uint64(uintptr(d.Texture.DescriptorSet.Handle())) & (1<<61 - 1)
	}
	return key
}

// worldBoundSphere returns the draw's mesh bounding sphere transformed to
// world space. A zero radius means the mesh has no bounds (always draw).
func (d *RenderObject) worldBoundSphere() (cx, cy, cz, r float32) {
	if d.Mesh.BoundRadius <= 0 {
		return 0, 0, 0, 0
	}
	m := &d.Model
	c := d.Mesh.BoundCenter
	cx = m[0]*c[0] + m[4]*c[1] + m[8]*c[2] + m[12]
	cy = m[1]*c[0] + m[5]*c[1] + m[9]*c[2] + m[13]
	cz = m[2]*c[0] + m[6]*c[1] + m[10]*c[2] + m[14]
	sx := float32(math.Sqrt(float64(m[0]*m[0] + m[1]*m[1] + m[2]*m[2])))
	sy := float32(math.Sqrt(float64(m[4]*m[4] + m[5]*m[5] + m[6]*m[6])))
	sz := float32(math.Sqrt(float64(m[8]*m[8] + m[9]*m[9] + m[10]*m[10])))
	r = d.Mesh.BoundRadius * max(sx, sy, sz)
	return cx, cy, cz, r
}

// pushConstantSize is the total push constant block size in bytes.
// Layout: mvp(64) + model(64) + tint(16) + sunDir(16) + sunColor(16) +
// pointPos(16) + pointColor(16) + ambient(16) + cameraPos(16) + fog(16) = 256
//
// Several vec4s carry a scalar in their w rather than padding, because there is
// nowhere else to put one: sunColor.w is the real sun's elevation, pointColor.w
// is roughness, ambient.w is metallic, cameraPos.w is fog density, and fog.zw
// is the real sun's horizontal direction.
//
// This is the whole budget on a device that reports 256, which many do, and
// the engine already required more than Vulkan's guaranteed 128. New per-frame
// values should go in a uniform buffer rather than here; there is no room left.
// Renderer.New checks the device limit and fails with a clear message.
const pushConstantSize = 256

// createFramebuffers creates one framebuffer per scene colour view, each
// referencing its own colour and depth attachments. The views passed in are the
// HDR targets, not the swapchain -- only the tonemap pass draws to the
// swapchain -- but there is still one per swapchain image so a frame can be
// recorded while another is presenting.
func createFramebuffers(deviceDriver core1_0.DeviceDriver, renderPass core1_0.RenderPass, imageViews []core1_0.ImageView, depthViews []core1_0.ImageView, msaaViews []core1_0.ImageView, extent core1_0.Extent2D) ([]core1_0.Framebuffer, error) {
	framebuffers := make([]core1_0.Framebuffer, len(imageViews))

	for i, view := range imageViews {
		var attachments []core1_0.ImageView
		if msaaViews != nil {
			// MSAA: [msaaColor, depth, resolve(hdr)]
			attachments = []core1_0.ImageView{msaaViews[i], depthViews[i], view}
		} else {
			// No MSAA: [color(hdr), depth]
			attachments = []core1_0.ImageView{view, depthViews[i]}
		}
		fb, _, err := deviceDriver.CreateFramebuffer(nil, core1_0.FramebufferCreateInfo{
			RenderPass:  renderPass,
			Attachments: attachments,
			Width:       extent.Width,
			Height:      extent.Height,
			Layers:      1,
		})
		if err != nil {
			return nil, err
		}
		framebuffers[i] = fb
	}

	log.Printf("Created %d framebuffers", len(framebuffers))
	return framebuffers, nil
}

// createCommandPool creates a command pool for the graphics queue family with
// per-buffer reset support.
func createCommandPool(deviceDriver core1_0.DeviceDriver, graphicsFamily int) (core1_0.CommandPool, error) {
	pool, _, err := deviceDriver.CreateCommandPool(nil, core1_0.CommandPoolCreateInfo{
		Flags:            core1_0.CommandPoolCreateResetBuffer,
		QueueFamilyIndex: graphicsFamily,
	})
	if err != nil {
		return core1_0.CommandPool{}, err
	}

	log.Println("Command pool created")
	return pool, nil
}

// createCommandBuffers allocates primary command buffers from the pool.
func createCommandBuffers(deviceDriver core1_0.DeviceDriver, pool core1_0.CommandPool, count int) ([]core1_0.CommandBuffer, error) {
	buffers, _, err := deviceDriver.AllocateCommandBuffers(core1_0.CommandBufferAllocateInfo{
		CommandPool:        pool,
		Level:              core1_0.CommandBufferLevelPrimary,
		CommandBufferCount: count,
	})
	if err != nil {
		return nil, err
	}

	log.Printf("Allocated %d command buffers", len(buffers))
	return buffers, nil
}

// packLightingPC fills push constant floats [36..59] with lighting + camera data.
// Metallic/roughness are per-object and packed by the caller at [51] and [55].
func packLightingPC(pc *[64]float32, lighting SceneLighting) {
	// tint is at [32..35], filled by caller
	// sunDir vec4 at [36..39]
	pc[36] = lighting.SunDir[0]
	pc[37] = lighting.SunDir[1]
	pc[38] = lighting.SunDir[2]
	pc[39] = 0 // padding
	// sunColor vec4 at [40..43] (w = nightFactor for fog horizon color)
	pc[40] = lighting.SunColor[0]
	pc[41] = lighting.SunColor[1]
	pc[42] = lighting.SunColor[2]
	pc[43] = lighting.SunElevation // sunColor.w
	// pointPos vec4 at [44..47] (xyz = pos, w = range)
	pc[44] = lighting.PointPos[0]
	pc[45] = lighting.PointPos[1]
	pc[46] = lighting.PointPos[2]
	pc[47] = lighting.PointRange
	// pointColor vec4 at [48..51] (w = roughness, set by caller)
	pc[48] = lighting.PointColor[0]
	pc[49] = lighting.PointColor[1]
	pc[50] = lighting.PointColor[2]
	// pc[51] = roughness — set by caller per-object
	// ambient vec4 at [52..55] (w = metallic, set by caller)
	pc[52] = lighting.Ambient[0]
	pc[53] = lighting.Ambient[1]
	pc[54] = lighting.Ambient[2]
	// pc[55] = metallic — set by caller per-object
	// cameraPos vec4 at [56..59] (w = fog density)
	pc[56] = lighting.CameraPos[0]
	pc[57] = lighting.CameraPos[1]
	pc[58] = lighting.CameraPos[2]
	pc[59] = lighting.FogDensity
	// fog vec4 at [60..63]
	pc[60] = lighting.FogHeight
	pc[61] = lighting.FogBaseHeight
	// fog.zw = the real sun's horizontal direction. Its y is already in
	// sunColor.w, so the shader rebuilds the full vector from the two rather
	// than spending a vec4 the push constant block does not have on a third
	// copy of the same information. See atmSunDirFrom in atmosphere.inc.
	pc[62] = lighting.RealSunDir[0]
	pc[63] = lighting.RealSunDir[2]
}

// tonemapPass is everything the HDR resolve needs, grouped for the same reason
// materialPipelines is: recordCommandBuffer's parameter list is long enough
// already.
type tonemapPass struct {
	renderPass  core1_0.RenderPass
	pipeline    core1_0.Pipeline
	framebuffer core1_0.Framebuffer
	set         core1_0.DescriptorSet

	exposure float32
	curve    float32
	white    float32
}

// materialPipelines groups the material variant's pipelines with their layouts.
//
// Grouped rather than passed loose because recordCommandBuffer's parameter list
// is already long enough that four more positional handles of two repeated types
// would be easy to transpose, and transposing a pipeline with its layout is a
// mistake only the validation layer would catch.
type materialPipelines struct {
	pipeline          core1_0.Pipeline
	layout            core1_0.PipelineLayout
	doubleSided       core1_0.Pipeline
	doubleSidedLayout core1_0.PipelineLayout
}

// recordCommandBuffer records the shadow depth pass followed by the main render pass
// that draws all scene objects and overlays.
func recordCommandBuffer(
	deviceDriver core1_0.DeviceDriver,
	cmdBuf core1_0.CommandBuffer,
	renderPass core1_0.RenderPass,
	framebuffer core1_0.Framebuffer,
	pipeline core1_0.Pipeline,
	litDoubleSidedPipeline core1_0.Pipeline,
	overlayPipeline core1_0.Pipeline,
	skyPipeline core1_0.Pipeline,
	starsPipeline core1_0.Pipeline,
	uiPipeline core1_0.Pipeline,
	msdfPipeline core1_0.Pipeline,
	skinnedPipeline core1_0.Pipeline,
	grassPipeline core1_0.Pipeline,
	waterPipeline core1_0.Pipeline,
	godRayPipeline core1_0.Pipeline,
	waterRenderPass core1_0.RenderPass,
	waterFramebuffer core1_0.Framebuffer,
	sceneColor *sceneColorTarget,
	sceneImage core1_0.Image,
	tonemap tonemapPass,
	particlePipeline core1_0.Pipeline,
	terrainPipeline core1_0.Pipeline,
	mat materialPipelines,
	stats *RenderStats,
	pipelineLayout core1_0.PipelineLayout,
	litPipelineLayout core1_0.PipelineLayout,
	skinnedPipelineLayout core1_0.PipelineLayout,
	terrainPipelineLayout core1_0.PipelineLayout,
	extent core1_0.Extent2D,
	draws []RenderObject,
	overlays []RenderObject,
	uiOverlays []UIRenderObject,
	msdfOverlays []RenderObject,
	lighting SceneLighting,
	fallbackTexture *Texture,
	shadow *shadowResources,
	grass *GrassSystem,
	particles *ParticleSystem,
	frame int,
	msaaEnabled bool,
	timer *gpuTimer,
) error {
	_, err := deviceDriver.BeginCommandBuffer(cmdBuf, core1_0.CommandBufferBeginInfo{})
	if err != nil {
		return err
	}

	// Outside any render pass, which vkCmdResetQueryPool requires, and before
	// the first timestamp is written into the slot being reset.
	stats.reset()
	timer.reset(deviceDriver, cmdBuf, frame)
	timer.begin(deviceDriver, cmdBuf, frame, frameQuery)

	timer.begin(deviceDriver, cmdBuf, frame, PassShadow)
	// ── Sun shadow depth passes (one per cascade) ──
	// Always run each pass to ensure the depth layer is cleared to 1.0
	// (fully lit). When shadows are disabled, we clear but skip drawing geometry.
	for cascade := 0; cascade < ShadowCascades; cascade++ {
		err = deviceDriver.CmdBeginRenderPass(cmdBuf, core1_0.SubpassContentsInline, core1_0.RenderPassBeginInfo{
			RenderPass:  shadow.renderPass,
			Framebuffer: shadow.framebuffers[frame][cascade],
			RenderArea: core1_0.Rect2D{
				Offset: core1_0.Offset2D{X: 0, Y: 0},
				Extent: core1_0.Extent2D{Width: ShadowMapSize, Height: ShadowMapSize},
			},
			ClearValues: []core1_0.ClearValue{
				core1_0.ClearValueDepthStencil{Depth: 1.0, Stencil: 0},
			},
		})
		if err != nil {
			return err
		}

		if lighting.ShadowEnabled {
			cascadeVP := lighting.CascadeVPs[cascade]
			cascadeFrustum := ExtractFrustum(cascadeVP)

			shadowViewport := core1_0.Viewport{
				X: 0, Y: 0,
				Width: ShadowMapSize, Height: ShadowMapSize,
				MinDepth: 0, MaxDepth: 1,
			}
			shadowScissor := core1_0.Rect2D{
				Offset: core1_0.Offset2D{X: 0, Y: 0},
				Extent: core1_0.Extent2D{Width: ShadowMapSize, Height: ShadowMapSize},
			}

			deviceDriver.CmdBindPipeline(cmdBuf, core1_0.PipelineBindPointGraphics, shadow.pipeline)
			deviceDriver.CmdSetViewport(cmdBuf, shadowViewport)
			deviceDriver.CmdSetScissor(cmdBuf, shadowScissor)
			currentShadowSkinned := false

			for i := range draws {
				d := &draws[i]
				if d.Emissive || d.NoCastShadow || d.Water != nil {
					continue // skip emissive (celestial bodies) and non-shadow-casters (ground)
				}

				// Cull casters outside this cascade's frustum.
				if cx, cy, cz, cr := d.worldBoundSphere(); cr > 0 && !cascadeFrustum.SphereInFrustum(cx, cy, cz, cr) {
					continue
				}

				skinned := d.Joints != nil
				if skinned != currentShadowSkinned {
					if skinned {
						deviceDriver.CmdBindPipeline(cmdBuf, core1_0.PipelineBindPointGraphics, shadow.skinnedPipeline)
					} else {
						deviceDriver.CmdBindPipeline(cmdBuf, core1_0.PipelineBindPointGraphics, shadow.pipeline)
					}
					deviceDriver.CmdSetViewport(cmdBuf, shadowViewport)
					deviceDriver.CmdSetScissor(cmdBuf, shadowScissor)
					currentShadowSkinned = skinned
				}

				if skinned {
					deviceDriver.CmdBindDescriptorSets(cmdBuf, core1_0.PipelineBindPointGraphics, shadow.skinnedPipelineLayout, 0, []core1_0.DescriptorSet{d.Joints.descriptorSets[frame]}, nil)
				}

				deviceDriver.CmdBindVertexBuffers(cmdBuf, 0, []core1_0.Buffer{d.Mesh.vertexBuffer}, []int{0})

				// Push constants: cascadeVP * model as MVP, and model matrix
				lightModel := cascadeVP.Mul4(d.Model)
				var shadowPC [32]float32
				copy(shadowPC[:16], lightModel[:])
				copy(shadowPC[16:32], d.Model[:])
				activeLayout := shadow.pipelineLayout
				if skinned {
					activeLayout = shadow.skinnedPipelineLayout
				}
				pcBytes := unsafe.Slice((*byte)(unsafe.Pointer(&shadowPC[0])), 128)
				deviceDriver.CmdPushConstants(cmdBuf, activeLayout, core1_0.StageVertex, 0, pcBytes)

				stats.addDraw(1, d.Mesh.IndexCount, d.Mesh.VertexCount)
				if d.Mesh.IndexCount > 0 {
					deviceDriver.CmdBindIndexBuffer(cmdBuf, d.Mesh.indexBuffer, 0, d.Mesh.indexType)
					deviceDriver.CmdDrawIndexed(cmdBuf, d.Mesh.IndexCount, 1, 0, 0, 0)
				} else {
					deviceDriver.CmdDraw(cmdBuf, d.Mesh.VertexCount, 1, 0, 0)
				}
			}
		}

		deviceDriver.CmdEndRenderPass(cmdBuf)
	}

	// ── Point light cube shadow pass (6 faces) ──
	if lighting.PointRange > 0 {
		cubeViewport := core1_0.Viewport{
			X: 0, Y: 0,
			Width: PointShadowMapSize, Height: PointShadowMapSize,
			MinDepth: 0, MaxDepth: 1,
		}
		cubeScissor := core1_0.Rect2D{
			Offset: core1_0.Offset2D{X: 0, Y: 0},
			Extent: core1_0.Extent2D{Width: PointShadowMapSize, Height: PointShadowMapSize},
		}

		lightPos := mgl32.Vec3{lighting.PointPos[0], lighting.PointPos[1], lighting.PointPos[2]}

		// Pre-cull casters against the light's range sphere once; the face
		// loops below then only frustum-test this subset. A zero radius means
		// unbounded — always considered in range.
		type cubeCaster struct {
			idx            int
			cx, cy, cz, cr float32
		}
		casters := make([]cubeCaster, 0, len(draws))
		for i := range draws {
			d := &draws[i]
			if d.Emissive || d.NoCastShadow || d.Water != nil {
				continue
			}
			cx, cy, cz, cr := d.worldBoundSphere()
			if cr > 0 {
				dx := cx - lightPos.X()
				dy := cy - lightPos.Y()
				dz := cz - lightPos.Z()
				reach := lighting.PointRange + cr
				if dx*dx+dy*dy+dz*dz > reach*reach {
					continue
				}
			}
			casters = append(casters, cubeCaster{idx: i, cx: cx, cy: cy, cz: cz, cr: cr})
		}

		for face := 0; face < 6; face++ {
			faceVP := ComputeCubeFaceVP(lightPos, lighting.PointRange, face)
			faceFrustum := ExtractFrustum(faceVP)

			err = deviceDriver.CmdBeginRenderPass(cmdBuf, core1_0.SubpassContentsInline, core1_0.RenderPassBeginInfo{
				RenderPass:  shadow.renderPass,
				Framebuffer: shadow.cubeFramebuffers[frame][face],
				RenderArea: core1_0.Rect2D{
					Offset: core1_0.Offset2D{X: 0, Y: 0},
					Extent: core1_0.Extent2D{Width: PointShadowMapSize, Height: PointShadowMapSize},
				},
				ClearValues: []core1_0.ClearValue{
					core1_0.ClearValueDepthStencil{Depth: 1.0, Stencil: 0},
				},
			})
			if err != nil {
				return err
			}

			deviceDriver.CmdBindPipeline(cmdBuf, core1_0.PipelineBindPointGraphics, shadow.pipeline)
			deviceDriver.CmdSetViewport(cmdBuf, cubeViewport)
			deviceDriver.CmdSetScissor(cmdBuf, cubeScissor)
			currentCubeSkinned := false

			for _, c := range casters {
				d := &draws[c.idx]
				if c.cr > 0 && !faceFrustum.SphereInFrustum(c.cx, c.cy, c.cz, c.cr) {
					continue
				}

				skinned := d.Joints != nil
				if skinned != currentCubeSkinned {
					if skinned {
						deviceDriver.CmdBindPipeline(cmdBuf, core1_0.PipelineBindPointGraphics, shadow.skinnedPipeline)
					} else {
						deviceDriver.CmdBindPipeline(cmdBuf, core1_0.PipelineBindPointGraphics, shadow.pipeline)
					}
					deviceDriver.CmdSetViewport(cmdBuf, cubeViewport)
					deviceDriver.CmdSetScissor(cmdBuf, cubeScissor)
					currentCubeSkinned = skinned
				}

				if skinned {
					deviceDriver.CmdBindDescriptorSets(cmdBuf, core1_0.PipelineBindPointGraphics, shadow.skinnedPipelineLayout, 0, []core1_0.DescriptorSet{d.Joints.descriptorSets[frame]}, nil)
				}

				deviceDriver.CmdBindVertexBuffers(cmdBuf, 0, []core1_0.Buffer{d.Mesh.vertexBuffer}, []int{0})

				faceMVP := faceVP.Mul4(d.Model)
				var cubePC [32]float32
				copy(cubePC[:16], faceMVP[:])
				copy(cubePC[16:32], d.Model[:])
				activeLayout := shadow.pipelineLayout
				if skinned {
					activeLayout = shadow.skinnedPipelineLayout
				}
				pcBytes := unsafe.Slice((*byte)(unsafe.Pointer(&cubePC[0])), 128)
				deviceDriver.CmdPushConstants(cmdBuf, activeLayout, core1_0.StageVertex, 0, pcBytes)

				stats.addDraw(1, d.Mesh.IndexCount, d.Mesh.VertexCount)
				if d.Mesh.IndexCount > 0 {
					deviceDriver.CmdBindIndexBuffer(cmdBuf, d.Mesh.indexBuffer, 0, d.Mesh.indexType)
					deviceDriver.CmdDrawIndexed(cmdBuf, d.Mesh.IndexCount, 1, 0, 0, 0)
				} else {
					deviceDriver.CmdDraw(cmdBuf, d.Mesh.VertexCount, 1, 0, 0)
				}
			}

			deviceDriver.CmdEndRenderPass(cmdBuf)
		}
	}

	// ── Main render pass ──
	clearValues := []core1_0.ClearValue{
		core1_0.ClearValueFloat{lighting.SkyColor[0], lighting.SkyColor[1], lighting.SkyColor[2], lighting.SkyColor[3]},
		core1_0.ClearValueDepthStencil{Depth: 0.0, Stencil: 0},
	}
	if msaaEnabled {
		// 3rd clear value for the resolve attachment (LoadOpDontCare, but Vulkan requires the count to match)
		clearValues = append(clearValues, core1_0.ClearValueFloat{0, 0, 0, 1})
	}
	err = deviceDriver.CmdBeginRenderPass(cmdBuf, core1_0.SubpassContentsInline, core1_0.RenderPassBeginInfo{
		RenderPass:  renderPass,
		Framebuffer: framebuffer,
		RenderArea: core1_0.Rect2D{
			Offset: core1_0.Offset2D{X: 0, Y: 0},
			Extent: extent,
		},
		ClearValues: clearValues,
	})
	if err != nil {
		return err
	}

	viewport := core1_0.Viewport{
		X:        0,
		Y:        0,
		Width:    float32(extent.Width),
		Height:   float32(extent.Height),
		MinDepth: 0,
		MaxDepth: 1,
	}
	scissor := core1_0.Rect2D{
		Offset: core1_0.Offset2D{X: 0, Y: 0},
		Extent: extent,
	}

	// Draw lit scene geometry (static, double-sided, and skinned)
	deviceDriver.CmdBindPipeline(cmdBuf, core1_0.PipelineBindPointGraphics, pipeline)
	deviceDriver.CmdSetViewport(cmdBuf, viewport)
	deviceDriver.CmdSetScissor(cmdBuf, scissor)
	currentSkinned := false
	currentDoubleSided := false

	// Shadow descriptor set for this frame
	shadowDS := shadow.descriptorSets[frame]

	timer.end(deviceDriver, cmdBuf, frame, PassShadow)
	timer.begin(deviceDriver, cmdBuf, frame, PassTerrain)
	// Terrain pass: splat-mapped ground via the dedicated terrain pipeline
	// (set 0 = 4 detail/splat samplers, set 1 = shadow). Rendered before the
	// general lit geometry so the lit bind-cache below starts clean.
	terrainBound := false
	for i := range draws {
		d := &draws[i]
		if d.ShadowOnly || d.TerrainMat == nil {
			continue
		}
		if !terrainBound {
			deviceDriver.CmdBindPipeline(cmdBuf, core1_0.PipelineBindPointGraphics, terrainPipeline)
			deviceDriver.CmdSetViewport(cmdBuf, viewport)
			deviceDriver.CmdSetScissor(cmdBuf, scissor)
			terrainBound = true
		}
		deviceDriver.CmdBindDescriptorSets(cmdBuf, core1_0.PipelineBindPointGraphics, terrainPipelineLayout, 0, []core1_0.DescriptorSet{d.TerrainMat.DescriptorSet, shadowDS}, nil)
		deviceDriver.CmdBindVertexBuffers(cmdBuf, 0, []core1_0.Buffer{d.Mesh.vertexBuffer}, []int{0})

		var pc [64]float32
		copy(pc[:16], d.MVP[:])
		copy(pc[16:32], d.Model[:])
		pc[32], pc[33], pc[34], pc[35] = d.Color[0], d.Color[1], d.Color[2], 0.0
		packLightingPC(&pc, lighting)
		roughness := d.Roughness
		if roughness == 0 {
			roughness = 0.5
		}
		pc[51] = roughness
		pc[55] = d.Metallic
		pcBytes := unsafe.Slice((*byte)(unsafe.Pointer(&pc[0])), pushConstantSize)
		deviceDriver.CmdPushConstants(cmdBuf, terrainPipelineLayout, core1_0.StageVertex|core1_0.StageFragment, 0, pcBytes)

		stats.addDraw(1, d.Mesh.IndexCount, d.Mesh.VertexCount)
		if d.Mesh.IndexCount > 0 {
			deviceDriver.CmdBindIndexBuffer(cmdBuf, d.Mesh.indexBuffer, 0, d.Mesh.indexType)
			deviceDriver.CmdDrawIndexed(cmdBuf, d.Mesh.IndexCount, 1, 0, 0, 0)
		} else {
			deviceDriver.CmdDraw(cmdBuf, d.Mesh.VertexCount, 1, 0, 0)
		}
	}

	timer.end(deviceDriver, cmdBuf, frame, PassTerrain)
	timer.begin(deviceDriver, cmdBuf, frame, PassOpaque)
	// Descriptor bind cache — draws arrive sorted by pipeline then texture,
	// so consecutive draws usually share bindings.
	var lastTex *Texture
	var lastJoints *JointBuffer
	var lastMaterial *Material
	bindValid := false
	currentMaterial := false

	for i := range draws {
		d := &draws[i]
		if d.ShadowOnly {
			continue // not visible from camera — only needed for shadow pass
		}
		if d.TerrainMat != nil {
			continue // already drawn in the terrain pass
		}
		if d.Water != nil {
			continue // drawn by the water pipeline, after everything opaque
		}
		skinned := d.Joints != nil
		// See RenderObject.Material: the skinned pipeline already spends set 1
		// on joints, so a material has nowhere to bind there.
		material := d.Material != nil && !skinned

		// Switch pipeline if needed
		doubleSided := d.DoubleSided
		if skinned != currentSkinned || doubleSided != currentDoubleSided || material != currentMaterial {
			switch {
			case skinned:
				deviceDriver.CmdBindPipeline(cmdBuf, core1_0.PipelineBindPointGraphics, skinnedPipeline)
			case material && doubleSided:
				deviceDriver.CmdBindPipeline(cmdBuf, core1_0.PipelineBindPointGraphics, mat.doubleSided)
			case material:
				deviceDriver.CmdBindPipeline(cmdBuf, core1_0.PipelineBindPointGraphics, mat.pipeline)
			case doubleSided:
				deviceDriver.CmdBindPipeline(cmdBuf, core1_0.PipelineBindPointGraphics, litDoubleSidedPipeline)
			default:
				deviceDriver.CmdBindPipeline(cmdBuf, core1_0.PipelineBindPointGraphics, pipeline)
			}
			deviceDriver.CmdSetViewport(cmdBuf, viewport)
			deviceDriver.CmdSetScissor(cmdBuf, scissor)
			currentSkinned = skinned
			currentDoubleSided = doubleSided
			currentMaterial = material
			bindValid = false
		}

		tex := d.Texture
		if tex == nil {
			tex = fallbackTexture
		}

		activeLayout := litPipelineLayout
		if material {
			// The material's own descriptor set replaces the plain texture at
			// set 0; the shadow set stays where every lit variant expects it.
			activeLayout = mat.layout
			if doubleSided {
				activeLayout = mat.doubleSidedLayout
			}
			if !bindValid || d.Material != lastMaterial {
				deviceDriver.CmdBindDescriptorSets(cmdBuf, core1_0.PipelineBindPointGraphics, activeLayout, 0, []core1_0.DescriptorSet{d.Material.DescriptorSet, shadowDS}, nil)
				lastMaterial = d.Material
				bindValid = true
			}
		} else if skinned {
			activeLayout = skinnedPipelineLayout
			if !bindValid || tex != lastTex || d.Joints != lastJoints {
				// Skinned: set 0=tex, set 1=joints, set 2=shadow
				deviceDriver.CmdBindDescriptorSets(cmdBuf, core1_0.PipelineBindPointGraphics, skinnedPipelineLayout, 0, []core1_0.DescriptorSet{tex.DescriptorSet, d.Joints.descriptorSets[frame], shadowDS}, nil)
				lastTex = tex
				lastJoints = d.Joints
				bindValid = true
			}
		} else if !bindValid || tex != lastTex {
			// Static lit: set 0=tex, set 1=shadow
			deviceDriver.CmdBindDescriptorSets(cmdBuf, core1_0.PipelineBindPointGraphics, litPipelineLayout, 0, []core1_0.DescriptorSet{tex.DescriptorSet, shadowDS}, nil)
			lastTex = tex
			bindValid = true
		}

		deviceDriver.CmdBindVertexBuffers(cmdBuf, 0, []core1_0.Buffer{d.Mesh.vertexBuffer}, []int{0})

		var pc [64]float32
		copy(pc[:16], d.MVP[:])
		copy(pc[16:32], d.Model[:])
		pc[32] = d.Color[0]
		pc[33] = d.Color[1]
		pc[34] = d.Color[2]
		pc[35] = 0.0
		if d.Emissive {
			pc[35] = 1.0
		} else if d.DoubleSided {
			pc[35] = -1.0 // signal flat shading for foliage
		}
		packLightingPC(&pc, lighting)
		roughness := d.Roughness
		if roughness == 0 {
			roughness = 0.5 // default to semi-rough if unset
		}
		pc[51] = roughness  // pointColor.w = roughness
		pc[55] = d.Metallic // ambient.w = metallic
		pcBytes := unsafe.Slice((*byte)(unsafe.Pointer(&pc[0])), pushConstantSize)
		deviceDriver.CmdPushConstants(cmdBuf, activeLayout, core1_0.StageVertex|core1_0.StageFragment, 0, pcBytes)

		stats.addDraw(1, d.Mesh.IndexCount, d.Mesh.VertexCount)
		if d.Mesh.IndexCount > 0 {
			deviceDriver.CmdBindIndexBuffer(cmdBuf, d.Mesh.indexBuffer, 0, d.Mesh.indexType)
			deviceDriver.CmdDrawIndexed(cmdBuf, d.Mesh.IndexCount, 1, 0, 0, 0)
		} else {
			deviceDriver.CmdDraw(cmdBuf, d.Mesh.VertexCount, 1, 0, 0)
		}
	}

	timer.end(deviceDriver, cmdBuf, frame, PassOpaque)
	timer.begin(deviceDriver, cmdBuf, frame, PassGrass)
	// Draw instanced grass variants (two-sided, depth tested, lit)
	if grass != nil && len(grass.Variants) > 0 {
		deviceDriver.CmdBindPipeline(cmdBuf, core1_0.PipelineBindPointGraphics, grassPipeline)
		deviceDriver.CmdSetViewport(cmdBuf, viewport)
		deviceDriver.CmdSetScissor(cmdBuf, scissor)

		// Push constants shared across all variants
		var pc [64]float32
		copy(pc[:16], lighting.VP[:])
		// model = identity
		pc[16] = 1
		pc[21] = 1
		pc[26] = 1
		pc[31] = 1
		// tint = white (vertex colors provide grass color)
		pc[32] = 1.0
		pc[33] = 1.0
		pc[34] = 1.0
		pc[35] = -1.0 // flat shading for grass (double-sided foliage)
		packLightingPC(&pc, lighting)
		pc[39] = lighting.Time // sunDir.w = time for wind animation
		pc[51] = 1.0           // roughness = fully matte
		pc[55] = 0.0           // metallic = non-metal
		pcBytes := unsafe.Slice((*byte)(unsafe.Pointer(&pc[0])), pushConstantSize)
		deviceDriver.CmdPushConstants(cmdBuf, litPipelineLayout, core1_0.StageVertex|core1_0.StageFragment, 0, pcBytes)

		// Cull tiles against the camera frustum and the shader's hard cull
		// distance; only visible tiles are drawn (contiguous instance ranges).
		camFrustum := ExtractFrustum(mgl32.Mat4(lighting.VP))
		camX, camY, camZ := lighting.CameraPos[0], lighting.CameraPos[1], lighting.CameraPos[2]

		var lastFloraTex *Texture
		visible := grass.visibleScratch[:0]
		for i := range grass.Variants {
			v := &grass.Variants[i]
			if v.InstanceCount == 0 {
				continue
			}

			// Bind this variant's texture (flowers/clover differ from grass)
			// at set 0, shadow at set 1; skip when unchanged between variants.
			tex := v.Texture
			if tex == nil {
				tex = grass.Texture
			}
			if tex == nil {
				tex = fallbackTexture
			}
			if tex != lastFloraTex {
				deviceDriver.CmdBindDescriptorSets(cmdBuf, core1_0.PipelineBindPointGraphics, litPipelineLayout, 0, []core1_0.DescriptorSet{tex.DescriptorSet, shadowDS}, nil)
				lastFloraTex = tex
			}

			deviceDriver.CmdBindVertexBuffers(cmdBuf, 0, []core1_0.Buffer{v.Mesh.vertexBuffer, v.InstanceBuffer}, []int{0, 0})
			deviceDriver.CmdBindIndexBuffer(cmdBuf, v.Mesh.indexBuffer, 0, v.Mesh.indexType)

			// Cull first, then draw nearest tile first.
			//
			// Order cannot change the image: grass depth-tests and writes depth,
			// so whichever blade is nearest wins wherever two overlap. What order
			// does change is how much work reaches the shader. Grass is dense and
			// overlaps itself heavily, and every fragment that survives runs an
			// alpha test, a 25-tap shadow lookup and the fog model before being
			// thrown away by a nearer blade drawn later.
			//
			// Front to back lets early depth reject those before the shader runs.
			// The alpha-test discard does not prevent that: it stops the hardware
			// writing depth early, not testing it.
			visible = visible[:0]
			for _, tile := range v.Tiles {
				dx := tile.Center[0] - camX
				dy := tile.Center[1] - camY
				dz := tile.Center[2] - camZ
				d2 := dx*dx + dy*dy + dz*dz
				maxDist := float32(GrassMaxDistance) + tile.Radius
				if d2 > maxDist*maxDist {
					stats.GrassTilesCulled++
					continue
				}
				if !camFrustum.SphereInFrustum(tile.Center[0], tile.Center[1], tile.Center[2], tile.Radius) {
					stats.GrassTilesCulled++
					continue
				}
				visible = append(visible, tileDraw{tile: tile, dist2: d2})
			}
			slices.SortFunc(visible, func(a, b tileDraw) int {
				switch {
				case a.dist2 < b.dist2:
					return -1
				case a.dist2 > b.dist2:
					return 1
				default:
					return 0
				}
			})

			for _, vt := range visible {
				tile := vt.tile

				// Thin distant tiles. Instances are shuffled within a tile at
				// build time, so a prefix is a uniform sample of it and simply
				// drawing fewer of them removes blades evenly rather than
				// clearing one side.
				count := tile.Count
				if keep := grassKeepFraction(float32(math.Sqrt(float64(vt.dist2)))); keep < 1 {
					count = int(float32(count) * keep)
					if count < 1 {
						count = 1
					}
				}

				stats.GrassTilesDrawn++
				stats.addDraw(count, v.Mesh.IndexCount, v.Mesh.VertexCount)
				deviceDriver.CmdDrawIndexed(cmdBuf, v.Mesh.IndexCount, count, 0, 0, uint32(tile.FirstInstance))
			}
		}
		grass.visibleScratch = visible
	}

	timer.end(deviceDriver, cmdBuf, frame, PassGrass)
	timer.begin(deviceDriver, cmdBuf, frame, PassSky)
	// Draw the sky and stars, after everything that writes depth.
	//
	// The sky is a fullscreen triangle, so drawn first it shades every pixel on
	// screen and the terrain then paints over most of them. That is affordable
	// for a gradient and ruinous for anything raymarched: the cost is paid for
	// pixels the player never sees. Drawn last with a depth test instead, it
	// only shades where nothing else landed.
	//
	// The test is GreaterOrEqual rather than Greater because depth is reversed
	// -- cleared to 0, which is the far plane -- and the sky sits exactly there.
	// Greater would reject it everywhere.
	if lighting.DrawSky {
		deviceDriver.CmdBindPipeline(cmdBuf, core1_0.PipelineBindPointGraphics, skyPipeline)
		deviceDriver.CmdSetViewport(cmdBuf, viewport)
		deviceDriver.CmdSetScissor(cmdBuf, scissor)
		deviceDriver.CmdBindDescriptorSets(cmdBuf, core1_0.PipelineBindPointGraphics, pipelineLayout, 0, []core1_0.DescriptorSet{fallbackTexture.DescriptorSet}, nil)

		var pc [64]float32
		copy(pc[:16], lighting.InvVP[:])
		pc[16] = lighting.CameraPos[0]
		pc[17] = lighting.CameraPos[1]
		pc[18] = lighting.CameraPos[2]
		pc[32] = lighting.Time
		pc[33] = lighting.NightFactor
		pc[34] = float32(lighting.CloudSteps)
		pc[36] = lighting.SunDir[0]
		pc[37] = lighting.SunDir[1]
		pc[38] = lighting.SunDir[2]
		pc[40] = lighting.SunColor[0]
		pc[41] = lighting.SunColor[1]
		pc[42] = lighting.SunColor[2]
		pc[43] = lighting.SunElevation
		// fog.zw, at the same offsets every other shader reads it from: the real
		// sun's horizontal direction. sky.frag declares the intervening cameraPos
		// and fog members solely to land on these offsets, so that there is one
		// convention rather than a per-shader packing to get wrong.
		pc[62] = lighting.RealSunDir[0]
		pc[63] = lighting.RealSunDir[2]
		pcBytes := unsafe.Slice((*byte)(unsafe.Pointer(&pc[0])), pushConstantSize)
		deviceDriver.CmdPushConstants(cmdBuf, pipelineLayout, core1_0.StageVertex|core1_0.StageFragment, 0, pcBytes)
		deviceDriver.CmdDraw(cmdBuf, 3, 1, 0, 0)
	}

	// Draw procedural stars (additive blend, no vertex buffer)
	if lighting.DrawStars {
		deviceDriver.CmdBindPipeline(cmdBuf, core1_0.PipelineBindPointGraphics, starsPipeline)
		deviceDriver.CmdSetViewport(cmdBuf, viewport)
		deviceDriver.CmdSetScissor(cmdBuf, scissor)
		deviceDriver.CmdBindDescriptorSets(cmdBuf, core1_0.PipelineBindPointGraphics, pipelineLayout, 0, []core1_0.DescriptorSet{fallbackTexture.DescriptorSet}, nil)

		var pc [64]float32
		copy(pc[:16], lighting.InvVP[:])
		pc[16] = lighting.CameraPos[0]
		pc[17] = lighting.CameraPos[1]
		pc[18] = lighting.CameraPos[2]
		pc[32] = lighting.Time
		pc[33] = lighting.NightFactor
		pcBytes := unsafe.Slice((*byte)(unsafe.Pointer(&pc[0])), pushConstantSize)
		deviceDriver.CmdPushConstants(cmdBuf, pipelineLayout, core1_0.StageVertex|core1_0.StageFragment, 0, pcBytes)
		deviceDriver.CmdDraw(cmdBuf, 3, 1, 0, 0)
	}

	timer.end(deviceDriver, cmdBuf, frame, PassSky)
	timer.begin(deviceDriver, cmdBuf, frame, PassParticles)
	// Draw instanced billboard particles (additive blend, depth test only)
	if particles != nil && particles.InstanceCount > 0 {
		deviceDriver.CmdBindPipeline(cmdBuf, core1_0.PipelineBindPointGraphics, particlePipeline)
		deviceDriver.CmdSetViewport(cmdBuf, viewport)
		deviceDriver.CmdSetScissor(cmdBuf, scissor)
		deviceDriver.CmdBindDescriptorSets(cmdBuf, core1_0.PipelineBindPointGraphics, pipelineLayout, 0, []core1_0.DescriptorSet{fallbackTexture.DescriptorSet}, nil)

		// Push constants: VP at [0..15], cameraRight packed into model col 0, cameraUp into model col 1
		var pc [64]float32
		copy(pc[:16], lighting.VP[:])
		// model column 0 = cameraRight
		pc[16] = lighting.CameraRight[0]
		pc[17] = lighting.CameraRight[1]
		pc[18] = lighting.CameraRight[2]
		// model column 1 = cameraUp
		pc[20] = lighting.CameraUp[0]
		pc[21] = lighting.CameraUp[1]
		pc[22] = lighting.CameraUp[2]
		pcBytes := unsafe.Slice((*byte)(unsafe.Pointer(&pc[0])), pushConstantSize)
		deviceDriver.CmdPushConstants(cmdBuf, pipelineLayout, core1_0.StageVertex|core1_0.StageFragment, 0, pcBytes)

		deviceDriver.CmdBindVertexBuffers(cmdBuf, 0, []core1_0.Buffer{particles.QuadMesh.vertexBuffer, particles.InstanceBuffers[frame]}, []int{0, 0})
		deviceDriver.CmdBindIndexBuffer(cmdBuf, particles.QuadMesh.indexBuffer, 0, particles.QuadMesh.indexType)
		deviceDriver.CmdDrawIndexed(cmdBuf, particles.QuadMesh.IndexCount, particles.InstanceCount, 0, 0, 0)
	}

	timer.end(deviceDriver, cmdBuf, frame, PassParticles)
	timer.begin(deviceDriver, cmdBuf, frame, PassOverlay)
	// Draw UI panels (alpha blended, textured, 9-slice)
	if len(uiOverlays) > 0 {
		deviceDriver.CmdBindPipeline(cmdBuf, core1_0.PipelineBindPointGraphics, uiPipeline)

		for i := range uiOverlays {
			d := &uiOverlays[i]
			if d.Mesh.IndexCount == 0 && d.Mesh.VertexCount == 0 {
				continue
			}

			tex := d.Texture
			if tex == nil {
				tex = fallbackTexture
			}
			deviceDriver.CmdBindDescriptorSets(cmdBuf, core1_0.PipelineBindPointGraphics, pipelineLayout, 0, []core1_0.DescriptorSet{tex.DescriptorSet}, nil)
			deviceDriver.CmdBindVertexBuffers(cmdBuf, 0, []core1_0.Buffer{d.Mesh.vertexBuffer}, []int{0})

			var pc [64]float32
			copy(pc[:16], d.MVP[:])
			// model = identity
			pc[16] = 1
			pc[21] = 1
			pc[26] = 1
			pc[31] = 1
			// tint.rgb = 1 (vertex color already tinted), tint.a = opacity
			pc[32] = 1.0
			pc[33] = 1.0
			pc[34] = 1.0
			pc[35] = d.Opacity
			// sunDir.x reused as texture mode flag (0=panel 9-slice, 1=straight texture)
			if d.TextureMode {
				pc[36] = 1.0
			}
			pcBytes := unsafe.Slice((*byte)(unsafe.Pointer(&pc[0])), pushConstantSize)
			deviceDriver.CmdPushConstants(cmdBuf, pipelineLayout, core1_0.StageVertex|core1_0.StageFragment, 0, pcBytes)

			stats.addDraw(1, d.Mesh.IndexCount, d.Mesh.VertexCount)
			if d.Mesh.IndexCount > 0 {
				deviceDriver.CmdBindIndexBuffer(cmdBuf, d.Mesh.indexBuffer, 0, d.Mesh.indexType)
				deviceDriver.CmdDrawIndexed(cmdBuf, d.Mesh.IndexCount, 1, 0, 0, 0)
			} else {
				deviceDriver.CmdDraw(cmdBuf, d.Mesh.VertexCount, 1, 0, 0)
			}
		}
	}

	// Draw overlays (no depth test, no culling) — unlit path (bars, cooldowns)
	if len(overlays) > 0 {
		deviceDriver.CmdBindPipeline(cmdBuf, core1_0.PipelineBindPointGraphics, overlayPipeline)
		deviceDriver.CmdBindDescriptorSets(cmdBuf, core1_0.PipelineBindPointGraphics, pipelineLayout, 0, []core1_0.DescriptorSet{fallbackTexture.DescriptorSet}, nil)

		for i := range overlays {
			d := &overlays[i]
			if d.Mesh.IndexCount == 0 && d.Mesh.VertexCount == 0 {
				continue
			}
			deviceDriver.CmdBindVertexBuffers(cmdBuf, 0, []core1_0.Buffer{d.Mesh.vertexBuffer}, []int{0})

			// Overlay: MVP + identity model + tint, lighting zeroed
			var pc [64]float32
			copy(pc[:16], d.MVP[:])
			// model = identity
			pc[16] = 1
			pc[21] = 1
			pc[26] = 1
			pc[31] = 1
			pc[32] = d.Color[0]
			pc[33] = d.Color[1]
			pc[34] = d.Color[2]
			pc[35] = 1.0
			// lighting fields stay zero — overlay shader ignores them
			pcBytes := unsafe.Slice((*byte)(unsafe.Pointer(&pc[0])), pushConstantSize)
			deviceDriver.CmdPushConstants(cmdBuf, pipelineLayout, core1_0.StageVertex|core1_0.StageFragment, 0, pcBytes)

			stats.addDraw(1, d.Mesh.IndexCount, d.Mesh.VertexCount)
			if d.Mesh.IndexCount > 0 {
				deviceDriver.CmdBindIndexBuffer(cmdBuf, d.Mesh.indexBuffer, 0, d.Mesh.indexType)
				deviceDriver.CmdDrawIndexed(cmdBuf, d.Mesh.IndexCount, 1, 0, 0, 0)
			} else {
				deviceDriver.CmdDraw(cmdBuf, d.Mesh.VertexCount, 1, 0, 0)
			}
		}
	}

	// Draw MSDF text overlays (alpha blended, textured)
	if len(msdfOverlays) > 0 {
		deviceDriver.CmdBindPipeline(cmdBuf, core1_0.PipelineBindPointGraphics, msdfPipeline)

		for i := range msdfOverlays {
			d := &msdfOverlays[i]
			if d.Mesh.IndexCount == 0 && d.Mesh.VertexCount == 0 {
				continue
			}

			// Bind the MSDF atlas texture descriptor set
			tex := d.Texture
			if tex == nil {
				tex = fallbackTexture
			}
			deviceDriver.CmdBindDescriptorSets(cmdBuf, core1_0.PipelineBindPointGraphics, pipelineLayout, 0, []core1_0.DescriptorSet{tex.DescriptorSet}, nil)
			deviceDriver.CmdBindVertexBuffers(cmdBuf, 0, []core1_0.Buffer{d.Mesh.vertexBuffer}, []int{0})

			// Push constants: MVP + identity model + tint(rgb=color, w=screenPxRange)
			var pc [64]float32
			copy(pc[:16], d.MVP[:])
			// model = identity
			pc[16] = 1
			pc[21] = 1
			pc[26] = 1
			pc[31] = 1
			// tint.rgb = 1 (per-vertex color handles text color), tint.w = screenPxRange
			pc[32] = 1.0
			pc[33] = 1.0
			pc[34] = 1.0
			pc[35] = d.Color[0] // screenPxRange
			pcBytes := unsafe.Slice((*byte)(unsafe.Pointer(&pc[0])), pushConstantSize)
			deviceDriver.CmdPushConstants(cmdBuf, pipelineLayout, core1_0.StageVertex|core1_0.StageFragment, 0, pcBytes)

			stats.addDraw(1, d.Mesh.IndexCount, d.Mesh.VertexCount)
			if d.Mesh.IndexCount > 0 {
				deviceDriver.CmdBindIndexBuffer(cmdBuf, d.Mesh.indexBuffer, 0, d.Mesh.indexType)
				deviceDriver.CmdDrawIndexed(cmdBuf, d.Mesh.IndexCount, 1, 0, 0, 0)
			} else {
				deviceDriver.CmdDraw(cmdBuf, d.Mesh.VertexCount, 1, 0, 0)
			}
		}
	}

	deviceDriver.CmdEndRenderPass(cmdBuf)

	// Water needs the finished opaque frame as a texture, so it runs in a
	// second pass. Scenes without water skip it entirely.
	timer.end(deviceDriver, cmdBuf, frame, PassOverlay)

	timer.begin(deviceDriver, cmdBuf, frame, PassWater)
	if sceneColor != nil && (hasWater(draws) || lighting.LightShafts > 0) {
		if err := recordWaterPass(deviceDriver, stats, cmdBuf, waterRenderPass, waterFramebuffer,
			waterPipeline, godRayPipeline, pipelineLayout, litPipelineLayout, extent, draws, lighting,
			sceneColor, sceneImage, shadowDS, msaaEnabled); err != nil {
			return err
		}
	}

	timer.end(deviceDriver, cmdBuf, frame, PassWater)

	timer.begin(deviceDriver, cmdBuf, frame, PassTonemap)
	if err := recordTonemap(deviceDriver, cmdBuf, tonemap, pipelineLayout, extent); err != nil {
		return err
	}
	timer.end(deviceDriver, cmdBuf, frame, PassTonemap)

	timer.end(deviceDriver, cmdBuf, frame, frameQuery)

	_, err = deviceDriver.EndCommandBuffer(cmdBuf)
	return err
}

// hasWater reports whether any draw needs the refraction pass.
func hasWater(draws []RenderObject) bool {
	for i := range draws {
		if draws[i].Water != nil && !draws[i].ShadowOnly {
			return true
		}
	}
	return false
}

// recordWaterPass copies the opaque scene into a sampled image and draws the
// water surfaces against it in a second render pass.
//
// The copy is the only way a fragment shader can read what is already on
// screen; the alternative, an input attachment, can only read the pixel being
// written, and refraction is precisely a read of a *different* pixel.
func recordWaterPass(
	deviceDriver core1_0.DeviceDriver,
	stats *RenderStats,
	cmdBuf core1_0.CommandBuffer,
	waterRenderPass core1_0.RenderPass,
	framebuffer core1_0.Framebuffer,
	waterPipeline core1_0.Pipeline,
	godRayPipeline core1_0.Pipeline,
	pipelineLayout core1_0.PipelineLayout,
	litPipelineLayout core1_0.PipelineLayout,
	extent core1_0.Extent2D,
	draws []RenderObject,
	lighting SceneLighting,
	sceneColor *sceneColorTarget,
	sceneImage core1_0.Image,
	shadowDS core1_0.DescriptorSet,
	msaa bool,
) error {
	colorRange := core1_0.ImageSubresourceRange{
		AspectMask: core1_0.ImageAspectColor,
		LevelCount: 1, LayerCount: 1,
	}

	// The first pass left the HDR target in ShaderReadOnlyOptimal. Borrow it as a
	// transfer source, copy it, and put it back so the water pass can render into
	// it and the tonemap pass can sample it afterwards.
	//
	// Copying the HDR image rather than the swapchain is what keeps refraction
	// working: the swapchain no longer holds the scene at this point in the
	// frame -- nothing has been tonemapped into it yet.
	deviceDriver.CmdPipelineBarrier(cmdBuf,
		core1_0.PipelineStageColorAttachmentOutput, core1_0.PipelineStageTransfer, 0, nil, nil,
		[]core1_0.ImageMemoryBarrier{
			{
				OldLayout:           core1_0.ImageLayoutShaderReadOnlyOptimal,
				NewLayout:           core1_0.ImageLayoutTransferSrcOptimal,
				SrcQueueFamilyIndex: -1, DstQueueFamilyIndex: -1,
				Image:            sceneImage,
				SubresourceRange: colorRange,
				SrcAccessMask:    core1_0.AccessColorAttachmentWrite,
				DstAccessMask:    core1_0.AccessTransferRead,
			},
			{
				// Previous contents are irrelevant; the whole image is rewritten.
				OldLayout:           core1_0.ImageLayoutUndefined,
				NewLayout:           core1_0.ImageLayoutTransferDstOptimal,
				SrcQueueFamilyIndex: -1, DstQueueFamilyIndex: -1,
				Image:            sceneColor.image,
				SubresourceRange: colorRange,
				SrcAccessMask:    0,
				DstAccessMask:    core1_0.AccessTransferWrite,
			},
		})

	layers := core1_0.ImageSubresourceLayers{
		AspectMask: core1_0.ImageAspectColor,
		LayerCount: 1,
	}
	deviceDriver.CmdCopyImage(cmdBuf,
		sceneImage, core1_0.ImageLayoutTransferSrcOptimal,
		sceneColor.image, core1_0.ImageLayoutTransferDstOptimal,
		core1_0.ImageCopy{
			SrcSubresource: layers,
			DstSubresource: layers,
			Extent:         core1_0.Extent3D{Width: extent.Width, Height: extent.Height, Depth: 1},
		})

	deviceDriver.CmdPipelineBarrier(cmdBuf,
		core1_0.PipelineStageTransfer, core1_0.PipelineStageFragmentShader, 0, nil, nil,
		[]core1_0.ImageMemoryBarrier{{
			OldLayout:           core1_0.ImageLayoutTransferDstOptimal,
			NewLayout:           core1_0.ImageLayoutShaderReadOnlyOptimal,
			SrcQueueFamilyIndex: -1, DstQueueFamilyIndex: -1,
			Image:            sceneColor.image,
			SubresourceRange: colorRange,
			SrcAccessMask:    core1_0.AccessTransferWrite,
			DstAccessMask:    core1_0.AccessShaderRead,
		}})

	// No barrier back for the HDR scene image. With MSAA the water pass
	// declares its resolve target Undefined and rewrites it wholesale; without
	// MSAA it declares TransferSrc, matching the copy. Either way the render
	// pass performs the transition, and a manual barrier to Undefined is not a
	// legal layout transition to ask for.
	_ = msaa

	err := deviceDriver.CmdBeginRenderPass(cmdBuf, core1_0.SubpassContentsInline, core1_0.RenderPassBeginInfo{
		RenderPass:  waterRenderPass,
		Framebuffer: framebuffer,
		RenderArea: core1_0.Rect2D{
			Offset: core1_0.Offset2D{X: 0, Y: 0},
			Extent: extent,
		},
	})
	if err != nil {
		return err
	}

	viewport := core1_0.Viewport{
		Width: float32(extent.Width), Height: float32(extent.Height),
		MinDepth: 0, MaxDepth: 1,
	}
	scissor := core1_0.Rect2D{Extent: extent}

	for i := range draws {
		d := &draws[i]
		if d.Water == nil || d.ShadowOnly {
			continue
		}
		deviceDriver.CmdBindPipeline(cmdBuf, core1_0.PipelineBindPointGraphics, waterPipeline)
		deviceDriver.CmdSetViewport(cmdBuf, viewport)
		deviceDriver.CmdSetScissor(cmdBuf, scissor)

		// Set 0 is the opaque scene rather than a material texture.
		deviceDriver.CmdBindDescriptorSets(cmdBuf, core1_0.PipelineBindPointGraphics, litPipelineLayout, 0,
			[]core1_0.DescriptorSet{sceneColor.texture.DescriptorSet, shadowDS}, nil)

		var pc [64]float32
		copy(pc[:16], d.MVP[:])
		copy(pc[16:32], d.Model[:])
		// tint carries the wave parameters; the water shader has no use for a
		// colour there, since both of its colours are per-vertex.
		pc[32] = lighting.Time
		pc[33] = d.Water.Amplitude
		pc[34] = d.Water.WaveLength
		pc[35] = d.Water.RefractStrength
		packLightingPC(&pc, lighting)
		pc[51] = d.Water.AbsorptionDepth // pointColor.w
		pc[55] = 0
		// sunDir.w, which packLightingPC leaves as padding and only the grass
		// pipeline otherwise claims. It is the last free scalar in this block.
		pc[39] = d.Water.WaveNoise
		pcBytes := unsafe.Slice((*byte)(unsafe.Pointer(&pc[0])), pushConstantSize)
		deviceDriver.CmdPushConstants(cmdBuf, litPipelineLayout, core1_0.StageVertex|core1_0.StageFragment, 0, pcBytes)

		deviceDriver.CmdBindVertexBuffers(cmdBuf, 0, []core1_0.Buffer{d.Mesh.vertexBuffer}, []int{0})
		stats.addDraw(1, d.Mesh.IndexCount, d.Mesh.VertexCount)
		if d.Mesh.IndexCount > 0 {
			deviceDriver.CmdBindIndexBuffer(cmdBuf, d.Mesh.indexBuffer, 0, d.Mesh.indexType)
			deviceDriver.CmdDrawIndexed(cmdBuf, d.Mesh.IndexCount, 1, 0, 0, 0)
		} else {
			deviceDriver.CmdDraw(cmdBuf, d.Mesh.VertexCount, 1, 0, 0)
		}
	}

	deviceDriver.CmdEndRenderPass(cmdBuf)
	return nil
}
