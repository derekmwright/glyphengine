#version 450
#extension GL_GOOGLE_include_directive : require

// The water surface's fragment stage. The body is in water_shading.inc, which
// water_foam.frag compiles a second time with the foam layer; see the note
// there for why it is one body.

#include "water_shading.inc"
