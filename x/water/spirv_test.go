package water

import (
	"bytes"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/derekmwright/glyphengine/shaders/include"
)

// -update recompiles and rewrites the committed .spv instead of comparing them.
// It is how this package's SPIR-V is generated: the check and the generator are
// one thing on purpose, because two copies of a compile command are two things
// that can disagree about a flag, and the engine's own `task shaders` and
// shaders/verify_test.go are a standing example of that risk.
//
//	task xwater:shaders
var update = flag.Bool("update", false, "recompile and rewrite the committed .spv")

// TestCommittedSPIRVMatchesGLSL asserts every committed .spv in this package is
// what glslc produces from the .frag beside it, compiled through the engine's
// exported GLSL include set exactly as x/README.md documents.
//
// The .spv are committed for the reason AGENTS.md rule 2 gives: water.go embeds
// them, so the package has to build for anyone who `go get`s it without a Vulkan
// SDK. The cost is that they can fall out of step with their source silently --
// a stale shader still compiles, still links, still renders, and renders what
// the source used to say.
//
// The include set is materialized and passed with -I even though this package's
// shaders name nothing out of it today. That is deliberate and worth stating
// plainly rather than leaving as an accident: these shaders integrate a medium
// from their own push constants and do not light, fog or grade a surface, so
// there is nothing in lighting.inc or atmosphere.inc for them to call. Keeping
// the documented build step anyway means a shader here that grows a dependency
// on the set compiles the same way the engine's own do, with no second
// mechanism to discover. x/internal/shaderinclude is the gate that proves the
// set itself still reaches a consumer.
//
// It skips when there is no SDK, because glslc is authoring-only and most people
// building this will not have one -- the same bargain `task shaders:verify`
// makes.
//
// Verified by breaking it, 2026-10-02 and again 2026-10-03 after the parameter
// block moved off push constants: changing the span clamp in
// water-scatter.frag from `min(travel, params.march.x)` to
// `min(travel, params.march.x * 2.0)` without recompiling fails with
// "water-scatter.frag.spv is stale (7532 bytes committed, 7552 fresh)" --
// 7496/7516 on the push-constant version of the same shader.
func TestCommittedSPIRVMatchesGLSL(t *testing.T) {
	glslc, ok := findGlslc(t)
	if !ok {
		return
	}

	incDir := filepath.Join(t.TempDir(), "include")
	if err := os.MkdirAll(incDir, 0o755); err != nil {
		t.Fatalf("mkdir include dir: %v", err)
	}
	if err := include.WriteTo(incDir); err != nil {
		t.Fatalf("materialize the engine include set: %v", err)
	}
	// An empty set would make the -I below meaningless, and a build step that
	// silently stopped materializing anything is exactly the kind of thing a
	// green test should not hide.
	if names := include.Names(); len(names) == 0 {
		t.Fatal("the exported include set is empty; the documented build step is not being exercised")
	}

	sources, err := filepath.Glob("*.frag")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(sources) == 0 {
		t.Fatal("no shader sources found; this test would pass by having nothing to check")
	}

	out := t.TempDir()
	for _, src := range sources {
		t.Run(src, func(t *testing.T) {
			fresh := filepath.Join(out, filepath.Base(src)+".spv")
			cmd := exec.Command(glslc, "-I", incDir, src, "-o", fresh)
			if msg, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("glslc failed: %v\n%s", err, msg)
			}
			want, err := os.ReadFile(fresh)
			if err != nil {
				t.Fatalf("read fresh output: %v", err)
			}
			if *update {
				if err := os.WriteFile(src+".spv", want, 0o644); err != nil {
					t.Fatalf("write %s.spv: %v", src, err)
				}
				t.Logf("wrote %s.spv (%d bytes)", src, len(want))
				return
			}
			got, err := os.ReadFile(src + ".spv")
			if err != nil {
				t.Fatalf("read committed %s.spv: %v", src, err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("%s.spv is stale (%d bytes committed, %d fresh) — run `task xwater:shaders`",
					src, len(got), len(want))
			}
		})
	}
}

func findGlslc(t *testing.T) (string, bool) {
	t.Helper()
	sdk := os.Getenv("VULKAN_SDK")
	if sdk == "" {
		t.Skip("VULKAN_SDK unset; glslc is authoring-only, nothing to verify against")
		return "", false
	}
	glslc := filepath.Join(sdk, "bin", "glslc")
	if runtime.GOOS == "windows" {
		glslc += ".exe"
	}
	if _, err := os.Stat(glslc); err != nil {
		t.Skipf("glslc not found at %s", glslc)
		return "", false
	}
	return glslc, true
}
