package renderer

import (
	"errors"

	"github.com/derekmwright/glyphengine/shaders"
	"github.com/vkngwrapper/core/v3/common"
	"github.com/vkngwrapper/core/v3/core1_0"
	"github.com/vkngwrapper/core/v3/loader"
	"strings"
	"testing"
)

func (d *resizeFakeDriver) CreateDescriptorSetLayout(_ *loader.AllocationCallbacks, _ core1_0.DescriptorSetLayoutCreateInfo) (core1_0.DescriptorSetLayout, common.VkResult, error) {
	if d.shouldFail("CreateDescriptorSetLayout") {
		return core1_0.DescriptorSetLayout{}, core1_0.VKErrorUnknown, errInjected
	}
	d.created["DescriptorSetLayout"]++
	return d.h.descriptorSetLayout(), core1_0.VKSuccess, nil
}
func (d *resizeFakeDriver) DestroyDescriptorSetLayout(core1_0.DescriptorSetLayout, *loader.AllocationCallbacks) {
	d.destroyed["DescriptorSetLayout"]++
}
func (d *resizeFakeDriver) CreateShaderModule(_ *loader.AllocationCallbacks, _ core1_0.ShaderModuleCreateInfo) (core1_0.ShaderModule, common.VkResult, error) {
	if d.shouldFail("CreateShaderModule") {
		return core1_0.ShaderModule{}, core1_0.VKErrorUnknown, errInjected
	}
	d.created["ShaderModule"]++
	return core1_0.InternalShaderModule(0, loader.VkShaderModule(d.h.n()), 0), core1_0.VKSuccess, nil
}
func (d *resizeFakeDriver) DestroyShaderModule(core1_0.ShaderModule, *loader.AllocationCallbacks) {
	d.destroyed["ShaderModule"]++
}
func (d *resizeFakeDriver) CreatePipelineLayout(_ *loader.AllocationCallbacks, _ core1_0.PipelineLayoutCreateInfo) (core1_0.PipelineLayout, common.VkResult, error) {
	if d.shouldFail("CreatePipelineLayout") {
		return core1_0.PipelineLayout{}, core1_0.VKErrorUnknown, errInjected
	}
	d.created["PipelineLayout"]++
	return d.h.layout(), core1_0.VKSuccess, nil
}
func (d *resizeFakeDriver) DestroyPipelineLayout(core1_0.PipelineLayout, *loader.AllocationCallbacks) {
	d.destroyed["PipelineLayout"]++
}
func (d *resizeFakeDriver) CreateGraphicsPipelines(_ *core1_0.PipelineCache, _ *loader.AllocationCallbacks, infos ...core1_0.GraphicsPipelineCreateInfo) ([]core1_0.Pipeline, common.VkResult, error) {
	for _, info := range infos {
		if info.RenderPass.Handle() != 0 || info.Subpass != 0 {
			panic("graphics pipeline still uses a render pass")
		}
		if _, ok := info.NextOptions.Next.(renderingFormats); !ok {
			panic("missing dynamic rendering pipeline formats")
		}
	}
	if d.shouldFail("CreateGraphicsPipelines") {
		return nil, core1_0.VKErrorUnknown, errInjected
	}
	d.created["Pipeline"]++
	return []core1_0.Pipeline{d.h.pipeline()}, core1_0.VKSuccess, nil
}
func (d *resizeFakeDriver) DestroyPipeline(core1_0.Pipeline, *loader.AllocationCallbacks) {
	d.destroyed["Pipeline"]++
}

func TestAppPassCreationUnwinds(t *testing.T) {
	for _, tc := range []struct {
		call string
		at   int
	}{{"CreateDescriptorSetLayout", 1}, {"CreateShaderModule", 1}, {"CreateShaderModule", 2}, {"CreatePipelineLayout", 1}, {"CreateGraphicsPipelines", 1}, {"AllocateDescriptorSets", 1}} {
		t.Run(tc.call+string(rune('0'+tc.at)), func(t *testing.T) {
			d := newResizeFakeDriver()
			r := newResizeFixture(d, 3)
			r.depth = &depthResources{format: core1_0.FormatD32SignedFloat}
			target, err := r.CreateRenderTarget(RenderTargetDesc{Name: "fixture target", Format: TargetR16F, Scale: 1})
			if err != nil {
				t.Fatal(err)
			}
			clear(d.calls)
			d.failCall, d.failAt = tc.call, tc.at
			_, err = r.CreateAppPass(AppPassDesc{Name: "fixture pass", Stage: StageBeforeScene, Target: target, Fullscreen: true, Vert: shaders.DepthResolveVertSpv, Frag: shaders.DepthResolveFragSpv})
			if !errors.Is(err, errInjected) {
				t.Fatalf("error: %v", err)
			}
			if tc.call == "CreateGraphicsPipelines" && !strings.Contains(err.Error(), "fixture pass") {
				t.Fatal("driver error lost pass name")
			}
			if len(r.appPasses) != 0 {
				t.Fatal("failed pass remained registered")
			}
			r.destroyAppResources()

			for kind, n := range d.created {
				if d.destroyed[kind] != n {
					t.Errorf("%s: created %d destroyed %d", kind, n, d.destroyed[kind])
				}
			}
		})
	}
}
