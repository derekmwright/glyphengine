package renderer

import (
	"fmt"
	"image"
	"image/draw"
	_ "image/jpeg"
	_ "image/png"
	"io/fs"
	"math"
	"math/bits"
	"unsafe"

	"github.com/vkngwrapper/core/v3/core1_0"
)

// Texture holds a GPU image with sampler and a pre-allocated descriptor set
// for binding as a combined image sampler at set=0, binding=0.
type Texture struct {
	image         core1_0.Image
	memory        core1_0.DeviceMemory
	view          core1_0.ImageView
	sampler       core1_0.Sampler
	DescriptorSet core1_0.DescriptorSet

	destroyed bool
}

// createDescriptorSetLayout creates a layout with a single combined image sampler
// at set=0, binding=0, visible to the fragment stage.
func createDescriptorSetLayout(deviceDriver core1_0.DeviceDriver) (core1_0.DescriptorSetLayout, error) {
	layout, _, err := deviceDriver.CreateDescriptorSetLayout(nil, core1_0.DescriptorSetLayoutCreateInfo{
		Bindings: []core1_0.DescriptorSetLayoutBinding{
			{
				Binding:         0,
				DescriptorType:  core1_0.DescriptorTypeCombinedImageSampler,
				DescriptorCount: 1,
				StageFlags:      core1_0.StageFragment,
			},
		},
	})
	if err != nil {
		return core1_0.DescriptorSetLayout{}, fmt.Errorf("create descriptor set layout: %w", err)
	}
	return layout, nil
}

// TerrainMaterial holds the descriptor set binding a terrain's detail textures
// (grass/path/rock at bindings 0-2) plus its splat weight map (binding 3).
type TerrainMaterial struct {
	DescriptorSet core1_0.DescriptorSet
}

// createTerrainDescriptorSetLayout creates a layout with four combined image
// samplers at set=0, bindings 0-3 (grass, path, rock, splat), fragment stage.
func createTerrainDescriptorSetLayout(deviceDriver core1_0.DeviceDriver) (core1_0.DescriptorSetLayout, error) {
	bindings := make([]core1_0.DescriptorSetLayoutBinding, 4)
	for i := range bindings {
		bindings[i] = core1_0.DescriptorSetLayoutBinding{
			Binding:         i,
			DescriptorType:  core1_0.DescriptorTypeCombinedImageSampler,
			DescriptorCount: 1,
			StageFlags:      core1_0.StageFragment,
		}
	}
	layout, _, err := deviceDriver.CreateDescriptorSetLayout(nil, core1_0.DescriptorSetLayoutCreateInfo{
		Bindings: bindings,
	})
	if err != nil {
		return core1_0.DescriptorSetLayout{}, fmt.Errorf("create terrain descriptor set layout: %w", err)
	}
	return layout, nil
}

// BuildTerrainMaterial allocates a descriptor set binding the four terrain
// textures. The textures remain owned by the caller (they outlive the material).
func (r *Renderer) BuildTerrainMaterial(grass, path, rock, splat *Texture) (*TerrainMaterial, error) {
	sets, _, err := r.deviceDriver.AllocateDescriptorSets(core1_0.DescriptorSetAllocateInfo{
		DescriptorPool: r.descriptorPool,
		SetLayouts:     []core1_0.DescriptorSetLayout{r.terrainSetLayout},
	})
	if err != nil {
		return nil, fmt.Errorf("allocate terrain descriptor set: %w", err)
	}

	textures := [4]*Texture{grass, path, rock, splat}
	writes := make([]core1_0.WriteDescriptorSet, 4)
	for i, t := range textures {
		writes[i] = core1_0.WriteDescriptorSet{
			DstSet:         sets[0],
			DstBinding:     i,
			DescriptorType: core1_0.DescriptorTypeCombinedImageSampler,
			ImageInfo: []core1_0.DescriptorImageInfo{
				{
					Sampler:     t.sampler,
					ImageView:   t.view,
					ImageLayout: core1_0.ImageLayoutShaderReadOnlyOptimal,
				},
			},
		}
	}
	if err := r.deviceDriver.UpdateDescriptorSets(writes, nil); err != nil {
		return nil, fmt.Errorf("update terrain descriptor set: %w", err)
	}
	return &TerrainMaterial{DescriptorSet: sets[0]}, nil
}

// maxMaterials is how many Materials the descriptor pool is sized for. Each one
// takes a set, four combined image samplers, and a uniform buffer, so this is a
// real budget rather than a formality — exceeding it makes CreateMaterial fail
// with an allocation error rather than corrupting anything.
const maxMaterials = 64

