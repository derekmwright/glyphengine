package renderer

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/go-gl/mathgl/mgl32"
	"github.com/vkngwrapper/core/v3/common"
	"github.com/vkngwrapper/core/v3/core1_0"
	"github.com/vkngwrapper/core/v3/loader"
	"github.com/vkngwrapper/extensions/v3/khr_dynamic_rendering"
)

// shadowDataOffsets is what the Go packer writes where, by GLSL member name.
func shadowDataOffsets() map[string]int {
	return map[string]int{
		"cascadeVP":      0,
		"nightGrade":     nightGradeOffset,
		"skyPalette":     skyPaletteOffset,
		"volumetric":     volumetricOffset,
		"cascadeVPExtra": cascadeExtraOffset,
		"cascadeInfo":    cascadeInfoOffset,
		"cascadeParams":  cascadeParamsOffset,
	}
}

var (
	shadowDataBlock  = regexp.MustCompile(`(?s)uniform ShadowData\s*\{(.*?)\}`)
	shadowDataMember = regexp.MustCompile(`(mat4|vec4)\s+(\w+)\s*(?:\[(\d+)\])?\s*;`)
	lineComment      = regexp.MustCompile(`//[^\n]*`)
)

// std140Members lays out a ShadowData body the way std140 does for the only
// two types the block uses: a vec4 is 16 bytes, a mat4 is four of them, and an
// array of either has that stride. Every member therefore starts on a 16-byte
// boundary with no padding to compute, which is what keeps this small enough
// to trust.
func std140Members(body string) (offsets map[string]int, order []string, size int) {
	offsets = map[string]int{}
	for _, m := range shadowDataMember.FindAllStringSubmatch(lineComment.ReplaceAllString(body, ""), -1) {
		stride := 16
		if m[1] == "mat4" {
			stride = 64
		}
		count := 1
		if m[3] != "" {
			count, _ = strconv.Atoi(m[3])
		}
		offsets[m[2]] = size
		order = append(order, m[2])
		size += stride * count
	}
	return offsets, order, size
}

// shadowDataSources is every file in the repository that declares the block:
// the engine's stages, the sky slot's fillings, the examples and check
// fixtures that stand in for a consumer, and the capability pages, whose
// declarations are what a consumer copies.
func shadowDataSources(t *testing.T) []string {
	t.Helper()
	var files []string
	for _, pattern := range []string{
		"../shaders/*.frag", "../shaders/*.vert", "../shaders/*.comp", "../shaders/include/*.inc",
		"../x/*/*.frag", "../x/*/*/*.frag", "../examples/*/*.frag", "../examples/*/*.comp",
		"../cmd/*/*.frag", "../cmd/*/*.inc", "../cmd/*/*.comp", "../docs/agents/*.md",
	} {
		m, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, m...)
	}
	return files
}

// TestCascadeTailOffsetsMatchGLSL reads every ShadowData declaration in the
// repository and lays it out with std140, then holds each member to the
// offset renderer/shadow.go packs it at.
//
// It is the test for the decision the tail exists to keep: the head --
// cascadeVP[2], nightGrade, the palette and volumetric -- has not moved, so a
// consumer that declares only the head reads what it always read, and the
// cascades past two sit after it. A head-only declaration may stop anywhere at
// or before litUBOHeadSize; a declaration that names any of the tail must name
// all of it and end exactly at litUBOSize, and a shader that declares the tail
// must say so to lighting.inc with SHADOW_CASCADE_TAIL, or it compiles against
// the two-cascade fallback and never sees a third cascade.
//
// Verified break (2026-10-17): moving the extra matrices into the head
// (cascadeUBOSize = MaxShadowCascades*64, cascadeExtraOffset =
// ShadowCascades*64) fails on the Go pins ("Go packs nightGrade at 256, want
// 128") and on every head-only file, e.g. "x/sky/sky.frag: nightGrade at
// std140 offset 128, Go packs it at 256" -- a consumer shader that would read
// the third matrix's first column as its night grade.
func TestCascadeTailOffsetsMatchGLSL(t *testing.T) {
	// Spelled as numbers because that is what a reader holds the GLSL to.
	for name, want := range map[string]int{"cascadeVP": 0, "nightGrade": 128, "skyPalette": 144, "volumetric": 240,
		"cascadeVPExtra": 256, "cascadeInfo": 384, "cascadeParams": 400} {
		if got := shadowDataOffsets()[name]; got != want {
			t.Errorf("Go packs %s at %d, want %d", name, got, want)
		}
	}
	if litUBOHeadSize != 256 || litUBOSize != 464 {
		t.Errorf("head %d bytes, block %d; want 256 and 464", litUBOHeadSize, litUBOSize)
	}

	want := shadowDataOffsets()
	tails, heads := 0, 0
	for _, file := range shadowDataSources(t) {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		blocks := shadowDataBlock.FindAllSubmatch(src, -1)
		for _, block := range blocks {
			offsets, order, size := std140Members(string(block[1]))
			if len(order) == 0 || order[0] != "cascadeVP" {
				t.Errorf("%s: a ShadowData block that does not begin with cascadeVP: %v", file, order)
				continue
			}
			for _, name := range order {
				w, ok := want[name]
				if !ok {
					t.Errorf("%s: ShadowData member %q has no Go offset", file, name)
					continue
				}
				if offsets[name] != w {
					t.Errorf("%s: %s at std140 offset %d, Go packs it at %d", file, name, offsets[name], w)
				}
			}
			_, tail := offsets["cascadeVPExtra"]
			_, info := offsets["cascadeInfo"]
			_, params := offsets["cascadeParams"]
			switch {
			case tail || info || params:
				tails++
				if size != litUBOSize {
					t.Errorf("%s: a block declaring the cascade tail ends at %d, want %d", file, size, litUBOSize)
				}
			default:
				heads++
				if size > litUBOHeadSize {
					t.Errorf("%s: a head-only block ends at %d, past litUBOHeadSize %d", file, size, litUBOHeadSize)
				}
			}
			if strings.HasSuffix(file, ".md") {
				continue
			}
			defines := strings.Contains(string(src), "#define SHADOW_CASCADE_TAIL")
			if (tail || info || params) != defines {
				t.Errorf("%s: declares the tail %v but defines SHADOW_CASCADE_TAIL %v", file, tail, defines)
			}
		}
	}
	// The engine's eight shading stages declare the tail; the sky readers,
	// the custom-passes example and the check fixtures are the consumers that
	// do not. Both populations have to be present for the check to mean
	// anything.
	if tails < 8 || heads < 5 {
		t.Fatalf("checked %d tail and %d head-only declarations; the glob lost files", tails, heads)
	}
	t.Logf("%d tail and %d head-only ShadowData declarations agree with the Go layout", tails, heads)
}

