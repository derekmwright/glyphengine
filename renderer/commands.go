package renderer

import (
	"fmt"
	"log"
	"math"
	"slices"

	"github.com/go-gl/mathgl/mgl32"
	"github.com/vkngwrapper/core/v3/core1_0"

	"github.com/derekmwright/glyphengine/renderer/lightcluster"
)

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

	// MilkyWay is the galactic band's strength in the star pass, 0 to 1.
	MilkyWay float32

	// StarDensity scales how many stars the sky draws, 1 being the default.
	StarDensity float32

	// CloudSteps is the volumetric cloud sample count; zero draws none.
	CloudSteps int

	// LightShafts is the god-ray strength this frame will actually draw with,
	// and SunScreenPos is where the sun lands in UV space, which is what the
	// effect radiates from.
	//
	// "Actually draw with" rather than "what the game set": the screen-edge
	// fade is folded in before it gets here (shaftEdgeFade, app.go), so zero
	// means the pass cannot produce a pixel -- sun below the horizon, behind
	// the camera, past the edge fade, or switched off. That is what lets
	// recordCommandBuffer decide whether a frame with no water has to pay for
	// a scene copy and a second render pass at all, which it cannot do from a
	// strength that has not been faded yet. A caller driving the renderer
	// directly owns that fade itself.
	LightShafts  float32
	SunScreenPos [2]float32

	// ShaftShape is how the shafts look rather than how strong they are. The
	// zero value is DefaultLightShaftShape, field by field, so a caller that
	// has never heard of it draws what it drew before the shape was tunable.
	ShaftShape LightShaftShape

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

	// NightGrade is the scotopic grade the lit shaders apply as daylight goes.
	// Nil means DefaultNightGrade, which is deliberately not the same as a
	// zero-valued NightGrade: that would read as Strength 0, and a caller who
	// has never heard of this field would silently lose its nights.
	NightGrade *NightGrade

	// SkyPalette is the six colours the dome, the fog and the water's
	// reflection all blend between. Nil means DefaultSkyPalette, for the same
	// reason NightGrade is a pointer: the zero value is six black colours.
	SkyPalette *SkyPalette

	// Volumetrics is the in-scattering march's medium and sampling. Nil means
	// DefaultVolumetrics, for the same reason again: a zero value's Steps is
	// 0, which marches nothing, so a light that asked to scatter would
	// silently not.
	Volumetrics *Volumetrics

	// Lights are the unshadowed point + spot lights for the GPU light buffer
	// (see shaders/lights.inc), in Clusters.Order: the cell lists in Clusters
	// index this slice, so the two must come from the same frame's binning.
	Lights []GpuLight
	// Clusters is that binning -- the froxel lookup, the cells and their
	// light lists. Nil means the caller did no binning, and the frame is
	// uploaded with no lights at all rather than with a stale grid.
	Clusters *lightcluster.Result
	// LightFlags is the header flag word: bit0 = brute force, bit1 = debug
	// heatmap. See Engine.SetLightDebugMode.
	LightFlags uint32

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
	Alpha        float32          // per-object opacity; 0 means opaque (see IsTranslucent)
	Instances    *InstanceSet     // non-nil = one instanced draw of the whole set
	DoubleSided  bool             // render both front and back faces (no culling)
	NoCastShadow bool             // skip this object in shadow pass (receives shadows only)
	ShadowOnly   bool             // in light frustum but not camera frustum — shadow pass only
	Joints       *JointBuffer     // non-nil = skinned mesh
	TerrainMat   *TerrainMaterial // non-nil = render via the terrain splat pipeline
	Water        *WaterParams     // non-nil = render via the water pipeline

	// Material, when set, renders via the material pipeline: the lit path plus
	// normal, metallic-roughness, occlusion and emissive maps. It supersedes
	// Texture, whose job the material's own albedo slot takes over.
	//
	// Works on skinned draws too. A Material takes set 0, which is where the
	// plain texture already sat, so joints stay at set 1 and shadow at set 2 --
	// the layout the skinned pipeline has always used.
	Material *Material

	// SortID is the scene's own stable identity for this draw -- the entity id,
	// where the engine builds the list -- and is read by nothing but the sort,
	// as its final tiebreak.
	//
	// Without it the sort has no total order: two draws with the same pipeline
	// variant and the same set-0 resource compare equal, as do two blended
	// draws at the same distance from the eye, and slices.SortFunc is not
	// stable, so which one is recorded first is decided by the order the list
	// arrived in -- a Go map walk. Zero is fine for a list nothing sorts
	// (overlays, celestials, MSDF text); those are recorded in the order the
	// caller gave them.
	SortID uint64
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

// IsTranslucent reports whether this draw goes through the blended pipeline
// rather than the opaque one.
//
// Zero Alpha means opaque, the same convention Roughness uses for "unset", so a
// RenderObject built without thinking about translucency renders exactly as it
// always has. One means opaque too: a game fading something in can run Alpha to
// 1 and get the cheaper path back without special-casing it.
//
// The plain lit path and the skinned path both have blended variants, built
// from their own vertex and fragment stages. Terrain, water and material draws
// do not, and they stay opaque rather than being rerouted -- rerouting would
// silently drop the splat blend, the refraction, or the normal and occlusion
// maps, which is a worse outcome than an object that is not as see-through as
// asked for. A skinned mesh that also carries a Material is excluded for that
// reason: it goes through the skinned *material* pipeline, which has no blended
// twin. See docs/agents/translucency.md.
//
// This is deliberately one function rather than a condition written out in both
// buildDrawList and the recorder: the two have to agree exactly, or a draw gets
// skipped by the opaque loop and skipped again by the blended one and simply
// vanishes.
func (d *RenderObject) IsTranslucent() bool {
	if d.Alpha <= 0 || d.Alpha >= 1 {
		return false
	}
	return d.TerrainMat == nil && d.Water == nil && d.Material == nil
}

// ViewDepth returns the squared distance from eye to this draw's world-space
// bound centre, which is what the blended pass sorts on.
//
// Squared, because a back-to-front ordering does not need the square root and
// the sort calls this on every comparison.
func (d *RenderObject) ViewDepth(eye [3]float32) float32 {
	cx, cy, cz := d.worldCenter()
	dx, dy, dz := cx-eye[0], cy-eye[1], cz-eye[2]
	return dx*dx + dy*dy + dz*dz
}

// SortKey groups draws to minimize state switches in the main pass: pipeline
// variant (skinned / double-sided / material) first, then the resource the draw
// binds at set 0.
//
// The low bits are the resource's creation id, not its descriptor set's
// address. Both group identically -- equal key exactly when the draws bind the
// same thing, so the same runs of draws come out contiguous and the recorder
// does the same number of binds -- but an address is different in every
// process, so it used to decide the order of the groups, and of anything tied
// inside them, by where the driver allocated. See resourceid.go and issue #53.
//
// This is not a total order on its own: draws binding the same resource in the
// same variant share a key. buildDrawList breaks that tie on SortID.
//
// Bit 63 is deliberately left clear. The engine's sortDraws sets it on blended
// draws, so "every opaque draw before every blended one, then by state" is one
// integer compare over a 24-byte key it can sort instead of sorting these
// 224-byte RenderObjects, which measured three and a half times as expensive.
func (d *RenderObject) SortKey() uint64 {
	var key uint64
	if d.Joints != nil {
		key |= 1 << 62
	}
	if d.DoubleSided {
		key |= 1 << 61
	}
	// Material draws use a different pipeline from plain textured ones, so they
	// have to sort apart from them rather than interleave by resource id.
	// Skinned material draws included: bit 62 already separates them from static
	// ones, so this bit only has to separate material from plain within each.
	if d.Material != nil {
		key |= 1 << 60
		key |= uint64(d.Material.id)
		return key
	}
	if d.Texture != nil {
		key |= uint64(d.Texture.id)
	}
	return key
}

// worldCenter returns the draw's mesh bound centre in world space.
//
// It is the one point that stands for a draw: what the blended pass sorts on
// and what the water split classifies. Sharing it means a draw cannot sort as
// if it were somewhere the split does not think it is.
func (d *RenderObject) worldCenter() (cx, cy, cz float32) {
	m := &d.Model
	c := d.Mesh.BoundCenter
	return m[0]*c[0] + m[4]*c[1] + m[8]*c[2] + m[12],
		m[1]*c[0] + m[5]*c[1] + m[9]*c[2] + m[13],
		m[2]*c[0] + m[6]*c[1] + m[10]*c[2] + m[14]
}

// modelScale is the largest axis scale in the draw's model matrix, which is
// what a bounding sphere has to be grown by.
func (d *RenderObject) modelScale() float32 {
	m := &d.Model
	sx := float32(math.Sqrt(float64(m[0]*m[0] + m[1]*m[1] + m[2]*m[2])))
	sy := float32(math.Sqrt(float64(m[4]*m[4] + m[5]*m[5] + m[6]*m[6])))
	sz := float32(math.Sqrt(float64(m[8]*m[8] + m[9]*m[9] + m[10]*m[10])))
	return max(sx, sy, sz)
}

