package renderer

import (
	"strings"
	"testing"

	"github.com/derekmwright/glyphengine/renderer/framegraph"
	"github.com/derekmwright/glyphengine/shaders"
	"github.com/vkngwrapper/core/v3/core1_0"
)

// Verified break: disabling SceneColor's stage/target guard accepts both
// color-before-scene and color-feedback; each fails with a missing Reads error.
func TestAppTextureReadValidation(t *testing.T) {
	r := &Renderer{}
	target := &RenderTarget{r: r, desc: RenderTargetDesc{Format: TargetR16F, Scale: 1}}
	target.texture.target = target
	good := AppPassDesc{Name: "texture reader", Stage: StageAfterScene, Target: target, Fullscreen: true, Vert: shaders.DepthResolveVertSpv, Frag: shaders.DepthResolveFragSpv}
	for _, tc := range []struct {
		name string
		edit func(*AppPassDesc)
	}{
		{"color-before-scene", func(d *AppPassDesc) { d.Stage = StageBeforeScene; d.Reads = []*Texture{r.SceneColor()} }},
		{"color-feedback", func(d *AppPassDesc) {
			d.Stage = StageBeforeBloom
			d.Target = nil
			d.Load = true
			d.Reads = []*Texture{r.SceneColor()}
		}},
		{"depth-before-scene", func(d *AppPassDesc) { d.Stage = StageBeforeScene; d.Reads = []*Texture{r.SceneDepth()} }},
		{"target-feedback", func(d *AppPassDesc) { d.Reads = []*Texture{target.Texture()} }},
		{"five-inputs", func(d *AppPassDesc) { d.Reads = make([]*Texture, 5) }},
		{"destroyed", func(d *AppPassDesc) { d.Reads = []*Texture{{destroyed: true}} }},
		{"nil", func(d *AppPassDesc) { d.Reads = []*Texture{nil} }},
		{"foreign-scene", func(d *AppPassDesc) { d.Reads = []*Texture{(&Renderer{}).SceneColor()} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := good
			tc.edit(&d)
			if err := r.validateAppPass(d); err == nil || !strings.Contains(err.Error(), "Reads") {
				t.Fatalf("expected Reads error, got %v", err)
			}
		})
	}
	for _, texture := range []*Texture{r.SceneColor(), r.SceneDepth(), {}} {
		d := good
		d.Reads = []*Texture{texture}
		if err := r.validateAppPass(d); err != nil {
			t.Fatal(err)
		}
	}
	target.desc.History = true
	good.Reads = []*Texture{target.Texture()}
	if err := r.validateAppPass(good); err != nil {
		t.Fatal(err)
	}
	t.Log("explicit scene textures and ordinary textures accepted; stage, feedback, nil and destroyed inputs rejected")
}

// Verified breaks: declaring history's write instance loses its sampled read;
// pinning SceneColor's view to image zero fails image one with a stale view.
func TestAppTextureGraphAndSceneViews(t *testing.T) {
	r := &Renderer{depth: &depthResources{format: core1_0.FormatD32SignedFloat}}
	for _, history := range []bool{false, true} {
		target := &RenderTarget{r: r, desc: RenderTargetDesc{Name: "target", Format: TargetR16F, Scale: 1, History: history}}
		target.texture.target = target
		r.appTargets = append(r.appTargets, target)
	}
	color, depth := r.SceneColor(), r.SceneDepth()
	p := &AppPass{r: r, desc: AppPassDesc{Stage: StageBeforeBloom, Target: r.appTargets[0], Reads: []*Texture{r.appTargets[1].Texture(), color, depth, {}}}}
	r.appPasses = []*AppPass{p}
	f, err := newFrameGraph(core1_0.Samples4, r.depth.format, core1_0.FormatB8G8R8A8SRGB, 2, r)
	if err != nil {
		t.Fatal(err)
	}
	uses := f.declarations[f.appNode(p)].Uses
	if len(uses) != 4 {
		t.Fatalf("got %d uses, want output plus history/color/depth; ordinary texture needs none", len(uses))
	}
	for _, id := range []framegraph.ResourceID{f.targets[r.appTargets[1]].read, f.hdr, f.resolvedDepth} {
		found := false
		for _, u := range uses {
			found = found || u.Resource == id && u.Access == framegraph.SampledRead
		}
		if !found {
			t.Fatalf("missing sampled resource %d in %+v", id, uses)
		}
	}
	h := &fakeHandles{}
	for resize := range 2 {
		r.hdr = &hdrTarget{sampler: h.sampler(), images: []core1_0.Image{h.image(), h.image()}, views: []core1_0.ImageView{h.imageView(), h.imageView()}, sceneSets: []core1_0.DescriptorSet{h.descSet(), h.descSet()}}
		for image := range 2 {
			r.prepareSceneColor(image)
			if r.SceneColor() != color || color.view != r.hdr.views[image] || color.image != r.hdr.images[image] || color.DescriptorSet != r.hdr.sceneSets[image] || color.scene != r {
				t.Fatalf("resize %d image %d: scene texture did not follow its image with a stable pointer", resize, image)
			}
		}
	}
	t.Log("history/color/depth have graph reads; ordinary texture has none; SceneColor pointer survives image selection and replacement")
}