// TestPackLitUBOCascadeTail is the packing half: where a third and fourth
// matrix go, what the count and per-cascade parameters say, and -- the
// compatibility claim -- that the head a two-cascade consumer reads is byte for
// byte the same at three cascades as at two.
func TestPackLitUBOCascadeTail(t *testing.T) {
	var vps [MaxShadowCascades]mgl32.Mat4
	for c := range vps {
		for i := range 16 {
			vps[c][i] = float32(c*100 + i + 1)
		}
	}
	receivers := [MaxShadowCascades]ShadowReceiverRange{{0.25, 0.75}, {}, {0.5, 0.625}, {0.125, 1}}

	two := make([]byte, litUBOSize)
	packLitUBO(two, 2, vps, receivers, nil, nil, nil)
	three := make([]byte, litUBOSize)
	packLitUBO(three, 3, vps, receivers, nil, nil, nil)

	if string(two[:litUBOHeadSize]) != string(three[:litUBOHeadSize]) {
		t.Fatal("the head a two-cascade declaration reads differs between two and three cascades")
	}
	for c := range 2 {
		for i := range 16 {
			if got := f32At(three, c*64+i*4); got != vps[c][i] {
				t.Fatalf("cascade %d element %d = %g at the head, want %g", c, i, got, vps[c][i])
			}
		}
	}
	for i := range 16 {
		if got := f32At(three, cascadeExtraOffset+i*4); got != vps[2][i] {
			t.Fatalf("cascade 2 element %d = %g in the tail, want %g", i, got, vps[2][i])
		}
		// Past the count is zero whatever the caller put there.
		if got := f32At(three, cascadeExtraOffset+64+i*4); got != 0 {
			t.Fatalf("cascade 3 element %d = %g on a three-cascade renderer, want 0", i, got)
		}
		if got := f32At(two, cascadeExtraOffset+i*4); got != 0 {
			t.Fatalf("cascade 2 element %d = %g on a two-cascade renderer, want 0", i, got)
		}
	}
	if got := f32At(three, cascadeInfoOffset); got != 3 {
		t.Fatalf("cascadeInfo.x = %g, want 3", got)
	}
	if got := f32At(two, cascadeInfoOffset); got != 2 {
		t.Fatalf("cascadeInfo.x = %g at the default, want 2", got)
	}
	wantParams := [][4]float32{
		{1, 37.4, 0.25, 0.75},
		{4, 224.9, 0, 1}, // the zero range is the whole volume
		{4, 224.9, 0.5, 0.625},
		{0, 0, 0, 0}, // past the count
	}
	for c, want := range wantParams {
		for i, w := range want {
			if got := f32At(three, cascadeParamsOffset+c*16+i*4); got != w {
				t.Errorf("cascadeParams[%d][%d] = %g, want %g", c, i, got, w)
			}
		}
	}
}

