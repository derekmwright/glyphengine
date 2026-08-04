package main

import (
	"image/png"
	"log"
	"os"

	glyph "github.com/derekmwright/glyphengine"
)

func dumpImpostorAtlas(e *glyph.Engine, path string) {
	img, err := e.Renderer().GrassImpostorAtlas()
	if err != nil {
		log.Printf("impostor dump failed: %v", err)
		return
	}
	f, err := os.Create(path)
	if err != nil {
		log.Printf("impostor create failed: %v", err)
		return
	}
	defer f.Close()
	png.Encode(f, img)
	log.Printf("wrote impostor atlas to %s", path)
}