// worldBoundSphere returns the draw's mesh bounding sphere transformed to
// world space. A zero radius means the mesh has no bounds (always draw).
func (d *RenderObject) worldBoundSphere() (cx, cy, cz, r float32) {
	if d.Mesh.BoundRadius <= 0 {
		return 0, 0, 0, 0
	}
	cx, cy, cz = d.worldCenter()
	return cx, cy, cz, d.Mesh.BoundRadius * d.modelScale()
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
			// Give back whatever this call already made rather than leaving it
			// for the caller: recreateSwapchain calls this on every rebuild, not
			// just at startup, and it has nothing to destroy since a failure
			// here means framebuffers is never assigned.
			for _, made := range framebuffers[:i] {
				deviceDriver.DestroyFramebuffer(made, nil)
			}
			return nil, fmt.Errorf("create framebuffer %d: %w", i, err)
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

// Push-constant floats the impostor pass borrows for billboard geometry.
//
// The block is full at 256 bytes, so anything the billboards need has to go in
// a slot grass does not read. These three qualify: grass.frag is diffuse-only,
// so roughness and metallic reach no code, and it never reads tint.w at all.
//
// Which slots are picked matters more than it looks. Every neighbouring slot is
// live lighting data that grass.frag does read, and writing one relights the
// billboards without touching the meshes -- silently, since it is a valid float
// in a valid slot. The billboard size sat in ambient.xy for one commit and the
// far field glowed pale at night while the grass in front of it stayed dark.
// TestImpostorPushSlotsAvoidLighting keeps them clear.
const (
	pcImpostorCell   = 35 // tint.w, otherwise the flat-shading flag
	pcImpostorWidth  = 51 // pointColor.w, otherwise roughness
	pcImpostorHeight = 55 // ambient.w, otherwise metallic
)

// grassImpostorCellStride packs the atlas cell count and the cell index into
// pcImpostorCell, because the billboards need four numbers and the block has
// three slots to spare. Both are small non-negative integers and a float32 is
// exact to 2^24, so nothing is lost; grassImpostorMaxCells is what keeps them
// from colliding.
const (
	grassImpostorCellStride = 64
	grassImpostorMaxCells   = grassImpostorCellStride
)

// tonemapPass is everything the HDR resolve needs, grouped for the same reason
// materialPipelines is: recordCommandBuffer's parameter list is long enough
// already.
type tonemapPass struct {
	renderPass  core1_0.RenderPass
	pipeline    core1_0.Pipeline
	framebuffer core1_0.Framebuffer
	set         core1_0.DescriptorSet
	layout      core1_0.PipelineLayout

	exposure float32
	curve    float32
	white    float32
	bloom    float32

	// ui is the screen-space UI's own HDR layer, or nil when a game did not ask
	// for one -- which is the default, and the path every existing render takes.
	//
	// It rides here rather than as another parameter to recordCommandBuffer for
	// a reason worth stating: that function's parameter list is already pinned
	// by TestRecordCommandBufferStreamIsUnchanged, whose fixture builds this
	// struct. A nil field in a struct the fixture already fills is a layer-off
	// frame with no edit to the test at all, which is what "off is free" has to
	// mean for the check that measures it. See uilayer.go.
	ui *uiLayerPass
}

// materialPipelines groups the material variant's pipelines with their layouts.
//
// Grouped rather than passed loose because recordCommandBuffer's parameter list
// is already long enough that four more positional handles of two repeated types
// would be easy to transpose, and transposing a pipeline with its layout is a
// mistake only the validation layer would catch.
type materialPipelines struct {
	skinned       core1_0.Pipeline
	skinnedLayout core1_0.PipelineLayout

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
	translucentPipeline core1_0.Pipeline,
	translucentDoubleSidedPipeline core1_0.Pipeline,
	skinnedTranslucentPipeline core1_0.Pipeline,
	instancedPipeline core1_0.Pipeline,
	instancedDoubleSidedPipeline core1_0.Pipeline,
	overlayPipeline core1_0.Pipeline,
	skyPipeline core1_0.Pipeline,
	// skyVolumetricPipeline draws in-scattering over the pixels the sky
	// covers. Recorded only when a light asked to scatter; see the draw.
	skyVolumetricPipeline core1_0.Pipeline,
	starsPipeline core1_0.Pipeline,
	celestialPipeline core1_0.Pipeline,
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
	recordCloudsFn func(core1_0.CommandBuffer) error,
	cloudSet core1_0.DescriptorSet,
	bloom bloomPass,
	tonemap tonemapPass,
	particlePipeline core1_0.Pipeline,
	terrainPipeline core1_0.Pipeline,
	mat materialPipelines,
	stats *RenderStats,
	pipelineLayout core1_0.PipelineLayout,
	// skyPipelineLayout is pipelineLayout plus the shadow/light set at set 1.
	// Only the sky draw uses it, and only because sky.frag marches the froxel
	// grid; the two are compatible for set 0 and for push constants, so the
	// draws either side of it are unaffected.
	skyPipelineLayout core1_0.PipelineLayout,
	litPipelineLayout core1_0.PipelineLayout,
	skinnedPipelineLayout core1_0.PipelineLayout,
	terrainPipelineLayout core1_0.PipelineLayout,
	extent core1_0.Extent2D,
	draws []RenderObject,
	overlays []RenderObject,
	celestials []RenderObject,
	uiOverlays []UIRenderObject,
	msdfOverlays []RenderObject,
	lighting SceneLighting,
	split blendSplit,
	fallbackTexture *Texture,
	milkyWayTex *Texture,
	shadow *shadowResources,
	grass *GrassSystem,
	grassLOD GrassLOD,
	impostor *grassImpostor,
	grassImpostorPipeline core1_0.Pipeline,
	particles *ParticleSystem,
	frame int,
	msaaEnabled bool,
	timer *gpuTimer,
	trace *StateTrace,
	scratch *commandScratch,
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

	// Clouds first, at half resolution, into their own target. The sky pass
	// samples it; the render pass's external dependency orders the two.
	timer.begin(deviceDriver, cmdBuf, frame, PassClouds)
	if err := recordCloudsFn(cmdBuf); err != nil {
		return err
	}
	timer.end(deviceDriver, cmdBuf, frame, PassClouds)

	timer.begin(deviceDriver, cmdBuf, frame, PassShadow)
	// ── Sun shadow depth passes (one per cascade) ──
	// Always run each pass to ensure the depth layer is cleared to 1.0
	// (fully lit). When shadows are disabled, we clear but skip drawing geometry.
	shadowCascadeArea := core1_0.Rect2D{
		Offset: core1_0.Offset2D{X: 0, Y: 0},
		Extent: core1_0.Extent2D{Width: ShadowMapSize, Height: ShadowMapSize},
	}
	for cascade := 0; cascade < ShadowCascades; cascade++ {
		err = scratch.beginRenderPass(deviceDriver, cmdBuf, core1_0.SubpassContentsInline, shadow.renderPass,
			shadow.framebuffers[frame][cascade], shadowCascadeArea, core1_0.ClearValueDepthStencil{Depth: 1.0, Stencil: 0})
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
			shadowScissor := shadowCascadeArea

			deviceDriver.CmdBindPipeline(cmdBuf, core1_0.PipelineBindPointGraphics, shadow.pipeline)
			scratch.setViewport(deviceDriver, cmdBuf, shadowViewport)
			scratch.setScissor(deviceDriver, cmdBuf, shadowScissor)
			currentShadowSkinned := false

			for i := range draws {
				d := &draws[i]
				if d.Emissive || d.NoCastShadow || d.Water != nil {
					continue // skip emissive (celestial bodies) and non-shadow-casters (ground)
				}
				if d.Instances != nil {
					continue // cast by recordInstancedShadow, below
				}
				// A translucent object casting a solid shadow is the giveaway
				// that turns a placement preview back into a building. The
				// engine also sets NoCastShadow on these where the draw is
				// built, so this is the guard for a game driving the renderer
				// directly.
				if d.IsTranslucent() {
					continue
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
					scratch.setViewport(deviceDriver, cmdBuf, shadowViewport)
					scratch.setScissor(deviceDriver, cmdBuf, shadowScissor)
					currentShadowSkinned = skinned
				}

				if skinned {
					scratch.bindDescriptorSets(deviceDriver, cmdBuf, core1_0.PipelineBindPointGraphics, shadow.skinnedPipelineLayout, 0, d.Joints.descriptorSets[frame])
				}

				scratch.bindVertexBuffers(deviceDriver, cmdBuf, 0, d.Mesh.vertexBuffer)

				// Push constants: cascadeVP * model as MVP, and model matrix
				lightModel := cascadeVP.Mul4(d.Model)
				copy(scratch.shadowPC[:16], lightModel[:])
				copy(scratch.shadowPC[16:32], d.Model[:])
				activeLayout := shadow.pipelineLayout
				if skinned {
					activeLayout = shadow.skinnedPipelineLayout
				}
				scratch.pushShadowConstants(deviceDriver, cmdBuf, activeLayout)

				stats.addDraw(1, d.Mesh.IndexCount, d.Mesh.VertexCount)
				if d.Mesh.IndexCount > 0 {
					deviceDriver.CmdBindIndexBuffer(cmdBuf, d.Mesh.indexBuffer, 0, d.Mesh.indexType)
					deviceDriver.CmdDrawIndexed(cmdBuf, d.Mesh.IndexCount, 1, 0, 0, 0)
				} else {
					deviceDriver.CmdDraw(cmdBuf, d.Mesh.VertexCount, 1, 0, 0)
				}
			}

			// Instance sets, after the individual casters so the pipeline bind
			// happens once rather than alternating with them.
			var cvp [16]float32
			copy(cvp[:], cascadeVP[:])
			recordInstancedShadow(deviceDriver, stats, cmdBuf, shadow.instancedPipeline,
				shadow.pipelineLayout, shadowViewport, shadowScissor, draws, cvp, cascadeFrustum, scratch)
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
		casters := scratch.cubeCasters[:0]
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
		scratch.cubeCasters = casters

		cubeArea := core1_0.Rect2D{
			Offset: core1_0.Offset2D{X: 0, Y: 0},
			Extent: core1_0.Extent2D{Width: PointShadowMapSize, Height: PointShadowMapSize},
		}
		for face := 0; face < 6; face++ {
			faceVP := ComputeCubeFaceVP(lightPos, lighting.PointRange, face)
			faceFrustum := ExtractFrustum(faceVP)

			err = scratch.beginRenderPass(deviceDriver, cmdBuf, core1_0.SubpassContentsInline, shadow.renderPass,
				shadow.cubeFramebuffers[frame][face], cubeArea, core1_0.ClearValueDepthStencil{Depth: 1.0, Stencil: 0})
			if err != nil {
				return err
			}

			deviceDriver.CmdBindPipeline(cmdBuf, core1_0.PipelineBindPointGraphics, shadow.pipeline)
			scratch.setViewport(deviceDriver, cmdBuf, cubeViewport)
			scratch.setScissor(deviceDriver, cmdBuf, cubeScissor)
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
					scratch.setViewport(deviceDriver, cmdBuf, cubeViewport)
					scratch.setScissor(deviceDriver, cmdBuf, cubeScissor)
					currentCubeSkinned = skinned
				}

				if skinned {
					scratch.bindDescriptorSets(deviceDriver, cmdBuf, core1_0.PipelineBindPointGraphics, shadow.skinnedPipelineLayout, 0, d.Joints.descriptorSets[frame])
				}

				scratch.bindVertexBuffers(deviceDriver, cmdBuf, 0, d.Mesh.vertexBuffer)

				faceMVP := faceVP.Mul4(d.Model)
				copy(scratch.shadowPC[:16], faceMVP[:])
				copy(scratch.shadowPC[16:32], d.Model[:])
				activeLayout := shadow.pipelineLayout
				if skinned {
					activeLayout = shadow.skinnedPipelineLayout
				}
				scratch.pushShadowConstants(deviceDriver, cmdBuf, activeLayout)

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
	mainArea := core1_0.Rect2D{Offset: core1_0.Offset2D{X: 0, Y: 0}, Extent: extent}
	scratch.colorClear = core1_0.ClearValueFloat{lighting.SkyColor[0], lighting.SkyColor[1], lighting.SkyColor[2], lighting.SkyColor[3]}
	mainDepthClear := core1_0.ClearValueDepthStencil{Depth: 0.0, Stencil: 0}
	if msaaEnabled {
		// 3rd clear value for the resolve attachment (LoadOpDontCare, but Vulkan requires the count to match)
		err = scratch.beginRenderPass(deviceDriver, cmdBuf, core1_0.SubpassContentsInline, renderPass, framebuffer, mainArea,
			&scratch.colorClear, mainDepthClear, core1_0.ClearValueFloat{0, 0, 0, 1})
	} else {
		err = scratch.beginRenderPass(deviceDriver, cmdBuf, core1_0.SubpassContentsInline, renderPass, framebuffer, mainArea,
			&scratch.colorClear, mainDepthClear)
	}
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
	scratch.setViewport(deviceDriver, cmdBuf, viewport)
	scratch.setScissor(deviceDriver, cmdBuf, scissor)
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
			scratch.setViewport(deviceDriver, cmdBuf, viewport)
			scratch.setScissor(deviceDriver, cmdBuf, scissor)
			terrainBound = true
		}
		scratch.bindDescriptorSets(deviceDriver, cmdBuf, core1_0.PipelineBindPointGraphics, terrainPipelineLayout, 0, d.TerrainMat.DescriptorSet, shadowDS)
		scratch.bindVertexBuffers(deviceDriver, cmdBuf, 0, d.Mesh.vertexBuffer)

		scratch.resetPC()
		copy(scratch.pc[:16], d.MVP[:])
		copy(scratch.pc[16:32], d.Model[:])
		scratch.pc[32], scratch.pc[33], scratch.pc[34], scratch.pc[35] = d.Color[0], d.Color[1], d.Color[2], 0.0
		packLightingPC(&scratch.pc, lighting)
		roughness := d.Roughness
		if roughness == 0 {
			roughness = 0.5
		}
		scratch.pc[51] = roughness
		scratch.pc[55] = d.Metallic
		scratch.pushConstants(deviceDriver, cmdBuf, terrainPipelineLayout, core1_0.StageVertex|core1_0.StageFragment)

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
		if d.IsTranslucent() {
			continue // drawn blended, after the sky; see recordTranslucent
		}
		if d.Instances != nil {
			continue // one instanced draw, recorded by recordInstanced
		}
		skinned := d.Joints != nil
		material := d.Material != nil

		// Switch pipeline if needed
		doubleSided := d.DoubleSided
		if skinned != currentSkinned || doubleSided != currentDoubleSided || material != currentMaterial {
			switch {
			case skinned && material:
				deviceDriver.CmdBindPipeline(cmdBuf, core1_0.PipelineBindPointGraphics, mat.skinned)
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
			scratch.setViewport(deviceDriver, cmdBuf, viewport)
			scratch.setScissor(deviceDriver, cmdBuf, scissor)
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
		if skinned && material {
			// Skinned material: set 0=material, set 1=joints, set 2=shadow.
			activeLayout = mat.skinnedLayout
			if !bindValid || d.Material != lastMaterial || d.Joints != lastJoints {
				scratch.bindDescriptorSets(deviceDriver, cmdBuf, core1_0.PipelineBindPointGraphics, activeLayout, 0,
					d.Material.DescriptorSet, d.Joints.descriptorSets[frame], shadowDS)
				lastMaterial = d.Material
				lastJoints = d.Joints
				bindValid = true
			}
		} else if material {
			// The material's own descriptor set replaces the plain texture at
			// set 0; the shadow set stays where every lit variant expects it.
			activeLayout = mat.layout
			if doubleSided {
				activeLayout = mat.doubleSidedLayout
			}
			if !bindValid || d.Material != lastMaterial {
				scratch.bindDescriptorSets(deviceDriver, cmdBuf, core1_0.PipelineBindPointGraphics, activeLayout, 0, d.Material.DescriptorSet, shadowDS)
				lastMaterial = d.Material
				bindValid = true
			}
		} else if skinned {
			activeLayout = skinnedPipelineLayout
			if !bindValid || tex != lastTex || d.Joints != lastJoints {
				// Skinned: set 0=tex, set 1=joints, set 2=shadow
				scratch.bindDescriptorSets(deviceDriver, cmdBuf, core1_0.PipelineBindPointGraphics, skinnedPipelineLayout, 0, tex.DescriptorSet, d.Joints.descriptorSets[frame], shadowDS)
				lastTex = tex
				lastJoints = d.Joints
				bindValid = true
			}
		} else if !bindValid || tex != lastTex {
			// Static lit: set 0=tex, set 1=shadow
			scratch.bindDescriptorSets(deviceDriver, cmdBuf, core1_0.PipelineBindPointGraphics, litPipelineLayout, 0, tex.DescriptorSet, shadowDS)
			lastTex = tex
			bindValid = true
		}

		scratch.bindVertexBuffers(deviceDriver, cmdBuf, 0, d.Mesh.vertexBuffer)

		scratch.resetPC()
		copy(scratch.pc[:16], d.MVP[:])
		copy(scratch.pc[16:32], d.Model[:])
		scratch.pc[32] = d.Color[0]
		scratch.pc[33] = d.Color[1]
		scratch.pc[34] = d.Color[2]
		scratch.pc[35] = 0.0
		if d.Emissive {
			scratch.pc[35] = 1.0
		} else if d.DoubleSided {
			scratch.pc[35] = -1.0 // signal flat shading for foliage
		}
		packLightingPC(&scratch.pc, lighting)
		roughness := d.Roughness
		if roughness == 0 {
			roughness = 0.5 // default to semi-rough if unset
		}
		scratch.pc[51] = roughness  // pointColor.w = roughness
		scratch.pc[55] = d.Metallic // ambient.w = metallic
		scratch.pushConstants(deviceDriver, cmdBuf, activeLayout, core1_0.StageVertex|core1_0.StageFragment)

		stats.addDraw(1, d.Mesh.IndexCount, d.Mesh.VertexCount)
		if d.Mesh.IndexCount > 0 {
			deviceDriver.CmdBindIndexBuffer(cmdBuf, d.Mesh.indexBuffer, 0, d.Mesh.indexType)
			deviceDriver.CmdDrawIndexed(cmdBuf, d.Mesh.IndexCount, 1, 0, 0, 0)
		} else {
			deviceDriver.CmdDraw(cmdBuf, d.Mesh.VertexCount, 1, 0, 0)
		}
	}

	// Instance sets, inside the opaque pass so they depth-test against
	// everything else exactly as individually drawn props would.
	recordInstanced(deviceDriver, stats, cmdBuf, instancedPipeline, instancedDoubleSidedPipeline,
		litPipelineLayout, viewport, scissor, draws, lighting, fallbackTexture, shadowDS, scratch)

	timer.end(deviceDriver, cmdBuf, frame, PassOpaque)
	timer.begin(deviceDriver, cmdBuf, frame, PassGrass)
	// grassOrder accumulates the tile draw sequence for the state trace: which
	// variant, which instance-buffer range, and how many of it survived
	// thinning, in the order recorded. Grass is one of the two systems issue
	// #40 was sighted in, and a per-tile instance COUNT that moves by one --
	// which a camera a hair further away does -- changes the silhouette without
	// changing anything the simulation hash would notice.
	grassOrder, grassDraws := NewHash, 0
	// Draw instanced grass variants (two-sided, depth tested, lit)
	if grass != nil && len(grass.Variants) > 0 {
		deviceDriver.CmdBindPipeline(cmdBuf, core1_0.PipelineBindPointGraphics, grassPipeline)
		scratch.setViewport(deviceDriver, cmdBuf, viewport)
		scratch.setScissor(deviceDriver, cmdBuf, scissor)

		// Push constants shared across all variants
		scratch.resetPC()
		copy(scratch.pc[:16], lighting.VP[:])
		// model = identity
		scratch.pc[16] = 1
		scratch.pc[21] = 1
		scratch.pc[26] = 1
		scratch.pc[31] = 1
		// tint = white (vertex colors provide grass color)
		scratch.pc[32] = 1.0
		scratch.pc[33] = 1.0
		scratch.pc[34] = 1.0
		scratch.pc[35] = -1.0 // flat shading for grass (double-sided foliage)
		packLightingPC(&scratch.pc, lighting)
		scratch.pc[39] = lighting.Time // sunDir.w = time for wind animation
		// pointPos.xy: the distance tuning grass.vert culls and fades by.
		scratch.pc[44] = grassLOD.MaxDistance
		scratch.pc[45] = grassLOD.FadeStart
		scratch.pc[51] = 1.0 // roughness = fully matte
		scratch.pc[55] = 0.0 // metallic = non-metal
		scratch.pushConstants(deviceDriver, cmdBuf, litPipelineLayout, core1_0.StageVertex|core1_0.StageFragment)

		// Cull tiles against the camera frustum and the shader's hard cull
		// distance; only visible tiles are drawn (contiguous instance ranges).
		camFrustum := ExtractFrustum(mgl32.Mat4(lighting.VP))
		camX, camY, camZ := lighting.CameraPos[0], lighting.CameraPos[1], lighting.CameraPos[2]

		var lastFloraTex *Texture
		// Far tiles are collected here and drawn after every variant's meshes,
		// so the impostor pipeline is bound once rather than per variant.
		impostorTiles := grass.impostorVariants[:0]
		impostorScratch := grass.impostorScratch[:0]
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
				scratch.bindDescriptorSets(deviceDriver, cmdBuf, core1_0.PipelineBindPointGraphics, litPipelineLayout, 0, tex.DescriptorSet, shadowDS)
				lastFloraTex = tex
			}

			scratch.bindVertexBuffers(deviceDriver, cmdBuf, 0, v.Mesh.vertexBuffer, v.InstanceBuffer)
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
				maxDist := grassLOD.MaxDistance + tile.Radius
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

			// Tiles are sorted near to far, so everything the impostor path
			// takes is a suffix. Splitting once beats testing per tile and keeps
			// the mesh draws contiguous.
			meshTiles := visible
			if grassLOD.ImpostorDistance > 0 && impostor != nil {
				cut := grassLOD.ImpostorDistance * grassLOD.ImpostorDistance
				meshTiles, impostorScratch, impostorTiles = splitImpostorTiles(visible, cut, impostorScratch, impostorTiles, i)
			}

			for _, vt := range meshTiles {
				tile := vt.tile

				// Thin distant tiles. Instances are shuffled within a tile at
				// build time, so a prefix is a uniform sample of it and simply
				// drawing fewer of them removes blades evenly rather than
				// clearing one side.
				count := tile.Count
				if keep := grassLOD.keepFraction(float32(math.Sqrt(float64(vt.dist2)))); keep < 1 {
					count = int(float32(count) * keep)
					if count < 1 {
						count = 1
					}
				}

				stats.GrassTilesDrawn++
				stats.addDraw(count, v.Mesh.IndexCount, v.Mesh.VertexCount)
				if trace != nil {
					grassOrder = grassOrder.Int(i).Int(tile.FirstInstance).Int(count)
					grassDraws++
				}
				deviceDriver.CmdDrawIndexed(cmdBuf, v.Mesh.IndexCount, count, 0, 0, uint32(tile.FirstInstance))
			}
		}

		// Billboards for everything past the impostor distance.
		//
		// One pipeline bind for all variants, after every mesh draw, because
		// switching back and forth per variant would cost more than the tiles
		// save. Order within grass does not matter: it depth-tests and writes
		// depth, so the nearest blade wins wherever two overlap.
		if len(impostorTiles) > 0 {
			deviceDriver.CmdBindPipeline(cmdBuf, core1_0.PipelineBindPointGraphics, grassImpostorPipeline)
			scratch.setViewport(deviceDriver, cmdBuf, viewport)
			scratch.setScissor(deviceDriver, cmdBuf, scissor)
			// The atlas at set 0 in place of the flora texture; grass.frag reads
			// whichever is bound and does not care which.
			scratch.bindDescriptorSets(deviceDriver, cmdBuf, core1_0.PipelineBindPointGraphics, litPipelineLayout, 0, impostor.set, shadowDS)

			// Billboard geometry only. The lighting slots stay exactly as the
			// mesh draws left them, which is what makes the two agree.
			scratch.pc[pcImpostorWidth] = impostor.worldWidth
			scratch.pc[pcImpostorHeight] = impostor.worldHeight

			for _, vt := range impostorTiles {
				v := &grass.Variants[vt.variant]
				scratch.bindVertexBuffers(deviceDriver, cmdBuf, 1, v.InstanceBuffer)

				scratch.pc[pcImpostorCell] = float32(impostor.cells*grassImpostorCellStride + vt.variant)
				scratch.pushConstants(deviceDriver, cmdBuf, litPipelineLayout, core1_0.StageVertex|core1_0.StageFragment)

				for _, t := range impostorScratch[vt.start:vt.end] {
					tile := t.tile
					count := tile.Count
					if keep := grassLOD.keepFraction(float32(math.Sqrt(float64(t.dist2)))); keep < 1 {
						count = int(float32(count) * keep)
						if count < 1 {
							count = 1
						}
					}
					stats.GrassTilesDrawn++
					// Six vertices, two triangles, against a mesh's few hundred.
					stats.addDraw(count, 6, 6)
					if trace != nil {
						grassOrder = grassOrder.Int(vt.variant).Int(tile.FirstInstance).Int(count).Byte('i')
						grassDraws++
					}
					deviceDriver.CmdDraw(cmdBuf, 6, count, 0, uint32(tile.FirstInstance))
				}
			}
		}
		grass.visibleScratch = visible
		grass.impostorScratch = impostorScratch
		grass.impostorVariants = impostorTiles
	}
	trace.CountHash("grass", grassDraws, grassOrder)

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
		scratch.setViewport(deviceDriver, cmdBuf, viewport)
		scratch.setScissor(deviceDriver, cmdBuf, scissor)
		// The half-resolution cloud target, which the sky composites over its
		// dome. It is written earlier in this same command buffer.
		scratch.bindDescriptorSets(deviceDriver, cmdBuf, core1_0.PipelineBindPointGraphics, pipelineLayout, 0, cloudSet)

		scratch.resetPC()
		copy(scratch.pc[:16], lighting.InvVP[:])
		scratch.pc[16] = lighting.CameraPos[0]
		scratch.pc[17] = lighting.CameraPos[1]
		scratch.pc[18] = lighting.CameraPos[2]
		scratch.pc[32] = lighting.Time
		scratch.pc[33] = lighting.NightFactor
		scratch.pc[34] = float32(lighting.CloudSteps)
		scratch.pc[36] = lighting.SunDir[0]
		scratch.pc[37] = lighting.SunDir[1]
		scratch.pc[38] = lighting.SunDir[2]
		scratch.pc[40] = lighting.SunColor[0]
		scratch.pc[41] = lighting.SunColor[1]
		scratch.pc[42] = lighting.SunColor[2]
		scratch.pc[43] = lighting.SunElevation
		// fog.zw, at the same offsets every other shader reads it from: the real
		// sun's horizontal direction. sky.frag declares the intervening cameraPos
		// and fog members solely to land on these offsets, so that there is one
		// convention rather than a per-shader packing to get wrong.
		scratch.pc[62] = lighting.RealSunDir[0]
		scratch.pc[63] = lighting.RealSunDir[2]
		scratch.pushConstants(deviceDriver, cmdBuf, pipelineLayout, core1_0.StageVertex|core1_0.StageFragment)
		deviceDriver.CmdDraw(cmdBuf, 3, 1, 0, 0)
	}

	// In-scattering from the local lights over the pixels nothing else
	// covered: the air in front of the dome, which is where a beam aimed at
	// the night sky lives. Same fullscreen triangle, same far-plane depth
	// test, blended additively over whatever the sky left there.
	//
	// Recorded only when a light actually asked to scatter, which is the
	// whole of what this feature costs a scene that does not use it: no
	// pipeline bind, no draw, nothing. That is also why it is a draw rather
	// than four lines in sky.frag -- see shaders/skyvolumetric.frag for the
	// three pixels the other arrangement moved.
	//
	// Not conditional on lighting.DrawSky. A game with no dome still has air,
	// and the depth test is what decides which pixels this covers, not
	// whether something was drawn on them first.
	if lighting.LightFlags&LightFlagVolumetric != 0 {
		deviceDriver.CmdBindPipeline(cmdBuf, core1_0.PipelineBindPointGraphics, skyVolumetricPipeline)
		scratch.setViewport(deviceDriver, cmdBuf, viewport)
		scratch.setScissor(deviceDriver, cmdBuf, scissor)
		// Set 0 is bound only because the layout has it; this shader reads
		// nothing from it. Set 1 is the shadow/light set, for the per-frame
		// block at binding 0 and the three light storage buffers at 3-5.
		scratch.bindDescriptorSets(deviceDriver, cmdBuf, core1_0.PipelineBindPointGraphics, skyPipelineLayout, 0, cloudSet, shadowDS)

		scratch.resetPC()
		copy(scratch.pc[:16], lighting.InvVP[:])
		// cameraPos and fog, at the offsets every lit shader reads them from.
		// The eye and the fog are the whole of what the march needs from the
		// push block, and the fog IS the medium it scatters off.
		scratch.pc[56] = lighting.CameraPos[0]
		scratch.pc[57] = lighting.CameraPos[1]
		scratch.pc[58] = lighting.CameraPos[2]
		scratch.pc[59] = lighting.FogDensity
		scratch.pc[60] = lighting.FogHeight
		scratch.pc[61] = lighting.FogBaseHeight
		scratch.pushConstants(deviceDriver, cmdBuf, skyPipelineLayout, core1_0.StageVertex|core1_0.StageFragment)
		deviceDriver.CmdDraw(cmdBuf, 3, 1, 0, 0)
	}

	// Draw procedural stars (additive blend, no vertex buffer)
	if lighting.DrawStars {
		deviceDriver.CmdBindPipeline(cmdBuf, core1_0.PipelineBindPointGraphics, starsPipeline)
		scratch.setViewport(deviceDriver, cmdBuf, viewport)
		scratch.setScissor(deviceDriver, cmdBuf, scissor)
		// The star pass has always bound a descriptor here without sampling it.
		// When a panorama is supplied it goes in that slot, and sunDir.x -- which
		// this pass does not otherwise use -- says whether it is real.
		starTex := fallbackTexture
		var haveBand float32
		if milkyWayTex != nil {
			starTex = milkyWayTex
			haveBand = 1
		}
		scratch.bindDescriptorSets(deviceDriver, cmdBuf, core1_0.PipelineBindPointGraphics, pipelineLayout, 0, starTex.DescriptorSet)

		scratch.resetPC()
		copy(scratch.pc[:16], lighting.InvVP[:])
		scratch.pc[16] = lighting.CameraPos[0]
		scratch.pc[17] = lighting.CameraPos[1]
		scratch.pc[18] = lighting.CameraPos[2]
		scratch.pc[32] = lighting.Time
		scratch.pc[33] = lighting.NightFactor
		scratch.pc[34] = lighting.MilkyWay
		scratch.pc[35] = lighting.StarDensity
		scratch.pc[36] = haveBand
		scratch.pushConstants(deviceDriver, cmdBuf, pipelineLayout, core1_0.StageVertex|core1_0.StageFragment)
		deviceDriver.CmdDraw(cmdBuf, 3, 1, 0, 0)
	}

	// The sun and moon, after the sky and its clouds rather than with the
	// opaque geometry. See createCelestialPipeline: drawn earlier they wrote
	// depth, which rejected the sky pass on those pixels and made it impossible
	// for a cloud to pass in front of the sun.
	if len(celestials) > 0 {
		deviceDriver.CmdBindPipeline(cmdBuf, core1_0.PipelineBindPointGraphics, celestialPipeline)
		scratch.setViewport(deviceDriver, cmdBuf, viewport)
		scratch.setScissor(deviceDriver, cmdBuf, scissor)
		scratch.bindDescriptorSets(deviceDriver, cmdBuf, core1_0.PipelineBindPointGraphics, pipelineLayout, 0, fallbackTexture.DescriptorSet)

		for i := range celestials {
			d := &celestials[i]
			if d.Mesh == nil || (d.Mesh.IndexCount == 0 && d.Mesh.VertexCount == 0) {
				continue
			}
			scratch.bindVertexBuffers(deviceDriver, cmdBuf, 0, d.Mesh.vertexBuffer)

			scratch.resetPC()
			copy(scratch.pc[:16], d.MVP[:])
			scratch.pc[16] = 1
			scratch.pc[21] = 1
			scratch.pc[26] = 1
			scratch.pc[31] = 1
			scratch.pc[32] = d.Color[0]
			scratch.pc[33] = d.Color[1]
			scratch.pc[34] = d.Color[2]
			scratch.pc[35] = 1.0
			scratch.pushConstants(deviceDriver, cmdBuf, pipelineLayout, core1_0.StageVertex|core1_0.StageFragment)

			stats.addDraw(1, d.Mesh.IndexCount, d.Mesh.VertexCount)
			if d.Mesh.IndexCount > 0 {
				deviceDriver.CmdBindIndexBuffer(cmdBuf, d.Mesh.indexBuffer, 0, d.Mesh.indexType)
				deviceDriver.CmdDrawIndexed(cmdBuf, d.Mesh.IndexCount, 1, 0, 0, 0)
			} else {
				deviceDriver.CmdDraw(cmdBuf, d.Mesh.VertexCount, 1, 0, 0)
			}
		}
	}

	timer.end(deviceDriver, cmdBuf, frame, PassSky)

	timer.begin(deviceDriver, cmdBuf, frame, PassTranslucent)
	recordTranslucent(deviceDriver, stats, cmdBuf, translucentPipeline, translucentDoubleSidedPipeline,
		skinnedTranslucentPipeline, litPipelineLayout, skinnedPipelineLayout, viewport, scissor,
		draws, lighting, fallbackTexture, shadowDS, frame, split, false, scratch)
	timer.end(deviceDriver, cmdBuf, frame, PassTranslucent)

	timer.begin(deviceDriver, cmdBuf, frame, PassParticles)
	// Billboard particles, the instances behind the water. With no water in the
	// frame that is all of them and this is the only particle draw, exactly as
	// it was before the split existed.
	recordParticles(deviceDriver, cmdBuf, particlePipeline, pipelineLayout, viewport, scissor,
		particles, lighting, fallbackTexture, frame, 0, particleBehind(particles), scratch)

	timer.end(deviceDriver, cmdBuf, frame, PassParticles)
	timer.begin(deviceDriver, cmdBuf, frame, PassOverlay)
	// World-space overlays (bars, markers): no depth test, no culling, and
	// always the topmost thing in the scene. The screen-space channels are
	// composited after the tonemap instead; see recordUIComposite.
	//
	// With water in the frame they move to the water pass. "On top" has to mean
	// on top of the water as well, and an overlay recorded here would also be
	// inside the refraction copy — a marker smeared through the waves and then
	// painted over is two wrongs rather than one.
	if !split.active() {
		recordOverlays(deviceDriver, stats, cmdBuf, overlayPipeline, pipelineLayout,
			viewport, scissor, overlays, fallbackTexture, scratch)
	}

	timer.end(deviceDriver, cmdBuf, frame, PassOverlay)

	// The end of the pass is where the MSAA colour resolves, and that is real GPU
	// time that belongs to no draw. It gets a bracket of its own so that it is
	// neither charged to whichever pass happens to close last nor left as an
	// unexplained gap between the passes' sum and the frame total.
	timer.begin(deviceDriver, cmdBuf, frame, PassSceneResolve)
	deviceDriver.CmdEndRenderPass(cmdBuf)
	timer.end(deviceDriver, cmdBuf, frame, PassSceneResolve)

	// Water needs the finished scene as a texture, so it runs in a second pass.
	//
	// The LightShafts arm is why a shafts-and-no-water frame enters it too:
	// godray.frag samples the same copy the water refracts through, so the copy
	// is the whole reason the pass exists for it. LightShafts arrives already
	// faded (see SceneLighting), so a frame that could not draw a shaft pixel
	// reads zero here and pays for neither the copy nor the pass.

	timer.begin(deviceDriver, cmdBuf, frame, PassWater)
	if sceneColor != nil && (hasWater(draws) || lighting.LightShafts > 0) {
		if err := recordWaterPass(deviceDriver, stats, cmdBuf, waterRenderPass, waterFramebuffer,
			waterPipeline, godRayPipeline, pipelineLayout, litPipelineLayout, extent, draws, lighting,
			sceneColor, sceneImage, shadowDS, msaaEnabled, overWater{
				translucent:        translucentPipeline,
				translucentDouble:  translucentDoubleSidedPipeline,
				skinnedTranslucent: skinnedTranslucentPipeline,
				particlePipeline:   particlePipeline,
				overlayPipeline:    overlayPipeline,
				skinnedLayout:      skinnedPipelineLayout,
				overlays:           overlays,
				particles:          particles,
				fallback:           fallbackTexture,
				split:              split,
				frame:              frame,
			}, timer, scratch); err != nil {
			return err
		}
	} else {
		// Every pass has to write both of its timestamps every frame. A query
		// that is reset and never written is not "not ready", it makes the
		// whole frame's readback come back NotReady and the timings vanish.
		timer.end(deviceDriver, cmdBuf, frame, PassWater)
		timer.begin(deviceDriver, cmdBuf, frame, PassShafts)
		timer.end(deviceDriver, cmdBuf, frame, PassShafts)
		timer.begin(deviceDriver, cmdBuf, frame, PassOverWater)
		timer.end(deviceDriver, cmdBuf, frame, PassOverWater)
		timer.begin(deviceDriver, cmdBuf, frame, PassWaterResolve)
		timer.end(deviceDriver, cmdBuf, frame, PassWaterResolve)
	}

	timer.begin(deviceDriver, cmdBuf, frame, PassBloom)
	if err := recordBloom(deviceDriver, cmdBuf, bloom, scratch); err != nil {
		return err
	}
	timer.end(deviceDriver, cmdBuf, frame, PassBloom)

	// The screen-space UI's own HDR layer and the glow chain over it, both of
	// which only exist when a game asked for them. They are recorded here
	// because they have to be outside the tonemap render pass -- a render pass
	// cannot begin inside another -- and because the composite that reads them
	// is the last thing in the frame.
	//
	// A frame with no UI at all skips both AND skips the composite, so it
	// presents exactly what a layer-off frame presents: a layer that was never
	// written must not be composited over the scene.
	//
	// Both brackets are written unconditionally; see the water arm above for
	// what a query that is reset and never written costs.
	useUILayer := tonemap.ui != nil && (len(uiOverlays) > 0 || len(msdfOverlays) > 0)
	timer.begin(deviceDriver, cmdBuf, frame, PassUILayer)
	if useUILayer {
		if err := recordUILayer(deviceDriver, stats, cmdBuf, tonemap.ui, extent,
			uiOverlays, msdfOverlays, fallbackTexture, scratch); err != nil {
			return err
		}
	}
	timer.end(deviceDriver, cmdBuf, frame, PassUILayer)

	timer.begin(deviceDriver, cmdBuf, frame, PassUIGlow)
	if useUILayer {
		if err := recordBloom(deviceDriver, cmdBuf, tonemap.ui.bloom, scratch); err != nil {
			return err
		}
	}
	timer.end(deviceDriver, cmdBuf, frame, PassUIGlow)

	// Screen-space UI is composited inside this pass, after the resolve. The
	// tonemap owns its own timing now that two intervals live in it.
	//
	// With the layer on the composite REPLACES those draws with one fullscreen
	// triangle of the finished layer rather than stacking on them. Drawing both
	// would put the UI on screen twice, once blended into a float layer and
	// once straight onto the swapchain, which reads as the HUD having gained
	// contrast rather than as a double draw.
	if err := recordTonemap(deviceDriver, cmdBuf, tonemap, tonemap.layout, extent, timer, frame,
		func(cmdBuf core1_0.CommandBuffer) {
			if useUILayer {
				recordUIResolve(deviceDriver, cmdBuf, tonemap.ui, extent, scratch)
				return
			}
			recordUIComposite(deviceDriver, stats, cmdBuf, uiPipeline, msdfPipeline,
				pipelineLayout, extent, uiOverlays, msdfOverlays, fallbackTexture, false, scratch)
		}, scratch); err != nil {
		return err
	}

	timer.end(deviceDriver, cmdBuf, frame, frameQuery)

	_, err = deviceDriver.EndCommandBuffer(cmdBuf)
	return err
}

