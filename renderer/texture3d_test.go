package renderer

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/derekmwright/glyphengine/shaders"
	"github.com/vkngwrapper/core/v3/core1_0"
	properties2 "github.com/vkngwrapper/extensions/v3/khr_get_physical_device_properties2"
)

// This file drives CreateTexture3D with no GPU, on widetexture_test.go's
// recording driver plus the two things a volume upload adds that a 2D one never
// needed watching: the blits, whose Z extents are where a chain that floors
// each axis separately differs from one that does not, and the device's 3D
// image limit, which the shared instance fixture reports as zero.

// volumeInstance is wideFormatInstance with a 3D image limit. The shared
// fixtures leave MaxImageDimension3D at its zero value, which would refuse
// every volume; 256 is the smallest a conformant device may report.
type volumeInstance struct {
	wideFormatInstance
	limit int
}

func (i volumeInstance) GetPhysicalDeviceProperties(pd core1_0.PhysicalDevice) (*core1_0.PhysicalDeviceProperties, error) {
	p, err := i.wideFormatInstance.GetPhysicalDeviceProperties(pd)
	if err == nil {
		p.Limits.MaxImageDimension3D = i.limit
	}
	return p, err
}

// volumeDriver records the blits the generated chain issues; the drivers below
// it discard them.
type volumeDriver struct {
	*wideTextureDriver
	blits []core1_0.ImageBlit
}

func (d *volumeDriver) CmdBlitImage(cmd core1_0.CommandBuffer, src core1_0.Image, srcLayout core1_0.ImageLayout, dst core1_0.Image, dstLayout core1_0.ImageLayout, regions []core1_0.ImageBlit, filter core1_0.Filter) error {
	if err := d.wideTextureDriver.CmdBlitImage(cmd, src, srcLayout, dst, dstLayout, regions, filter); err != nil {
		return err
	}
	d.blits = append(d.blits, regions...)
	return nil
}

// blittable is what a conformant device reports for every Texture3DFormat:
// sampled, linearly filtered, and a linear blit source and destination.
const blittable = sampledLinear | core1_0.FormatFeatureBlitSource | core1_0.FormatFeatureBlitDestination

func volumeFixture(features core1_0.FormatFeatureFlags, limit int) (*Renderer, *volumeDriver) {
	r, w := wideFixture(features)
	d := &volumeDriver{wideTextureDriver: w}
	r.deviceDriver = d
	r.instanceDriver = volumeInstance{wideFormatInstance{features: features}, limit}
	return r, d
}

// volume fills a w*h*d volume at texel bytes per texel with a value that
// depends on its position, so a slice copied to the wrong level or offset is
// visible in the staged bytes.
func volume(w, h, d, texel int) []byte {
	pix := make([]byte, w*h*d*texel)
	for i := range pix {
		pix[i] = byte(i*7 + 3)
	}
	return pix
}

// chain is the supplied mip chain below a w*h*d base, halved and floored to one
// on each axis, n levels of it.
func chain(w, h, d, texel, n int) [][]byte {
	var levels [][]byte
	for range n {
		w, h, d = max(w/2, 1), max(h/2, 1), max(d/2, 1)
		levels = append(levels, volume(w, h, d, texel))
	}
	return levels
}

func extent3(w, h, d int) core1_0.Extent3D { return core1_0.Extent3D{Width: w, Height: h, Depth: d} }