// maxHDRSets bounds the tonemap pass's descriptor sets: one per swapchain
// image. Named rather than folded into the slack above because the swapchain
// image count is the driver's decision, not ours, and a headroom number that
// silently covers it is a number nobody can check.
const maxHDRSets = 8

// maxBloomSets bounds the bloom chain's descriptor sets: one per level per
// swapchain image. Sized against maxHDRSets rather than the real image count for
// the same reason -- the driver picks that, and it can change on a resize.
//
// Resizing rebuilds both chains without resetting the pool, so this is headroom
// for several resizes rather than an exact fit. A long session of window
// dragging will eventually exhaust it and fail the reallocation with a clear
// error, which is worse than resetting the pool but better than the silent
// corruption of reusing sets that still name freed views.
const maxBloomSets = maxHDRSets * bloomLevels

// createDescriptorPool creates a pool that can allocate combined image sampler and
// uniform buffer descriptor sets. Extra capacity for shadow mapping descriptors.
func createDescriptorPool(deviceDriver core1_0.DeviceDriver, maxSets int) (core1_0.DescriptorPool, error) {
	pool, _, err := deviceDriver.CreateDescriptorPool(nil, core1_0.DescriptorPoolCreateInfo{
		MaxSets: maxSets + 36 + maxMaterials + maxHDRSets + maxBloomSets,
		PoolSizes: []core1_0.DescriptorPoolSize{
			{
				Type: core1_0.DescriptorTypeCombinedImageSampler,
				// +4 sun shadow + 2 cube shadow samplers, then four maps per material
				// The tonemap's sets take two samplers each, hence the doubling.
				DescriptorCount: maxSets + 6 + maxMaterials*materialTextureBindings + maxHDRSets*2 + maxBloomSets,
			},
			{
				Type: core1_0.DescriptorTypeUniformBuffer,
				// +4 shadow UBO + point lights UBO per frame, then one per material
				DescriptorCount: 36 + maxFramesInFlight + maxMaterials,
			},
		},
	})
	if err != nil {
		return core1_0.DescriptorPool{}, fmt.Errorf("create descriptor pool: %w", err)
	}
	return pool, nil
}

// createJointDescriptorSetLayout creates a layout with a single UBO at set=1,
// binding=0, visible to the vertex stage. Used for skeletal animation joint matrices.
func createJointDescriptorSetLayout(deviceDriver core1_0.DeviceDriver) (core1_0.DescriptorSetLayout, error) {
	layout, _, err := deviceDriver.CreateDescriptorSetLayout(nil, core1_0.DescriptorSetLayoutCreateInfo{
		Bindings: []core1_0.DescriptorSetLayoutBinding{
			{
				Binding:         0,
				DescriptorType:  core1_0.DescriptorTypeUniformBuffer,
				DescriptorCount: 1,
				StageFlags:      core1_0.StageVertex,
			},
		},
	})
	if err != nil {
		return core1_0.DescriptorSetLayout{}, fmt.Errorf("create joint descriptor set layout: %w", err)
	}
	return layout, nil
}

// beginSingleTimeCommands allocates and begins a one-shot command buffer.
func (r *Renderer) beginSingleTimeCommands() (core1_0.CommandBuffer, error) {
	bufs, _, err := r.deviceDriver.AllocateCommandBuffers(core1_0.CommandBufferAllocateInfo{
		CommandPool:        r.commandPool,
		Level:              core1_0.CommandBufferLevelPrimary,
		CommandBufferCount: 1,
	})
	if err != nil {
		return core1_0.CommandBuffer{}, err
	}
	_, err = r.deviceDriver.BeginCommandBuffer(bufs[0], core1_0.CommandBufferBeginInfo{
		Flags: core1_0.CommandBufferUsageOneTimeSubmit,
	})
	if err != nil {
		return core1_0.CommandBuffer{}, err
	}
	return bufs[0], nil
}

// endSingleTimeCommands ends, submits, and waits for a one-shot command buffer.
func (r *Renderer) endSingleTimeCommands(cmdBuf core1_0.CommandBuffer) error {
	_, err := r.deviceDriver.EndCommandBuffer(cmdBuf)
	if err != nil {
		return err
	}
	_, err = r.deviceDriver.QueueSubmit(r.graphicsQueue, nil, core1_0.SubmitInfo{
		CommandBuffers: []core1_0.CommandBuffer{cmdBuf},
	})
	if err != nil {
		return err
	}
	r.deviceDriver.QueueWaitIdle(r.graphicsQueue)
	r.deviceDriver.FreeCommandBuffers(cmdBuf)
	return nil
}

