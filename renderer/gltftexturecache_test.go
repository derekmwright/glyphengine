package renderer

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"testing/fstest"

	"github.com/vkngwrapper/core/v3/core1_0"
)

// This file drives LoadGLTF's image upload -- createTexture and all -- without
// a GPU, which nothing in this package did before: textureTestDriver adds the
// two image-copy commands createTexture records and bufferTestDriver had no
// use for.
// The fixtures it loads are the committed ones, because the bug this covers is
// exactly their shape: examples/08-grass/assets/flora holds four .gltf
// documents that all name one Grass.png, the small version of the sixty props
// sharing a trim sheet in issue #136.

// textureTestDriver is bufferTestDriver plus the image-copy commands
// createTexture records. Everything else it needs -- images, views, samplers,
// descriptor sets, staging buffers, the one-shot command buffer -- the two
// drivers it builds on already have, with the same create/destroy counting
// and the same failCall/failAt injection.
type textureTestDriver struct {
	*bufferTestDriver
	imageCopies int
}

func (d *textureTestDriver) CmdCopyBufferToImage(_ core1_0.CommandBuffer, _ core1_0.Buffer, _ core1_0.Image, _ core1_0.ImageLayout, _ ...core1_0.BufferImageCopy) error {
	if d.shouldFail("CmdCopyBufferToImage") {
		return errInjected
	}
	d.imageCopies++
	return nil
}

func (d *textureTestDriver) CmdBlitImage(_ core1_0.CommandBuffer, _ core1_0.Image, _ core1_0.ImageLayout, _ core1_0.Image, _ core1_0.ImageLayout, _ []core1_0.ImageBlit, _ core1_0.Filter) error {
	return nil
}

func textureFixture() (*Renderer, *textureTestDriver) {
	r, b := bufferFixture()
	d := &textureTestDriver{bufferTestDriver: b}
	r.deviceDriver = d
	return r, d
}

// textureBalance is assertBalanced over the kinds an image upload makes,
// including the staging buffer and its memory. Takes testing.TB so the
// meta-check below can watch it without failing.
func textureBalance(t testing.TB, d *textureTestDriver) {
	t.Helper()
	for _, kind := range []string{"Image", "ImageView", "DeviceMemory", "Sampler", "DescriptorSet", "Buffer", "CommandBuffer"} {
		if d.created[kind] != d.destroyed[kind] {
			t.Errorf("%s: created %d, destroyed %d (leaked or double-freed %d)",
				kind, d.created[kind], d.destroyed[kind], d.created[kind]-d.destroyed[kind])
		}
	}
}

// countingFS counts Open calls per path, which is how these tests count
// DECODES without a seam in the loader: decodeGLTFImages reads the file and
// hands the bytes straight to decodeImage, so an image opened once is an image
// decoded once, and an image never opened is the whole saving.
//
// A POINTER type on purpose. The cache keys on the fs.FS the caller named, so
// the fixture's own wrapper has to be comparable and has to compare equal
// across two loads -- a pointer is both, whatever it wraps. A struct value
// wrapping an fs.FS would also pass reflect's Comparable(), and would panic in
// the map write if the thing inside it were an fstest.MapFS; see
// TestGLTFTextureCacheSkipsUncomparableFilesystem.
type countingFS struct {
	fsys  fs.FS
	opens map[string]int
}

func newCountingFS(fsys fs.FS) *countingFS {
	return &countingFS{fsys: fsys, opens: map[string]int{}}
}

func (c *countingFS) Open(name string) (fs.File, error) {
	c.opens[name]++
	return c.fsys.Open(name)
}

const (
	grassAssets = "../examples/08-grass/assets"
	grassSheet  = "flora/Grass.png"
)

var grassDocs = []string{
	"flora/Grass_Common_Short.gltf",
	"flora/Grass_Common_Tall.gltf",
	"flora/Grass_Wispy_Short.gltf",
	"flora/Grass_Wispy_Tall.gltf",
}