// TestCascadeBiasMatchesHeadOnlyFallback holds three things equal: the two
// bias pairs the shader used before they were data, the pairs a default
// renderer now packs for cascades 0 and 1, and the literals lighting.inc's
// head-only fallback compiles in. The first is the byte-identical default:
// the engine stages read the packed pair where they used to read a constant,
// so the packed pair has to be that constant. The third is the consumer that
// never declared the tail.
//
// Verified break (2026-10-17): giving cascade 1 the old special case's other
// arm (cascadeBiasScale[1], cascadeBiasDepth[1] = 1.0, 37.4) fails with
// "default cascade 1 packs bias (1, 37.4); the shader used (4, 224.9)".
func TestCascadeBiasMatchesHeadOnlyFallback(t *testing.T) {
	historical := [ShadowCascades][2]float32{{1.0, 37.4}, {4.0, 224.9}}

	buf := make([]byte, litUBOSize)
	packLitUBO(buf, ShadowCascades, ComputeCascadeVPs([3]float32{0.3, 0.8, 0.2}, mgl32.Vec3{}), noReceivers, nil, nil, nil)
	for c, want := range historical {
		scale, depth := f32At(buf, cascadeParamsOffset+c*16), f32At(buf, cascadeParamsOffset+c*16+4)
		if scale != want[0] || depth != want[1] {
			t.Errorf("default cascade %d packs bias (%g, %g); the shader used (%g, %g)", c, scale, depth, want[0], want[1])
		}
		near, far := f32At(buf, cascadeParamsOffset+c*16+8), f32At(buf, cascadeParamsOffset+c*16+12)
		if near != 0 || far != 1 {
			t.Errorf("default cascade %d packs receiver band [%g, %g]; the depth test was [0, 1]", c, near, far)
		}
	}

	src, err := os.ReadFile(filepath.Join("..", "shaders", "include", "lighting.inc"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`\(c == 0\) \? vec4\(([^)]*)\) : vec4\(([^)]*)\)`).FindSubmatch(src)
	if m == nil {
		t.Fatal("lighting.inc no longer has the head-only fallback this test reads")
	}
	for c, literal := range [][]byte{m[1], m[2]} {
		var v [4]float32
		for i, f := range strings.Split(string(literal), ",") {
			x, err := strconv.ParseFloat(strings.TrimSpace(f), 32)
			if err != nil || i >= 4 {
				t.Fatalf("fallback literal %q: %v", literal, err)
			}
			v[i] = float32(x)
		}
		if v != [4]float32{historical[c][0], historical[c][1], 0, 1} {
			t.Errorf("head-only fallback for cascade %d is %v; want bias %v and the whole volume", c, v, historical[c])
		}
	}
}

// layerRecordingDriver is the resize fixture's driver with the two creation
// arguments this file is about written down: how many layers each image has
// and how many each view covers.
type layerRecordingDriver struct {
	*resizeFakeDriver
	imageLayers, viewLayers []int
}

func (d *layerRecordingDriver) CreateImage(cb *loader.AllocationCallbacks, o core1_0.ImageCreateInfo) (core1_0.Image, common.VkResult, error) {
	d.imageLayers = append(d.imageLayers, o.ArrayLayers)
	return d.resizeFakeDriver.CreateImage(cb, o)
}

func (d *layerRecordingDriver) CreateImageView(cb *loader.AllocationCallbacks, o core1_0.ImageViewCreateInfo) (core1_0.ImageView, common.VkResult, error) {
	d.viewLayers = append(d.viewLayers, o.SubresourceRange.LayerCount)
	return d.resizeFakeDriver.CreateImageView(cb, o)
}

func newCascadeMapFixture(n int) (*shadowResources, *layerRecordingDriver, core1_0.PhysicalDevice) {
	d := &layerRecordingDriver{resizeFakeDriver: newResizeFakeDriver()}
	return &shadowResources{cascades: n, format: core1_0.FormatD32SignedFloat}, d, d.h.physicalDevice()
}

// TestShadowCascadeLayers pins the allocation profile per count: one image
// per frame in flight with exactly n layers, n attachment views and one array
// view of n layers each, n rendering targets and nothing past them. The
// default is the profile every renderer had before the count was an option --
// two images of two layers and six views -- and three and four add one and
// two layers to each image and one and two views per frame, nothing else.
//
// Verified break (2026-10-17): allocating ArrayLayers: ShadowCascades fails
// with "n=3: an image of 2 layers".
func TestShadowCascadeLayers(t *testing.T) {
	for _, n := range []int{2, 3, 4} {
		s, d, pd := newCascadeMapFixture(n)
		if err := s.createSunCascadeMaps(resizeFakeInstanceDriver{}, d, pd); err != nil {
			t.Fatal(err)
		}
		if got := d.created["Image"]; got != maxFramesInFlight {
			t.Errorf("n=%d: %d images, want %d", n, got, maxFramesInFlight)
		}
		for _, layers := range d.imageLayers {
			if layers != n {
				t.Errorf("n=%d: an image of %d layers", n, layers)
			}
		}
		if got, want := d.created["ImageView"], maxFramesInFlight*(n+1); got != want {
			t.Errorf("n=%d: %d views, want %d", n, got, want)
		}
		arrays := 0
		for _, layers := range d.viewLayers {
			if layers == n {
				arrays++
			} else if layers != 1 {
				t.Errorf("n=%d: a view of %d layers", n, layers)
			}
		}
		if arrays != maxFramesInFlight {
			t.Errorf("n=%d: %d array views of all %d layers, want %d", n, arrays, n, maxFramesInFlight)
		}
		for f := range maxFramesInFlight {
			for c := range MaxShadowCascades {
				if (s.targets[f][c] != nil) != (c < n) {
					t.Errorf("n=%d: frame %d cascade %d target present = %v", n, f, c, s.targets[f][c] != nil)
				}
			}
		}
		// The fake sizes every image at 4096 bytes; the real driver's answer
		// is what a game reads.
		if want := maxFramesInFlight * 4096; s.mapBytes != want {
			t.Errorf("n=%d: mapBytes %d, want %d", n, s.mapBytes, want)
		}
		s.destroySunCascadeMaps(d)
		assertBalanced(t, d.resizeFakeDriver)
		t.Logf("n=%d: %d images of %d layers, %d views", n, d.created["Image"], n, d.created["ImageView"])
	}
	s, d, pd := newCascadeMapFixture(MaxShadowCascades + 1)
	if err := s.createSunCascadeMaps(resizeFakeInstanceDriver{}, d, pd); err == nil || d.created["Image"] != 0 {
		t.Fatalf("five cascades: err %v after %d images", err, d.created["Image"])
	}
}

// TestShadowCascadeMapsUnwind fails every creation call the sun arrays make,
// once per site and at every count, and requires the teardown
// createShadowResources runs on failure to give everything back. The
// meta-check then removes one destroy of each kind and requires the balance to
// notice, because a balance check that has never failed proves nothing.
func TestShadowCascadeMapsUnwind(t *testing.T) {
	for _, n := range []int{2, 3, 4} {
		s, control, pd := newCascadeMapFixture(n)
		if err := s.createSunCascadeMaps(resizeFakeInstanceDriver{}, control, pd); err != nil {
			t.Fatal(err)
		}
		s.destroySunCascadeMaps(control)
		assertBalanced(t, control.resizeFakeDriver)
		for _, call := range []string{"CreateImage", "AllocateMemory", "CreateImageView"} {
			sites := control.calls[call]
			if sites == 0 {
				t.Fatalf("n=%d: no %s sites", n, call)
			}
			for at := 1; at <= sites; at++ {
				s, d, pd := newCascadeMapFixture(n)
				d.failCall, d.failAt = call, at
				err := s.createSunCascadeMaps(resizeFakeInstanceDriver{}, d, pd)
				if !errors.Is(err, errInjected) {
					t.Fatalf("n=%d %s %d: %v", n, call, at, err)
				}
				s.destroySunCascadeMaps(d)
				assertBalanced(t, d.resizeFakeDriver)
			}
			t.Logf("n=%d %s: all %d sites unwind without leaks", n, call, sites)
		}
		for kind, count := range control.created {
			if count == 0 {
				continue
			}
			control.destroyed[kind]--
			captured := &capturingT{TB: t}
			assertBalanced(captured, control.resizeFakeDriver)
			control.destroyed[kind]++
			if !captured.failed {
				t.Fatalf("n=%d: balance meta-check missed %s", n, kind)
			}
		}
	}
}

// cascadeFrame is buildFrame(97) on a renderer with n cascades: the extra
// layers' targets and a frame graph that declares n layers. Handles for the
// extra targets come from a separate counter so the default frame's handles,
// and with them its pinned stream hash, are untouched.
func cascadeFrame(n int) *frame {
	fx := buildFrame(97)
	h := &fakeHandles{next: 90000}
	fx.shadow.cascades = n
	for f := range maxFramesInFlight {
		for c := ShadowCascades; c < n; c++ {
			fx.shadow.targets[f][c] = h.depthTarget(ShadowMapSize, c)
		}
	}
	g, err := newFrameGraph(core1_0.Samples4, core1_0.FormatD32SignedFloat, core1_0.FormatB8G8R8A8SRGB, 1, false, n)
	if err != nil {
		panic(err)
	}
	fx.graph = g
	g.sizeScratch(&fx.scratch)
	fx.initGraphBindings()
	return fx
}

// cascadePassCounter counts the sun cascade passes a frame enters and the
// draws recorded inside each.
type cascadePassCounter struct {
	*fakeDriver
	inPass bool
	draws  []int
}

func (d *cascadePassCounter) CmdBeginRendering(cb core1_0.CommandBuffer, o khr_dynamic_rendering.RenderingInfo) error {
	d.inPass = o.DepthAttachment != nil && o.RenderArea.Extent.Width == ShadowMapSize && len(o.ColorAttachments) == 0
	if d.inPass {
		d.draws = append(d.draws, 0)
	}
	return d.fakeDriver.CmdBeginRendering(cb, o)
}

func (d *cascadePassCounter) CmdEndRendering(cb core1_0.CommandBuffer) {
	d.inPass = false
	d.fakeDriver.CmdEndRendering(cb)
}

func (d *cascadePassCounter) CmdDraw(cb core1_0.CommandBuffer, vertexCount, instanceCount int, firstVertex, firstInstance uint32) {
	if d.inPass {
		d.draws[len(d.draws)-1]++
	}
	d.fakeDriver.CmdDraw(cb, vertexCount, instanceCount, firstVertex, firstInstance)
}

func (d *cascadePassCounter) CmdDrawIndexed(cb core1_0.CommandBuffer, indexCount, instanceCount int, firstIndex uint32, vertexOffset int, firstInstance uint32) {
	if d.inPass {
		d.draws[len(d.draws)-1]++
	}
	d.fakeDriver.CmdDrawIndexed(cb, indexCount, instanceCount, firstIndex, vertexOffset, firstInstance)
}

// TestShadowPassRecordsEveryCascade is the work half of the profile: n cascade
// passes, each drawing the casters its volume holds, so three and four add one
// and two passes of the same casters to the default. The fixture's cascade
// matrices are all equal, which makes every pass hold the same casters and
// the per-pass counts comparable. The static, skinned and instanced casters
// in buildFrame all go through this loop, and LOD and impostor sets reach it
// as instance sets, so each lands in every layer.
//
// The default is also held to the stream TestMigrationDrawStreams pins for
// buildFrame(97), with the count written explicitly rather than left zero.
//
// Verified break (2026-10-17): recording `range ShadowCascades` instead of
// `range shadow.cascadeCount()` fails with "n=3: 2 cascade passes, want 3".
func TestShadowPassRecordsEveryCascade(t *testing.T) {
	base := &fakeDriver{hashing: true, drawsOnly: true}
	if err := cascadeFrame(ShadowCascades).record(base, 1); err != nil {
		t.Fatal(err)
	}
	if base.calls != 3293 || uint64(base.h) != 0xe0799690a2c563cc {
		t.Fatalf("explicit two-cascade stream %d %#x; want the pinned 3293 0xe0799690a2c563cc", base.calls, base.h)
	}

	var perPass int
	for _, n := range []int{2, 3, 4} {
		fx := cascadeFrame(n)
		d := &cascadePassCounter{fakeDriver: &fakeDriver{}}
		if err := fx.record(d, 1); err != nil {
			t.Fatal(err)
		}
		if len(d.draws) != n {
			t.Fatalf("n=%d: %d cascade passes, want %d", n, len(d.draws), n)
		}
		if n == 2 {
			perPass = d.draws[0]
		}
		for c, draws := range d.draws {
			if draws == 0 || draws != perPass {
				t.Errorf("n=%d: cascade %d recorded %d draws, want %d like every other", n, c, draws, perPass)
			}
		}
		t.Logf("n=%d: %d passes of %d draws", n, len(d.draws), perPass)
	}

	// Shadows off still clears every layer and draws nothing into any.
	fx := cascadeFrame(3)
	fx.lighting.ShadowEnabled = false
	d := &cascadePassCounter{fakeDriver: &fakeDriver{}}
	if err := fx.record(d, 0); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(d.draws) != "[0 0 0]" {
		t.Fatalf("shadows off at n=3: passes %v, want three empty ones", d.draws)
	}
}

// TestShadowCascadeRecordingAllocates nothing per frame at the largest count,
// for the reason TestRecordCommandBufferAllocsAreConstant gives at the
// default: a per-cascade slice or closure would cost every frame of every game
// that asked for the extra tiers.
//
// Verified break (2026-10-17): appending each cascade index to a fresh slice
// at the top of the cascade loop fails with "4 allocs per frame at 4
// cascades".
func TestShadowCascadeRecordingAllocatesNothing(t *testing.T) {
	fx := cascadeFrame(MaxShadowCascades)
	d := &fakeDriver{}
	if err := fx.record(d, 0); err != nil {
		t.Fatal(err)
	}
	allocs := testing.AllocsPerRun(20, func() {
		if err := fx.record(d, 0); err != nil {
			t.Fatal(err)
		}
	})
	if allocs != 0 {
		t.Fatalf("%v allocs per frame at %d cascades", allocs, MaxShadowCascades)
	}

	buf := make([]byte, litUBOSize)
	vps := ComputeCascadeVPs([3]float32{0.3, 0.8, 0.2}, mgl32.Vec3{})
	if allocs := testing.AllocsPerRun(20, func() {
		packLitUBO(buf, MaxShadowCascades, vps, noReceivers, nil, nil, nil)
	}); allocs != 0 {
		t.Fatalf("packLitUBO: %v allocs per frame", allocs)
	}
}

// The frame graph's sun-map declaration names every layer, so a barrier it
// derives for a compute dispatch that reads the cascades covers the third and
// fourth as well as the first two.
//
// Verified break (2026-10-17): declaring Layers from ShadowCascades fails with
// "n=3: sun shadow maps declared with 2 layers".
func TestFrameGraphDeclaresEveryCascadeLayer(t *testing.T) {
	for _, n := range []int{2, 3, 4} {
		f, err := newFrameGraph(core1_0.Samples4, core1_0.FormatD32SignedFloat, core1_0.FormatB8G8R8A8SRGB, 1, false, n)
		if err != nil {
			t.Fatal(err)
		}
		if got := f.plan.Resources[f.sunShadow].Desc.Layers; got != uint32(n) {
			t.Fatalf("n=%d: sun shadow maps declared with %d layers", n, got)
		}
	}
}

// consumerCoverage is the planet configuration the extra tiers were asked
// for: 90 m, 3 km and 120 km, each with 200 km of caster reach toward the
// light and 120 km away, and the two finer tiers holding their receivers to
// their own radius along the light.
func consumerCoverage() ShadowCoverage {
	return ShadowCoverage{Count: 3, Cascades: [MaxShadowCascades]ShadowCascadeCoverage{
		{Radius: 90, TowardLight: 200000, AwayFromLight: 120000, ReceiverDepth: 90},
		{Radius: 3000, TowardLight: 200000, AwayFromLight: 120000, ReceiverDepth: 3000},
		{Radius: 120000, TowardLight: 200000, AwayFromLight: 120000},
	}}
}

// shadowCascadeFor is calcShadow's selection on the CPU: the first cascade
// whose XY bounds, with the PCF margin, and whose receiver band contain the
// position. TestSelectionMirrorMatchesShader holds the shader to the same
// comparison.
func shadowCascadeFor(p mgl32.Vec3, count int, vps [MaxShadowCascades]mgl32.Mat4, receivers [MaxShadowCascades]ShadowReceiverRange) int {
	for c := range count {
		ls := vps[c].Mul4x1(p.Vec4(1))
		x, y, z := ls[0]*0.5+0.5, ls[1]*0.5+0.5, ls[2]
		near, far := receivers[c].bounds()
		if x < 0.01 || x > 0.99 || y < 0.01 || y > 0.99 || z < near || z > far {
			continue
		}
		return c
	}
	return -1
}

// The mirror above is only worth what its anchor is: the shader must make the
// same comparison against the same packed operands.
//
// Verified break (2026-10-17): putting the old `proj.z < 0.0 || proj.z > 1.0`
// back in lighting.inc fails with "lighting.inc no longer contains ...".
func TestSelectionMirrorMatchesShader(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "shaders", "include", "lighting.inc"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"proj.x < 0.01 || proj.x > 0.99 || proj.y < 0.01 || proj.y > 0.99 || proj.z < params.z || proj.z > params.w",
		"vec4 params = shadowCascadeParams(c);",
		"return shadow.cascadeParams[c];",
		"return min(int(shadow.cascadeInfo.x), MaxShadowCascades);",
		// The head loop's constant bound is what keeps default frames
		// byte-identical; see calcShadow. This only holds the text -- the
		// fixed-clock captures against af52592 are what showed that one loop
		// to the count moved pixels.
		"for (int c = 0; c < NumCascades; c++) {\n        if (shadowCascadeLookup(c, shadowPos, NdotL, lit)) {",
	} {
		if !strings.Contains(string(src), want) {
			t.Errorf("lighting.inc no longer contains %q", want)
		}
	}
}

