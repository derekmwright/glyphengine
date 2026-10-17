// cmd/tex3dcheck's probe table, shared by the graphics and the compute arm so
// that the two sample the same points through the same expression and differ
// only in which stage asked.
//
// Three volumes at set 2 bindings 0..2, each a row of the chart; the column
// picks a point and a level. Coordinates are in level-0 texel units, where the
// centre of texel (i,j,k) is (i+0.5, j+0.5, k+0.5), and are normalised by the
// extent in pc.data[0].xyz so one table serves every arm's extent.
layout(set = 2, binding = 0) uniform sampler3D suppliedRepeat;  // a supplied two-level chain, repeat on X, Y and Z
layout(set = 2, binding = 1) uniform sampler3D generatedRepeat; // a generated chain, repeat on X, Y and Z
layout(set = 2, binding = 2) uniform sampler3D suppliedClampZ;  // the supplied chain, repeat on X and Y, clamp on Z
layout(push_constant) uniform ApplicationPush { layout(offset = 128) vec4 data[8]; } pc;

const int probeColumns = 11;
const int probeRows = 3;

vec4 probe(int col, int row) {
    vec3 extent = pc.data[0].xyz;
    vec3 p;
    float lod = 0.0;
    switch (col) {
    case 0: p = vec3(1.5, 2.5, 3.5); break;   // the centre of texel (1,2,3): the stored value
    case 1: p = vec3(1.5, 2.5, 3.0); break;   // halfway between slices 2 and 3: their mean, if Z interpolates
    case 2: p = vec3(2.0, 2.5, 3.5); break;   // halfway in X
    case 3: p = vec3(1.5, 2.0, 3.5); break;   // halfway in Y
    case 4: p = vec3(0.0, 2.5, 3.5); break;   // the X edge: repeat blends the last column into the first
    case 5: p = vec3(1.5, 0.0, 3.5); break;   // the Y edge
    case 6: p = vec3(1.5, 2.5, 0.0); break;   // the Z edge: repeat blends the last slice in; clamp holds slice 0
    case 7: p = vec3(1.5, 2.5, extent.z); break; // the far Z edge: the same point under repeat; clamp holds the last slice
    case 8: p = vec3(1.5, 2.5, -0.25); break; // a quarter texel past the Z edge: three parts last slice, one part first
    case 9: p = vec3(1.0, 3.0, 3.0); lod = 1.0; break; // level 1, the centre of its texel (0,1,1)
    default: p = vec3(2.0, 2.0, 2.0); lod = 2.0; break; // level 2, the centre of its texel (0,0,0)
    }
    vec3 uvw = p / extent;
    if (row == 0) return textureLod(suppliedRepeat, uvw, lod);
    if (row == 1) return textureLod(generatedRepeat, uvw, lod);
    return textureLod(suppliedClampZ, uvw, lod);
}