// particleBehind is how many instances belong before the refraction copy.
// Zero for a frame with no particle system, and all of them when the frame has
// no water; see ParticleSystem.splitAtWater.
func particleBehind(particles *ParticleSystem) int {
	if particles == nil {
		return 0
	}
	return particles.behind
}

// recordParticles draws instances [first, first+count) of the frame's billboard
// particles: additive, depth-tested, writing no depth.
//
// One contiguous range rather than the whole buffer, because a frame with water
// draws this twice — the instances behind the surface before the refraction
// copy and the ones in front of it after the water. firstInstance is what makes
// that free: the vertex fetch is offset by it, and particle.vert reads no
// gl_InstanceIndex, so the second draw sees exactly the instances it should.
func recordParticles(
	deviceDriver core1_0.DeviceDriver,
	cmdBuf core1_0.CommandBuffer,
	particlePipeline core1_0.Pipeline,
	pipelineLayout core1_0.PipelineLayout,
	viewport core1_0.Viewport,
	scissor core1_0.Rect2D,
	particles *ParticleSystem,
	lighting SceneLighting,
	fallbackTexture *Texture,
	frame, first, count int,
	scratch *commandScratch,
) {
	if particles == nil || count <= 0 {
		return
	}
	deviceDriver.CmdBindPipeline(cmdBuf, core1_0.PipelineBindPointGraphics, particlePipeline)
	scratch.setViewport(deviceDriver, cmdBuf, viewport)
	scratch.setScissor(deviceDriver, cmdBuf, scissor)
	scratch.bindDescriptorSets(deviceDriver, cmdBuf, core1_0.PipelineBindPointGraphics, pipelineLayout, 0, fallbackTexture.DescriptorSet)

	// Push constants: VP at [0..15], cameraRight packed into model col 0, cameraUp into model col 1
	scratch.resetPC()
	copy(scratch.pc[:16], lighting.VP[:])
	// model column 0 = cameraRight
	scratch.pc[16] = lighting.CameraRight[0]
	scratch.pc[17] = lighting.CameraRight[1]
	scratch.pc[18] = lighting.CameraRight[2]
	// model column 1 = cameraUp
	scratch.pc[20] = lighting.CameraUp[0]
	scratch.pc[21] = lighting.CameraUp[1]
	scratch.pc[22] = lighting.CameraUp[2]
	scratch.pushConstants(deviceDriver, cmdBuf, pipelineLayout, core1_0.StageVertex|core1_0.StageFragment)

	scratch.bindVertexBuffers(deviceDriver, cmdBuf, 0, particles.QuadMesh.vertexBuffer, particles.InstanceBuffers[frame])
	deviceDriver.CmdBindIndexBuffer(cmdBuf, particles.QuadMesh.indexBuffer, 0, particles.QuadMesh.indexType)
	deviceDriver.CmdDrawIndexed(cmdBuf, particles.QuadMesh.IndexCount, count, 0, 0, uint32(first))
}

