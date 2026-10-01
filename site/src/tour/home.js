// @ts-nocheck — ported from the B2 mock's inline script; plain browser JavaScript on purpose.
/*
 * The home-page tour: stop bar, tour card, card placement, buttons, and the
 * after-card census and copy button. runtime.js owns scroll, hints, keys and
 * the lid and reboot effects; this file only tells the story.
 */
import K from "./kit.js";
import S from "./scenario.js";
import { start } from "./runtime.js";
import { tourWeights } from "../data/usecases";

const $ = (id) => document.getElementById(id);
const CH = S.chapters;
const LAST = CH.length - 1; // the outro chapter
const STOPS = LAST - 1;     // chapters 1 … 6
const narrow = () => innerWidth < 900;

if (location.hash === "#capture") document.documentElement.setAttribute("data-capture", "");

const intro = $("tour-intro"); // server-rendered; it holds the page's only <h1>
const card = $("tour");
intro.classList.remove("static");

// The stops the stop bar and the card count: all six on the site. tools/readme-gif plays only
// some of them and sets window.__quilCaptureStops (their chapter numbers) before this runs, so
// the GIF numbers what it shows (1 / 4 …) instead of skipping (1 / 6, 3 / 6 …).
const SHOWN = Array.isArray(window.__quilCaptureStops)
  ? window.__quilCaptureStops
  : CH.slice(1, LAST).map((_, i) => i + 1);

/* the stop bar, drawn like the TUI's own tab bar */
const stops = $("stops");
stops.innerHTML =
  '<span class="lbl">TOUR</span>' +
  SHOWN.map((n, k) =>
    '<button type="button" data-go="' + n + '"><i class="fill"></i><span>' + (k + 1) + " " + K.esc(CH[n].short) + "</span></button>").join("") +
  '<span class="r"><b>n</b> next · <b>p</b> back · <b>Alt+Shift+P</b> search</span>';
const stopButtons = [...stops.querySelectorAll("button")];

let shown = -1;
function fillCard(ci, api) {
  if (ci === shown) return;
  shown = ci;
  if (ci === 0) return;
  if (ci === LAST) {
    const cs = api.census(LAST + 0.5);
    card.innerHTML =
      '<span class="bt">─ tour done ─</span><h2>One terminal became this.</h2>' +
      "<p>" + cs.projects + " projects · " + cs.tabs + " tabs · " + cs.panes + " panes · " + cs.agents + " agents · " +
      cs.remote + " remote machine · " + cs.sandbox + " sandbox. Click any tab or pane; the keys still work.</p>" +
      '<div class="nav"><button type="button" data-prev>← back</button><button type="button" class="go" data-install>install it <span class="chev">▼</span></button></div>';
    return;
  }
  const c = CH[ci];
  const keys = c.keys.map((k) => "<span><b>" + K.esc(k[0]) + "</b>" + K.esc(k[1]) + "</span>").join("");
  const at = SHOWN.indexOf(ci);
  const nextStop = at >= 0 ? SHOWN[at + 1] : ci < STOPS ? ci + 1 : undefined;
  card.innerHTML =
    '<span class="bt">─ ' + (at >= 0 ? at + 1 : ci) + " / " + (at >= 0 ? SHOWN.length : STOPS) + " ─</span>" +
    '<div class="per">' + K.esc(c.persona) + "</div>" +
    "<h2>" + K.esc(c.title) + "</h2><p>" + K.esc(c.line) + "</p>" +
    (keys ? '<div class="ks">' + keys + "</div>" : "") +
    '<div class="nav"><button type="button" data-prev>← p back</button>' +
    '<a class="doc" href="/docs/#' + c.id + '">how it works →</a>' +
    '<button type="button" class="go" data-next>' + (nextStop ? "n next: " + K.esc(CH[nextStop].short) : "n finish") + " →</button></div>";
}

function place(pos, ci, api) {
  const off = (pos >= 3.40 && pos < 3.70) || (pos >= 6.05 && pos < 6.58); // lid shut; reboot dark
  intro.hidden = ci !== 0 || off;
  card.hidden = ci === 0 || off;
  const el = ci === 0 ? intro : card;
  if (el.hidden || narrow()) return; // under 900 px, tour.css docks the card at the bottom
  const pr = api.pin.getBoundingClientRect();
  if (ci === 0 && pos < 0.34) {
    el.classList.add("center");
    el.style.left = "";
    el.style.top = Math.round(pr.height - el.offsetHeight - 40) + "px";
    return;
  }
  el.classList.toggle("center", ci === LAST);
  if (ci === LAST) { el.style.left = ""; el.style.top = Math.round(pr.height * 0.28) + "px"; return; }
  const ar = api.win.area.getBoundingClientRect();
  el.style.left = Math.round(ar.left - pr.left + 22) + "px";
  el.style.top = Math.round(ar.top - pr.top + 34) + "px";
}

const api = start({
  S,
  cols: 230, maxFs: 13.5,
  weights: tourWeights,
  hintStyle: "coach", keys: true,
  avoid: () => {
    const el = !intro.hidden ? intro : !card.hidden ? card : null;
    if (!el || !el.offsetWidth) return [];
    const pr = $("pin").getBoundingClientRect(), r = el.getBoundingClientRect();
    return [{ x: r.left - pr.left - 8, y: r.top - pr.top - 8, w: r.width + 16, h: r.height + 16 }];
  },
  bootHTML: (pos, BANNER) =>
    '<span class="c-dim">Last login: Fri 16:10 on ttys004</span>\n\n' + BANNER + "\n\n" +
    '<span class="c-white">AI coding went parallel. The terminal still expects one person typing one command.</span>\n\n' +
    '<span class="c-path">~/work/quil</span> <span class="c-prompt">$</span> <span class="c-white">' +
    "quil".slice(0, Math.round(K.seg(pos, 0.18, 0.30) * 4)) + '</span><span class="q-caret"></span>',
  onFrame: (pos, ci, a) => {
    fillCard(ci, a);
    place(pos, ci, a);
    stopButtons.forEach((b) => {
      const n = +b.dataset.go;
      const f = K.clamp(pos - n);
      b.classList.toggle("done", f >= 1);
      b.classList.toggle("on", n === ci);
      b.firstChild.style.width = (f >= 1 ? 0 : f * 100) + "%";
    });
  },
});

document.addEventListener("click", (e) => {
  const t = e.target;
  if (!(t instanceof Element)) return;
  const g = t.closest("[data-go]");
  if (g) { api.goTo(+g.dataset.go + 0.03); return; }
  if (t.closest("[data-next]")) api.next();
  else if (t.closest("[data-prev]")) api.prev();
  else if (t.closest("[data-install]")) $("after").scrollIntoView({ behavior: K.reduced ? "auto" : "smooth" });
});

/* the after card: what the session became, and the one-liner */
const cs = api.census(LAST + 0.5);
$("census").innerHTML =
  ["projects", "tabs", "panes", "agents"].map((k) => "<span><b>" + cs[k] + "</b> " + k + "</span>").join("") +
  "<span><b>" + cs.remote + "</b> remote machine</span><span><b>" + cs.sandbox + "</b> sandbox</span>";
const copy = $("copybtn");
copy.addEventListener("click", () => {
  const select = () => getSelection().selectAllChildren($("cmd"));
  if (!navigator.clipboard) { select(); return; }
  navigator.clipboard.writeText($("cmd").textContent).then(() => {
    copy.textContent = "copied";
    setTimeout(() => (copy.textContent = "copy"), 1400);
  }, select);
});
