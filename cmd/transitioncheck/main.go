// Command transitioncheck asserts that a yamlui `transition:` block actually
// moves a widget while the game is paused, lands exactly where a widget with no
// block lands, and moves nothing else.
//
//	task transition
//
// This is issue #148's arm, and the thing it exists to catch is the reason the
// block needed a new clock at all. `Engine.Elapsed` is scaled by SetTimeScale,
// so a fade driven off it stops dead at scale 0 -- and a modal dialog is very
// nearly always opened while the game is paused. A transition wired to the
// wrong clock therefore works perfectly in every unit test, in every example
// that does not pause, and never once in the situation it was built for.
//
// Nothing else in the repository can see that. `task ci` drives the widget tree
// with a clock a test hands it, so the engine's two clocks never enter the
// picture; `task smoke` renders a frame; `task validate` is silent whatever the
// dialog is doing; `task determinism` is happy with a dialog that never opens,
// because it never opens the same way twice in a row. The failure is a picture
// of a menu that is simply not there.
//
// So this reads pixels, out of captures of one paused scene:
//
//	-hidden   the frame before `visible` flips: the state everything starts from
//	-rest     the frame the in-transition finishes
//	-mid      a frame partway through, which must sit BETWEEN the two
//	-same     a frame past the end, which must equal -rest pixel for pixel
//
// and checks four things, which fail differently:
//
//   - the interior box of a fading widget (-box) reads a colour strictly
//     between the hidden and the rest colour, by at least -sep 8-bit steps on
//     the channel that separates them best. A transition that never ran reads
//     the hidden colour; one that cut straight to opaque reads the rest colour.
//     Both are exactly what a wrong clock and a missing block look like.
//   - the widget's top edge (-column) has travelled between -slidemin and
//     -slidemax pixels from where it rests. A fade with no slide reads 0; a
//     widget that jumped to its resting place reads 0 as well, which is why the
//     colour arm alone is not enough.
//   - the hidden capture has NO edge in that column at all, so "between" is
//     measured against a state that really is empty.
//   - every -scene box is byte-identical across every capture. The scene is
//     paused; if it moved, the run is not measuring a transition against a
//     still background and the numbers above mean less than they look like.
//
// Whole-image equality for -same goes through the pixels rather than the file
// bytes, for the reason cmd/pngsame is written down: a toolchain bump rewrites
// PNG bytes with no pixel changed.
//
// PROVED by breaking the real code and running `task transition` on a staged
// tree. What each printed is in the Taskfile beside the invocation.
package main

import (
	"flag"
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
	"strconv"
	"strings"
)

// box is a rectangle of a capture, in the capture's own pixels.
type boxes struct{ items []image.Rectangle }

func (b *boxes) String() string { return fmt.Sprint(len(b.items)) }

func (b *boxes) Set(s string) error {
	v, err := ints(s, 4)
	if err != nil {
		return err
	}
	if v[2] <= 0 || v[3] <= 0 {
		return fmt.Errorf("width and height must be positive, got %dx%d", v[2], v[3])
	}
	b.items = append(b.items, image.Rect(v[0], v[1], v[0]+v[2], v[1]+v[3]))
	return nil
}

type paths struct{ items []string }

func (p *paths) String() string { return fmt.Sprint(len(p.items)) }

func (p *paths) Set(s string) error {
	p.items = append(p.items, s)
	return nil
}

func ints(s string, n int) ([]int, error) {
	parts := strings.Split(s, ",")
	if len(parts) != n {
		return nil, fmt.Errorf("want %d comma-separated numbers, got %q", n, s)
	}
	out := make([]int, n)
	for i, p := range parts {
		v, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			return nil, fmt.Errorf("%q is not a number", p)
		}
		out[i] = v
	}
	return out, nil
}

type shot struct {
	name string
	img  image.Image
}

