package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qmuntal/gltf"
	"github.com/qmuntal/gltf/modeler"

	glyph "github.com/derekmwright/glyphengine"
)

// TestRunRequiresExactlyOneInput covers the usage-error paths: no input,
// two inputs at once, and no -out.
func TestRunRequiresExactlyOneInput(t *testing.T) {
	var out, errw bytes.Buffer
	if code := run([]string{"-out", "x.heightmap"}, &out, &errw); code != 2 {
		t.Errorf("no input: exit %d, want 2 (usage error); stderr: %s", code, errw.String())
	}

	errw.Reset()
	if code := run([]string{"-mesh", "a.glb", "-image", "b.png", "-out", "x.heightmap"}, &out, &errw); code != 2 {
		t.Errorf("two inputs: exit %d, want 2; stderr: %s", code, errw.String())
	}

	errw.Reset()
	if code := run([]string{"-mesh", "a.glb", "-grid", "3x3"}, &out, &errw); code != 2 {
		t.Errorf("no -out: exit %d, want 2; stderr: %s", code, errw.String())
	}
}

// TestRunImageEndToEnd drives the whole -image pipeline through run(),
// including writing the .heightmap and reading it back with LoadHeightmap
// -- the same round trip a real invocation performs.
func TestRunImageEndToEnd(t *testing.T) {
	dir := t.TempDir()
	pngPath := filepath.Join(dir, "h.png")
	writeGray16PNG(t, pngPath, 0, 65535, 0, 0) // top-right corner is the peak

	outPath := filepath.Join(dir, "out.heightmap")
	var out, errw bytes.Buffer
	code := run([]string{
		"-image", pngPath,
		"-world", "10x10",
		"-range", "0,100",
		"-out", outPath,
	}, &out, &errw)
	if code != 0 {
		t.Fatalf("run: exit %d, stdout: %s, stderr: %s", code, out.String(), errw.String())
	}

	h, err := glyph.LoadHeightmap(os.DirFS(dir), "out.heightmap")
	if err != nil {
		t.Fatalf("LoadHeightmap: %v", err)
	}
	if h.GridW != 2 || h.GridH != 2 {
		t.Fatalf("grid = %dx%d, want 2x2", h.GridW, h.GridH)
	}
	// Top-right image pixel (row 0, col 1) -> far edge, right column ->
	// grid(ix=1, iz=1).
	if got := h.Heights[1*2+1]; got != 100 {
		t.Errorf("peak height = %g, want 100 (the image's top-right sample)", got)
	}
}

// TestRunMeshEndToEnd drives -mesh through run() using the same asymmetric
// fixture mesh_test.go builds, then checks -info against the file it wrote.
func TestRunMeshEndToEnd(t *testing.T) {
	dir := t.TempDir()
	meshPath := filepath.Join(dir, "terrain.glb")
	writeSimpleRampMesh(t, meshPath)

	outPath := filepath.Join(dir, "out.heightmap")
	var out, errw bytes.Buffer
	code := run([]string{
		"-mesh", meshPath,
		"-grid", "3x3",
		"-out", outPath,
	}, &out, &errw)
	if code != 0 {
		t.Fatalf("run: exit %d, stdout: %s, stderr: %s", code, out.String(), errw.String())
	}
	if !strings.Contains(out.String(), "rasterised node") {
		t.Errorf("stdout missing the rasterise summary line: %s", out.String())
	}

	out.Reset()
	if code := run([]string{"-info", outPath}, &out, &errw); code != 0 {
		t.Fatalf("-info: exit %d, stderr: %s", code, errw.String())
	}
	if !strings.Contains(out.String(), "grid:") || !strings.Contains(out.String(), "3 x 3") {
		t.Errorf("-info output missing grid summary: %s", out.String())
	}
}

// TestRunMeshHoleFailsByDefaultAndFillSucceeds is the CLI-level check for
// the issue's explicit ask: a hole fails the run by default (with a count
// and coordinates), and -fill opts in to filling it instead.
//
// Verified to fail: making convertMesh always fill with -fill nearest
// regardless of whether a.fill was set (ignoring mode.kind == "") made this
// print "run with a hole and no -fill succeeded, want a non-zero exit",
// with the stdout showing a heightmap was written and a misleading "filled
// 2 hole(s) with -fill " line (the empty flag value leaking through, which
// is itself a symptom that -fill was never checked).
func TestRunMeshHoleFailsByDefaultAndFillSucceeds(t *testing.T) {
	dir := t.TempDir()
	meshPath := filepath.Join(dir, "hole.glb")
	writeMeshWithHole(t, meshPath)

	outPath := filepath.Join(dir, "out.heightmap")
	var out, errw bytes.Buffer
	code := run([]string{"-mesh", meshPath, "-grid", "3x2", "-out", outPath}, &out, &errw)
	if code == 0 {
		t.Fatalf("run with a hole and no -fill succeeded, want a non-zero exit; stdout: %s", out.String())
	}
	if !strings.Contains(errw.String(), "hole") {
		t.Errorf("stderr does not mention the hole: %s", errw.String())
	}
	if _, err := os.Stat(outPath); err == nil {
		t.Error("a .heightmap was written despite the run failing on an unfilled hole")
	}

	out.Reset()
	errw.Reset()
	code = run([]string{"-mesh", meshPath, "-grid", "3x2", "-fill", "value:-1", "-out", outPath}, &out, &errw)
	if code != 0 {
		t.Fatalf("run with -fill value:-1 failed: exit %d, stderr: %s", code, errw.String())
	}
	h, err := glyph.LoadHeightmap(os.DirFS(dir), "out.heightmap")
	if err != nil {
		t.Fatalf("LoadHeightmap: %v", err)
	}
	found := false
	for _, v := range h.Heights {
		if v == -1 {
			found = true
		}
	}
	if !found {
		t.Errorf("filled heightmap has no -1 sample: %v", h.Heights)
	}
}

