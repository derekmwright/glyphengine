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
	if len(sources) == 0 {
		t.Fatal("no shader sources found")
	}

	out := t.TempDir()
	for _, src := range sources {
		t.Run(src, func(t *testing.T) {
			fresh := filepath.Join(out, src+".spv")
			cmd := exec.Command(glslc, src, "-o", fresh)
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