// TestTexture3DSuppliedChain is the shape of an upload with an explicit chain:
// a 3D image and view of the right extent and level count, one copy per level
// at the offset the level was staged at, no blit and no TRANSFER_SRC, and the
// staged bytes being every level's bytes in order.
//
// The per-level byte count is checked from each level's extent as derived from
// the level before it, which is the only derivation that is right once an axis
// has reached one. The 64x32x8 case is the one that tells the two apart.
//
// BROKEN: computed each level's expected extent from level 0 and the level
// index (width>>i, height>>i, depth>>i) in checkVolumeChain. FAILED with
// "64x32x8 with the full chain: texture3d: Mips: level 4 holds 32 bytes for
// 4x2x0, want 0 (4 bytes per texel)" -- the floor is lost the moment Z runs out,
// and a level that holds the right bytes is refused as holding too many. The
// 8x8x8 case passed under the break, which is why the non-cubic case is here.
//
// The offset assertions earned their place before any deliberate break: the
// first upload path staged every level correctly and then copied level 1 from
// offset 256 -- the supplied level's own length, not the base's 2048 -- and
// level 2 from 288. This test reported "copy 1: offset 256 extent {4 4 4}
// into level 1, want offset 2048" on its first run, and the validation layer
// would have said nothing -- the offsets are inside the buffer, and a level
// holding a slice of the base is a legal, wrong image.
func TestTexture3DSuppliedChain(t *testing.T) {
	t.Run("8x8x8 with two levels", func(t *testing.T) {
		r, d := volumeFixture(blittable, 256)
		base := volume(8, 8, 8, 4)
		levels := chain(8, 8, 8, 4, 2)
		tex, err := r.CreateTexture3D(base, 8, 8, 8, Texture3DOptions{Format: Texture3DRGBA8, Filter: FilterLinear, Mips: levels})
		if err != nil {
			t.Fatal(err)
		}
		img := d.images[0]
		if img.ImageType != core1_0.ImageType3D {
			t.Errorf("created a %v image, want 3D", img.ImageType)
		}
		if img.Extent != extent3(8, 8, 8) {
			t.Errorf("created extent %+v, want 8x8x8", img.Extent)
		}
		if img.MipLevels != 3 {
			t.Errorf("created %d mip levels, want 3 (the base and two supplied)", img.MipLevels)
		}
		if img.Usage&core1_0.ImageUsageTransferSrc != 0 {
			t.Error("a supplied chain was created with TRANSFER_SRC, which only a generated chain reads through")
		}
		if v := d.views[0]; v.ViewType != core1_0.ImageViewType3D || v.SubresourceRange.LevelCount != 3 {
			t.Errorf("view type %v over %d levels, want 3D over 3", v.ViewType, v.SubresourceRange.LevelCount)
		}
		wantStaging := len(base) + len(levels[0]) + len(levels[1])
		if d.stagingSize[0] != wantStaging {
			t.Errorf("staging buffer is %d bytes, want %d (every level)", d.stagingSize[0], wantStaging)
		}
		staged := append(append(append([]byte{}, base...), levels[0]...), levels[1]...)
		if string(d.mapped[0]) != string(staged) {
			t.Error("the staged bytes are not the base followed by each level")
		}
		if len(d.copies) != 3 {
			t.Fatalf("recorded %d buffer-to-image copies, want 3", len(d.copies))
		}
		for i, want := range []struct {
			offset int
			extent core1_0.Extent3D
		}{{0, extent3(8, 8, 8)}, {2048, extent3(4, 4, 4)}, {2048 + 256, extent3(2, 2, 2)}} {
			c := d.copies[i]
			if c.BufferOffset != want.offset || c.ImageExtent != want.extent || c.ImageSubresource.MipLevel != i {
				t.Errorf("copy %d: offset %d extent %+v into level %d, want offset %d extent %+v into level %d",
					i, c.BufferOffset, c.ImageExtent, c.ImageSubresource.MipLevel, want.offset, want.extent, i)
			}
		}
		if len(d.blits) != 0 {
			t.Errorf("a supplied chain recorded %d blits, want none", len(d.blits))
		}
		if s := d.samplers[0]; s.MaxLod != 3 || s.MipmapMode != core1_0.SamplerMipmapModeLinear {
			t.Errorf("MaxLod %g mipmap mode %v, want 3 and linear: the sampler has to reach the supplied levels", s.MaxLod, s.MipmapMode)
		}
		if tex.Dimensions() != 3 {
			t.Errorf("Dimensions() = %d, want 3", tex.Dimensions())
		}
	})

	t.Run("64x32x8 with the full chain", func(t *testing.T) {
		r, d := volumeFixture(blittable, 256)
		levels := chain(64, 32, 8, 4, 6)
		if _, err := r.CreateTexture3D(volume(64, 32, 8, 4), 64, 32, 8, Texture3DOptions{Format: Texture3DRGBA8, Mips: levels}); err != nil {
			t.Fatalf("64x32x8 with the full chain: %v", err)
		}
		want := []core1_0.Extent3D{extent3(64, 32, 8), extent3(32, 16, 4), extent3(16, 8, 2), extent3(8, 4, 1), extent3(4, 2, 1), extent3(2, 1, 1), extent3(1, 1, 1)}
		for i, c := range d.copies {
			if c.ImageExtent != want[i] {
				t.Errorf("level %d copied at %+v, want %+v", i, c.ImageExtent, want[i])
			}
		}
		// One level more than the chain has is refused by name.
		levels = append(levels, []byte{0, 0, 0, 0})
		_, err := r.CreateTexture3D(volume(64, 32, 8, 4), 64, 32, 8, Texture3DOptions{Format: Texture3DRGBA8, Mips: levels})
		if err == nil || !strings.Contains(err.Error(), "level 7 is past the 1x1x1") {
			t.Errorf("a level past 1x1x1: %v", err)
		}
		// A level of the wrong size names the level, its extent and both counts.
		levels = chain(64, 32, 8, 4, 2)
		levels[1] = levels[1][:100]
		_, err = r.CreateTexture3D(volume(64, 32, 8, 4), 64, 32, 8, Texture3DOptions{Format: Texture3DRGBA8, Mips: levels})
		for _, s := range []string{"level 2 holds 100 bytes", "16x8x2", "want 1024", "4 bytes per texel"} {
			if err == nil || !strings.Contains(err.Error(), s) {
				t.Errorf("short level: error %v does not mention %q", err, s)
			}
		}
	})
}

