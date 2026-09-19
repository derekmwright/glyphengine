// Command heightmapconv is the encoder LoadHeightmap has never had a
// writer-side partner for: it turns a sculpted Blender terrain mesh, or a
// heightmap image from World Machine/Gaea/a displacement bake, into the
// .heightmap file glyphengine.LoadHeightmap reads.
//
// Three input shapes, one output:
//
//	heightmapconv -mesh terrain.glb -node Terrain -grid 129x129 -out terrain.heightmap
//	heightmapconv -image heights.png -world 200x200 -range 0,40 -out terrain.heightmap
//	heightmapconv -raw heights.r16 -rawsize 512x512 -world 200x200 -range 0,40 -out terrain.heightmap
//	heightmapconv -info terrain.heightmap
//
// See docs/agents/terrain-heightmap.md for the file format, the flags, and
// -- the thing every heightmap import gets backwards once -- the exact
// orientation convention this tool uses, with a worked diagram.
//
// This is a GPU-free command-line tool in the shape cmd/tracefield and
// cmd/palettecheck already are: plain flag package, no third-party CLI
// library, a thin main() over functions that are unit tested directly. It
// reads glTF with github.com/qmuntal/gltf rather than the renderer
// package's loader, on purpose -- renderer pulls in the Vulkan bindings,
// and nothing about rasterising a mesh onto a grid needs a GPU.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	glyph "github.com/derekmwright/glyphengine"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is main's body as a function so tests can drive it without exec'ing
// the binary and without main() calling os.Exit out from under a test
// process -- the same shape cmd/tracefield's extract() split out for.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("heightmapconv", flag.ContinueOnError)
	fs.SetOutput(stderr)

	mesh := fs.String("mesh", "", "sculpted terrain mesh to rasterise (.glb/.gltf)")
	node := fs.String("node", "", "name of the mesh's node (required if the document has more than one mesh-bearing node)")
	grid := fs.String("grid", "", "output grid size WxH, e.g. 129x129")
	cell := fs.Float64("cell", 0, "output grid spacing in metres, alternative to -grid")
	bounds := fs.String("bounds", "", "minX,minZ,maxX,maxZ override; default is the mesh's own XZ extent")
	fill := fs.String("fill", "", "nearest, min, or value:N -- fill samples that hit no surface instead of failing")

	image := fs.String("image", "", "16-bit (or 8-bit, with a terracing warning) greyscale heightmap PNG")
	raw := fs.String("raw", "", "headerless little-endian uint16 heightmap (.r16 -- World Machine/Gaea/Unity convention)")
	rawsize := fs.String("rawsize", "", "WxH of -raw, required with -raw")
	bigEndian := fs.Bool("bigendian", false, "-raw is big-endian instead of the format's usual little-endian")
	world := fs.String("world", "", "world-space size WxD in metres, for -image/-raw")
	origin := fs.String("origin", "0,0", "world-space origin X,Z (min corner), for -image/-raw")
	rng := fs.String("range", "", "minY,maxY that sample 0 and sample 65535 map to, for -image/-raw")

	out := fs.String("out", "", "output .heightmap path")
	info := fs.String("info", "", "print a summary of an existing .heightmap file instead of converting anything")

	if err := fs.Parse(args); err != nil {
		return 2
	}

	if *info != "" {
		if err := printInfo(stdout, *info); err != nil {
			fmt.Fprintf(stderr, "heightmapconv: %v\n", err)
			return 1
		}
		return 0
	}

	inputs := 0
	for _, s := range []string{*mesh, *image, *raw} {
		if s != "" {
			inputs++
		}
	}
	if inputs != 1 {
		fmt.Fprintln(stderr, "heightmapconv: exactly one of -mesh, -image, -raw, or -info is required")
		return 2
	}
	if *out == "" {
		fmt.Fprintln(stderr, "heightmapconv: -out is required")
		return 2
	}

	var h *glyph.Heightmap
	var err error
	switch {
	case *mesh != "":
		h, err = convertMesh(stdout, meshArgs{
			path: *mesh, node: *node, grid: *grid, cell: *cell,
			bounds: *bounds, fill: *fill,
		})
	case *image != "":
		h, err = convertImage(stdout, *image, *world, *origin, *rng)
	case *raw != "":
		h, err = convertRaw(stdout, *raw, *rawsize, *bigEndian, *world, *origin, *rng)
	}
	if err != nil {
		fmt.Fprintf(stderr, "heightmapconv: %v\n", err)
		return 1
	}

	f, err := os.Create(*out)
	if err != nil {
		fmt.Fprintf(stderr, "heightmapconv: %v\n", err)
		return 1
	}
	defer f.Close()
	n, err := h.WriteTo(f)
	if err != nil {
		fmt.Fprintf(stderr, "heightmapconv: write %s: %v\n", *out, err)
		return 1
	}
	fmt.Fprintf(stdout, "heightmapconv: wrote %s (%d bytes, %dx%d grid)\n", *out, n, h.GridW, h.GridH)
	return 0
}

type meshArgs struct {
	path, node, grid string
	cell             float64
	bounds, fill     string
}

