package renderer

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/vkngwrapper/core/v3/common"
	"github.com/vkngwrapper/core/v3/core1_0"
	"github.com/vkngwrapper/core/v3/loader"
)

type targetSamplerProbe struct {
	*resizeFakeDriver
	samplers map[core1_0.Sampler]core1_0.SamplerCreateInfo
	sets     map[core1_0.DescriptorSet]core1_0.DescriptorImageInfo
}

func newTargetSamplerProbe() *targetSamplerProbe {
	return &targetSamplerProbe{newResizeFakeDriver(), make(map[core1_0.Sampler]core1_0.SamplerCreateInfo), make(map[core1_0.DescriptorSet]core1_0.DescriptorImageInfo)}
}

func (d *targetSamplerProbe) CreateSampler(cb *loader.AllocationCallbacks, info core1_0.SamplerCreateInfo) (core1_0.Sampler, common.VkResult, error) {
	s, result, err := d.resizeFakeDriver.CreateSampler(cb, info)
	if err == nil {
		d.samplers[s] = info
	}
	return s, result, err
}

func (d *targetSamplerProbe) UpdateDescriptorSets(writes []core1_0.WriteDescriptorSet, _ []core1_0.CopyDescriptorSet) error {
	for _, w := range writes {
		if w.DstBinding == 0 && len(w.ImageInfo) > 0 {
			d.sets[w.DstSet] = w.ImageInfo[0]
		}
	}
	return nil
}

type targetFormatProbe struct {
	resizeFakeInstanceDriver
	props core1_0.FormatProperties
}

func (d targetFormatProbe) GetPhysicalDeviceFormatProperties(core1_0.PhysicalDevice, core1_0.Format) *core1_0.FormatProperties {
	return &d.props
}

func TestTargetFilterValidation(t *testing.T) {
	for _, tc := range []struct {
		field string
		desc  RenderTargetDesc
	}{
		{"Filter", RenderTargetDesc{Format: TargetR16F, Scale: 1, Filter: -1}},
		{"Filter", RenderTargetDesc{Format: TargetR16F, Scale: 1, Filter: 2}},
		{"Wrap", RenderTargetDesc{Format: TargetR16F, Scale: 1, Wrap: -1}},
		{"Wrap", RenderTargetDesc{Format: TargetR16F, Scale: 1, Wrap: 2}},
	} {
		tc.desc.Name = "invalid sampling"
		_, err := new(Renderer).CreateRenderTarget(tc.desc)
		if err == nil || !strings.Contains(err.Error(), tc.field+":") || !strings.Contains(err.Error(), tc.desc.Name) {
			t.Fatalf("%s validation: %v", tc.field, err)
		}
	}
	for _, format := range []TargetFormat{TargetR16F, TargetRG16F, TargetRGBA16F, TargetR32F, TargetRGBA32F} {
		d := newResizeFakeDriver()
		r := newResizeFixture(d, 3)
		// Linear-tiling and buffer support must not authorize optimal images.
		r.instanceDriver = targetFormatProbe{props: core1_0.FormatProperties{
			LinearTilingFeatures:  core1_0.FormatFeatureSampledImageFilterLinear,
			BufferFeatures:        core1_0.FormatFeatureSampledImageFilterLinear,
			OptimalTilingFeatures: core1_0.FormatFeatureStorageImage,
		}}
		for _, storage := range []bool{false, true} {
			_, err := r.CreateRenderTarget(RenderTargetDesc{Name: "unsupported field", Format: format, Scale: 1, Filter: FilterLinear, Storage: storage})
			vk, _ := targetFormat(format)
			for _, want := range []string{"Filter:", "unsupported field", fmt.Sprint(vk)} {
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Fatalf("error lacks %q: %v", want, err)
				}
			}
		}
		if len(d.created) != 0 || len(r.appTargets) != 0 {
			t.Fatal("unsupported filtering allocated or registered a target")
		}
		nearest, err := r.CreateRenderTarget(RenderTargetDesc{Format: format, Scale: 1})
		if err != nil {
			t.Fatalf("nearest requires no linear feature: %v", err)
		}
		nearest.color.destroy(d)
		assertBalanced(t, d)
	}
}