// TestGLTFTextureCacheSharesOneSheetBetweenDocuments is issue #136 in
// miniature, on the committed assets that have the bug's shape: four glTF
// documents in one directory, each naming Grass.png, loaded from one fs.FS.
//
// Before the cache this was four decodes and four GPU textures for one file.
//
// BROKEN: made gltfImageKeys key on rd.base instead of rd.fsys -- the change
// that looks harmless because base IS the document's directory. FAILED with
// "opened flora/Grass.png 4 times, want 1 -- the sheet is decoded once and
// shared", "created 4 images for one shared sheet, want 1" and
// "Textures/CachedTextures/TextureShares = 4/4/4, want 1/1/4". fs.Sub returns
// a fresh *subFS per call, so every document gets a unique key and nothing
// ever hits. Restored with `git checkout -- renderer/gltftexturecache.go`.
//
// BROKEN: dropped the `c.shares++` from acquireGLTFTexture. FAILED with
// "Textures/CachedTextures/TextureShares = 1/1/1, want 1/1/4" and then
// "after releasing 1 of 4 documents: Textures = 0 (want 1), TextureShares = 0
// (want 3)" -- the first release took a sheet three documents still draw with.
//
// BROKEN: removed the `if skip[i] { continue }` from decodeGLTFImages, so the
// sharing happens at upload but every document still decodes. FAILED with
// "opened flora/Grass.png 4 times, want 1 -- the sheet is decoded once and
// shared" while the image count stayed at 1: exactly the half-fix this
// assertion, and not the texture count, exists to catch.
func TestGLTFTextureCacheSharesOneSheetBetweenDocuments(t *testing.T) {
	r, d := textureFixture()
	fsys := newCountingFS(os.DirFS(grassAssets))

	var models []*Model
	for _, name := range grassDocs {
		m, err := r.LoadGLTF(fsys, name)
		if err != nil {
			t.Fatalf("LoadGLTF %s: %v", name, err)
		}
		models = append(models, m)
	}

	if got := fsys.opens[grassSheet]; got != 1 {
		t.Errorf("opened %s %d times, want 1 -- the sheet is decoded once and shared", grassSheet, got)
	}
	if got := d.created["Image"]; got != 1 {
		t.Errorf("created %d images for one shared sheet, want 1", got)
	}
	for i, m := range models[1:] {
		if m.Meshes[0].Texture != models[0].Meshes[0].Texture {
			t.Errorf("%s got its own texture rather than the shared one", grassDocs[i+1])
		}
	}
	counts := r.ResourceCounts()
	if counts.Textures != 1 || counts.CachedTextures != 1 || counts.TextureShares != len(grassDocs) {
		t.Errorf("Textures/CachedTextures/TextureShares = %d/%d/%d, want 1/1/%d",
			counts.Textures, counts.CachedTextures, counts.TextureShares, len(grassDocs))
	}

	// Releasing all but the last must not take the sheet with it.
	for i, m := range models[:len(models)-1] {
		r.DestroyModel(m)
		r.flushAllDeferred()
		want := len(models) - 1 - i
		counts = r.ResourceCounts()
		if counts.Textures != 1 || counts.TextureShares != want {
			t.Fatalf("after releasing %d of %d documents: Textures = %d (want 1), TextureShares = %d (want %d)",
				i+1, len(models), counts.Textures, counts.TextureShares, want)
		}
		if d.destroyed["Image"] != 0 {
			t.Fatalf("a document's release destroyed a sheet %d other documents are still drawing with", want)
		}
	}

	r.DestroyModel(models[len(models)-1])
	r.flushAllDeferred()
	counts = r.ResourceCounts()
	if counts.Textures != 0 || counts.CachedTextures != 0 || counts.TextureShares != 0 {
		t.Errorf("after the last release: Textures/CachedTextures/TextureShares = %d/%d/%d, want 0/0/0",
			counts.Textures, counts.CachedTextures, counts.TextureShares)
	}
	if d.destroyed["Image"] != 1 {
		t.Errorf("destroyed %d images after the last share went, want 1", d.destroyed["Image"])
	}
	textureBalance(t, d)
}

