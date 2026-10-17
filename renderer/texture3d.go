package renderer

import (
	"fmt"

	"github.com/vkngwrapper/core/v3/core1_0"
)

// Texture3DFormat is the image format a volume is uploaded into. The zero value
// is not a format: a volume that is R8 and one that is RGBA8 differ four times
// in memory and in what a shader reads back, so there is no default a caller
// should be handed without asking.
type Texture3DFormat int

const (
	// Texture3DR8 is R8_UNORM: one byte per texel, read back as 0..1. A density
	// field, a signed distance scaled into the range, a coverage mask.
	Texture3DR8 Texture3DFormat = iota + 1

	// Texture3DRGBA8 is R8G8B8A8_UNORM: four bytes per texel. Four fields that
	// are always read together -- density with its gradient, or a colour and
	// an occupancy.
	Texture3DRGBA8

	// Texture3DRGBA16F is R16G16B16A16_SFLOAT: eight bytes per texel holding
	// four IEEE binary16 values, each as its two little-endian BYTES, which is
	// the host order on every platform this engine runs on. Float16 produces
	// the bits; the caller packs them, because this constructor takes one byte
	// slice for every format so that the mip chain can be one slice per level
	// whatever the format. The same trap CreateTextureRGBA16F has applies: a
	// small integer written as bytes is a half-float near zero.
	Texture3DRGBA16F
)

// volumeFormats is the VkFormat and texel size behind each Texture3DFormat, in
// the order the constants are declared. Both Capabilities and the constructor
// read this table, so the device query the report answers is the one the
// upload makes.
var volumeFormats = [...]struct {
	name string
	textureFormat
}{
	Texture3DR8:      {"R8", textureFormat{vk: core1_0.FormatR8UnsignedNormalized, texel: 1}},
	Texture3DRGBA8:   {"RGBA8", textureFormat{vk: core1_0.FormatR8G8B8A8UnsignedNormalized, texel: 4}},
	Texture3DRGBA16F: {"RGBA16F", textureFormat{vk: core1_0.FormatR16G16B16A16SignedFloat, texel: 8}},
}

// Texture3DOptions is how a volume is uploaded and sampled.
//
// Format is required. The rest of the zero value is nearest filtering,
// clamp-to-edge on all three axes and a single level, which is what a lookup
// table wants and what a cached density field does not: a field marched through
// by a shader wants FilterLinear so that the hardware interpolates between
// slices, and WrapRepeat on the axes it tiles along.
//
// The three wrap axes are separate because a volume that repeats in X and Y and
// clamps in Z is a real case -- a tiling cloud layer with a floor and a ceiling
// -- and a single field could not say it. They are Vulkan's addressModeU, V and
// W, named for the extent axes they govern.
type Texture3DOptions struct {
	Format Texture3DFormat

	// Filter is minification and magnification together, as in TextureOptions.
	// FilterLinear on a format the device cannot filter linearly is refused
	// rather than demoted; every format here is required to support it in core
	// Vulkan, so that cannot happen on a conformant device.
	Filter TargetFilter

	// WrapX, WrapY and WrapZ address the width, height and depth axes. The zero
	// value of each is clamp-to-edge.
	WrapX, WrapY, WrapZ TargetWrap

	// Mips is an explicit chain below level 0: one slice per level, each the
	// previous level's extent halved on every axis and floored to one, in the
	// same format as the base. The chain may stop early; it may not continue
	// past 1x1x1. Every level's byte count is checked before anything is
	// allocated.
	//
	// Supply one when the levels have to mean something in particular -- a
	// chain built by the caller's own filter, or levels that are not averages at
	// all. For a plain box-filtered chain ask for Mipmap instead.
	Mips [][]byte

	// Mipmap generates the full chain by successive 3D linear blits, each
	// level the 2x2x2 box mean of the one above it, floored to one in each axis
	// as the extent runs out. It needs the format to be a linear blit source
	// and destination with optimal tiling; a device without that is refused
	// with an error naming the format rather than handed a shorter chain, which
	// is the opposite of the 2D path's silent fallback. Capabilities reports
	// the answer as VolumeMipGeneration. Mips and Mipmap are exclusive.
	//
	// A generated chain does not select anisotropic filtering, where a 2D one
	// does. Anisotropy is for a surface seen at a grazing angle; a volume is
	// sampled along a ray through its interior, and a sampler that picked a
	// finer level along one axis would make the level a shader reads at a given
	// point depend on the ray's direction.
	Mipmap bool
}