// recordOverlays draws the world-space unlit overlays: no depth test, no
// culling, one draw each.
//
// It sets the viewport and scissor itself rather than inheriting whatever the
// last pipeline bind left behind. That was safe while this only ever ran at the
// end of the scene pass behind half a dozen other draws; it runs in the water
// pass now as well, and a group that depends on something else having been
// drawn first is a trap rather than a saving.
func recordOverlays(
	deviceDriver core1_0.DeviceDriver,
	stats *RenderStats,
	cmdBuf core1_0.CommandBuffer,
	overlayPipeline core1_0.Pipeline,
	pipelineLayout core1_0.PipelineLayout,
	viewport core1_0.Viewport,
	scissor core1_0.Rect2D,
	overlays []RenderObject,
	fallbackTexture *Texture,
	scratch *commandScratch,
) {
	if len(overlays) == 0 {
		return
	}
	deviceDriver.CmdBindPipeline(cmdBuf, core1_0.PipelineBindPointGraphics, overlayPipeline)
	scratch.setViewport(deviceDriver, cmdBuf, viewport)
	scratch.setScissor(deviceDriver, cmdBuf, scissor)
	scratch.bindDescriptorSets(deviceDriver, cmdBuf, core1_0.PipelineBindPointGraphics, pipelineLayout, 0, fallbackTexture.DescriptorSet)

	for i := range overlays {
		d := &overlays[i]
		if d.Mesh.IndexCount == 0 && d.Mesh.VertexCount == 0 {
			continue
		}
		scratch.bindVertexBuffers(deviceDriver, cmdBuf, 0, d.Mesh.vertexBuffer)

		// Overlay: MVP + identity model + tint, lighting zeroed
		scratch.resetPC()
		copy(scratch.pc[:16], d.MVP[:])
		// model = identity
		scratch.pc[16] = 1
		scratch.pc[21] = 1
		scratch.pc[26] = 1
		scratch.pc[31] = 1
		scratch.pc[32] = d.Color[0]
		scratch.pc[33] = d.Color[1]
		scratch.pc[34] = d.Color[2]
		scratch.pc[35] = 1.0
		// lighting fields stay zero — overlay shader ignores them
		scratch.pushConstants(deviceDriver, cmdBuf, pipelineLayout, core1_0.StageVertex|core1_0.StageFragment)

		stats.addDraw(1, d.Mesh.IndexCount, d.Mesh.VertexCount)
		if d.Mesh.IndexCount > 0 {
			deviceDriver.CmdBindIndexBuffer(cmdBuf, d.Mesh.indexBuffer, 0, d.Mesh.indexType)
			deviceDriver.CmdDrawIndexed(cmdBuf, d.Mesh.IndexCount, 1, 0, 0, 0)
		} else {
			deviceDriver.CmdDraw(cmdBuf, d.Mesh.VertexCount, 1, 0, 0)
		}
	}
}