// TestTexture3DGeneratedChain pins the level count and the extents of a chain
// built by blits, on the volume whose Z axis runs out first: 64x32x8 is seven
// levels, and from level 3 on the blit's Z range is one slice.
//
// It also pins that a volume's chain selects no anisotropy where a 2D one does,
// with the 2D control in the same test so the two cannot drift apart unnoticed.
func TestTexture3DGeneratedChain(t *testing.T) {
	r, d := volumeFixture(blittable, 256)
	r.caps.MaxAnisotropy = 16
	if _, err := r.CreateTexture3D(volume(64, 32, 8, 1), 64, 32, 8, Texture3DOptions{Format: Texture3DR8, Filter: FilterLinear, Mipmap: true}); err != nil {
		t.Fatal(err)
	}
	img := d.images[0]
	if img.MipLevels != 7 {
		t.Errorf("created %d mip levels for 64x32x8, want 7", img.MipLevels)
	}
	if img.Usage&core1_0.ImageUsageTransferSrc == 0 {
		t.Error("a generated chain was created without TRANSFER_SRC, so the blits cannot read it")
	}
	if len(d.copies) != 1 {
		t.Errorf("recorded %d copies, want 1: only the base is uploaded", len(d.copies))
	}
	want := []core1_0.Offset3D{{X: 64, Y: 32, Z: 8}, {X: 32, Y: 16, Z: 4}, {X: 16, Y: 8, Z: 2}, {X: 8, Y: 4, Z: 1}, {X: 4, Y: 2, Z: 1}, {X: 2, Y: 1, Z: 1}, {X: 1, Y: 1, Z: 1}}
	if len(d.blits) != 6 {
		t.Fatalf("recorded %d blits, want 6", len(d.blits))
	}
	for i, b := range d.blits {
		if b.SrcOffsets[1] != want[i] || b.DstOffsets[1] != want[i+1] || b.SrcSubresource.MipLevel != i || b.DstSubresource.MipLevel != i+1 {
			t.Errorf("blit %d: level %d %+v -> level %d %+v, want level %d %+v -> level %d %+v",
				i, b.SrcSubresource.MipLevel, b.SrcOffsets[1], b.DstSubresource.MipLevel, b.DstOffsets[1], i, want[i], i+1, want[i+1])
		}
	}
	s := d.samplers[0]
	if s.MaxLod != 7 || s.MipmapMode != core1_0.SamplerMipmapModeLinear {
		t.Errorf("MaxLod %g mipmap mode %v, want 7 and linear", s.MaxLod, s.MipmapMode)
	}
	if s.AnisotropyEnable || s.MaxAnisotropy != 0 {
		t.Errorf("a volume's sampler enables %gx anisotropy; a chain through a field selects none", s.MaxAnisotropy)
	}

	// The 2D control, on the same renderer with the same anisotropy on offer.
	if _, err := r.CreateTexture(make([]byte, 8*8*4), 8, 8); err != nil {
		t.Fatal(err)
	}
	if s := d.samplers[1]; !s.AnisotropyEnable || s.MaxAnisotropy != 16 {
		t.Errorf("the 2D mipmapped control lost its anisotropy (%v, %gx)", s.AnisotropyEnable, s.MaxAnisotropy)
	}
	if d.images[1].ImageType != core1_0.ImageType2D || d.views[1].ViewType != core1_0.ImageViewType2D || d.images[1].Extent.Depth != 1 {
		t.Error("the 2D control is no longer a 2D image with a 2D view and depth 1")
	}
}

