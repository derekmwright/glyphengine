#!/bin/sh
# Reproduce issue #40: "first run of a session renders differently".
#
# One cycle is: force a genuine rebuild of the renderer package (a content
# change, because Go's build cache is keyed on content and `touch` does not
# invalidate it), then N identical captures back to back. The first capture is
# therefore the first run of a freshly built binary and the rest are "later
# runs" in the issue's sense. Every capture keeps its state trace beside it.
#
#   tools/firstrun-repro.sh <cycles> <label> <example> [args...]
#
# Env knobs (all default off):
#   REPRO_NOBUILD=1    skip the forced rebuild -- separates "fresh binary"
#                      from "first run" by making every cycle a warm binary
#   REPRO_VALIDATION=1 run under the validation layer
#   REPRO_FOREGROUND=1 show the window instead of GLYPHENGINE_BACKGROUND=1
#   REPRO_FRAMES=n     frame count (default 90)
#   REPRO_RUNS=n       captures per cycle (default 2)
#   REPRO_CLEARCACHE=1 delete the driver's on-disk shader cache before a cycle
#   REPRO_PRE=cmd      run this between the build and the captures
set -u

root=$(cd "$(dirname "$0")/.." && pwd)
cycles=$1; label=$2; shift 2
ex=$1; shift
frames=${REPRO_FRAMES:-90}
runs=${REPRO_RUNS:-2}
out=$root/.task/firstrun/$label
rm -rf "$out"; mkdir -p "$out"

export GLYPHENGINE_FIXED_FRAME_TIME=16.667ms
[ "${REPRO_FOREGROUND:-0}" = 1 ] || export GLYPHENGINE_BACKGROUND=1
[ "${REPRO_VALIDATION:-0}" = 1 ] && export GLYPHENGINE_VALIDATION=1

stamp=$root/renderer/grass.go
differ=0; cycled=0

for c in $(seq 1 "$cycles"); do
  if [ "${REPRO_NOBUILD:-0}" != 1 ]; then
    # A unique trailing comment: new package content, so the whole dependency
    # cone above renderer is recompiled and relinked the way a real edit does.
    #
    # The original line count is remembered so the file comes back exactly.
    # `sed -e '$d' -e '$d'` deletes the LAST line twice over rather than two
    # lines, which left one blank line behind on every cycle of the first run
    # of this script.
    keep=$(wc -l < "$stamp")
    printf '\n// repro cycle %s-%s\n' "$label" "$c" >> "$stamp"
    (cd "$root/examples" && go build -o "$out/pre.exe" "./$ex") >/dev/null 2>&1
    rm -f "$out/pre.exe"
  fi
  if [ "${REPRO_CLEARCACHE:-0}" = 1 ]; then
    rm -rf "$LOCALAPPDATA/AMD/DxCache" "$LOCALAPPDATA/AMD/GLCache" "$LOCALAPPDATA/AMD/VkCache" 2>/dev/null
  fi
  [ -z "${REPRO_PRE:-}" ] || (cd "$root" && eval "$REPRO_PRE") >"$out/c$c-pre.log" 2>&1
  r=1
  while [ "$r" -le "$runs" ]; do
    (cd "$root/examples" && GLYPHENGINE_STATE_TRACE="$out/c$c-$r.trace" \
      go run "./$ex" -frames "$frames" "$@" -screenshot "$out/c$c-$r.png") >"$out/c$c-$r.log" 2>&1
    r=$((r+1))
  done
  if [ "${REPRO_NOBUILD:-0}" != 1 ]; then
    head -n "$keep" "$stamp" > "$stamp.restore" && mv "$stamp.restore" "$stamp"
  fi

  verdict=same
  r=2
  while [ "$r" -le "$runs" ]; do
    if [ ! -s "$out/c$c-1.png" ] || [ ! -s "$out/c$c-$r.png" ]; then
      verdict="MISSING CAPTURE"
    elif ! (cd "$root" && go run ./cmd/pngsame "$out/c$c-1.png" "$out/c$c-$r.png") >"$out/c$c-cmp$r.txt" 2>&1; then
      verdict="DIFFER"
    fi
    r=$((r+1))
  done
  cycled=$((cycled+1))
  [ "$verdict" = same ] || differ=$((differ+1))
  echo "cycle $c: $verdict $(tail -1 "$out/c$c-cmp2.txt" 2>/dev/null | sed 's|.*png and ||')"
done
echo "RATE $label: $differ/$cycled cycles differed"
