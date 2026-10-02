package include_test

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/derekmwright/glyphengine/shaders/include"
)

var includeDirective = regexp.MustCompile(`(?m)^\s*#include\s+"([^"]+)"`)

// TestEveryIncludedFragmentIsExported asserts that every name the engine's own
// shaders pull in with #include is present in the exported set.
//
// Most of that is structural rather than tested: `task shaders` compiles with
// -Iinclude, so the engine's own shaders resolve out of the same directory the
// embed pattern reads, and a fragment renamed out of `*.inc` breaks both at once.
// Confirmed, 2026-10-02: renaming srgb.inc to srgb.glsl fails this test on
// msdf.frag and ui.frag AND fails `task shaders:verify` with "Cannot find or open
// include file". That is a loud failure and needs no help.
//
// What is left is the quiet one, and it is what the path check below is for. A
// shared fragment placed one directory further down and included as
// "sub/helper.inc" resolves perfectly well through -Iinclude, so the engine
// builds and the committed SPIR-V still matches -- while `*.inc` never matched
// it, so a package in the x module gets nothing but glslc complaining about a
// file it cannot see, in a different repository, long after the change.
//
// Verified by breaking it, 2026-10-02: adding shaders/include/sub/helper.inc and
// an #include "sub/helper.inc" to ui.frag left `task shaders:verify` green (ok,
// 14.1s) and failed here with
//
//	..\ui.frag includes "sub/helper.inc" by path; use a bare name so -I resolves it
func TestEveryIncludedFragmentIsExported(t *testing.T) {
	exported := include.Names()
	if len(exported) == 0 {
		t.Fatal("the exported include set is empty; the embed pattern matched nothing")
	}

	var sources []string
	for _, pattern := range []string{"../*.vert", "../*.frag", "../*.comp", "*.inc"} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatalf("glob %s: %v", pattern, err)
		}
		sources = append(sources, matches...)
	}
	if len(sources) == 0 {
		t.Fatal("no shader sources found; this test would prove nothing")
	}

	for _, src := range sources {
		data, err := os.ReadFile(src)
		if err != nil {
			t.Fatalf("read %s: %v", src, err)
		}
		for _, m := range includeDirective.FindAllStringSubmatch(string(data), -1) {
			name := m[1]
			// The engine's own shaders name includes bare, resolved by the
			// -Iinclude search path. Anything with a directory in it is reaching
			// into the source tree, which does not survive `go get`.
			if strings.ContainsAny(name, "/\\") {
				t.Errorf("%s includes %q by path; use a bare name so -I resolves it", src, name)
				continue
			}
			if !slices.Contains(exported, name) {
				t.Errorf("%s includes %q, which the exported set does not carry (have %v)",
					src, name, exported)
			}
		}
	}
}

// TestWriteToMaterializesTheWholeSet asserts WriteTo lands every exported file,
// because glslc resolves the includes' own #includes through the same directory
// -- writing only the one a shader names would compile until the first shader
// that reaches lighting.inc, which pulls in three more.
func TestWriteToMaterializesTheWholeSet(t *testing.T) {
	dir := t.TempDir()
	if err := include.WriteTo(dir); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}
	for _, name := range include.Names() {
		want, err := include.FS.ReadFile(name)
		if err != nil {
			t.Fatalf("read embedded %s: %v", name, err)
		}
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read written %s: %v", name, err)
		}
		if string(got) != string(want) {
			t.Errorf("%s written content differs from the embedded copy", name)
		}
	}
}