// TestGLTFTextureCacheSharesOneDocumentLoadedTwice is the case a game hits
// without meaning to: the same file opened twice, because two systems each
// load the prop they need. The key does not mention the document, so this is
// the same hit as two different documents -- and it is worth its own test
// because it is the one that also proves the SECOND load takes its own share
// rather than aliasing the first model's.
//
// BROKEN: had releaseGLTFTexture destroy on every call rather than only at
// zero shares (`c.shares--` then falling through). FAILED with "releasing one
// of two loads of the same document destroyed the shared sheet: Textures = 0,
// TextureShares = 0, want 1 and 1".
//
// BROKEN: put DestroyTexture back in DestroyModel's loop in place of
// releaseGLTFTexture. FAILED with the same line, and with "after releasing 1
// of 4 documents: Textures = 0 (want 1), TextureShares = 0 (want 3)" in
// TestGLTFTextureCacheSharesOneSheetBetweenDocuments.
func TestGLTFTextureCacheSharesOneDocumentLoadedTwice(t *testing.T) {
	r, _ := textureFixture()
	fsys := newCountingFS(os.DirFS(grassAssets))

	first, err := r.LoadGLTF(fsys, grassDocs[0])
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.LoadGLTF(fsys, grassDocs[0])
	if err != nil {
		t.Fatal(err)
	}
	if first.Meshes[0].Texture != second.Meshes[0].Texture {
		t.Fatal("the same document loaded twice uploaded its image twice")
	}
	if got := fsys.opens[grassSheet]; got != 1 {
		t.Errorf("opened %s %d times, want 1", grassSheet, got)
	}

	r.DestroyModel(first)
	r.flushAllDeferred()
	if counts := r.ResourceCounts(); counts.Textures != 1 || counts.TextureShares != 1 {
		t.Fatalf("releasing one of two loads of the same document destroyed the shared sheet: Textures = %d, TextureShares = %d, want 1 and 1", counts.Textures, counts.TextureShares)
	}
	r.DestroyModel(second)
	r.flushAllDeferred()
	if counts := r.ResourceCounts(); counts.Textures != 0 || counts.CachedTextures != 0 {
		t.Fatalf("the second release left %d textures and %d cache entries, want 0 and 0", counts.Textures, counts.CachedTextures)
	}
}