// convertMesh is -mesh's whole pipeline: load, find the node, rasterise,
// report holes/overhangs, apply -fill, and build the Heightmap. Split out
// from run() so it is testable without going through flag parsing or
// touching the filesystem for output.
func convertMesh(out io.Writer, a meshArgs) (*glyph.Heightmap, error) {
	tris, nodeName, err := loadMeshTriangles(a.path, a.node)
	if err != nil {
		return nil, err
	}

	minX, minZ, maxX, maxZ := meshBounds(tris)
	if a.bounds != "" {
		minX, minZ, maxX, maxZ, err = parseBounds(a.bounds)
		if err != nil {
			return nil, err
		}
	}
	if maxX <= minX || maxZ <= minZ {
		return nil, fmt.Errorf("bounds %g,%g .. %g,%g are empty or inverted", minX, minZ, maxX, maxZ)
	}

	gridW, gridH, err := resolveGrid(a.grid, a.cell, maxX-minX, maxZ-minZ)
	if err != nil {
		return nil, err
	}

	var mode fillMode
	if a.fill != "" {
		mode, err = parseFillMode(a.fill)
		if err != nil {
			return nil, err
		}
	}

	start := time.Now()
	res := rasterizeMesh(tris, gridW, gridH, minX, minZ, maxX, maxZ)
	elapsed := time.Since(start)
	fmt.Fprintf(out, "heightmapconv: rasterised node %q (%d triangles) onto %dx%d in %s\n",
		nodeName, len(tris), gridW, gridH, elapsed)

	if len(res.overhangs) > 0 {
		fmt.Fprintf(out, "heightmapconv: %d sample(s) have more than one surface (overhang/cave/bridge) -- using the topmost:\n", len(res.overhangs))
		printCoords(out, res.overhangs, res.overhangCounts)
	}

	if len(res.holes) > 0 {
		if mode.kind == "" {
			fmt.Fprintf(out, "heightmapconv: %d sample(s) hit no surface (holes):\n", len(res.holes))
			printCoords(out, res.holes, nil)
			return nil, fmt.Errorf("%d hole(s) in the rasterised grid -- pass -fill nearest|min|value:N to fill them, or fix the mesh so every column has ground", len(res.holes))
		}
		applyFill(res.heights, gridW, gridH, res.holes, mode)
		fmt.Fprintf(out, "heightmapconv: filled %d hole(s) with -fill %s\n", len(res.holes), a.fill)
	}

	return glyph.NewHeightmap(gridW, gridH, float32(maxX-minX), float32(maxZ-minZ), float32(minX), float32(minZ), res.heights)
}

func printCoords(out io.Writer, coords []coord, counts []int) {
	const limit = 8
	n := len(coords)
	if n > limit {
		n = limit
	}
	for i := 0; i < n; i++ {
		c := coords[i]
		if counts != nil {
			fmt.Fprintf(out, "  grid(%d,%d) world(%.3f,%.3f): %d surfaces\n", c.ix, c.iz, c.x, c.z, counts[i])
		} else {
			fmt.Fprintf(out, "  grid(%d,%d) world(%.3f,%.3f)\n", c.ix, c.iz, c.x, c.z)
		}
	}
	if len(coords) > limit {
		fmt.Fprintf(out, "  ... and %d more\n", len(coords)-limit)
	}
}

func convertImage(out io.Writer, path, worldStr, originStr, rangeStr string) (*glyph.Heightmap, error) {
	samples, w, h, warn, err := readPNGHeights(path)
	if err != nil {
		return nil, err
	}
	if warn != "" {
		fmt.Fprintf(out, "heightmapconv: warning: %s\n", warn)
	}
	return buildFromSamples(samples, w, h, worldStr, originStr, rangeStr)
}

func convertRaw(out io.Writer, path, rawsize string, bigEndian bool, worldStr, originStr, rangeStr string) (*glyph.Heightmap, error) {
	w, h, err := parseWxH(rawsize)
	if err != nil {
		return nil, fmt.Errorf("-rawsize: %w", err)
	}
	samples, err := readRawHeights(path, w, h, bigEndian)
	if err != nil {
		return nil, err
	}
	return buildFromSamples(samples, w, h, worldStr, originStr, rangeStr)
}

func buildFromSamples(samples []uint16, w, h int, worldStr, originStr, rangeStr string) (*glyph.Heightmap, error) {
	if worldStr == "" {
		return nil, fmt.Errorf("-world is required")
	}
	if rangeStr == "" {
		return nil, fmt.Errorf("-range is required")
	}
	worldW, worldD, err := parseWxHFloat(worldStr)
	if err != nil {
		return nil, fmt.Errorf("-world: %w", err)
	}
	originX, originZ, err := parseXY(originStr)
	if err != nil {
		return nil, fmt.Errorf("-origin: %w", err)
	}
	rangeMin, rangeMax, err := parseXY(rangeStr)
	if err != nil {
		return nil, fmt.Errorf("-range: %w", err)
	}
	heights := heightsFromSamples(samples, w, h, float32(rangeMin), float32(rangeMax))
	return glyph.NewHeightmap(w, h, float32(worldW), float32(worldD), float32(originX), float32(originZ), heights)
}

func printInfo(out io.Writer, path string) error {
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	h, err := glyph.LoadHeightmap(os.DirFS(dir), base)
	if err != nil {
		return err
	}
	lo, hi := h.Heights[0], h.Heights[0]
	var sum float64
	for _, v := range h.Heights {
		if v < lo {
			lo = v
		}
		if v > hi {
			hi = v
		}
		sum += float64(v)
	}
	mean := sum / float64(len(h.Heights))
	fmt.Fprintf(out, "%s\n", path)
	fmt.Fprintf(out, "  grid:       %d x %d\n", h.GridW, h.GridH)
	fmt.Fprintf(out, "  world size: %g x %g\n", h.WorldW, h.WorldD)
	fmt.Fprintf(out, "  origin:     %g, %g\n", h.OriginX, h.OriginZ)
	fmt.Fprintf(out, "  height:     min %g  max %g  mean %g\n", lo, hi, mean)
	return nil
}
