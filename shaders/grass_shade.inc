// Shared conditioning for the flora vertex-colour gradient.
//
// The flora assets carry COLOR_0 as a greyscale base-to-tip gradient -- dark at
// the root, pure 1.0 at the tip -- and glTF requires it be multiplied into base
// colour, which is what the engine does. Neither side is wrong. It aliases
// anyway: a tip that lands on a pixel or two shades roughly four times its
// neighbours, MSAA does not supersample shading, and the wind changes which
// tips win each frame. That is the sparkle.
//
// Flattening the top of the gradient removes it. Measured on 08-grass at six
// times of day under a fixed frame clock, counting isolated pixels above 2.5x
// the local median in the grass band: 5, 6, 2, 0, 2, 2 before and zero at every
// one of them after. The cost is RMS 0.014 at midday and about a third of that
// at dawn and night. 0.90 is the highest ceiling tested that still reaches zero.
//
// It has to run per fragment. Capping the vertex colours instead lowers the
// gradient's peak but leaves it a ramp, and the artifact is local contrast, not
// absolute brightness -- the same 0.90 applied per vertex left 4 of the 5 specks
// standing at night. Clamping the interpolated value flattens the top of the
// gradient into a plateau, which is what removes the contrast.
//
// Included by every stage that shades COLOR_0 -- the blade meshes and the
// impostor bake -- because a cap applied to one and not the other would leave
// the billboards brighter-tipped than the grass they replace.
const float GRASS_SHADE_CEILING = 0.90;

vec3 grassShade(vec3 vertexColor) {
	return min(vertexColor, vec3(GRASS_SHADE_CEILING));
}
