#version 450
#extension GL_GOOGLE_include_directive : require

// The water surface's fragment stage with whitecap foam: water_shading.inc with
// WATER_FOAM defined, which adds the persistent foam target and its grid at
// set 2 and the foam layer between the subsurface term and the glints. The
// foam pipeline binds it only for a surface that has a foam target; every
// other surface draws water.frag and pays nothing for the option.

#define WATER_FOAM
#include "water_shading.inc"