// TestTexture3DAddressingReachesTheSampler is the three-axis claim: each wrap
// field reaches its own sampler axis. The cases are chosen so that no two axes
// agree, because a W that merely followed U would pass any case where they do.
//
// BROKEN: made Texture3DOptions.internal copy addressU into addressW -- the 2D
// path's "W rides along with U" applied to a volume. FAILED with "repeat/repeat/
// clamp: address modes Repeat/Repeat/Repeat, want Repeat/Repeat/Clamp to Edge"
// and "clamp/clamp/repeat: address modes Clamp to Edge/Clamp to Edge/Clamp to
// Edge, want Clamp to Edge/Clamp to Edge/Repeat". The all-repeat case passed, as
// it must under that break.
func TestTexture3DAddressingReachesTheSampler(t *testing.T) {
	const (
		clamp  = core1_0.SamplerAddressModeClampToEdge
		repeat = core1_0.SamplerAddressModeRepeat
	)
	for _, tc := range []struct {
		name    string
		opts    Texture3DOptions
		u, v, w core1_0.SamplerAddressMode
	}{
		{"repeat/repeat/clamp", Texture3DOptions{WrapX: WrapRepeat, WrapY: WrapRepeat}, repeat, repeat, clamp},
		{"clamp/clamp/repeat", Texture3DOptions{WrapZ: WrapRepeat}, clamp, clamp, repeat},
		{"clamp/repeat/clamp", Texture3DOptions{WrapY: WrapRepeat}, clamp, repeat, clamp},
		{"repeat/repeat/repeat", Texture3DOptions{WrapX: WrapRepeat, WrapY: WrapRepeat, WrapZ: WrapRepeat}, repeat, repeat, repeat},
	} {
		r, d := volumeFixture(blittable, 256)
		tc.opts.Format = Texture3DR8
		if _, err := r.CreateTexture3D(volume(4, 4, 4, 1), 4, 4, 4, tc.opts); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		s := d.samplers[0]
		if s.AddressModeU != tc.u || s.AddressModeV != tc.v || s.AddressModeW != tc.w {
			t.Errorf("%s: address modes %v/%v/%v, want %v/%v/%v", tc.name, s.AddressModeU, s.AddressModeV, s.AddressModeW, tc.u, tc.v, tc.w)
		}
	}
}

// TestTexture3DFormatsPinTheirEncoding is the VkFormat and the texel size of each
// Texture3DFormat, through the image and the staging buffer, for the reason
// TestEightBitConstructorsPinTheirEncoding gives: a format and a texel size that
// disagree upload and sample without a word from the validation layer.
func TestTexture3DFormatsPinTheirEncoding(t *testing.T) {
	for _, tc := range []struct {
		format Texture3DFormat
		vk     core1_0.Format
		texel  int
	}{
		{Texture3DR8, core1_0.FormatR8UnsignedNormalized, 1},
		{Texture3DRGBA8, core1_0.FormatR8G8B8A8UnsignedNormalized, 4},
		{Texture3DRGBA16F, core1_0.FormatR16G16B16A16SignedFloat, 8},
	} {
		r, d := volumeFixture(blittable, 256)
		if _, err := r.CreateTexture3D(volume(2, 2, 2, tc.texel), 2, 2, 2, Texture3DOptions{Format: tc.format}); err != nil {
			t.Fatalf("format %d: %v", tc.format, err)
		}
		if d.images[0].Format != tc.vk || d.views[0].Format != tc.vk {
			t.Errorf("format %d: image %v view %v, want %v", tc.format, d.images[0].Format, d.views[0].Format, tc.vk)
		}
		if d.stagingSize[0] != 8*tc.texel {
			t.Errorf("format %d: staged %d bytes for 8 texels, want %d", tc.format, d.stagingSize[0], 8*tc.texel)
		}
	}
}