// textureOptions describes how pixel data becomes a sampled image.
//
// The public constructors below differ only in these four fields. They were
// three near-identical copies of the same 200-line upload, which is how
// CreateTextureLinear came to be missing the mip chain CreateTexture has.
type textureOptions struct {
	// srgb picks R8G8B8A8_SRGB over _UNORM. An sRGB image decodes to linear on
	// every read, which is what colour wants and what data must not have —
	// normals, roughness, occlusion, and distance fields are numbers, not
	// light. Getting this wrong is silent: the texture still samples, just
	// with every value bent through a gamma curve.
	srgb bool

	filter  core1_0.Filter
	address core1_0.SamplerAddressMode

	// mipmap generates the full chain by successive linear blits. Minified
	// sampling without it picks near-random texels on sub-pixel geometry
	// (distant grass) and shimmers frame to frame.
	//
	// It also selects anisotropic filtering, because the two want the same
	// surfaces: tiling world textures seen at grazing angles, rather than the
	// screen-aligned UI and glyph atlases that sample at 1:1.
	mipmap bool
}

// CreateTexture uploads RGBA pixel data as an sRGB texture with a full mipmap
// chain, linear filtering, and repeat addressing. This is the constructor for
// colour: albedo, terrain detail, foliage cutouts.
func (r *Renderer) CreateTexture(pixels []byte, width, height int) (*Texture, error) {
	return r.createTexture(pixels, width, height, textureOptions{
		srgb:    true,
		filter:  core1_0.FilterLinear,
		address: core1_0.SamplerAddressModeRepeat,
		mipmap:  true,
	})
}

// SetMilkyWayTexture supplies a sky panorama for the star pass to sample as
// the galactic band, replacing the procedural one.
//
// The texture must be in the layout EquirectToSkyMap produces. Handing it a
// raw equirectangular image compiles, binds and draws -- and paints a smeared,
// mirrored sky, because the pass decodes the square as a hemi-octahedral map.
// Run the source through EquirectToSkyMap first; that is the only supported
// input, and it takes the equirect layout every all-sky survey ships.
//
// Nil, the default, leaves the procedural band. The engine ships no image --
// a sky panorama is megabytes and carries someone's licence, neither of which
// belongs in a module every consumer downloads.
//
// Supply one stripped of its point stars. The renderer draws its own, and they
// are sharp at any resolution and any field of view where a panorama's are not:
// a 6000-pixel-wide equirect is 16.7 pixels per degree against the 23 a 1280
// wide window at 55 degrees needs, so its stars arrive soft while its band --
// which has no detail that fine -- arrives intact. A median filter over the
// source removes the points and leaves the band.
func (r *Renderer) SetMilkyWayTexture(tex *Texture) { r.milkyWayTex = tex }

// Must match GALACTIC_POLE in shaders/stars.frag. The galactic orientation is
// baked into the map here rather than rebuilt per fragment.
var galacticPole = [3]float64{0.8619, 0.1603, -0.4810}

// EquirectToSkyMap resamples an equirectangular all-sky panorama into the
// square sky map SetMilkyWayTexture expects, returning RGBA pixels size by
// size. src is RGBA, srcW by srcH, longitude across and latitude down.
//
// The projection is hemi-octahedral: the upper hemisphere folded onto a square.
// Feeding the shader the equirect directly would be simpler and is what the
// first version did, but it wastes half the texels below a horizon the pass
// never samples, seams where atan2 wraps -- visibly, because the UV derivative
// jumps there and takes mip selection with it -- and pinches at the zenith.
// The octahedral square has none of those and decodes with arithmetic rather
// than trig.
//
// Sampling is bilinear and wraps in longitude, so the source's own seam does
// not survive into the map. size is the output edge; the panorama's horizontal
// resolution divided by four is a reasonable starting point, since the map
// covers a hemisphere rather than a sphere.
//
// This is CPU work over size*size texels and belongs at load time, not in a
// frame.
func EquirectToSkyMap(src []byte, srcW, srcH, size int) []byte {
	// Same basis the shader used before the rotation was baked in, so an image
	// prepared for either lands in the same place.
	gy := galacticPole
	gz := normalize3(cross3(gy, [3]float64{0, 1, 0}))
	gx := cross3(gy, gz)

	out := make([]byte, size*size*4)
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			// Square -> direction, upper hemisphere only.
			tx := (float64(x)+0.5)/float64(size)*2 - 1
			ty := (float64(y)+0.5)/float64(size)*2 - 1
			px := (tx + ty) * 0.5
			pz := (tx - ty) * 0.5
			d := normalize3([3]float64{px, 1 - math.Abs(px) - math.Abs(pz), pz})

			// World -> galactic, then an equirect lookup into the source.
			g := [3]float64{dot3(d, gx), dot3(d, gy), dot3(d, gz)}
			u := math.Atan2(g[2], g[0])/(2*math.Pi) + 0.5
			v := math.Acos(math.Min(1, math.Max(-1, g[1]))) / math.Pi

			r, gg, b := bilinearWrapU(src, srcW, srcH, u*float64(srcW), v*float64(srcH))
			o := (y*size + x) * 4
			out[o], out[o+1], out[o+2], out[o+3] = r, gg, b, 255
		}
	}
	return out
}

