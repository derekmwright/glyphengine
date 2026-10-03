package lut

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

// -update recompiles and rewrites the committed .spv instead of comparing it.
// The check and the generator are one thing on purpose, for the reason x/sky's
// and x/water's say: two copies of a compile command are two things that can
// disagree about a flag.
//
//	task xskylut:shaders
var update = flag.Bool("update", false, "recompile and rewrite the committed .spv")

// TestCommittedSPIRVMatchesGLSL asserts the committed .spv is what glslc
// produces from the .frag beside it, compiled through the engine's exported GLSL
// include set exactly as x/README.md documents.
//
// The .spv is committed because lut.go embeds it, so the package has to build for
// anyone who `go get`s it without a Vulkan SDK (AGENTS.md rule 2). The cost is
// that it can fall out of step with its source in silence: a stale shader still
// compiles, still links, still renders, and renders what the source used to say.
//
// It is also the compile gate for the include seam. skylut.frag calls
// atmSunDirFrom out of atmosphere.inc, and bake.go reimplements atmDaylight,
// atmTwilight, atmSkyPalette and atmSunGlow out of the same file -- so a changed
// signature there fails here at build, and a changed CURVE there fails in
// TestAtmosphereIncStillSaysWhatWeCopied. Those two cover the seam between them;
// neither covers it alone.
//
// It skips when there is no SDK, because glslc is authoring-only and most people
// building this will not have one -- the same bargain `task shaders:verify` makes.
//
// Verified by breaking it twice. Changing the proximity axis in skylut.frag from
// `sqrt(0.5 - 0.5 * prox)` to `sqrt(0.5 - 0.49 * prox)` without recompiling fails
// with "skylut.frag.spv is stale (5008 bytes committed, 5024 fresh)". Changing
// LUT_PROX from 32.0 to 31.0 fails with "stale (5008 bytes committed, 5008
// fresh)" -- equal sizes, which is the case a length comparison alone would have
// missed.
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
				t.Errorf("%s.spv is stale (%d bytes committed, %d fresh) — run `task xskylut:shaders`",
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
