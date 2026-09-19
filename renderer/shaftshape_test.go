package renderer

import (
	"encoding/binary"
	"math"
	"testing"

	"github.com/vkngwrapper/core/v3/core1_0"
)

// TestLightShaftShapeResolve pins the two promises LightShaftShape makes: a
// field left at zero is the default for THAT field and no other, and a value
// that cannot be drawn with becomes one that can instead of reaching the shader.
//
// Verified to fail: with resolve returning its receiver untouched, the zero
// case reports a radius of 0 (which the recorder would divide by), and the
// crossed window reports high <= low, which GLSL's smoothstep leaves undefined.
func TestLightShaftShapeResolve(t *testing.T) {
	d := DefaultLightShaftShape()
	nan := float32(math.NaN())
	inf := float32(math.Inf(1))

	for _, tc := range []struct {
		name string
		in   LightShaftShape
		want LightShaftShape
	}{
		{"zero is the default", LightShaftShape{}, d},
		{"one field set keeps the others", LightShaftShape{Radius: 1.3},
			LightShaftShape{Radius: 1.3, Decay: d.Decay, Threshold: d.Threshold}},
		{"only the window set", LightShaftShape{Threshold: [2]float32{0.3, 0.5}},
			LightShaftShape{Radius: d.Radius, Decay: d.Decay, Threshold: [2]float32{0.3, 0.5}}},
		{"a window that starts at zero is allowed", LightShaftShape{Threshold: [2]float32{0, 0.2}},
			LightShaftShape{Radius: d.Radius, Decay: d.Decay, Threshold: [2]float32{0, 0.2}}},
		{"negative radius and decay", LightShaftShape{Radius: -1, Decay: -0.5}, d},
		{"NaN everywhere", LightShaftShape{Radius: nan, Decay: nan, Threshold: [2]float32{nan, nan}}, d},
		{"infinite radius", LightShaftShape{Radius: inf}, d},
		{"decay above one is a wash, not a runaway", LightShaftShape{Decay: 1.5},
			LightShaftShape{Radius: d.Radius, Decay: 1, Threshold: d.Threshold}},
		{"a negative window edge", LightShaftShape{Threshold: [2]float32{-0.1, 0.5}}, d},
	} {
		if got := tc.in.resolve(); got != tc.want {
			t.Errorf("%s: resolve(%+v) = %+v, want %+v", tc.name, tc.in, got, tc.want)
		}
	}

	// Crossed and touching edges: a hard cut at the lower edge, never a window
	// smoothstep cannot evaluate.
	for _, th := range [][2]float32{{0.5, 0.5}, {0.7, 0.4}} {
		got := LightShaftShape{Threshold: th}.resolve().Threshold
		if got[0] != th[0] || !(got[1] > got[0]) || got[1]-got[0] > 1e-3 {
			t.Errorf("window %v resolved to %v, want a hard cut just above %v", th, got, th[0])
		}
	}
}

// pushCaptureDriver keeps the bytes of every push-constant update made between
// PassShafts' two timestamps, which is the shaft draw's and nothing else's.
type pushCaptureDriver struct {
	*timingDriver
	inShafts bool
	pushes   [][]byte
}

func (d *pushCaptureDriver) CmdWriteTimestamp(cb core1_0.CommandBuffer, st core1_0.PipelineStageFlags, qp core1_0.QueryPool, query int) {
	if Pass((query%queriesPerFrame)/2) == PassShafts {
		d.inShafts = (query%queriesPerFrame)%2 == 0
	}
	d.timingDriver.CmdWriteTimestamp(cb, st, qp, query)
}

func (d *pushCaptureDriver) CmdPushConstants(cb core1_0.CommandBuffer, layout core1_0.PipelineLayout, stages core1_0.ShaderStageFlags, offset int, valueBytes []byte) {
	if d.inShafts {
		d.pushes = append(d.pushes, append([]byte(nil), valueBytes...))
	}
	d.timingDriver.CmdPushConstants(cb, layout, stages, offset, valueBytes)
}

// TestShaftShapeReachesThePushBlock: the shape a game sets is the shape the
// shader reads, slot by slot, and an untouched one reads as the defaults.
//
// The resolve test above cannot see this half. Everything in it passes with
// recordLightShafts still packing the constants it was written with, which is
// how a field can be documented, plumbed through three structs and ignored --
// the state LightShafts itself was in for seven weeks.
//
// Verified to fail that way: with pc[35..37] packed from shaftDecay and
// shaftLobeRadius again, the custom case reports decay 0.96, want 0.9, and the
// lobe reciprocal for 0.9 rather than for 1.3; with pc[38] and pc[39] left
// unpacked, both cases report a window of 0..0.
func TestShaftShapeReachesThePushBlock(t *testing.T) {
	d := DefaultLightShaftShape()
	for _, tc := range []struct {
		name  string
		shape LightShaftShape
		want  LightShaftShape
	}{
		{"untouched", LightShaftShape{}, d},
		{"custom", LightShaftShape{Radius: 1.3, Decay: 0.9, Threshold: [2]float32{0.3, 0.5}},
			LightShaftShape{Radius: 1.3, Decay: 0.9, Threshold: [2]float32{0.3, 0.5}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := buildFrame(61)
			fx.timer = &gpuTimer{supported: true}
			fx.lighting.LightShafts = 0.35
			fx.lighting.ShaftShape = tc.shape
			drv := &pushCaptureDriver{timingDriver: &timingDriver{fakeDriver: &fakeDriver{}}}
			if err := fx.record(drv, 0); err != nil {
				t.Fatalf("record: %v", err)
			}
			if len(drv.pushes) != 1 {
				t.Fatalf("the shafts bracket holds %d push-constant updates, want 1", len(drv.pushes))
			}
			pc := func(i int) float32 {
				return math.Float32frombits(binary.LittleEndian.Uint32(drv.pushes[0][i*4:]))
			}
			aspect := float32(fx.extent.Width) / float32(fx.extent.Height)
			for _, f := range []struct {
				what      string
				got, want float32
			}{
				{"strength", pc(34), 0.35},
				{"decay", pc(35), tc.want.Decay},
				{"lobe reciprocal, x", pc(36), aspect / tc.want.Radius},
				{"lobe reciprocal, y", pc(37), 1 / tc.want.Radius},
				{"window low", pc(38), tc.want.Threshold[0]},
				{"window high", pc(39), tc.want.Threshold[1]},
			} {
				if f.got != f.want {
					t.Errorf("%s: the shader is handed %v, want %v", f.what, f.got, f.want)
				}
			}
		})
	}
}
