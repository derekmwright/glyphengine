# blender_terrain.heightmap

`cmd/heightmapconv`'s output, converted from `cmd/heightmapconv/testdata/blender_terrain.glb`
(a real Blender 5.0.1 export -- see that directory's own README for how it
was built and the hand-computed coordinates it is checked against):

```
go run ./cmd/heightmapconv -mesh cmd/heightmapconv/testdata/blender_terrain.glb -grid 65x65 -out examples/07-terrain/assets/blender_terrain.heightmap
```

65x65 grid, 16,924 bytes -- small enough to commit alongside the example
(the issue's own budget was "a 129x129 float grid is ~66 KB -- acceptable";
this is a quarter of that resolution and about a quarter of the size).

`07-terrain -heightmap assets/blender_terrain.heightmap` loads it instead of
generating a heightmap procedurally -- see the example's own top-of-file
doc comment and `docs/agents/terrain-heightmap.md`'s "Through the example"
section for a description of the render and how honestly the peak and ridge
came through at this fixture's small (10x10 unit) scale.
