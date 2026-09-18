#version 450

layout(location = 0) in vec2 fragUV;
layout(location = 1) in vec4 fragColor;

layout(push_constant) uniform PushConstants {
    mat4 mvp;
    mat4 model;
    vec4 tint;
    vec4 params; // x = textureMode (0=panel, 1=straight texture)
    vec4 fill;   // rgb = panel interior colour, w = its opacity; w < 0 = derive
} pc;

layout(set = 0, binding = 0) uniform sampler2D uiTexture;
layout(location = 0) out vec4 outColor;

#include "srgb.inc"

// edgeCoverage returns how much of this pixel the quad actually covers, from
// the distance to the quad's outer edge in UV space.
//
// The UI is composited after the tonemap, onto a single-sampled swapchain, so
// there is no MSAA coverage to smooth a panel edge -- and there is none to be
// had, because MSAA is resolved into the HDR target long before this pass runs.
// Every engine that composites its UI after tone mapping is in the same
// position and computes coverage instead. Text already did: MSDF antialiases in
// the fragment shader and never depended on the rasterizer.
//
// min(uv, 1-uv) is the distance to the nearest *outer* edge. That is what makes
// it correct for a nine-slice: the nine quads tile the atlas, so the panel's
// outer boundary is exactly where UV reaches 0 or 1, while the interior seams
// sit at inset/texSize and keep a positive distance. Antialiasing those would
// draw a visible line down the middle of every panel where two quads abut.
//
// fwidth converts UV to pixels, which handles each quad having its own
// UV-per-pixel ratio -- corner quads clamp to half the panel for a panel
// narrower than two corners, so that ratio is not constant across a panel.
//
// A quad whose geometry was not expanded past its edge still gets the inner
// half of the ramp: the rasterizer only generates fragments whose centre is
// inside, so the distance never goes negative. That is a softer edge biased
// half a pixel inward rather than a hard step, which is why this degrades
// gracefully on any quad emitter that has not been updated.
float edgeCoverage(vec2 uv) {
    vec2 d = min(uv, 1.0 - uv);
    vec2 w = max(fwidth(uv), vec2(1e-8));
    vec2 px = d / w;
    return clamp(min(px.x, px.y) + 0.5, 0.0, 1.0);
}

void main() {
    // The skirt carries UV outside [0,1] so the coverage ramp has an outside
    // half. The sampler addresses Repeat, so it has to be clamped back before
    // sampling or the skirt wraps to the far edge of the atlas.
    vec4 texel = texture(uiTexture, clamp(fragUV, 0.0, 1.0));
    float coverage = edgeCoverage(fragUV);

    // The game's colour is sRGB; the texel is not, because a colour texture is
    // created as R8G8B8A8_SRGB and the sampler has already decoded it.
    vec3 color = srgbToLinear(fragColor.rgb);

    if (pc.params.x > 0.5) {
        // Straight texture mode: texture * colour, respecting texture alpha.
        outColor = vec4(texel.rgb * color, texel.a * fragColor.a * coverage);
    } else {
        // Panel 9-slice mode.
        // Border (texel.a=1): decorated border at full tint opacity.
        // Center (texel.a=0): the interior fill.
        vec3 fillColor;
        float fillAlpha;
        if (pc.fill.w < 0.0) {
            // Derived: the look this shader had before the interior was
            // configurable, kept so a panel that asks for nothing does not move.
            fillColor = color * 0.2;
            fillAlpha = fragColor.a * 0.7;
        } else {
            fillColor = srgbToLinear(pc.fill.rgb);
            fillAlpha = pc.fill.w;
        }

        vec3 borderColor = color * texel.rgb;
        outColor = vec4(
            mix(fillColor, borderColor, texel.a),
            mix(fillAlpha, fragColor.a, texel.a) * coverage
        );
    }
}
