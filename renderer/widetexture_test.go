package renderer

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"testing"

	"github.com/vkngwrapper/core/v3/common"
	"github.com/vkngwrapper/core/v3/core1_0"
	"github.com/vkngwrapper/core/v3/loader"
)

// This file drives CreateTextureRGBA16F and CreateTextureR32F with no GPU, on
// the same fake-driver stack the glTF image upload uses
// (gltftexturecache_test.go's textureTestDriver, which is storagebuffer_test.go's
// bufferTestDriver plus the image-copy commands, which is
// recreateswapchain_test.go's resizeFakeDriver plus buffers). What it adds is the
// two things an eight-bit upload never had to be checked for: which VkFormat the
// image was created with, and how many bytes of the caller's slice reached the
// staging map.
//
// The instance driver is this file's own rather than bufferTestInstance, because
// the wide constructors ASK about format support and the shared fixture reports
// none -- it answers every format with the depth-attachment and storage bits,
// which is all findDepthFormat ever needed from it. Changing that one would
// change the mip-chain fallback under every existing texture test; adding one
// here changes nothing else.

// wideFormatInstance answers GetPhysicalDeviceFormatProperties with exactly the
// optimal-tiling features a test wants to give the device, for every format. Per
// format would be more faithful and would prove less: what the refusal paths
// need is a device that says no, and which format it says no about is the test's
// business rather than this type's.
type wideFormatInstance struct {
	bufferTestInstance
	features core1_0.FormatFeatureFlags
}

func (i wideFormatInstance) GetPhysicalDeviceFormatProperties(core1_0.PhysicalDevice, core1_0.Format) *core1_0.FormatProperties {
	return &core1_0.FormatProperties{OptimalTilingFeatures: i.features}
}

// sampledLinear is what a conformant device reports for R16G16B16A16_SFLOAT:
// both formats must be sampled, and this one must also filter linearly.
// sampledOnly is R32_SFLOAT's guaranteed minimum -- linear filtering of it is
// optional in core Vulkan, which is the one refusal below that can happen on
// real hardware.
const (
	sampledOnly   = core1_0.FormatFeatureSampledImage
	sampledLinear = core1_0.FormatFeatureSampledImage | core1_0.FormatFeatureSampledImageFilterLinear
)

// wideTextureDriver records the create-infos the upload path passes, which the
// drivers it builds on discard. Each method records only after delegating, so a
// call the failure injection turned into an error is not recorded as having
// happened.
type wideTextureDriver struct {
	*textureTestDriver

	images      []core1_0.ImageCreateInfo
	views       []core1_0.ImageViewCreateInfo
	samplers    []core1_0.SamplerCreateInfo
	stagingSize []int
	copies      []core1_0.BufferImageCopy
	sampled     []core1_0.DescriptorImageInfo
}

func (d *wideTextureDriver) CreateImage(cb *loader.AllocationCallbacks, o core1_0.ImageCreateInfo) (core1_0.Image, common.VkResult, error) {
	img, res, err := d.textureTestDriver.CreateImage(cb, o)
	if err == nil {
		d.images = append(d.images, o)
	}
	return img, res, err
}

func (d *wideTextureDriver) CreateImageView(cb *loader.AllocationCallbacks, o core1_0.ImageViewCreateInfo) (core1_0.ImageView, common.VkResult, error) {
	v, res, err := d.textureTestDriver.CreateImageView(cb, o)
	if err == nil {
		d.views = append(d.views, o)
	}
	return v, res, err
}

func (d *wideTextureDriver) CreateSampler(cb *loader.AllocationCallbacks, o core1_0.SamplerCreateInfo) (core1_0.Sampler, common.VkResult, error) {
	s, res, err := d.textureTestDriver.CreateSampler(cb, o)
	if err == nil {
		d.samplers = append(d.samplers, o)
	}
	return s, res, err
}

func (d *wideTextureDriver) CreateBuffer(cb *loader.AllocationCallbacks, o core1_0.BufferCreateInfo) (core1_0.Buffer, common.VkResult, error) {
	b, res, err := d.textureTestDriver.CreateBuffer(cb, o)
	if err == nil {
		d.stagingSize = append(d.stagingSize, o.Size)
	}
	return b, res, err
}

func (d *wideTextureDriver) CmdCopyBufferToImage(cmd core1_0.CommandBuffer, src core1_0.Buffer, dst core1_0.Image, layout core1_0.ImageLayout, regions ...core1_0.BufferImageCopy) error {
	if err := d.textureTestDriver.CmdCopyBufferToImage(cmd, src, dst, layout, regions...); err != nil {
		return err
	}
	d.copies = append(d.copies, regions...)
	return nil
}

