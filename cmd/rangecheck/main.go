// Command rangecheck pins rendered equivalence of independent meshes and
// shared ranges, including offsets, index widths, transforms and retirement.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"image"
	"image/png"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/derekmwright/glyphengine/renderer"
	"github.com/derekmwright/glyphengine/window"
	"github.com/go-gl/mathgl/mgl32"
)

func geometry(n int) ([]renderer.Vertex, []uint32) {
	c := [3]float32{0.2, 0.3, 0.25}
	c[n] = 0.8
	v := []renderer.Vertex{
		{Pos: [3]float32{-0.125, -0.25, 0.5}, Normal: [3]float32{0, 0, 1}, Color: c},
		{Pos: [3]float32{0.125, -0.25, 0.5}, Normal: [3]float32{0, 0, 1}, Color: c},
		{Pos: [3]float32{0.125, 0.25, 0.5}, Normal: [3]float32{0, 0, 1}, Color: c},
		{Pos: [3]float32{-0.125, 0.25, 0.5}, Normal: [3]float32{0, 0, 1}, Color: c},
	}
	i := []uint32{0, 1, 2, 0, 2, 3}
	if n == 0 {
		i = i[:3]
	}
	return v, i
}

func capture(mode string, wide, validate, omit bool) (*image.RGBA, error) {
	shadow := strings.HasPrefix(mode, "shadow-")
	mode = strings.TrimPrefix(mode, "shadow-")
	w, err := window.New(640, 360, "Mesh ranges check")
	if err != nil {
		return nil, err
	}
	defer w.Destroy()
	r, err := renderer.New(w, renderer.WithValidation(validate), renderer.WithMSAASamples(4))
	if err != nil {
		return nil, err
	}
	defer r.Destroy()
	r.SetMeshRangeBatching(mode == "indirect")
	var a *renderer.MeshArena
	if mode != "separate" {
		a, err = r.CreateMeshArena(renderer.MeshArenaDesc{Name: "three distinct ranges", Vertices: 32, Indices: 48, Index32: wide})
		if err != nil {
			return nil, err
		}
	}
	makeMesh := func(n int) (*renderer.Mesh, error) {
		v, i := geometry(n)
		if a != nil {
			return a.Alloc(v, i)
		}
		if wide {
			return r.CreateIndexedMesh32(v, i)
		}
		idx := make([]uint16, len(i))
		for j, x := range i {
			idx[j] = uint16(x)
		}
		return r.CreateIndexedMesh(v, idx)
	}
	draws := make([]renderer.RenderObject, 3)
	for n := range draws {
		m, err := makeMesh(n)
		if err != nil {
			return nil, err
		}
		model := mgl32.Ident4()
		model[12] = float32(n-1) * 0.5
		model[0] = []float32{1, -1, 0.5}[n]
		draws[n] = renderer.RenderObject{Mesh: m, Model: model, MVP: model, Color: [3]float32{1, 1, 1}, Emissive: !shadow, DoubleSided: true, NoCastShadow: !shadow}
		if strings.HasPrefix(mode, "lod-") {
			set, err := r.CreateInstanceSetLOD(renderer.InstanceSetLODDesc{GPU: mode == "lod-gpu", Levels: []renderer.LODLevel{{Mesh: m, MaxDistance: 100}}, Capacity: 1, ShadowLevel: -1}, []renderer.MeshInstance{{Model: model, Tint: [4]float32{1, 1, 1, 1}}})
			if err != nil {
				return nil, err
			}
			draws[n].InstancesLOD = set
		}
		if mode == "instanced" {
			set, err := r.CreateInstanceSet(m, 1, []renderer.MeshInstance{{Model: model, Tint: [4]float32{1, 1, 1, 1}}})
			if err != nil {
				return nil, err
			}
			draws[n].Instances = set
		}
	}
	light := renderer.SceneLighting{VP: mgl32.Ident4(), InvVP: mgl32.Ident4(), SunDir: [3]float32{0, 0, 1}, SkyColor: [4]float32{0.02, 0.03, 0.04, 1}}
	if shadow {
		light.ShadowEnabled = true
		for i := range light.CascadeVPs {
			light.CascadeVPs[i] = mgl32.Ident4()
		}
		light.SunColor = [3]float32{0.5, 0.5, 0.5}
		light.Ambient = [3]float32{0.2, 0.2, 0.2}
		light.PointPos = [3]float32{0, 0, 3}
		light.PointRange = 10
		light.PointColor = [3]float32{0.4, 0.3, 0.2}
	}
	for frame := 0; frame < 18; frame++ {
		w.PollEvents()
		if a != nil && (mode == "ranges" || mode == "indirect") && frame > 0 && frame%4 == 0 {
			old := draws[1].Mesh
			next, err := makeMesh(1)
			if err != nil {
				return nil, err
			}
			draws[1].Mesh = next
			a.Free(old)
		}
		visible := draws
		if omit {
			visible = append([]renderer.RenderObject{draws[0]}, draws[2])
		}
		if err := r.DrawFrame(visible, nil, nil, nil, nil, light); err != nil {
			return nil, err
		}
	}
	stats := r.Stats()
	want := 3
	if omit {
		want = 2
	}
	if !shadow && stats.Instances != want {
		return nil, fmt.Errorf("%s: got %d instances, want %d", mode, stats.Instances, want)
	}
	img, err := r.CaptureFrame()
	if err != nil {
		return nil, err
	}
	if a != nil {
		for _, d := range draws {
			a.Free(d.Mesh)
		}
		r.DestroyMeshArena(a)
	}
	if shadow && stats.Instances <= want {
		return nil, fmt.Errorf("shadow paths submitted no extra geometry")
	}
	fmt.Printf("%s index32=%v shadow=%v omit=%v: draws=%d instances=%d triangles=%d\n", mode, wide, shadow, omit, stats.DrawCalls, stats.Instances, stats.Triangles)
	return img, nil
}

