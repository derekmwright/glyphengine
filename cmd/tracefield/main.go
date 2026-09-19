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