// UpdateDescriptorSets records the IMAGE infos, where bufferTestDriver records
// the buffer ones. Both are needed: the buffer half is what the storage-buffer
// tests read, and the image half is how this file sees which view and sampler a
// SetShaderTexture flush actually wrote.
func (d *wideTextureDriver) UpdateDescriptorSets(writes []core1_0.WriteDescriptorSet, copies []core1_0.CopyDescriptorSet) error {
	for _, w := range writes {
		d.sampled = append(d.sampled, w.ImageInfo...)
	}
	return d.textureTestDriver.UpdateDescriptorSets(writes, copies)
}

func wideFixture(features core1_0.FormatFeatureFlags) (*Renderer, *wideTextureDriver) {
	r, t := textureFixture()
	d := &wideTextureDriver{textureTestDriver: t}
	r.deviceDriver = d
	r.instanceDriver = wideFormatInstance{features: features}
	return r, d
}

// halfRamp is the test data: n values spanning three decades, which is the range
// an eight-bit texture cannot hold however its levels are spread and therefore
// the range the wide formats exist for.
func halfRamp(n int) ([]uint16, []float32) {
	bits := make([]uint16, n*4)
	want := make([]float32, n)
	for i := range n {
		v := float32(0.001 * math.Pow(2000, float64(i)/float64(max(n-1, 1))))
		want[i] = v
		for ch := range 4 {
			bits[i*4+ch] = Float16(v * float32(ch+1))
		}
	}
	return bits, want
}

