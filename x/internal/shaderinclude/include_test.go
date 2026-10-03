package shaderinclude_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/derekmwright/glyphengine/shaders/include"
)

// probe is the smallest fragment shader that exercises both ends of the export:
// srgb.inc, which is self-contained, and lighting.inc, which pulls in
// atmosphere.inc, lights.inc and volumetric.inc and expects the including file
// to have declared the shadow set, the push constants and LIGHT_SET first.
//
// The preamble is not decoration. It is the fixed layout contract from
// docs/agents/render-targets.md, and an x package shader that gets it wrong does
// not render wrong -- it fails to compile, here, which is the only cheap signal
// there is. Keeping a copy of it in this gate means a change to what the
// includes expect breaks this test rather than breaking a game.
//
// Both functions are called, not merely included: a compiler is free to discard
// an unreferenced function, so an include whose body stopped compiling could
// otherwise slip through.
const probe = `#version 450
#extension GL_GOOGLE_include_directive : require

layout(location = 0) in vec3 fragWorldPos;
layout(location = 1) in vec3 fragWorldNormal;
layout(location = 2) in vec3 fragShadowPos;

layout(set = 1, binding = 0) uniform ShadowData {
    mat4 cascadeVP[2];
    vec4 nightGrade;
    vec4 skyPalette[6];
    vec4 volumetric;
} shadow;
layout(set = 1, binding = 1) uniform sampler2DArrayShadow shadowMap;
layout(set = 1, binding = 2) uniform samplerCube pointShadowMap;

#define LIGHT_SET 1

layout(push_constant) uniform PushConstants {
    mat4 mvp;
    mat4 model;
    vec4 tint;
    vec4 sunDir;
    vec4 sunColor;
    vec4 pointPos;
    vec4 pointColor;
    vec4 ambient;
    vec4 cameraPos;
    vec4 fog;
} pc;

layout(location = 0) out vec4 outColor;

#include "srgb.inc"
#include "lighting.inc"

void main() {
    vec3 albedo = srgbToLinear(pc.tint.rgb);
    vec3 N = normalize(fragWorldNormal);
    vec3 V = normalize(pc.cameraPos.xyz - fragWorldPos);
    vec3 lit = evalLighting(albedo, vec3(0.04), 32.0, N, V, fragWorldPos, fragShadowPos);
    outColor = vec4(applyFog(lit, fragWorldPos), 1.0);
}
`

// leafProbe is the dependency boundary volumetric_common.inc exists to draw: a
// version line, the include, and the two functions called. No descriptor set, no
// push-constant block, no LIGHT_SET -- because the whole claim about that file is
// that a pass needs none of them to use the jitter or the phase function.
//
// x/water/water-scatter.frag is the real case this stands in for. It integrates a
// medium from its own push constants and reads no light list, and before the
// split it had to copy volStartJitter to get at it.
const leafProbe = `#version 450
#extension GL_GOOGLE_include_directive : require

layout(location = 0) in vec2 fragPx;
layout(location = 0) out vec4 outColor;

#include "volumetric_common.inc"

void main() {
    float j = volStartJitter(fragPx);
    outColor = vec4(j, volPhase(j, 0.65), 0.0, 1.0);
}
`

// leafControl is leafProbe over volumetric.inc instead, and it must NOT compile.
//
// Without it the test above proves only that something compiled. The split is a
// claim about where the binding dependency sits, so the control is the half that
// says the dependency is still there on the other side of the line: volumetric.inc
// reaches for pc, the shadow UBO and lights.inc's lb, and a bindingless shader
// cannot have it.
const leafControl = `#version 450
#extension GL_GOOGLE_include_directive : require

layout(location = 0) in vec2 fragPx;
layout(location = 0) out vec4 outColor;

#include "volumetric.inc"

void main() {
    float j = volStartJitter(fragPx);
    outColor = vec4(j, volPhase(j, 0.65), 0.0, 1.0);
}
`

