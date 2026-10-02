package renderer

import (
	"strings"
	"testing"

	"github.com/derekmwright/glyphengine/shaders"
)

// Scene-only totals for buildFrame(n) with no application passes, measured on
// the fixtures BEFORE application draws were counted at all. They are the right
// hand side of the parity rule in RenderStats: with application passes present,
// DrawCalls - App.DrawCalls has to land back on exactly these numbers, or
// counting the application's work has quietly changed what a game watching
// DrawCalls for scene regressions is reading.
var sceneOnlyTotals = map[int]RenderStats{
	7:   {DrawCalls: 95, Instances: 1094, Triangles: 39962},
	97:  {DrawCalls: 603, Instances: 5284, Triangles: 228828},
	511: {DrawCalls: 3159, Instances: 39067, Triangles: 1537856},
}

// The fixture's mesh pass draws fx.draws[:2], both the 360-index mesh: two
// draws, 120 triangles each. The fullscreen pass is one triangle.
const (
	fixtureMeshDraws     = 2
	fixtureMeshTriangles = 240
)

func appStatsFixture(t *testing.T, n int, compute bool) *frame {
	t.Helper()
	fx := withAppFrame(buildFrame(n), true, compute)
	if err := fx.record(&fakeDriver{}, 1); err != nil {
		t.Fatal(err)
	}
	return fx
}

func appPassNamed(t *testing.T, fx *frame, name string) *AppPass {
	t.Helper()
	for _, p := range fx.graph.apps {
		if p.desc.Name == name {
			return p
		}
	}
	t.Fatalf("fixture has no application pass %q", name)
	return nil
}

// TestAppStatsCountsMeshPass is the reported reproduction (#162): an enabled
// StageBeforeScene mesh pass with a known indexed triangle count, rendered,
// then disabled. Before the fix both captures were identical, which is how an
// application pass submitting millions of triangles read as nothing at all
// beside its own GPU timer.
func TestAppStatsCountsMeshPass(t *testing.T) {
	for n := range sceneOnlyTotals {
		fx := appStatsFixture(t, n, false)
		on := fx.stats
		mesh := appPassNamed(t, fx, "fixture mesh")

		scene := sceneOnlyTotals[n]
		if on.DrawCalls-on.App.DrawCalls != scene.DrawCalls ||
			on.Instances-on.App.Instances != scene.Instances ||
			on.Triangles-on.App.Triangles != scene.Triangles {
			t.Errorf("n=%d scene-only parity: %d/%d/%d, want %d/%d/%d", n,
				on.DrawCalls-on.App.DrawCalls, on.Instances-on.App.Instances, on.Triangles-on.App.Triangles,
				scene.DrawCalls, scene.Instances, scene.Triangles)
		}

		// Three application draws: two mesh, one fullscreen triangle.
		if on.App.DrawCalls != 3 || on.App.Instances != 3 || on.App.Triangles != fixtureMeshTriangles+1 || on.App.Dispatches != 0 {
			t.Errorf("n=%d application totals %d draws, %d instances, %d triangles, %d dispatches; want 3, 3, %d, 0",
				n, on.App.DrawCalls, on.App.Instances, on.App.Triangles, on.App.Dispatches, fixtureMeshTriangles+1)
		}
		got, ok := on.App.Pass("fixture mesh")
		if want := (AppPassStats{Name: "fixture mesh", DrawCalls: fixtureMeshDraws, Instances: fixtureMeshDraws, Triangles: fixtureMeshTriangles}); !ok || got != want {
			t.Errorf("n=%d mesh pass %+v (found %v), want %+v", n, got, ok, want)
		}
		if got, ok := on.App.Pass("fixture fullscreen"); !ok || got != (AppPassStats{Name: "fixture fullscreen", DrawCalls: 1, Instances: 1, Triangles: 1}) {
			t.Errorf("n=%d fullscreen pass %+v (found %v)", n, got, ok)
		}
		if _, ok := on.App.Pass("no such pass"); ok {
			t.Errorf("n=%d an unknown name was reported as a pass", n)
		}

		// Disabling the pass has to remove exactly its own delta, in the
		// aggregate and in the application block, and leave its row in place
		// reading zero rather than making the row disappear.
		mesh.SetEnabled(false)
		if err := fx.record(&fakeDriver{}, 1); err != nil {
			t.Fatal(err)
		}
		off := fx.stats
		if d := on.DrawCalls - off.DrawCalls; d != fixtureMeshDraws {
			t.Errorf("n=%d disabling the mesh pass removed %d draws, want %d", n, d, fixtureMeshDraws)
		}
		if d := on.Triangles - off.Triangles; d != fixtureMeshTriangles {
			t.Errorf("n=%d disabling the mesh pass removed %d triangles, want %d", n, d, fixtureMeshTriangles)
		}
		if off.DrawCalls != sceneOnlyTotals[n].DrawCalls+1 || off.Triangles != sceneOnlyTotals[n].Triangles+1 {
			t.Errorf("n=%d with the mesh pass off: %d draws, %d triangles; want the scene plus the fullscreen triangle", n, off.DrawCalls, off.Triangles)
		}
		if got, ok := off.App.Pass("fixture mesh"); !ok || got != (AppPassStats{Name: "fixture mesh"}) {
			t.Errorf("n=%d disabled mesh pass %+v (found %v), want a zeroed row", n, got, ok)
		}
		mesh.SetEnabled(true)
		t.Logf("n=%d: %d draws / %d triangles with the mesh pass, %d / %d without, scene-only %d / %d",
			n, on.DrawCalls, on.Triangles, off.DrawCalls, off.Triangles, on.DrawCalls-on.App.DrawCalls, on.Triangles-on.App.Triangles)
	}
}

