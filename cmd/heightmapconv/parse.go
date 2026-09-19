package main

import (
	"fmt"
	"strconv"
	"strings"
)

// parseWxH parses "WxH" integers, e.g. "129x129" -> (129, 129).
func parseWxH(s string) (w, h int, err error) {
	parts := strings.SplitN(s, "x", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("want WxH, got %q", s)
	}
	w, err = strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0, fmt.Errorf("%q is not an integer", parts[0])
	}
	h, err = strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return 0, 0, fmt.Errorf("%q is not an integer", parts[1])
	}
	if w < 2 || h < 2 {
		return 0, 0, fmt.Errorf("grid must be at least 2x2, got %dx%d", w, h)
	}
	return w, h, nil
}

// parseWxHFloat parses "WxD" floats, e.g. "200x200" -> (200, 200) metres.
func parseWxHFloat(s string) (w, d float64, err error) {
	parts := strings.SplitN(s, "x", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("want WxD, got %q", s)
	}
	w, err = strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	if err != nil {
		return 0, 0, fmt.Errorf("%q is not a number", parts[0])
	}
	d, err = strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if err != nil {
		return 0, 0, fmt.Errorf("%q is not a number", parts[1])
	}
	return w, d, nil
}

// parseXY parses "a,b" floats -- shared by -origin and -range, which are
// both just a pair of numbers with different meanings.
func parseXY(s string) (a, b float64, err error) {
	parts := strings.SplitN(s, ",", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("want a,b, got %q", s)
	}
	a, err = strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	if err != nil {
		return 0, 0, fmt.Errorf("%q is not a number", parts[0])
	}
	b, err = strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if err != nil {
		return 0, 0, fmt.Errorf("%q is not a number", parts[1])
	}
	return a, b, nil
}

// parseBounds parses "minX,minZ,maxX,maxZ".
func parseBounds(s string) (minX, minZ, maxX, maxZ float64, err error) {
	parts := strings.Split(s, ",")
	if len(parts) != 4 {
		return 0, 0, 0, 0, fmt.Errorf("want minX,minZ,maxX,maxZ, got %q", s)
	}
	vals := make([]float64, 4)
	for i, p := range parts {
		vals[i], err = strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			return 0, 0, 0, 0, fmt.Errorf("%q is not a number", p)
		}
	}
	return vals[0], vals[1], vals[2], vals[3], nil
}

// resolveGrid picks the output grid size from -grid (explicit) or -cell
// (spacing in metres, converted against the world size this grid will
// cover), and errors if neither was given -- a silent default would be a
// surprise the first time someone's terrain came out coarser or finer than
// they expected.
func resolveGrid(gridStr string, cell float64, worldW, worldD float64) (w, h int, err error) {
	switch {
	case gridStr != "":
		return parseWxH(gridStr)
	case cell > 0:
		w = int(worldW/cell+0.5) + 1
		h = int(worldD/cell+0.5) + 1
		if w < 2 || h < 2 {
			return 0, 0, fmt.Errorf("-cell %g over a %gx%g world produces a %dx%d grid, want at least 2x2", cell, worldW, worldD, w, h)
		}
		return w, h, nil
	default:
		return 0, 0, fmt.Errorf("one of -grid or -cell is required")
	}
}
