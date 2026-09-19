package renderer

import (
	"math"

	"github.com/go-gl/mathgl/mgl32"
	"github.com/qmuntal/gltf"
	"github.com/qmuntal/gltf/ext/lightspunctual"
)

// ModelLightKind is which of glTF's three punctual light types a ModelLight
// is -- point, spot or directional (KHR_lights_punctual's own vocabulary,
// see lightspunctual.TypePoint/TypeSpot/TypeDirectional).
type ModelLightKind int

const (
	// LightKindPoint emits in all directions from the node's position.
	// InnerCone/OuterCone are meaningless.
	LightKindPoint ModelLightKind = iota
	// LightKindSpot emits in a cone down the node's local -Z axis, shaped by
	// InnerCone/OuterCone.
	LightKindSpot
	// LightKindDirectional acts as though infinitely far away, emitting
	// uniformly down the node's local -Z axis. Range and the node's position
	// are meaningless -- only the direction matters.
	LightKindDirectional
)

// String names the light kind the way glTF's own "type" field spells it, so
// a log line reads the same word the document does.
func (k ModelLightKind) String() string {
	switch k {
	case LightKindSpot:
		return lightspunctual.TypeSpot
	case LightKindDirectional:
		return lightspunctual.TypeDirectional
	default:
		return lightspunctual.TypePoint
	}
}

// ModelLight is one KHR_lights_punctual light from a glTF document, placed by
// the node it is attached to -- a light carries no position or direction of
// its own in glTF, only the node reference does (see LightWorldPosDir).
//
// Every field here is the raw glTF value, unconverted. In particular
// Intensity is in glTF's own units -- candela for Point/Spot, lux for
// Directional -- and glyphengine.PointLight/SpotLight carry no unit at all,
// so loading a level means a game picks its own intensity -> Color scale
// rather than the engine guessing an exposure. See the units caveat in
// docs/agents/lights.md.
type ModelLight struct {
	// Node is the index into Model.Nodes this light is attached to.
	Node int

	Kind ModelLightKind
	Name string

	// Color is glTF's raw linear RGB factor, ColorOrDefault's (1,1,1) when
	// the document omitted it -- not multiplied by Intensity. A game
	// combines the two into whatever glyphengine.PointLight.Color or
	// SpotLight.Color expects; see docs/agents/lights.md.
	Color [3]float32

	// Intensity is glTF's raw value: candela for Point/Spot, lux for
	// Directional. 1 when the document omitted it (glTF's own default).
	Intensity float32

	// Range is glTF's raw value, and 0 specifically means the document said
	// "unbounded" (glTF's own default, and what an omitted range decodes
	// to) -- not "no light". glyphengine's PointLight and SpotLight have no
	// notion of an unbounded range, so a game loading a level MUST supply a
	// finite range for any light whose Range is 0 here; see the failure mode
	// in docs/agents/lights.md.
	Range float32

	// InnerCone and OuterCone are half-angles from the light's aim axis, in
	// radians. This is the SAME convention glyphengine.SpotLight.Inner/Outer
	// already uses (verified against docs/agents/lights.md, which documents
	// Inner/Outer as half-angles), so these carry straight over with no
	// conversion. Meaningless for Point and Directional. glTF's own defaults
	// -- inner 0, outer pi/4 -- are applied when the document's spot object
	// omits either, or (invalid glTF, but see extractNodes' tolerance for
	// the same kind of thing) omits the spot object on a spot light
	// entirely.
	InnerCone, OuterCone float32
}

// extractLights builds Model.Lights from a document's KHR_lights_punctual
// extension: the document-level "lights" array, matched to every node whose
// own extension names one.
//
// A pure function of *gltf.Document and the already-extracted nodes (only
// their count and identity are needed here; World is read later by
// LightWorldPosDir), so it is tested the same way extractNodes is -- built
// in memory, no GPU. See TestExtractLightsRoundTrip for why this is trusted
// rather than assumed: importing ext/lightspunctual registers its Unmarshal
// with gltf.RegisterExtension in that package's own init(), which is what
// makes doc.Extensions["KHR_lights_punctual"] arrive as lightspunctual.Lights
// and a node's own reference arrive as a lightspunctual.LightIndex rather
// than json.RawMessage -- confirmed by encoding a document and decoding it
// back with the real gltf.Decoder, not by reading the package and assuming.
func extractLights(doc *gltf.Document, nodes []ModelNode) []ModelLight {
	lights, ok := documentLights(doc)
	if !ok || len(lights) == 0 {
		return nil
	}

	var out []ModelLight
	for ni, gn := range doc.Nodes {
		if gn == nil {
			continue
		}
		idx, ok := nodeLightIndex(gn)
		if !ok || idx < 0 || idx >= len(lights) || lights[idx] == nil {
			continue
		}
		if ni < 0 || ni >= len(nodes) {
			continue
		}
		out = append(out, modelLightFrom(ni, lights[idx]))
	}
	return out
}