// TestWideTextureFormatsAndUpload is the shape of both uploads: the VkFormat the
// image was created with, the staging buffer and map sized to the caller's
// slice, the copy covering the whole extent, and the bytes in the map being the
// caller's own values in host order.
//
// The byte comparison is the half of this with teeth. A format and a texel size
// that disagree -- R16G16B16A16_SFLOAT with four bytes per texel, say -- creates,
// copies, uploads and samples without a word from the validation layer; the
// image is simply the top-left quarter of the data, stretched.
//
// BROKEN: set formatRGBA16F.vk to FormatR8G8B8A8UnsignedNormalized, the format
// mapping going wrong with the texel size left right. FAILED with "RGBA16F:
// created R8G8B8A8 Unsigned Normalized, want R16G16B16A16 Signed Float" and
// "RGBA16F: the view reads R8G8B8A8 Unsigned Normalized, want R16G16B16A16
// Signed Float". Restored with `git checkout -- renderer/texture.go`.
//
// BROKEN: set formatRGBA16F.texel to 4, the other half of the same mistake and
// the half the validation layer cannot see. FAILED before reaching any of the
// assertions here, in createTexture's own length check: "texture: 2048 bytes of
// pixels for 64x4 at 4 bytes per texel, want 1024", in this test and in three
// more. Worth recording because it is not what was expected -- the plan was for
// the staging-size assertion to catch it -- and it is the better answer: a
// format and a texel size that disagree are refused at the call rather than
// measured afterwards.
func TestWideTextureFormatsAndUpload(t *testing.T) {
	const w, h = 64, 4
	bits, _ := halfRamp(w * h)
	scalars := make([]float32, w*h)
	for i := range scalars {
		scalars[i] = Float16Value(bits[i*4])
	}

	t.Run("RGBA16F", func(t *testing.T) {
		r, d := wideFixture(sampledLinear)
		tex, err := r.CreateTextureRGBA16F(bits, w, h, TextureOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if got := d.images[0].Format; got != core1_0.FormatR16G16B16A16SignedFloat {
			t.Errorf("RGBA16F: created %v, want %v", got, core1_0.FormatR16G16B16A16SignedFloat)
		}
		if got := d.views[0].Format; got != core1_0.FormatR16G16B16A16SignedFloat {
			t.Errorf("RGBA16F: the view reads %v, want %v", got, core1_0.FormatR16G16B16A16SignedFloat)
		}
		want := w * h * 8
		if got := d.stagingSize[0]; got != want {
			t.Errorf("RGBA16F: staging buffer is %d bytes, want %d", got, want)
		}
		if got := len(d.mapped[0]); got != want {
			t.Errorf("RGBA16F: mapped %d bytes of %d", got, want)
		}
		raw := make([]byte, len(bits)*2)
		for i, b := range bits {
			binary.LittleEndian.PutUint16(raw[i*2:], b)
		}
		if string(d.mapped[0]) != string(raw) {
			t.Error("RGBA16F: the staged bytes are not the half-float bits the caller passed, in host order")
		}
		checkCopyExtent(t, d, w, h)
		if tex.destroyed {
			t.Error("RGBA16F: a fresh texture reads as destroyed")
		}
	})

	t.Run("R32F", func(t *testing.T) {
		r, d := wideFixture(sampledOnly)
		if _, err := r.CreateTextureR32F(scalars, w, h, TextureOptions{}); err != nil {
			t.Fatal(err)
		}
		if got := d.images[0].Format; got != core1_0.FormatR32SignedFloat {
			t.Errorf("R32F: created %v, want %v", got, core1_0.FormatR32SignedFloat)
		}
		want := w * h * 4
		if got := d.stagingSize[0]; got != want {
			t.Errorf("R32F: staging buffer is %d bytes, want %d", got, want)
		}
		if got := len(d.mapped[0]); got != want {
			t.Errorf("R32F: mapped %d bytes of %d", got, want)
		}
		raw := make([]byte, len(scalars)*4)
		for i, v := range scalars {
			binary.LittleEndian.PutUint32(raw[i*4:], math.Float32bits(v))
		}
		if string(d.mapped[0]) != string(raw) {
			t.Error("R32F: the staged bytes are not the float32s the caller passed, in host order")
		}
		checkCopyExtent(t, d, w, h)
	})
}

func checkCopyExtent(t *testing.T, d *wideTextureDriver, w, h int) {
	t.Helper()
	if len(d.copies) != 1 {
		t.Fatalf("recorded %d buffer-to-image copies, want 1", len(d.copies))
	}
	e := d.copies[0].ImageExtent
	if e.Width != w || e.Height != h || e.Depth != 1 {
		t.Errorf("the copy covers %dx%dx%d, want %dx%dx1", e.Width, e.Height, e.Depth, w, h)
	}
}

// TestEightBitConstructorsPinTheirEncoding is here because the wide-format
// refactor moved the one line whose own comment says getting it wrong is silent,
// and nothing in this package was watching it.
//
// rgba8 is that line. Before the refactor the sRGB-versus-UNORM choice was a
// bool on textureOptions read once inside createTexture; now it is a function
// four constructors and the glTF loader call. Making it return
// R8G8B8A8_UNORM for every caller -- the whole of the mistake -- was tried, and
// the ENTIRE renderer package still passed: nothing asserted which VkFormat an
// eight-bit upload creates, so the only symptom was the one the comment
// describes, every colour texture in every scene sampling 2.2 gammas too dark
// and every normal map correct by accident.
//
// So this pins all six. The glTF pair is not redundant with the four above it:
// the loader picks its own srgb from the material slot an image is bound to, and
// TestGLTFTextureCacheKeyDistinguishesWrapAndEncoding -- which looks like it
// covers this -- passed under the break too, because the cache KEY carries srgb
// independently of what the upload does with it.
//
// BROKEN: made rgba8 ignore srgb and return R8G8B8A8_UNORM for every caller.
// FAILED three times and only three: "CreateTexture: created R8G8B8A8 Unsigned
// Normalized, want R8G8B8A8 sRGB (colour, so it decodes to linear on every
// read)", "CreateTextureNearest: created R8G8B8A8 Unsigned Normalized, want
// R8G8B8A8 sRGB (pixel art is colour)" and "a glTF base colour image was
// uploaded as R8G8B8A8 Unsigned Normalized, want R8G8B8A8 sRGB". The two data
// constructors passed, which is right -- UNORM is what they ask for.
//
// BROKEN: the mirror, returning R8G8B8A8_SRGB for every caller, which is the
// expensive direction -- it bends every normal, roughness, occlusion and MSDF
// distance through a gamma curve. FAILED with "CreateDataTexture: created
// R8G8B8A8 sRGB, want R8G8B8A8 Unsigned Normalized (material maps hold numbers,
// not light)", "CreateTextureLinear: created R8G8B8A8 sRGB, want R8G8B8A8
// Unsigned Normalized (MSDF distances must be read as written)" and "a glTF
// normal map was uploaded as R8G8B8A8 sRGB, want R8G8B8A8 Unsigned Normalized".
func TestEightBitConstructorsPinTheirEncoding(t *testing.T) {
	const (
		srgb  = core1_0.FormatR8G8B8A8SRGB
		unorm = core1_0.FormatR8G8B8A8UnsignedNormalized
	)
	pix := make([]byte, 2*2*4)

	r, d := wideFixture(sampledLinear)
	for _, tc := range []struct {
		name   string
		make   func() (*Texture, error)
		want   core1_0.Format
		reason string
	}{
		{"CreateTexture", func() (*Texture, error) { return r.CreateTexture(pix, 2, 2) }, srgb,
			"colour, so it decodes to linear on every read"},
		{"CreateDataTexture", func() (*Texture, error) { return r.CreateDataTexture(pix, 2, 2) }, unorm,
			"material maps hold numbers, not light"},
		{"CreateTextureLinear", func() (*Texture, error) { return r.CreateTextureLinear(pix, 2, 2) }, unorm,
			"MSDF distances must be read as written"},
		{"CreateTextureNearest", func() (*Texture, error) { return r.CreateTextureNearest(pix, 2, 2) }, srgb,
			"pixel art is colour"},
	} {
		d.images = nil
		if _, err := tc.make(); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if len(d.images) != 1 {
			t.Fatalf("%s created %d images, want 1", tc.name, len(d.images))
		}
		if got := d.images[0].Format; got != tc.want {
			t.Errorf("%s: created %v, want %v (%s)", tc.name, got, tc.want, tc.reason)
		}
	}

	// The glTF loader's own choice, which is per material slot rather than per
	// constructor. Two documents naming one sheet.png, one binding it as base
	// colour and one as a normal map: they must not share (the cache key test
	// covers that) and they must not be uploaded with the same format (nothing
	// covered this).
	dir := t.TempDir()
	writeSheetDocs(t, dir, []string{"sheet.png"}, map[string]string{
		"albedo.gltf": sheetDoc("sheet.png", 0, "albedo"),
		"normal.gltf": sheetDoc("sheet.png", 0, "normal"),
	})
	fsys := os.DirFS(dir)
	g, gd := wideFixture(sampledLinear)
	if _, err := g.LoadGLTF(fsys, "albedo.gltf"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.LoadGLTF(fsys, "normal.gltf"); err != nil {
		t.Fatal(err)
	}
	if len(gd.images) != 2 {
		t.Fatalf("two documents binding one image to two slots created %d images, want 2", len(gd.images))
	}
	if got := gd.images[0].Format; got != srgb {
		t.Errorf("a glTF base colour image was uploaded as %v, want %v", got, srgb)
	}
	if got := gd.images[1].Format; got != unorm {
		t.Errorf("a glTF normal map was uploaded as %v, want %v", got, unorm)
	}
}

// TestWideTextureSamplerOptions is the claim that the wide path reaches the same
// sampler choices the eight-bit constructors are named for, since that is the
// whole of what TextureOptions is.
//
// The mip-chain case is the one that is more than a field copy: a chain is built
// by blitting, so it needs the image created with the extra levels, the extra
// usage flag, a view covering them and MaxLod set to the count -- four places
// one bool has to reach.
//
// BROKEN: pinned `mipmap: false` in TextureOptions.internal. FAILED with
// "linear/repeat/mipmap: created 1 mip levels, want 7", "the view covers 1
// levels, want 7", "MaxLod 0, want 7" and "a mipmapped image was created without
// TRANSFER_SRC, so the blits cannot read it" -- all four places the one bool has
// to reach.
func TestWideTextureSamplerOptions(t *testing.T) {
	const w, h = 64, 4
	bits, _ := halfRamp(w * h)
	for _, tc := range []struct {
		name       string
		opts       TextureOptions
		filter     core1_0.Filter
		address    core1_0.SamplerAddressMode
		mipLevels  int
		mipmapMode core1_0.SamplerMipmapMode
	}{
		{"the zero value", TextureOptions{}, core1_0.FilterNearest, core1_0.SamplerAddressModeClampToEdge, 1, core1_0.SamplerMipmapModeNearest},
		{"linear/clamp", TextureOptions{Filter: FilterLinear}, core1_0.FilterLinear, core1_0.SamplerAddressModeClampToEdge, 1, core1_0.SamplerMipmapModeLinear},
		{"nearest/repeat", TextureOptions{Wrap: WrapRepeat}, core1_0.FilterNearest, core1_0.SamplerAddressModeRepeat, 1, core1_0.SamplerMipmapModeNearest},
		{"linear/repeat/mipmap", TextureOptions{Filter: FilterLinear, Wrap: WrapRepeat, Mipmap: true}, core1_0.FilterLinear, core1_0.SamplerAddressModeRepeat, 7, core1_0.SamplerMipmapModeLinear},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, d := wideFixture(sampledLinear)
			if _, err := r.CreateTextureRGBA16F(bits, w, h, tc.opts); err != nil {
				t.Fatal(err)
			}
			s := d.samplers[0]
			if s.MagFilter != tc.filter || s.MinFilter != tc.filter {
				t.Errorf("%s: sampler filters %v/%v, want %v", tc.name, s.MagFilter, s.MinFilter, tc.filter)
			}
			if s.AddressModeU != tc.address || s.AddressModeV != tc.address || s.AddressModeW != tc.address {
				t.Errorf("%s: address modes %v/%v/%v, want %v", tc.name, s.AddressModeU, s.AddressModeV, s.AddressModeW, tc.address)
			}
			if s.MipmapMode != tc.mipmapMode {
				t.Errorf("%s: mipmap mode %v, want %v", tc.name, s.MipmapMode, tc.mipmapMode)
			}
			if got := d.images[0].MipLevels; got != tc.mipLevels {
				t.Errorf("%s: created %d mip levels, want %d", tc.name, got, tc.mipLevels)
			}
			if got := d.views[0].SubresourceRange.LevelCount; got != tc.mipLevels {
				t.Errorf("%s: the view covers %d levels, want %d", tc.name, got, tc.mipLevels)
			}
			wantLod := float32(0)
			if tc.opts.Mipmap {
				wantLod = float32(tc.mipLevels)
			}
			if s.MaxLod != wantLod {
				t.Errorf("%s: MaxLod %g, want %g", tc.name, s.MaxLod, wantLod)
			}
			if tc.opts.Mipmap && d.images[0].Usage&core1_0.ImageUsageTransferSrc == 0 {
				t.Errorf("%s: a mipmapped image was created without TRANSFER_SRC, so the blits cannot read it", tc.name)
			}
		})
	}
}