func TestTargetSamplerOptionsAndResize(t *testing.T) {
	for _, filter := range []TargetFilter{FilterNearest, FilterLinear} {
		for _, wrap := range []TargetWrap{WrapClampToEdge, WrapRepeat} {
			for _, history := range []bool{false, true} {
				for _, storage := range []bool{false, true} {
					t.Run(fmt.Sprintf("filter%d/wrap%d/history%v/storage%v", filter, wrap, history, storage), func(t *testing.T) {
						d := newTargetSamplerProbe()
						r := newResizeFixture(d.resizeFakeDriver, 3)
						r.deviceDriver = d
						r.instanceDriver = targetFormatProbe{props: core1_0.FormatProperties{OptimalTilingFeatures: core1_0.FormatFeatureSampledImageFilterLinear | core1_0.FormatFeatureStorageImage}}
						target, err := r.CreateRenderTarget(RenderTargetDesc{Name: "sampling", Format: TargetR16F, Scale: 0.5, Filter: filter, Wrap: wrap, History: history, Storage: storage})
						if err != nil {
							t.Fatal(err)
						}
						ptr, old := target.Texture(), *target.Texture()
						check := func() {
							t.Helper()
							wantFilter, wantWrap := core1_0.FilterNearest, core1_0.SamplerAddressModeClampToEdge
							if filter == FilterLinear {
								wantFilter = core1_0.FilterLinear
							}
							if wrap == WrapRepeat {
								wantWrap = core1_0.SamplerAddressModeRepeat
							}
							for _, tex := range target.color.textures {
								s := d.samplers[tex.sampler]
								if s.MinFilter != wantFilter || s.MagFilter != wantFilter || s.AddressModeU != wantWrap || s.AddressModeV != wantWrap || s.AddressModeW != core1_0.SamplerAddressModeClampToEdge || s.AnisotropyEnable || s.CompareEnable || s.MinLod != 0 || s.MaxLod != 0 {
									t.Fatalf("sampler: %+v", s)
								}
								if info := d.sets[tex.DescriptorSet]; info.Sampler != tex.sampler || info.ImageView != tex.view {
									t.Fatal("texture descriptor did not follow sampler/view")
								}
							}
							if got := d.sets[target.Texture().DescriptorSet]; got.Sampler != target.color.sampler || got.ImageView != target.Texture().view {
								t.Fatal("selected texture descriptor is stale")
							}
							wantCount := 1
							if history {
								wantCount = 2
							}
							if len(target.color.textures) != wantCount {
								t.Fatal("missing history instance")
							}
						}
						check()
						r.releaseAppResizeTargets()
						r.sc.extent = core1_0.Extent2D{Width: 800, Height: 600}
						var undo rebuildUndo
						if err := r.rebuildAppTargets(&undo); err != nil {
							t.Fatal(err)
						}
						if target.Texture() != ptr || ptr.sampler == old.sampler || ptr.DescriptorSet == old.DescriptorSet || ptr.image == old.image {
							t.Fatal("resize failed to preserve pointer and replace sampler, set and image")
						}
						if w, h := target.Extent(); w != 400 || h != 300 {
							t.Fatalf("extent %dx%d", w, h)
						}
						check()
						undo.unwind()
						assertBalanced(t, d.resizeFakeDriver)
					})
				}
			}
		}
	}
}

func TestTargetSamplerFailureUnwinds(t *testing.T) {
	for _, history := range []bool{false, true} {
		d := newResizeFakeDriver()
		r := newResizeFixture(d, 3)
		r.instanceDriver = targetFormatProbe{props: core1_0.FormatProperties{OptimalTilingFeatures: core1_0.FormatFeatureSampledImageFilterLinear | core1_0.FormatFeatureStorageImage}}
		d.failCall, d.failAt = "CreateSampler", 1
		_, err := r.CreateRenderTarget(RenderTargetDesc{Name: "sampler failure", Format: TargetR16F, Scale: 1, Filter: FilterLinear, Wrap: WrapRepeat, History: history, Storage: true})
		if !errors.Is(err, errInjected) || !strings.Contains(err.Error(), "sampler failure") {
			t.Fatalf("error: %v", err)
		}
		if len(r.appTargets) != 0 || r.graphDirty {
			t.Fatal("failed target was registered")
		}
		if d.created["Image"] == 0 {
			t.Fatal("failure injection did not reach allocated images")
		}
		assertBalanced(t, d)
	}
}

func TestSceneDepthSamplerStaysNearest(t *testing.T) {
	d := newTargetSamplerProbe()
	r := appResizeFixture(d.resizeFakeDriver)
	r.deviceDriver = d
	oldCache := appCacheKeys(r)
	var undo rebuildUndo
	if err := r.rebuildSwapchainTargets(r.sc.extent, &undo); err != nil {
		t.Fatal(err)
	}
	s := d.samplers[r.depthResolve.color.sampler]
	if s.MinFilter != core1_0.FilterNearest || s.MagFilter != core1_0.FilterNearest || s.AddressModeU != core1_0.SamplerAddressModeClampToEdge || s.AddressModeV != core1_0.SamplerAddressModeClampToEdge {
		t.Fatalf("scene depth sampler: %+v", s)
	}
	undo.unwind()
	freeNewAppPasses(r, oldCache)
	assertBalanced(t, d.resizeFakeDriver)
}
