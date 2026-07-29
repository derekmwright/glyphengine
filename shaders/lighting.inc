// Shared lighting functions for lit fragment shaders.
// Expects shadowMap (sampler2DArrayShadow), pointShadowMap, lb, pc, and
// shadow (ShadowData UBO with mat4 cascadeVP[2]) to be declared by the
// including file.
//
// Light contributions use the "irradiance" pattern (same as three.js):
// irradiance = saturate(NdotL) * lightColor, then both diffuse and specular
// are multiplied by irradiance. This structurally prevents specular from
// leaking through surfaces where NdotL <= 0.

#include "atmosphere.inc"

const int NumCascades = 2;

// calcShadow selects the first cascade whose frustum contains the (normal-
// offset) world position, then applies 5x5 PCF on that cascade's layer.
float calcShadow(vec3 shadowPos, float NdotL) {
    for (int c = 0; c < NumCascades; c++) {
        vec4 lsPos = shadow.cascadeVP[c] * vec4(shadowPos, 1.0);
        vec3 proj = lsPos.xyz; // orthographic: w == 1
        proj.xy = proj.xy * 0.5 + 0.5;

        // Small margin so PCF taps near the edge fall through to the next
        // cascade instead of clamping.
        if (proj.x < 0.01 || proj.x > 0.99 || proj.y < 0.01 || proj.y > 0.99 || proj.z < 0.0 || proj.z > 1.0) {
            continue;
        }

        // Normal-based bias: surfaces at grazing angles need much more bias.
        // The far cascade covers more world units per texel, so scale up.
        float biasScale = (c == 0) ? 1.0 : 4.0;
        float bias = max(0.008 * (1.0 - NdotL), 0.001) * biasScale;
        float ref = proj.z - bias;

        // 5x5 PCF
        float result = 0.0;
        vec2 texelSize = 1.0 / vec2(textureSize(shadowMap, 0).xy);
        for (int x = -2; x <= 2; x++) {
            for (int y = -2; y <= 2; y++) {
                result += texture(shadowMap, vec4(proj.xy + vec2(x, y) * texelSize, float(c), ref));
            }
        }
        return mix(0.35, 1.0, result / 25.0);
    }
    return 1.0; // outside all cascades = fully lit
}

float calcPointShadow(vec3 fragPos, vec3 lightPos, float range) {
    vec3 lightToFrag = fragPos - lightPos;
    float faceDepth = max(abs(lightToFrag.x), max(abs(lightToFrag.y), abs(lightToFrag.z)));
    float storedDepth = texture(pointShadowMap, lightToFrag).r;
    float near = 0.1;
    float closestZ = near * range / (range - storedDepth * (range - near));
    float bias = 0.03;
    return faceDepth - bias > closestZ ? 0.35 : 1.0;
}

// fogColor returns the same horizon colour sky.frag draws, so fogged geometry
// melts into the sky rather than into a colour that merely resembles it.
//
// It shares the model rather than reimplementing it. The two used to be
// separate copies of the same constants, kept in step by a comment.
vec3 fogColor(vec3 viewDir) {
    // The scattering has to come from the real sun, not from pc.sunDir -- that
    // is the moon after dusk, and glowing warm around it puts a sunset on the
    // wrong side of the sky at the wrong time of night.
    float sunY = pc.sunColor.w;
    vec3 sunDir = atmSunDirFrom(sunY, pc.fog.zw);
    vec3 zenith, horizon;
    atmSkyPalette(sunY, zenith, horizon);
    return horizon + atmSunGlow(viewDir, sunDir, pc.sunColor.rgb, sunY);
}