// CreateTexture3D uploads a volume of width by height by depth texels, tightly
// packed with x varying fastest, then y, then z, and returns a Texture that
// binds as a sampler3D through AppPassDesc.Reads or AppComputeDesc.Reads with
// its index listed in Reads3D. Nothing else accepts one: the shared slots
// SetShaderTexture fills, a material's maps and a draw's texture are all
// declared sampler2D by the engine's own shaders, and binding a 3D view there
// is refused where the engine has a say and a validation-layer error where it
// does not.
//
// pixels holds width*height*depth texels at the format's size, in bytes. Every
// check -- the extent against the device's 3D image limit, the byte count of
// the base and of each supplied level, the format's sampled, filtered and
// blittable support -- runs before any allocation, so a refused upload creates
// nothing and counts nothing.
//
// It is released by DestroyTexture under the one contract every texture
// follows, and counted in ResourceCounts.Textures.
func (r *Renderer) CreateTexture3D(pixels []byte, width, height, depth int, opts Texture3DOptions) (*Texture, error) {
	if opts.Format <= 0 || int(opts.Format) >= len(volumeFormats) {
		return nil, fmt.Errorf("texture3d: Format: %d is not a Texture3DFormat; choose Texture3DR8, Texture3DRGBA8 or Texture3DRGBA16F", opts.Format)
	}
	f := volumeFormats[opts.Format]
	if width <= 0 || height <= 0 || depth <= 0 {
		return nil, fmt.Errorf("texture3d: %dx%dx%d has no texels", width, height, depth)
	}

	// The limit first, before the byte count: a caller who asked for an extent
	// the device cannot make should hear that rather than how many bytes the
	// impossible extent would have needed.
	props, err := r.instanceDriver.GetPhysicalDeviceProperties(r.physicalDevice)
	if err != nil {
		return nil, fmt.Errorf("texture3d: query device limits: %w", err)
	}
	if limit := props.Limits.MaxImageDimension3D; width > limit || height > limit || depth > limit {
		return nil, fmt.Errorf("texture3d: %dx%dx%d exceeds the device's 3D image limit of %d on every axis (Capabilities.MaxTexture3DExtent)",
			width, height, depth, limit)
	}
	if want := width * height * depth * f.texel; len(pixels) != want {
		return nil, fmt.Errorf("texture3d: %d bytes of pixels for a %dx%dx%d %s volume, want %d (%d bytes per texel)",
			len(pixels), width, height, depth, f.name, want, f.texel)
	}

	o, err := opts.internal(f.textureFormat, depth)
	if err != nil {
		return nil, err
	}
	if len(opts.Mips) > 0 && opts.Mipmap {
		return nil, fmt.Errorf("texture3d: Mips and Mipmap: supply a chain or ask for one generated, not both")
	}
	if err := checkVolumeChain(opts.Mips, width, height, depth, f.texel); err != nil {
		return nil, err
	}

	features := r.instanceDriver.GetPhysicalDeviceFormatProperties(r.physicalDevice, f.vk).OptimalTilingFeatures
	if features&core1_0.FormatFeatureSampledImage == 0 {
		return nil, fmt.Errorf("texture3d: format %v cannot be sampled on this device", f.vk)
	}
	if o.filter == core1_0.FilterLinear && features&core1_0.FormatFeatureSampledImageFilterLinear == 0 {
		return nil, fmt.Errorf("texture3d: Filter: format %v does not support linear sampling on this device", f.vk)
	}
	if opts.Mipmap && !volumeBlitSupported(features) {
		return nil, fmt.Errorf("texture3d: Mipmap: format %v cannot be linearly blitted on this device; supply Mips instead (Capabilities.VolumeMipGeneration)", f.vk)
	}
	return r.createTexture(pixels, width, height, o)
}

