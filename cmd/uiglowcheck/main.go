// Command uiglowcheck asserts that the screen-space UI's HDR layer glows where
// it is asked to, stays where it belongs, and changes nothing where it is not.
//
//	task uiglow
//
// The layer exists so a UI element can be brighter than 1 and bleed light onto
// the elements around it. Three things can go wrong with that, and each of them
// is green under every other gate in this repository: `task smoke` renders the
// frame, `task validate` is silent, `task determinism` is happy because the same
// wrong image comes out every time, and `task hud` measures text over water on a
// path this feature is not even on.
//
//	the glow stops being drawn          -- the composite unbound, the bloom chain
//	                                       skipped, the threshold left above what
//	                                       anything can reach
//	the glow stops staying in its lane  -- a leak that lights elements that asked
//	                                       for nothing, which reads as "the HUD
//	                                       got brighter" rather than as a bug
//	the layer stops being neutral       -- premultiplied alpha regressing to
//	                                       straight alpha, which puts a dark
//	                                       fringe on every antialiased edge and
//	                                       reads as "the antialiasing is broken"
//
// So this takes three captures of the same scene at the same fixed clock and
// differences two pairs of them:
//
//	direct  the UI drawn straight onto the swapchain, as it is by default
//	base    the same geometry through the layer, with nothing asking to glow
//	glow    the same geometry again, with three elements emitting
//
// direct against base is the LAYER, over identical geometry. It must come out
// the same picture: the layer's resolve is identity for everything at or below
// 1, so the only difference allowed is one 8-bit rounding step from blending in
// a float layer and encoding once instead of blending into an 8-bit sRGB target.
// That is the arm the premultiply failure lands in, and it lands hard -- with
// the layer's blend left at SrcAlpha while the shaders premultiply, 13-ui
// differed on 33745 pixels at a maximum of 55/255 against a tolerance of 2.
//
// base against glow is the GLOW, and nothing else: same scene, same geometry,
// same frame, one set of Glow values. Four regions, because no one of them can
// carry the whole statement:
//
//   - lit is an element that emits. It fails when nothing is drawn.
//   - near is a NON-emitting element a few pixels away. It fails when the glow
//     is drawn but does not cross between elements, which is the only part of
//     this a game could not already do for itself with WithShaders.
//   - far is UI at the other end of the frame that must not move. It fails when
//     the glow leaks into elements that asked for nothing -- a push constant
//     that is not reset, a per-object value applied per pass.
//   - scene is a patch of the 3D scene with no UI over it at all, which must not
//     move AT ALL. The UI is composited after the tonemap, so the only way it
//     can reach the scene is through a bug.
//
// lit alone is not enough and the reason is worth stating: a build that applied
// the glow to every UI draw would pass it comfortably, and look like a HUD
// someone turned up. far is what that fails.
package main

import (
	"flag"
	"fmt"
	"image"
	"image/png"
	"os"
	"strconv"
	"strings"
)

