package renderer

import (
	"bytes"
	"math"
	"testing"

	"github.com/go-gl/mathgl/mgl32"
	"github.com/qmuntal/gltf"
	"github.com/qmuntal/gltf/ext/lightspunctual"
)

// This file's first test round-trips a document through the REAL encoder and
// decoder rather than building a *gltf.Document by hand and calling
// extractLights directly, unlike gltfnodes_test.go's tests. That is
// deliberate: the whole premise of extractLights is that importing
// ext/lightspunctual makes doc.Extensions["KHR_lights_punctual"] decode to
// lightspunctual.Lights and a node's own reference decode to a
// lightspunctual.LightIndex, purely as a side effect of that package's
// init(). Reading the package source says that should be true; only an
// actual encode-then-decode proves it, which is why the issue this
// implements calls for one explicitly rather than trusting the reading.
//
// khrLightsPayload/khrLightRefPayload/spotPayload below are the WRITE side of
// KHR_lights_punctual, which ext/lightspunctual does not provide (it only
// registers a decoder) -- the same small structs examples/22-level/gen/main.go
// uses to author the committed level.glb, kept independent here rather than
// imported so a mistake in one is not hidden by the same mistake in the
// other.

type khrLightsPayload struct {
	Lights []gltfLightPayload `json:"lights"`
}

type khrLightRefPayload struct {
	Light int `json:"light"`
}

type gltfLightPayload struct {
	Type      string       `json:"type"`
	Name      string       `json:"name,omitempty"`
	Color     *[3]float64  `json:"color,omitempty"`
	Intensity *float64     `json:"intensity,omitempty"`
	Range     *float64     `json:"range,omitempty"`
	Spot      *spotPayload `json:"spot,omitempty"`
}

type spotPayload struct {
	InnerConeAngle float64  `json:"innerConeAngle,omitempty"`
	OuterConeAngle *float64 `json:"outerConeAngle,omitempty"`
}