func normalize3(v [3]float64) [3]float64 {
	l := math.Sqrt(v[0]*v[0] + v[1]*v[1] + v[2]*v[2])
	return [3]float64{v[0] / l, v[1] / l, v[2] / l}
}

func cross3(a, b [3]float64) [3]float64 {
	return [3]float64{a[1]*b[2] - a[2]*b[1], a[2]*b[0] - a[0]*b[2], a[0]*b[1] - a[1]*b[0]}
}

func dot3(a, b [3]float64) float64 { return a[0]*b[0] + a[1]*b[1] + a[2]*b[2] }

// bilinearWrapU samples RGBA pixels at a pixel-space coordinate, wrapping in x
// and clamping in y -- which is what an equirect needs: longitude is periodic,
// latitude is not.
func bilinearWrapU(pix []byte, w, h int, fx, fy float64) (byte, byte, byte) {
	x0 := int(math.Floor(fx - 0.5))
	y0 := int(math.Floor(fy - 0.5))
	ax := fx - 0.5 - float64(x0)
	ay := fy - 0.5 - float64(y0)

	var r, g, b float64
	for j := 0; j < 2; j++ {
		sy := y0 + j
		if sy < 0 {
			sy = 0
		} else if sy >= h {
			sy = h - 1
		}
		for i := 0; i < 2; i++ {
			sx := ((x0+i)%w + w) % w
			o := (sy*w + sx) * 4
			wt := (1 - math.Abs(float64(i)-ax)) * (1 - math.Abs(float64(j)-ay))
			r += float64(pix[o]) * wt
			g += float64(pix[o+1]) * wt
			b += float64(pix[o+2]) * wt
		}
	}
	return byte(r), byte(g), byte(b)
}

// CreateDataTexture uploads RGBA pixel data as a linear (non-sRGB) texture with
// a full mipmap chain, linear filtering, and repeat addressing.
//
// This is the constructor for material maps — normal, metallic-roughness,
// occlusion. They hold numbers rather than colour, so sRGB decoding would
// corrupt them: a flat normal of 128 would arrive as 0.216 instead of 0.502,
// tilting every surface toward -X-Y. Nothing errors and the image still
// samples, so the only symptom is lighting that is wrong in a way that looks
// like a bad map.
//
// It differs from CreateTextureLinear, which is also non-sRGB but clamps and
// carries no mip chain because MSDF atlases need exact texel values.
func (r *Renderer) CreateDataTexture(pixels []byte, width, height int) (*Texture, error) {
	return r.createTexture(pixels, width, height, textureOptions{
		srgb:    false,
		filter:  core1_0.FilterLinear,
		address: core1_0.SamplerAddressModeRepeat,
		mipmap:  true,
	})
}

// CreateTextureLinear uploads RGBA pixel data as a linear (non-sRGB) texture with
// clamp-to-edge sampling. Used for MSDF atlases where distance values must be read as-is.
func (r *Renderer) CreateTextureLinear(pixels []byte, width, height int) (*Texture, error) {
	return r.createTexture(pixels, width, height, textureOptions{
		srgb:    false,
		filter:  core1_0.FilterLinear,
		address: core1_0.SamplerAddressModeClampToEdge,
		mipmap:  false,
	})
}

// CreateTextureNearest uploads RGBA pixel data as an sRGB texture with
// nearest-neighbor filtering and clamp-to-edge sampling. Used for pixel art.
func (r *Renderer) CreateTextureNearest(pixels []byte, width, height int) (*Texture, error) {
	return r.createTexture(pixels, width, height, textureOptions{
		srgb:    true,
		filter:  core1_0.FilterNearest,
		address: core1_0.SamplerAddressModeClampToEdge,
		mipmap:  false,
	})
}

