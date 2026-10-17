package glyphengine

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/derekmwright/glyphengine/renderer"
	"github.com/derekmwright/glyphengine/shaders"
)

// Real-device ABI/lifetime check for the extended shared texture layout and a
// mixed graphics/compute timing budget above sixteen. Opt in with the SDK and
// Vulkan available: GLYPHENGINE_RESOURCE_TEST=1 go test . -run SharedResourcesGPU.
func TestSharedResourcesGPU(t *testing.T) {
	if os.Getenv("GLYPHENGINE_RESOURCE_TEST") != "1" {
		t.Skip("set GLYPHENGINE_RESOURCE_TEST=1")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	var messages bytes.Buffer
	previousLog := log.Writer()
	log.SetOutput(&messages)
	defer log.SetOutput(previousLog)
	compile := func(name, source string) []byte {
		path := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(path, []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("glslc", "--target-env=vulkan1.0", path, "-o", path+".spv").CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		out, err := os.ReadFile(path + ".spv")
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	g := &sharedResourceProbe{t: t}
	g.compute = compile("field.comp", `#version 450
layout(local_size_x=8,local_size_y=8) in;
layout(set=2,binding=4,rgba16f) uniform writeonly image2D field;
layout(set=2,binding=12,std140) uniform Value {vec4 value;};
void main(){ivec2 p=ivec2(gl_GlobalInvocationID.xy);if(all(lessThan(p,imageSize(field))))imageStore(field,p,value);}`)
	g.solid = compile("solid.frag", `#version 450
layout(location=0) out vec4 color;void main(){color=vec4(0);}`)
	g.probe = compile("probe.frag", `#version 450
layout(set=1,binding=7) uniform sampler2D t0;
layout(set=1,binding=8) uniform sampler2D t1;
layout(set=1,binding=9) uniform sampler2D t2;
layout(set=1,binding=10) uniform sampler2D t3;
layout(set=1,binding=11) uniform sampler2D t4;
layout(set=2,binding=12,std140) uniform Expected {vec4 expected;};
layout(location=0) out vec4 color;
void main(){vec2 uv=gl_FragCoord.xy/vec2(256,64);
vec4 a=vec4(texture(t0,uv).r,texture(t1,uv).r,texture(t2,uv).r,texture(t3,uv).r);
bool bad=any(greaterThan(abs(a-vec4(16,48,96,192)/255.0),vec4(0.001)))||abs(texture(t4,uv).r-expected.x)>0.001;
color=vec4(bad?1:0,1,0,1);}`)
	e, err := New(g, WithWindowSize(256, 64), WithBackgroundWindow(), WithValidation(true), WithMSAA(1), WithVSync(false), WithShaderTextureSlots(5), WithAppTimingCapacity(32), WithMaxFrames(88))
	if err != nil {
		t.Fatal(err)
	}
	defer e.Destroy()
	e.Run()
	e.Destroy()
	if g.checked != 9 {
		t.Errorf("checked %d states, want 9", g.checked)
	}
	if strings.Contains(messages.String(), "VULKAN ERROR") || strings.Contains(messages.String(), "VULKAN WARNING") {
		t.Fatal(messages.String())
	}
}

type sharedResourceProbe struct {
	t                     *testing.T
	compute, solid, probe []byte
	field                 *renderer.RenderTarget
	producer              *renderer.AppCompute
	receiver              *renderer.AppPass
	extraCompute          []*renderer.AppCompute
	extraGraphics         []*renderer.AppPass
	frame, checked        int
}

func resourceValue(x float32) []byte {
	var b [16]byte
	binary.LittleEndian.PutUint32(b[:], math.Float32bits(x))
	return b[:]
}
func (g *sharedResourceProbe) createField(e *Engine, value float32, history bool) error {
	var err error
	g.field, err = e.Renderer().CreateRenderTarget(renderer.RenderTargetDesc{Name: "shared field", Format: renderer.TargetRGBA16F, Scale: 0.5, Storage: true, History: history, Filter: renderer.FilterLinear})
	if err != nil {
		return err
	}
	g.producer, err = e.Renderer().CreateAppCompute(renderer.AppComputeDesc{Name: "shared field producer", Stage: renderer.StageBeforeScene, Comp: g.compute, Writes: []*renderer.RenderTarget{g.field}, Params: 16, Timed: true})
	if err != nil {
		return err
	}
	g.producer.SetDispatch(16, 4, 1)
	if err = g.producer.SetParams(resourceValue(value)); err != nil {
		return err
	}
	return e.Renderer().SetShaderTarget(4, g.field)
}
func (g *sharedResourceProbe) Init(e *Engine) error {
	r := e.Renderer()
	for i, value := range []byte{16, 48, 96, 192} {
		t, err := r.CreateTextureLinear([]byte{value, value, value, 255}, 1, 1)
		if err != nil {
			return err
		}
		if err = r.SetShaderTexture(i, t); err != nil {
			return err
		}
	}
	if err := g.createField(e, 0.25, false); err != nil {
		return err
	}
	scratch, err := r.CreateRenderTarget(renderer.RenderTargetDesc{Name: "timing scratch", Format: renderer.TargetRGBA16F, Width: 8, Height: 8, Storage: true})
	if err != nil {
		return err
	}
	for i := range 16 {
		name := fmt.Sprintf("extra timer %02d", i)
		if i%2 == 0 {
			p, err := r.CreateAppCompute(renderer.AppComputeDesc{Name: name, Stage: renderer.StageBeforeScene, Comp: g.compute, Writes: []*renderer.RenderTarget{scratch}, Params: 16, Timed: true})
			if err != nil {
				return err
			}
			p.SetDispatch(1, 1, 1)
			p.SetEnabled(false)
			g.extraCompute = append(g.extraCompute, p)
		} else {
			p, err := r.CreateAppPass(renderer.AppPassDesc{Name: name, Stage: renderer.StageBeforeScene, Target: scratch, Fullscreen: true, Vert: shaders.DepthResolveVertSpv, Frag: g.solid, Timed: true})
			if err != nil {
				return err
			}
			p.SetEnabled(false)
			g.extraGraphics = append(g.extraGraphics, p)
		}
	}
	g.receiver, err = r.CreateAppPass(renderer.AppPassDesc{Name: "shared field receiver", Stage: renderer.StageBeforeTonemap, Load: true, Fullscreen: true, Vert: shaders.DepthResolveVertSpv, Frag: g.probe, Params: 16, Timed: true})
	if err != nil {
		return err
	}
	return g.receiver.SetParams(resourceValue(0.25))
}
func (g *sharedResourceProbe) Update(e *Engine, _ float32) {
	if g.frame > 0 && g.frame%8 == 0 {
		im, err := e.Renderer().CaptureFrame()
		if err != nil {
			g.t.Fatal(err)
		}
		bad := 0
		for y := range 64 {
			for x := range 256 {
				c := im.RGBAAt(x, y)
				if c.R > 10 || c.G < 100 {
					bad++
				}
			}
		}
		if g.checked == 8 {
			// Deliberately ask for the wrong value. A blank capture or a probe
			// returning unconditional green must not pass the positive checks.
			if bad != 256*64 {
				g.t.Errorf("negative control detected only %d wrong samples", bad)
			}
		} else if bad != 0 {
			g.t.Errorf("state %d: %d wrong shared texture samples", g.checked, bad)
		}
		seen := map[string]bool{}
		for _, v := range e.Renderer().GPUTimings().App {
			if seen[v.Name] {
				g.t.Errorf("duplicate timer %q", v.Name)
			}
			seen[v.Name] = true
		}
		for i := range 16 {
			if !seen[fmt.Sprintf("extra timer %02d", i)] {
				g.t.Errorf("timer %d missing in state %d", i, g.checked)
			}
		}
		g.checked++
	}
	check := func(err error) {
		if err != nil {
			g.t.Fatal(err)
		}
	}
	switch g.frame {
	case 8:
		check(g.producer.SetParams(resourceValue(0.75)))
		check(g.receiver.SetParams(resourceValue(0.75)))
	case 16:
		e.Renderer().NotifyResize()
	case 24:
		g.producer.SetEnabled(false)
	case 32:
		e.Renderer().DestroyRenderTarget(g.field)
		check(g.createField(e, 0.125, false))
		check(g.receiver.SetParams(resourceValue(0.125)))
	case 40:
		e.Renderer().DestroyRenderTarget(g.field)
		check(g.receiver.SetParams(resourceValue(1)))
	case 48:
		check(g.createField(e, 0.5, true))
		check(g.receiver.SetParams(resourceValue(0.5)))
	case 56:
		for _, p := range g.extraCompute {
			p.SetEnabled(true)
		}
		for _, p := range g.extraGraphics {
			p.SetEnabled(true)
		}
	case 64:
		check(g.receiver.SetParams(resourceValue(0.75)))
	case 72:
		e.Close()
	}
	g.frame++
}

func TestApplicationCapacityOptionsReachRenderer(t *testing.T) {
	var c config
	WithShaderTextureSlots(8)(&c)
	WithAppTimingCapacity(32)(&c)
	r := applyRendererOptions(c.rendererOptions())
	if r.ShaderTextureCapacity() != 8 || r.AppTimingCapacity() != 32 {
		t.Fatal("engine dropped capacity options")
	}
}
