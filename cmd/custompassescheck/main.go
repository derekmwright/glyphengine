// Command custompassescheck compares the worked example's fixed-camera captures.
// All regions are in its 960x540 output; reject any other extent rather than
// silently comparing an empty or misplaced region.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"image"
	"image/png"
	"log"
	"os"
)

func main() {
	onPath := flag.String("on", "", "passes-on PNG")
	offPath := flag.String("off", "", "passes-off PNG")
	repeatPath := flag.String("repeat", "", "second passes-on PNG (require byte identity)")
	flag.Parse()
	on, onBytes := read(*onPath)
	off, _ := read(*offPath)
	repeat, repeatBytes := read(*repeatPath)
	failed := false
	for _, region := range []struct {
		name string
		box  image.Rectangle
		min  int
	}{
		{"ground pattern", image.Rect(180, 285, 930, 470), 30000},
		{"fog above horizon", image.Rect(100, 175, 860, 200), 15000},
		{"untouched upper sky", image.Rect(100, 20, 860, 80), 0},
	} {
		changed, visible, contrast := difference(on, off, region.box)
		fmt.Printf("%s: %d pixels differ, %d >=8/255, max contrast %d/255", region.name, changed, visible, contrast)
		if region.min == 0 {
			fmt.Println("; require 0 differing pixels")
			failed = failed || changed != 0
		} else {
			fmt.Printf("; require >=%d visible pixels\n", region.min)
			failed = failed || visible < region.min
		}
	}
	changed, _, contrast := difference(on, repeat, on.Bounds())
	identical := bytes.Equal(onBytes, repeatBytes)
	fmt.Printf("repeat: byte-identical=%v; %d pixels differ, max contrast %d/255\n", identical, changed, contrast)
	if failed || !identical {
		log.Fatal("custompasses: FAIL")
	}
	fmt.Println("custompasses: PASS")
}

func read(path string) (image.Image, []byte) {
	b, err := os.ReadFile(path)
	if err != nil {
		log.Fatal(err)
	}
	im, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		log.Fatalf("%s: %v", path, err)
	}
	if im.Bounds() != image.Rect(0, 0, 960, 540) {
		log.Fatalf("%s: expected 960x540, got %v", path, im.Bounds())
	}
	return im, b
}

func difference(a, b image.Image, box image.Rectangle) (changed, visible, contrast int) {
	for y := box.Min.Y; y < box.Max.Y; y++ {
		for x := box.Min.X; x < box.Max.X; x++ {
			ar, ag, ab, aa := a.At(x, y).RGBA()
			br, bg, bb, ba := b.At(x, y).RGBA()
			delta := max(abs(int(ar)-int(br)), abs(int(ag)-int(bg)), abs(int(ab)-int(bb))) / 257
			if ar != br || ag != bg || ab != bb || aa != ba {
				changed++
			}
			if delta >= 8 {
				visible++
			}
			contrast = max(contrast, delta)
		}
	}
	return
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