// overWater is everything the water pass needs to draw after the surface.
//
// Grouped rather than passed loose for the same reason materialPipelines is:
// recordWaterPass's parameter list is long enough already, and five more
// positional handles of two repeated types would be easy to transpose in a way
// only the validation layer would catch.
//
// Every pipeline here was created against the SCENE render pass and is bound
// inside the water one. That is legal because the two passes are render pass
// compatible: same attachment count, formats, sample counts, subpass references
// and subpass dependencies, differing only in load and store ops and in image
// layouts, which compatibility explicitly allows. The dependency is the part
// that is easy to get wrong and the layer does catch — see sceneEntryDependency,
// which exists so the two passes cannot drift apart. Building a second set of
// identical pipelines would double their startup cost to say the same thing.
type overWater struct {
	translucent        core1_0.Pipeline
	translucentDouble  core1_0.Pipeline
	skinnedTranslucent core1_0.Pipeline
	particlePipeline   core1_0.Pipeline
	overlayPipeline    core1_0.Pipeline

	skinnedLayout core1_0.PipelineLayout

	overlays  []RenderObject
	particles *ParticleSystem
	fallback  *Texture
	split     blendSplit
	frame     int
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

// recordWaterPass copies the opaque scene into a sampled image, draws the water
// surfaces against it in a second render pass, and then draws the blended
// geometry that belongs in front of the water.
//
// The copy is the only way a fragment shader can read what is already on
// screen; the alternative, an input attachment, can only read the pixel being
// written, and refraction is precisely a read of a *different* pixel.
//
// The blended group after it is issue #45. Water does not write depth, so those
// draws depth-test against the opaque scene exactly as they did in the scene
// pass — a flame behind a hill is still behind the hill — and they composite
// over the surface rather than under it. What decides which draws come here and
// which stay before the copy is blendSplit; see waterorder.go.
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
	ow overWater,
	timer *gpuTimer,
	scratch *commandScratch,
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
	scratch.pipelineBarrier(deviceDriver, cmdBuf,
		core1_0.PipelineStageColorAttachmentOutput, core1_0.PipelineStageTransfer,
		core1_0.ImageMemoryBarrier{
			OldLayout:           core1_0.ImageLayoutShaderReadOnlyOptimal,
			NewLayout:           core1_0.ImageLayoutTransferSrcOptimal,
			SrcQueueFamilyIndex: -1, DstQueueFamilyIndex: -1,
			Image:            sceneImage,
			SubresourceRange: colorRange,
			SrcAccessMask:    core1_0.AccessColorAttachmentWrite,
			DstAccessMask:    core1_0.AccessTransferRead,
		},
		core1_0.ImageMemoryBarrier{
			// Previous contents are irrelevant; the whole image is rewritten.
			OldLayout:           core1_0.ImageLayoutUndefined,
			NewLayout:           core1_0.ImageLayoutTransferDstOptimal,
			SrcQueueFamilyIndex: -1, DstQueueFamilyIndex: -1,
			Image:            sceneColor.image,
			SubresourceRange: colorRange,
			SrcAccessMask:    0,
			DstAccessMask:    core1_0.AccessTransferWrite,
		})

	layers := core1_0.ImageSubresourceLayers{
		AspectMask: core1_0.ImageAspectColor,
		LayerCount: 1,
	}
	scratch.copyImage(deviceDriver, cmdBuf,
		sceneImage, core1_0.ImageLayoutTransferSrcOptimal,
		sceneColor.image, core1_0.ImageLayoutTransferDstOptimal,
		core1_0.ImageCopy{
			SrcSubresource: layers,
			DstSubresource: layers,
			Extent:         core1_0.Extent3D{Width: extent.Width, Height: extent.Height, Depth: 1},
		})

	scratch.pipelineBarrier(deviceDriver, cmdBuf,
		core1_0.PipelineStageTransfer, core1_0.PipelineStageFragmentShader,
		core1_0.ImageMemoryBarrier{
			OldLayout:           core1_0.ImageLayoutTransferDstOptimal,
			NewLayout:           core1_0.ImageLayoutShaderReadOnlyOptimal,
			SrcQueueFamilyIndex: -1, DstQueueFamilyIndex: -1,
			Image:            sceneColor.image,
			SubresourceRange: colorRange,
			SrcAccessMask:    core1_0.AccessTransferWrite,
			DstAccessMask:    core1_0.AccessShaderRead,
		})

	// No barrier back for the HDR scene image. With MSAA the water pass
	// declares its resolve target Undefined and rewrites it wholesale; without
	// MSAA it declares TransferSrc, matching the copy. Either way the render
	// pass performs the transition, and a manual barrier to Undefined is not a
	// legal layout transition to ask for.
	_ = msaa

	err := scratch.beginRenderPass(deviceDriver, cmdBuf, core1_0.SubpassContentsInline, waterRenderPass, framebuffer,
		core1_0.Rect2D{Offset: core1_0.Offset2D{X: 0, Y: 0}, Extent: extent})
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
		scratch.setViewport(deviceDriver, cmdBuf, viewport)
		scratch.setScissor(deviceDriver, cmdBuf, scissor)

		// Set 0 is the opaque scene rather than a material texture.
		scratch.bindDescriptorSets(deviceDriver, cmdBuf, core1_0.PipelineBindPointGraphics, litPipelineLayout, 0,
			sceneColor.texture.DescriptorSet, shadowDS)

		scratch.resetPC()
		copy(scratch.pc[:16], d.MVP[:])
		copy(scratch.pc[16:32], d.Model[:])
		// tint carries the wave parameters; the water shader has no use for a
		// colour there, since both of its colours are per-vertex.
		scratch.pc[32] = lighting.Time
		scratch.pc[33] = d.Water.Amplitude
		scratch.pc[34] = d.Water.WaveLength
		scratch.pc[35] = d.Water.RefractStrength
		packLightingPC(&scratch.pc, lighting)
		scratch.pc[51] = d.Water.AbsorptionDepth // pointColor.w
		scratch.pc[55] = 0
		// sunDir.w, which packLightingPC leaves as padding and only the grass
		// pipeline otherwise claims. It is the last free scalar in this block.
		scratch.pc[39] = d.Water.WaveNoise
		scratch.pushConstants(deviceDriver, cmdBuf, litPipelineLayout, core1_0.StageVertex|core1_0.StageFragment)

		scratch.bindVertexBuffers(deviceDriver, cmdBuf, 0, d.Mesh.vertexBuffer)
		stats.addDraw(1, d.Mesh.IndexCount, d.Mesh.VertexCount)
		if d.Mesh.IndexCount > 0 {
			deviceDriver.CmdBindIndexBuffer(cmdBuf, d.Mesh.indexBuffer, 0, d.Mesh.indexType)
			deviceDriver.CmdDrawIndexed(cmdBuf, d.Mesh.IndexCount, 1, 0, 0, 0)
		} else {
			deviceDriver.CmdDraw(cmdBuf, d.Mesh.VertexCount, 1, 0, 0)
		}
	}

	// The surface is on screen; the rest of this pass is the blended geometry
	// in front of it, which is a different thing to attribute. Closing PassWater
	// here rather than around the whole call keeps the two intervals disjoint --
	// nested ones would make the passes sum to more than the frame, which is the
	// signal this instrument uses to say it is broken.
	timer.end(deviceDriver, cmdBuf, ow.frame, PassWater)

	// Light shafts go here: after the water surface, before the draws #45 moved
	// in front of it.
	//
	// The smear is built from the scene COPY, which holds the opaque world and
	// the sky and nothing else. The water surface is part of that world -- haze
	// over a lake reflecting the sunset is the same haze -- so painting over it
	// is right. The blended draws after it are not in the copy at all, so the
	// shafts were computed without them; laying a warm wash over a flame, a
	// particle or a world overlay would add light the effect never accounted
	// for, and it would wash out the overlays that exist to stay legible.
	//
	// Not observable today, and the honest version of that is worth writing
	// down. Moving this block below the PassOverWater bracket renders
	// byte-identical frames for `09-water -plume -ghost -marker -submerged` at
	// every pose where the shafts are strong (yaw 1.771, 1.2 and 0.9 at time
	// 0.72), because no example can put blended geometry inside the lobe: the
	// sun's azimuth is always on the +Z side by construction in
	// DayNight.SunDir, 09-water's blended effects sit toward the lake about 90
	// degrees away, and the horizontal field of view is 72. So this is a
	// decision taken on what the smear is made of rather than on a picture, and
	// TestShaftBracketHoldsTheShaftDrawAndOnlyIt pins it so that the first
	// scene able to see the difference does not get the other order by
	// accident.
	timer.begin(deviceDriver, cmdBuf, ow.frame, PassShafts)
	if lighting.LightShafts > 0 {
		recordLightShafts(deviceDriver, cmdBuf, godRayPipeline, pipelineLayout,
			viewport, scissor, extent, lighting, sceneColor, scratch)
	}
	timer.end(deviceDriver, cmdBuf, ow.frame, PassShafts)

	timer.begin(deviceDriver, cmdBuf, ow.frame, PassOverWater)

	// Same order the scene pass uses for these three: translucent meshes back
	// to front, then additive particles over them, then overlays on top of
	// everything. Only the water now sits underneath instead of on top.
	recordTranslucent(deviceDriver, stats, cmdBuf, ow.translucent, ow.translucentDouble,
		ow.skinnedTranslucent, litPipelineLayout, ow.skinnedLayout, viewport, scissor,
		draws, lighting, ow.fallback, shadowDS, ow.frame, ow.split, true, scratch)

	behind := particleBehind(ow.particles)
	if ow.particles != nil {
		recordParticles(deviceDriver, cmdBuf, ow.particlePipeline, pipelineLayout, viewport, scissor,
			ow.particles, lighting, ow.fallback, ow.frame, behind, ow.particles.InstanceCount-behind, scratch)
	}

	if ow.split.active() {
		recordOverlays(deviceDriver, stats, cmdBuf, ow.overlayPipeline, pipelineLayout,
			viewport, scissor, ow.overlays, ow.fallback, scratch)
	}

	timer.end(deviceDriver, cmdBuf, ow.frame, PassOverWater)

	// The water pass resolves its MSAA colour on the way out, the same as the
	// scene pass, and it gets the same treatment: a bracket of its own. It was
	// briefly charged to PassOverWater instead, on the argument that otherwise
	// 0.021 ms belonged to nobody -- true, and the wrong cure, because it made
	// "overwater" read 0.021 ms on a lake with nothing in front of it, which is
	// the very thing PassOverlay was criticised for. PassWater + PassOverWater +
	// PassWaterResolve is what PassWater alone used to be.
	timer.begin(deviceDriver, cmdBuf, ow.frame, PassWaterResolve)
	deviceDriver.CmdEndRenderPass(cmdBuf)
	timer.end(deviceDriver, cmdBuf, ow.frame, PassWaterResolve)
	return nil
}

