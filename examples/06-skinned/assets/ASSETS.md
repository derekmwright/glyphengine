# Example assets

Every binary file here is listed with its source and license. Anything not
listed does not belong in the repository.

| File | Source | License |
|---|---|---|
| `character.glb` | [Kenney](https://kenney.nl) — Mini Characters pack | CC0 1.0 (public domain) |
| `Textures/colormap.png` | [Kenney](https://kenney.nl) — shared palette atlas for the above | CC0 1.0 (public domain) |

`character.glb` is a rigged, animated character: 7 joints, 32 animation clips,
and a single shared `colormap` palette texture. Note the texture is *external*
rather than embedded — the model references `Textures/colormap.png` by URI
relative to itself, so the directory layout here is load-bearing. Together they
are 0.23 MB, which is small
enough to live in an examples directory without Git LFS — the Go module proxy
does not run LFS smudge, so a pointer stub is what consumers would receive.

CC0 imposes no attribution requirement, which is why it is usable in an
MIT-licensed repository without adding an obligation downstream consumers would
inherit. Crediting Kenney is nonetheless the decent thing to do, and this file
is that credit.

Assets whose provenance cannot be established do not go in this repository,
regardless of how likely they are to be fine. Re-establishing a license after
the fact is far harder than recording it once.