func main() {
	direct := flag.String("direct", "", "capture of the UI drawn straight onto the swapchain")
	base := flag.String("base", "", "capture through the layer with nothing glowing")
	glow := flag.String("glow", "", "capture through the layer with elements glowing")

	litArg := flag.String("lit", "", "x,y,w,h of an element that emits")
	nearArg := flag.String("near", "", "x,y,w,h of a non-emitting element beside it")
	farArg := flag.String("far", "", "x,y,w,h of UI far away that must not move")
	sceneArg := flag.String("scene", "", "x,y,w,h of 3D scene with no UI over it")

	minLit := flag.Float64("minlit", 25, "the emitting element must gain at least this much mean luma")
	minNear := flag.Float64("minnear", 3, "the element beside it must gain at least this much")
	maxFar := flag.Float64("maxfar", 0.5, "distant UI may gain at most this much")
	maxScene := flag.Float64("maxscene", 0, "the scene may gain at most this much")

	// The base luma the lit box must already have WITHOUT any glow. A run where
	// the element was never drawn would report every box gaining nothing and
	// pass the far and scene arms while failing lit for the wrong reason; this
	// says which. It is the same guard hudcheck puts on its median ink.
	litBase := flag.Float64("litbase", 80, "the lit box must be this bright before it glows")

	maxDelta := flag.Int("maxdelta", 2, "largest 8-bit channel difference allowed between direct and base")
	flag.Parse()

	if *base == "" || *glow == "" {
		fmt.Fprintln(os.Stderr, "uiglowcheck: -base and -glow are required")
		os.Exit(2)
	}

	baseImg := mustLoad(*base)
	glowImg := mustLoad(*glow)
	if baseImg.Bounds() != glowImg.Bounds() {
		fmt.Fprintf(os.Stderr, "uiglowcheck: captures differ in size: %v vs %v\n", baseImg.Bounds(), glowImg.Bounds())
		os.Exit(2)
	}

	status := 0

	// ── the layer is neutral ──
	if *direct != "" {
		directImg := mustLoad(*direct)
		if directImg.Bounds() != baseImg.Bounds() {
			fmt.Fprintf(os.Stderr, "uiglowcheck: captures differ in size: %v vs %v\n", directImg.Bounds(), baseImg.Bounds())
			os.Exit(2)
		}
		differing, over1, max := compare(directImg, baseImg)
		fmt.Printf("layer vs direct: %d px differ, %d of them above 1/255, max delta %d (tolerance %d)\n",
			differing, over1, max, *maxDelta)
		if max > *maxDelta {
			fmt.Printf("  FAIL: the layer is not neutral. Its resolve is supposed to be identity for\n")
			fmt.Printf("  everything at or below 1, so a UI colour means exactly what it means on the\n")
			fmt.Printf("  direct path. A difference this size on antialiased edges is premultiplied\n")
			fmt.Printf("  alpha gone: check overlayBlend, the layer's (0,0,0,0) clear, and the\n")
			fmt.Printf("  premultiply branch in ui.frag and msdf.frag -- all three have to agree\n")
			status = 1
		}
	}

	// ── the glow ──
	type region struct {
		name    string
		arg     string
		floor   bool // true = must gain at least limit; false = at most
		limit   float64
		explain string
	}
	regions := []region{
		{"lit", *litArg, true, *minLit,
			"nothing is being drawn -- check that the composite still binds uiResolvePipeline,\n" +
				"that recordBloom runs over the UI layer, and that SetUIGlow's threshold is still\n" +
				"below what the element's colour times (1 + Glow) can reach"},
		{"near", *nearArg, true, *minNear,
			"the glow is drawn but does not cross between elements, which is the only part of\n" +
				"this a game could not already do for itself -- check the bloom radius and that\n" +
				"the chain reads the LAYER rather than the element"},
		{"far", *farArg, false, *maxFar,
			"the glow is leaking into elements that asked for none -- check that the per-object\n" +
				"Glow is reset between draws (commandScratch.resetPC) rather than persisting"},
		{"scene", *sceneArg, false, *maxScene,
			"the UI reached the 3D scene. It is composited after the tonemap, so this is not a\n" +
				"look decision going wrong, it is the composite writing where it should not"},
	}

	for _, reg := range regions {
		if reg.arg == "" {
			continue
		}
		box, err := parseBox(reg.arg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "uiglowcheck: -%s: %v\n", reg.name, err)
			os.Exit(2)
		}
		if !box.In(baseImg.Bounds()) {
			fmt.Fprintf(os.Stderr, "uiglowcheck: -%s %v is outside the capture %v\n", reg.name, box, baseImg.Bounds())
			os.Exit(2)
		}
		added, b := gain(glowImg, baseImg, box)
		word := "ceiling"
		if reg.floor {
			word = "floor"
		}
		fmt.Printf("%-6s %-18v %+7.2f mean luma (background %6.2f, %s %.2f)\n",
			reg.name, reg.arg, added, b, word, reg.limit)

		if reg.name == "lit" && b < *litBase {
			fmt.Printf("  FAIL: the lit box sits at %.1f before anything glows, under %.1f -- the element\n", b, *litBase)
			fmt.Printf("  it is supposed to measure is not in the capture, so every number below is\n")
			fmt.Printf("  about an empty box and this check proves nothing\n")
			status = 1
		}
		if reg.floor && added < reg.limit {
			fmt.Printf("  FAIL: %s gained %.2f, under its floor of %.2f\n", reg.name, added, reg.limit)
			fmt.Printf("  %s\n", indent(reg.explain))
			status = 1
		}
		if !reg.floor && added > reg.limit {
			fmt.Printf("  FAIL: %s gained %.2f, over its ceiling of %.2f\n", reg.name, added, reg.limit)
			fmt.Printf("  %s\n", indent(reg.explain))
			status = 1
		}
	}

	if status != 0 {
		os.Exit(status)
	}
	fmt.Println("the UI glows where it was asked to, crosses to its neighbour, and moves nothing else")
}

