package renderer

import (
	"math"
	"testing"
)

// decodeSkyMapUV is the shader's half of the projection, transcribed from
// stars.frag:
//
//	vec2 p = dir.xz / (abs(dir.x) + abs(dir.y) + abs(dir.z));
//	vec2 uv = vec2(p.x + p.y, p.x - p.y) * 0.5 + 0.5;
//
// It is duplicated here on purpose. EquirectToSkyMap and this expression are
// inverses of each other across a language boundary, nothing checks that at
// build time, and getting the fold transposed produces a sky that draws
// perfectly while being mirrored -- the kind of wrong that looks like art
// direction until someone recognises Orion facing the wrong way.
func decodeSkyMapUV(dir [3]float64) (u, v float64) {
	l := math.Abs(dir[0]) + math.Abs(dir[1]) + math.Abs(dir[2])
	px, pz := dir[0]/l, dir[2]/l
	return (px+pz)*0.5 + 0.5, (px-pz)*0.5 + 0.5
}

// equirectUV is where a direction lands in the source panorama: the same
// galactic rotation and equirect convention EquirectToSkyMap uses.
func equirectUV(dir [3]float64) (u, v float64) {
	gy := galacticPole
	gz := normalize3(cross3(gy, [3]float64{0, 1, 0}))
	gx := cross3(gy, gz)
	g := [3]float64{dot3(dir, gx), dot3(dir, gy), dot3(dir, gz)}
	return math.Atan2(g[2], g[0])/(2*math.Pi) + 0.5,
		math.Acos(math.Min(1, math.Max(-1, g[1]))) / math.Pi
}

// gradientEquirect builds a panorama whose red and green channels encode the
// texel's own (u, v). Reading a colour back out of the sky map therefore says
// exactly which part of the source landed there.
func gradientEquirect(w, h int) []byte {
	pix := make([]byte, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			o := (y*w + x) * 4
			pix[o] = byte((float64(x) + 0.5) / float64(w) * 255)
			pix[o+1] = byte((float64(y) + 0.5) / float64(h) * 255)
			pix[o+3] = 255
		}
	}
	return pix
}

// TestSkyMapMatchesTheShaderProjection walks directions across the upper
// hemisphere, asks the shader's decode where each one samples the map, and
// checks the texel there holds the part of the panorama that direction points
// at.
//
// Verified by breaking it: transposing the octahedral fold to
// vec2(p.x-p.y, p.x+p.y) -- a mirrored sky, and a plausible typo -- takes the
// worst error from 0.009 to 0.938 and fails this.
func TestSkyMapMatchesTheShaderProjection(t *testing.T) {
	const (
		srcW, srcH = 512, 256
		size       = 256
	)
	src := gradientEquirect(srcW, srcH)
	m := EquirectToSkyMap(src, srcW, srcH, size)

	// Bilinear across a wrapping seam blends 0 and 1 into 0.5, so a direction
	// landing near longitude 0 legitimately reads back nothing like its own u.
	// Skip that band rather than loosening the tolerance for every sample.
	const seam = 0.02

	var worst, worstAt float64
	var checked int
	for _, elev := range []float64{0.08, 0.25, 0.5, 0.75, 0.95} {
		for az := 0.0; az < 2*math.Pi; az += math.Pi / 12 {
			horiz := math.Sqrt(1 - elev*elev)
			dir := [3]float64{horiz * math.Cos(az), elev, horiz * math.Sin(az)}

			wantU, wantV := equirectUV(dir)
			if wantU < seam || wantU > 1-seam {
				continue
			}

			u, v := decodeSkyMapUV(dir)
			x := int(u * float64(size))
			y := int(v * float64(size))
			if x < 0 || y < 0 || x >= size || y >= size {
				t.Fatalf("direction %v decodes to (%.3f, %.3f), outside the map", dir, u, v)
			}
			o := (y*size + x) * 4
			gotU := float64(m[o]) / 255
			gotV := float64(m[o+1]) / 255

			checked++
			for _, d := range []float64{math.Abs(gotU - wantU), math.Abs(gotV - wantV)} {
				if d > worst {
					worst, worstAt = d, elev
				}
			}
		}
	}

	if checked < 40 {
		t.Fatalf("only %d directions checked; the seam filter is eating the sample set", checked)
	}
	// One source texel is 1/512 in u, and the map is coarser than the panorama
	// near the horizon, so a few texels of disagreement is expected: the measured
	// worst is 0.009. 0.02 leaves room for that without approaching the 0.938 a
	// transposed fold gives.
	if worst > 0.02 {
		t.Errorf("sky map disagrees with the shader's projection by %.3f (worst near elevation %.2f); "+
			"EquirectToSkyMap and stars.frag have different conventions", worst, worstAt)
	}
}

// TestSkyMapCoversTheHemisphere checks the square is fully used: every texel
// decodes to a real direction above the horizon, which is the entire reason for
// preferring this over cropping an equirect.
func TestSkyMapCoversTheHemisphere(t *testing.T) {
	const size = 64
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			tx := (float64(x)+0.5)/float64(size)*2 - 1
			ty := (float64(y)+0.5)/float64(size)*2 - 1
			px := (tx + ty) * 0.5
			pz := (tx - ty) * 0.5
			if elev := 1 - math.Abs(px) - math.Abs(pz); elev < 0 {
				t.Fatalf("texel (%d,%d) decodes below the horizon (y=%.4f); "+
					"the map is wasting texels the star pass will never sample", x, y, elev)
			}
		}
	}
}
