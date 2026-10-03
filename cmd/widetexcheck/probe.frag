#version 450

// cmd/widetexcheck's probe: how far a texture's sampled value is from the exact
// one, written into the frame so an 8-bit capture can read it.
//
// Both textures are bound through Renderer.SetShaderTexture, which is light-set
// bindings 7 and 8 -- the seam a game reaches a CPU-uploaded texture through, so
// the thing under test is measured the way it is used.
layout(set = 1, binding = 7) uniform sampler2D measured; // the texture under test
layout(set = 1, binding = 8) uniform sampler2D exact;    // R32F, the same ramp exactly

layout(push_constant) uniform Push {
    // x = frame width in pixels, y = ramp length in texels, z = the control's
    // decode range (0 for a texture that holds its values directly).
    layout(offset = 128) vec4 p;
} pc;

layout(location = 0) out vec4 color;

void main() {
    int n = int(pc.p.y);
    int col = clamp(int(gl_FragCoord.x / pc.p.x * pc.p.y), 0, n - 1);

    // texelFetch, not texture(): the ramp is one texel per entry and the whole
    // point is to read the stored value rather than a blend of two of them. It
    // also means neither sampler's filter is in the measurement, so this says
    // something about the FORMAT and not about the sampler.
    float got = texelFetch(measured, ivec2(col, 0), 0).r;

    // The 8-bit control stores sqrt(v/range) in a UNORM texture, which is the
    // transfer x/sky/lut used before this upload path existed. Undoing it here is
    // what makes the comparison radiance against radiance either way, so the
    // number the two configurations produce is the format's error and not the
    // difference between two conventions.
    if (pc.p.z > 0.0) {
        got = got * got * pc.p.z;
    }

    float want = texelFetch(exact, ivec2(col, 0), 0).r;
    float rel = abs(got - want) / want;

    // Three decades of gain in three channels, so one 8-bit capture resolves a
    // relative error from 1/255/1000 = 3.9e-6 in red up to 1 in blue. main.go
    // reads the finest channel that has not saturated. Without this the whole
    // measurement would be limited to the 1/255 the frame itself carries, which
    // is 0.4 percent -- four times the claim being made.
    vec3 v = clamp(vec3(rel * 1000.0, rel * 10.0, rel), 0.0, 1.0);

    // Undo the swapchain's sRGB encoding so each captured byte measures v
    // directly rather than through a curve, as cmd/apppasscheck's
    // target-display.frag does for the same reason.
    color = vec4(mix(v / 12.92, pow((v + 0.055) / 1.055, vec3(2.4)), greaterThan(v, vec3(0.04045))), 1.0);
}
