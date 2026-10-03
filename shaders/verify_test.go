package shaders

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// TestCommittedSPIRVMatchesGLSL asserts every committed .spv is what glslc
// produces from the .vert/.frag beside it.
//
// The .spv are committed on purpose — shaders.go embeds them, so the module
// has to build for anyone who `go get`s it without a Vulkan SDK. The cost is
// that they can silently fall out of step with their source, and nothing else
// notices: a stale shader still compiles, still links, still renders. It just
// renders what the source used to say.
//
// This is a real failure mode rather than a theoretical one. Editing a shader
// means remembering to run `task shaders`, and the consequence of forgetting
// is wrong pixels on someone else's machine with no error anywhere.
//
// It skips when there is no SDK, because glslc is an authoring-only
// dependency and most people building this will not have one.
func TestCommittedSPIRVMatchesGLSL(t *testing.T) {
	sdk := os.Getenv("VULKAN_SDK")
	if sdk == "" {
		t.Skip("VULKAN_SDK unset; glslc is authoring-only, nothing to verify against")
	}

	glslc := filepath.Join(sdk, "bin", "glslc")
	if runtime.GOOS == "windows" {
		glslc += ".exe"
	}
	if _, err := os.Stat(glslc); err != nil {
		t.Skipf("glslc not found at %s", glslc)
	}

	sources, err := filepath.Glob("*.vert")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	frags, err := filepath.Glob("*.frag")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	sources = append(sources, frags...)
	// The engine's own compute stages, which this test did not cover at all until
	// #154's evaluation added a second one and noticed: lodselect.comp is
	// embedded from committed .spv exactly as the graphics stages are, and a
	// stale one is just as invisible -- it still compiles, links and renders.
	// Verified by widening a .comp's workgroup to 16x8 without recompiling:
	// "depthpyramid.comp.spv is stale (3444 bytes committed, 3460 fresh)", on the
	// shader that evaluation added. A comment-only edit does NOT fail it, because
	// glslc without -g emits the same bytes for it, which is the right behaviour
	// and worth knowing before trusting this to catch an edit.
	comps, err := filepath.Glob("*.comp")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	sources = append(sources, comps...)
	probes, err := filepath.Glob("../cmd/skyshadowcheck/*.frag")
	if err != nil {
		t.Fatal(err)
	}
	sources = append(sources, probes...)
	for _, pattern := range []string{"../cmd/apppasscheck/*.vert", "../cmd/apppasscheck/*.frag", "../cmd/apppasscheck/*.comp", "../examples/*/*.vert", "../examples/*/*.frag", "../examples/*/*.comp"} {
		app, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatal(err)
		}
		sources = append(sources, app...)
	}
	if len(sources) == 0 {
		t.Fatal("no shader sources found")
	}

	out := t.TempDir()
	for _, src := range sources {
		t.Run(src, func(t *testing.T) {
			fresh := filepath.Join(out, filepath.Base(src)+".spv")
			// -Iinclude must match `task shaders`. The shared fragments live in
			// shaders/include so shaders/include/include.go can embed them, and
			// every shader names them bare so the search path is what resolves
			// them. Omitting it fails the compile outright rather than
			// producing different bytes, but the task and this test have to
			// agree on the flag or they are no longer checking the same build.
			cmd := exec.Command(glslc, "-Iinclude", src, "-o", fresh)
			if msg, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("glslc failed: %v\n%s", err, msg)
			}

			want, err := os.ReadFile(fresh)
			if err != nil {
				t.Fatalf("read fresh output: %v", err)
			}
			got, err := os.ReadFile(src + ".spv")
			if err != nil {
				t.Fatalf("read committed %s.spv: %v", src, err)
			}

			if !bytes.Equal(got, want) {
				t.Errorf("%s.spv is stale (%d bytes committed, %d fresh) — run `task shaders`",
					src, len(got), len(want))
			}
		})
	}
}
