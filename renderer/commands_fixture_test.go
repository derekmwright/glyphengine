package renderer

import (
	"github.com/vkngwrapper/core/v3/core1_0"
)

// frame is a GPU-free scene: every field recordCommandBuffer takes, built from
// fakeHandles rather than a real device. It exists so the allocation and
// command-stream tests in this package can drive the actual recording path --
// not a stand-in for it -- without a window, a Vulkan instance, or a GPU.
//
// n controls how many draws the frame carries, scaled independently across
// every pipeline the recorder reaches (see buildFrame), so the same shape at
// a small and a large n is what TestRecordCommandBufferAllocsAreConstant needs
// to tell "zero per draw" apart from "zero because there were only a few".
type frame struct {
	cmdBuf                                                                       core1_0.CommandBuffer
	renderPass, waterRenderPass                                                  core1_0.RenderPass
	framebuffer, waterFramebuffer                                                core1_0.Framebuffer
	pipeline, litDoubleSidedPipeline                                             core1_0.Pipeline
	translucentPipeline, translucentDoubleSidedPipeline, skinnedTranslucentPipel core1_0.Pipeline
	instancedPipeline, instancedDoubleSidedPipeline                              core1_0.Pipeline
	overlayPipeline, skyPipeline, starsPipeline, celestialPipeline               core1_0.Pipeline
	uiPipeline, msdfPipeline, skinnedPipeline                                    core1_0.Pipeline
	grassPipeline, waterPipeline, godRayPipeline                                 core1_0.Pipeline
	particlePipeline, terrainPipeline, grassImpostorPipeline                     core1_0.Pipeline
	cloudSet                                                                     core1_0.DescriptorSet
	sceneColor                                                                   *sceneColorTarget
	sceneImage                                                                   core1_0.Image
	bloom                                                                        bloomPass
	tonemap                                                                      tonemapPass
	mat                                                                          materialPipelines
	stats                                                                        RenderStats
	pipelineLayout, litPipelineLayout, skinnedPipelineLayout, terrainPipeLayout  core1_0.PipelineLayout
	extent                                                                       core1_0.Extent2D
	draws                                                                        []RenderObject
	overlays, celestials, msdfOverlays                                           []RenderObject
	uiOverlays                                                                   []UIRenderObject
	lighting                                                                     SceneLighting
	split                                                                        blendSplit
	fallbackTexture, milkyWayTex                                                 *Texture
	shadow                                                                       *shadowResources
	grass                                                                        *GrassSystem
	grassLOD                                                                     GrassLOD
	impostor                                                                     *grassImpostor
	particles                                                                    *ParticleSystem
	timer                                                                        *gpuTimer
	scratch                                                                      commandScratch
}

// identityMat is a 4x4 identity with a per-draw wobble folded into the
// translation column, so no two draws in the fixture share an MVP -- which
// matters for the hash test: two draws with identical push-constant bytes
// would hide a scratch mixup that swapped their order.
func identityMat(salt int) [16]float32 {
	var m [16]float32
	m[0], m[5], m[10], m[15] = 1, 1, 1, 1
	m[12] = float32(salt%97) * 0.25
	m[13] = float32((salt/97)%53) * 0.5
	m[14] = -float32(salt % 31)
	return m
}

func fakeMesh(h *fakeHandles, vertexCount, indexCount int, boundRadius float32) *Mesh {
	return &Mesh{
		vertexBuffer: h.buffer(),
		VertexCount:  vertexCount,
		indexBuffer:  h.buffer(),
		IndexCount:   indexCount,
		indexType:    core1_0.IndexTypeUInt16,
		BoundCenter:  [3]float32{0, 1, 0},
		BoundRadius:  boundRadius,
	}
}

func fakeTexture(h *fakeHandles) *Texture { return &Texture{DescriptorSet: h.descSet()} }

func fakeMaterial(h *fakeHandles) *Material { return &Material{DescriptorSet: h.descSet()} }

func fakeTerrainMaterial(h *fakeHandles) *TerrainMaterial {
	return &TerrainMaterial{DescriptorSet: h.descSet()}
}