// TestTexture3DDeviceChecks is every refusal the constructor makes before it
// allocates, with its message pinned and the proof that nothing was created.
//
// BROKEN: removed the MaxImageDimension3D comparison. FAILED six times, for
// the width and the depth case alike: "over the limit: a 257x1x1 volume was
// accepted on a device whose 3D image limit is 256", "over the limit: a refused
// upload created 1 images and 1 staging buffers" and "over the limit: a refused
// upload left 1 textures" -- the image was created at an extent the device never
// promised, which on hardware is a creation error at best and a silent clamp at
// worst. The other ten refusals passed, as they are separate checks.
func TestTexture3DDeviceChecks(t *testing.T) {
	for _, tc := range []struct {
		name     string
		features core1_0.FormatFeatureFlags
		pixels   []byte
		w, h, d  int
		opts     Texture3DOptions
		want     []string
	}{
		{"over the limit", blittable, make([]byte, 257), 257, 1, 1, Texture3DOptions{Format: Texture3DR8},
			[]string{"257x1x1", "3D image limit of 256", "MaxTexture3DExtent"}},
		{"over the limit in depth", blittable, make([]byte, 257), 1, 1, 257, Texture3DOptions{Format: Texture3DR8},
			[]string{"1x1x257", "3D image limit of 256"}},
		{"not sampled", core1_0.FormatFeatureStorageImage, volume(2, 2, 2, 4), 2, 2, 2, Texture3DOptions{Format: Texture3DRGBA8},
			[]string{"cannot be sampled"}},
		{"linear without the bit", sampledOnly, volume(2, 2, 2, 4), 2, 2, 2, Texture3DOptions{Format: Texture3DRGBA8, Filter: FilterLinear},
			[]string{"Filter", "linear sampling"}},
		{"generated chain without blits", sampledLinear, volume(2, 2, 2, 4), 2, 2, 2, Texture3DOptions{Format: Texture3DRGBA8, Mipmap: true},
			[]string{"Mipmap", "cannot be linearly blitted", "supply Mips", "VolumeMipGeneration"}},
		{"both chains", blittable, volume(2, 2, 2, 4), 2, 2, 2, Texture3DOptions{Format: Texture3DRGBA8, Mipmap: true, Mips: chain(2, 2, 2, 4, 1)},
			[]string{"Mips and Mipmap", "not both"}},
		{"short", blittable, make([]byte, 16), 2, 2, 2, Texture3DOptions{Format: Texture3DRGBA8},
			[]string{"16 bytes of pixels", "2x2x2 RGBA8", "want 32", "4 bytes per texel"}},
		{"long half-floats", blittable, make([]byte, 65), 2, 2, 2, Texture3DOptions{Format: Texture3DRGBA16F},
			[]string{"65 bytes of pixels", "RGBA16F", "want 64", "8 bytes per texel"}},
		{"no format", blittable, make([]byte, 8), 2, 2, 2, Texture3DOptions{},
			[]string{"Format", "not a Texture3DFormat"}},
		{"no depth", blittable, nil, 2, 2, 0, Texture3DOptions{Format: Texture3DR8},
			[]string{"2x2x0", "no texels"}},
		{"unknown filter", blittable, make([]byte, 8), 2, 2, 2, Texture3DOptions{Format: Texture3DR8, Filter: FilterLinear + 1},
			[]string{"Filter", "unknown filter"}},
		{"unknown wrap on Z", blittable, make([]byte, 8), 2, 2, 2, Texture3DOptions{Format: Texture3DR8, WrapZ: WrapRepeat + 1},
			[]string{"WrapZ", "unknown wrap"}},
	} {
		r, d := volumeFixture(tc.features, 256)
		_, err := r.CreateTexture3D(tc.pixels, tc.w, tc.h, tc.d, tc.opts)
		if err == nil {
			t.Errorf("%s: a %dx%dx%d volume was accepted on a device whose 3D image limit is 256", tc.name, tc.w, tc.h, tc.d)
		}
		for _, s := range tc.want {
			if err != nil && !strings.Contains(err.Error(), s) {
				t.Errorf("%s: error %q does not mention %q", tc.name, err, s)
			}
		}
		if n := len(d.images) + len(d.stagingSize); n != 0 {
			t.Errorf("%s: a refused upload created %d images and %d staging buffers", tc.name, len(d.images), len(d.stagingSize))
		}
		if counts := r.ResourceCounts(); counts.Textures != 0 {
			t.Errorf("%s: a refused upload left %d textures", tc.name, counts.Textures)
		}
	}

	// The limit is inclusive: a volume exactly at it is accepted.
	r, _ := volumeFixture(blittable, 256)
	if _, err := r.CreateTexture3D(make([]byte, 256), 256, 1, 1, Texture3DOptions{Format: Texture3DR8}); err != nil {
		t.Errorf("a 256x1x1 volume was refused on a device whose limit is 256: %v", err)
	}
}

