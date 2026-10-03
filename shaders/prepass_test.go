package shaders

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// positionExpressions pulls out everything a shader does on the way to
// gl_Position, in order: every local assignment ahead of it, then the assignment
// itself. Lines after it are ignored -- lit.vert goes on to compute a world
// position and a shadow offset for the fragment stage, which the depth-only twin
// has no reason to carry. Whitespace is normalized, so the comparison is about
// arithmetic rather than formatting.
var (
	positionLine = regexp.MustCompile(`^\s*gl_Position\s*=\s*(.*);\s*$`)
	localLine    = regexp.MustCompile(`^\s*(?:vec4|vec3|mat4|mat3|float)\s+\w+\s*=\s*.*;\s*$`)
)

func positionExpressions(t *testing.T, path string) []string {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var out []string
	for _, line := range strings.Split(string(src), "\n") {
		if m := positionLine.FindStringSubmatch(line); m != nil {
			return append(out, strings.Join(strings.Fields(m[1]), " "))
		}
		if localLine.MatchString(line) {
			out = append(out, strings.Join(strings.Fields(line), " "))
		}
	}
	t.Fatalf("%s: no gl_Position assignment found, so this comparison would pass on nothing", path)
	return nil
}

// TestPrepassStagesMatchTheirLitTwins asserts the depth prepass's vertex stages
// reach gl_Position by exactly the arithmetic their lit counterparts use.
//
// This is not style policing. The main pass re-tests what the prepass wrote with
// a compare of EQUAL, so a difference in the last bit of a depth value is not a
// dimmer pixel, it is a surface that is not drawn at all. The trap is specific
// and already in this directory: shadow_instanced.vert writes
// `pc.vp * inModel * vec4(...)`, which GLSL left-associates into a matrix-matrix
// product and then a matrix-vector one, while lit_instanced.vert writes
// `pc.vp * (inModel * vec4(...))`, which is two matrix-vector products. Those are
// different floating-point operations and they do not agree in the low bits --
// which is why the prepass has its own vertex stages rather than reusing the
// shadow pass's.
//
// Verified to fail: rewriting prepass_instanced.vert's two lines as the single
// `gl_Position = pc.vp * inModel * vec4(inPosition, 1.0);` reports
// `prepass_instanced.vert computes gl_Position in 1 step(s) ...,
// lit_instanced.vert in 2 ...`.
func TestPrepassStagesMatchTheirLitTwins(t *testing.T) {
	for _, pair := range [][2]string{
		{"prepass.vert", "lit.vert"},
		{"prepass_instanced.vert", "lit_instanced.vert"},
	} {
		t.Run(pair[0], func(t *testing.T) {
			got, want := positionExpressions(t, pair[0]), positionExpressions(t, pair[1])
			if len(got) != len(want) {
				t.Fatalf("%s computes gl_Position in %d step(s) %v, %s in %d %v",
					pair[0], len(got), got, pair[1], len(want), want)
			}
			for i := range got {
				if got[i] != want[i] {
					t.Errorf("step %d differs:\n %s: %s\n %s: %s", i, pair[0], got[i], pair[1], want[i])
				}
			}
		})
	}
}