// TestWideTextureMipFallbackIsSilent pins the one behaviour on this path that is
// neither honoured nor refused, because docs/agents/textures.md now names it and a
// documented silent behaviour with no check is a sentence nothing holds.
//
// The chain is built by linear blits, so it needs the same optional
// SAMPLED_IMAGE_FILTER_LINEAR bit the linear-filter refusal tests. On a device
// without it, createTexture drops to one level rather than recording an illegal
// blit -- which means Mipmap and Filter differ on the same device and the same
// format: an explicit filter request is an error, and a mip request is quietly
// honoured as far as it can be. That asymmetry is deliberate (a sampler cannot
// silently become something else, a chain can simply be shorter) and it is the
// kind of thing that reads as a bug to whoever meets it next.
//
// BROKEN: made the fallback an error instead, which is the other reasonable
// design and the one this is NOT. FAILED with "Mipmap on a device without linear
// filtering must not error: texture: Mipmap: format R32 Signed Float cannot be
// linearly blitted on this device" -- recorded because a later change that makes
// it an error on purpose should update this test and the page together.
func TestWideTextureMipFallbackIsSilent(t *testing.T) {
	r, d := wideFixture(sampledOnly)
	pix := make([]float32, 64*64)
	if _, err := r.CreateTextureR32F(pix, 64, 64, TextureOptions{Mipmap: true}); err != nil {
		t.Fatalf("Mipmap on a device without linear filtering must not error: %v", err)
	}
	if got := d.images[0].MipLevels; got != 1 {
		t.Errorf("created %d mip levels on a device that cannot linearly blit the format, want the fallback to 1", got)
	}
	if got := d.samplers[0].MaxLod; got != 1 {
		t.Errorf("MaxLod %g, want 1 -- the sampler has to follow the chain that was actually built", got)
	}

	// And the contrast: the same device, the same format, an explicit linear
	// filter. That one IS an error.
	if _, err := r.CreateTextureR32F(pix, 64, 64, TextureOptions{Filter: FilterLinear}); err == nil {
		t.Error("FilterLinear was accepted on the device whose missing bit shortened the chain above")
	}
}