// writeSimpleRampMesh writes a small 2x2-quad grid mesh (a plain ramp, no
// node transform beyond identity) for CLI-level tests that only need a
// valid, hole-free mesh to convert -- the orientation/transform math itself
// is pinned by TestLoadMeshTrianglesPutsAnAsymmetricTerrainInWorldSpace.
func writeSimpleRampMesh(t *testing.T, path string) {
	t.Helper()
	var positions [][3]float32
	for lz := 0; lz < 3; lz++ {
		for lx := 0; lx < 3; lx++ {
			positions = append(positions, [3]float32{float32(lx), float32(lx), float32(lz)})
		}
	}
	idx := func(lx, lz int) uint32 { return uint32(lz*3 + lx) }
	var indices []uint32
	for lz := 0; lz < 2; lz++ {
		for lx := 0; lx < 2; lx++ {
			v00, v10 := idx(lx, lz), idx(lx+1, lz)
			v01, v11 := idx(lx, lz+1), idx(lx+1, lz+1)
			indices = append(indices, v00, v10, v11, v00, v11, v01)
		}
	}
	writeGLTFMesh(t, path, positions, indices, "Terrain")
}

// writeMeshWithHole writes two separated flat quads with a gap between them
// in X, so the grid columns between the two quads hit no geometry.
func writeMeshWithHole(t *testing.T, path string) {
	t.Helper()
	positions := [][3]float32{
		{0, 1, 0}, {1, 1, 0}, {1, 1, 1}, {0, 1, 1}, // quad A: x in [0,1]
		{4, 1, 0}, {5, 1, 0}, {5, 1, 1}, {4, 1, 1}, // quad B: x in [4,5]
	}
	indices := []uint32{
		0, 1, 2, 0, 2, 3,
		4, 5, 6, 4, 6, 7,
	}
	writeGLTFMesh(t, path, positions, indices, "Terrain")
}

func writeGLTFMesh(t *testing.T, path string, positions [][3]float32, indices []uint32, nodeName string) {
	t.Helper()
	doc := &gltf.Document{
		Asset: gltf.Asset{Version: "2.0"},
		Nodes: []*gltf.Node{{Name: nodeName, Mesh: gltf.Index(0)}},
		Meshes: []*gltf.Mesh{{
			Name:       nodeName,
			Primitives: []*gltf.Primitive{{Mode: gltf.PrimitiveTriangles}},
		}},
		Scene:  gltf.Index(0),
		Scenes: []*gltf.Scene{{Nodes: []int{0}}},
	}
	posIdx := modeler.WritePosition(doc, positions)
	idxIdx := modeler.WriteIndices(doc, indices)
	doc.Meshes[0].Primitives[0].Attributes = gltf.PrimitiveAttributes{gltf.POSITION: posIdx}
	doc.Meshes[0].Primitives[0].Indices = gltf.Index(idxIdx)
	if err := gltf.SaveBinary(doc, path); err != nil {
		t.Fatalf("SaveBinary: %v", err)
	}
}

// TestPrintInfo checks the -info summary's numbers directly (min/max/mean),
// independent of the CLI plumbing above.
func TestPrintInfo(t *testing.T) {
	h := &glyph.Heightmap{
		GridW: 2, GridH: 2, WorldW: 10, WorldD: 10,
		Heights: []float32{0, 10, 20, 30},
	}
	var buf bytes.Buffer
	if _, err := h.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	// printInfo reads through os.DirFS, so this needs a real temp file
	// rather than an in-memory fstest.MapFS.
	dir := t.TempDir()
	path := filepath.Join(dir, "t.heightmap")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := printInfo(&out, path); err != nil {
		t.Fatalf("printInfo: %v", err)
	}
	s := out.String()
	for _, want := range []string{"min 0", "max 30", "mean 15"} {
		if !strings.Contains(s, want) {
			t.Errorf("printInfo output missing %q: %s", want, s)
		}
	}
}
