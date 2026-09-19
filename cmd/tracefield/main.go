// Command tracefield pulls one field out of a GLYPHENGINE_STATE_TRACE file so
// two runs can be diffed on it alone.
//
//	task determinism
//
// A whole trace cannot be diffed: `slot=` and `image=` name the frame-in-flight
// and the swapchain image the acquire happened to return, and both legitimately
// differ between two runs that drew the same picture. Diffing the whole file
// would report every run as divergent and the gate would have to be ignored,
// which is the same as not having one.
//
// So the gate names the field it is asserting about — `draws=`, the ordered
// hash of the draw list — and this extracts it. The output carries `loop=` too,
// so a diff points at the frame rather than at a line number.
//
// It is a Go program rather than a line of shell because the shell `task` runs
// on Windows has no sed, and `grep -o` is not something this repo has proven is
// there. cmd/skycheck and cmd/hudcheck are here for the same reason.
//
// A field that is absent from a line is written as `<key>=-` rather than
// dropped, so a run that stopped recording draw lists shows up as a difference
// instead of as a shorter file that diff reports in a way nobody reads. A file
// in which the field is absent from EVERY line is an error: that is the shape a
// vacuous gate has, and it has happened here before.
//
// -constant asserts about ONE run instead of two: that the field holds the
// same value on every frame of it. That is a different question from
// determinism and diff cannot ask it -- `task reload` needs it, because a
// level being swapped for a freshly loaded copy must not change what is drawn
// on any frame, and the failure it is looking for (the level blinking out
// between a release and the next load) is invisible to a comparison of two
// runs that both blink in the same place.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strings"
)

func main() {
	in := flag.String("in", "", "state trace to read")
	out := flag.String("out", "", "file to write (default stdout)")
	key := flag.String("key", "draws", "field to extract")
	constant := flag.Bool("constant", false, "assert the field holds one value on every line; exit non-zero naming the first line that differs")
	count := flag.Bool("count", false, "keep only the count from a count/hash field such as draws=8/1a2b…, dropping the hash")
	skip := flag.Int("skip", 0, "drop the first N lines before comparing; for a -constant assertion about a steady state that the run takes a few frames to reach")
	flag.Parse()

	if *in == "" {
		fmt.Fprintln(os.Stderr, "tracefield: -in is required")
		os.Exit(2)
	}

	lines, found, err := extract(*in, *key)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tracefield: %v\n", err)
		os.Exit(1)
	}
	if len(lines) == 0 {
		fmt.Fprintf(os.Stderr, "tracefield: %s is empty -- nothing to compare\n", *in)
		os.Exit(1)
	}
	if found == 0 {
		fmt.Fprintf(os.Stderr, "tracefield: %s has no %s= field on any of its %d lines -- comparing it would prove nothing\n", *in, *key, len(lines))
		os.Exit(1)
	}

	if *skip > 0 {
		// Skipping is only ever right for a -constant assertion about a state
		// the run has to reach -- 22-level's camera takes a dozen frames to
		// settle onto its authored pose, and every draw's MVP moves with it.
		// It is a flag rather than a hardcoded number so the gate that needs
		// it has to say how many frames it is not looking at, and refusing to
		// skip the file away keeps "constant over 0 lines" from passing.
		if *skip >= len(lines) {
			fmt.Fprintf(os.Stderr, "tracefield: -skip %d leaves nothing of %s's %d lines to compare\n", *skip, *in, len(lines))
			os.Exit(1)
		}
		lines = lines[*skip:]
	}

	if *count {
		// A CountHash field is "<n>/<hash>": how many of the thing there were
		// and what they were. Keeping only the count asks a much weaker
		// question than the whole field does, and there is exactly one gate
		// that wants it -- `task reload`, where the hash legitimately changes
		// while the camera settles and the COUNT must not move at all,
		// because a level blinking out between a release and the next load is
		// precisely a frame or two with fewer draws in it.
		for i, l := range lines {
			v := fieldOf(l, *key)
			if n, _, ok := strings.Cut(v, "/"); ok {
				lines[i] = loopOf(l) + " " + *key + "=" + n
			}
		}
	}

	if *constant {
		checkConstant(*in, *key, lines)
		return
	}

	w := os.Stdout
	if *out != "" {
		f, err := os.Create(*out)
		if err != nil {
			fmt.Fprintf(os.Stderr, "tracefield: %v\n", err)
			os.Exit(1)
		}
		defer f.Close()
		w = f
	}
	bw := bufio.NewWriter(w)
	for _, l := range lines {
		fmt.Fprintln(bw, l)
	}
	if err := bw.Flush(); err != nil {
		fmt.Fprintf(os.Stderr, "tracefield: %v\n", err)
		os.Exit(1)
	}
}

// checkConstant asserts every extracted line carries the same field value, and
// exits non-zero naming the first that does not.
//
// It reports the line count on success on purpose: a gate that passed over
// three frames has not looked at a reload loop, and "constant over 3 lines" is
// the only way that is visible from the outside.
func checkConstant(path, key string, lines []string) {
	first := fieldOf(lines[0], key)
	for i, l := range lines[1:] {
		if v := fieldOf(l, key); v != first {
			fmt.Fprintf(os.Stderr, "tracefield: %s: %s changed at %s: %s, was %s at %s\n",
				path, key, loopOf(l), v, first, loopOf(lines[0]))
			fmt.Fprintf(os.Stderr, "tracefield: that is line %d of %d\n", i+2, len(lines))
			os.Exit(1)
		}
	}
	fmt.Printf("%s: %s constant over %d lines (%s)\n", path, key, len(lines), first)
}

func fieldOf(line, key string) string {
	v, _ := field(line, key)
	return v
}

func loopOf(line string) string {
	v, ok := field(line, "loop")
	if !ok {
		return "loop=?"
	}
	return "loop=" + v
}

// extract returns one "loop=<n> <key>=<value>" line per trace line, and how
// many of them actually carried the field.
func extract(path, key string) (lines []string, found int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	// A trace line is short, but the buffer is raised anyway: a truncated line
	// would be read as a changed field, which is a false positive in a gate.
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		loop, ok := field(sc.Text(), "loop")
		if !ok {
			loop = "?"
		}
		v, ok := field(sc.Text(), key)
		if ok {
			found++
		} else {
			v = "-"
		}
		lines = append(lines, "loop="+loop+" "+key+"="+v)
	}
	return lines, found, sc.Err()
}

// field returns the value of key in a space-separated "k=v" line.
//
// The key is matched whole: "draws" must not match "drawsset", which sits on
// the same line and holds the order-independent hash. A prefix match there
// would have the gate comparing the field that cannot fail.
func field(line, key string) (string, bool) {
	for _, f := range strings.Fields(line) {
		k, v, ok := strings.Cut(f, "=")
		if ok && k == key {
			return v, true
		}
	}
	return "", false
}