// applyFog blends toward the horizon colour, then applies the night shift.
//
// Two modes, chosen by pc.fog.x:
//
// Uniform (fog.x == 0) is the original: density is the same at every altitude,
// so the only thing that thickens fog is distance. It is cheap and it is what
// every existing scene was tuned against, so it stays the default.
//
// Height fog (fog.x > 0) makes density fall off exponentially with altitude,
// which is what real fog does — it settles. That gives a valley floor thicker
// air than the ridge above it, and a mountain that emerges from haze as it
// rises, neither of which a distance-only fog can express no matter how it is
// tuned.
//
// The integral along the view ray has a closed form, so this costs two
// exponentials rather than a raymarch:
//
//	density(y) = D * exp(-(y - y0) / H)
//	tau        = integral of density along the ray
//	           = D*H*L/(dy) * (exp(-(Py-y0)/H) - exp(-(Qy-y0)/H))
//
// where L is the ray length and dy its vertical extent. As dy approaches zero
// that expression is 0/0, and a horizontal ray is not an edge case — it is
// where the camera spends most of its time — so it falls back to the constant
// density at eye height.
//
// The night shift lives here because every lit shader calls applyFog exactly
// once as its last step, so this is the one place that cannot miss a surface.
//
// Density rides in pc.cameraPos.w; zero disables fog.
vec3 applyFog(vec3 color, vec3 worldPos) {
    float density = pc.cameraPos.w;
    if (density <= 0.0) {
        return atmNightShift(color, pc.sunColor.w);
    }

    vec3 toFrag = worldPos - pc.cameraPos.xyz;
    float dist = length(toFrag);
    vec3 viewDir = toFrag / max(dist, 0.001);

    float heightFalloff = pc.fog.x;

    // How much of the nominal density this ray actually passes through. One
    // for uniform fog; less when the ray spends its length above the base
    // height, more when it runs along the bottom.
    float amount = 1.0;
    if (heightFalloff > 0.0) {
        float invH = 1.0 / heightFalloff;
        float camRel = (pc.cameraPos.y - pc.fog.y) * invH;
        float fragRel = (worldPos.y - pc.fog.y) * invH;
        float dy = fragRel - camRel;

        if (abs(dy) < 1e-4) {
            amount = exp(-camRel);
        } else {
            amount = (exp(-camRel) - exp(-fragRel)) / dy;
        }
        amount = max(amount, 0.0);
    }

    // Both modes share the same exp-squared curve, so Density means the same
    // thing either way and Height only redistributes fog vertically. Making
    // the height mode a plain Beer term instead -- which is what the integral
    // naturally gives -- is linear in distance where the other is quadratic,
    // and turning Height on then thickens every scene at the same Density.
    float d = dist * density * amount;
    float f = 1.0 - exp(-d * d);

    return atmNightShift(mix(color, fogColor(viewDir), clamp(f, 0.0, 1.0)), pc.sunColor.w);
}

// Blinn-Phong specular BRDF (returns unscaled specular color).
vec3 brdfSpecular(vec3 L, vec3 V, vec3 N, vec3 F0, float shininess) {
    vec3 H = normalize(L + V);
    float NdotH = max(dot(N, H), 0.0);
    float VdotH = max(dot(V, H), 0.0);
    float D = pow(NdotH, shininess);
    vec3 F = F0 + (1.0 - F0) * pow(1.0 - VdotH, 5.0);
    return F * D;
}

