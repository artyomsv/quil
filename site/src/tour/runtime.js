// @ts-nocheck — ported from the B2 mock (kit/live.js); plain browser JavaScript on purpose.
/* ==========================================================================
   live.js — runtime for the "page is a Quil session" designs.

   QuilLive.start(cfg) mounts the TUI replica, maps scroll to story position,
   draws hints next to the thing they explain, shows the keys being pressed,
   plays the lid and reboot effects, and makes the real keys work
   (Alt+Shift+P, Alt+Shift+A, Alt+N, n / p). Each page adds its own way of
   telling the story through cfg.onFrame.
   ========================================================================== */
import K from "./kit.js";

  /* block letters for the login banner (ANSI Shadow) */
  const FIG = {
    A: [" █████╗ ", "██╔══██╗", "███████║", "██╔══██║", "██║  ██║", "╚═╝  ╚═╝"],
    E: ["███████╗", "██╔════╝", "█████╗  ", "██╔══╝  ", "███████╗", "╚══════╝"],
    H: ["██╗  ██╗", "██║  ██║", "███████║", "██╔══██║", "██║  ██║", "╚═╝  ╚═╝"],
    I: ["██╗", "██║", "██║", "██║", "██║", "╚═╝"],
    L: ["██╗     ", "██║     ", "██║     ", "██║     ", "███████╗", "╚══════╝"],
    M: ["███╗   ███╗", "████╗ ████║", "██╔████╔██║", "██║╚██╔╝██║", "██║ ╚═╝ ██║", "╚═╝     ╚═╝"],
    N: ["███╗   ██╗", "████╗  ██║", "██╔██╗ ██║", "██║╚██╗██║", "██║ ╚████║", "╚═╝  ╚═══╝"],
    O: [" ██████╗ ", "██╔═══██╗", "██║   ██║", "██║   ██║", "╚██████╔╝", " ╚═════╝ "],
    R: ["██████╗ ", "██╔══██╗", "██████╔╝", "██╔══██╗", "██║  ██║", "╚═╝  ╚═╝"],
    S: ["███████╗", "██╔════╝", "███████╗", "╚════██║", "███████║", "╚══════╝"],
    T: ["████████╗", "╚══██╔══╝", "   ██║   ", "   ██║   ", "   ██║   ", "   ╚═╝   "],
    W: ["██╗    ██╗", "██║    ██║", "██║ █╗ ██║", "██║███╗██║", "╚███╔███╔╝", " ╚══╝╚══╝ "],
    ".": ["   ", "   ", "   ", "   ", "██╗", "╚═╝"],
    " ": ["  ", "  ", "  ", "  ", "  ", "  "],
  };
  const fig = (s) => { const rows = ["", "", "", "", "", ""]; for (const ch of s) FIG[ch].forEach((r, i) => (rows[i] += r)); return rows.join("\n"); };
  const BANNER = '<span class="fig">' + fig("IT STARTS WITH") + "\n" + fig("ONE TERMINAL.") + "</span>";

  const h = (tag, cls, html) => { const e = document.createElement(tag); if (cls) e.className = cls; if (html != null) e.innerHTML = html; return e; };
  const NS = "http://www.w3.org/2000/svg";
  const sv = (tag, attrs) => { const e = document.createElementNS(NS, tag); for (const k in attrs) e.setAttribute(k, attrs[k]); return e; };

  function start(cfg) {
    const S = cfg.S;
    const $ = (id) => document.getElementById(id);
    const pin = $("pin"), live = $("live"), marks = $("marks");
    const CH = S.chapters;
    const NCH = CH.length;

    /* ---------------------------------------------------- boot screen */
    S.overlays.unshift({ id: "boot", from: -2, to: 0.34, layer: "win", cls: "q-raw boot", enter: false,
      html: (t, pos) => cfg.bootHTML(pos, BANNER) });

    const W = K.world(S);
    const win = W.mountWindow($("tui"), {
      cols: cfg.cols || 220, maxFs: cfg.maxFs || 13.5, variant: "b", notesWidth: cfg.notesWidth || "34%",
      notes: cfg.notes ? (pos, act) => cfg.notes(pos, act, api) : null,
    });
    if (cfg.notesHTML) win._notesHTML = (nt) => cfg.notesHTML(nt, api);

    /* ---------------------------------------------------- layers */
    const hintsEl = h("div", "hints " + (cfg.hintStyle || "coach"));
    const svg = sv("svg", { "aria-hidden": "true" });
    hintsEl.append(svg);
    const keysEl = h("div", "keys");
    if (cfg.keysAt) Object.assign(keysEl.style, cfg.keysAt);
    keysEl.hidden = !cfg.keys;
    const lid = h("div", "lid");
    const inset = h("div", "inset", '<div class="ih"><span><b>gpu01</b> · quild · ~/work/train</span><span class="ok" id="inset-st">still running</span></div><div class="ib"><div class="q"></div></div>');
    const insetQ = inset.querySelector(".ib > .q");
    const dark = h("div", "dark", '<span class="a" id="dark-a">POWERED OFF</span><span class="b" id="dark-b"></span>');
    const kb = h("div", "kb");
    const flashEl = h("div", "flash");
    pin.append(hintsEl, keysEl, lid, inset, dark, kb, flashEl);

    /* ---------------------------------------------------- scroll → pos */
    const weights = cfg.weights || CH.map(() => 1.5);
    // One scroll mark per chapter (id tour-<id>). TourStage.astro server-renders them so the page
    // has its full length before any script runs; create them only when it did not. Their
    // heights are CSS vh, never px from innerHeight: a phone's 100vh stays put while its URL bar
    // shows and hides, innerHeight does not, and re-sizing the marks on that resize made the
    // tour ~13 toolbar-heights longer or shorter mid-scroll, so the story jumped.
    const vhOf = (i) => Math.round(weights[i] * 100);
    if (!marks.children.length) CH.forEach((c, i) => {
      const d = h("div");
      d.id = "tour-" + c.id;
      d.style.height = i === CH.length - 1 ? "calc(" + vhOf(i) + "vh + 100vh - var(--hh))" : vhOf(i) + "vh";
      marks.append(d);
    });
    let starts = [], HH = 34;
    function layoutMarks() {
      const hdr = document.querySelector(".hdr");
      HH = hdr ? hdr.offsetHeight : 0;
      const els = marks.children, top = els[0].offsetTop;
      starts = [];
      for (let i = 0; i < els.length; i++) starts.push(els[i].offsetTop - top);
      // The last mark also carries the pinned screen's height; its chapter is only its vh share.
      const vhPx = els[0].offsetHeight / vhOf(0);
      starts.push(starts[els.length - 1] + vhOf(els.length - 1) * vhPx);
    }
    layoutMarks();
    const top0 = () => live.offsetTop - HH;
    function posFromScroll() {
      const y = scrollY - top0();
      if (y <= 0) return 0;
      for (let i = 0; i < NCH; i++) if (y < starts[i + 1]) return i + (y - starts[i]) / (starts[i + 1] - starts[i]);
      return NCH - 0.001;
    }
    const scrollFor = (p) => { const i = Math.min(NCH - 1, Math.floor(p)), f = p - i; return top0() + starts[i] + f * (starts[i + 1] - starts[i]); };
    function goTo(p, instant) { scrollTo({ top: scrollFor(p) + 1, behavior: instant || K.reduced ? "auto" : "smooth" }); }
    // Capture API for tools/readme-gif: jump to story position p and draw it now.
    window.__quilTour = { seek: (p) => { scrollTo({ top: scrollFor(p), behavior: "auto" }); frame(true); } };

    /* ---------------------------------------------------- live overrides */
    let over = null, overCh = -1, curPos = 0, lastPos = -1;
    function setOver(tab, pane) { over = { tab, pane }; overCh = Math.floor(curPos); frame(true); }

    /* ---------------------------------------------------- hints */
    const hintEls = new Map();
    let placed = [];
    const rel = (r, pr) => ({ x: r.left - pr.left, y: r.top - pr.top, w: r.width, h: r.height });
    function visible(el) { return el && el.offsetParent !== null && el.getBoundingClientRect().width > 0; }
    function resolve(at) {
      if (at === "screen") return { kind: "screen" };
      if (at === "palette") { const k = Object.keys(win.overlayEls).find((x) => x.startsWith("pal")); return k ? { kind: "palette", el: win.overlayEls[k] } : null; }
      if (at === "notes") return visible(win.notes) ? { kind: "notes", el: win.notes } : null;
      if (at.pane) { const p = W.panes[at.pane]; return p && visible(p.el) ? { kind: "pane", el: p.el } : null; }
      if (at.label) { const p = W.panes[at.label]; return p && visible(p.tNm) ? { kind: "label", el: p.tNm } : null; }
      if (at.foot) { const p = W.panes[at.foot]; return p && visible(p.foot) ? { kind: "foot", el: p.foot } : null; }
      if (at.line) {
        const p = W.panes[at.line[0]];
        if (!p || !visible(p.el)) return null;
        const r = p.lines.find((x) => x.on && x.el.textContent.includes(at.line[1]));
        return r && visible(r.el) ? { kind: "line", el: r.el } : null;
      }
      if (at.row) { const r = [...win.side.querySelectorAll(".row")].find((x) => x.textContent.includes(at.row)); return r && visible(r) ? { kind: "row", el: r } : null; }
      if (at.tab != null) { const t = win.tabs.children[at.tab]; return t && visible(t) ? { kind: "tab", el: t } : null; }
      return null;
    }
    function renderHints(pos) {
      const show = cfg.hintStyle !== "none";
      const want = show ? S.hints.filter((x) => pos >= x.from && pos < x.to) : [];
      const keep = new Set();
      placed = [];
      while (svg.lastChild) svg.lastChild.remove();
      const pr = pin.getBoundingClientRect();
      let n = 0;
      for (const hint of want) {
        const key = hint.from + ":" + hint.text;
        const tg = resolve(hint.at);
        if (!tg) continue;
        keep.add(key);
        n++;
        let rec = hintEls.get(key);
        if (!rec) {
          if (hint.kind === "box") rec = { el: h("div", "hint-box", '<span class="tag">' + K.esc(hint.text) + "</span>") };
          else rec = { el: h("div", "hint" + (tg.kind === "screen" ? " big" : ""), (hint.head ? '<span class="hh">' + K.esc(hint.head) + "</span>" : "") + K.esc(hint.text)) };
          if (!K.reduced && hint.kind !== "box") rec.el.classList.add("in");
          if (cfg.hintStyle === "pins" && hint.kind !== "box" && tg.kind !== "screen") { rec.mark = h("div", "pin-mark"); hintsEl.append(rec.mark); }
          hintsEl.append(rec.el);
          hintEls.set(key, rec);
        }
        place(rec, hint, tg, pr, n);
      }
      for (const [key, rec] of hintEls) if (!keep.has(key)) { rec.el.remove(); if (rec.mark) rec.mark.remove(); hintEls.delete(key); }
    }
    function place(rec, hint, tg, pr, n) {
      const el = rec.el;
      const PW = pr.width, PH = pr.height;
      if (hint.kind === "box") {
        const T = rel(tg.el.getBoundingClientRect(), pr);
        Object.assign(el.style, { left: T.x - 6 + "px", top: T.y - 6 + "px", width: T.w + 12 + "px", height: T.h + 12 + "px" });
        return;
      }
      if (tg.kind === "screen") {
        el.style.left = Math.round((PW - el.offsetWidth) / 2) + "px";
        el.style.top = Math.round(Math.max(10, PH * 0.21 - el.offsetHeight - 16)) + "px";
        return;
      }
      const T = rel(tg.el.getBoundingClientRect(), pr);
      const bw = el.offsetWidth, bh = el.offsetHeight;
      let x, y, ax, ay;
      switch (tg.kind) {
        case "pane": ax = T.x + T.w - 36; ay = T.y + 2; x = T.x + T.w - bw - 22; y = T.y + 30; break;
        case "label": ax = T.x + T.w / 2; ay = T.y + T.h; x = T.x + T.w - bw; y = T.y + T.h + 16; break;
        case "foot": ax = T.x + 70; ay = T.y; x = T.x + 24; y = T.y - bh - 16; break;
        case "line": ax = T.x + 40; ay = T.y + T.h; x = T.x + 40; y = T.y + T.h + 12; if (y + bh > PH - 30) { y = T.y - bh - 12; ay = T.y; } break;
        case "row": ax = T.x + T.w - 4; ay = T.y + T.h / 2; x = T.x + T.w + 18; y = T.y - 8; break;
        case "tab": ax = T.x + T.w / 2; ay = T.y + T.h; x = T.x; y = T.y + T.h + 16; break;
        case "palette": ax = T.x + T.w; ay = T.y + 40; x = T.x + T.w + 20; y = T.y + 16; if (x + bw > PW - 10) { x = T.x + T.w - bw; y = T.y + T.h + 14; ax = T.x + T.w - 40; ay = T.y + T.h; } break;
        case "notes": ax = T.x; ay = T.y + 70; x = T.x - bw - 20; y = T.y + 50; break;
        default: ax = T.x; ay = T.y; x = T.x; y = T.y;
      }
      x = Math.max(10, Math.min(PW - bw - 10, x));
      y = Math.max(10, Math.min(PH - bh - 10, y));
      // keep clear of the page's own furniture (a tour card) and of hints already placed
      const obstacles = (cfg.avoid ? cfg.avoid() : []).concat(placed);
      for (let pass = 0; pass < 3; pass++) {
        let moved = false;
        for (const o of obstacles) {
          const hit = x < o.x + o.w && x + bw > o.x && y < o.y + o.h && y + bh > o.y;
          if (!hit) continue;
          moved = true;
          if (o.y + o.h + 10 + bh < PH - 10) y = o.y + o.h + 10;
          else x = Math.min(PW - bw - 10, o.x + o.w + 12);
        }
        if (!moved) break;
      }
      placed.push({ x, y, w: bw, h: bh });
      el.style.left = Math.round(x) + "px";
      el.style.top = Math.round(y) + "px";
      if (rec.mark) {
        rec.mark.textContent = String(n);
        rec.mark.style.left = Math.round(ax) + "px";
        rec.mark.style.top = Math.round(ay) + "px";
        return;
      }
      // leader: from the nearest point of the box to the anchor
      const bx = Math.max(x, Math.min(x + bw, ax)), by = Math.max(y, Math.min(y + bh, ay));
      svg.append(sv("path", { class: "hint-line", d: "M" + bx + " " + by + " L" + ax + " " + ay }));
      svg.append(sv("circle", { class: "hint-ring", cx: ax, cy: ay, r: 6 }));
      svg.append(sv("circle", { class: "hint-dot", cx: ax, cy: ay, r: 2.5 }));
    }

    /* ---------------------------------------------------- keystrokes */
    function renderKeys(pos) {
      if (!cfg.keys) return;
      const recent = S.keys.filter((k) => pos >= k[0] && pos < k[0] + 0.05).slice(-3);
      const html = recent.map((k) => {
        const caps = /^[A-Z][a-z]*\+/.test(k[1]) ? k[1].split("+").map((c) => '<span class="cap">' + K.esc(c) + "</span>").join('<span class="plus">+</span>') : '<span class="cap">' + K.esc(k[1]) + "</span>";
        return '<div class="ks" data-k="' + k[0] + '">' + caps + (k[2] ? '<span class="what">' + K.esc(k[2]) + "</span>" : "") + "</div>";
      }).join("");
      if (keysEl._h !== html) { keysEl._h = html; keysEl.innerHTML = html; }
    }

    /* ---------------------------------------------------- lid + reboot */
    const trainView = W.views["t/1"] && W.views["t/1"].el;
    function lidFx(pos) {
      if (!trainView) return;
      const lidT = K.smooth(pos, 3.40, 3.44) * (1 - K.smooth(pos, 3.58, 3.62));
      lid.style.opacity = (lidT * 0.95).toFixed(3);
      const away = pos >= 3.40 && pos < 3.70;
      inset.style.opacity = (K.smooth(pos, 3.40, 3.43) * (1 - K.smooth(pos, 3.68, 3.70))).toFixed(3);
      if (away && trainView.parentNode !== insetQ) { insetQ.append(trainView); trainView.hidden = false; }
      if (!away && trainView.parentNode === insetQ) { win.viewsHost.append(trainView); trainView.hidden = true; }
      const st = $("inset-st");
      if (st) st.textContent = pos < 3.60 ? "still running · the laptop sleeps" : "still running · the laptop reconnects";
    }
    function rebootFx(pos) {
      const a = K.seg(pos, 6.14, 6.18), b = K.seg(pos, 6.18, 6.21);
      const root = win.root;
      if (pos >= 6.14 && pos < 6.36) {
        root.style.transform = "scale(" + (1 - b).toFixed(3) + "," + Math.max(0.004, 1 - a).toFixed(3) + ")";
        root.style.filter = a > 0.5 ? "brightness(2.2)" : "";
      } else if (root.style.transform) { root.style.transform = ""; root.style.filter = ""; }
      dark.style.opacity = pos >= 6.21 && pos < 6.36 ? "1" : "0";
      $("dark-a").textContent = pos < 6.29 ? "POWERED OFF" : "POWER ON";
      $("dark-b").textContent = pos < 6.29 ? "fri 18:04" : "mon 09:14";
    }

    /* ---------------------------------------------------- frame */
    function frame(force) {
      const pos = Math.min(NCH - 0.001, posFromScroll());
      if (!force && Math.abs(pos - lastPos) < 1e-5) return;
      lastPos = pos;
      curPos = pos;
      const ci = Math.min(NCH - 1, Math.floor(pos));
      if (over && ci !== overCh) over = null;
      lidFx(pos);
      W.update(pos, (id, p) => {
        const r = S.restoreCtx(id, p);
        if (over && !r) return { active: id === over.pane };
        return r;
      });
      win.render(pos, over ? { tab: over.tab, pane: over.pane } : {});
      rebootFx(pos);
      // the page's own furniture first, so hints can keep clear of it
      if (cfg.onFrame) cfg.onFrame(pos, ci, api);
      renderHints(pos);
      renderKeys(pos);
      if (pal) palRender();
    }
    let raf = 0;
    addEventListener("scroll", () => { if (!raf) raf = requestAnimationFrame(() => { raf = 0; frame(); }); }, { passive: true });
    addEventListener("resize", () => { layoutMarks(); win.fit(); frame(true); });

    /* ---------------------------------------------------- mouse */
    win.tabs.addEventListener("click", (e) => {
      const el = e.target.closest(".q-tab");
      if (!el) return;
      const act = over || W.activeAt(curPos);
      const tabs = W.tabsOf(W.tabById[act.tab].project, curPos);
      const t = tabs[[...win.tabs.querySelectorAll(".q-tab")].indexOf(el)];
      if (t) setOver(t.id, W.panesOfTab(t.id, curPos)[0]);
    });
    win.area.addEventListener("click", (e) => {
      const p = e.target.closest(".q-pane");
      if (p && p.dataset.pane) setOver(W.panes[p.dataset.pane].s.tab, p.dataset.pane);
    });
    win.side.addEventListener("click", (e) => {
      const row = e.target.closest(".row.proj");
      if (!row) return;
      const name = row.textContent.replace(/^[▸\s]+/, "").split(/\s/)[0];
      const proj = S.projects.find((p) => p.name === name);
      const t = proj && W.tabsOf(proj.id, curPos)[0];
      if (t) setOver(t.id, W.panesOfTab(t.id, curPos)[0]);
    });

    /* ---------------------------------------------------- keyboard */
    let pal = null, flashT = 0;
    function flash(msg) { flashEl.textContent = msg; flashEl.classList.add("on"); clearTimeout(flashT); flashT = setTimeout(() => flashEl.classList.remove("on"), 1700); }
    function closeLayer() { kb.innerHTML = ""; pal = null; }
    function wrapQ(inner, shade) {
      const wrap = h("div", "q");
      wrap.style.cssText = "position:absolute;inset:0;background:" + (shade ? "rgba(12,12,12,.55)" : "transparent") + ";pointer-events:" + (shade ? "auto" : "none") + ";--q-fs:" + (win.fs || 13) + "px";
      inner.style.pointerEvents = "auto";
      wrap.append(inner);
      kb.append(wrap);
      return wrap;
    }
    function openPalette() {
      closeLayer();
      pal = { q: "", sel: 0, items: [] };
      const box = h("div", "q-dlg", '<div class="row"><span class="sel">&gt;</span> <input id="palq" aria-label="Search everything" autocomplete="off" spellcheck="false"></div><div id="palr"></div><div class="row sub" style="margin-top:1em">↑↓ nav · Enter run · Esc close</div>');
      wrapQ(box, false);
      const input = $("palq");
      input.addEventListener("input", () => { pal.q = input.value; pal.sel = 0; palRender(); });
      input.focus();
      palRender();
    }
    function palItems() {
      const q = pal.q.trim().toLowerCase();
      const items = [];
      for (const t of S.tabs) {
        if (curPos < t.at) continue;
        const proj = W.projById[t.project];
        const label = "Switch to " + proj.name + " · " + t.name + (proj.host ? "@" + proj.host : "");
        if (!q || label.toLowerCase().includes(q)) items.push({ kind: "cmd", label, go: () => setOver(t.id, W.panesOfTab(t.id, curPos)[0]) });
      }
      if (!q || "go to the agent waiting longest".includes(q)) items.push({ kind: "cmd", label: "Go to the agent waiting longest", key: "Alt+Shift+A", go: jumpWaiting });
      if (q) for (const hit of W.search(pal.q, curPos).slice(0, 6)) {
        items.push({ kind: "hit", label: hit.tabIdx + "." + hit.paneIdx + " · " + hit.type + (hit.name ? " · " + hit.name : "") + " · " + W.projById[hit.project].name, n: hit.n, last: hit.last, go: () => setOver(hit.tab.id, hit.id) });
      }
      return items.slice(0, 12);
    }
    function palRender() {
      if (!pal) return;
      pal.items = palItems();
      let html = "", hdr = false;
      pal.items.forEach((it, i) => {
        if (it.kind === "hit" && !hdr) { html += '<div class="row hdr" style="color:#626262">FOUND IN PANES</div>'; hdr = true; }
        const sel = i === pal.sel;
        html += '<div class="row' + (sel ? " sel" : "") + '">' + (sel ? "› " : "  ") + K.esc(it.label) + (it.n ? ' <span class="sub">' + it.n + "×</span>" : "") + (it.key ? ' <span class="key">' + it.key + "</span>" : "") + "</div>";
        if (it.last) html += '<div class="row sub">    ' + K.esc(it.last.slice(0, 64)) + "</div>";
      });
      if (!pal.items.length) html = '<div class="row sub">No matches in any pane</div>';
      const r = $("palr");
      if (r) r.innerHTML = html;
    }
    function jumpWaiting() {
      let best = null;
      for (const t of S.tabs) {
        if (curPos < t.at) continue;
        for (const id of W.panesOfTab(t.id, curPos)) {
          const p = W.panes[id];
          if (p.stateAt(curPos) !== "blocked") continue;
          const since = [...p.s.states].reverse().find((e) => e[1] === "blocked" && e[0] <= curPos)[0];
          if (!best || since < best.since) best = { since, t, id };
        }
      }
      if (best) { setOver(best.t.id, best.id); flash("▲ " + W.panes[best.id].nameAt(curPos) + " has waited longest"); }
      else flash("No agent is waiting on you right now");
    }
    function notifPanel() {
      closeLayer();
      const box = h("div", "q-notif focused");
      box.style.cssText = "top:calc(var(--q-fs) * 1.36);bottom:calc(var(--q-fs) * 1.36);";
      const cards = [];
      for (const t of S.tabs) {
        if (curPos < t.at) continue;
        for (const id of W.panesOfTab(t.id, curPos)) {
          const p = W.panes[id], st = p.stateAt(curPos);
          if (!st || st === "idle") continue;
          const title = st === "blocked" ? "Needs approval: Bash" : st === "done" ? "Turn finished" : "Working";
          cards.push('<div class="card"><div class="src"><span class="' + (st === "blocked" ? "sev-warn" : "sev-info") + '">' + K.esc(p.nameAt(curPos)) + '</span><span class="age">now</span></div>  ' + title + '\n<span class="sub">  ' + K.esc(W.projById[t.project].name + " · " + t.name) + '</span></div><div class="sep">· · · · · · · · · · · · · · ·</div>');
        }
      }
      box.innerHTML = '<span class="nt"> Notifications </span>' + (cards.join("") || '<div class="card sub">No notifications</div>');
      wrapQ(box, false);
    }
    function helpPanel() {
      closeLayer();
      const box = h("div", "q-dlg");
      const rows = [
        '<span class="title">Quil v' + (S.version || "1.82.1") + "</span>", '<span class="sub">github.com/artyomsv/quil</span>', "",
        '<span class="title">On this page</span>',
        '  <span class="key">scroll</span>         play the session',
        '  <span class="key">n / p</span>          next / previous use case',
        '  <span class="key">Alt+Shift+P</span>    search tabs, panes and scrollback',
        '  <span class="key">Alt+Shift+A</span>    jump to the agent waiting longest',
        '  <span class="key">Alt+N</span>          notifications',
        '  <span class="key">click</span>          a tab, a pane or a project', "",
        '<span class="title">Use cases</span>',
      ].concat(CH.map((c, i) => '  <a href="#ch-' + c.id + '" data-go="' + i + '"><span class="key">' + i + "</span>  " + K.esc(c.persona.toLowerCase()) + "</a>")).concat(["", '<span class="sub">Esc close</span>']);
      box.innerHTML = rows.join("\n");
      wrapQ(box, true);
      box.addEventListener("click", (e) => { const a = e.target.closest("[data-go]"); if (a) { e.preventDefault(); closeLayer(); goTo(+a.dataset.go + 0.02); } });
    }
    addEventListener("keydown", (e) => {
      const k = e.key;
      if (pal) {
        if (k === "Escape") { closeLayer(); e.preventDefault(); return; }
        if (k === "ArrowDown") { pal.sel = Math.min(pal.items.length - 1, pal.sel + 1); palRender(); e.preventDefault(); return; }
        if (k === "ArrowUp") { pal.sel = Math.max(0, pal.sel - 1); palRender(); e.preventDefault(); return; }
        if (k === "Enter") { const it = pal.items[pal.sel]; closeLayer(); if (it) it.go(); e.preventDefault(); return; }
        return;
      }
      if (e.target && (/^(INPUT|TEXTAREA|SELECT)$/.test(e.target.tagName) || e.target.isContentEditable)) return;
      // The tour's keys work only while the tour is on screen; below it, n and p are just letters.
      const lr = live.getBoundingClientRect();
      if (lr.bottom <= 0 || lr.top >= innerHeight) return;
      if (e.altKey && e.shiftKey && (k === "P" || k === "p")) { openPalette(); e.preventDefault(); return; }
      if (e.altKey && e.shiftKey && (k === "A" || k === "a")) { jumpWaiting(); e.preventDefault(); return; }
      if (e.altKey && !e.shiftKey && (k === "n" || k === "N")) { if (kb.firstChild) closeLayer(); else notifPanel(); e.preventDefault(); return; }
      if (k === "Escape") { closeLayer(); return; }
      if (k === "F1" || k === "?") { if (kb.firstChild) closeLayer(); else helpPanel(); e.preventDefault(); return; }
      if (e.altKey || e.ctrlKey || e.metaKey) return;
      if (k === "n") { next(); e.preventDefault(); }
      if (k === "p") { prev(); e.preventDefault(); }
      if (cfg.onKey) cfg.onKey(e, api);
    });
    const next = () => goTo(Math.min(NCH - 1, Math.floor(curPos) + 1) + 0.03);
    const prev = () => goTo(Math.max(0, Math.floor(curPos) - (curPos - Math.floor(curPos) < 0.12 ? 1 : 0)) + 0.03);

    /* ---------------------------------------------------- api */
    const api = {
      S, W, win, pin, goTo, next, prev, frame, flash,
      pos: () => curPos,
      chapter: () => Math.min(NCH - 1, Math.floor(curPos)),
      scrollFor,
      census: (p) => K.census(W, p),
    };

    // deep link: /#tour-agents … /#tour-remember (plain #agents belongs to the #use-cases article)
    const dl = /^#tour-(.+)$/.exec(location.hash);
    const hi = dl ? CH.findIndex((c) => c.id === dl[1]) : -1;
    frame(true);
    if (hi > 0) setTimeout(() => goTo(hi + 0.03, true), 30);
    return api;
  }

export { start, fig, BANNER };