func run(dir string, validate bool) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	for _, wide := range []bool{false, true} {
		var base *image.RGBA
		for _, mode := range []string{"separate", "ranges", "indirect", "indirect", "instanced", "lod-cpu", "lod-gpu"} {
			img, err := capture(mode, wide, validate, false)
			if err != nil {
				return err
			}
			path := filepath.Join(dir, fmt.Sprintf("%s-%v.png", mode, wide))
			f, err := os.Create(path)
			if err != nil {
				return err
			}
			err = png.Encode(f, img)
			f.Close()
			if err != nil {
				return err
			}
			if base == nil {
				base = img
				continue
			}
			if !bytes.Equal(base.Pix, img.Pix) {
				return fmt.Errorf("%s index32=%v: pixels differ from separate meshes", mode, wide)
			}
			fmt.Printf("%s index32=%v: byte-identical RGBA (%d bytes)\n", mode, wide, len(img.Pix))
		}
		control, err := capture("ranges", wide, validate, true)
		if err != nil {
			return err
		}
		different := 0
		for i := 0; i < len(base.Pix); i += 4 {
			if !bytes.Equal(base.Pix[i:i+4], control.Pix[i:i+4]) {
				different++
			}
		}
		if different < 1000 {
			return fmt.Errorf("visibility control: only %d pixels changed", different)
		}
		fmt.Printf("index32=%v missing-middle control: %d changed pixels (floor 1000)\n", wide, different)
		var shadowBase *image.RGBA
		for _, mode := range []string{"shadow-separate", "shadow-ranges", "shadow-indirect"} {
			img, err := capture(mode, wide, validate, false)
			if err != nil {
				return err
			}
			if shadowBase == nil {
				shadowBase = img
				continue
			}
			if !bytes.Equal(shadowBase.Pix, img.Pix) {
				return fmt.Errorf("%s index32=%v: shadow capture differs", mode, wide)
			}
			fmt.Printf("%s index32=%v: byte-identical shadow RGBA\n", mode, wide)
		}
	}
	return nil
}

func main() {
	runtime.LockOSThread()
	dir := flag.String("out", ".task/ranges", "capture directory")
	validation := flag.Bool("validate", true, "require silent Vulkan validation")
	flag.Parse()
	var messages bytes.Buffer
	log.SetOutput(io.MultiWriter(os.Stderr, &messages))
	err := run(*dir, *validation)
	log.SetOutput(os.Stderr)
	if err != nil {
		log.Fatal(err)
	}
	if bytes.Contains(messages.Bytes(), []byte("VULKAN ERROR")) || bytes.Contains(messages.Bytes(), []byte("VULKAN WARNING")) {
		log.Fatal("validation was not silent")
	}
	if *validation && !bytes.Contains(messages.Bytes(), []byte("Vulkan validation layer enabled")) {
		log.Fatal("validation layer did not run")
	}
	fmt.Println("rangecheck: PASS")
}