// TestAppStatsCountsDispatch pins the other half of the counting rule: a
// dispatch is submitted work, but it is not a draw and it has no triangles, so
// it moves App.Dispatches and nothing else.
func TestAppStatsCountsDispatch(t *testing.T) {
	graphics := appStatsFixture(t, 97, false).stats
	fx := appStatsFixture(t, 97, true)
	s := fx.stats

	if s.DrawCalls != graphics.DrawCalls || s.Triangles != graphics.Triangles || s.App.DrawCalls != graphics.App.DrawCalls {
		t.Errorf("the compute pass moved draw counters: %d/%d draws, %d/%d triangles",
			s.DrawCalls, graphics.DrawCalls, s.Triangles, graphics.Triangles)
	}
	if s.App.Dispatches != 1 {
		t.Errorf("App.Dispatches %d, want 1", s.App.Dispatches)
	}
	if got, ok := s.App.Pass("fixture compute"); !ok || got != (AppPassStats{Name: "fixture compute", Dispatches: 1}) {
		t.Errorf("compute pass %+v (found %v), want one dispatch and no draw", got, ok)
	}

	// Per-pass rows are in frame-graph order, which is creation order within a
	// stage: the compute pass was created after the mesh pass at
	// StageBeforeScene, and the fullscreen pass runs at StageBeforeBloom.
	var names []string
	for _, p := range s.App.Passes {
		names = append(names, p.Name)
	}
	if strings.Join(names, ",") != "fixture mesh,fixture compute,fixture fullscreen" {
		t.Errorf("per-pass rows %v, want graph order", names)
	}

	// A zero axis skips the dispatch command, so it is no submitted work.
	appPassNamed(t, fx, "fixture compute").compute.SetDispatch(80, 0, 1)
	if err := fx.record(&fakeDriver{}, 1); err != nil {
		t.Fatal(err)
	}
	if fx.stats.App.Dispatches != 0 {
		t.Errorf("a zero-axis dispatch counted %d dispatches", fx.stats.App.Dispatches)
	}
	t.Logf("compute fixture: %d dispatches, %d application draws, %d application triangles",
		s.App.Dispatches, s.App.DrawCalls, s.App.Triangles)
}

// TestAppPassNamesAreUnique holds the decision that makes name lookup mean
// something: a duplicate is rejected where the application still knows which
// pass it meant, rather than silently summed into one row.
func TestAppPassNamesAreUnique(t *testing.T) {
	r := &Renderer{}
	target := &RenderTarget{r: r, desc: RenderTargetDesc{Format: TargetR16F, Scale: 1}}
	target.texture.target = target
	pass := AppPassDesc{Name: "glow", Stage: StageBeforeScene, Target: target, Fullscreen: true,
		Vert: shaders.DepthResolveVertSpv, Frag: shaders.DepthResolveFragSpv}
	compute := AppComputeDesc{Name: "glow", Stage: StageBeforeScene, Comp: shaders.DepthResolveFragSpv}
	if err := r.validateAppPass(pass); err != nil {
		t.Fatal(err)
	}
	if err := r.validateAppCompute(compute); err != nil {
		t.Fatal(err)
	}

	live := &AppPass{r: r, desc: AppPassDesc{Name: "glow"}}
	r.appPasses = []*AppPass{live}
	for _, test := range []struct {
		kind string
		err  error
	}{{"graphics", r.validateAppPass(pass)}, {"compute", r.validateAppCompute(compute)}} {
		if test.err == nil || !strings.Contains(test.err.Error(), "Name") {
			t.Errorf("%s duplicate accepted: %v", test.kind, test.err)
		} else {
			t.Logf("%s duplicate rejected: %v", test.kind, test.err)
		}
	}
	// A different name is still fine beside it, and destroying the holder frees
	// the name again.
	other := pass
	other.Name = "haze"
	if err := r.validateAppPass(other); err != nil {
		t.Errorf("a distinct name was rejected: %v", err)
	}
	r.appPasses = nil
	if err := r.validateAppPass(pass); err != nil {
		t.Errorf("name not freed by destroying its pass: %v", err)
	}
}