// TestReceiverDepthKeepsFarReceiversOffTheNearMap is the selection rule's
// measurement on the consumer's radii, and the reason the rule exists.
//
// With the depth test alone, a receiver 10 km down-sun of the centre and a
// few metres off the light axis is selected by the 90 m map: it projects
// inside the map's XY bounds, and 200 km of caster depth puts it inside the
// depth range too. That is the issue's failure, shown here as arithmetic
// rather than argued. With the receiver bands it falls through the 90 m and
// 3 km tiers -- it is past both ReceiverDepths -- to the 120 km one.
//
// The near receiver is the other side of the same rule: 150 m from the centre
// along the light and 40 m off it, inside the 90 m map's XY bounds but past
// its receiver depth, it is shadowed by the 3 km tier rather than left with no
// cascade at all. And a receiver 20 m away stays on the 90 m map.
//
// Verified break (2026-10-17): making ReceiverRanges return the zero array --
// the rule removed -- fails with "far receiver 10 km down-sun selected cascade
// 0, want 2".
func TestReceiverDepthKeepsFarReceiversOffTheNearMap(t *testing.T) {
	sun := mgl32.Vec3{0.8, 0.25, 0}.Normalize()
	coverage := consumerCoverage()
	if err := coverage.ValidateFor(3); err != nil {
		t.Fatal(err)
	}
	vps, err := ComputeCascadeVPsWithCoverage(sun, mgl32.Vec3{}, coverage)
	if err != nil {
		t.Fatal(err)
	}
	receivers := coverage.ReceiverRanges()
	side := sun.Cross(mgl32.Vec3{0, 0, 1}).Normalize()

	far := sun.Mul(-10000).Add(side.Mul(5))
	if got := shadowCascadeFor(far, 3, vps, noReceivers); got != 0 {
		t.Fatalf("control: with the depth test alone the far receiver selected %d; the 90 m map was expected to take it", got)
	}
	if got := shadowCascadeFor(far, 3, vps, receivers); got != 2 {
		t.Fatalf("far receiver 10 km down-sun selected cascade %d, want 2", got)
	}

	near := sun.Mul(-150).Add(side.Mul(40))
	if got := shadowCascadeFor(near, 3, vps, noReceivers); got != 0 {
		t.Fatalf("control: the near receiver selected %d with the depth test alone, want 0", got)
	}
	if got := shadowCascadeFor(near, 3, vps, receivers); got != 1 {
		t.Fatalf("near receiver past the 90 m map's receiver depth selected %d, want the 3 km tier", got)
	}
	if got := shadowCascadeFor(sun.Mul(-20).Add(side.Mul(10)), 3, vps, receivers); got != 0 {
		t.Fatalf("a receiver 20 m away selected %d, want the 90 m map", got)
	}

	// The band edges are where the matrix puts the centre plus and minus the
	// receiver depth along the light.
	for c := range 2 {
		depth := coverage.Cascades[c].ReceiverDepth
		for _, edge := range []struct {
			along float32
			want  float32
		}{{-depth, receivers[c].Far}, {depth, receivers[c].Near}} {
			z := vps[c].Mul4x1(sun.Mul(edge.along).Vec4(1))[2]
			if math.Abs(float64(z-edge.want)) > 1e-6 {
				t.Errorf("cascade %d: %g m along the light is at depth %g, band edge %g", c, edge.along, z, edge.want)
			}
		}
	}
	t.Logf("receiver bands: 90 m [%g, %g], 3 km [%g, %g]", receivers[0].Near, receivers[0].Far, receivers[1].Near, receivers[1].Far)
}

