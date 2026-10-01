#!/bin/sh
# make.sh — renders the quil.cc home-page tour into the README GIF.
#
# Needs only Docker. Builds the site, plays the tour in headless Chromium
# under a fake clock (capture.mjs), then encodes with ffmpeg + gifsicle.
# Output: out/<name>.gif and out/<name>-poster.png (<name> from cut.json).
# Fails when the GIF is 5 MB or more: GitHub shows README images from other
# hosts through its Camo proxy, which refuses anything that large.
set -eu
cd "$(dirname "$0")"

# Docker Desktop on Windows wants a Windows path for the bind mount, and Git
# Bash would rewrite "/work" into one unless MSYS_NO_PATHCONV is set.
if pwd -W >/dev/null 2>&1; then
  REPO=$(cd ../.. && pwd -W); export MSYS_NO_PATHCONV=1
else
  REPO=$(cd ../.. && pwd)
fi
PW=$(sed -n 's/.*"playwright": *"\([0-9][0-9.]*\)".*/\1/p' package.json)
[ -n "$PW" ] || { echo "make.sh: package.json must pin an exact playwright version" >&2; exit 1; }

docker run --rm --ipc=host \
  -v "$REPO:/work" \
  -v quil-site-nm:/work/site/node_modules \
  -v quil-gif-nm:/work/tools/readme-gif/node_modules \
  -w /work "mcr.microsoft.com/playwright:v$PW-noble" \
  sh -c '
    set -eu
    apt-get update -qq >/dev/null && apt-get install -y -qq ffmpeg gifsicle >/dev/null
    cd /work/site && npm ci --no-audit --no-fund && npm run build
    cd /work/tools/readme-gif && npm ci --no-audit --no-fund
    rm -rf out && mkdir -p out/frames
    node capture.mjs /work/site/dist out/frames
    get() { node -p "require(\"./cut.json\").$1"; }
    NAME=$(get name); FPS=$(get fps); WIDTH=$(get width); COLORS=$(get colors); LOSSY=$(get lossy); MAX=$(get maxBytes)
    ffmpeg -loglevel error -y -framerate "$FPS" -i out/frames/%05d.png \
      -vf "scale=$WIDTH:-1:flags=lanczos,split[a][b];[a]palettegen=max_colors=$COLORS:stats_mode=diff[p];[b][p]paletteuse=dither=bayer:bayer_scale=4:diff_mode=rectangle" \
      -loop 0 "out/$NAME.raw.gif"
    gifsicle -O3 --lossy="$LOSSY" "out/$NAME.raw.gif" -o "out/$NAME.gif"
    ffmpeg -loglevel error -y -i out/poster.png -vf "scale=$WIDTH:-1:flags=lanczos" "out/$NAME-poster.png"
    SIZE=$(stat -c %s "out/$NAME.gif")
    echo "make.sh: out/$NAME.gif is $SIZE bytes (limit $MAX)"
    [ "$SIZE" -lt "$MAX" ] || { echo "make.sh: too big for GitHub (Camo refuses $MAX bytes or more). Lower fps or colors, or raise lossy, in cut.json." >&2; exit 1; }
  '