// TestTexture3DReadsDimensionMismatch is the binding contract at pass creation,
// in both directions and for both kinds of pass: a volume at an index Reads3D
// does not list, and a 2D texture at one it does, are each refused naming the
// index and both dimensions; a Reads3D index outside Reads, or listed twice, is
// refused naming Reads3D; and the matching declaration is accepted.
//
// Textures here are built by hand -- {volume: true} and {} -- because the
// check reads nothing else from them, and because that is also what makes the
// test independent of the upload path it is not about.
//
// BROKEN: removed the Dimensions comparison from validateAppReads. FAILED in
// both directions and both kinds: "graphics/volume where 2D is declared: a
// mismatch was accepted", "graphics/2D where 3D is declared: a mismatch was
// accepted", and the same two lines for compute. The Reads3D range and
// duplicate cases still passed, as they are a separate check.
func TestTexture3DReadsDimensionMismatch(t *testing.T) {
	r := &Renderer{}
	vol, flat := &Texture{volume: true}, &Texture{}
	for _, kind := range []struct {
		name     string
		validate func(reads []*Texture, reads3D []int) error
	}{
		{"graphics", func(reads []*Texture, reads3D []int) error {
			return r.validateAppPass(AppPassDesc{Name: "volume reader", Stage: StageBeforeScene, Target: &RenderTarget{r: r, desc: RenderTargetDesc{Format: TargetR16F, Scale: 1}},
				Fullscreen: true, Vert: shaders.DepthResolveVertSpv, Frag: shaders.DepthResolveFragSpv, Reads: reads, Reads3D: reads3D})
		}},
		{"compute", func(reads []*Texture, reads3D []int) error {
			return r.validateAppCompute(AppComputeDesc{Name: "volume reader", Stage: StageBeforeScene, Comp: computeCode(t), Reads: reads, Reads3D: reads3D})
		}},
	} {
		for _, tc := range []struct {
			name    string
			reads   []*Texture
			reads3D []int
			want    []string
		}{
			{"volume where 2D is declared", []*Texture{flat, vol}, nil,
				[]string{`"volume reader"`, "Reads", "index 1 is a 3D texture", "declared 2D"}},
			{"2D where 3D is declared", []*Texture{vol, flat}, []int{0, 1},
				[]string{`"volume reader"`, "Reads", "index 1 is a 2D texture", "declared 3D"}},
			{"Reads3D past Reads", []*Texture{vol}, []int{1},
				[]string{"Reads3D", "index 1 names no read", "Reads has 1"}},
			{"Reads3D negative", []*Texture{vol}, []int{-1},
				[]string{"Reads3D", "index -1 names no read"}},
			{"Reads3D twice", []*Texture{vol, vol}, []int{0, 0},
				[]string{"Reads3D", "index 0 is listed twice"}},
		} {
			err := kind.validate(tc.reads, tc.reads3D)
			if err == nil {
				t.Errorf("%s/%s: a mismatch was accepted", kind.name, tc.name)
				continue
			}
			for _, s := range tc.want {
				if !strings.Contains(err.Error(), s) {
					t.Errorf("%s/%s: error %q does not mention %q", kind.name, tc.name, err, s)
				}
			}
		}
		for _, tc := range []struct {
			name    string
			reads   []*Texture
			reads3D []int
		}{
			{"a volume declared 3D", []*Texture{vol}, []int{0}},
			{"mixed, each declared", []*Texture{flat, vol, flat, vol}, []int{3, 1}},
			{"no volumes, nothing declared", []*Texture{flat, flat}, nil},
		} {
			if err := kind.validate(tc.reads, tc.reads3D); err != nil {
				t.Errorf("%s/%s: refused: %v", kind.name, tc.name, err)
			}
		}
	}
}

