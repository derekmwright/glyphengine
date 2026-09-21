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
	draws := e.buildDrawList(mgl32.Ident4(), true, vps[:]...)
	if len(draws) != 1 || !draws[0].ShadowOnly {
		t.Fatal("near-only distant caster was culled")
	}
	bad := c
	bad.Cascades[0].Radius = -1
	if e.SetShadowCoverage(bad) == nil || e.shadowCoverage != c {
		t.Fatal("invalid setter changed active coverage")
	}
}