// TestCascadeTexelSizes reads each tier's XY texel size back out of its
// matrix: two clip units over ShadowMapSize texels, divided by the matrix's
// X scale. The middle tier's 2.93 m is the figure the issue asked for.
//
// Verified break (2026-10-17): widening every cascade by 1% in
// computeLightVPCoverage fails with "cascade 0: 0.08876954100542314 m per
// texel, want 0.087890625".
func TestCascadeTexelSizes(t *testing.T) {
	vps, err := ComputeCascadeVPsWithCoverage([3]float32{0.8, 0.25, 0}, mgl32.Vec3{}, consumerCoverage())
	if err != nil {
		t.Fatal(err)
	}
	for c, want := range []float64{0.087890625, 2.9296875, 117.1875} {
		row := mgl32.Vec3{vps[c][0], vps[c][4], vps[c][8]}.Len()
		got := 2 / float64(ShadowMapSize) / float64(row)
		if math.Abs(got-want)/want > 1e-5 {
			t.Errorf("cascade %d: %g m per texel, want %g", c, got, want)
		}
		t.Logf("cascade %d: %.6g m per texel at %d", c, got, ShadowMapSize)
	}
}

// TestShadowCoverageCount covers the count's own validation: a coverage must
// describe as many cascades as the renderer allocated, may not set a cascade
// past its count, and may not ask for a count outside [2, 4].
//
// Verified break (2026-10-17): dropping ValidateFor's count comparison fails
// with "default at three: err <nil>, want ok=false" and the two after it.
func TestShadowCoverageCount(t *testing.T) {
	three := consumerCoverage()
	for _, tc := range []struct {
		name     string
		coverage ShadowCoverage
		renderer int
		ok       bool
	}{
		{"zero at two", ShadowCoverage{}, 2, true},
		{"zero renderer count", ShadowCoverage{}, 0, true},
		{"default at two", DefaultShadowCoverage(), 2, true},
		{"zero at three", ShadowCoverage{}, 3, true}, // DefaultShadowCoverageFor(3)
		{"default at three", DefaultShadowCoverage(), 3, false},
		{"three at three", three, 3, true},
		{"three at two", three, 2, false},
		{"three at four", three, 4, false},
		{"stray fourth", func() ShadowCoverage { c := three; c.Cascades[3].Radius = 1; return c }(), 3, false},
		{"stray third without a count", func() ShadowCoverage {
			c := DefaultShadowCoverage()
			c.Count = 0
			c.Cascades[2] = three.Cascades[2]
			return c
		}(), 2, false},
		{"count five", ShadowCoverage{Count: 5}, 5, false},
		{"count one", ShadowCoverage{Count: 1, Cascades: [MaxShadowCascades]ShadowCascadeCoverage{three.Cascades[0]}}, 1, false},
		{"negative receiver depth", func() ShadowCoverage { c := three; c.Cascades[1].ReceiverDepth = -1; return c }(), 3, false},
		{"NaN receiver depth", func() ShadowCoverage { c := three; c.Cascades[0].ReceiverDepth = float32(math.NaN()); return c }(), 3, false},
		{"infinite receiver depth", func() ShadowCoverage { c := three; c.Cascades[2].ReceiverDepth = float32(math.Inf(1)); return c }(), 3, false},
	} {
		err := tc.coverage.ValidateFor(tc.renderer)
		if (err == nil) != tc.ok {
			t.Errorf("%s: err %v, want ok=%v", tc.name, err, tc.ok)
		}
	}
	// A three-cascade coverage computes three matrices and leaves the fourth
	// zero.
	vps, err := ComputeCascadeVPsWithCoverage([3]float32{0, 1, 0}, mgl32.Vec3{}, three)
	if err != nil {
		t.Fatal(err)
	}
	if vps[2] == (mgl32.Mat4{}) || vps[3] != (mgl32.Mat4{}) {
		t.Fatal("three-cascade coverage did not fill exactly three matrices")
	}
}

