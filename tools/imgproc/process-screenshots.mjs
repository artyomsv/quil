// Quil screenshot pipeline — crop OS/title-bar chrome, trim dead black
// space, and export CDN-ready variants in multiple sizes/formats.
//
//   node process-screenshots.mjs            # process everything
//   node process-screenshots.mjs --probe    # just report detected crops
//   node process-screenshots.mjs foo.png    # process a single source
//
// Inputs + crop intent live in manifest.json. Export sizes/formats are
// the PROFILES below. Outputs go to out/screenshots/ (gitignored) ready
// to sync to a DigitalOcean Space (see README.md).
//
// Why Node/sharp and not Python: Python isn't installed on this machine;
// sharp ships with the site toolchain and is the right tool for batch
// resize/format. Port to Pillow later if desired — the manifest + PROFILES
// shape carries over 1:1.

import sharp from "sharp";
import { readFile, writeFile, mkdir, rm } from "node:fs/promises";
import { existsSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));

// --- Export profiles -------------------------------------------------
//
// width  → longest-edge resize, aspect preserved, never upscaled.
// cover  → hard crop to exactly [w,h] (used for the social/OG card).
// Text-heavy screenshots: WebP for the web (small + crisp), PNG where
// universal support matters (README, OG cards read by social scrapers).
const PROFILES = {
  full: { width: 2048, variants: [{ ext: "webp", opts: { quality: 84 } }] },
  hero: {
    width: 1280,
    variants: [
      { ext: "webp", opts: { quality: 84 } },
      { ext: "png", opts: { compressionLevel: 9 } },
    ],
  },
  card: { width: 800, variants: [{ ext: "webp", opts: { quality: 82 } }] },
  og: {
    cover: [1200, 630],
    variants: [{ ext: "png", opts: { compressionLevel: 9 } }],
  },
};

// Output filename: width profiles use the pixel width, og uses "-og".
function outName(stem, profileKey, profile, ext) {
  if (profile.cover) return `${stem}-og.${ext}`;
  return `${stem}-${profile.width}.${ext}`;
}

// --- Chrome detection ------------------------------------------------
//
// The Windows Terminal title bar is a light-grey band at the very top;
// the drag handle is a thin light band at the very bottom. The Quil app
// background is near-black. Scan row brightness to find where the chrome
// ends and the app begins, so we strip the OS chrome but keep the app's
// own tab bar / status bar.
async function detectChrome(srcPath) {
  const { data, info } = await sharp(srcPath)
    .greyscale()
    .raw()
    .toBuffer({ resolveWithObject: true });
  const W = info.width;
  const H = info.height;
  const rowMean = (y) => {
    let s = 0;
    for (let x = 0; x < W; x++) s += data[y * W + x];
    return s / W;
  };
  const APP_MAX = 24; // app background rows sit well below this
  const SCAN = Math.min(140, Math.floor(H / 4));

  let top = 0;
  if (rowMean(0) > APP_MAX) {
    for (let y = 1; y < SCAN; y++) {
      if (rowMean(y) < APP_MAX) {
        top = y;
        break;
      }
    }
  }

  let bottom = 0;
  if (rowMean(H - 1) > APP_MAX) {
    for (let y = H - 2; y > H - SCAN; y--) {
      if (rowMean(y) < APP_MAX) {
        bottom = H - 1 - y;
        break;
      }
    }
  }
  // If the title bar was present, the bottom drag handle almost always is
  // too but can be too thin for the brightness scan to catch — strip a
  // small fixed band so `trim` isn't blocked by it.
  if (top > 0 && bottom === 0) bottom = Math.round(H * 0.012);

  return { width: W, height: H, top, bottom };
}

// --- Per-image cropping ----------------------------------------------
//
// Returns a sharp instance of the cleaned master (chrome removed, black
// trimmed, dialogs tight-cropped + padded).
async function cropMaster(srcPath, item, cfg) {
  const { width, height, top } = await detectChrome(srcPath);
  let { bottom } = await detectChrome(srcPath);

  // Dialog shots have only black + the OS drag handle below the box. The
  // thin handle is too faint for the brightness scan and would block the
  // bottom trim, so strip a generous band — the dialog is centred well
  // above it and `trim` re-crops to the box anyway.
  if (item.type === "dialog") {
    bottom = Math.max(bottom, Math.round(height * 0.03));
  }

  let img = sharp(srcPath);
  const cropH = height - top - bottom;
  if (top > 0 || bottom > 0) {
    img = img.extract({ left: 0, top, width, height: cropH });
  }

  // Trim near-background borders. Content shots that are edge-to-edge
  // have no black margin, so this is a no-op for them; the MCP/dialog
  // shots get their dead space removed.
  let buf = await img.toBuffer();
  try {
    buf = await sharp(buf)
      .trim({ background: cfg.bg, threshold: cfg.trimThreshold })
      .toBuffer();
  } catch {
    // sharp throws if there's nothing to trim — keep the untrimmed buffer.
  }

  if (item.type === "dialog" && cfg.dialogPadding > 0) {
    const p = cfg.dialogPadding;
    buf = await sharp(buf)
      .extend({
        top: p,
        bottom: p,
        left: p,
        right: p,
        background: { ...cfg.bg, alpha: 1 },
      })
      .toBuffer();
  }

  return sharp(buf);
}

