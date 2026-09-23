package renderer

import (
	"github.com/derekmwright/glyphengine/renderer/framegraph"
	"github.com/vkngwrapper/core/v3/core1_0"
	"testing"
)

type appDescriptorProbe struct {
	*resizeFakeDriver
	writes int
	set    core1_0.DescriptorSet
	view   core1_0.ImageView
	recent [16]appDescriptorWrite
}

type appDescriptorWrite struct {
	set     core1_0.DescriptorSet
	binding int
	view    core1_0.ImageView
}

func (d *appDescriptorProbe) UpdateDescriptorSets(writes []core1_0.WriteDescriptorSet, _ []core1_0.CopyDescriptorSet) error {
	for _, w := range writes {
		d.recent[d.writes%len(d.recent)] = appDescriptorWrite{w.DstSet, w.DstBinding, w.ImageInfo[0].ImageView}
		d.writes++
		d.set = w.DstSet
		d.view = w.ImageInfo[0].ImageView
	}
	return nil
}

func TestShaderTextureIntentAndFrameSlots(t *testing.T) {
	d := &appDescriptorProbe{resizeFakeDriver: newResizeFakeDriver()}
	h := &d.h
	r := &Renderer{deviceDriver: d, shadow: &shadowResources{}, fallbackTexture: &Texture{view: h.imageView(), sampler: h.sampler()}}
	for i := range r.shadow.descriptorSets {
		r.shadow.descriptorSets[i] = h.descSet()
	}
	target := &RenderTarget{r: r, desc: RenderTargetDesc{History: true}, color: &appImages{textures: []Texture{{view: h.imageView(), sampler: h.sampler()}, {view: h.imageView(), sampler: h.sampler()}}}}
	if err := r.SetShaderTarget(3, target); err != nil {
		t.Fatal(err)
	}
	if d.writes != 0 {
		t.Fatal("setter wrote an in-flight set")
	}
	for f := range maxFramesInFlight {
		target.selectTexture(f)
		if err := r.flushShaderTextures(f); err != nil {
			t.Fatal(err)
		}
		if d.set != r.shadow.descriptorSets[f] || d.view != target.color.textures[1-f].view {
			t.Fatal("write did not select waited frame and previous history instance")
		}
	}
	n := d.writes
	if err := r.flushShaderTextures(1); err != nil {
		t.Fatal(err)
	}
	if n != d.writes {
		t.Fatal("unchanged slots were rewritten")
	}
	if err := r.SetShaderTexture(3, nil); err != nil {
		t.Fatal(err)
	}
	if n != d.writes {
		t.Fatal("nil setter wrote an in-flight set")
	}
	if err := r.flushShaderTextures(0); err != nil {
		t.Fatal(err)
	}
	if d.view != r.fallbackTexture.view || d.set != r.shadow.descriptorSets[0] {
		t.Fatal("nil did not restore the waited slot's fallback")
	}
	if r.SetShaderTarget(4, nil) == nil || r.SetShaderTexture(-1, nil) == nil {
		t.Fatal("out-of-range slot accepted")
	}
	t.Log("setters make zero writes; waited slots select opposite history images; nil restores fallback")
}

type appTimestampProbe struct {
	*fakeDriver
	queries []int
	resets  []int
}

func (d *appTimestampProbe) CmdResetQueryPool(_ core1_0.CommandBuffer, _ core1_0.QueryPool, first, count int) {
	d.resets = append(d.resets, first, count)
}
func (d *appTimestampProbe) CmdWriteTimestamp(_ core1_0.CommandBuffer, _ core1_0.PipelineStageFlags, _ core1_0.QueryPool, q int) {
	d.queries = append(d.queries, q)
}

func TestAppTimingsWriteDisabledEdges(t *testing.T) {
	fx := withAppFrame(buildFrame(7), true)
	var passes []*AppPass
	for i := range fx.graph.nodes {
		if p := fx.graph.nodes[i].app; p != nil {
			p.desc.Timed = true
			passes = append(passes, p)
		}
	}
	fx.timer = &gpuTimer{supported: true, apps: passes}
	for _, enabled := range []bool{true, false} {
		for _, p := range passes {
			p.SetEnabled(enabled)
		}
		d := &appTimestampProbe{fakeDriver: &fakeDriver{}}
		if err := fx.record(d, 1); err != nil {
			t.Fatal(err)
		}
		counts := make(map[int]int)
		for _, q := range d.queries {
			counts[q]++
		}
		for i := 0; i < 2*len(passes); i++ {
			if counts[appQueryBase(1)+i] != 1 {
				t.Fatalf("enabled=%v app query %d written %d times", enabled, i, counts[appQueryBase(1)+i])
			}
		}
		if len(d.resets) != 4 || d.resets[3] != 4 {
			t.Fatalf("app reset range: %v", d.resets)
		}
		t.Logf("enabled=%v: all %d app timestamp edges written once", enabled, 2*len(passes))
	}
}

// A previous-frame write needs visibility even when both sides already use
// ShaderReadOnlyOptimal. In particular the legacy scene's entry dependency
// alone does not cover vertex texture fetches. Removing the producer declaration
// makes this fail with zero barriers for both ordinary and history targets.
func TestAppHistoryVisibility(t *testing.T) {
	for _, history := range []bool{false, true} {
		r := &Renderer{depth: &depthResources{format: core1_0.FormatD32SignedFloat}}
		target := &RenderTarget{r: r, desc: RenderTargetDesc{Name: "previous output", Format: TargetR16F, Scale: 1, History: history}}
		r.appTargets = []*RenderTarget{target}
		f, err := newFrameGraph(core1_0.Samples4, r.depth.format, core1_0.FormatB8G8R8A8SRGB, 3, r)
		if err != nil {
			t.Fatal(err)
		}
		step := f.plan.Steps[f.engine[graphLegacy]-1]
		if len(step.Barriers) != 1 {
			t.Fatalf("history=%v: %d barriers, want previous write visibility", history, len(step.Barriers))
		}
		b := step.Barriers[0]
		if b.Resource != f.targets[target].read || b.SrcAccess&core1_0.AccessColorAttachmentWrite == 0 || b.DstStage&core1_0.PipelineStageVertexShader == 0 || b.NewLayout != core1_0.ImageLayoutShaderReadOnlyOptimal {
			t.Fatalf("history=%v: incomplete visibility: %+v", history, b)
		}
		if f.declarations[0].Kind != framegraph.Legacy {
			t.Fatal("previous submission has no declared producer")
		}
	}
}
