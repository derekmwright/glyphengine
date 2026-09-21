// Command shadowcheck checks the three captures from task shadowcoverage.
package main

import (
	"flag"
	"fmt"
	"image"
	"image/png"
	"log"
	"math"
	"os"
)

func load(path string) image.Image {
	f, err := os.Open(path)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	i, err := png.Decode(f)
	if err != nil {
		log.Fatal(err)
	}
	if i.Bounds().Dx() != 800 || i.Bounds().Dy() != 600 {
		log.Fatal("expected 800x600 capture")
	}
	return i
}
func mean(i image.Image, b image.Rectangle) float64 {
	var sum float64
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, b, _ := i.At(x, y).RGBA()
			sum += float64(r+g+b) / (3 * 257)
		}
	}
	return sum / float64(b.Dx()*b.Dy())
}
func main() {
	on := flag.String("on", "", "extended coverage capture")
	off := flag.String("off", "", "default coverage capture")
	control := flag.String("control", "", "extended coverage without distant caster")
	flag.Parse()
	a, b, c := load(*on), load(*off), load(*control)
	box := image.Rect(370, 280, 430, 320)
	x, y, z := mean(a, box), mean(b, box), mean(c, box)
	fmt.Printf("receiver mean /255: extended %.2f, default %.2f, no caster %.2f; reduction %.2f (%.2fx)\n", x, y, z, y-x, y/math.Max(x, 1))
	if y < 100 || z < 100 || y-x < 30 || y/math.Max(x, 1) < 1.3 || math.Abs(y-z) > 2 {
		log.Fatal("distant caster did not produce a visible shadow, or control is invalid")
	}
	// Outside the distant shadow, the near prop still casts the same shadow.
	near := image.Rect(140, 295, 165, 310)
	p, q := mean(b, near), mean(c, near)
	fmt.Printf("near prop mean /255: default %.2f, extended %.2f\n", p, q)
	if math.Abs(p-q) > 2 || y-p < 30 || z-q < 30 {
		log.Fatal("near prop changed with caster coverage")
	}
}