// TestTexture3DCountsBindsAndReleases walks one volume through the contract
// outside the upload: counted, refused by every 2D binding site, written into
// a pass's input set with its own view and sampler, and released with its set
// and its handles given back.
func TestTexture3DCountsBindsAndReleases(t *testing.T) {
	r, d := volumeFixture(blittable, 256)
	fallback, err := r.CreateTexture(make([]byte, 4), 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	r.fallbackTexture = fallback

	before := r.ResourceCounts()
	tex, err := r.CreateTexture3D(volume(8, 8, 8, 1), 8, 8, 8, Texture3DOptions{Format: Texture3DR8, Filter: FilterLinear, Mipmap: true})
	if err != nil {
		t.Fatal(err)
	}
	after := r.ResourceCounts()
	if after.Textures != before.Textures+1 || after.DescriptorSets != before.DescriptorSets+1 {
		t.Errorf("after one volume: Textures %d (want %d), DescriptorSets %d (want %d)",
			after.Textures, before.Textures+1, after.DescriptorSets, before.DescriptorSets+1)
	}

	// The 2D sites. Each refuses before it allocates anything.
	created := d.created["DescriptorSet"] + d.created["Buffer"]
	if err := r.SetShaderTexture(0, tex); err == nil || !strings.Contains(err.Error(), "sampler2D") {
		t.Errorf("SetShaderTexture took a volume: %v", err)
	}
	if _, err := r.BuildTerrainMaterial(fallback, fallback, tex, fallback); err == nil || !strings.Contains(err.Error(), "texture 2") {
		t.Errorf("BuildTerrainMaterial took a volume: %v", err)
	}
	if _, err := r.CreateMaterial(MaterialOptions{Normal: tex}); err == nil || !strings.Contains(err.Error(), "Normal") {
		t.Errorf("CreateMaterial took a volume: %v", err)
	}
	if now := d.created["DescriptorSet"] + d.created["Buffer"]; now != created {
		t.Errorf("the refusals created %d objects before refusing", now-created)
	}
	p := &AppPass{r: r, desc: AppPassDesc{Name: "draws a volume"}, draws: []RenderObject{{Texture: tex}}, sets: []core1_0.DescriptorSet{d.h.descSet(), d.h.descSet()}}
	r.appPasses = []*AppPass{p}
	r.appTargets = nil
	if err := r.flushAppFrameBindings(0, 0); err == nil || !strings.Contains(err.Error(), "draw 0 has a 3D texture") {
		t.Errorf("a mesh draw with a volume texture was flushed: %v", err)
	}
	r.appPasses = nil

	// The one site that takes it: a pass input set, written with the volume's
	// own view and sampler.
	p = &AppPass{r: r, desc: AppPassDesc{Reads: []*Texture{fallback, tex}, Reads3D: []int{1}}}
	d.sampled = nil
	if err := r.flushAppInputs(p, d.h.descSet()); err != nil {
		t.Fatal(err)
	}
	if len(d.sampled) != 4 || d.sampled[1].ImageView != tex.view || d.sampled[1].Sampler != tex.sampler {
		t.Error("the flush did not write the volume's own view and sampler at binding 1")
	}

	r.DestroyTexture(tex)
	if !tex.destroyed || tex.Dimensions() != 3 {
		t.Error("a released volume does not read as destroyed, or forgot its shape")
	}
	r.flushAllDeferred()
	if got := r.ResourceCounts(); got.Textures != before.Textures || got.DescriptorSets != before.DescriptorSets {
		t.Errorf("after release: Textures %d (want %d), DescriptorSets %d (want %d)",
			got.Textures, before.Textures, got.DescriptorSets, before.DescriptorSets)
	}
	r.Destroy()
	textureBalance(t, d.textureTestDriver)
}

// TestTexture3DCreationUnwinds injects a failure at every Vulkan call the upload
// acts on, for a supplied chain and for a generated one, and requires that
// nothing is left created or counted. The meta-check breaks the balance for
// every resource kind, because a balance check that has never failed proves
// nothing.
func TestTexture3DCreationUnwinds(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts Texture3DOptions
	}{
		{"supplied", Texture3DOptions{Format: Texture3DRGBA8, Mips: chain(8, 8, 8, 4, 3)}},
		{"generated", Texture3DOptions{Format: Texture3DRGBA8, Mipmap: true}},
	} {
		create := func(r *Renderer) error {
			_, err := r.CreateTexture3D(volume(8, 8, 8, 4), 8, 8, 8, tc.opts)
			return err
		}
		rc, ctl := volumeFixture(blittable, 256)
		if err := create(rc); err != nil {
			t.Fatal(err)
		}
		for _, call := range []string{"CreateBuffer", "AllocateMemory", "MapMemory", "CreateImage", "CreateImageView", "CreateSampler", "AllocateDescriptorSets"} {
			sites := ctl.calls[call]
			if sites == 0 {
				t.Fatalf("%s: no sites for %s", tc.name, call)
			}
			for at := 1; at <= sites; at++ {
				t.Run(fmt.Sprintf("%s/%s/%d", tc.name, call, at), func(t *testing.T) {
					r, d := volumeFixture(blittable, 256)
					d.failCall, d.failAt = call, at
					if err := create(r); !errors.Is(err, errInjected) {
						t.Fatalf("error: %v", err)
					}
					if counts := r.ResourceCounts(); counts.Textures != 0 || counts.DescriptorSets != 0 {
						t.Fatalf("a failed upload left %d textures and %d descriptor sets", counts.Textures, counts.DescriptorSets)
					}
					r.Destroy()
					textureBalance(t, d.textureTestDriver)
				})
			}
		}

		rm, meta := volumeFixture(blittable, 256)
		if err := create(rm); err != nil {
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
				t.Fatalf("%s: balance meta-check missed %s", tc.name, kind)
			}
		}
	}
}

