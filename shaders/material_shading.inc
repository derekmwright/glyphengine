// Material shading: the body shared by every pipeline that reads a Material.
//
// Included rather than duplicated because the only thing that differs between
// the static and skinned variants is which descriptor set the shadow data sits
// in -- set 1 when there are no joints, set 2 when there are. That is a
// declaration, not logic, so each .frag declares its own and includes this.
//
// The includer must have already declared: the fragment inputs, the material
// set (albedoTex through matl), the shadow set, the push constant block, the
// outColor output, and lighting.inc. lit_material.frag is the reference.

// perturbNormal applies a tangent-space normal map using a frame derived from
// screen-space derivatives of world position and UV.
//
// The alternative is a real tangent vertex attribute, which is higher quality on
// heavily distorted UVs but touches every mesh builder, every vertex buffer, and
// every pipeline's vertex input state. This costs four derivatives and reads the
// frame out of data the rasterizer is already interpolating, so normal maps land
// without the vertex format changing at all.
//
// Standard cotangent-frame construction: solve for the tangent and bitangent
// that carry the UV gradients, with N held fixed so the map perturbs the
// interpolated normal rather than replacing it.
vec3 perturbNormal(vec3 N, vec3 worldPos, vec2 uv, vec3 mapSample, vec2 mapScale) {
    vec3 dp1 = dFdx(worldPos);
    vec3 dp2 = dFdy(worldPos);
    vec2 duv1 = dFdx(uv);
    vec2 duv2 = dFdy(uv);

    vec3 dp2perp = cross(dp2, N);
    vec3 dp1perp = cross(N, dp1);
    vec3 T = dp2perp * duv1.x + dp1perp * duv2.x;
    vec3 B = dp2perp * duv1.y + dp1perp * duv2.y;

    // A face with zero UV area leaves T and B at zero, and normalizing that
    // produces NaN -- which propagates through the lighting and paints black or
    // white garbage rather than failing visibly. Fall back to the geometric
    // normal, which is what an unmapped surface would have used anyway.
    float maxLen = max(dot(T, T), dot(B, B));
    if (maxLen < 1e-16) {
        return N;
    }

    float invMax = inversesqrt(maxLen);
    mat3 TBN = mat3(T * invMax, B * invMax, N);

    vec3 n = mapSample * 2.0 - 1.0;
    n.xy *= mapScale;
    return normalize(TBN * n);
}

void main() {
    vec4 texSample = texture(albedoTex, fragUV);
    if (texSample.a < 0.5) discard; // alpha test for foliage cutout
    vec3 baseColor = fragColor * texSample.rgb;

    // Emissive early-out: tint.w > 0 bypasses lighting (used for moon disc etc.)
    if (pc.tint.w > 0.0) {
        outColor = vec4(baseColor, 1.0);
        return;
    }

    // The per-object values stay factors that the maps multiply, which is both
    // glTF's rule and what keeps MeshRef.Metallic/.Roughness meaningful on a
    // material that supplies no metallic-roughness map.
    float metallic = pc.ambient.w;
    float roughness = pc.pointColor.w;
    if (matl.maps.y > 0.5) {
        vec4 mr = texture(metalRoughTex, fragUV);
        roughness *= mr.g;
        metallic *= mr.b;
    }
    roughness = clamp(roughness, 0.04, 1.0);
    float shininess = clamp(2.0 / (roughness * roughness) - 2.0, 1.0, 2048.0);

    vec3 diffuseColor = baseColor * (1.0 - metallic);
    vec3 F0 = mix(vec3(0.04), baseColor, metallic);

    vec3 V = normalize(pc.cameraPos.xyz - fragWorldPos);
    vec3 N = normalize(fragWorldNormal);
    if (matl.maps.x > 0.5) {
        N = perturbNormal(N, fragWorldPos, fragUV, texture(normalTex, fragUV).rgb, matl.scale.xy);
    }

    float ao = 1.0;
    if (matl.maps.z > 0.5) {
        ao = mix(1.0, texture(occlusionTex, fragUV).r, matl.maps.w);
    }

    vec3 lit = evalLightingAO(diffuseColor, F0, shininess, N, V, fragWorldPos, fragShadowPos, ao);

    // Emission is added after lighting and before fog. It takes no shadow and no
    // occlusion -- a surface that emits does so whether or not a light can see
    // it, and baked AO describes how much ambient reaches the surface, not how
    // much leaves it. Fog still applies, because the air between the surface and
    // the eye scatters emitted light exactly like any other.
    //
    // This is the one term in the engine allowed to exceed 1. It reaches the
    // half-float target intact; see docs/agents/hdr-tonemap.md.
    vec3 emission = matl.emissive.rgb;
    if (matl.emissive.w > 0.5) {
        emission *= texture(emissiveTex, fragUV).rgb;
    }
    lit += emission;

    outColor = vec4(applyFog(lit, fragWorldPos), 1.0);
}
