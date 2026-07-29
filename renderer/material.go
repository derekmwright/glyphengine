package renderer

import (
	"fmt"
	"unsafe"

	"github.com/vkngwrapper/core/v3/core1_0"
)

// materialTextureBindings is how many combined image samplers a Material binds
// before its uniform buffer: albedo, normal, metallic-roughness, occlusion.
const materialTextureBindings = 4

// materialUniformSize is the byte size of materialUniform, which must match the
// MaterialBlock declared in lit_material.frag.
const materialUniformSize = 32

// Material binds the texture maps that describe one surface — albedo, normal,
// metallic-roughness, occlusion — plus the flags telling the shader which of
// them are real.
//
// It is the multi-map counterpart to a bare *Texture. A Texture is one combined
// image sampler and nothing else, so a shader reading it can only vary colour
// per pixel while the surface still lights as one perfectly smooth material.
// A Material is four samplers at set 0 bindings 0-3 and a small uniform buffer
// at binding 4, which is why it needs its own descriptor set layout, pipeline
// layout, and fragment shader rather than riding the plain lit path.
//
// TerrainMaterial is the same idea with a fixed four-albedo layout; this
// follows its shape.
type Material struct {
	DescriptorSet core1_0.DescriptorSet

	// Per-material constants: which maps exist, and how strongly to apply
	// them. Written once at creation and never touched again — nothing here
	// varies per frame, which is the whole reason it can live in a uniform
	// buffer the renderer never revisits rather than in push constants, where
	// there is no room left anyway (see pushConstantSize).
	uniform       core1_0.Buffer
	uniformMemory core1_0.DeviceMemory

	destroyed bool
}

// materialUniform is the CPU-side mirror of the shader's MaterialBlock. Both
// members are vec4 because std140 rounds every member up to 16 bytes regardless,
// so packing them tighter would buy nothing and invite a layout mismatch.
type materialUniform struct {
	// Maps holds x,y,z = has normal / metallic-roughness / occlusion, and
	// w = occlusion strength.
	Maps [4]float32
	// Scale holds xy = tangent-space normal scale; a negative y flips green.
	Scale [4]float32
}

// MaterialOptions describes one surface. Every map is optional: an unsupplied
// slot still gets a valid descriptor bound (Vulkan requires it) but the shader
// is told to skip it, so the result is bit-identical to the plain lit path
// rather than merely close to it.
type MaterialOptions struct {
	// Albedo is the base colour map. nil leaves the entity's vertex colour and
	// tint as the only colour, exactly as an untextured lit draw does.
	Albedo *Texture

	// Normal is a tangent-space normal map. It must come from
	// CreateDataTexture or LoadDataTexture: loaded as sRGB colour instead,
	// every normal tilts toward -X-Y and the lighting is quietly wrong with
	// nothing in the log to say so.
	Normal *Texture

	// MetallicRoughness follows the glTF packing — roughness in G, metallic in
	// B. Both multiply the per-object MeshRef.Roughness and .Metallic, so those
	// keep working as factors rather than being overridden.
	MetallicRoughness *Texture

	// Occlusion is baked ambient occlusion in R. It scales the ambient term
	// only; baked occlusion has no business dimming a light that can actually
	// see the surface, and applying it to direct light is what makes
	// AO-mapped geometry read as dirty rather than as shaped.
	Occlusion *Texture

	// NormalScale exaggerates (>1) or flattens (<1) the normal map's
	// tangent-space XY. Zero means one.
	NormalScale float32

	// FlipGreen negates the normal map's green channel, for maps authored in
	// the DirectX convention (green down) rather than glTF's (green up). The
	// symptom of getting it wrong is lighting that looks inverted along one
	// axis but correct along the other.
	FlipGreen bool

	// OcclusionStrength blends the occlusion map toward no occlusion.
	// Zero means one, matching glTF's default. To switch occlusion off,
	// leave Occlusion nil rather than setting this to zero.
	OcclusionStrength float32
}