// volumeCapsInstance is capsTestInstance answering the volume formats' feature
// query with one feature set, which is enough to turn VolumeMipGeneration on and
// off; which format says no is the constructor's business and its own test.
type volumeCapsInstance struct {
	*capsTestInstance
	features core1_0.FormatFeatureFlags
}

func (i volumeCapsInstance) GetPhysicalDeviceFormatProperties(core1_0.PhysicalDevice, core1_0.Format) *core1_0.FormatProperties {
	return &core1_0.FormatProperties{OptimalTilingFeatures: i.features}
}

// TestCapabilitiesReportVolumeSupport is the two volume fields: the 3D extent
// limit copied from the device, and VolumeMipGeneration answering yes only when
// every Texture3DFormat can be linearly blitted -- the same test the constructor
// applies, through the same function.
func TestCapabilitiesReportVolumeSupport(t *testing.T) {
	d := generousDevice()
	d.limits.MaxImageDimension3D = 2048
	for _, tc := range []struct {
		name     string
		features core1_0.FormatFeatureFlags
		want     bool
	}{
		{"blittable", blittable, true},
		{"sampled and filtered, no blit", sampledLinear, false},
		{"blit without linear filtering", sampledOnly | core1_0.FormatFeatureBlitSource | core1_0.FormatFeatureBlitDestination, false},
	} {
		a, err := queryDevice(volumeCapsInstance{d, tc.features}, properties2.ExtensionDriver(nil), core1_0.PhysicalDevice{}, 1)
		if err != nil {
			t.Fatal(err)
		}
		c := negotiateCapabilities(a, core1_0.Samples1)
		if c.VolumeMipGeneration != tc.want {
			t.Errorf("%s: VolumeMipGeneration %v, want %v", tc.name, c.VolumeMipGeneration, tc.want)
		}
		if c.MaxTexture3DExtent != 2048 {
			t.Errorf("%s: MaxTexture3DExtent %d, want 2048", tc.name, c.MaxTexture3DExtent)
		}
	}
	// A device that answered nothing about the formats cannot have promised
	// anything.
	if c := negotiateCapabilities(deviceAnswers{props: &core1_0.PhysicalDeviceProperties{Limits: &core1_0.PhysicalDeviceLimits{}}, features: &core1_0.PhysicalDeviceFeatures{}}, core1_0.Samples1); c.VolumeMipGeneration {
		t.Error("VolumeMipGeneration reported true with no format answers at all")
	}
}