// createTexture uploads RGBA pixel data to a device-local image and returns a
// Texture with a ready-to-bind descriptor set at set=0, binding=0.
func (r *Renderer) createTexture(pixels []byte, width, height int, opts textureOptions) (*Texture, error) {
	imageSize := width * height * 4

	format := core1_0.FormatR8G8B8A8UnsignedNormalized
	if opts.srgb {
		format = core1_0.FormatR8G8B8A8SRGB
	}

	// Full mip chain; fall back to a single level if the format can't be
	// linearly blitted (never the case for R8G8B8A8 on desktop GPUs).
	mipLevels := 1
	if opts.mipmap {
		mipLevels = bits.Len(uint(max(width, height)))
		fmtProps := r.instanceDriver.GetPhysicalDeviceFormatProperties(r.physicalDevice, format)
		if fmtProps.OptimalTilingFeatures&core1_0.FormatFeatureSampledImageFilterLinear == 0 {
			mipLevels = 1
		}
	}

	// Create staging buffer
	stagingBuf, _, err := r.deviceDriver.CreateBuffer(nil, core1_0.BufferCreateInfo{
		Size:        imageSize,
		Usage:       core1_0.BufferUsageTransferSrc,
		SharingMode: core1_0.SharingModeExclusive,
	})
	if err != nil {
		return nil, fmt.Errorf("create staging buffer: %w", err)
	}
	defer r.deviceDriver.DestroyBuffer(stagingBuf, nil)

	stagingMemReqs := r.deviceDriver.GetBufferMemoryRequirements(stagingBuf)
	stagingMemType, err := findMemoryType(
		r.instanceDriver, r.physicalDevice,
		stagingMemReqs.MemoryTypeBits,
		core1_0.MemoryPropertyHostVisible|core1_0.MemoryPropertyHostCoherent,
	)
	if err != nil {
		return nil, err
	}

	stagingMem, _, err := r.deviceDriver.AllocateMemory(nil, core1_0.MemoryAllocateInfo{
		AllocationSize:  stagingMemReqs.Size,
		MemoryTypeIndex: stagingMemType,
	})
	if err != nil {
		return nil, fmt.Errorf("allocate staging memory: %w", err)
	}
	defer r.deviceDriver.FreeMemory(stagingMem, nil)

	_, err = r.deviceDriver.BindBufferMemory(stagingBuf, stagingMem, 0)
	if err != nil {
		return nil, fmt.Errorf("bind staging buffer: %w", err)
	}

	// Copy pixel data to staging
	ptr, _, err := r.deviceDriver.MapMemory(stagingMem, 0, imageSize, 0)
	if err != nil {
		return nil, fmt.Errorf("map staging memory: %w", err)
	}
	dst := unsafe.Slice((*byte)(ptr), imageSize)
	copy(dst, pixels)
	r.deviceDriver.UnmapMemory(stagingMem)

	// Create device-local image (TransferSrc needed to blit mips from it)
	usage := core1_0.ImageUsageTransferDst | core1_0.ImageUsageSampled
	if mipLevels > 1 {
		usage |= core1_0.ImageUsageTransferSrc
	}
	img, _, err := r.deviceDriver.CreateImage(nil, core1_0.ImageCreateInfo{
		ImageType: core1_0.ImageType2D,
		Format:    format,
		Extent: core1_0.Extent3D{
			Width:  width,
			Height: height,
			Depth:  1,
		},
		MipLevels:     mipLevels,
		ArrayLayers:   1,
		Samples:       core1_0.Samples1,
		Tiling:        core1_0.ImageTilingOptimal,
		Usage:         usage,
		SharingMode:   core1_0.SharingModeExclusive,
		InitialLayout: core1_0.ImageLayoutUndefined,
	})
	if err != nil {
		return nil, fmt.Errorf("create texture image: %w", err)
	}

	imgMemReqs := r.deviceDriver.GetImageMemoryRequirements(img)
	imgMemType, err := findMemoryType(
		r.instanceDriver, r.physicalDevice,
		imgMemReqs.MemoryTypeBits,
		core1_0.MemoryPropertyDeviceLocal,
	)
	if err != nil {
		r.deviceDriver.DestroyImage(img, nil)
		return nil, err
	}

	imgMem, _, err := r.deviceDriver.AllocateMemory(nil, core1_0.MemoryAllocateInfo{
		AllocationSize:  imgMemReqs.Size,
		MemoryTypeIndex: imgMemType,
	})
	if err != nil {
		r.deviceDriver.DestroyImage(img, nil)
		return nil, fmt.Errorf("allocate texture memory: %w", err)
	}

	_, err = r.deviceDriver.BindImageMemory(img, imgMem, 0)
	if err != nil {
		r.deviceDriver.FreeMemory(imgMem, nil)
		r.deviceDriver.DestroyImage(img, nil)
		return nil, fmt.Errorf("bind texture image memory: %w", err)
	}

	// Transition Undefined → TransferDstOptimal
	cmdBuf, err := r.beginSingleTimeCommands()
	if err != nil {
		r.deviceDriver.FreeMemory(imgMem, nil)
		r.deviceDriver.DestroyImage(img, nil)
		return nil, err
	}

	// Transition all mip levels Undefined → TransferDstOptimal
	r.deviceDriver.CmdPipelineBarrier(cmdBuf,
		core1_0.PipelineStageTopOfPipe, core1_0.PipelineStageTransfer,
		0, nil, nil,
		[]core1_0.ImageMemoryBarrier{
			{
				OldLayout:           core1_0.ImageLayoutUndefined,
				NewLayout:           core1_0.ImageLayoutTransferDstOptimal,
				SrcQueueFamilyIndex: -1,
				DstQueueFamilyIndex: -1,
				Image:               img,
				SubresourceRange: core1_0.ImageSubresourceRange{
					AspectMask: core1_0.ImageAspectColor,
					LevelCount: mipLevels,
					LayerCount: 1,
				},
				SrcAccessMask: 0,
				DstAccessMask: core1_0.AccessTransferWrite,
			},
		},
	)

	// Copy buffer to mip 0
	r.deviceDriver.CmdCopyBufferToImage(cmdBuf, stagingBuf, img, core1_0.ImageLayoutTransferDstOptimal,
		core1_0.BufferImageCopy{
			ImageSubresource: core1_0.ImageSubresourceLayers{
				AspectMask: core1_0.ImageAspectColor,
				LayerCount: 1,
			},
			ImageExtent: core1_0.Extent3D{
				Width:  width,
				Height: height,
				Depth:  1,
			},
		},
	)

	// Generate the mip chain: blit each level from the one above it, then
	// transition the source level to ShaderReadOnlyOptimal.
	mipW, mipH := width, height
	for i := 1; i < mipLevels; i++ {
		r.deviceDriver.CmdPipelineBarrier(cmdBuf,
			core1_0.PipelineStageTransfer, core1_0.PipelineStageTransfer,
			0, nil, nil,
			[]core1_0.ImageMemoryBarrier{{
				OldLayout:           core1_0.ImageLayoutTransferDstOptimal,
				NewLayout:           core1_0.ImageLayoutTransferSrcOptimal,
				SrcQueueFamilyIndex: -1,
				DstQueueFamilyIndex: -1,
				Image:               img,
				SubresourceRange: core1_0.ImageSubresourceRange{
					AspectMask:   core1_0.ImageAspectColor,
					BaseMipLevel: i - 1,
					LevelCount:   1,
					LayerCount:   1,
				},
				SrcAccessMask: core1_0.AccessTransferWrite,
				DstAccessMask: core1_0.AccessTransferRead,
			}},
		)

		nextW := max(mipW/2, 1)
		nextH := max(mipH/2, 1)
		err = r.deviceDriver.CmdBlitImage(cmdBuf,
			img, core1_0.ImageLayoutTransferSrcOptimal,
			img, core1_0.ImageLayoutTransferDstOptimal,
			[]core1_0.ImageBlit{{
				SrcSubresource: core1_0.ImageSubresourceLayers{
					AspectMask: core1_0.ImageAspectColor,
					MipLevel:   i - 1,
					LayerCount: 1,
				},
				SrcOffsets: [2]core1_0.Offset3D{{X: 0, Y: 0, Z: 0}, {X: mipW, Y: mipH, Z: 1}},
				DstSubresource: core1_0.ImageSubresourceLayers{
					AspectMask: core1_0.ImageAspectColor,
					MipLevel:   i,
					LayerCount: 1,
				},
				DstOffsets: [2]core1_0.Offset3D{{X: 0, Y: 0, Z: 0}, {X: nextW, Y: nextH, Z: 1}},
			}},
			core1_0.FilterLinear,
		)
		if err != nil {
			r.deviceDriver.FreeMemory(imgMem, nil)
			r.deviceDriver.DestroyImage(img, nil)
			return nil, fmt.Errorf("blit mip %d: %w", i, err)
		}

		r.deviceDriver.CmdPipelineBarrier(cmdBuf,
			core1_0.PipelineStageTransfer, core1_0.PipelineStageFragmentShader,
			0, nil, nil,
			[]core1_0.ImageMemoryBarrier{{
				OldLayout:           core1_0.ImageLayoutTransferSrcOptimal,
				NewLayout:           core1_0.ImageLayoutShaderReadOnlyOptimal,
				SrcQueueFamilyIndex: -1,
				DstQueueFamilyIndex: -1,
				Image:               img,
				SubresourceRange: core1_0.ImageSubresourceRange{
					AspectMask:   core1_0.ImageAspectColor,
					BaseMipLevel: i - 1,
					LevelCount:   1,
					LayerCount:   1,
				},
				SrcAccessMask: core1_0.AccessTransferRead,
				DstAccessMask: core1_0.AccessShaderRead,
			}},
		)

		mipW, mipH = nextW, nextH
	}

	// Final level (still TransferDst) → ShaderReadOnlyOptimal
	r.deviceDriver.CmdPipelineBarrier(cmdBuf,
		core1_0.PipelineStageTransfer, core1_0.PipelineStageFragmentShader,
		0, nil, nil,
		[]core1_0.ImageMemoryBarrier{
			{
				OldLayout:           core1_0.ImageLayoutTransferDstOptimal,
				NewLayout:           core1_0.ImageLayoutShaderReadOnlyOptimal,
				SrcQueueFamilyIndex: -1,
				DstQueueFamilyIndex: -1,
				Image:               img,
				SubresourceRange: core1_0.ImageSubresourceRange{
					AspectMask:   core1_0.ImageAspectColor,
					BaseMipLevel: mipLevels - 1,
					LevelCount:   1,
					LayerCount:   1,
				},
				SrcAccessMask: core1_0.AccessTransferWrite,
				DstAccessMask: core1_0.AccessShaderRead,
			},
		},
	)

	err = r.endSingleTimeCommands(cmdBuf)
	if err != nil {
		r.deviceDriver.FreeMemory(imgMem, nil)
		r.deviceDriver.DestroyImage(img, nil)
		return nil, err
	}

	// Create image view covering the full mip chain
	view, _, err := r.deviceDriver.CreateImageView(nil, core1_0.ImageViewCreateInfo{
		Image:    img,
		ViewType: core1_0.ImageViewType2D,
		Format:   format,
		SubresourceRange: core1_0.ImageSubresourceRange{
			AspectMask: core1_0.ImageAspectColor,
			LevelCount: mipLevels,
			LayerCount: 1,
		},
	})
	if err != nil {
		r.deviceDriver.FreeMemory(imgMem, nil)
		r.deviceDriver.DestroyImage(img, nil)
		return nil, fmt.Errorf("create texture image view: %w", err)
	}

	// Mip-aware sampling only for images that have a chain; the rest sample one
	// level, so MaxLod stays at zero and the mip mode follows the filter.
	mipMode := core1_0.SamplerMipmapModeNearest
	if opts.filter == core1_0.FilterLinear {
		mipMode = core1_0.SamplerMipmapModeLinear
	}
	var maxLod, maxAniso float32
	if opts.mipmap {
		mipMode = core1_0.SamplerMipmapModeLinear
		maxLod = float32(mipLevels)
		maxAniso = r.maxAnisotropy
	}

	sampler, _, err := r.deviceDriver.CreateSampler(nil, core1_0.SamplerCreateInfo{
		MagFilter:        opts.filter,
		MinFilter:        opts.filter,
		AddressModeU:     opts.address,
		AddressModeV:     opts.address,
		AddressModeW:     opts.address,
		MipmapMode:       mipMode,
		MaxLod:           maxLod,
		AnisotropyEnable: maxAniso > 0,
		MaxAnisotropy:    maxAniso,
	})
	if err != nil {
		r.deviceDriver.DestroyImageView(view, nil)
		r.deviceDriver.FreeMemory(imgMem, nil)
		r.deviceDriver.DestroyImage(img, nil)
		return nil, fmt.Errorf("create sampler: %w", err)
	}

	// Allocate descriptor set
	sets, _, err := r.deviceDriver.AllocateDescriptorSets(core1_0.DescriptorSetAllocateInfo{
		DescriptorPool: r.descriptorPool,
		SetLayouts:     []core1_0.DescriptorSetLayout{r.descriptorSetLayout},
	})
	if err != nil {
		r.deviceDriver.DestroySampler(sampler, nil)
		r.deviceDriver.DestroyImageView(view, nil)
		r.deviceDriver.FreeMemory(imgMem, nil)
		r.deviceDriver.DestroyImage(img, nil)
		return nil, fmt.Errorf("allocate descriptor set: %w", err)
	}

	// Update descriptor set with image + sampler
	err = r.deviceDriver.UpdateDescriptorSets([]core1_0.WriteDescriptorSet{
		{
			DstSet:         sets[0],
			DstBinding:     0,
			DescriptorType: core1_0.DescriptorTypeCombinedImageSampler,
			ImageInfo: []core1_0.DescriptorImageInfo{
				{
					Sampler:     sampler,
					ImageView:   view,
					ImageLayout: core1_0.ImageLayoutShaderReadOnlyOptimal,
				},
			},
		},
	}, nil)
	if err != nil {
		r.deviceDriver.DestroySampler(sampler, nil)
		r.deviceDriver.DestroyImageView(view, nil)
		r.deviceDriver.FreeMemory(imgMem, nil)
		r.deviceDriver.DestroyImage(img, nil)
		return nil, fmt.Errorf("update descriptor set: %w", err)
	}

	tex := &Texture{
		image:         img,
		memory:        imgMem,
		view:          view,
		sampler:       sampler,
		DescriptorSet: sets[0],
	}
	r.textures = append(r.textures, tex)
	return tex, nil
}