// Capabilities reports the count the renderer was built with and the most it
// accepts, so a game can pick the ShadowData declaration its shaders need
// without assuming its option arrived.
//
// Verified break (2026-10-17): leaving ShadowCascades out of adopt fails with
// "WithShadowCascades(0): Capabilities report 0 of 4".
func TestCapabilitiesReportShadowCascades(t *testing.T) {
	for _, n := range []int{0, 2, 3, 4} {
		r := &Renderer{}
		WithShadowCascades(n)(r)
		r.adopt(Capabilities{})
		want := n
		if n == 0 {
			want = ShadowCascades
		}
		if c := r.Capabilities(); c.ShadowCascades != want || c.MaxShadowCascades != MaxShadowCascades {
			t.Errorf("WithShadowCascades(%d): Capabilities report %d of %d", n, c.ShadowCascades, c.MaxShadowCascades)
		}
		if r.ShadowCascadeCount() != want {
			t.Errorf("WithShadowCascades(%d): ShadowCascadeCount %d", n, r.ShadowCascadeCount())
		}
	}
	r := &Renderer{shadow: &shadowResources{mapBytes: 12345}}
	if got := r.ResourceCounts().ShadowMapBytes; got != 12345 {
		t.Fatalf("ResourceCounts.ShadowMapBytes = %d, want the shadow resources' 12345", got)
	}
}