async function renderVariants(master, stem, profileKeys, outDir) {
  const written = [];
  for (const key of profileKeys) {
    const profile = PROFILES[key];
    if (!profile) {
      console.warn(`  ! unknown profile "${key}" — skipping`);
      continue;
    }
    for (const v of profile.variants) {
      let pipe = master.clone();
      if (profile.cover) {
        pipe = pipe.resize(profile.cover[0], profile.cover[1], {
          fit: "cover",
          position: "centre",
        });
      } else {
        pipe = pipe.resize({ width: profile.width, withoutEnlargement: true });
      }
      const fmt = v.ext === "jpg" ? "jpeg" : v.ext;
      pipe = pipe.toFormat(fmt, v.opts);
      const file = outName(stem, key, profile, v.ext);
      const dest = path.join(outDir, file);
      const info = await pipe.toFile(dest);
      written.push({ file, w: info.width, h: info.height, bytes: info.size });
    }
  }
  return written;
}

async function main() {
  const args = process.argv.slice(2);
  const probe = args.includes("--probe");
  const only = args.find((a) => !a.startsWith("--"));

  const cfg = JSON.parse(
    await readFile(path.join(__dirname, "manifest.json"), "utf8"),
  );
  const srcDir = path.resolve(__dirname, cfg.srcDir);
  const outDir = path.resolve(__dirname, cfg.outDir);

  let images = cfg.images;
  if (only) images = images.filter((i) => i.src === only);
  if (images.length === 0) {
    console.error(only ? `No manifest entry for "${only}"` : "No images.");
    process.exit(1);
  }

  if (!probe) {
    if (existsSync(outDir)) await rm(outDir, { recursive: true, force: true });
    await mkdir(outDir, { recursive: true });
  }

  let totalBytes = 0;
  let count = 0;
  const urlGroups = [];
  for (const item of images) {
    const srcPath = path.join(srcDir, item.src);
    if (!existsSync(srcPath)) {
      console.warn(`! missing source: ${item.src}`);
      continue;
    }
    const stem = path.basename(item.src, path.extname(item.src));
    const c = await detectChrome(srcPath);
    console.log(
      `\n${item.src}  (${c.width}x${c.height})  type=${item.type}  strip top=${c.top}px bottom=${c.bottom}px`,
    );
    if (probe) continue;

    const master = await cropMaster(srcPath, item, cfg);
    const m = await master.metadata();
    console.log(`  cleaned master: ${m.width}x${m.height}`);
    const written = await renderVariants(master, stem, item.profiles, outDir);
    urlGroups.push({ stem, files: written.map((w) => w.file) });
    for (const w of written) {
      totalBytes += w.bytes;
      count++;
      console.log(
        `  → ${w.file.padEnd(40)} ${w.w}x${w.h}  ${(w.bytes / 1024).toFixed(0)} KB`,
      );
    }
  }

  if (!probe) {
    // Emit a copy-paste CDN URL list for every variant produced.
    if (cfg.cdnBase) {
      const base = cfg.cdnBase.replace(/\/$/, "");
      const lines = [`# CDN URLs`, ``, `Base: ${base}`, ``];
      for (const g of urlGroups) {
        lines.push(`## ${g.stem}`);
        for (const f of g.files) lines.push(`- ${base}/${f}`);
        lines.push(``);
      }
      const urlsPath = path.join(outDir, "urls.md");
      await writeFile(urlsPath, lines.join("\n"), "utf8");
      console.log(`\nURL list → ${path.relative(process.cwd(), urlsPath)}`);
    }
    console.log(
      `\nDone: ${count} files, ${(totalBytes / 1024 / 1024).toFixed(2)} MB total → ${path.relative(process.cwd(), outDir)}`,
    );
  }
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