// createFallbackTexture creates a 1x1 white RGBA texture used when no texture is assigned.
func (r *Renderer) createFallbackTexture() (*Texture, error) {
	white := []byte{255, 255, 255, 255}
	return r.CreateTexture(white, 1, 1)
}

// DestroyTexture releases GPU resources for a texture.
func (r *Renderer) DestroyTexture(t *Texture) {
	if t == nil || t.destroyed {
		return
	}
	t.destroyed = true

	// Deregister for the same reason as DestroyMesh: the renderer's own
	// cleanup would otherwise free these handles a second time.
	for i, other := range r.textures {
		if other == t {
			r.textures = append(r.textures[:i], r.textures[i+1:]...)
			break
		}
	}

	r.deviceDriver.DestroySampler(t.sampler, nil)
	r.deviceDriver.DestroyImageView(t.view, nil)
	r.deviceDriver.FreeMemory(t.memory, nil)
	r.deviceDriver.DestroyImage(t.image, nil)
}

// decodeRGBA reads an image (PNG or JPEG) from fsys and returns it as tightly
// packed RGBA bytes, which is the only layout the upload path accepts.
func decodeRGBA(fsys fs.FS, name string) (pix []byte, w, h int, err error) {
	f, err := fsys.Open(name)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("open texture %q: %w", name, err)
	}
	defer f.Close()

	img, _, err := image.Decode(f)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("decode texture %q: %w", name, err)
	}

	bounds := img.Bounds()
	rgba := image.NewRGBA(bounds)
	draw.Draw(rgba, bounds, img, bounds.Min, draw.Src)
	return rgba.Pix, bounds.Dx(), bounds.Dy(), nil
}

