package renderer

import (
	"io/fs"
	"log"
	"path"
	"reflect"

	"github.com/qmuntal/gltf"
)

// gltfTextureKey identifies one uploaded EXTERNAL glTF image well enough that
// two documents naming the same file get the same GPU texture (issue #136).
//
// Every field is part of the identity of the uploaded image, not of the
// document that asked for it:
//
//   - fsys is the filesystem the CALLER named, not rd.base. fs.Sub returns a
//     fresh *subFS on every call, so a key built from the document's own
//     subtree would be unique per load and share nothing -- which is the whole
//     bug this exists to fix, and it would have looked like it worked.
//   - path is path.Join(dir(gltf), uri), which cleans: "props/../trim/T.png"
//     from one document and "trim/T.png" from another are the same sheet and
//     have to key the same.
//   - wrap and srgb are the two things uploadGLTFImages decides per image that
//     change the bytes or the sampler. An image used as albedo in one document
//     and as a normal map in another is uploaded as _SRGB in one and as
//     _UNORM in the other; sharing those would bend every normal through a
//     gamma curve, silently (see dataImageIndices).
//
// Embedded images -- buffer views and data URIs -- get no key at all. They
// cannot be shared by construction: the bytes live inside the one document,
// so two documents carrying identical pixels are still two different images,
// and comparing the bytes to find out would cost what the cache is meant to
// save.
type gltfTextureKey struct {
	fsys fs.FS
	path string
	wrap imageWrap
	srgb bool
}

// cachedTexture is one shared texture and how many live models hold a share
// of it.
//
// shares is incremented by uploadGLTFImages per IMAGE INDEX it satisfies, not
// per document and not per ModelMesh, which is exactly what modelResources
// records -- a document that lists the same URI as two images gets two shares
// and hands back two. Keeping those two counts defined the same way is what
// makes DestroyModel's release a plain decrement rather than a set difference.
type cachedTexture struct {
	key    gltfTextureKey
	tex    *Texture
	shares int
}

// comparableFS reports whether fsys can be used as a map key without taking
// the process down.
//
// An interface value is only comparable when its dynamic type is, and a map
// write with a non-comparable key PANICS -- it is not an error the runtime
// hands back. os.DirFS (a string), embed.FS (a struct of one pointer) and
// fs.Sub's *subFS are all fine; fstest.MapFS is a map and is not.
//
// reflect.Comparable() alone is not enough, which is the part that is easy to
// get wrong: a struct type with an fs.FS FIELD reports comparable, and
// comparing two of them panics anyway if that field holds a map. The only
// reliable test is to perform the operation, so this performs it, on a
// throwaway map so that a panic cannot leave the real cache mid-write.
//
// A filesystem that fails this does not fail the load. The cache is an
// optimisation and the honest answer to "I cannot tell whether these two
// filesystems are the same" is "assume they are not", which is precisely what
// the loader did before this cache existed -- so such a caller keeps working,
// decoding and uploading per document exactly as it does today. Returning an
// error instead would turn a working LoadGLTF into a failing one for a
// performance feature. The log line is there so the lost sharing is a fact in
// the output rather than an unexplained number in ResourceCounts.
func comparableFS(fsys fs.FS) (ok bool) {
	t := reflect.TypeOf(fsys)
	if t == nil || !t.Comparable() {
		return false
	}
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	probe := make(map[fs.FS]struct{}, 1)
	probe[fsys] = struct{}{}
	return true
}

// warnedUncacheableFS remembers which filesystem types have already been
// reported, so a game that loads six hundred props from one uncacheable FS
// gets one line and not six hundred. Keyed by reflect.Type, which is
// comparable for every type including the ones this is about.
var warnedUncacheableFS = map[reflect.Type]bool{}

// gltfImageKeys builds the cache key for every image of rd's document that has
// one, keyed by image index. An image absent from the result is uploaded
// per-document the way it always has been.
//
// wrapModes and dataImages are passed in rather than recomputed because
// uploadGLTFImages needs both anyway and they have to be the SAME answers: a
// key that disagreed with the textureOptions the upload then used would share
// a texture uploaded with different sampler or encoding.
func gltfImageKeys(rd *gltfRead, dataImages map[int]bool, wrapModes map[int]imageWrap) map[int]gltfTextureKey {
	if rd.fsys == nil {
		return nil
	}
	if !comparableFS(rd.fsys) {
		t := reflect.TypeOf(rd.fsys)
		if !warnedUncacheableFS[t] {
			warnedUncacheableFS[t] = true
			log.Printf("gltf: %v cannot be used as a map key, so external images loaded from it are not shared between documents; wrap it in a pointer type to get sharing (see docs/agents/models.md)", t)
		}
		return nil
	}

	dir := path.Dir(rd.name)
	keys := make(map[int]gltfTextureKey, len(rd.doc.Images))
	for i, img := range rd.doc.Images {
		if !externalImage(img) {
			continue
		}
		wrap, ok := wrapModes[i]
		if !ok {
			wrap = repeatWrap
		}
		keys[i] = gltfTextureKey{
			fsys: rd.fsys,
			path: path.Join(dir, img.URI),
			wrap: wrap,
			srgb: !dataImages[i],
		}
	}
	return keys
}

// externalImage reports whether an image's pixels come from a file beside the
// document, in the same order decodeGLTFImages decides it: a buffer view wins
// over a URI, and a data: URI is bytes rather than a filename.
func externalImage(img *gltf.Image) bool {
	return img != nil && img.BufferView == nil && img.URI != "" && !img.IsEmbeddedResource()
}

// acquireGLTFTexture returns the shared texture for key and counts one more
// share, or nil when nothing has uploaded it yet.
func (r *Renderer) acquireGLTFTexture(key gltfTextureKey) *Texture {
	c := r.gltfTextureCache[key]
	if c == nil {
		return nil
	}
	c.shares++
	return c.tex
}

// shareGLTFTexture records a freshly uploaded texture as the shared one for
// key, with the uploading model's own share already counted.
func (r *Renderer) shareGLTFTexture(key gltfTextureKey, tex *Texture) {
	if r.gltfTextureCache == nil {
		r.gltfTextureCache = make(map[gltfTextureKey]*cachedTexture)
		r.gltfTextureShares = make(map[*Texture]*cachedTexture)
	}
	c := &cachedTexture{key: key, tex: tex, shares: 1}
	r.gltfTextureCache[key] = c
	r.gltfTextureShares[tex] = c
}

// releaseGLTFTexture gives back one share of t and destroys it when the last
// one goes. A texture that was never cached -- an embedded image, or one from
// a filesystem that cannot be a key -- is destroyed outright, which is what
// DestroyModel did to every texture before this existed.
//
// The eviction is NOT here: DestroyTexture does it, so there is exactly one
// place the cache can stop naming a texture and it is the place the texture
// actually dies. See DestroyTexture.
func (r *Renderer) releaseGLTFTexture(t *Texture) {
	if c, ok := r.gltfTextureShares[t]; ok {
		c.shares--
		if c.shares > 0 {
			return
		}
	}
	r.DestroyTexture(t)
}

// forgetGLTFTexture drops t from the cache. Called from DestroyTexture, so it
// covers both the last share going and an application destroying a shared
// texture behind DestroyModel's back -- in which case the remaining share
// holders have dangling pointers either way, but at least the next LoadGLTF
// does not get handed a destroyed texture on top of it.
func (r *Renderer) forgetGLTFTexture(t *Texture) {
	c, ok := r.gltfTextureShares[t]
	if !ok {
		return
	}
	delete(r.gltfTextureShares, t)
	delete(r.gltfTextureCache, c.key)
}
