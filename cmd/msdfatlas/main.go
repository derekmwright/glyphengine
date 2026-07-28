// Command msdfatlas generates a multi-channel signed distance field font
// atlas from a TTF or OTF file.
//
// It replaces msdf-atlas-gen for this engine's purposes. That tool is a
// separate C++ build, which means every contributor has to install it, keep
// its version matched, and cannot invoke it from `go generate`. This does the
// same job with the Go toolchain you already have:
//
//	go run github.com/derekmwright/glyphengine/cmd/msdfatlas \
//	    -font MyFont.ttf -out assets/fonts/body
//
// That writes assets/fonts/body.png and assets/fonts/body.json, ready for
// renderer.LoadFont. The JSON matches msdf-atlas-gen's schema, so atlases from
// either generator are interchangeable.
//
// Defaults match what this engine has always used: 48px em, 4px range,
// printable ASCII.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"golang.org/x/image/font/gofont/goregular"

	"github.com/derekmwright/glyphengine/msdf"
)

func main() {
	log.SetFlags(0)

	fontPath := flag.String("font", "", "path to a .ttf or .otf font")
	builtin := flag.Bool("builtin", false, "use Go Regular, the Go project's own BSD-licensed typeface, instead of -font")
	out := flag.String("out", "", "output path without extension; writes <out>.png and <out>.json (required)")
	size := flag.Int("size", 48, "em size in pixels each glyph is rendered at")
	rng := flag.Float64("range", 4, "distance field range in pixels")
	charset := flag.String("charset", "", "explicit characters to include; defaults to printable ASCII")
	corner := flag.Float64("corner-angle", 30, "degrees of direction change treated as a corner")
	flag.Parse()

	if *out == "" || (*fontPath != "") == *builtin {
		flag.Usage()
		log.Fatal("\n-out is required, along with exactly one of -font or -builtin")
	}

	// -builtin exists so the tool runs with no inputs at all, which makes it
	// possible to try out in one command and gives the examples a font with no
	// binary blob in the repository and no attribution to propagate.
	data := goregular.TTF
	if *fontPath != "" {
		var err error
		data, err = os.ReadFile(*fontPath)
		if err != nil {
			log.Fatalf("read font: %v", err)
		}
	}

	opt := msdf.Options{
		Size:        *size,
		Range:       *rng,
		CornerAngle: *corner,
	}
	if *charset != "" {
		opt.Charset = []rune(*charset)
	}

	atlas, err := msdf.Generate(data, opt)
	if err != nil {
		log.Fatalf("generate: %v", err)
	}

	base := strings.TrimSuffix(*out, ".png")
	base = strings.TrimSuffix(base, ".json")

	if err := atlas.WritePNG(base + ".png"); err != nil {
		log.Fatalf("%v", err)
	}
	if err := atlas.WriteJSON(base + ".json"); err != nil {
		log.Fatalf("%v", err)
	}

	fmt.Printf("%s.png  %dx%d, %d glyphs, %gpx em, %gpx range\n",
		base, atlas.Meta.Atlas.Width, atlas.Meta.Atlas.Height,
		len(atlas.Meta.Glyphs), atlas.Meta.Atlas.Size, atlas.Meta.Atlas.DistanceRange)
}
