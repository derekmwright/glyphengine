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
	for _, want := range []string{"srgb.inc", "lighting.inc", "atmosphere.inc", "lights.inc", "volumetric.inc"} {
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