// TestWideTextureExtentIsChecked is the error a caller gets for the mistake this
// path makes easiest: a slice whose length does not match the extent.
//
// It is reported in the caller's own units -- half-floats or float32s -- rather
// than in bytes, and the dimensions are in the message because the two numbers
// that go wrong together are the extent and the stride. The eight-bit
// constructors never checked at all: the staging copy is a `copy`, so a short
// slice left the rest of the image holding whatever the allocation came with and
// a long one was truncated, and both sample.
//
// BROKEN: removed the length comparison from wideExtent, leaving only the
// dimension guard. The upload still failed -- createTexture's byte check caught
// it -- and this FAILED on the message, eight times: "RGBA16F short: error
// \"texture: 32 bytes of pixels for 64x4 at 8 bytes per texel, want 2048\" does
// not mention \"16 half-floats\"" and the same for "want 1024", "4 per texel",
// the long case and the R32F case. That is exactly what wideExtent is for: the
// backstop refuses the upload, and only the units the caller passed tell them
// which of the extent and the slice is the wrong one.
//
// BROKEN: removed createTexture's own byte-length check, which is the backstop
// for the four eight-bit constructors that have no wideExtent in front of them.
// FAILED with "RGBA8 short was accepted" and "a refused upload left 1
// textures" -- a 2x2 texture built from three bytes, uploaded, counted, and
// sampled.
func TestWideTextureExtentIsChecked(t *testing.T) {
	r, _ := wideFixture(sampledLinear)

	for _, tc := range []struct {
		name string
		err  error
		want []string
	}{
		{
			"RGBA16F short", mustFail(r.CreateTextureRGBA16F(make([]uint16, 16), 64, 4, TextureOptions{})),
			[]string{"16 half-floats", "64x4", "want 1024", "4 per texel"},
		},
		{
			"RGBA16F long", mustFail(r.CreateTextureRGBA16F(make([]uint16, 2048), 64, 4, TextureOptions{})),
			[]string{"2048 half-floats", "64x4", "want 1024"},
		},
		{
			"R32F short", mustFail(r.CreateTextureR32F(make([]float32, 3), 8, 8, TextureOptions{})),
			[]string{"3 float32s", "8x8", "want 64", "1 per texel"},
		},
		{
			"zero height", mustFail(r.CreateTextureR32F(nil, 8, 0, TextureOptions{})),
			[]string{"8x0", "no texels"},
		},
		{
			// The backstop, through the constructor that has no wideExtent in
			// front of it.
			"RGBA8 short", mustFail(r.CreateTexture(make([]byte, 3), 2, 2)),
			[]string{"3 bytes", "2x2", "4 bytes per texel", "want 16"},
		},
	} {
		if tc.err == nil {
			t.Errorf("%s was accepted", tc.name)
			continue
		}
		for _, want := range tc.want {
			if !strings.Contains(tc.err.Error(), want) {
				t.Errorf("%s: error %q does not mention %q", tc.name, tc.err, want)
			}
		}
	}
	if counts := r.ResourceCounts(); counts.Textures != 0 {
		t.Errorf("a refused upload left %d textures", counts.Textures)
	}
}

