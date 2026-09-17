// sRGB transfer function, for colours that arrive from a game rather than from
// a texture.
//
// The swapchain is B8G8R8A8_SRGB, so the hardware applies the linear-to-sRGB
// encode on write. A shader that passes a game's colour straight through is
// therefore declaring it linear, and a UI author picking 0.05 to mean "nearly
// black" gets 0.248 on the display -- a mid slate. The error is largest exactly
// in the dark values a HUD is built from, and it is invisible until someone
// compares the colour they chose against the pixel they got.
//
// Decoding here makes a UI colour mean what it says: what a game writes is what
// reaches the display.
//
// Texels do not go through this. A colour texture is created as
// R8G8B8A8_SRGB (see textureOptions.srgb), so the sampler has already decoded
// it, and decoding twice would darken every texture the UI draws.
//
// Branchless because the two halves are cheap and a UI fragment shader runs
// over large areas of flat colour where a divergent branch buys nothing.
vec3 srgbToLinear(vec3 c) {
    return mix(c / 12.92,
               pow((c + 0.055) / 1.055, vec3(2.4)),
               step(vec3(0.04045), c));
}