func main() {
	hidden := flag.String("hidden", "", "the capture with the widget not yet shown")
	rest := flag.String("rest", "", "the capture with the transition finished")

	var mids, sames paths
	flag.Var(&mids, "mid", "a capture partway through a transition; must sit between hidden and rest (repeatable)")
	flag.Var(&sames, "same", "a capture that must equal -rest pixel for pixel (repeatable)")

	var box, scene boxes
	flag.Var(&box, "box", "x,y,w,h: an interior box of the fading widget, inside it in EVERY capture (repeatable)")
	flag.Var(&scene, "scene", "x,y,w,h: a box of the paused scene, which must not change between captures (repeatable)")

	column := flag.String("column", "", "x,y0,y1: the column the widget's top edge is found in")
	slideMin := flag.Int("slidemin", 2, "least the top edge must have travelled at a -mid frame, in pixels")
	slideMax := flag.Int("slidemax", 0, "most it may have travelled; 0 disables the upper bound")
	sep := flag.Float64("sep", 8, "least a -mid box must differ from BOTH hidden and rest, in 8-bit steps")
	tol := flag.Float64("tol", 12, "how far a pixel must be from the hidden capture to count as covered")
	flag.Parse()

	if *hidden == "" || *rest == "" {
		fmt.Fprintln(os.Stderr, "transitioncheck: -hidden and -rest are required")
		os.Exit(2)
	}
	if len(box.items) == 0 && *column == "" {
		fmt.Fprintln(os.Stderr, "transitioncheck: at least one of -box and -column is required, or this checks nothing")
		os.Exit(2)
	}

	hid := shot{"hidden", mustLoad(*hidden)}
	res := shot{"rest", mustLoad(*rest)}
	all := []shot{hid, res}
	var midShots, sameShots []shot
	for _, p := range mids.items {
		s := shot{"mid " + p, mustLoad(p)}
		midShots = append(midShots, s)
		all = append(all, s)
	}
	for _, p := range sames.items {
		s := shot{"same " + p, mustLoad(p)}
		sameShots = append(sameShots, s)
		all = append(all, s)
	}
	for _, s := range all {
		if s.img.Bounds() != hid.img.Bounds() {
			fmt.Fprintf(os.Stderr, "transitioncheck: %s is %v, but hidden is %v\n", s.name, s.img.Bounds(), hid.img.Bounds())
			os.Exit(2)
		}
	}

	status := 0

	// ── the widget's colour ──
	for _, b := range box.items {
		if !b.In(hid.img.Bounds()) {
			fmt.Fprintf(os.Stderr, "transitioncheck: -box %v is outside the capture %v\n", b, hid.img.Bounds())
			os.Exit(2)
		}
		hidMean := mean(hid.img, b)
		restMean := mean(res.img, b)

		// The channel that separates the two states best is the one the
		// "between" test has any power on. If none of them separate, the box is
		// over something the transition does not change and proves nothing.
		ch, spread := bestChannel(hidMean, restMean)
		fmt.Printf("box %-20v hidden (%6.2f %6.2f %6.2f)  rest (%6.2f %6.2f %6.2f)  channel %s separates by %.2f\n",
			b, hidMean[0], hidMean[1], hidMean[2], restMean[0], restMean[1], restMean[2], "rgb"[ch:ch+1], spread)
		if spread < 3**sep {
			fmt.Printf("  FAIL: hidden and rest are only %.2f apart on their best channel, under the %.2f\n", spread, 3**sep)
			fmt.Printf("  this box needs to have any say about what lies between them. Pick a box over\n")
			fmt.Printf("  something the transition actually changes.\n")
			status = 1
			continue
		}

		for _, s := range midShots {
			m := mean(s.img, b)
			dHidden := math.Abs(m[ch] - hidMean[ch])
			dRest := math.Abs(m[ch] - restMean[ch])
			between := (hidMean[ch] < m[ch]) == (m[ch] < restMean[ch])
			fmt.Printf("    %-32s (%6.2f %6.2f %6.2f)  %.2f from hidden, %.2f from rest\n",
				s.name, m[0], m[1], m[2], dHidden, dRest)
			if !between || dHidden < *sep || dRest < *sep {
				fmt.Printf("    FAIL: it does not sit between them with %.2f to spare.\n", *sep)
				switch {
				case dHidden < *sep:
					fmt.Printf("    It is on the HIDDEN colour: the transition did not move at all. If this\n")
					fmt.Printf("    capture was taken with the scene paused, check that the widget tree is\n")
					fmt.Printf("    fed Engine.UnscaledElapsed and not Elapsed -- Elapsed is scaled by\n")
					fmt.Printf("    SetTimeScale and does not advance at scale 0.\n")
				case dRest < *sep:
					fmt.Printf("    It is on the REST colour: the widget cut straight to its finished state\n")
					fmt.Printf("    rather than easing into it. Check the duration and the clock's delta.\n")
				default:
					fmt.Printf("    It is outside both, which is a colour going somewhere else entirely --\n")
					fmt.Printf("    an opacity multiplied twice, or a quad on the wrong draw path.\n")
				}
				status = 1
			}
		}
	}

	// ── the widget's position ──
	if *column != "" {
		v, err := ints(*column, 3)
		if err != nil {
			fmt.Fprintf(os.Stderr, "transitioncheck: -column: %v\n", err)
			os.Exit(2)
		}
		x, y0, y1 := v[0], v[1], v[2]

		if edge, ok := topEdge(hid.img, hid.img, x, y0, y1, *tol); ok {
			fmt.Printf("FAIL: the hidden capture already has an edge at y %d in column %d.\n", edge, x)
			fmt.Printf("  Everything below is measured against a state that is supposed to be empty,\n")
			fmt.Printf("  so this gate proves nothing until the column or the frame is right.\n")
			status = 1
		}

		restEdge, ok := topEdge(res.img, hid.img, x, y0, y1, *tol)
		if !ok {
			fmt.Printf("FAIL: no edge in column %d between y %d and %d in the rest capture.\n", x, y0, y1)
			fmt.Printf("  The widget is not drawn at all once its transition finished.\n")
			status = 1
		} else {
			fmt.Printf("column %d: the widget's top edge rests at y %d\n", x, restEdge)
			for _, s := range midShots {
				edge, ok := topEdge(s.img, hid.img, x, y0, y1, *tol)
				if !ok {
					fmt.Printf("    %-32s no edge at all; the widget is not on screen\n", s.name)
					status = 1
					continue
				}
				travel := restEdge - edge
				fmt.Printf("    %-32s top edge y %d, %d px above its resting place\n", s.name, edge, travel)
				if travel < *slideMin || (*slideMax > 0 && travel > *slideMax) {
					fmt.Printf("    FAIL: that is outside the %d..%d px this frame should have travelled.\n", *slideMin, *slideMax)
					if travel <= 0 {
						fmt.Printf("    At or past its resting place: the offset is not being applied, or the\n")
						fmt.Printf("    transition had already finished by this frame.\n")
					} else {
						fmt.Printf("    Too far: the transition is running slower than its duration says, or\n")
						fmt.Printf("    the capture is at the wrong frame.\n")
					}
					status = 1
				}
			}
		}
	}

	// ── the settled frame ──
	for _, s := range sameShots {
		diff, worst := pixelDiff(s.img, res.img)
		fmt.Printf("%-36s %d pixels differ from rest, worst channel %d\n", s.name, diff, worst)
		if diff != 0 {
			fmt.Printf("  FAIL: a frame past the end of the transition is not the settled frame.\n")
			fmt.Printf("  Once the progress reaches 1 the widget has to be on exactly the draw path a\n")
			fmt.Printf("  widget with no transition block is on -- same objects, same vertices, same\n")
			fmt.Printf("  text. A residue here means the transform is still being applied at rest,\n")
			fmt.Printf("  or a faded quad never went back into the shared vertex stream.\n")
			status = 1
		}
	}

	// ── the paused scene ──
	for _, b := range scene.items {
		if !b.In(hid.img.Bounds()) {
			fmt.Fprintf(os.Stderr, "transitioncheck: -scene %v is outside the capture %v\n", b, hid.img.Bounds())
			os.Exit(2)
		}
		still := true
		for _, s := range all[1:] {
			diff, worst := pixelDiffIn(s.img, hid.img, b)
			if diff != 0 {
				still = false
				fmt.Printf("FAIL: the paused scene at %v moved between hidden and %s: %d pixels, worst channel %d\n",
					b, s.name, diff, worst)
				fmt.Printf("  These captures are supposed to differ by the transition and by nothing else.\n")
				fmt.Printf("  Either the scene is not paused, or something in the frame is running off a\n")
				fmt.Printf("  clock that SetTimeScale does not stop.\n")
				status = 1
			}
		}
		if still {
			fmt.Printf("scene %-20v still across all %d captures\n", b, len(all))
		}
	}

	if status != 0 {
		os.Exit(status)
	}
	fmt.Println("the transition moves while the game is paused, settles onto the untransitioned frame, and moves nothing else")
}