// internal translates the public options into the upload path's, rejecting an
// out-of-range enum rather than reading it as its zero value, for the reason
// TextureOptions.internal gives.
func (opts Texture3DOptions) internal(f textureFormat, depth int) (textureOptions, error) {
	o := textureOptions{
		format: f,
		filter: core1_0.FilterNearest,
		mipmap: opts.Mipmap,
		depth:  depth,
		levels: opts.Mips,
	}
	switch opts.Filter {
	case FilterNearest:
	case FilterLinear:
		o.filter = core1_0.FilterLinear
	default:
		return textureOptions{}, fmt.Errorf("texture3d: Filter: unknown filter %d", opts.Filter)
	}
	for _, axis := range []struct {
		name string
		wrap TargetWrap
		dst  *core1_0.SamplerAddressMode
	}{{"WrapX", opts.WrapX, &o.addressU}, {"WrapY", opts.WrapY, &o.addressV}, {"WrapZ", opts.WrapZ, &o.addressW}} {
		switch axis.wrap {
		case WrapClampToEdge:
			*axis.dst = core1_0.SamplerAddressModeClampToEdge
		case WrapRepeat:
			*axis.dst = core1_0.SamplerAddressModeRepeat
		default:
			return textureOptions{}, fmt.Errorf("texture3d: %s: unknown wrap %d", axis.name, axis.wrap)
		}
	}
	return o, nil
}

// checkVolumeChain is the per-level extent and byte-count check for a supplied
// mip chain. Each level's extent is derived from the level before it -- halved
// and floored to one on each axis -- rather than from level 0 and the level
// index, because the two agree only while no axis has reached one: a 64x32x8
// volume's level 4 is 4x2x1, and 64>>4 x 32>>4 x 8>>4 would be 4x2x0.
func checkVolumeChain(levels [][]byte, width, height, depth, texel int) error {
	w, h, d := width, height, depth
	for i, level := range levels {
		if w == 1 && h == 1 && d == 1 {
			return fmt.Errorf("texture3d: Mips: level %d is past the 1x1x1 the chain ends at; %d levels are the most a %dx%dx%d volume has",
				i+1, i+1, width, height, depth)
		}
		w, h, d = max(w/2, 1), max(h/2, 1), max(d/2, 1)
		if want := w * h * d * texel; len(level) != want {
			return fmt.Errorf("texture3d: Mips: level %d holds %d bytes for %dx%dx%d, want %d (%d bytes per texel)",
				i+1, len(level), w, h, d, want, texel)
		}
	}
	return nil
}

// volumeBlitSupported is the format-feature test a generated volume chain needs:
// a linear blit reads the format as a blit source, writes it as a blit
// destination, and filters it linearly on the way. One function, read by the
// constructor's refusal and by Capabilities.VolumeMipGeneration, so the report
// cannot promise what the upload would refuse.
func volumeBlitSupported(features core1_0.FormatFeatureFlags) bool {
	const want = core1_0.FormatFeatureBlitSource | core1_0.FormatFeatureBlitDestination | core1_0.FormatFeatureSampledImageFilterLinear
	return features&want == want
}

// Dimensions is 3 for a texture CreateTexture3D made and 2 for every other --
// an uploaded image, a render target's texture, the borrowed scene colour and
// depth. It is what an application pass's Reads3D declaration is checked
// against: a shader's sampler2D or sampler3D is not visible to the engine, so
// the pass says which it declared and the texture says which it is, and the
// two have to agree before the pass exists.
func (t *Texture) Dimensions() int {
	if t != nil && t.volume {
		return 3
	}
	return 2
}
