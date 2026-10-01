// @ts-nocheck — ported from the B2 mock (kit/quil-kit.js); plain browser JavaScript on purpose.
/* ==========================================================================
   quil-kit.js — renders the Quil TUI replica from a scroll-position timeline.

   Everything visible is a pure function of one number, `pos` (0 … 12, one
   unit per chapter), so a page can scrub backwards and forwards freely.
   The only time-driven things are the braille spinner (200 ms, as in
   internal/tui/workstate.go), the blinking caret and optional "ambient"
   log streams.

   API (default export of this module):
     K.world(scenario)              -> World: panes + views built once
     world.mountWindow(el, opts)    -> Win: chrome + all tab views in one TUI
     world.update(pos)              -> updates every pane / view
     win.render(pos)                -> updates chrome, active view, overlays
   ========================================================================== */

  const K = {};
  K.clamp = (x, a = 0, b = 1) => Math.min(b, Math.max(a, x));
  K.seg = (pos, a, b) => K.clamp((pos - a) / (b - a));
  K.ease = (t) => (t < 0.5 ? 2 * t * t : 1 - Math.pow(-2 * t + 2, 2) / 2);
  K.smooth = (pos, a, b) => K.ease(K.seg(pos, a, b));
  K.lerp = (a, b, t) => a + (b - a) * t;
  /** Value of a [[pos, value], …] timeline at pos (last entry whose pos <= pos). */
  K.at = (tl, pos, dflt) => {
    let v = dflt;
    if (!tl) return v;
    for (let i = 0; i < tl.length; i++) {
      if (pos >= tl[i][0]) v = tl[i][1];
      else break;
    }
    return v;
  };
  K.inWin = (wins, pos) => !!wins && wins.some((w) => pos >= w[0] && pos < w[1]);
  K.esc = (s) => String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
  K.reduced = typeof matchMedia === "function" && matchMedia("(prefers-reduced-motion: reduce)").matches;

  /* ---------------------------------------------------------------- spinner */
  K.SPIN = "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏";
  K.frame = 0;
  K.spin = () => '<span class="q-spin">' + K.SPIN[K.frame] + "</span>";
  setInterval(() => {
    K.frame = (K.frame + 1) % K.SPIN.length;
    const f = K.SPIN[K.frame];
    const els = document.getElementsByClassName("q-spin");
    for (let i = 0; i < els.length; i++) els[i].textContent = f;
  }, 200);

  /* ------------------------------------------------------------- DOM helper */
  K.h = (tag, cls, html) => {
    const e = document.createElement(tag);
    if (cls) e.className = cls;
    if (html != null) e.innerHTML = html;
    return e;
  };
  const setHTML = (el, html) => {
    if (el._h !== html) {
      el._h = html;
      el.innerHTML = html;
    }
  };
  const toggle = (el, cls, on) => {
    if (el.classList.contains(cls) !== !!on) el.classList.toggle(cls, !!on);
  };

  /* =================================================================== Pane */
  const TYPE_MODE = {
    "claude-code": "stream", codex: "stream", opencode: "stream", terminal: "stream", ssh: "stream",
    stripe: "stream", pg: "stream", lazygit: "screen", hunk: "screen", k9s: "screen", lazysql: "screen",
  };

  class Pane {
    constructor(id, spec, world) {
      this.id = id;
      this.s = spec;
      this.world = world;
      this.el = K.h("div", "q-pane");
      this.el.dataset.pane = id;
      this.title = K.h("div", "q-ptitle");
      this.tCwd = K.h("span", "cwd");
      this.tMid = K.h("span", "mid");
      this.tNm = K.h("span", "nm");
      this.title.append(this.tCwd, this.tMid, this.tNm);
      this.body = K.h("div", "q-body stream");
      this.linesEl = K.h("div", "q-lines");
      this.foot = K.h("div", "q-foot");
      this.center = K.h("div", "q-center");
      this.center.hidden = true;
      this.body.append(this.center, this.linesEl, this.foot);
      this.el.append(this.title, this.body);

      // Pre-build every scripted line. A `clear` marker ends every line before it.
      this.lines = [];
      const src = spec.lines || [];
      for (let i = 0; i < src.length; i++) {
        const L = src[i];
        if (L.clear != null) {
          for (const p of this.lines) if (p.until == null || p.until > L.clear) p.until = L.clear;
          continue;
        }
        const el = K.h("div", "q-ln off" + (L.cls ? " " + L.cls : ""));
        const rec = { at: L.at, until: L.until, el, on: false, L };
        if (L.type) {
          rec.typed = true;
          rec.lastN = -1;
        } else {
          el.innerHTML = L.html === "" ? " " : L.html;
        }
        this.lines.push(rec);
        this.linesEl.append(el);
      }
      this.ambientLines = [];
      this._state = {};
    }

    /** 0 … 1 — how much of the layout this pane occupies (split animation). */
    presence(pos) {
      const s = this.s;
      const inT = s.at == null ? 1 : K.smooth(pos, s.at, s.at + 0.03);
      const outT = s.until == null ? 1 : 1 - K.smooth(pos, s.until - 0.03, s.until);
      return Math.min(inT, outT);
    }
    typeAt(pos) { return typeof this.s.type === "string" ? this.s.type : K.at(this.s.type, pos, "terminal"); }
    stateAt(pos) { return K.at(this.s.states, pos, null); }
    pinnedAt(pos) { return !!K.at(this.s.pin, pos, false); }
    cwdAt(pos) { return typeof this.s.cwd === "string" ? this.s.cwd : K.at(this.s.cwd, pos, ""); }
    nameAt(pos) { return typeof this.s.name === "string" || this.s.name == null ? this.s.name || "" : K.at(this.s.name, pos, ""); }
    gitAt(pos) { return typeof this.s.git === "string" || this.s.git == null ? this.s.git || "" : K.at(this.s.git, pos, ""); }
    /** Visible, searchable text at pos — what the palette's scrollback search reads. */
    textAt(pos) {
      const out = [];
      for (const r of this.lines) {
        if (pos >= r.at && (r.until == null || pos < r.until)) out.push(r.typed ? (r.L.prompt || "") + r.L.text : r.el.textContent);
      }
      return out;
    }

    update(pos, ctx) {
      const s = this.s;
      const st = this._state;
      const type = this.typeAt(pos);
      const mode = s.mode || TYPE_MODE[type] || "stream";
      if (st.mode !== mode) {
        this.body.className = "q-body " + mode;
        st.mode = mode;
      }

      // --- lines
      for (const r of this.lines) {
        const on = pos >= r.at && (r.until == null || pos < r.until);
        if (on !== r.on) {
          r.el.classList.toggle("off", !on);
          r.on = on;
        }
        if (on && r.typed) {
          const L = r.L;
          const t = K.seg(pos, r.at, r.at + (L.dur || 0.04));
          const n = Math.round(t * L.text.length);
          const caret = L.caret === false ? false : pos < (L.done != null ? L.done : r.at + (L.dur || 0.04) + 0.012);
          const key = n + (caret ? "c" : "");
          if (key !== r.lastN) {
            r.lastN = key;
            r.el.innerHTML = (L.prompt || "") + '<span class="' + (L.tcls || "c-white") + '">' + K.esc(L.text.slice(0, n)) + "</span>" + (caret ? '<span class="q-caret"></span>' : "");
          }
        }
      }

      // --- ambient stream (logs that keep moving while you read)
      if (s.ambient && pos >= s.ambient.from && pos < (s.ambient.to || 99)) this._ambient(true);
      else this._ambient(false);

      // --- footer: last entry at or before pos; entries may be functions of pos
      let foot = K.at(s.foot, pos, "");
      if (typeof foot === "function") foot = foot(pos);
      setHTML(this.foot, foot || "");

      // --- centred placeholder (worktree preparing, restore checklist)
      let center = K.at(s.center, pos, "");
      if (typeof center === "function") center = center(pos);
      if (ctx.center) center = ctx.center;
      const showCenter = !!center;
      if (this.center.hidden !== !showCenter) this.center.hidden = !showCenter;
      if (showCenter) setHTML(this.center, center);
      if (this.linesEl.hidden !== showCenter) this.linesEl.hidden = showCenter;
      const hideFoot = showCenter || !!ctx.ghostLines;
      if (this.foot.hidden !== hideFoot) this.foot.hidden = hideFoot;

      // --- title
      const state = ctx.label ? null : this.stateAt(pos);
      const working = state === "working";
      const cwd = this.cwdAt(pos);
      const cwdHTML = (working ? " " + K.spin() + " " : " ") + K.esc(cwd) + " ";
      setHTML(this.tCwd, cwdHTML);
      setHTML(this.tMid, ctx.focus ? "* FOCUS *" : "");
      let nm = this.nameAt(pos) || "";
      const muted = s.muted === true || K.at(s.muted, pos, false) === true;
      if (ctx.label) nm = ctx.label;
      else if (K.inWin(s.preparing, pos)) nm = K.SPIN[0] + " preparing...";
      else if (muted) nm = "[muted]" + (nm ? " " + nm : "");
      if (/^[⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏]/.test(nm)) setHTML(this.tNm, " " + K.spin() + K.esc(nm.slice(1)) + " ");
      else setHTML(this.tNm, nm ? " " + K.esc(nm) + " " : "");

      // --- border colour, precedence as in pane.go:1246-1297 (later wins)
      let b = "";
      if (state === "done" && !ctx.active) b = "done";
      if (this.pinnedAt(pos)) b = "pinned";
      if (K.at(s.del, pos, false)) b = "delete";
      if (ctx.active) b = "active";
      if (ctx.ghost || K.inWin(s.preparing, pos)) b = "ghost";
      if (ctx.drag) b = "drag";
      if (K.inWin(s.mcp, pos)) b = "mcp";
      if (st.b !== b) {
        this.el.classList.remove("active", "done", "pinned", "delete", "ghost", "drag", "mcp");
        if (b) this.el.classList.add(b);
        st.b = b;
      }
      toggle(this.el, "ghosted", !!ctx.ghostLines);
      toggle(this.el, "dark", !!ctx.dark);
    }

    _ambient(on) {
      const a = this.s.ambient;
      if (!a) return;
      if (on && !this._amb) {
        let i = 0;
        const tick = () => {
          const html = a.pool[i % a.pool.length];
          i++;
          const el = K.h("div", "q-ln", typeof html === "function" ? html(i) : html);
          this.linesEl.append(el);
          this.ambientLines.push(el);
          if (this.ambientLines.length > (a.keep || 40)) this.ambientLines.shift().remove();
        };
        if (!K.reduced) this._amb = setInterval(tick, a.every || 900);
      } else if (!on && this._amb) {
        clearInterval(this._amb);
        this._amb = null;
        for (const el of this.ambientLines) el.remove();
        this.ambientLines = [];
      }
    }
  }

  /* =================================================================== View */
  /** One tab's split tree. Layout notation: ["h"|"v", child, child, …], a child
   *  is a pane id or another array. A pane's flex weight is spec.w (default 1). */
  class View {
    constructor(tab, world) {
      this.tab = tab;
      this.world = world;
      this.el = K.h("div", "q-view");
      this.el.dataset.tab = tab.id;
      this.cells = [];
      this.splits = [];
      this.el.append(this._build(tab.layout, this.el, true));
    }
    _build(node, parent, root) {
      if (typeof node === "string") {
        const cell = K.h("div", "q-cell");
        const pane = this.world.panes[node];
        cell.append(pane.el);
        pane.home = cell;
        this.cells.push({ cell, pane });
        return cell;
      }
      const [dir, ...kids] = node;
      const sp = K.h("div", "q-split " + dir);
      const rec = { el: sp, kids: [], dir };
      for (const k of kids) {
        const child = this._build(k, sp, false);
        sp.append(child);
        rec.kids.push({ el: child, node: k });
      }
      this.splits.push(rec);
      if (root) {
        sp.style.flex = "1 1 0";
      }
      return sp;
    }
    _presence(node, pos) {
      if (typeof node === "string") return this.world.panes[node].presence(pos);
      let m = 0;
      for (let i = 1; i < node.length; i++) m = Math.max(m, this._presence(node[i], pos));
      return m;
    }
    _weight(node) {
      if (typeof node === "string") return this.world.panes[node].s.w || 1;
      return node.w || 1;
    }
    update(pos) {
      for (const sp of this.splits) {
        for (const k of sp.kids) {
          const p = this._presence(k.node, pos);
          const w = p * this._weight(k.node);
          const gone = p <= 0.001;
          if (k._gone !== gone) {
            k.el.classList.toggle("gone", gone);
            k._gone = gone;
          }
          const g = w.toFixed(3);
          if (k._g !== g) {
            k.el.style.flexGrow = g;
            k._g = g;
          }
        }
      }
    }
    paneIds() {
      const out = [];
      const walk = (n) => (typeof n === "string" ? out.push(n) : n.slice(1).forEach(walk));
      walk(this.tab.layout);
      return out;
    }
  }

  /* ================================================================== World */
  class World {
    constructor(S) {
      this.S = S;
      this.panes = {};
      for (const id in S.panes) this.panes[id] = new Pane(id, S.panes[id], this);
      this.views = {};
      for (const tab of S.tabs) this.views[tab.id] = new View(tab, this);
      this.tabById = {};
      for (const t of S.tabs) this.tabById[t.id] = t;
      this.projById = {};
      for (const p of S.projects) this.projById[p.id] = p;
    }
    activeAt(pos) {
      const a = K.at(this.S.focus, pos, this.S.focus[0][1]);
      return { tab: a[0], pane: a[1] };
    }
    update(pos, ctxFor) {
      const act = this.activeAt(pos);
      for (const id in this.panes) {
        const p = this.panes[id];
        const ctx = (ctxFor && ctxFor(id, pos)) || {};
        if (ctx.active == null) ctx.active = id === act.pane;
        p.update(pos, ctx);
      }
      for (const id in this.views) this.views[id].update(pos);
    }
    tabsOf(proj, pos) {
      return this.S.tabs.filter((t) => t.project === proj && pos >= t.at && (t.until == null || pos < t.until));
    }
    projects(pos) {
      return this.S.projects.filter((p) => pos >= p.at && (p.until == null || pos < p.until));
    }
    panesOfTab(tabId, pos) {
      return this.views[tabId].paneIds().filter((id) => this.panes[id].presence(pos) > 0.5);
    }
    /** Roll-up counts for a project — blocked, working and done are exclusive per pane. */
    badges(projId, pos) {
      const c = { blocked: 0, working: 0, done: 0, pinned: 0 };
      const act = this.activeAt(pos);
      for (const t of this.tabsOf(projId, pos)) {
        for (const id of this.panesOfTab(t.id, pos)) {
          const p = this.panes[id];
          const st = p.stateAt(pos);
          if (st === "blocked") c.blocked++;
          else if (st === "working") c.working++;
          else if (st === "done" && id !== act.pane) c.done++;
          if (p.pinnedAt(pos)) c.pinned++;
        }
      }
      return c;
    }
    /** Every pane whose loaded output contains `q` (literal, case-insensitive). */
    search(q, pos) {
      if (!q) return [];
      const needle = q.toLowerCase();
      const hits = [];
      for (const t of this.S.tabs) {
        if (pos < t.at || (t.until != null && pos >= t.until)) continue;
        const ids = this.panesOfTab(t.id, pos);
        ids.forEach((id, i) => {
          const p = this.panes[id];
          const text = p.textAt(pos);
          let n = 0, last = "";
          for (const ln of text) {
            const l = ln.toLowerCase();
            let k = l.indexOf(needle);
            while (k >= 0) { n++; last = ln; k = l.indexOf(needle, k + needle.length); }
          }
          if (n) {
            const tabs = this.tabsOf(t.project, pos);
            hits.push({ id, tab: t, tabIdx: tabs.indexOf(t) + 1, paneIdx: i + 1, n, last: last.trim(), type: p.typeAt(pos), name: p.nameAt(pos), project: t.project });
          }
        });
      }
      hits.sort((a, b) => b.n - a.n);
      return hits;
    }

    mountWindow(el, opts) {
      return new Win(this, el, opts || {});
    }
  }

  /* ================================================================ Window */
  class Win {
    constructor(world, host, opts) {
      this.world = world;
      this.opts = opts;
      this.root = K.h("div", "q");
      this.win = K.h("div", "q-win");
      this.side = K.h("div", "q-side");
      this.tabs = K.h("div", "q-tabs");
      this.area = K.h("div", "q-area");
      this.status = K.h("div", "q-status");
      this.statusL = K.h("span", "l");
      this.statusR = K.h("span", "r");
      this.status.append(this.statusL, this.statusR);
      this.ovArea = K.h("div", "q-ov");
      this.ovWin = K.h("div", "q-ov");
      this.viewsHost = K.h("div", "q-views");
      this.viewsHost.style.cssText = "position:absolute;inset:0;";
      this.notes = K.h("div", "q-notes");
      this.notes.hidden = true;
      this.area.append(this.viewsHost, this.notes, this.ovArea);
      this.win.append(this.side, this.tabs, this.area, this.status);
      this.root.append(this.win, this.ovWin);
      host.append(this.root);
      this.root.style.position = "absolute";
      this.root.style.inset = "0";
      for (const id in world.views) {
        const v = world.views[id];
        v.el.hidden = true;
        this.viewsHost.append(v.el);
      }
      this.overlayEls = {};
      this.fit();
      if (typeof ResizeObserver === "function") new ResizeObserver(() => this.fit()).observe(host);
    }
    /** Font size from the host width: aim for `cols` columns, clamped. */
    fit() {
      const r = this.root.getBoundingClientRect();
      const cols = this.opts.cols || 150;
      const fs = this.opts.fs || K.clamp(r.width / (cols * 0.6), this.opts.minFs || 6, this.opts.maxFs || 13.5);
      this.root.style.setProperty("--q-fs", fs.toFixed(2) + "px");
      this.fs = fs;
      // the real sidebar hides itself below 100 columns (sidebar.go:14-15)
      this.cols = Math.round(r.width / (fs * 0.6));
      this.rows = Math.round(r.height / (fs * 1.36));
      toggle(this.win, "no-sidebar", this.cols < 100);
    }

    render(pos, over) {
      over = over || {};
      const W = this.world, S = W.S;
      const act = over.tab ? { tab: over.tab, pane: over.pane } : W.activeAt(pos);
      const tab = W.tabById[act.tab];
      const proj = W.projById[tab.project];

      // --- which view is visible
      const hideViews = over.hideViews || K.inWin(S.hideViews, pos);
      for (const id in W.views) {
        const v = W.views[id];
        // a view borrowed by the page (shown somewhere else for a moment) is not ours to hide
        if (v.el.parentNode !== this.viewsHost) continue;
        const vis = id === act.tab && !hideViews;
        if (v.el.hidden === vis) v.el.hidden = !vis;
      }

      // --- pane notes (Alt+E): the views give up the right-hand share
      const nt = this.opts.notes ? this.opts.notes(pos, act) : (K.inWin(S.notesOpen, pos) && S.notesAt ? S.notesAt(pos) : null);
      const showNotes = !!nt && !hideViews;
      if (this.notes.hidden === showNotes) this.notes.hidden = !showNotes;
      const nw = showNotes ? (nt.width || this.opts.notesWidth || "40%") : "0px";
      if (this._nw !== nw) {
        this._nw = nw;
        this.viewsHost.style.right = nw;
        this.notes.style.width = showNotes ? "calc(" + nw + " - 1ch)" : "";
      }
      if (showNotes) setHTML(this.notes, this._notesHTML(nt));

      // --- chrome visibility (the boot sequence shows it piece by piece)
      const chrome = over.chrome || K.at(S.chrome, pos, { side: true, tabs: true, status: true });
      this.side.style.visibility = chrome.side ? "" : "hidden";
      this.tabs.style.visibility = chrome.tabs ? "" : "hidden";
      this.status.style.visibility = chrome.status ? "" : "hidden";
      toggle(this.win, "no-sidebar", chrome.side === "none" || this.cols < 100);

      setHTML(this.side, this._sidebar(pos, act, proj, chrome));
      setHTML(this.tabs, this._tabbar(pos, act, proj, chrome));
      const st = this._status(pos, act, proj, over);
      setHTML(this.statusL, st[0]);
      setHTML(this.statusR, st[1]);

      // --- overlays
      const want = {};
      for (const o of S.overlays || []) {
        if (pos >= o.from && pos < o.to && (!o.only || o.only === this.opts.variant) && !(o.skip && o.skip === this.opts.variant)) want[o.id] = o;
      }
      for (const id in this.overlayEls) {
        if (!want[id]) {
          this.overlayEls[id].remove();
          delete this.overlayEls[id];
        }
      }
      for (const id in want) {
        const o = want[id];
        let el = this.overlayEls[id];
        if (!el) {
          el = K.h("div", o.cls || "q-dlg");
          if (!K.reduced && o.enter !== false) el.classList.add("enter");
          (o.layer === "win" ? this.ovWin : this.ovArea).append(el);
          this.overlayEls[id] = el;
        }
        setHTML(el, o.html(K.seg(pos, o.from, o.to), pos, this));
      }
    }

    _notesHTML(nt) {
      const out = [];
      out.push('<div class="hd"><span class="badge"> INPUT </span> <span class="ttl">notes: ' + K.esc(nt.title) + (nt.dirty ? " *" : "") + "</span></div>");
      const rows = [];
      const lines = nt.lines || [];
      lines.forEach((l, i) => rows.push('<span class="ln">' + (i + 1) + "</span>" + l));
      for (let i = 0; i < 60; i++) rows.push('<span class="tilde">~</span>');
      out.push('<div class="ed">' + rows.join("\n") + "</div>");
      out.push('<div class="ft">' + (nt.foot || "Tab pane  Ctrl+S save  Esc exit  Alt+E") + "</div>");
      return out.join("");
    }

    _tabbar(pos, act, proj, chrome) {
      const W = this.world;
      if (!chrome.tabs) return "";
      const tabs = W.tabsOf(proj.id, pos);
      const out = [];
      tabs.forEach((t, i) => {
        const ids = W.panesOfTab(t.id, pos);
        let working = false, blocked = false, pinned = false, done = false, pinnedFocused = false;
        for (const id of ids) {
          const p = W.panes[id];
          const s = p.stateAt(pos);
          if (s === "working") working = true;
          if (s === "blocked") blocked = true;
          if (s === "done" && id !== act.pane) done = true;
          if (p.pinnedAt(pos)) { pinned = true; if (id === act.pane) pinnedFocused = true; }
        }
        const isActive = t.id === act.tab;
        let label = i + 1 + ":" + K.esc(t.name);
        if (pinned && !pinnedFocused) label = "◆" + label;
        if (working) label = K.spin() + " " + label;
        if (isActive) label = "* " + label;
        let cls = "q-tab";
        if (blocked) cls += " blocked" + (isActive ? " is-active" : "");
        else if (pinned && !pinnedFocused) cls += " pinned" + (isActive ? " is-active" : "");
        else if (done && !isActive) cls += " done";
        else if (isActive) cls += " active";
        out.push('<span class="' + cls + '">' + label + "</span>");
      });
      return out.join("");
    }

    _sidebar(pos, act, proj, chrome) {
      const W = this.world, S = W.S;
      if (!chrome.side || chrome.side === "none") return "";
      const rows = [];
      rows.push('<div class="h">PROJECTS</div>');
      const projs = chrome.projects ? W.projects(pos).slice(0, chrome.projects) : W.projects(pos);
      for (const p of projs) {
        const b = W.badges(p.id, pos);
        let badge = "";
        if (b.blocked) badge += ' <span class="st-blocked">▲' + b.blocked + "</span>";
        if (b.working) badge += ' <span class="st-working">' + K.spin() + b.working + "</span>";
        if (b.done) badge += ' <span class="st-done">✓' + b.done + "</span>";
        if (b.pinned) badge += ' <span class="st-pinned">◆' + b.pinned + "</span>";
        const link = K.at(p.link, pos, null);
        if (link === "retry") badge += ' <span class="st-retry">⟳</span>';
        if (link === "down") badge += ' <span class="st-down">⚡</span>';
        const isAct = p.id === proj.id;
        const cls = "proj" + (isAct ? " active" : "") + (link === "down" ? " offline" : "");
        rows.push('<div class="row ' + cls + '"><span>' + (isAct ? "▸ " : "  ") + K.esc(p.name) + "</span><span>" + badge + "</span></div>");
        if (p.host) rows.push('<div class="row host"><span>   @' + K.esc(p.host) + "</span></div>");
      }
      if (chrome.panes === false) return rows.join("");
      rows.push('<div class="gap"></div>');
      rows.push('<div class="h">PANES</div>');
      const tabs = W.tabsOf(proj.id, pos);
      let first = true;
      tabs.forEach((t, i) => {
        if (!first) rows.push('<div class="gap"></div>');
        first = false;
        const isAct = t.id === act.tab;
        rows.push('<div class="row tabh' + (isAct ? " active" : "") + '"><span>' + (isAct ? "▸ " : "  ") + (i + 1) + ":" + K.esc(t.name) + "</span></div>");
        for (const id of W.panesOfTab(t.id, pos)) {
          const p = W.panes[id];
          const s = p.stateAt(pos);
          const focused = id === act.pane;
          let glyph = "○", cls = "st-idle", extra = "";
          if (s === "blocked" && !focused) { glyph = "▲"; cls = "st-blocked"; extra = p.s.blockedTool ? " " + K.at(p.s.blockedTool, pos, "") : ""; }
          else if (s === "working") { glyph = K.spin(); cls = "st-working"; }
          else if (p.pinnedAt(pos)) { glyph = "◆"; cls = "st-pinned"; }
          else if (s === "done" && !focused) { glyph = "✓"; cls = "st-done"; }
          const suffix = p.pinnedAt(pos) && glyph !== "◆" ? ' <span class="st-pinned">◆</span>' : "";
          rows.push('<div class="row ' + cls + '"><span>' + (focused ? "▸ " : "  ") + glyph + " " + K.esc(p.nameAt(pos) || id) + K.esc(extra) + suffix + "</span></div>");
          const git = p.gitAt(pos);
          if (git) rows.push('<div class="row git"><span>  ⎇ ' + K.esc(git) + "</span></div>");
        }
      });
      return rows.join("");
    }

    _status(pos, act, proj, over) {
      const W = this.world, S = W.S;
      const pane = W.panes[act.pane];
      const tabs = W.tabsOf(proj.id, pos);
      const tabIdx = tabs.findIndex((t) => t.id === act.tab) + 1;
      const panes = W.panesOfTab(act.tab, pos).length;
      let left = "";
      if (over.notes || K.inWin(S.notesOpen, pos)) left += "[notes] ";
      if (over.focus) left += "[focus] ";
      left += K.esc(pane ? pane.cwdAt(pos) : "");
      const model = pane && K.at(pane.s.model, pos, "");
      if (model) left += "  " + K.esc(model) + "  ";
      else left += "  ";
      left += "tab " + tabIdx + "/" + tabs.length + "  panes:" + panes;
      let right = "";
      const ev = K.at(S.events, pos, 0);
      const notifOpen = over.notifOpen || (S.overlays || []).some((o) => o.cls === "q-notif" && pos >= o.from && pos < o.to);
      if (ev && !notifOpen) right += "[" + ev + " events] ";
      const extra = K.at(S.statusExtra, pos, "");
      if (extra) right += extra;
      const mem = K.at(S.mem, pos, "");
      if (mem) right += "mem " + mem + " | ";
      right += "^T tab | ^N pane | ^W close | F1 help | ^Q quit | v" + (S.version || "1.82.1");
      return [left, right];
    }
  }

  K.World = World;
  K.world = (S) => new World(S);
  K.Pane = Pane;
  K.Win = Win;
  /** Chrome pieces without a mounted window (used by boards that lay tabs out side by side). */
  K.sidebarHTML = (world, pos, act, chrome) => Win.prototype._sidebar.call({ world }, pos, act, world.projById[world.tabById[act.tab].project], chrome || { side: true, tabs: true, status: true });
  K.tabbarHTML = (world, pos, act, projId) => Win.prototype._tabbar.call({ world }, pos, act, world.projById[projId], { tabs: true });
  /** Counts for the closing line: what the one terminal turned into. */
  K.census = (world, pos) => {
    const c = { projects: 0, tabs: 0, panes: 0, agents: 0, remote: 0, sandbox: 0 };
    const projs = world.projects(pos);
    c.projects = projs.length;
    c.remote = projs.filter((p) => p.host).length;
    for (const t of world.S.tabs) {
      if (pos < t.at || (t.until != null && pos >= t.until)) continue;
      c.tabs++;
      for (const id of world.panesOfTab(t.id, pos)) {
        c.panes++;
        const ty = world.panes[id].typeAt(pos);
        if (ty === "claude-code" || ty === "codex" || ty === "opencode") c.agents++;
        if (world.panes[id].s.sandbox) c.sandbox++;
      }
    }
    return c;
  };
export default K;