func fakeJointBuffer(h *fakeHandles) *JointBuffer {
	jb := &JointBuffer{jointCount: 32}
	for i := range jb.descriptorSets {
		jb.descriptorSets[i] = h.descSet()
	}
	return jb
}

func fakeInstanceSet(h *fakeHandles, mesh *Mesh, count int, boundRadius float32) *InstanceSet {
	return &InstanceSet{Mesh: mesh, buffer: h.buffer(), count: count, boundRadius: boundRadius}
}

func fakeShadowResources(h *fakeHandles) *shadowResources {
	s := &shadowResources{
		renderPass:            h.renderPass(),
		pipeline:              h.pipeline(),
		skinnedPipeline:       h.pipeline(),
		instancedPipeline:     h.pipeline(),
		pipelineLayout:        h.layout(),
		skinnedPipelineLayout: h.layout(),
	}
	for f := 0; f < maxFramesInFlight; f++ {
		s.descriptorSets[f] = h.descSet()
		for c := 0; c < ShadowCascades; c++ {
			s.framebuffers[f][c] = h.framebuffer()
		}
		for face := 0; face < 6; face++ {
			s.cubeFramebuffers[f][face] = h.framebuffer()
		}
	}
	return s
}

// buildFrame returns a frame with n opaque draws spread round-robin over every
// opaque variant (plain, double-sided, material, skinned, skinned+material,
// emissive), plus terrain, translucent, instanced, shadow-only/no-shadow and
// water draws that scale more gently -- a real scene has a handful of terrain
// patches and water surfaces regardless of how many props are in it, and
// scaling those too would test nothing extra while making the two draw counts
// harder to compare.
func buildFrame(n int) *frame {
	h := &fakeHandles{}

	textures := []*Texture{fakeTexture(h), fakeTexture(h), fakeTexture(h)}
	materials := []*Material{fakeMaterial(h), fakeMaterial(h)}
	joints := []*JointBuffer{fakeJointBuffer(h), fakeJointBuffer(h)}
	mesh := fakeMesh(h, 240, 360, 2)
	skinMesh := fakeMesh(h, 420, 600, 3)

	draws := make([]RenderObject, 0, n+64)
	for i := 0; i < n; i++ {
		m := identityMat(i)
		base := RenderObject{
			Mesh: mesh, MVP: m, Model: m,
			Color:     [3]float32{float32(i%5) / 5, float32(i%3) / 3, float32(i%7) / 7},
			Roughness: 0.2 + float32(i%4)*0.1, Metallic: float32(i % 2),
		}
		switch i % 6 {
		case 0:
			base.Texture = textures[i%len(textures)]
		case 1:
			base.Texture = textures[i%len(textures)]
			base.DoubleSided = true
		case 2:
			base.Material = materials[i%len(materials)]
		case 3:
			base.Mesh = skinMesh
			base.Texture = textures[i%len(textures)]
			base.Joints = joints[i%len(joints)]
		case 4:
			base.Mesh = skinMesh
			base.Material = materials[i%len(materials)]
			base.Joints = joints[i%len(joints)]
		case 5:
			base.Texture = textures[i%len(textures)]
			base.Emissive = true
		}
		draws = append(draws, base)
	}

	terrainMat := []*TerrainMaterial{fakeTerrainMaterial(h), fakeTerrainMaterial(h)}
	terrainMesh := fakeMesh(h, 800, 1200, 60)
	for i := 0; i < max(1, n/8); i++ {
		m := identityMat(i + 1000)
		draws = append(draws, RenderObject{Mesh: terrainMesh, TerrainMat: terrainMat[i%len(terrainMat)], MVP: m, Model: m})
	}

	for i := 0; i < max(2, n/6); i++ {
		m := identityMat(i + 2000)
		if i%2 == 0 {
			draws = append(draws, RenderObject{Mesh: mesh, Texture: textures[i%len(textures)], Alpha: 0.5, MVP: m, Model: m})
		} else {
			draws = append(draws, RenderObject{Mesh: skinMesh, Texture: textures[i%len(textures)], Joints: joints[i%len(joints)], Alpha: 0.35, MVP: m, Model: m})
		}
	}

	instMesh := fakeMesh(h, 60, 90, 1.5)
	for i := 0; i < max(2, n/10); i++ {
		set := fakeInstanceSet(h, instMesh, 50+i, 30)
		draws = append(draws, RenderObject{Mesh: instMesh, Instances: set, Texture: textures[i%len(textures)], DoubleSided: i%2 == 0})
	}

	for i := 0; i < 2; i++ {
		mA := identityMat(i + 4000)
		mB := identityMat(i + 4100)
		draws = append(draws, RenderObject{Mesh: mesh, Texture: textures[0], ShadowOnly: true, MVP: mA, Model: mA})
		draws = append(draws, RenderObject{Mesh: mesh, Texture: textures[0], NoCastShadow: true, MVP: mB, Model: mB})
	}

	waterMesh := fakeMesh(h, 900, 1350, 80)
	for i := 0; i < 2; i++ {
		m := identityMat(i + 5000)
		draws = append(draws, RenderObject{Mesh: waterMesh, MVP: m, Model: m, Water: &WaterParams{
			Amplitude: 0.4, WaveLength: 6, AbsorptionDepth: 3, RefractStrength: 0.02, WaveNoise: 0.1,
		}})
	}

	overlayMesh := fakeMesh(h, 12, 18, 0.5)
	overlays := make([]RenderObject, 0, max(2, n/8))
	for i := 0; i < max(2, n/8); i++ {
		m := identityMat(i + 6000)
		overlays = append(overlays, RenderObject{Mesh: overlayMesh, MVP: m, Color: [3]float32{1, float32(i % 2), 0}})
	}

	celestialMesh := fakeMesh(h, 24, 36, 5)
	celestials := []RenderObject{
		{Mesh: celestialMesh, MVP: identityMat(7001), Color: [3]float32{1, 1, 0.9}},
		{Mesh: celestialMesh, MVP: identityMat(7002), Color: [3]float32{0.8, 0.8, 1}},
	}

	uiMesh := fakeMesh(h, 4, 6, 0)
	uiOverlays := make([]UIRenderObject, 0, max(2, n/8))
	for i := 0; i < max(2, n/8); i++ {
		m := identityMat(i + 8000)
		uiOverlays = append(uiOverlays, UIRenderObject{
			RenderObject: RenderObject{Mesh: uiMesh, Texture: textures[i%len(textures)], MVP: m},
			Opacity:      0.9, TextureMode: i%2 == 0,
		})
	}

	msdfMesh := fakeMesh(h, 4, 6, 0)
	msdfOverlays := make([]RenderObject, 0, max(2, n/8))
	for i := 0; i < max(2, n/8); i++ {
		m := identityMat(i + 9000)
		msdfOverlays = append(msdfOverlays, RenderObject{Mesh: msdfMesh, Texture: textures[i%len(textures)], MVP: m, Color: [3]float32{2.5, 0, 0}})
	}

	grassMesh := fakeMesh(h, 12, 18, 1)
	grassTex := fakeTexture(h)
	grassTiles := make([]GrassTile, 0, max(3, n/5))
	first := 0
	for i := 0; i < max(3, n/5); i++ {
		count := 20 + i%7
		// Spread tiles across, inside and past the impostor cut (20) and the
		// hard cull (grassLOD.MaxDistance, 80), so mesh draws, impostor draws
		// and culled tiles are all exercised by one fixture.
		dist := float32(5 + (i*11)%95)
		grassTiles = append(grassTiles, GrassTile{FirstInstance: first, Count: count, Center: [3]float32{dist, 0, 0}, Radius: 2})
		first += count
	}
	grass := &GrassSystem{
		Texture: grassTex,
		Variants: []GrassVariant{
			{Mesh: grassMesh, Texture: grassTex, InstanceBuffer: h.buffer(), InstanceCount: first, Tiles: grassTiles},
			{Mesh: grassMesh, InstanceBuffer: h.buffer(), InstanceCount: first, Tiles: grassTiles},
		},
	}

	impostor := &grassImpostor{
		set: h.descSet(), cells: 2, worldWidth: 4, worldHeight: 3,
	}

	particles := &ParticleSystem{
		QuadMesh:      fakeMesh(h, 4, 6, 0),
		InstanceCount: max(4, n/3),
		behind:        max(2, n/6),
	}
	for f := range particles.InstanceBuffers {
		particles.InstanceBuffers[f] = h.buffer()
	}

	sceneColor := &sceneColorTarget{
		image: h.image(), texture: fakeTexture(h), extent: core1_0.Extent2D{Width: 640, Height: 360},
	}

	fx := &frame{
		cmdBuf:                         h.commandBuffer(),
		renderPass:                     h.renderPass(),
		waterRenderPass:                h.renderPass(),
		framebuffer:                    h.framebuffer(),
		waterFramebuffer:               h.framebuffer(),
		pipeline:                       h.pipeline(),
		litDoubleSidedPipeline:         h.pipeline(),
		translucentPipeline:            h.pipeline(),
		translucentDoubleSidedPipeline: h.pipeline(),
		skinnedTranslucentPipel:        h.pipeline(),
		instancedPipeline:              h.pipeline(),
		instancedDoubleSidedPipeline:   h.pipeline(),
		overlayPipeline:                h.pipeline(),
		skyPipeline:                    h.pipeline(),
		starsPipeline:                  h.pipeline(),
		celestialPipeline:              h.pipeline(),
		uiPipeline:                     h.pipeline(),
		msdfPipeline:                   h.pipeline(),
		skinnedPipeline:                h.pipeline(),
		grassPipeline:                  h.pipeline(),
		waterPipeline:                  h.pipeline(),
		godRayPipeline:                 h.pipeline(),
		particlePipeline:               h.pipeline(),
		terrainPipeline:                h.pipeline(),
		grassImpostorPipeline:          h.pipeline(),
		cloudSet:                       h.descSet(),
		sceneColor:                     sceneColor,
		sceneImage:                     h.image(),
		bloom:                          bloomPass{enabled: false},
		tonemap: tonemapPass{
			renderPass: h.renderPass(), pipeline: h.pipeline(), framebuffer: h.framebuffer(),
			set: h.descSet(), layout: h.layout(), exposure: 1, curve: 1, white: 4, bloom: 0,
		},
		mat: materialPipelines{
			skinned: h.pipeline(), skinnedLayout: h.layout(),
			pipeline: h.pipeline(), layout: h.layout(),
			doubleSided: h.pipeline(), doubleSidedLayout: h.layout(),
		},
		pipelineLayout:        h.layout(),
		litPipelineLayout:     h.layout(),
		skinnedPipelineLayout: h.layout(),
		terrainPipeLayout:     h.layout(),
		extent:                core1_0.Extent2D{Width: 640, Height: 360},
		draws:                 draws,
		overlays:              overlays,
		celestials:            celestials,
		uiOverlays:            uiOverlays,
		msdfOverlays:          msdfOverlays,
		lighting: SceneLighting{
			SunDir: [3]float32{0.3, 0.8, 0.2}, SunColor: [3]float32{1, 0.95, 0.85},
			PointPos: [3]float32{2, 3, -4}, PointRange: 12, PointColor: [3]float32{1, 0.6, 0.3},
			Ambient: [3]float32{0.1, 0.1, 0.15}, SkyColor: [4]float32{0.4, 0.6, 0.9, 1},
			DrawSky: true, DrawStars: true, ShadowEnabled: true, CloudSteps: 0,
			VP: identityMat(42), CameraRight: [3]float32{1, 0, 0}, CameraUp: [3]float32{0, 1, 0},
			CameraPos: [3]float32{0, 2, 10},
		},
		split:           blendSplit{}, // inactive: everything records in the main pass
		fallbackTexture: fakeTexture(h),
		milkyWayTex:     fakeTexture(h),
		shadow:          fakeShadowResources(h),
		grass:           grass,
		grassLOD:        GrassLOD{ThinNear: 20, ThinFar: 60, ThinMin: 0.3, MaxDistance: 80, FadeStart: 50, ImpostorDistance: 20},
		impostor:        impostor,
		particles:       particles,
		timer:           &gpuTimer{}, // supported=false: every method is a no-op
	}
	return fx
}