// materialPipelines bundles the material variant's pipelines for the command
// recorder, so the pairing of each pipeline with its own layout is written down
// once here rather than at the call site.
func (r *Renderer) materialPipelines() materialPipelines {
	return materialPipelines{
		pipeline:          r.materialPipeline,
		layout:            r.materialPipelineLayout,
		doubleSided:       r.materialDoubleSidedPipeline,
		doubleSidedLayout: r.materialDoubleSidedPipelineLayout,
	}
}

// newMaterialUniform packs the per-material constants the shader reads.
//
// Split out of CreateMaterial so it can be checked without a device. The flags
// it computes are the only thing telling the shader which of the four bound maps
// are real, and every slot always has *something* bound — so a wrong flag does
// not fail, it silently samples a neutral 1x1 or, worse, samples a map the
// caller did not supply.
func newMaterialUniform(opts MaterialOptions) materialUniform {
	normalScale := opts.NormalScale
	if normalScale == 0 {
		normalScale = 1
	}
	occlusionStrength := opts.OcclusionStrength
	if occlusionStrength == 0 {
		occlusionStrength = 1
	}

	// Green flips by negating the scale's y rather than by a separate flag, so
	// the shader multiplies once either way.
	greenSign := normalScale
	if opts.FlipGreen {
		greenSign = -normalScale
	}

	return materialUniform{
		Maps: [4]float32{
			boolToFloat(opts.Normal != nil),
			boolToFloat(opts.MetallicRoughness != nil),
			boolToFloat(opts.Occlusion != nil),
			occlusionStrength,
		},
		Scale: [4]float32{normalScale, greenSign, 0, 0},
	}
}

// createMaterialDescriptorSetLayout creates a layout with four combined image
// samplers at set=0, bindings 0-3 (albedo, normal, metallic-roughness,
// occlusion) plus the per-material uniform buffer at binding 4, all fragment
// stage.
func createMaterialDescriptorSetLayout(deviceDriver core1_0.DeviceDriver) (core1_0.DescriptorSetLayout, error) {
	bindings := make([]core1_0.DescriptorSetLayoutBinding, materialTextureBindings+1)
	for i := 0; i < materialTextureBindings; i++ {
		bindings[i] = core1_0.DescriptorSetLayoutBinding{
			Binding:         i,
			DescriptorType:  core1_0.DescriptorTypeCombinedImageSampler,
			DescriptorCount: 1,
			StageFlags:      core1_0.StageFragment,
		}
	}
	bindings[materialTextureBindings] = core1_0.DescriptorSetLayoutBinding{
		Binding:         materialTextureBindings,
		DescriptorType:  core1_0.DescriptorTypeUniformBuffer,
		DescriptorCount: 1,
		StageFlags:      core1_0.StageFragment,
	}

	layout, _, err := deviceDriver.CreateDescriptorSetLayout(nil, core1_0.DescriptorSetLayoutCreateInfo{
		Bindings: bindings,
	})
	if err != nil {
		return core1_0.DescriptorSetLayout{}, fmt.Errorf("create material descriptor set layout: %w", err)
	}
	return layout, nil
}