// The light shafts' default shape. These were compile-time constants when the
// pass was first drawn, and they are a look rather than a fact: how far light
// reaches through the air, how hard a pillar's streak is, and what counts as
// bright enough to be a source all depend on the sky a game has -- and since
// #12 that sky is the game's to choose. So they are the defaults of
// LightShaftShape now, and the measurements below are what the defaults were
// chosen on, not a claim that they suit every scene.
//
// Both were measured on `09-water -time 0.72 -yaw 1.771 -pitch -0.185
// -pillars` under GLYPHENGINE_FIXED_FRAME_TIME: dusk, the sun peeking over a
// ridge from behind a row of pillars, which is the scene the effect exists for.
// Every reading below is mean sRGB luma ADDED against the same frame rendered
// with -shafts 0, at -shafts 0.35 rather than at the 0.25 default -- these are
// ablations of one constant at a time and a brighter setting separates them
// more clearly. For what the default itself does, see Sky.LightShafts, which
// carries the strength sweep. The four boxes:
//
//	gap      739,440,80x80   terrain lit through the gap beside pillar 4
//	shadow   459,440,80x80   terrain in pillar 4's streak, same radius from
//	                         the sun, so only the occluder separates them
//	pillar4  585,200,50x180  the occluder itself, against the sun
//	ground    50,600,300x100 foreground hillside, metres from the eye
const (
	// shaftLobeRadius is how far from the sun the shafts reach, measured in
	// SCREEN HEIGHTS, and it is the constant that decides whether this reads as
	// light in the air or as a smudge on the lens.
	//
	// It stands in for depth. This pass has none (see godray.frag), so it
	// cannot tell near ground from far sky and left alone it lights both --
	// which is what #50's spike did. Screen distance from the sun is the one
	// thing the pass does know, and to within the small-angle error of a
	// perspective projection that is angular distance from the sun, which is
	// what a forward-scattering lobe falls off with anyway.
	//
	//	radius   gap   shadow  pillar4  ground  frame  pixels changed
	//	 none   +56.3   +14.8    +48.2    +9.2  +25.3    98.1%
	//	 0.62   +27.9    +6.5    +41.4    +0.0   +5.0    42.0%
	//	 0.90   +40.7   +10.0    +44.7    +0.2   +8.9    70.9%
	//	 1.30   +48.1   +12.2    +46.5    +1.8  +13.8    97.0%
	//
	// The "none" row is the lobe turned off -- rule 12's ablation -- and it is
	// the spike exactly: the ground a few metres from the eye gains as much as
	// the terrain the shafts are actually falling on, and almost every pixel in
	// the frame moves. 1.30 has that coming back. 0.62 keeps the shafts inside
	// a tight disc that stops visibly short of where light should reach. 0.90
	// is the widest setting that still leaves the near ground alone.
	shaftLobeRadius = 0.90

	// shaftDecay weights each step of the march, heaviest at the fragment and
	// lightest at the sun, so it sets how far along its own ray a fragment
	// still sees. That is what gives an occluder a streak rather than only a
	// veil: a fragment whose line to the sun crosses a pillar loses the run of
	// samples the pillar covers, and decay decides what a crossing far up the
	// ray still costs it.
	//
	//	decay    gap   shadow  gap/shadow  pillar4
	//	 1.00   +75.1   +25.6     2.94      +84.5
	//	 0.96   +40.7   +10.0     4.06      +44.7
	//	 0.90   +10.1    +1.4     7.41      +10.4
	//
	// Read the ratio, not the absolute numbers: decay moves the brightness by
	// 7x across that range and brightness is what Sky.LightShafts is for.
	// Normalised to the same gap reading, pillar4 veils by 42 to 46 whichever
	// decay is used -- a pillar beside the sun exits into bright sky on its
	// first step either way -- so decay buys streak definition and nothing
	// else, and it buys it by shortening the reach. At 0.90 the whole effect
	// collapses into a glow around the disc; at 1.00 it is a uniform wash with
	// the streaks half washed out. 0.96 leaves the last sample at 0.14 of the
	// first, which is far enough to still have the sun in it.
	shaftDecay = 0.96

	// shaftThresholdLow and shaftThresholdHigh are the linear-luminance window
	// a pixel has to clear to count as a source; godray.frag's bright() is a
	// smoothstep across it and carries the measurements the window was placed
	// by. They are the two numbers most tied to Earth's sky: a palette whose
	// plain daytime sky clears the lower edge smears the whole dome into
	// itself, and one whose sunset never reaches it draws nothing at all.
	shaftThresholdLow  = 0.62
	shaftThresholdHigh = 0.88
)

