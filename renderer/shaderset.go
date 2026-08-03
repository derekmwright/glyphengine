package renderer

import "github.com/derekmwright/glyphengine/shaders"

// ShaderSet holds the SPIR-V for every pipeline the renderer builds.
//
// It exists so a game can replace engine shaders without forking the engine.
// Pipeline creation has always taken raw []byte, so the seam was already
// there; this makes it reachable. Supply your own module, swap one stage, or
// take the defaults.
//
// Any field left nil falls back to the engine's embedded shader for that
// stage, so overriding one pipeline does not mean supplying twenty-four
// blobs:
//
//	custom := renderer.DefaultShaders()
//	custom.LitFrag = myLitFragSpv
//	r, err := renderer.New(win, renderer.WithShaders(custom))
//
// A replacement must match the vertex input layout, descriptor set layout, and
// push-constant ranges the engine's pipelines declare. A mismatch is a
// pipeline-creation failure at startup, or — worse — a shader that links and
// draws nothing. Run with WithValidation while developing one.
type ShaderSet struct {
	TriangleVert, TriangleFrag []byte // diagnostic tri-color triangle
	MeshVert, MeshFrag         []byte // unlit textured mesh
	LitVert, LitFrag           []byte // lit static geometry
	LitMaterialFrag            []byte // lit + normal/roughness/AO maps (shares LitVert)
	TerrainFrag                []byte // terrain splat blend (shares LitVert)
	SkinnedLitVert             []byte // GPU-skinned lit geometry
	SkinnedLitFrag             []byte
	ShadowVert, ShadowFrag     []byte // depth-only shadow pass
	ShadowSkinnedVert          []byte // depth-only, skinned
	SkyVert, SkyFrag           []byte // sky gradient
	StarsVert, StarsFrag       []byte // star field
	GrassVert, GrassFrag       []byte // instanced grass
	WaterVert, WaterFrag       []byte // animated water surface
	GodRayFrag                 []byte // screen-space light shafts (uses SkyVert)
	TonemapFrag                []byte // HDR resolve to the swapchain (uses SkyVert)
	ParticleVert, ParticleFrag []byte // billboard particles
	MsdfVert, MsdfFrag         []byte // MSDF text
	UIVert, UIFrag             []byte // 9-slice UI panels
}

// DefaultShaders returns the shader set the engine embeds. Copy it, override
// the stages you care about, and pass the result to WithShaders.
func DefaultShaders() ShaderSet {
	return ShaderSet{
		TriangleVert:      shaders.TriangleVertSpv,
		TriangleFrag:      shaders.TriangleFragSpv,
		MeshVert:          shaders.MeshVertSpv,
		MeshFrag:          shaders.MeshFragSpv,
		LitVert:           shaders.LitVertSpv,
		LitFrag:           shaders.LitFragSpv,
		LitMaterialFrag:   shaders.LitMaterialFragSpv,
		TerrainFrag:       shaders.TerrainFragSpv,
		SkinnedLitVert:    shaders.SkinnedLitVertSpv,
		SkinnedLitFrag:    shaders.SkinnedLitFragSpv,
		ShadowVert:        shaders.ShadowVertSpv,
		ShadowFrag:        shaders.ShadowFragSpv,
		ShadowSkinnedVert: shaders.ShadowSkinnedVertSpv,
		SkyVert:           shaders.SkyVertSpv,
		SkyFrag:           shaders.SkyFragSpv,
		StarsVert:         shaders.StarsVertSpv,
		StarsFrag:         shaders.StarsFragSpv,
		GrassVert:         shaders.GrassVertSpv,
		GrassFrag:         shaders.GrassFragSpv,
		WaterVert:         shaders.WaterVertSpv,
		WaterFrag:         shaders.WaterFragSpv,
		GodRayFrag:        shaders.GodRayFragSpv,
		TonemapFrag:       shaders.TonemapFragSpv,
		ParticleVert:      shaders.ParticleVertSpv,
		ParticleFrag:      shaders.ParticleFragSpv,
		MsdfVert:          shaders.MsdfVertSpv,
		MsdfFrag:          shaders.MsdfFragSpv,
		UIVert:            shaders.UIVertSpv,
		UIFrag:            shaders.UIFragSpv,
	}
}

// withDefaults returns s with every nil stage filled in from the embedded
// defaults, so callers can override one shader without supplying the rest.
func (s ShaderSet) withDefaults() ShaderSet {
	d := DefaultShaders()
	pairs := []struct {
		dst *[]byte
		src []byte
	}{
		{&s.TriangleVert, d.TriangleVert}, {&s.TriangleFrag, d.TriangleFrag},
		{&s.MeshVert, d.MeshVert}, {&s.MeshFrag, d.MeshFrag},
		{&s.LitVert, d.LitVert}, {&s.LitFrag, d.LitFrag},
		{&s.LitMaterialFrag, d.LitMaterialFrag},
		{&s.TerrainFrag, d.TerrainFrag},
		{&s.SkinnedLitVert, d.SkinnedLitVert}, {&s.SkinnedLitFrag, d.SkinnedLitFrag},
		{&s.ShadowVert, d.ShadowVert}, {&s.ShadowFrag, d.ShadowFrag},
		{&s.ShadowSkinnedVert, d.ShadowSkinnedVert},
		{&s.SkyVert, d.SkyVert}, {&s.SkyFrag, d.SkyFrag},
		{&s.StarsVert, d.StarsVert}, {&s.StarsFrag, d.StarsFrag},
		{&s.GrassVert, d.GrassVert}, {&s.GrassFrag, d.GrassFrag},
		{&s.WaterVert, d.WaterVert}, {&s.WaterFrag, d.WaterFrag},
		{&s.GodRayFrag, d.GodRayFrag},
		{&s.TonemapFrag, d.TonemapFrag},
		{&s.ParticleVert, d.ParticleVert}, {&s.ParticleFrag, d.ParticleFrag},
		{&s.MsdfVert, d.MsdfVert}, {&s.MsdfFrag, d.MsdfFrag},
		{&s.UIVert, d.UIVert}, {&s.UIFrag, d.UIFrag},
	}
	for _, p := range pairs {
		if *p.dst == nil {
			*p.dst = p.src
		}
	}
	return s
}
