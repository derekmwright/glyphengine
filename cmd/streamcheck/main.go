// Command streamcheck renders a patch grid streamed in over 100 frames and
// requires the last frame to match, byte for byte, a capture of the same grid
// created synchronously. It covers standalone meshes and arena ranges by both
// paths, and keeps a control that must differ.
//
// Verified not to be vacuous twice over. The mid-stream control changes 21716
// pixels against the reference, so the comparator is looking at something. And
// recording every staged copy from source offset 0 instead of its own -- so
// the index bytes land in the vertex buffer -- fails with "stream: pixels
// differ from the synchronous grid" while every ticket still becomes ready,
// which is the half a readiness check alone cannot see.
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

	"github.com/derekmwright/glyphengine/renderer"
	"github.com/derekmwright/glyphengine/window"
	"github.com/go-gl/mathgl/mgl32"
)

const (
	patches = 12
	frames  = 100
)

// geometry is a distinct little quad per patch: a different colour, a
// different corner offset, and for one of them a different index count, so a
// capture cannot match by every patch happening to look alike.
func geometry(n int) ([]renderer.Vertex, []uint32) {
	c := [3]float32{0.2, 0.3, 0.25}
	c[n%3] = 0.4 + float32(n)*0.05
	w, h := float32(0.06), float32(0.12)
	v := []renderer.Vertex{
		{Pos: [3]float32{-w, -h, 0.5}, Normal: [3]float32{0, 0, 1}, Color: c},
		{Pos: [3]float32{w, -h, 0.5}, Normal: [3]float32{0, 0, 1}, Color: c},
		{Pos: [3]float32{w, h, 0.5}, Normal: [3]float32{0, 0, 1}, Color: c},
		{Pos: [3]float32{-w, h + float32(n)*0.01, 0.5}, Normal: [3]float32{0, 0, 1}, Color: c},
	}
	i := []uint32{0, 1, 2, 0, 2, 3}
	if n == 0 {
		i = i[:3]
	}
	return v, i
}

func model(n int) [16]float32 {
	m := mgl32.Ident4()
	m[12] = (float32(n%4) - 1.5) * 0.4
	m[13] = (float32(n/4) - 1) * 0.5
	return m
}