func indent(s string) string { return strings.ReplaceAll(s, "\n", "\n  ") }

// compare returns how many pixels differ at all, how many differ by more than
// one 8-bit step, and the largest single-channel difference.
//
// One step is the floor this measurement can reach rather than a tolerance
// anyone chose: the two captures blend the same colours in different precisions
// -- a half-float layer encoded once against an 8-bit sRGB target blended into
// repeatedly -- so the last bit is expected to move on a blended pixel and
// nowhere else.
func compare(a, b image.Image) (differing, over1, max int) {
	r := a.Bounds()
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			ar, ag, ab, _ := a.At(x, y).RGBA()
			br, bg, bb, _ := b.At(x, y).RGBA()
			d := 0
			for _, p := range [][2]uint32{{ar, br}, {ag, bg}, {ab, bb}} {
				v := int(p[0]>>8) - int(p[1]>>8)
				if v < 0 {
					v = -v
				}
				if v > d {
					d = v
				}
			}
			if d > 0 {
				differing++
				if d > 1 {
					over1++
				}
				if d > max {
					max = d
				}
			}
		}
	}
	return differing, over1, max
}

// gain returns the mean luma the glow added over a region, and the mean luma of
// the region without it -- the second so a reader can see whether the first is a
// small change to something bright or a large change to something dark.
func gain(withGlow, without image.Image, r image.Rectangle) (added, base float64) {
	var sumA, sumB float64
	n := 0
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			sumA += luma(withGlow, x, y)
			sumB += luma(without, x, y)
			n++
		}
	}
	if n == 0 {
		return 0, 0
	}
	return (sumA - sumB) / float64(n), sumB / float64(n)
}

// luma is Rec. 601 luminance, the same measure shaftcheck, blendcheck and
// skycheck use.
func luma(img image.Image, x, y int) float64 {
	r, g, b, _ := img.At(x, y).RGBA()
	return 0.299*float64(r>>8) + 0.587*float64(g>>8) + 0.114*float64(b>>8)
}

func parseBox(s string) (image.Rectangle, error) {
	parts := strings.Split(s, ",")
	if len(parts) != 4 {
		return image.Rectangle{}, fmt.Errorf("want x,y,w,h, got %q", s)
	}
	var v [4]int
	for i, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			return image.Rectangle{}, fmt.Errorf("%q is not a number", p)
		}
		v[i] = n
	}
	if v[2] <= 0 || v[3] <= 0 {
		return image.Rectangle{}, fmt.Errorf("width and height must be positive, got %dx%d", v[2], v[3])
	}
	return image.Rect(v[0], v[1], v[0]+v[2], v[1]+v[3]), nil
}

func mustLoad(path string) image.Image {
	f, err := os.Open(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "uiglowcheck: %v\n", err)
		os.Exit(2)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		fmt.Fprintf(os.Stderr, "uiglowcheck: decode %s: %v\n", path, err)
		os.Exit(2)
	}
	return img
}
