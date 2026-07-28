// Command 01-triangle is the smallest possible GlyphEngine program: it opens a
// window and draws the classic tri-color triangle.
//
// This is the toolchain proof. If this runs, then CGo, your C compiler, the
// Vulkan loader, your GPU driver, GLFW, and the whole
// instance -> device -> swapchain -> pipeline -> present path all work. Every
// other example builds on top of exactly this.
//
// It loads nothing from disk. The triangle's vertices and colors are baked into
// the shader and indexed by gl_VertexIndex, so there is no asset directory to
// find and no working directory to get wrong.
//
//	go run ./01-triangle              # windowed 800x600
//	go run ./01-triangle -frames 60   # render 60 frames, then exit (CI smoke test)
package main

import (
	"flag"
	"log"
	"runtime"

	"github.com/derekmwright/glyphengine/input"
	"github.com/derekmwright/glyphengine/renderer"
	"github.com/derekmwright/glyphengine/window"
)

func init() {
	// GLFW must be called from the thread that initialized it.
	runtime.LockOSThread()
}

func main() {
	width := flag.Int("width", 800, "window width")
	height := flag.Int("height", 600, "window height")
	fullscreen := flag.Bool("fullscreen", false, "run fullscreen on the primary monitor")
	frames := flag.Int("frames", 0, "render N frames then exit (0 = run until closed)")
	shot := flag.String("screenshot", "", "write a PNG of the last frame to this path")
	flag.Parse()

	var opts []window.Option
	if *fullscreen {
		opts = append(opts, window.WithFullscreen())
	}

	win, err := window.New(*width, *height, "GlyphEngine - 01 Triangle", opts...)
	if err != nil {
		log.Fatalf("create window: %v", err)
	}
	defer win.Destroy()

	rend, err := renderer.New(win)
	if err != nil {
		log.Fatalf("create renderer: %v", err)
	}
	defer rend.Destroy()

	in := input.New(win.Handle())

	log.Println("Triangle running. Press Escape to quit.")

	for count := 0; !win.ShouldClose(); count++ {
		in.Update()
		win.PollEvents()

		if win.WasResized() {
			rend.NotifyResize()
		}
		if in.KeyPressed(input.KeyEscape) {
			win.Close()
		}

		// Minimized windows have a zero-area swapchain; skip drawing.
		if rend.Minimized() {
			continue
		}

		if err := rend.DrawTriangle(); err != nil {
			log.Fatalf("draw: %v", err)
		}

		if *frames > 0 && count+1 >= *frames {
			// This example drives the renderer directly rather than through
			// Engine, so it calls SaveScreenshot itself instead of using
			// glyphengine.WithScreenshot.
			if *shot != "" {
				if err := rend.SaveScreenshot(*shot); err != nil {
					log.Printf("screenshot: %v", err)
				} else {
					log.Printf("wrote screenshot %s", *shot)
				}
			}
			log.Printf("rendered %d frames, exiting", count+1)
			break
		}
	}
}