type appSetBindProbe struct {
	*fakeDriver
	sets  [][3]core1_0.DescriptorSet
	count int
	bad   bool
}

func (d *appSetBindProbe) CmdBindDescriptorSets(_ core1_0.CommandBuffer, _ core1_0.PipelineBindPoint, _ core1_0.PipelineLayout, first int, sets []core1_0.DescriptorSet, _ []int) {
	if first != 0 || len(sets) != 3 {
		d.bad = true
	} else {
		copy(d.sets[d.count][:], sets)
	}
	d.count++
}

// Verified breaks: binding pass inputs as the draw texture fails draw zero;
// allocating sets for draws trips the descriptor-allocation assertion; writing
// the opposite frame's input set fails pass zero/binding zero. At 4096 draws
// the restored path allocates zero sets and zero Go objects during the flush.
func TestAppPassReusesTextureSets(t *testing.T) {
	fx := withAppFrame(buildFrame(7), true)
	var mesh, full *AppPass
	for _, n := range fx.graph.nodes {
		if n.app != nil {
			if n.app.desc.Fullscreen {
				full = n.app
			} else {
				mesh = n.app
			}
		}
	}
	r := mesh.r
	d := &appDescriptorProbe{resizeFakeDriver: newResizeFakeDriver()}
	r.deviceDriver, r.depthResolve, r.appTargets = d, nil, nil
	r.shadow = &shadowResources{}
	for i := range r.shadow.descriptorSets {
		r.shadow.descriptorSets[i] = d.h.descSet()
	}
	texture := &Texture{DescriptorSet: d.h.descSet(), view: d.h.imageView(), sampler: d.h.sampler()}
	input := &Texture{view: d.h.imageView(), sampler: d.h.sampler()}
	mesh.desc.Reads, full.desc.Reads = []*Texture{input}, []*Texture{input}
	draws := make([]RenderObject, 4096)
	for i := range draws {
		draws[i] = fx.draws[0]
		draws[i].Texture = texture
	}
	draws[0].Texture = nil
	draws[1].Texture = &Texture{destroyed: true}
	for _, count := range []int{1, len(draws)} {
		mesh.SetDraws(draws[:count])
		for frame := range maxFramesInFlight {
			if err := r.flushShaderTextures(frame); err != nil {
				t.Fatal(err)
			}
			d.writes = 0
			if err := r.flushAppFrameBindings(frame, 0); err != nil {
				t.Fatal(err)
			}
			if d.writes != 8 {
				t.Fatalf("got %d input writes, want 4 per pass", d.writes)
			}
			for i, p := range []*AppPass{mesh, full} {
				for binding := range 4 {
					want := r.fallbackTexture.view
					if binding == 0 {
						want = input.view
					}
					if d.recent[i*4+binding] != (appDescriptorWrite{p.sets[frame], binding, want}) {
						t.Fatalf("pass %d binding %d: input did not use the waited slot and expected texture", i, binding)
					}
				}
			}
		}
	}
	if d.calls["AllocateDescriptorSets"] != 0 {
		t.Fatal("changing mesh draw count allocated descriptor sets")
	}
	allocs := testing.AllocsPerRun(25, func() {
		if err := r.flushAppFrameBindings(1, 0); err != nil {
			panic(err)
		}
	})
	if allocs != 0 {
		t.Fatalf("input flush allocated %.0f times", allocs)
	}
	probe := &appSetBindProbe{fakeDriver: &fakeDriver{}, sets: make([][3]core1_0.DescriptorSet, len(draws)+1)}
	c := &graphFrame{driver: probe, scratch: &fx.scratch, frame: 1, shadowDS: r.shadow.descriptorSets[1], extent: fx.extent, lighting: fx.lighting}
	mesh.record(c)
	full.record(c)
	if probe.bad || probe.count != len(draws)+1 {
		t.Fatalf("bound %d triples, bad=%v", probe.count, probe.bad)
	}
	for i, sets := range probe.sets {
		want0, want2 := texture.DescriptorSet, mesh.sets[1]
		if i < 2 || i == len(draws) {
			want0 = r.fallbackTexture.DescriptorSet
		}
		if i == len(draws) {
			want2 = full.sets[1]
		}
		if sets != [3]core1_0.DescriptorSet{want0, c.shadowDS, want2} {
			t.Fatalf("draw %d: wrong descriptor sets", i)
		}
	}
	t.Log("1 -> 4096 mesh draws: zero descriptor allocations, zero flush allocations; draw texture, light set and pass input set bound independently")
}