func mustFail(_ *Texture, err error) error { return err }

// TestWideTextureRefusesAnUnsupportedFormat covers the two device answers the
// wide constructors ask for, and the enum a caller can get wrong.
//
// The sampled-image refusal cannot happen on a conformant device -- core Vulkan
// requires it for both formats with optimal tiling -- and it is here anyway,
// because the alternative to failing at the constructor is a descriptor write
// naming an image view the device never had to support. The LINEAR refusal is
// real: SAMPLED_IMAGE_FILTER_LINEAR is mandatory for R16G16B16A16_SFLOAT and
// NOT for R32_SFLOAT, so a linear R32F texture is a thing hardware says no to.
//
// BROKEN: removed the linear-filter check from createWideTexture, which is the
// quiet demotion to nearest this refuses. FAILED with "a linear R32F texture was
// accepted on a device reporting no linear filtering", and with nothing else in
// the package: a sampler created with a filter the format does not support is
// undefined behaviour rather than an error, so there is nothing else to notice
// it.
func TestWideTextureRefusesAnUnsupportedFormat(t *testing.T) {
	bits, _ := halfRamp(4)
	scalars := []float32{1, 2, 3, 4}

	r, _ := wideFixture(core1_0.FormatFeatureStorageImage)
	if _, err := r.CreateTextureRGBA16F(bits, 2, 2, TextureOptions{}); err == nil {
		t.Error("an RGBA16F texture was accepted on a device reporting no sampled-image support")
	} else if !strings.Contains(err.Error(), "cannot be sampled") {
		t.Errorf("error %q does not say the format cannot be sampled", err)
	}

	r, _ = wideFixture(sampledOnly)
	if _, err := r.CreateTextureR32F(scalars, 2, 2, TextureOptions{Filter: FilterLinear}); err == nil {
		t.Error("a linear R32F texture was accepted on a device reporting no linear filtering")
	} else if !strings.Contains(err.Error(), "linear sampling") {
		t.Errorf("error %q does not name linear sampling", err)
	}
	// The same device takes it at nearest, which is the point of refusing rather
	// than failing the format outright.
	if _, err := r.CreateTextureR32F(scalars, 2, 2, TextureOptions{}); err != nil {
		t.Errorf("a nearest R32F texture was refused on a device that supports sampling it: %v", err)
	}

	r, _ = wideFixture(sampledLinear)
	for _, tc := range []struct {
		name string
		opts TextureOptions
		want string
	}{
		{"an unknown filter", TextureOptions{Filter: FilterLinear + 1}, "unknown filter"},
		{"an unknown wrap", TextureOptions{Wrap: WrapRepeat + 1}, "unknown wrap"},
	} {
		if _, err := r.CreateTextureRGBA16F(bits, 2, 2, tc.opts); err == nil {
			t.Errorf("%s was accepted", tc.name)
		} else if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %q does not say %q", tc.name, err, tc.want)
		}
	}
}

