package renderer

import "math"

// Float16 and Float16Value are the IEEE 754 binary16 transfer, in both
// directions.
//
// They are here because CreateTextureRGBA16F takes the half-float BITS rather
// than float32s: a caller with radiance in float32 has to encode, and hand-
// rolling that is the kind of arithmetic that is wrong only at the edges --
// subnormals below 2^-14, the round-to-nearest-even tie, and the overflow above
// 65504 that must become an infinity rather than wrapping the exponent into a
// small positive number. None of those show up in a gradient's middle, which is
// what a first test of a new upload path looks at.
//
// Taking []float32 and converting inside the constructor was the alternative. It
// would make the API impossible to misuse, and it was not chosen because it
// hides an allocation the size of the image inside what is otherwise a copy, and
// because a caller who already holds halves -- read from a file, or produced by
// a previous frame -- would have to widen them back to have them narrowed again.
//
// Go has no float16 type and no stdlib conversion, so these are the whole of
// what exists.

// Float16 returns v as IEEE 754 binary16 bits, rounded to nearest with ties to
// even -- the same rule the hardware uses writing a half-float render target, so
// a value that makes the round trip through a texture and a value encoded here
// agree.
//
// A magnitude above binary16's largest finite 65504 becomes an infinity rather
// than clamping. That is the IEEE answer and it is also the useful one: an
// infinity in a texture is visible as a hole in the frame, where a silent clamp
// to 65504 is a number that looks like data.
func Float16(v float32) uint16 {
	b := math.Float32bits(v)
	sign := uint16(b>>16) & 0x8000
	exp := int32(b>>23) & 0xFF
	mant := b & 0x7FFFFF

	if exp == 0xFF {
		if mant != 0 {
			// A quiet NaN with a nonzero payload, rather than mant>>13, which is
			// zero for every NaN whose payload lives in the low bits -- and a
			// NaN with a zero mantissa is an infinity.
			return sign | 0x7E00
		}
		return sign | 0x7C00
	}
	if exp == 0 && mant == 0 {
		return sign // Signed zero; float32 has no other subnormal binary16 can hold.
	}

	// binary16's exponent bias is 15 against float32's 127.
	e := exp - 112
	switch {
	case e >= 0x1F:
		return sign | 0x7C00 // Overflow, including every float32 infinity's neighbours.
	case e <= 0:
		// Subnormal binary16, or below it. The implicit leading 1 comes back
		// into the mantissa and the whole significand shifts right, which is
		// where the precision this format has at small magnitudes comes from:
		// 2^-24 is representable, 2^-25 is not.
		if e < -10 {
			return sign
		}
		m := mant | 0x800000
		shift := uint32(14 - e)
		half := uint16(m >> shift)
		rem := m & (1<<shift - 1)
		tie := uint32(1) << (shift - 1)
		if rem > tie || (rem == tie && half&1 == 1) {
			half++ // Carries into the normal range, which is the correct answer there.
		}
		return sign | half
	default:
		half := uint16(e)<<10 | uint16(mant>>13)
		rem := mant & 0x1FFF
		if rem > 0x1000 || (rem == 0x1000 && half&1 == 1) {
			// Carries through the mantissa into the exponent, and from the
			// largest finite value into the infinity above it.
			half++
		}
		return sign | half
	}
}

// Float16Value decodes IEEE 754 binary16 bits to the float32 that represents
// them exactly -- every binary16 value is a float32 value, so this loses
// nothing.
//
// It is the inverse of Float16 for everything except a NaN payload, which
// Float16 replaces with a quiet one.
func Float16Value(h uint16) float32 {
	sign := uint32(h>>15) << 31
	exp := uint32(h>>10) & 0x1F
	mant := uint32(h) & 0x3FF
	switch exp {
	case 0:
		if mant == 0 {
			return math.Float32frombits(sign)
		}
		// Subnormal in binary16, normal in float32: shift the significand up
		// until the leading 1 appears and pay for it in the exponent.
		shift := uint32(0)
		for mant&0x400 == 0 {
			mant <<= 1
			shift++
		}
		mant &= 0x3FF
		return math.Float32frombits(sign | (127-15-shift+1)<<23 | mant<<13)
	case 0x1F:
		return math.Float32frombits(sign | 0xFF<<23 | mant<<13)
	default:
		return math.Float32frombits(sign | (exp+127-15)<<23 | mant<<13)
	}
}
