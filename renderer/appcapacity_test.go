package renderer

import (
	"fmt"
	"testing"

	"github.com/vkngwrapper/core/v3/core1_0"
)

func TestExtendedShaderTextureBindings(t *testing.T) {
	d := &appDescriptorProbe{resizeFakeDriver: newResizeFakeDriver()}
	h := &d.h
	r := &Renderer{deviceDriver: d, shadow: &shadowResources{}, fallbackTexture: &Texture{view: h.imageView(), sampler: h.sampler()}}
	WithShaderTextureSlots(8)(r)
	for i := range r.shadow.descriptorSets {
		r.shadow.descriptorSets[i] = h.descSet()
	}
	for slot := range 8 {
		if err := r.SetShaderTexture(slot, &Texture{view: h.imageView(), sampler: h.sampler()}); err != nil {
			t.Fatal(err)
		}
	}
	bindings := extendedShadowSetBindings(8)
	for slot := range 8 {
		b := bindings[7+slot]
		if b.Binding != 7+slot || b.DescriptorType != core1_0.DescriptorTypeCombinedImageSampler || b.StageFlags != core1_0.StageVertex|core1_0.StageFragment|core1_0.StageCompute {
			t.Fatalf("slot %d has wrong binding: %+v", slot, b)
		}
	}
	for frame := range maxFramesInFlight {
		if err := r.flushShaderTextures(frame); err != nil {
			t.Fatal(err)
		}
		for slot := range 8 {
			w := d.recent[(d.writes-8+slot)%len(d.recent)]
			if w.binding != 7+slot || w.set != r.shadow.descriptorSets[frame] || w.view != r.shaderTextures[slot].view {
				t.Fatalf("frame %d slot %d descriptor mismatch", frame, slot)
			}
		}
	}
	n := d.writes
	if err := r.SetShaderTexture(7, nil); err != nil {
		t.Fatal(err)
	}
	if d.writes != n {
		t.Fatal("setter modified an in-flight descriptor")
	}
	if err := r.flushShaderTextures(0); err != nil {
		t.Fatal(err)
	}
	if d.writes != n+1 || d.view != r.fallbackTexture.view {
		t.Fatal("extended slot did not restore fallback")
	}
	if r.SetShaderTexture(8, nil) == nil {
		t.Fatal("disabled extension slot accepted")
	}
}

func TestSharedSamplerDeviceLimits(t *testing.T) {
	l := &core1_0.PhysicalDeviceLimits{MaxPerStageDescriptorSamplers: 64, MaxPerStageDescriptorSampledImages: 64, MaxDescriptorSetSamplers: 64, MaxDescriptorSetSampledImages: 64, MaxPerStageResources: 128}
	if n := shaderTextureLimit(l); n != 32 {
		t.Fatalf("limit %d", n)
	}
	l.MaxPerStageDescriptorSamplers = 16
	if n := shaderTextureLimit(l); n != 16-(2+materialTextureBindings+4) {
		t.Fatalf("sampler limit ignored: %d", n)
	}
	l.MaxDescriptorSetSampledImages = 12
	if n := shaderTextureLimit(l); n != 12-(2+materialTextureBindings+4) {
		t.Fatalf("set sampled-image limit ignored: %d", n)
	}
	l.MaxPerStageResources = 1
	if n := shaderTextureLimit(l); n != 0 {
		t.Fatalf("stage resource limit ignored: %d", n)
	}
}

func TestConfigurableAppTimingCapacity(t *testing.T) {
	r := &Renderer{caps: Capabilities{GPUTimestamps: true}}
	WithAppTimingCapacity(32)(r)
	fail := func(field, message string) error { return fmt.Errorf("%s: %s", field, message) }
	wrap := func(field string, err error) error { return fmt.Errorf("%s: %w", field, err) }
	for i := 0; i < 32; i++ {
		if err := r.validateAppTiming(true, fail, wrap); err != nil {
			t.Fatalf("timer %d: %v", i, err)
		}
		r.appPasses = append(r.appPasses, &AppPass{desc: AppPassDesc{Name: fmt.Sprint(i), Timed: true}})
	}
	if r.validateAppTiming(true, fail, wrap) == nil {
		t.Fatal("accepted timer beyond configured capacity")
	}
	d := &appTimestampProbe{fakeDriver: &fakeDriver{}}
	timer := &gpuTimer{capacity: 32, supported: true, apps: r.appPasses}
	used := map[int]bool{}
	for frame := range maxFramesInFlight {
		timer.reset(d, core1_0.CommandBuffer{}, frame)
		for _, p := range r.appPasses {
			timer.beginApp(d, core1_0.CommandBuffer{}, frame, p)
			timer.endApp(d, core1_0.CommandBuffer{}, frame, p)
		}
		for _, q := range d.queries {
			if used[q] {
				t.Fatalf("frame %d reused query %d", frame, q)
			}
			used[q] = true
		}
		d.queries = nil
	}
	if len(used) != 64*maxFramesInFlight {
		t.Fatalf("only %d timestamp edges", len(used))
	}
}