// TestWideTextureCountsBindsAndReleases walks one wide texture through the whole
// contract outside the upload itself: it is counted, it binds through
// SetShaderTexture, the flush writes ITS view and sampler, and releasing it
// gives every handle and its descriptor set back.
//
// The descriptor-set count is the part nothing else can see. Vulkan reports
// neither how many sets a pool has handed out nor how many are left, so
// ResourceCounts.DescriptorSets is the only signal that a released texture gave
// its set back.
//
// BROKEN: dropped `r.textures = append(r.textures, tex)` from createTexture.
// FAILED with "Textures = 0 after one upload, want 1" and then, after
// Renderer.Destroy, with the whole balance: "Image: created 2, destroyed 1",
// and the same line for ImageView, DeviceMemory, Sampler and DescriptorSet --
// a texture the renderer's own teardown never sweeps. Eight other tests in the
// package failed with it, which is the shape of a one-line omission in a path
// everything shares.
func TestWideTextureCountsBindsAndReleases(t *testing.T) {
	const w, h = 8, 8
	bits, _ := halfRamp(w * h)
	r, d := wideFixture(sampledLinear)

	// flushShaderTextures falls back to this for every slot nothing is bound to.
	fallback, err := r.CreateTexture(make([]byte, 4), 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	r.fallbackTexture = fallback

	before := r.ResourceCounts()
	tex, err := r.CreateTextureRGBA16F(bits, w, h, TextureOptions{Filter: FilterLinear})
	if err != nil {
		t.Fatal(err)
	}
	after := r.ResourceCounts()
	if after.Textures != before.Textures+1 {
		t.Errorf("Textures = %d after one upload, want %d", after.Textures, before.Textures+1)
	}
	if after.DescriptorSets != before.DescriptorSets+1 {
		t.Errorf("DescriptorSets = %d after one upload, want %d", after.DescriptorSets, before.DescriptorSets+1)
	}

	if err := r.SetShaderTexture(0, tex); err != nil {
		t.Fatal(err)
	}
	d.sampled = nil
	if err := r.flushShaderTextures(0); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, info := range d.sampled {
		if info.ImageView.Handle() == tex.view.Handle() && info.Sampler.Handle() == tex.sampler.Handle() {
			found = true
		}
	}
	if !found {
		t.Error("the flush did not write the wide texture's own view and sampler to a shader-texture slot")
	}

	r.DestroyTexture(tex)
	r.flushAllDeferred()
	if got := r.ResourceCounts(); got.Textures != before.Textures || got.DescriptorSets != before.DescriptorSets {
		t.Errorf("after release: Textures = %d (want %d), DescriptorSets = %d (want %d)",
			got.Textures, before.Textures, got.DescriptorSets, before.DescriptorSets)
	}
	if !tex.destroyed {
		t.Error("a released texture does not read as destroyed")
	}
	// A destroyed texture must not be bindable: the slot would sample a view
	// whose image is gone.
	if err := r.SetShaderTexture(0, tex); err == nil {
		t.Error("SetShaderTexture accepted a destroyed texture")
	}

	r.Destroy()
	textureBalance(t, d.textureTestDriver)
}

// TestWideTextureCreationUnwinds injects a failure at every Vulkan call the wide
// upload acts on and requires that nothing is left created and nothing is left
// counted.
//
// The sites are the same seven the glTF image upload has, because both go
// through one createTexture -- which is the point of there being one: a second
// copy of the 200-line upload for the wide formats would have needed its own
// seven unwinds, and the eight-bit copies drifted exactly that way once already
// (CreateTextureLinear lost the mip chain).
//
// The meta-check at the end breaks the balance for every resource kind, because
// a balance check that has never failed proves nothing.
func TestWideTextureCreationUnwinds(t *testing.T) {
	const w, h = 8, 8
	bits, _ := halfRamp(w * h)

	// The control counts how many times the upload makes each call, so failAt can
	// walk every site rather than only the first. One upload per renderer, so the
	// counts start at zero and the Nth call is the Nth site.
	rc, ctl := wideFixture(sampledLinear)
	if _, err := rc.CreateTextureRGBA16F(bits, w, h, TextureOptions{Filter: FilterLinear}); err != nil {
		t.Fatal(err)
	}

	// CmdCopyBufferToImage and CmdBlitImage are absent on purpose: createTexture
	// discards what the blit returns and the copy returns nothing it reads, so
	// there is no site to unwind. The list is the calls whose failure it acts on.
	for _, call := range []string{"CreateBuffer", "AllocateMemory", "MapMemory", "CreateImage", "CreateImageView", "CreateSampler", "AllocateDescriptorSets"} {
		sites := ctl.calls[call]
		if sites == 0 {
			t.Fatalf("no sites for %s", call)
		}
		for at := 1; at <= sites; at++ {
			t.Run(fmt.Sprintf("%s/%d", call, at), func(t *testing.T) {
				r, d := wideFixture(sampledLinear)
				d.failCall, d.failAt = call, at
				_, err := r.CreateTextureRGBA16F(bits, w, h, TextureOptions{Filter: FilterLinear})
				if !errors.Is(err, errInjected) {
					t.Fatalf("error: %v", err)
				}
				if counts := r.ResourceCounts(); counts.Textures != 0 || counts.DescriptorSets != 0 {
					t.Fatalf("a failed upload left %d textures and %d descriptor sets", counts.Textures, counts.DescriptorSets)
				}
				r.Destroy()
				textureBalance(t, d.textureTestDriver)
			})
		}
		t.Logf("%s: all %d wide-upload sites unwind without leaking a handle", call, sites)
	}

	rm, meta := wideFixture(sampledLinear)
	if _, err := rm.CreateTextureRGBA16F(bits, w, h, TextureOptions{Filter: FilterLinear}); err != nil {
		t.Fatal(err)
	}
	rm.Destroy()
	for kind, n := range meta.created {
		if n == 0 {
			continue
		}
		meta.destroyed[kind]--
		captured := &capturingT{TB: t}
		textureBalance(captured, meta.textureTestDriver)
		meta.destroyed[kind]++
		if !captured.failed {
			t.Fatalf("balance meta-check missed %s", kind)
		}
	}
	t.Log("balance meta-check detects a missing destroy for every wide-upload resource kind")
}

// TestFloat16RoundTrip is the transfer itself, at the places it is wrong when it
// is written from memory: the subnormal range below 2^-14, the round-to-nearest-
// even tie, the largest finite value and the overflow above it.
//
// A table rather than a sweep for the edges, and a sweep for the middle: what a
// sweep cannot do is say which value was expected, and what a table cannot do is
// cover the 0.05 percent claim the wide formats are for.
//
// BROKEN: replaced the normal range's round-to-nearest-even with a plain
// truncation. FAILED with "Float16(65520) = 0x7bff (65504.000000), want 0x7c00
// (+Inf)", "Float16(1.0005) = 0x3c00 (1.000000), want 0x3c01 (1.000977)", and
// the sweep at "half-float worst relative error 0.000974 over 0.001 to 2.0,
// want at most 0.000489" -- 0.000974 against 0.000486, twice the error, which is
// exactly what losing the round costs.
func TestFloat16RoundTrip(t *testing.T) {
	for _, tc := range []struct {
		v    float32
		bits uint16
	}{
		{0, 0x0000},
		{-0, 0x0000},
		{1, 0x3C00},
		{-1, 0xBC00},
		{2, 0x4000},
		{65504, 0x7BFF},        // the largest finite binary16
		{65520, 0x7C00},        // halfway to 65536 rounds up, to infinity
		{70000, 0x7C00},        // overflow
		{-70000, 0xFC00},       //
		{6.1035e-5, 0x0400},    // 2^-14, the smallest normal
		{5.96046e-8, 0x0001},   // 2^-24, the smallest subnormal
		{2.98023e-8, 0x0000},   // 2^-25 is a tie at zero, which is even
		{4.4703484e-8, 0x0001}, // between them, above the tie
		{1.0005, 0x3C01},       // rounds up off the tie
	} {
		if got := Float16(tc.v); got != tc.bits {
			t.Errorf("Float16(%g) = %#04x (%f), want %#04x (%f)", tc.v, got, Float16Value(got), tc.bits, Float16Value(tc.bits))
		}
	}
	if got := Float16(float32(math.Inf(1))); got != 0x7C00 {
		t.Errorf("Float16(+Inf) = %#04x, want 0x7c00", got)
	}
	if got := Float16Value(0x7C00); !math.IsInf(float64(got), 1) {
		t.Errorf("Float16Value(0x7c00) = %g, want +Inf", got)
	}
	if got := Float16(float32(math.NaN())); got != 0x7E00 {
		t.Errorf("Float16(NaN) = %#04x, want a quiet NaN 0x7e00", got)
	}
	if got := Float16Value(Float16(float32(math.NaN()))); !math.IsNaN(float64(got)) {
		t.Errorf("a NaN did not survive the round trip: %g", got)
	}

	// Every binary16 bit pattern that is a finite number decodes and re-encodes
	// to itself. This is what says the two functions are inverses rather than
	// two plausible functions.
	for b := 0; b < 1<<16; b++ {
		h := uint16(b)
		if exp := h >> 10 & 0x1F; exp == 0x1F {
			continue // infinities and NaNs, covered above
		}
		if got := Float16(Float16Value(h)); got != h {
			t.Fatalf("binary16 %#04x decoded to %g and re-encoded to %#04x", h, Float16Value(h), got)
		}
	}

	// And the claim the wide formats are chosen for: across the three decades an
	// eight-bit texture cannot hold, the relative error is a twentieth of a
	// percent. 2^-11 = 0.000488 is half a step of the 10-bit significand, so
	// this is the format and nothing else.
	var worst float64
	var worstAt float32
	for i := range 20001 {
		v := float32(0.001 * math.Pow(2000, float64(i)/20000))
		back := Float16Value(Float16(v))
		if rel := math.Abs(float64(back-v)) / float64(v); rel > worst {
			worst, worstAt = rel, v
		}
	}
	t.Logf("half-float worst relative error over 0.001 to 2.0: %.6f, at %g", worst, worstAt)
	if worst > 0.000489 {
		t.Errorf("half-float worst relative error %.6f over 0.001 to 2.0, want at most 0.000489", worst)
	}
}