// Evaluate all light sources and return final lit color.
// shadowPos is the normal-offset world position used for shadow lookups.
vec3 evalLighting(vec3 diffuseColor, vec3 F0, float shininess,
                  vec3 N, vec3 V,
                  vec3 worldPos, vec3 shadowPos) {
    // --- Directional (sun) light ---
    vec3 L = normalize(pc.sunDir.xyz);
    float NdotL = clamp(dot(N, L), 0.0, 1.0);
    float sunShadow = calcShadow(shadowPos, NdotL);
    vec3 irradiance = NdotL * pc.sunColor.rgb * sunShadow;

    vec3 sunDiffuse  = irradiance * diffuseColor;
    vec3 sunSpecular = irradiance * brdfSpecular(L, V, N, F0, shininess);

    // --- Shadowed point light ---
    vec3 toLight = pc.pointPos.xyz - worldPos;
    float dist = length(toLight);
    float range = pc.pointPos.w;
    vec3 pointDiffuse = vec3(0.0);
    vec3 pointSpecular = vec3(0.0);
    if (range > 0.0 && dist < range) {
        float ratio = dist / range;
        float atten = (1.0 - ratio * ratio);
        atten = atten * atten;
        vec3 PL = normalize(toLight);
        float NdotPL = clamp(dot(N, PL), 0.0, 1.0);
        float ptShadow = calcPointShadow(worldPos, pc.pointPos.xyz, range);
        vec3 ptIrradiance = NdotPL * atten * pc.pointColor.rgb * ptShadow;

        pointDiffuse  = ptIrradiance * diffuseColor;
        pointSpecular = ptIrradiance * brdfSpecular(PL, V, N, F0, shininess);
    }

    // --- Unshadowed point lights ---
    vec3 uDiffuse = vec3(0.0);
    vec3 uSpecular = vec3(0.0);
    for (int i = 0; i < lb.numLights; i++) {
        vec3 uPos = lb.lights[i].posRange.xyz;
        float uRange = lb.lights[i].posRange.w;
        vec3 uColor = lb.lights[i].color.rgb;
        vec3 uToLight = uPos - worldPos;
        float uDist = length(uToLight);
        if (uRange > 0.0 && uDist < uRange) {
            float uRatio = uDist / uRange;
            float uAtten = (1.0 - uRatio * uRatio);
            uAtten = uAtten * uAtten;
            vec3 UL = normalize(uToLight);
            float uNdotL = clamp(dot(N, UL), 0.0, 1.0);
            vec3 uIrradiance = uNdotL * uAtten * uColor;

            uDiffuse  += uIrradiance * diffuseColor;
            uSpecular += uIrradiance * brdfSpecular(UL, V, N, F0, shininess);
        }
    }

    vec3 ambientTerm = diffuseColor * pc.ambient.rgb;
    return ambientTerm + sunDiffuse + sunSpecular + pointDiffuse + pointSpecular + uDiffuse + uSpecular;
}

// evalLightingDiffuse evaluates all light sources diffuse-only — no specular,
// no Fresnel. Used by grass: on sub-pixel blades the Fresnel term explodes at
// grazing sun angles (dawn/dusk) and turns normal noise into bright speckle.
vec3 evalLightingDiffuse(vec3 diffuseColor, vec3 N, vec3 worldPos, vec3 shadowPos) {
    // --- Directional (sun) light ---
    vec3 L = normalize(pc.sunDir.xyz);
    float NdotL = clamp(dot(N, L), 0.0, 1.0);
    float sunShadow = calcShadow(shadowPos, NdotL);
    vec3 color = NdotL * pc.sunColor.rgb * sunShadow * diffuseColor;

    // --- Shadowed point light ---
    vec3 toLight = pc.pointPos.xyz - worldPos;
    float dist = length(toLight);
    float range = pc.pointPos.w;
    if (range > 0.0 && dist < range) {
        float ratio = dist / range;
        float atten = (1.0 - ratio * ratio);
        atten = atten * atten;
        float NdotPL = clamp(dot(N, normalize(toLight)), 0.0, 1.0);
        float ptShadow = calcPointShadow(worldPos, pc.pointPos.xyz, range);
        color += NdotPL * atten * pc.pointColor.rgb * ptShadow * diffuseColor;
    }

    // --- Unshadowed point lights ---
    for (int i = 0; i < lb.numLights; i++) {
        vec3 uPos = lb.lights[i].posRange.xyz;
        float uRange = lb.lights[i].posRange.w;
        vec3 uToLight = uPos - worldPos;
        float uDist = length(uToLight);
        if (uRange > 0.0 && uDist < uRange) {
            float uRatio = uDist / uRange;
            float uAtten = (1.0 - uRatio * uRatio);
            uAtten = uAtten * uAtten;
            float uNdotL = clamp(dot(N, normalize(uToLight)), 0.0, 1.0);
            color += uNdotL * uAtten * lb.lights[i].color.rgb * diffuseColor;
        }
    }

    return diffuseColor * pc.ambient.rgb + color;
}
