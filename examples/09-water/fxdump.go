package main

import (
	"encoding/json"
	"os"

	glyph "github.com/derekmwright/glyphengine"
)

// fxDump is what -fxdump writes after the last frame: everything `task waterfx`
// needs to predict the frame on the CPU and compare it with what was drawn. The
// camera puts a grid point on a pixel, the grid and the options let the check
// evaluate the same wave function there, and the foam target is what the GPU
// actually accumulated, read back.
type fxDump struct {
	Frames  int                `json:"frames"`
	Elapsed float32            `json:"elapsed"`
	Width   int                `json:"width"`
	Height  int                `json:"height"`
	VP      [16]float32        `json:"vp"`
	SunDir  [3]float32         `json:"sunDir"`
	Options glyph.WaterOptions `json:"options"`
	Grid    glyph.WaterGrid    `json:"grid"`
	// Foam is the target's coverage, one value per grid point in rows along X,
	// or absent when the surface has no target.
	Foam []float32 `json:"foam,omitempty"`
}

func (g *game) writeFXDump(e *glyph.Engine, path string) error {
	w, h := e.Renderer().Extent()
	d := fxDump{
		Frames: e.FrameCount(), Elapsed: e.Elapsed(), Width: w, Height: h,
		VP: e.ViewProjection(), Options: g.opts, Grid: g.grid,
	}
	if env := e.Scene.Env; env != nil {
		d.SunDir = env.State().SunDir
	}
	if f := g.surface.WaterFoam(); f != nil {
		cov, err := f.Coverage()
		if err != nil {
			return err
		}
		d.Foam = cov
	}
	out, err := json.Marshal(d)
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o644)
}
