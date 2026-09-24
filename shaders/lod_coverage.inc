// Constant 0: ordinary lit geometry, 1: ordered coverage, 2: MSAA coverage.
// Only the LOD pipeline specialises it; ordinary InstanceSet alpha is unused.
layout(constant_id = 0) const int lodCoverageMode = 0;
float lodCoverage(float fade, float phase) {
    const int bayer[16] = int[16](0,8,2,10,12,4,14,6,3,11,1,9,15,7,13,5);
    ivec2 p = ivec2(gl_FragCoord.xy) & 3;
    float threshold = (float(bayer[p.y*4+p.x])+0.5)/16.0;
    // Adjacent bands use opposite thresholds. Equal alpha-to-coverage masks
    // alone overlap, leaving background visible through both meshes at 50%.
    if (phase > 0.5) threshold = 1.0-threshold;
    if (lodCoverageMode == 1) {
        if (fade <= threshold) discard;
        return 1.0;
    }
    // MSAA smooths each ordered threshold through sample coverage. Only one
    // of sixteen pixel phases is fractional at once; the rest are complementary.
    return clamp((fade-threshold)*16.0+0.5, 0.0, 1.0);
}