// TestPackageShaderCompilesAgainstExportedIncludes walks the documented build
// step end to end: write the embedded include set to a temp directory, point
// glslc at it with -I, and compile a shader that names the fragments bare.
//
// This is the x module's half of the contract the engine's own
// shaders/verify_test.go holds up for the engine. It is not testing glslc; it is
// testing that the includes are reachable from outside the engine module at all,
// and that they still compile for a caller who supplies only the layouts the
// capability page says to supply. Vendoring a copy of lighting.inc instead would
// make this test pass forever while the engine moved underneath it.
//
// It skips without a Vulkan SDK for exactly the reason shaders/verify_test.go
// does: glslc is an authoring-only dependency, and most people building this will
// not have one. CI has no SDK on the x path, so this gate runs locally; that is
// the same bargain `task shaders:verify` already makes.
//
// Verified by breaking it, 2026-10-02: renaming srgbToLinear to srgbDecode in
// shaders/include/srgb.inc fails here with glslc reporting
// `'srgbToLinear' : no matching overloaded function found`, while `go build ./...`
// in both modules stays green -- which is the whole reason this exists.
func TestPackageShaderCompilesAgainstExportedIncludes(t *testing.T) {
	glslc := findGlslc(t)

	incDir := filepath.Join(t.TempDir(), "include")
	if err := os.MkdirAll(incDir, 0o755); err != nil {
		t.Fatalf("mkdir include dir: %v", err)
	}
	if err := include.WriteTo(incDir); err != nil {
		t.Fatalf("materialize include set: %v", err)
	}
	// A directory with nothing in it would make the compile below fail for the
	// wrong reason, and an empty set that somehow compiled would make this test
	// vacuous.
	names := include.Names()
	if len(names) == 0 {
		t.Fatal("the exported include set is empty; this gate would prove nothing")
	}
	for _, want := range []string{"srgb.inc", "lighting.inc", "atmosphere.inc", "lights.inc", "volumetric.inc", "volumetric_common.inc"} {
		if _, err := os.Stat(filepath.Join(incDir, want)); err != nil {
			t.Fatalf("%s missing from the materialized set: %v", want, err)
		}
	}

	work := t.TempDir()
	src := filepath.Join(work, "probe.frag")
	if err := os.WriteFile(src, []byte(probe), 0o644); err != nil {
		t.Fatalf("write probe: %v", err)
	}
	spv := filepath.Join(work, "probe.frag.spv")

	cmd := exec.Command(glslc, "-I", incDir, src, "-o", spv)
	if msg, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("glslc failed against the exported includes (%v):\n%s", err, msg)
	}
	out, err := os.Stat(spv)
	if err != nil {
		t.Fatalf("stat SPIR-V: %v", err)
	}
	if out.Size() == 0 {
		t.Fatal("glslc reported success and wrote an empty module")
	}

	// The control. Without -I the same source must fail: if it compiled anyway,
	// the include set is being resolved from somewhere else -- a stray copy in
	// the temp dir, a system include path -- and the test above was not
	// measuring the export.
	plain := exec.Command(glslc, src, "-o", filepath.Join(work, "control.frag.spv"))
	msg, err := plain.CombinedOutput()
	if err == nil {
		t.Fatal("the probe compiled with no -I, so this test is not exercising the exported include set")
	}
	if !strings.Contains(string(msg), "srgb.inc") {
		t.Errorf("expected the no -I control to fail on a missing include, got:\n%s", msg)
	}
}

// TestVolumetricCommonNeedsNoBindings compiles both halves of the split: the leaf
// fragment alone with nothing declared, which must succeed, and volumetric.inc
// alone with nothing declared, which must fail.
//
// Together they are the boundary, and neither alone is worth much. The pair is
// what a package outside the engine actually depends on -- x/water includes the
// leaf and declares no light set -- and it is what would catch the slow failure
// mode here, which is a leaf helper quietly growing a reference to pc or lb and
// taking the whole clustered light declaration with it.
//
// Verified by breaking it, 2026-10-03: moving volPhase back out of
// volumetric_common.inc into volumetric.inc fails the leaf half with
// "'volPhase' : no matching overloaded function found", and moving
// volFogHeightExponent (which reads pc.fog.y) into the leaf fragment fails it with
// "'pc' : undeclared identifier" -- the second being the drift this guards
// against rather than a mistake anyone would make deliberately. Pointing
// leafControl at volumetric_common.inc so it stops failing is caught too, with
// "volumetric.inc compiled with no light set declared".
func TestVolumetricCommonNeedsNoBindings(t *testing.T) {
	glslc := findGlslc(t)

	incDir := filepath.Join(t.TempDir(), "include")
	if err := os.MkdirAll(incDir, 0o755); err != nil {
		t.Fatalf("mkdir include dir: %v", err)
	}
	if err := include.WriteTo(incDir); err != nil {
		t.Fatalf("materialize include set: %v", err)
	}

	work := t.TempDir()
	leaf := filepath.Join(work, "leaf.frag")
	if err := os.WriteFile(leaf, []byte(leafProbe), 0o644); err != nil {
		t.Fatalf("write leaf probe: %v", err)
	}
	cmd := exec.Command(glslc, "-I", incDir, leaf, "-o", filepath.Join(work, "leaf.frag.spv"))
	if msg, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("volumetric_common.inc did not compile with no bindings declared (%v):\n%s", err, msg)
	}

	control := filepath.Join(work, "control.frag")
	if err := os.WriteFile(control, []byte(leafControl), 0o644); err != nil {
		t.Fatalf("write control: %v", err)
	}
	plain := exec.Command(glslc, "-I", incDir, control, "-o", filepath.Join(work, "control.frag.spv"))
	msg, err := plain.CombinedOutput()
	if err == nil {
		t.Fatal("volumetric.inc compiled with no light set declared, so the split is not the dependency boundary this test claims")
	}
	// Named rather than merely non-zero: the control has to fail on the light
	// buffer it cannot have, not on a typo in the probe above it.
	if !strings.Contains(string(msg), "'lb' : undeclared identifier") {
		t.Errorf("expected the control to fail on lights.inc's lb, got:\n%s", msg)
	}
}

// findGlslc locates the authoring-only compiler, or skips.
//
// It skips without a Vulkan SDK for exactly the reason shaders/verify_test.go
// does: glslc is an authoring-only dependency, and most people building this will
// not have one. CI has no SDK on the x path, so these gates run locally; that is
// the same bargain `task shaders:verify` already makes.
func findGlslc(t *testing.T) string {
	t.Helper()
	sdk := os.Getenv("VULKAN_SDK")
	if sdk == "" {
		t.Skip("VULKAN_SDK unset; glslc is authoring-only, nothing to compile with")
	}
	glslc := filepath.Join(sdk, "bin", "glslc")
	if runtime.GOOS == "windows" {
		glslc += ".exe"
	}
	if _, err := os.Stat(glslc); err != nil {
		t.Skipf("glslc not found at %s", glslc)
	}
	return glslc
}
