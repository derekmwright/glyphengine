package renderer

import (
	"encoding/binary"
	"math"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/vkngwrapper/core/v3/core1_0"
)

// pushProbe keeps every push-constant block a recording makes, in order, so
// two recordings of the same frame can be compared word for word.
type pushProbe struct {
	*fakeDriver
	pushes [][64]float32
}

func (d *pushProbe) CmdPushConstants(cb core1_0.CommandBuffer, layout core1_0.PipelineLayout, stages core1_0.ShaderStageFlags, offset int, data []byte) {
	var pc [64]float32
	for i := 0; i < len(data)/4 && offset/4+i < 64; i++ {
		pc[offset/4+i] = math.Float32frombits(binary.LittleEndian.Uint32(data[i*4:]))
	}
	d.pushes = append(d.pushes, pc)
	d.fakeDriver.CmdPushConstants(cb, layout, stages, offset, data)
}

func recordPushes(t *testing.T, fx *frame) [][64]float32 {
	t.Helper()
	d := &pushProbe{fakeDriver: &fakeDriver{}}
	if err := fx.record(d, 1); err != nil {
		t.Fatal(err)
	}
	return d.pushes
}

// The subsurface option is push data and nothing else: the same frame with the
// term on records the same calls, and the only words that move are the four the
// term owns, on the water draws and nowhere else.
//
// The off half is TestRecordCommandBufferStreamIsUnchanged and the other pinned
// stream hashes, whose fixture carries water with no subsurface: they did not
// move when the option arrived.
//
// Verified to fail, both ways. Writing the colour and strength unconditionally
// (no strength test around them) moves the fixture's water pushes off the pinned
// stream: TestRecordCommandBufferStreamIsUnchanged reports "command stream hash
// = 0xd28d06504ec4210d, want 0x8c25083b2f66a28d". Moving pcWaterSubsurfaceColor
// to 48 fails here with "push call 89: slot 48 moved from 1 to 0.3; the
// subsurface term owns only map[44:0.3 45:0.52 46:0.51 55:1.75]".
func TestWaterSubsurfaceChangesOnlyItsPushWords(t *testing.T) {
	off := recordPushes(t, buildFrame(7))

	fx := buildFrame(7)
	water := 0
	for i := range fx.draws {
		if w := fx.draws[i].Water; w != nil {
			w.SubsurfaceColor = [3]float32{0.3, 0.52, 0.51}
			w.SubsurfaceStrength = 1.75
			water++
		}
	}
	if water == 0 {
		t.Fatal("the fixture has no water draw; this test would prove nothing")
	}
	on := recordPushes(t, fx)

	if len(on) != len(off) {
		t.Fatalf("subsurface changed the number of push-constant calls: %d against %d", len(on), len(off))
	}
	// Literal slots rather than the constants: these are where water.frag's
	// block puts pointPos.xyz and ambient.w, which is where the shader reads
	// the term from, and a constant that moved would move both sides of a
	// comparison against itself.
	owned := map[int]float32{44: 0.3, 45: 0.52, 46: 0.51, 55: 1.75}
	changed := 0
	for call := range on {
		if on[call] == off[call] {
			continue
		}
		changed++
		for slot := range on[call] {
			if on[call][slot] == off[call][slot] {
				continue
			}
			want, ok := owned[slot]
			if !ok {
				t.Errorf("push call %d: slot %d moved from %v to %v; the subsurface term owns only %v", call, slot, off[call][slot], on[call][slot], owned)
				continue
			}
			if on[call][slot] != want {
				t.Errorf("push call %d: slot %d is %v, want %v", call, slot, on[call][slot], want)
			}
		}
	}
	if changed != water {
		t.Fatalf("%d push calls changed, want exactly the %d water draws", changed, water)
	}
	t.Logf("%d push calls, %d of them water; only slots 44..46 and 55 moved", len(on), water)
}

// The water draw reuses pointPos.xyz, pointColor.rgb and pointPos.w for its own
// options because water.frag does not read the legacy point light. That is a
// property of the shader source, so the source is what this reads: no call into
// the lit evaluators that read the point light, and every pc.pointPos and
// pc.pointColor read is one of the sanctioned swizzles.
//
// Verified to fail: multiplying the subsurface lobe by pc.pointPos.x reports
// "water shader reads pc.pointPos.x; the water draw pushes its own options
// there", and a call to evalLightingAO inside subsurface() reports the
// evaluator.
func TestWaterPushSlotsAreUnreadByWater(t *testing.T) {
	var src strings.Builder
	for _, name := range []string{"../shaders/include/water_shading.inc", "../shaders/water.vert", "../shaders/include/water_waves.inc", "../shaders/include/water_wave_sum.inc"} {
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		src.Write(b)
	}
	// Comments name evalLightingAO for its conventions; only code counts.
	text := regexp.MustCompile(`//[^
]*`).ReplaceAllString(src.String(), "")
	if regexp.MustCompile(`evalLighting\w*\s*\(`).MatchString(text) {
		t.Fatal("the water shader calls an evalLighting variant, which reads the legacy point light out of the slots the water options are pushed in")
	}
	reads := regexp.MustCompile(`pc\.point(Pos|Color)(\.[a-z]+)?`).FindAllString(text, -1)
	if len(reads) == 0 {
		t.Fatal("found no pointPos/pointColor read at all; the subsurface colour must be read from somewhere, so this pattern no longer matches the shader")
	}
	sanctioned := map[string]bool{
		"pc.pointPos.rgb":   true, // subsurface colour
		"pc.pointPos.w":     true, // foam threshold
		"pc.pointColor.rgb": true, // foam colour
		"pc.pointColor.w":   true, // absorption depth
	}
	for _, r := range reads {
		if !sanctioned[r] {
			t.Errorf("water shader reads %s; the water draw pushes its own options there", r)
		}
	}
}