// tracedCascades is the cascades= field traceLighting writes for l at count.
func tracedCascades(t *testing.T, l SceneLighting, count int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cascades.trace")
	tr, err := OpenStateTrace(path)
	if err != nil {
		t.Fatal(err)
	}
	tr.Begin(0)
	traceLighting(tr, l, count)
	tr.End()
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`cascades=([0-9a-f]+)`).FindSubmatch(body)
	if m == nil {
		t.Fatalf("no cascades= field in %q", body)
	}
	return string(m[1])
}

// The per-frame cascades= field folds the matrices the renderer draws and no
// others, and the receiver bands only once a coverage set one -- so a default
// frame hashes what it always hashed, a third cascade's matrix moves the field
// on a renderer that draws it, and a receiver band is not invisible to a diff.
//
// Verified break (2026-10-17): folding the receiver bands only when the count
// is past two fails with "a receiver band absent from cascades=".
func TestTraceLightingFoldsActiveCascades(t *testing.T) {
	l := SceneLighting{ShadowEnabled: true, CascadeVPs: ComputeCascadeVPs([3]float32{0.3, 0.8, 0.2}, mgl32.Vec3{})}
	// The pre-count hash, written out as the old loop wrote it.
	old := NewHash.Bool(true).Float32s(l.CascadeVPs[0][:]).Float32s(l.CascadeVPs[1][:])
	if got := tracedCascades(t, l, 2); got != fmt.Sprintf("%x", uint64(old)) {
		t.Fatalf("default cascades=%s, the two-matrix hash is %x", got, uint64(old))
	}
	base := tracedCascades(t, l, 2)
	extra := l
	extra.CascadeVPs[2] = mgl32.Ident4()
	if tracedCascades(t, extra, 2) != base {
		t.Error("a matrix past the count moved a two-cascade trace")
	}
	if tracedCascades(t, extra, 3) == tracedCascades(t, l, 3) {
		t.Error("the third matrix absent from a three-cascade trace")
	}
	banded := l
	banded.CascadeReceivers[0] = ShadowReceiverRange{0.25, 0.75}
	if tracedCascades(t, banded, 2) == base {
		t.Error("a receiver band absent from cascades=")
	}
}