// LoadTextureNearest reads a PNG from fsys and creates a nearest-neighbor
// filtered texture. Used for pixel-art UI assets.
func (r *Renderer) LoadTextureNearest(fsys fs.FS, name string) (*Texture, error) {
	pix, w, h, err := decodeRGBA(fsys, name)
	if err != nil {
		return nil, err
	}
	return r.CreateTextureNearest(pix, w, h)
}

// LoadTextureLinear reads a PNG from fsys and creates a linearly-filtered
// texture. Used for photographic or high-res UI assets.
func (r *Renderer) LoadTextureLinear(fsys fs.FS, name string) (*Texture, error) {
	pix, w, h, err := decodeRGBA(fsys, name)
	if err != nil {
		return nil, err
	}
	return r.CreateTextureLinear(pix, w, h)
}

// LoadTexture reads an image (PNG or JPEG) from fsys and creates a linearly-filtered,
// repeat-sampled sRGB texture. Used for tiling world textures like terrain grass.
func (r *Renderer) LoadTexture(fsys fs.FS, name string) (*Texture, error) {
	pix, w, h, err := decodeRGBA(fsys, name)
	if err != nil {
		return nil, err
	}
	return r.CreateTexture(pix, w, h)
}

// LoadDataTexture reads an image (PNG or JPEG) from fsys as a material map:
// linear, tiling, mipmapped. Use it for normal, metallic-roughness, and
// occlusion maps; LoadTexture would decode them as sRGB colour.
func (r *Renderer) LoadDataTexture(fsys fs.FS, name string) (*Texture, error) {
	pix, w, h, err := decodeRGBA(fsys, name)
	if err != nil {
		return nil, err
	}
	return r.CreateDataTexture(pix, w, h)
}
