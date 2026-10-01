# README GIF (`tools/readme-gif`)

Renders the quil.cc home-page tour into the GIF at the top of the repository README.

    sh tools/readme-gif/make.sh        # needs only Docker; Git Bash works on Windows

What it does: builds `site/`, opens it in headless Chromium at 1280×720 under Playwright's fake
clock (`#capture` hides everything but the tour), steps through the segments in `cut.json`, and
encodes a looping GIF with ffmpeg and gifsicle. Everything that moves is driven by the fake clock,
so the same inputs give the same picture; a few anti-aliased pixels on rounded corners can still
differ between runs (Chromium raster noise), so do not expect byte-identical files.
The site's fonts come from Google Fonts, so the render needs network access;
`capture.mjs` stops before the first frame when they did not load, because a GIF
in fallback fonts is not the site.

`stops` in `cut.json` lists the tour chapters the cut plays (1 agents … 6 reboot). The page then
numbers its stop bar and cards from that list, so a four-stop cut reads 1 / 4 … 4 / 4. Keep it in
step with `segments`.

Limits: GitHub shows images from other hosts through its Camo proxy, which refuses files of
5 MB or more, so `make.sh` fails at `maxBytes`. Camo also caches by URL: a new cut gets a new
`name` (`quil-tour-v2`), never the same file name.

Upload `out/<name>.gif` to `https://cdn.stukans.com/quil/media/` (the same Space as
`tools/imgproc`, under `media/` rather than `screenshots/`), then point the README at it.