// LightShaftShape is how the light shafts look, as opposed to how strong they
// are, which is SceneLighting.LightShafts.
//
// Every field's zero value means "the default", separately, so a game changes
// the one it cares about and keeps the rest: a Radius of 0 is a lobe with
// nothing in it and a Decay of 0 is a march of one sample, so neither zero was
// a setting anyone could have wanted.
type LightShaftShape struct {
	// Radius is how far from the sun the shafts reach, in screen heights. See
	// shaftLobeRadius for what it trades: wider reaches further and starts
	// lighting ground a few metres from the eye, because this pass has no
	// depth to tell the two apart.
	Radius float32

	// Decay is the weight each step of the march toward the sun keeps from the
	// one before it, in (0, 1]. Lower gives an occluder a harder streak and the
	// shafts less reach; 1 is a uniform wash. See shaftDecay.
	Decay float32

	// Threshold is the linear-luminance window a pixel has to clear to count as
	// a light source, as {low, high}: nothing below low contributes, everything
	// above high contributes fully. It is what makes terrain an occluder. A sky
	// palette much brighter or dimmer than the default wants this moved with
	// it. {0, 0} is the default; for a window that really starts at zero, give
	// high a value.
	Threshold [2]float32
}

// DefaultLightShaftShape is the shape every scene had before it was tunable.
func DefaultLightShaftShape() LightShaftShape {
	return LightShaftShape{
		Radius:    shaftLobeRadius,
		Decay:     shaftDecay,
		Threshold: [2]float32{shaftThresholdLow, shaftThresholdHigh},
	}
}

