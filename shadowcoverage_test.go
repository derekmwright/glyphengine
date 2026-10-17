package glyphengine

import (
	"testing"

	"github.com/derekmwright/glyphengine/renderer"
	"github.com/go-gl/mathgl/mgl32"
)

func TestShadowCoverageCullsAgainstEveryCascade(t *testing.T) {
	// Proved to fail when inShadowVolume tests only the last cascade:
	// "near-only distant caster was culled" (2026-09-21).
	e := &Engine{Scene: NewScene()}
	c := renderer.DefaultShadowCoverage()
	c.Cascades[0].TowardLight = 1500 // deliberately not contained by the far cascade
	if err := e.SetShadowCoverage(c); err != nil {
		t.Fatal(err)
	}
	vps, err := renderer.ComputeCascadeVPsWithCoverage([3]float32{0.8, 0.6, 0}, mgl32.Vec3{}, c)
	if err != nil {
		t.Fatal(err)
	}
	ent := e.Spawn()
	e.C.Transform.Set(ent, &Transform{Position: mgl32.Vec3{800, 600, 0}, Scale: mgl32.Vec3{1, 1, 1}})
	e.C.MeshRef.Set(ent, &MeshRef{Mesh: &renderer.Mesh{BoundRadius: 1}})
	if draws := e.buildDrawList(mgl32.Ident4(), true, vps[1]); len(draws) != 0 {
		t.Fatal("control caster visible to camera/far map")
	}
	draws := e.buildDrawList(mgl32.Ident4(), true, vps[:c.CascadeCount()]...)
	if len(draws) != 1 || !draws[0].ShadowOnly {
		t.Fatal("near-only distant caster was culled")
	}
	bad := c
	bad.Cascades[0].Radius = -1
	if e.SetShadowCoverage(bad) == nil || e.shadowCoverage != c {
		t.Fatal("invalid setter changed active coverage")
	}
}

// threeCascadeCoverage is the default two with a third, wider tier beside
// them: the shape of coverage WithShadowCascades(3) needs before an engine
// draws any shadows.
func threeCascadeCoverage() renderer.ShadowCoverage {
	c := renderer.DefaultShadowCoverage()
	c.Count = 3
	c.Cascades[2] = renderer.ShadowCascadeCoverage{Radius: 2000, TowardLight: 3000, AwayFromLight: 3000}
	return c
}

// TestShadowCoverageCullsAgainstEveryActiveCascade is the union cull at three
// cascades, through the same fit renderFrame uses: a caster 500 units off the
// light axis is outside the 15 and 90 unit cascades and inside only the third,
// so culling against the first two -- the count before it was an option --
// drops it, and culling against every active cascade keeps it as a shadow
// caster.
//
// Verified breaks (2026-10-17): buildDrawList testing only the first two
// volumes it is handed fails with "third-cascade-only caster was culled", and
// fitShadowCascades returning renderer.ShadowCascades as the count fails with
// "fit: 2 cascades, enabled true".
func TestShadowCoverageCullsAgainstEveryActiveCascade(t *testing.T) {
	e := &Engine{Scene: NewScene(), shadowCascades: 3}
	if err := e.SetShadowCoverage(threeCascadeCoverage()); err != nil {
		t.Fatal(err)
	}
	ent := e.Spawn()
	e.C.Transform.Set(ent, &Transform{Position: mgl32.Vec3{0, 0, 500}, Scale: mgl32.Vec3{1, 1, 1}})
	e.C.MeshRef.Set(ent, &MeshRef{Mesh: &renderer.Mesh{BoundRadius: 1}})

	vps, n, ok := e.fitShadowCascades(true, [3]float32{0.8, 0.6, 0})
	if !ok || n != 3 {
		t.Fatalf("fit: %d cascades, enabled %v", n, ok)
	}
	if draws := e.buildDrawList(mgl32.Ident4(), true, vps[:2]...); len(draws) != 0 {
		t.Fatal("control: the first two cascades already see the caster")
	}
	draws := e.buildDrawList(mgl32.Ident4(), true, vps[:n]...)
	if len(draws) != 1 || !draws[0].ShadowOnly {
		t.Fatal("third-cascade-only caster was culled")
	}
}

// An engine built with three cascades draws shadows before the game sets a
// coverage -- the zero value is DefaultShadowCoverageFor(3), three matrices,
// none of them zero -- and refuses an explicit coverage that describes two.
//
// Verified breaks (2026-10-17): SetShadowCoverage calling Validate instead of
// ValidateFor fails with "a two-cascade coverage accepted at three", and
// fitShadowCascades passing the zero coverage through unreplaced fails with
// "zero coverage at three cascades did not fit DefaultShadowCoverageFor(3)".
func TestShadowCoverageMustMatchTheCascadeCount(t *testing.T) {
	e := &Engine{Scene: NewScene(), shadowCascades: 3}
	vps, n, ok := e.fitShadowCascades(true, [3]float32{0, 1, 0})
	if !ok || n != 3 {
		t.Fatalf("zero coverage at three cascades: enabled %v, n %d", ok, n)
	}
	want, err := renderer.ComputeCascadeVPsWithCoverage([3]float32{0, 1, 0}, mgl32.Vec3{}, renderer.DefaultShadowCoverageFor(3))
	if err != nil {
		t.Fatal(err)
	}
	if vps != want || vps[2] == (mgl32.Mat4{}) {
		t.Fatal("zero coverage at three cascades did not fit DefaultShadowCoverageFor(3)")
	}
	if err := e.SetShadowCoverage(renderer.ShadowCoverage{}); err != nil {
		t.Fatalf("the zero coverage refused at three: %v", err)
	}
	if e.SetShadowCoverage(renderer.DefaultShadowCoverage()) == nil {
		t.Fatal("a two-cascade coverage accepted at three")
	}
	if err := e.SetShadowCoverage(threeCascadeCoverage()); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := e.fitShadowCascades(true, [3]float32{0, 1, 0}); !ok {
		t.Fatal("a three-cascade coverage did not turn shadows on")
	}
	two := &Engine{Scene: NewScene()}
	if two.SetShadowCoverage(threeCascadeCoverage()) == nil {
		t.Fatal("a three-cascade coverage accepted by a two-cascade engine")
	}
}
