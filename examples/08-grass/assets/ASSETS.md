# Example assets

Every binary file here is listed with its source and license. Anything not
listed does not belong in the repository.

| File | Source | License |
|---|---|---|
| `flora/Grass_Common_Short.gltf` + `.bin` | [Quaternius](https://quaternius.com) — Ultimate Nature Pack | CC0 1.0 (public domain) |
| `flora/Grass_Common_Tall.gltf` + `.bin` | Quaternius — Ultimate Nature Pack | CC0 1.0 |
| `flora/Grass_Wispy_Short.gltf` + `.bin` | Quaternius — Ultimate Nature Pack | CC0 1.0 |
| `flora/Grass_Wispy_Tall.gltf` + `.bin` | Quaternius — Ultimate Nature Pack | CC0 1.0 |
| `flora/Grass.png` | Quaternius — shared palette atlas for the above | CC0 1.0 |

CC0 imposes no attribution requirement, which is why these are usable in an
MIT-licensed repository without adding an obligation downstream consumers would
inherit. Crediting Quaternius is nonetheless the decent thing to do, and this
file is that credit.

## Why only grass

The same pack has clover and flower models, and they were deliberately left
out: they reference a 2048x2048 leaf atlas and a second flower atlas, which
together are 2.9 MB — twenty times the size of everything here — for garnish in
an example about instanced grass. The whole directory is 140 KB.

Keeping example assets small is not fussiness. There is no Git LFS in this
repository, because the Go module proxy does not run LFS smudge and consumers
would receive pointer stubs where `go:embed` expects real content. Every byte
here ships to everyone who clones.