// capture renders the grid and returns the frame at the end of the run.
//
// stream publishes one patch per frame; otherwise every patch exists before
// the first frame. at is the frame whose image is returned, which is what
// lets the control ask for a frame the stream has not caught up with yet.
func capture(arena, stream bool, at int, validate bool) (*image.RGBA, error) {
	w, err := window.New(640, 360, "Streamed upload check")
	if err != nil {
		return nil, err
	}
	defer w.Destroy()
	r, err := renderer.New(w, renderer.WithValidation(validate), renderer.WithMSAASamples(4))
	if err != nil {
		return nil, err
	}
	defer r.Destroy()
	var a *renderer.MeshArena
	if arena {
		if a, err = r.CreateMeshArena(renderer.MeshArenaDesc{Name: "streamed patches", Vertices: patches * 8, Indices: patches * 8, Index32: true}); err != nil {
			return nil, err
		}
	}
	tickets := make([]*renderer.UploadTicket, 0, patches)
	make1 := func(n int) (*renderer.Mesh, error) {
		v, i := geometry(n)
		switch {
		case arena && stream:
			m, t, err := a.AllocAsync(v, i)
			tickets = append(tickets, t)
			return m, err
		case arena:
			return a.Alloc(v, i)
		case stream:
			m, t, err := r.CreateIndexedMesh32Async(v, i)
			tickets = append(tickets, t)
			return m, err
		}
		return r.CreateIndexedMesh32(v, i)
	}

	light := renderer.SceneLighting{VP: mgl32.Ident4(), InvVP: mgl32.Ident4(), SunDir: [3]float32{0, 0, 1}, SkyColor: [4]float32{0.02, 0.03, 0.04, 1}}
	var draws []renderer.RenderObject
	var img *image.RGBA
	skipped, published := 0, 0
	if !stream {
		for n := range patches {
			m, err := make1(n)
			if err != nil {
				return nil, err
			}
			draws = append(draws, renderer.RenderObject{Mesh: m, Model: model(n), MVP: model(n), Color: [3]float32{1, 1, 1}, Emissive: true, DoubleSided: true, NoCastShadow: true})
			published++
		}
	}
	for frame := range frames {
		w.PollEvents()
		if stream && published < patches {
			m, err := make1(published)
			if err != nil {
				return nil, err
			}
			draws = append(draws, renderer.RenderObject{Mesh: m, Model: model(published), MVP: model(published), Color: [3]float32{1, 1, 1}, Emissive: true, DoubleSided: true, NoCastShadow: true})
			published++
		}
		if err := r.DrawFrame(draws, nil, nil, nil, nil, light); err != nil {
			return nil, err
		}
		skipped += r.Stats().UploadsSkipped
		if frame == at {
			if img, err = r.CaptureFrame(); err != nil {
				return nil, err
			}
		}
	}
	if stream {
		if skipped == 0 {
			return nil, fmt.Errorf("arena=%v: no draw was ever held back; the skip path never ran", arena)
		}
		for n, t := range tickets {
			if !t.Ready() {
				return nil, fmt.Errorf("arena=%v: ticket %d never became ready", arena, n)
			}
		}
		if got := r.ResourceCounts().PendingUploads; got != 0 {
			return nil, fmt.Errorf("arena=%v: %d staging buffers still retained", arena, got)
		}
	}
	if got := r.Stats().UploadsSkipped; got != 0 {
		return nil, fmt.Errorf("arena=%v stream=%v: the last frame still skipped %d draws", arena, stream, got)
	}
	if a != nil {
		for _, d := range draws {
			a.Free(d.Mesh)
		}
		r.DestroyMeshArena(a)
	}
	fmt.Printf("arena=%v stream=%v frame %d: %d patches, %d draws held back over the run\n", arena, stream, at, published, skipped)
	return img, nil
}

func write(dir, name string, img *image.RGBA) error {
	f, err := os.Create(filepath.Join(dir, name+".png"))
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}

func run(dir string, validate bool) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	var base *image.RGBA
	for _, c := range []struct {
		name          string
		arena, stream bool
	}{
		{"sync", false, false},
		{"stream", false, true},
		{"arena", true, false},
		{"arena-stream", true, true},
	} {
		img, err := capture(c.arena, c.stream, frames-1, validate)
		if err != nil {
			return err
		}
		if err := write(dir, c.name, img); err != nil {
			return err
		}
		if base == nil {
			base = img
			continue
		}
		if !bytes.Equal(base.Pix, img.Pix) {
			return fmt.Errorf("%s: pixels differ from the synchronous grid", c.name)
		}
		fmt.Printf("%s: byte-identical RGBA (%d bytes)\n", c.name, len(img.Pix))
	}
	// The control: a frame the stream has not caught up with. Without it a
	// comparator that matched two identical empty frames would pass every
	// check above.
	control, err := capture(false, true, 2, validate)
	if err != nil {
		return err
	}
	if err := write(dir, "control", control); err != nil {
		return err
	}
	different := 0
	for i := 0; i < len(base.Pix); i += 4 {
		if !bytes.Equal(base.Pix[i:i+4], control.Pix[i:i+4]) {
			different++
		}
	}
	if different < 1000 {
		return fmt.Errorf("mid-stream control: only %d pixels changed", different)
	}
	fmt.Printf("mid-stream control: %d changed pixels (floor 1000)\n", different)
	return nil
}

func main() {
	runtime.LockOSThread()
	dir := flag.String("out", ".task/stream", "capture directory")
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
	fmt.Println("streamcheck: PASS")
}