// mean is the per-channel average over a box, in 8-bit units.
func mean(img image.Image, r image.Rectangle) [3]float64 {
	var sum [3]float64
	n := 0.0
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			cr, cg, cb, _ := img.At(x, y).RGBA()
			sum[0] += float64(cr >> 8)
			sum[1] += float64(cg >> 8)
			sum[2] += float64(cb >> 8)
			n++
		}
	}
	return [3]float64{sum[0] / n, sum[1] / n, sum[2] / n}
}

// bestChannel is the channel on which two colours are furthest apart, and by
// how much. A "between" test on any other channel has less to say.
func bestChannel(a, b [3]float64) (int, float64) {
	best, spread := 0, 0.0
	for ch := 0; ch < 3; ch++ {
		if d := math.Abs(a[ch] - b[ch]); d > spread {
			best, spread = ch, d
		}
	}
	return best, spread
}

// topEdge scans a column downwards for the first row that differs from the
// reference capture by more than tol on any channel.
//
// The reference is the hidden frame rather than a fixed colour, so the edge is
// "where this capture stopped looking like the empty one" -- which is the same
// statement whatever the scene behind it happens to be, and needs no knowledge
// of the widget's own colour.
func topEdge(img, ref image.Image, x, y0, y1 int, tol float64) (int, bool) {
	for y := y0; y <= y1; y++ {
		if !image.Pt(x, y).In(img.Bounds()) {
			continue
		}
		ar, ag, ab, _ := img.At(x, y).RGBA()
		br, bg, bb, _ := ref.At(x, y).RGBA()
		d := math.Max(math.Abs(float64(ar>>8)-float64(br>>8)),
			math.Max(math.Abs(float64(ag>>8)-float64(bg>>8)), math.Abs(float64(ab>>8)-float64(bb>>8))))
		if d > tol {
			return y, true
		}
	}
	return 0, false
}

func pixelDiff(a, b image.Image) (int, int) { return pixelDiffIn(a, b, a.Bounds()) }

// pixelDiffIn counts differing pixels and the worst single channel difference
// over a box. Pixels rather than file bytes: a toolchain bump rewrites PNG
// bytes with nothing changed. See cmd/pngsame.
func pixelDiffIn(a, b image.Image, r image.Rectangle) (int, int) {
	diff, worst := 0, 0
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			ar, ag, ab, aa := a.At(x, y).RGBA()
			br, bg, bb, ba := b.At(x, y).RGBA()
			d := 0
			for _, p := range [4][2]uint32{{ar, br}, {ag, bg}, {ab, bb}, {aa, ba}} {
				if v := int(p[0]>>8) - int(p[1]>>8); v > d {
					d = v
				} else if -v > d {
					d = -v
				}
			}
			if d != 0 {
				diff++
				if d > worst {
					worst = d
				}
			}
		}
	}
	return diff, worst
}

func mustLoad(path string) image.Image {
	f, err := os.Open(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "transitioncheck: %v\n", err)
		os.Exit(2)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		fmt.Fprintf(os.Stderr, "transitioncheck: decode %s: %v\n", path, err)
		os.Exit(2)
	}
	return img
}