// documentLights returns the document's KHR_lights_punctual light array and
// whether the extension was present and decoded to the registered type.
//
// The second return is false both when the document carries no such
// extension and when it does but decoded to something else -- a malformed
// document, or one read before ext/lightspunctual's init() ran, which cannot
// happen from this package but is not this function's job to assume.
func documentLights(doc *gltf.Document) (lightspunctual.Lights, bool) {
	if doc == nil {
		return nil, false
	}
	raw, ok := doc.Extensions[lightspunctual.ExtensionName]
	if !ok {
		return nil, false
	}
	lights, ok := raw.(lightspunctual.Lights)
	return lights, ok
}

// nodeLightIndex returns the light index a node's own KHR_lights_punctual
// extension names, and whether it had one.
func nodeLightIndex(gn *gltf.Node) (int, bool) {
	raw, ok := gn.Extensions[lightspunctual.ExtensionName]
	if !ok {
		return 0, false
	}
	idx, ok := raw.(lightspunctual.LightIndex)
	return int(idx), ok
}

// modelLightFrom converts one decoded lightspunctual.Light, attached to node,
// into the engine's ModelLight.
func modelLightFrom(node int, l *lightspunctual.Light) ModelLight {
	ml := ModelLight{
		Node:      node,
		Kind:      modelLightKind(l.Type),
		Name:      l.Name,
		Intensity: float32(l.IntensityOrDefault()),
	}

	c := l.ColorOrDefault()
	ml.Color = [3]float32{float32(c[0]), float32(c[1]), float32(c[2])}

	// l.Range is never nil after a real decode -- Light.UnmarshalJSON defaults
	// it to +Inf -- but a Light built by hand (a test, or a future caller)
	// might leave it nil, and treating that the same as "unbounded" is the
	// same defaulting glTF itself would apply.
	if l.Range == nil || math.IsInf(*l.Range, 1) {
		ml.Range = 0
	} else {
		ml.Range = float32(*l.Range)
	}

	if ml.Kind == LightKindSpot {
		if l.Spot != nil {
			ml.InnerCone = float32(l.Spot.InnerConeAngle)
			ml.OuterCone = float32(l.Spot.OuterConeAngleOrDefault())
		} else {
			// glTF requires "spot" on a spot light; a document that omits it
			// anyway gets glTF's own defaults rather than a zero-width cone,
			// the same tolerance extractNodes gives other malformed input.
			ml.OuterCone = float32(math.Pi / 4)
		}
	}

	return ml
}

// modelLightKind maps glTF's "type" string to ModelLightKind, defaulting to
// LightKindPoint for anything unrecognized -- glTF requires the field, but
// treating an unknown value as "emits everywhere" is a safer wrong answer
// than treating it as a directional light with a range that silently doesn't
// matter, or a spot with no cone at all.
func modelLightKind(t string) ModelLightKind {
	switch t {
	case lightspunctual.TypeSpot:
		return LightKindSpot
	case lightspunctual.TypeDirectional:
		return LightKindDirectional
	default:
		return LightKindPoint
	}
}

// LightWorldPosDir returns light's position and aim direction in the glTF
// scene's space (see the space caveat on Model.Nodes -- the same one applies
// here), derived from the node it is attached to.
//
// glTF punctual lights carry no position or direction fields of their own;
// per the spec (and docs/agents/models.md's socket example, which uses the
// identical convention) a light points down its node's local -Z axis. dir is
// normalized; if light.Node is out of range this returns the node's
// position as the zero vector and -Z as a harmless default rather than
// indexing out of bounds.
func (m *Model) LightWorldPosDir(light ModelLight) (pos, dir mgl32.Vec3) {
	return lightWorldPosDir(m.Nodes, light)
}

// lightWorldPosDir is LightWorldPosDir's pure arithmetic, split out so it can
// be tested without a Renderer.
func lightWorldPosDir(nodes []ModelNode, light ModelLight) (pos, dir mgl32.Vec3) {
	dir = mgl32.Vec3{0, 0, -1}
	if light.Node < 0 || light.Node >= len(nodes) {
		return pos, dir
	}
	w := nodes[light.Node].World
	pos = w.Mul4x1(mgl32.Vec4{0, 0, 0, 1}).Vec3()
	dir = w.Mul4x1(mgl32.Vec4{0, 0, -1, 0}).Vec3().Normalize()
	return pos, dir
}