// writeSheetDocs writes one PNG and a .gltf per spec into dir, each document
// naming that one PNG. Meshless on purpose: the images are what is under test,
// uploadGLTFImages walks doc.Images regardless of what draws them, and a
// synthetic primitive would only add accessors that prove nothing here. The
// documents with real geometry are the committed grass ones above.
func writeSheetDocs(t *testing.T, dir string, sheets []string, docs map[string]string) {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	for i := range img.Pix {
		img.Pix[i] = byte(i)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	write := func(name string, body []byte) {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, sheet := range sheets {
		write(sheet, buf.Bytes())
	}
	for name, body := range docs {
		write(name, []byte(body))
	}
}

// sheetDoc is a meshless glTF naming one image URI. wrap is the sampler's
// wrapS/wrapT (0 leaves the sampler off entirely, which glTF defines as
// repeat), and slot picks the material slot the texture is bound to, which is
// what decides sRGB versus UNORM.
func sheetDoc(uri string, wrap int, slot string) string {
	sampler, samplerRef := "", ""
	if wrap != 0 {
		sampler = fmt.Sprintf(`"samplers":[{"wrapS":%d,"wrapT":%d}],`, wrap, wrap)
		samplerRef = `"sampler":0,`
	}
	material := `"pbrMetallicRoughness":{"baseColorTexture":{"index":0}}`
	if slot == "normal" {
		material = `"normalTexture":{"index":0}`
	}
	return fmt.Sprintf(`{"asset":{"version":"2.0"},`+
		`"images":[{"uri":%q}],`+
		`%s"textures":[{%s"source":0}],`+
		`"materials":[{"name":"Sheet",%s}]}`, uri, sampler, samplerRef, material)
}

// TestGLTFTextureCacheKeyDistinguishesWrapAndEncoding is the other half of the
// key: two documents that name the same FILE but want a different texture out
// of it must not share, because the difference is in the upload and not in the
// sampler a draw picks.
//
// The sRGB case is the one with teeth. An image bound as a normal map is
// uploaded _UNORM and as albedo _SRGB; sharing them would light every surface
// through a gamma curve it never asked for, and nothing about the frame would
// say so (see dataImageIndices).
//
// BROKEN: pinned the key's srgb to true, which is dropping it from the key.
// FAILED on albedo_and_normal with "created 1 images, want 2" and
// "CachedTextures = 1 (want 2)".
//
// BROKEN: pinned the key's wrap to repeatWrap. FAILED on repeat_and_clamp
// with the same two lines.
//
// BROKEN: keyed on img.URI rather than path.Join(dir, img.URI). FAILED on
// same_URI_in_two_directories with the same two lines -- and that one is not
// a missed saving, it is one prop drawn with the other prop's sheet.
func TestGLTFTextureCacheKeyDistinguishesWrapAndEncoding(t *testing.T) {
	const (
		clampToEdge = 33071
		repeat      = 10497
	)
	for _, tc := range []struct {
		name         string
		sheets       []string
		aName, aBody string
		bName, bBody string
		share        bool
	}{
		{"repeat and clamp", []string{"sheet.png"},
			"a.gltf", sheetDoc("sheet.png", repeat, "albedo"),
			"b.gltf", sheetDoc("sheet.png", clampToEdge, "albedo"), false},
		{"albedo and normal", []string{"sheet.png"},
			"a.gltf", sheetDoc("sheet.png", repeat, "albedo"),
			"b.gltf", sheetDoc("sheet.png", repeat, "normal"), false},
		{"no sampler and explicit repeat", []string{"sheet.png"},
			"a.gltf", sheetDoc("sheet.png", 0, "albedo"),
			"b.gltf", sheetDoc("sheet.png", repeat, "albedo"), true},
		// The directory half of the key, and the case that would be a WRONG
		// texture rather than a missed saving: two props in two folders, each
		// with its own sheet.png beside it. The URIs are identical and the
		// files are not.
		{"same URI in two directories", []string{"one/sheet.png", "two/sheet.png"},
			"one/a.gltf", sheetDoc("sheet.png", repeat, "albedo"),
			"two/b.gltf", sheetDoc("sheet.png", repeat, "albedo"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeSheetDocs(t, dir, tc.sheets, map[string]string{
				tc.aName: tc.aBody,
				tc.bName: tc.bBody,
			})
			r, d := textureFixture()
			fsys := os.DirFS(dir)
			if _, err := r.LoadGLTF(fsys, tc.aName); err != nil {
				t.Fatal(err)
			}
			if _, err := r.LoadGLTF(fsys, tc.bName); err != nil {
				t.Fatal(err)
			}

			want := 2
			if tc.share {
				want = 1
			}
			if got := d.created["Image"]; got != want {
				t.Errorf("created %d images, want %d", got, want)
			}
			if counts := r.ResourceCounts(); counts.CachedTextures != want || counts.TextureShares != 2 {
				t.Errorf("CachedTextures = %d (want %d), TextureShares = %d (want 2)", counts.CachedTextures, want, counts.TextureShares)
			}
		})
	}
}

// TestGLTFTextureCacheSkipsEmbeddedImages holds the line the issue draws: a
// GLB's buffer-view image cannot be shared by construction, because its bytes
// live inside the one document. Two loads of the same .glb therefore get two
// textures and the cache stays empty -- which is also the proof that the
// cache is not quietly keyed on something weaker, like the document's name.
//
// BROKEN: made externalImage return true for every non-nil image, so a
// buffer-view image is keyed on its document's directory and an empty URI.
// FAILED with "an embedded image was cached: CachedTextures = 1,
// TextureShares = 2, want 0 and 0" -- two loads of one .glb sharing a texture
// that has no file behind it at all.
func TestGLTFTextureCacheSkipsEmbeddedImages(t *testing.T) {
	r, d := textureFixture()
	fsys := os.DirFS("testdata/blender")

	first, err := r.LoadGLTF(fsys, "level.glb")
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.LoadGLTF(fsys, "level.glb")
	if err != nil {
		t.Fatal(err)
	}
	counts := r.ResourceCounts()
	if counts.CachedTextures != 0 || counts.TextureShares != 0 {
		t.Fatalf("an embedded image was cached: CachedTextures = %d, TextureShares = %d, want 0 and 0", counts.CachedTextures, counts.TextureShares)
	}
	if counts.Textures != 2 || d.created["Image"] != 2 {
		t.Errorf("two loads of one .glb made %d textures and %d images, want 2 and 2", counts.Textures, d.created["Image"])
	}
	if first.Meshes[0].Texture == second.Meshes[0].Texture {
		t.Error("two loads of one .glb shared an embedded image")
	}

	// And the uncached release path is the old one: destroyed outright.
	r.DestroyModel(first)
	r.DestroyModel(second)
	r.flushAllDeferred()
	if got := r.ResourceCounts().Textures; got != 0 {
		t.Errorf("Textures = %d after releasing both, want 0", got)
	}
	textureBalance(t, d)
}

// TestGLTFTextureCacheSkipsUncomparableFilesystem is the panic this feature
// could have shipped: an fs.FS is only usable as a map key when its dynamic
// type is comparable, and a map write with one that is not takes the process
// down rather than returning an error.
//
// fstest.MapFS is a map. wrappedFS is the subtler one: reflect reports it
// comparable, because a struct of one interface field IS comparable as a type,
// and the comparison panics anyway once that field holds the MapFS. It is here
// because comparableFS's reflect check alone would pass it.
//
// Neither is an error. The load succeeds and simply does not share, which is
// what the loader did for everyone before the cache existed.
//
// BROKEN: removed the probe from comparableFS, leaving only the reflect
// check. The struct_wrapping_a_map subtest PANICKED with "panic: hash of
// unhashable type: fstest.MapFS" rather than failing -- which is the point:
// without the probe this is a crash in a game's loading screen, not a
// missing optimisation. BROKEN again by skipping the comparableFS call
// entirely: the bare map subtest panicked with the same message.
func TestGLTFTextureCacheSkipsUncomparableFilesystem(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	mapFS := fstest.MapFS{
		"sheet.png": &fstest.MapFile{Data: buf.Bytes()},
		"a.gltf":    &fstest.MapFile{Data: []byte(sheetDoc("sheet.png", 0, "albedo"))},
		"b.gltf":    &fstest.MapFile{Data: []byte(sheetDoc("sheet.png", 0, "albedo"))},
	}
	for _, tc := range []struct {
		name string
		fsys fs.FS
	}{
		{"map", mapFS},
		{"struct wrapping a map", wrappedFS{mapFS}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, d := textureFixture()
			for _, name := range []string{"a.gltf", "b.gltf"} {
				if _, err := r.LoadGLTF(tc.fsys, name); err != nil {
					t.Fatalf("LoadGLTF %s: %v -- an uncacheable filesystem must still load", name, err)
				}
			}
			counts := r.ResourceCounts()
			if counts.CachedTextures != 0 || counts.TextureShares != 0 {
				t.Errorf("CachedTextures = %d, TextureShares = %d, want 0 and 0", counts.CachedTextures, counts.TextureShares)
			}
			if counts.Textures != 2 || d.created["Image"] != 2 {
				t.Errorf("Textures = %d, images = %d, want 2 and 2 -- uncached means one upload per document", counts.Textures, d.created["Image"])
			}
		})
	}
	if comparableFS(wrappedFS{mapFS}) {
		t.Error("comparableFS accepted a struct whose fs.FS field holds a map; the reflect check alone is not enough")
	}
	if !comparableFS(os.DirFS(".")) || !comparableFS(newCountingFS(os.DirFS("."))) {
		t.Error("comparableFS rejected a filesystem that IS a usable key")
	}
}

// wrappedFS is comparable as a type and not as a value: the thing every
// "just check reflect.Comparable" version of this gets wrong.
type wrappedFS struct{ inner fs.FS }

func (w wrappedFS) Open(name string) (fs.File, error) { return w.inner.Open(name) }

// TestGLTFImageUploadFailureReleasesEveryShare injects a failure at each
// Vulkan call an image upload makes and requires that nothing is left created
// and no share is left outstanding. A pinned share is the cache's own failure
// mode and it is quieter than a leaked texture: the texture is never freed AND
// the entry keeps being handed out, so the count never returns to its
// baseline and nothing else ever notices.
//
// The second document is loaded against a renderer that already shares the
// sheet, which is the case the unwind can get exactly backwards -- releasing
// the share it took is right, destroying the texture the first document is
// still drawing with is not.
//
// The meta-check at the end breaks the balance for every resource kind,
// because a balance check that has never failed proves nothing.
//
// BROKEN: emptied uploadGLTFImages's fail closure so it returned the error
// without giving the shares back. FAILED at every injected site with "a
// failed load left the cache at 1 entries/2 shares, want 1/1" -- the pinned
// share, which nothing else in the suite sees.
func TestGLTFImageUploadFailureReleasesEveryShare(t *testing.T) {
	dir := t.TempDir()
	writeSheetDocs(t, dir, []string{"sheet.png"}, map[string]string{
		"a.gltf": sheetDoc("sheet.png", 0, "albedo"),
		// two images, so a failure can land after one has been created
		"two.gltf": `{"asset":{"version":"2.0"},` +
			`"images":[{"uri":"sheet.png"},{"uri":"other.png"}],` +
			`"textures":[{"source":0},{"source":1}],` +
			`"materials":[{"name":"A","pbrMetallicRoughness":{"baseColorTexture":{"index":0}}},` +
			`{"name":"B","pbrMetallicRoughness":{"baseColorTexture":{"index":1}}}]}`,
	})
	if err := os.Link(filepath.Join(dir, "sheet.png"), filepath.Join(dir, "other.png")); err != nil {
		t.Fatal(err)
	}
	fsys := os.DirFS(dir)

	// Every run starts from a renderer that has already loaded a.gltf, so the
	// sheet is in the cache with one share and two.gltf's first image is a
	// HIT. sheet is that texture, which no unwind may touch; baseline is how
	// many of each call a.gltf itself made, so failAt can be counted from the
	// start of the load under test rather than from the start of the process.
	control := func(t testing.TB) (r *Renderer, d *textureTestDriver, sheet *Texture, baseline map[string]int) {
		t.Helper()
		r, d = textureFixture()
		if _, err := r.LoadGLTF(fsys, "a.gltf"); err != nil {
			t.Fatal(err)
		}
		if len(r.textures) != 1 {
			t.Fatalf("a.gltf uploaded %d textures, want 1", len(r.textures))
		}
		baseline = map[string]int{}
		for k, v := range d.calls {
			baseline[k] = v
		}
		return r, d, r.textures[0], baseline
	}
	r, ctl, sheet, baseline := control(t)
	if _, err := r.LoadGLTF(fsys, "two.gltf"); err != nil {
		t.Fatal(err)
	}
	if counts := r.ResourceCounts(); counts.CachedTextures != 2 || counts.TextureShares != 3 {
		t.Fatalf("fixture is wrong: CachedTextures = %d (want 2), TextureShares = %d (want 3 -- a.gltf plus two.gltf's two images, one of them the shared sheet)", counts.CachedTextures, counts.TextureShares)
	}
	if r.textures[0] != sheet {
		t.Fatal("fixture is wrong: two.gltf did not reuse a.gltf's sheet")
	}

	// CmdCopyBufferToImage and CmdBlitImage are absent on purpose: createTexture
	// discards what they return, so there is nothing to inject and no site to
	// unwind. The list is the calls whose failure createTexture actually acts
	// on.
	for _, call := range []string{"CreateBuffer", "AllocateMemory", "MapMemory", "CreateImage", "CreateImageView", "CreateSampler", "AllocateDescriptorSets"} {
		sites := ctl.calls[call] - baseline[call]
		if sites == 0 {
			t.Fatalf("no sites for %s", call)
		}
		for at := 1; at <= sites; at++ {
			t.Run(fmt.Sprintf("%s/%d", call, at), func(t *testing.T) {
				r, d, sheet, baseline := control(t)
				before := r.ResourceCounts()
				d.failCall, d.failAt = call, baseline[call]+at
				if _, err := r.LoadGLTF(fsys, "two.gltf"); err == nil {
					t.Fatal("missing injected error")
				}
				after := r.ResourceCounts()
				if after.CachedTextures != before.CachedTextures || after.TextureShares != before.TextureShares {
					t.Fatalf("a failed load left the cache at %d entries/%d shares, want %d/%d",
						after.CachedTextures, after.TextureShares, before.CachedTextures, before.TextureShares)
				}
				if after.Textures != before.Textures {
					t.Fatalf("a failed load left %d textures, want %d", after.Textures, before.Textures)
				}
				// The share two.gltf took of the sheet went back, and the
				// sheet itself did not: it belongs to the document that
				// loaded first, which is still drawing with it. Releasing
				// the share and destroying the texture is the mistake this
				// names.
				if len(r.textures) != 1 || r.textures[0] != sheet || sheet.destroyed {
					t.Fatalf("the unwind took the shared sheet with it: textures = %v, destroyed = %v", r.textures, sheet.destroyed)
				}
				r.Destroy()
				textureBalance(t, d)
			})
		}
		t.Logf("%s: all %d image-upload sites unwind without pinning a share", call, sites)
	}

	// Meta-check: the balance above has to be able to fail.
	r, meta, _, _ := control(t)
	r.Destroy()
	for kind, n := range meta.created {
		if n == 0 {
			continue
		}
		meta.destroyed[kind]--
		captured := &capturingT{TB: t}
		textureBalance(captured, meta)
		meta.destroyed[kind]++
		if !captured.failed {
			t.Fatalf("balance meta-check missed %s", kind)
		}
	}
	t.Log("balance meta-check detects a missing destroy for every image-upload resource kind")
}

// TestSharedSheetLoadCost is the measurement issue #136 asks for, at the scale
// it describes: N documents naming one 2048x2048 PNG, which is the size of the
// trim sheets in the report.
//
// It is a gate as well as a number -- one decode and one image, whatever N is
// -- but the numbers are the point, and they are recorded in
// docs/agents/models.md so the next person can re-run this rather than trust
// it. Go test's own -bench machinery is not used because the interesting
// quantity is total allocation over a one-shot load, not steady-state
// throughput.
//
// BROKEN: removed decodeGLTFImages's skip, leaving the upload shared and the
// decode not. FAILED with "4 documents naming one sheet: 4 decodes and 1
// images, want 1 and 1", and the line below read "decodes 4 -> 4, CreateImage
// 4 -> 1, TotalAlloc 193 MiB -> 145 MiB": sharing only the GPU texture leaves
// three quarters of the allocation in place.
func TestSharedSheetLoadCost(t *testing.T) {
	// Four documents rather than the sixty in the report: the cost is linear
	// in the uncached case and the run is the most expensive test in this
	// package as it is -- under -race, four uncached decodes and their NRGBA
	// conversions are most of the ten seconds it takes.
	const (
		docs = 4
		side = 2048
	)
	dir := t.TempDir()
	img := image.NewNRGBA(image.Rect(0, 0, side, side))
	for i := 0; i < len(img.Pix); i += 4 {
		x, y := (i/4)%side, (i/4)/side
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = uint8(x), uint8(y), uint8(x^y), 255
	}
	// BestSpeed for the fixture only. It changes the file on disk, not a
	// single decoded pixel, and it is worth naming because it is not free
	// elsewhere: under -race the default level spends 2.4s encoding this one
	// image and 0.75s per decode, against 0.47s and 0.37s here, which is most
	// of what this test costs `task ci`.
	var buf bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	sheet, err := os.Create(filepath.Join(dir, "sheet.png"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sheet.Write(buf.Bytes()); err != nil {
		t.Fatal(err)
	}
	sheet.Close()

	names := make([]string, docs)
	files := map[string]string{}
	for i := range docs {
		names[i] = fmt.Sprintf("prop%02d.gltf", i)
		files[names[i]] = sheetDoc("sheet.png", 0, "albedo")
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	load := func(share bool) (decodes, images int, alloc uint64) {
		r, d := textureFixture()
		defer r.Destroy()
		fsys := newCountingFS(os.DirFS(dir))
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		for _, name := range names {
			if _, err := r.LoadGLTF(fsys, name); err != nil {
				t.Fatal(err)
			}
			if !share {
				// Drop the cache between documents, which is what the loader
				// did before it had one: every document decodes and uploads
				// its own copy.
				r.gltfTextureCache = nil
				r.gltfTextureShares = nil
			}
		}
		runtime.ReadMemStats(&after)
		return fsys.opens["sheet.png"], d.created["Image"], after.TotalAlloc - before.TotalAlloc
	}

	wasDecodes, wasImages, wasAlloc := load(false)
	isDecodes, isImages, isAlloc := load(true)

	if isDecodes != 1 || isImages != 1 {
		t.Errorf("%d documents naming one sheet: %d decodes and %d images, want 1 and 1", docs, isDecodes, isImages)
	}
	if wasDecodes != docs || wasImages != docs {
		t.Fatalf("the uncached control is wrong: %d decodes and %d images for %d documents, want %d of each", wasDecodes, wasImages, docs, docs)
	}
	t.Logf("%d documents, one %dx%d PNG: decodes %d -> %d, CreateImage %d -> %d, TotalAlloc %.0f MiB -> %.0f MiB",
		docs, side, side, wasDecodes, isDecodes, wasImages, isImages,
		float64(wasAlloc)/(1<<20), float64(isAlloc)/(1<<20))
}