// DefaultShadowCoverageFor pins the derived default an engine with more
// cascades draws until it is told otherwise: the historical pair extended by
// the factor of six between them, reach scaled the same way. These numbers are
// derived, not measured, and environment.md says so. At two it must be exactly
// DefaultShadowCoverage, or the default frame moves.
//
// Verified break (2026-10-17): a factor of 5 instead of 6 fails with
// "n=2: {2 [{15 15 22.5 0} {75 75 112.5 0} ...".
func TestDefaultShadowCoverageFor(t *testing.T) {
	want := [MaxShadowCascades]ShadowCascadeCoverage{
		{Radius: 15, TowardLight: 15, AwayFromLight: 22.5},
		{Radius: 90, TowardLight: 90, AwayFromLight: 135},
		{Radius: 540, TowardLight: 540, AwayFromLight: 810},
		{Radius: 3240, TowardLight: 3240, AwayFromLight: 4860},
	}
	for _, n := range []int{2, 3, 4} {
		c := DefaultShadowCoverageFor(n)
		exp := ShadowCoverage{Count: n}
		copy(exp.Cascades[:n], want[:n])
		if c != exp {
			t.Errorf("n=%d: %v, want %v", n, c, exp)
		}
		if err := c.ValidateFor(n); err != nil {
			t.Errorf("n=%d: the default fails its own validation: %v", n, err)
		}
	}
	if DefaultShadowCoverageFor(2) != DefaultShadowCoverage() {
		t.Fatal("DefaultShadowCoverageFor(2) is not the two-cascade default")
	}
	old := ShadowCoverage{Count: 2, Cascades: [MaxShadowCascades]ShadowCascadeCoverage{want[0], want[1]}}
	if DefaultShadowCoverage() != old {
		t.Fatal("DefaultShadowCoverage moved off 15 and 90")
	}
}