// roundTrip encodes doc as GLB and decodes it back, so every assertion after
// this call is against what a REAL glTF reader would see, not against the
// in-memory struct the test happened to build.
func roundTrip(t *testing.T, doc *gltf.Document) *gltf.Document {
	t.Helper()
	var buf bytes.Buffer
	if err := gltf.NewEncoder(&buf).Encode(doc); err != nil {
		t.Fatalf("encode: %v", err)
	}
	got := new(gltf.Document)
	if err := gltf.NewDecoder(bytes.NewReader(buf.Bytes())).Decode(got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return got
}

// TestExtractLightsRoundTrip is the real-round-trip proof described above:
// a document-level light array of three lights, only two of which any node
// references (the third proves extractLights returns lights ATTACHED to a
// node, not every light the document happens to define), through encode and
// decode.
//
// It has teeth: asserting on the decoded types directly (rather than through
// extractLights) confirms the premise --
// doc.Extensions["KHR_lights_punctual"].(lightspunctual.Lights) and
// gn.Extensions["KHR_lights_punctual"].(lightspunctual.LightIndex) both
// type-assert cleanly after a real decode, which is the fact the rest of
// this file and gltflights.go depend on. Also verified by making
// modelLightKind always return LightKindPoint: reported "Spot.Kind = point,
// want LightKindSpot" plus InnerCone/OuterCone both 0 instead of 0.3/0.6 (the
// spot-only fields modelLightFrom skips for a non-spot kind). Reverted after.
//
// The world pos/dir assertions at the end have teeth of their own, checked
// the same way: aiming lightWorldPosDir's direction vector down +Z instead
// of -Z reported "Bulb world dir = [0 -1 ...], want [0 1 0]" and "Spot world
// dir = [0 0 1], want [0 0 -1]"; reading nodes[light.Node].Local instead of
// .World (dropping the parent chain) reported both lights' world pos as
// [0 0 0] instead of [2 3 4], since Root's translation never gets composed
// in. Both introduced and reverted to confirm.
func TestExtractLightsRoundTrip(t *testing.T) {
	doc := &gltf.Document{
		Asset:              gltf.Asset{Version: "2.0"},
		ExtensionsUsed:     []string{lightspunctual.ExtensionName},
		ExtensionsRequired: []string{lightspunctual.ExtensionName},
		Extensions: gltf.Extensions{
			lightspunctual.ExtensionName: khrLightsPayload{Lights: []gltfLightPayload{
				{ // 0: point, referenced by node "Bulb"
					Type:      lightspunctual.TypePoint,
					Name:      "Bulb",
					Color:     &[3]float64{1.0, 0.6, 0.2},
					Intensity: gltf.Float(500),
					Range:     gltf.Float(6),
				},
				{ // 1: spot, referenced by node "Spot"
					Type:      lightspunctual.TypeSpot,
					Name:      "Spot",
					Color:     &[3]float64{0.9, 0.95, 1.0},
					Intensity: gltf.Float(1200),
					Range:     gltf.Float(10),
					Spot: &spotPayload{
						InnerConeAngle: 0.3,
						OuterConeAngle: gltf.Float(0.6),
					},
				},
				{ // 2: directional, defined but never referenced by any node
					Type:      lightspunctual.TypeDirectional,
					Name:      "Sun",
					Intensity: gltf.Float(2),
				},
			}},
		},
		Nodes: []*gltf.Node{
			{ // 0: root, translated
				Name:        "Root",
				Translation: [3]float64{2, 3, 4},
				Rotation:    identQuat,
				Scale:       [3]float64{1, 1, 1},
				Children:    []int{1, 2},
			},
			{ // 1: child, rotated 90deg about X -- local -Z (glTF's aim axis)
				// then points at world +Y, the same hand-checked rotation
				// TestExtractNodesOrientationSurvives uses.
				Name:       "Bulb",
				Rotation:   [4]float64{0.70710678, 0, 0, 0.70710678},
				Extensions: gltf.Extensions{lightspunctual.ExtensionName: khrLightRefPayload{Light: 0}},
			},
			{ // 2: sibling, no rotation -- aims down glTF's bare -Z.
				Name:       "Spot",
				Extensions: gltf.Extensions{lightspunctual.ExtensionName: khrLightRefPayload{Light: 1}},
			},
		},
	}

	got := roundTrip(t, doc)

	// Prove the premise directly before trusting extractLights to have used
	// it correctly.
	rawDocLights, ok := got.Extensions[lightspunctual.ExtensionName]
	if !ok {
		t.Fatal("decoded document lost its top-level KHR_lights_punctual extension")
	}
	if _, ok := rawDocLights.(lightspunctual.Lights); !ok {
		t.Fatalf("doc.Extensions[KHR_lights_punctual] decoded as %T, want lightspunctual.Lights", rawDocLights)
	}
	rawNodeLight, ok := got.Nodes[1].Extensions[lightspunctual.ExtensionName]
	if !ok {
		t.Fatal("decoded node lost its KHR_lights_punctual reference")
	}
	if _, ok := rawNodeLight.(lightspunctual.LightIndex); !ok {
		t.Fatalf("node.Extensions[KHR_lights_punctual] decoded as %T, want lightspunctual.LightIndex", rawNodeLight)
	}

	nodes := extractNodes(got)
	lights := extractLights(got, nodes)

	if len(lights) != 2 {
		t.Fatalf("got %d lights, want 2 (the third is defined but unreferenced)", len(lights))
	}

	var bulb, spot ModelLight
	for _, l := range lights {
		switch l.Name {
		case "Bulb":
			bulb = l
		case "Spot":
			spot = l
		default:
			t.Errorf("unexpected light %+v", l)
		}
	}

	if bulb.Kind != LightKindPoint {
		t.Errorf("Bulb.Kind = %v, want LightKindPoint", bulb.Kind)
	}
	if bulb.Color != [3]float32{1.0, 0.6, 0.2} {
		t.Errorf("Bulb.Color = %v, want [1 0.6 0.2]", bulb.Color)
	}
	if bulb.Intensity != 500 {
		t.Errorf("Bulb.Intensity = %v, want 500", bulb.Intensity)
	}
	if bulb.Range != 6 {
		t.Errorf("Bulb.Range = %v, want 6", bulb.Range)
	}

	if spot.Kind != LightKindSpot {
		t.Errorf("Spot.Kind = %v, want LightKindSpot", spot.Kind)
	}
	if spot.InnerCone != 0.3 {
		t.Errorf("Spot.InnerCone = %v, want 0.3", spot.InnerCone)
	}
	if spot.OuterCone != 0.6 {
		t.Errorf("Spot.OuterCone = %v, want 0.6", spot.OuterCone)
	}

	// World position/direction, computed by hand: Bulb's World is Root's
	// translation (2,3,4) composed with a 90deg-about-X rotation and no
	// further translation, so pos is exactly Root's translation and dir is
	// (0,0,-1) rotated the same way TestExtractNodesOrientationSurvives
	// hand-checks: y' = -z = 1, so (0,1,0).
	pos, dir := lightWorldPosDir(nodes, bulb)
	wantPos := mgl32.Vec3{2, 3, 4}
	wantDir := mgl32.Vec3{0, 1, 0}
	if !vec3Close(pos, wantPos, 1e-5) {
		t.Errorf("Bulb world pos = %v, want %v", pos, wantPos)
	}
	if !vec3Close(dir, wantDir, 1e-5) {
		t.Errorf("Bulb world dir = %v, want %v", dir, wantDir)
	}

	// Spot has no rotation, so it aims down bare -Z from the same translated
	// root.
	pos2, dir2 := lightWorldPosDir(nodes, spot)
	if !vec3Close(pos2, wantPos, 1e-5) {
		t.Errorf("Spot world pos = %v, want %v", pos2, wantPos)
	}
	if !vec3Close(dir2, (mgl32.Vec3{0, 0, -1}), 1e-5) {
		t.Errorf("Spot world dir = %v, want [0 0 -1]", dir2)
	}
}

// TestExtractLightsAppliesGltfDefaults checks the defaults the issue calls
// out by name: an omitted range means "unbounded", which ModelLight
// represents as 0 (see its doc comment), and a spot with no "spot" object at
// all -- invalid glTF, but the kind of thing a hand-authored or buggy
// exporter's document can contain, the same tolerance extractNodes gives
// other malformed input -- still gets glTF's own default outer half-angle of
// pi/4 rather than a zero-width cone.
//
// It has teeth: replacing the `l.Range == nil || math.IsInf(*l.Range, 1)`
// check in modelLightFrom with just `l.Range == nil` reports the unbounded
// light's Range as +Inf instead of 0, failing this test (+Inf survives the
// round trip as float64, so it is not caught by the float32 conversion
// either). Introduced and reverted to confirm.
func TestExtractLightsAppliesGltfDefaults(t *testing.T) {
	doc := &gltf.Document{
		Asset: gltf.Asset{Version: "2.0"},
		Extensions: gltf.Extensions{
			lightspunctual.ExtensionName: khrLightsPayload{Lights: []gltfLightPayload{
				{Type: lightspunctual.TypePoint},                      // 0: every optional field omitted
				{Type: lightspunctual.TypeSpot, Name: "NoSpotObject"}, // 1: spot, no "spot" key at all
			}},
		},
		Nodes: []*gltf.Node{
			{Name: "A", Extensions: gltf.Extensions{lightspunctual.ExtensionName: khrLightRefPayload{Light: 0}}},
			{Name: "B", Extensions: gltf.Extensions{lightspunctual.ExtensionName: khrLightRefPayload{Light: 1}}},
		},
	}

	got := roundTrip(t, doc)
	nodes := extractNodes(got)
	lights := extractLights(got, nodes)
	if len(lights) != 2 {
		t.Fatalf("got %d lights, want 2", len(lights))
	}

	var a, b ModelLight
	for _, l := range lights {
		if l.Node == 0 {
			a = l
		} else {
			b = l
		}
	}

	if a.Color != [3]float32{1, 1, 1} {
		t.Errorf("default Color = %v, want [1 1 1]", a.Color)
	}
	if a.Intensity != 1 {
		t.Errorf("default Intensity = %v, want 1", a.Intensity)
	}
	if a.Range != 0 {
		t.Errorf("omitted (unbounded) Range = %v, want 0", a.Range)
	}

	if b.InnerCone != 0 {
		t.Errorf("default InnerCone = %v, want 0", b.InnerCone)
	}
	if b.OuterCone != float32(math.Pi/4) {
		t.Errorf("default OuterCone = %v, want pi/4 = %v", b.OuterCone, math.Pi/4)
	}
}

// TestExtractLightsIgnoresUnreferenced covers the tolerance side: a document
// with no KHR_lights_punctual extension at all, and a node whose light index
// is out of range of the (present) array -- neither may panic or invent a
// light, the same tolerance extractNodes/meshOwnerNodes give other malformed
// glTF.
//
// It has teeth: removing the `idx < 0 || idx >= len(lights)` bounds check in
// extractLights panics with an index out of range on the second case.
// Introduced and reverted to confirm.
func TestExtractLightsIgnoresUnreferenced(t *testing.T) {
	t.Run("no extension at all", func(t *testing.T) {
		doc := &gltf.Document{
			Asset: gltf.Asset{Version: "2.0"},
			Nodes: []*gltf.Node{{Name: "Plain"}},
		}
		got := roundTrip(t, doc)
		nodes := extractNodes(got)
		if lights := extractLights(got, nodes); lights != nil {
			t.Errorf("got %v, want nil", lights)
		}
	})

	t.Run("node references an out-of-range light index", func(t *testing.T) {
		doc := &gltf.Document{
			Asset: gltf.Asset{Version: "2.0"},
			Extensions: gltf.Extensions{
				lightspunctual.ExtensionName: khrLightsPayload{Lights: []gltfLightPayload{
					{Type: lightspunctual.TypePoint},
				}},
			},
			Nodes: []*gltf.Node{
				{Name: "Bad", Extensions: gltf.Extensions{lightspunctual.ExtensionName: khrLightRefPayload{Light: 5}}},
			},
		}
		got := roundTrip(t, doc) // must not panic
		nodes := extractNodes(got)
		if lights := extractLights(got, nodes); lights != nil {
			t.Errorf("got %v, want nil (out-of-range light index skipped)", lights)
		}
	})
}

// TestLightWorldPosDirOutOfRange covers lightWorldPosDir's bounds check,
// independent of extractLights ever producing a light.Node this invalid --
// defensive, but the function is exported through Model.LightWorldPosDir and
// a caller can hand it any ModelLight, including a zero value.
//
// It has teeth: removing the bounds check panics with an index out of range
// on an empty nodes slice. Introduced and reverted to confirm.
func TestLightWorldPosDirOutOfRange(t *testing.T) {
	pos, dir := lightWorldPosDir(nil, ModelLight{Node: 3})
	if pos != (mgl32.Vec3{}) {
		t.Errorf("pos = %v, want zero", pos)
	}
	if dir != (mgl32.Vec3{0, 0, -1}) {
		t.Errorf("dir = %v, want [0 0 -1] (harmless default)", dir)
	}
}