// resolve returns the shape the pass will draw with: defaults where a field
// was left at zero, and something drawable where it was set to a value that is
// not. It never rejects, the same way the light cone angles are clamped rather
// than refused -- a shape arrives every frame from game code, and a frame is
// the wrong place to find out about a typo by losing the sky.
func (s LightShaftShape) resolve() LightShaftShape {
	d := DefaultLightShaftShape()
	// The comparisons are written so that NaN fails them and takes the default.
	if !(s.Radius > 0) || math.IsInf(float64(s.Radius), 0) {
		s.Radius = d.Radius
	}
	if !(s.Decay > 0) {
		s.Decay = d.Decay
	}
	if s.Decay > 1 {
		// Above one the far end of the march outweighs the near end and the
		// weights grow without bound over 48 steps.
		s.Decay = 1
	}
	lo, hi := s.Threshold[0], s.Threshold[1]
	if (lo == 0 && hi == 0) || !(lo >= 0) || !(hi >= 0) {
		s.Threshold = d.Threshold
	} else if !(hi > lo) {
		// GLSL leaves smoothstep undefined when the edges meet or cross. A hard
		// cut at lo is what was asked for, so give it the narrowest window that
		// is still one.
		s.Threshold[1] = lo + 1e-4
	}
	return s
}

// recordLightShafts draws the screen-space light shafts: one fullscreen
// triangle, additive, sampling the scene copy this pass already made.
//
// No stats.addDraw. RenderStats counts scene geometry so a game can see what
// its own draw list costs, and a post-process triangle is not that; bloom and
// the tonemap do not count themselves either.
//
// The push block is filled by hand rather than through packLightingPC, the way
// recordTonemap fills its own: godray.frag reads four floats and a vec2, and
// none of the lighting the other pipelines share means anything to it. What it
// does read is documented on the struct in shaders/godray.frag.
func recordLightShafts(
	deviceDriver core1_0.DeviceDriver,
	cmdBuf core1_0.CommandBuffer,
	godRayPipeline core1_0.Pipeline,
	pipelineLayout core1_0.PipelineLayout,
	viewport core1_0.Viewport,
	scissor core1_0.Rect2D,
	extent core1_0.Extent2D,
	lighting SceneLighting,
	sceneColor *sceneColorTarget,
	scratch *commandScratch,
) {
	deviceDriver.CmdBindPipeline(cmdBuf, core1_0.PipelineBindPointGraphics, godRayPipeline)
	scratch.setViewport(deviceDriver, cmdBuf, viewport)
	scratch.setScissor(deviceDriver, cmdBuf, scissor)

	// Set 0 is the scene as it stood before this pass: opaque geometry and the
	// sky, which is where the sun disc lives.
	scratch.bindDescriptorSets(deviceDriver, cmdBuf, core1_0.PipelineBindPointGraphics, pipelineLayout, 0,
		sceneColor.texture.DescriptorSet)

	// The lobe is round in PIXELS, not in UV. A UV step across the frame is
	// aspect times as many pixels as the same step down it, so the x reciprocal
	// carries the aspect ratio and the shafts stay circular on a wide window
	// instead of being stretched with it.
	aspect := float32(1)
	if extent.Height > 0 {
		aspect = float32(extent.Width) / float32(extent.Height)
	}

	shape := lighting.ShaftShape.resolve()

	scratch.resetPC()
	scratch.pc[32] = lighting.SunScreenPos[0]
	scratch.pc[33] = lighting.SunScreenPos[1]
	scratch.pc[34] = lighting.LightShafts
	scratch.pc[35] = shape.Decay
	scratch.pc[36] = aspect / shape.Radius
	scratch.pc[37] = 1 / shape.Radius
	scratch.pc[38] = shape.Threshold[0]
	scratch.pc[39] = shape.Threshold[1]
	scratch.pushConstants(deviceDriver, cmdBuf, pipelineLayout, core1_0.StageVertex|core1_0.StageFragment)

	deviceDriver.CmdDraw(cmdBuf, 3, 1, 0, 0)
}
