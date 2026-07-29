package shaders

import _ "embed"

//go:embed triangle.vert.spv
var TriangleVertSpv []byte

//go:embed triangle.frag.spv
var TriangleFragSpv []byte

//go:embed mesh.vert.spv
var MeshVertSpv []byte

//go:embed mesh.frag.spv
var MeshFragSpv []byte

//go:embed lit.vert.spv
var LitVertSpv []byte

//go:embed lit.frag.spv
var LitFragSpv []byte

//go:embed lit_material.frag.spv
var LitMaterialFragSpv []byte

//go:embed terrain.frag.spv
var TerrainFragSpv []byte

//go:embed stars.vert.spv
var StarsVertSpv []byte

//go:embed stars.frag.spv
var StarsFragSpv []byte

//go:embed sky.vert.spv
var SkyVertSpv []byte

//go:embed sky.frag.spv
var SkyFragSpv []byte

//go:embed msdf.vert.spv
var MsdfVertSpv []byte

//go:embed msdf.frag.spv
var MsdfFragSpv []byte

//go:embed ui.vert.spv
var UIVertSpv []byte

//go:embed ui.frag.spv
var UIFragSpv []byte

//go:embed skinned_lit.vert.spv
var SkinnedLitVertSpv []byte

//go:embed skinned_lit.frag.spv
var SkinnedLitFragSpv []byte

//go:embed grass.vert.spv
var GrassVertSpv []byte

//go:embed grass.frag.spv
var GrassFragSpv []byte

//go:embed particle.vert.spv
var ParticleVertSpv []byte

//go:embed particle.frag.spv
var ParticleFragSpv []byte

//go:embed shadow.vert.spv
var ShadowVertSpv []byte

//go:embed shadow_skinned.vert.spv
var ShadowSkinnedVertSpv []byte

//go:embed shadow.frag.spv
var ShadowFragSpv []byte

//go:embed water.vert.spv
var WaterVertSpv []byte

//go:embed water.frag.spv
var WaterFragSpv []byte

//go:embed godray.frag.spv
var GodRayFragSpv []byte