// CreateMaterial allocates a descriptor set and per-material uniform buffer for
// one surface's maps. The textures stay owned by the caller: several materials
// commonly share one albedo, and they outlive any single material.
//
// Attach the result to an entity with MaterialRef.PBR to route it through the
// material pipeline.
func (r *Renderer) CreateMaterial(opts MaterialOptions) (*Material, error) {
	m := &Material{}

	buf, mem, err := r.createBuffer(materialUniformSize,
		core1_0.BufferUsageUniformBuffer,
		core1_0.MemoryPropertyHostVisible|core1_0.MemoryPropertyHostCoherent,
	)
	if err != nil {
		return nil, fmt.Errorf("create material uniform buffer: %w", err)
	}
	m.uniform, m.uniformMemory = buf, mem

	cleanup := func() {
		r.deviceDriver.FreeMemory(mem, nil)
		r.deviceDriver.DestroyBuffer(buf, nil)
	}

	u := newMaterialUniform(opts)

	ptr, _, err := r.deviceDriver.MapMemory(mem, 0, materialUniformSize, 0)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("map material uniform buffer: %w", err)
	}
	copy(unsafe.Slice((*byte)(ptr), materialUniformSize),
		unsafe.Slice((*byte)(unsafe.Pointer(&u)), materialUniformSize))
	r.deviceDriver.UnmapMemory(mem)

	sets, _, err := r.deviceDriver.AllocateDescriptorSets(core1_0.DescriptorSetAllocateInfo{
		DescriptorPool: r.descriptorPool,
		SetLayouts:     []core1_0.DescriptorSetLayout{r.materialSetLayout},
	})
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("allocate material descriptor set: %w", err)
	}
	m.DescriptorSet = sets[0]

	// Unsupplied slots get a neutral map rather than nothing. The shader skips
	// them by flag so the content never reaches the image, but a descriptor
	// still has to be written: an unwritten binding that a shader statically
	// references is undefined behaviour, and the validation layer says so.
	textures := [materialTextureBindings]*Texture{
		orFallback(opts.Albedo, r.fallbackTexture),
		orFallback(opts.Normal, r.fallbackNormal),
		orFallback(opts.MetallicRoughness, r.fallbackTexture),
		orFallback(opts.Occlusion, r.fallbackTexture),
	}

	writes := make([]core1_0.WriteDescriptorSet, 0, materialTextureBindings+1)
	for i, t := range textures {
		writes = append(writes, core1_0.WriteDescriptorSet{
			DstSet:         m.DescriptorSet,
			DstBinding:     i,
			DescriptorType: core1_0.DescriptorTypeCombinedImageSampler,
			ImageInfo: []core1_0.DescriptorImageInfo{{
				Sampler:     t.sampler,
				ImageView:   t.view,
				ImageLayout: core1_0.ImageLayoutShaderReadOnlyOptimal,
			}},
		})
	}
	writes = append(writes, core1_0.WriteDescriptorSet{
		DstSet:         m.DescriptorSet,
		DstBinding:     materialTextureBindings,
		DescriptorType: core1_0.DescriptorTypeUniformBuffer,
		BufferInfo: []core1_0.DescriptorBufferInfo{{
			Buffer: m.uniform,
			Offset: 0,
			Range:  materialUniformSize,
		}},
	})
	if err := r.deviceDriver.UpdateDescriptorSets(writes, nil); err != nil {
		cleanup()
		return nil, fmt.Errorf("update material descriptor set: %w", err)
	}

	r.materials = append(r.materials, m)
	return m, nil
}

// DestroyMaterial unregisters the material and defers releasing its uniform
// buffer until every in-flight frame has finished referencing it. The
// descriptor set goes back with the pool; the textures belong to the caller.
func (r *Renderer) DestroyMaterial(m *Material) {
	if m == nil || m.destroyed {
		return
	}
	m.destroyed = true

	// Deregister for the same reason DestroyTexture does: the renderer's own
	// shutdown sweep would otherwise free these handles a second time.
	for i, other := range r.materials {
		if other == m {
			r.materials = append(r.materials[:i], r.materials[i+1:]...)
			break
		}
	}

	r.DeferDestroy(func() {
		r.deviceDriver.FreeMemory(m.uniformMemory, nil)
		r.deviceDriver.DestroyBuffer(m.uniform, nil)
	})
}

// createFallbackNormalTexture creates the 1x1 flat tangent-space normal
// (0, 0, 1) that unsupplied normal slots bind.
//
// It has to be a data texture: sRGB decoding would turn 128 into 0.216 rather
// than 0.502, so "flat" would arrive tilted.
func (r *Renderer) createFallbackNormalTexture() (*Texture, error) {
	flat := []byte{128, 128, 255, 255}
	return r.CreateDataTexture(flat, 1, 1)
}

func orFallback(t, fallback *Texture) *Texture {
	if t != nil {
		return t
	}
	return fallback
}

func boolToFloat(b bool) float32 {
	if b {
		return 1
	}
	return 0
}
