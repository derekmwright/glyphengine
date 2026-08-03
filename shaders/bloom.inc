// Shared sampling kernels for the bloom chain.
//
// Both the downsample and the upsample are wider than a naive bilinear step on
// purpose. Halving an image with one bilinear tap aliases badly, and bloom is
// exactly the case where that shows: a bright pixel that survives at one mip and
// not the next flickers as the camera moves, and the flicker is the brightest
// thing on screen. These are the kernels from Jimenez's SIGGRAPH 2014 course
// notes, which is where most engines' bloom comes from.

// downsample13 takes thirteen bilinear taps in a pattern that weights the centre
// quad heavily and the corners lightly, approximating a wide tent in one pass.
//
// texel is one texel of the SOURCE image, not the destination.
vec3 downsample13(sampler2D src, vec2 uv, vec2 texel) {
    vec3 a = texture(src, uv + texel * vec2(-2.0, -2.0)).rgb;
    vec3 b = texture(src, uv + texel * vec2( 0.0, -2.0)).rgb;
    vec3 c = texture(src, uv + texel * vec2( 2.0, -2.0)).rgb;

    vec3 d = texture(src, uv + texel * vec2(-1.0, -1.0)).rgb;
    vec3 e = texture(src, uv + texel * vec2( 1.0, -1.0)).rgb;

    vec3 f = texture(src, uv + texel * vec2(-2.0,  0.0)).rgb;
    vec3 g = texture(src, uv).rgb;
    vec3 h = texture(src, uv + texel * vec2( 2.0,  0.0)).rgb;

    vec3 i = texture(src, uv + texel * vec2(-1.0,  1.0)).rgb;
    vec3 j = texture(src, uv + texel * vec2( 1.0,  1.0)).rgb;

    vec3 k = texture(src, uv + texel * vec2(-2.0,  2.0)).rgb;
    vec3 l = texture(src, uv + texel * vec2( 0.0,  2.0)).rgb;
    vec3 m = texture(src, uv + texel * vec2( 2.0,  2.0)).rgb;

    // The inner four taps carry half the weight between them; the three outer
    // quads share the rest. Weights sum to 1.
    vec3 sum = (d + e + i + j) * 0.125;
    sum += (a + b + g + f) * 0.03125;
    sum += (b + c + h + g) * 0.03125;
    sum += (f + g + l + k) * 0.03125;
    sum += (g + h + m + l) * 0.03125;
    return sum;
}

// upsampleTent takes a 3x3 tent filter, which is what makes the accumulated
// chain read as a smooth halo rather than as visible mip boundaries.
//
// radius widens the tent in source texels. Larger spreads the glow further at
// the cost of it going soft.
vec3 upsampleTent(sampler2D src, vec2 uv, vec2 texel, float radius) {
    vec2 o = texel * radius;

    vec3 sum = texture(src, uv + vec2(-o.x,  o.y)).rgb;
    sum += texture(src, uv + vec2( 0.0,  o.y)).rgb * 2.0;
    sum += texture(src, uv + vec2( o.x,  o.y)).rgb;

    sum += texture(src, uv + vec2(-o.x,  0.0)).rgb * 2.0;
    sum += texture(src, uv).rgb * 4.0;
    sum += texture(src, uv + vec2( o.x,  0.0)).rgb * 2.0;

    sum += texture(src, uv + vec2(-o.x, -o.y)).rgb;
    sum += texture(src, uv + vec2( 0.0, -o.y)).rgb * 2.0;
    sum += texture(src, uv + vec2( o.x, -o.y)).rgb;

    return sum * (1.0 / 16.0);
}
