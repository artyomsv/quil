import { AgentStatePoller } from './agentstate';
import { bannerFor, type BannerState } from './banner';
import { type AttachSizes, type Clock, Connection, type SocketLike } from './connection';
import { type QuilTestHook, shouldRegisterE2EHook } from './e2ehook';
import { type FetchLike, hasSession, postLogin } from './login';
import type { Message, PaneInfo, PaneSize, WebWelcome, WorkspaceState } from './protocol';
import { DaemonSizes, fitFontSize, gridFor, isFollower, Sizer, windowCells } from './sizing';
import { StateRev } from './staterev';
import { LOGIN_KEY, routedStorage, SafeStorage } from './storage';
import { TerminalStore } from './terminals';
import { activeProjectOf, activeTabOf, parseWorkspaceState, placedPanes, sidebarModel, tabBarModel } from './view';
import { BASE_FONT, createXtermPane, type XtermPane } from './xterm';

// How often, and how many times, layout retries while no terminal has been
// drawn yet to measure a cell from.
const CELL_RETRY_MS = 100;
const CELL_RETRIES = 20;

const browserClock: Clock = {
  setTimeout: (fn, ms) => window.setTimeout(fn, ms),
  clearTimeout: (h) => window.clearTimeout(h as number),
  now: () => performance.now(),
};

// The browser calls a WebSocket's handlers with an event argument that the
// Connection's handlers ignore or read only .data from; binaryType is set to
// arraybuffer before any frame arrives. Same runtime shape, so the cast holds.
function openSocket(): SocketLike {
  const scheme = location.protocol === 'https:' ? 'wss' : 'ws';
  return new WebSocket(`${scheme}://${location.host}/ws`) as unknown as SocketLike;
}

interface Shown {
  el: HTMLElement;
  w: number;
  h: number;
}

export type View = 'checking' | 'login' | 'workspace';

// App owns the connection and every store the page needs, and turns daemon
// messages into the reactive fields the components render. Components only
// render these fields and call the methods below.
//
// The page always shows the daemon's active tab: a click sends switch_tab or
// switch_project and the view changes when the daemon's next state says so.
// A read-only tab sends nothing at all: no input, no resize, no switch.
export class App {
  view = $state<View>('checking');
  welcome = $state.raw<WebWelcome | null>(null);
  state = $state.raw<WorkspaceState | null>(null);
  agentStates = $state.raw<Record<string, string>>({});
  banner = $state.raw<BannerState | null>(null);
  readOnly = $state(false);
  activeTabId = $derived(activeTabOf(this.state));
  activeProjectId = $derived(activeProjectOf(this.state));
  sidebar = $derived(sidebarModel(this.state, this.agentStates));
  tabBar = $derived(tabBarModel(this.state, this.agentStates));
  placed = $derived(placedPanes(this.state));
  isMaster = $derived(this.welcome !== null && this.state?.size_master === this.welcome.client_id);

  // One storage for the login form and the connection: the key one writes is
  // the key the other sends.
  private readonly storage = new SafeStorage(
    routedStorage(
      () => window.localStorage,
      () => window.sessionStorage,
    ),
  );
  private readonly fetchFn: FetchLike = (url, init) => window.fetch(url, init);
  private readonly conn: Connection;
  private readonly terminals: TerminalStore;
  private readonly sizer: Sizer;
  private readonly poller: AgentStatePoller;
  private readonly stateRev = new StateRev();
  private readonly daemonSizes = new DaemonSizes();
  private readonly xterms = new Map<string, XtermPane>();
  private readonly fonts = new Map<string, number>();
  private readonly shown = new Map<string, Shown>();
  private area = { w: 0, h: 0 };
  private win = { cols: 0, rows: 0 };
  // Pixels per cell at BASE_FONT, measured from the first drawn terminal.
  private cell: { w: number; h: number } | undefined;
  private cellRetries = 0;
  // A workspace_state has been applied on the current socket. Nothing about
  // size goes out before it: the daemon behind a new socket may not know us.
  private fresh = false;
  private focusPending = '';
  private layoutTimer: number | undefined;

  constructor() {
    const send = (m: Message): void => this.conn.send(m);
    this.terminals = new TerminalStore(
      (id) => {
        const x = createXtermPane(id);
        this.xterms.set(id, x);
        this.fonts.set(id, BASE_FONT);
        return x;
      },
      (b, e) => this.conn.processed(b, e),
      () => this.conn.epoch,
    );
    this.sizer = new Sizer(send);
    this.poller = new AgentStatePoller(send, browserClock);
    this.conn = new Connection(
      openSocket,
      this.storage,
      browserClock,
      {
        onWelcome: (w) => this.onWelcome(w),
        onMessage: (m) => this.onMessage(m),
        onOutput: (f) => this.terminals.output(f),
        onReconnecting: () => this.resetLink(),
        onClosed: (code, reason, retrying) => this.onClosed(code, reason, retrying),
      },
      () => this.attachSizes(),
    );
    if (shouldRegisterE2EHook(import.meta.env)) {
      const hook: QuilTestHook = {
        bufferText: (id) => this.xterms.get(id)?.text() ?? '',
        clientId: () => this.welcome?.client_id ?? '',
      };
      (window as unknown as { __quilTest?: QuilTestHook }).__quilTest = hook;
    }
  }

  // boot shows the workspace when the server still knows this browser's
  // session and the page still holds its key; otherwise the login form.
  async boot(): Promise<void> {
    const live = await hasSession(this.fetchFn);
    if (live && this.storage.getItem(LOGIN_KEY)) this.start();
    else this.view = 'login';
  }

  // login answers '' on success, else the message to show.
  async login(code: string): Promise<string> {
    const r = await postLogin(this.fetchFn, code.trim());
    if (!r.key) return r.error ?? '';
    this.storage.setItem(LOGIN_KEY, r.key);
    this.start();
    return '';
  }

  start(): void {
    this.view = 'workspace';
    this.banner = null;
    this.conn.start();
  }

  stop(): void {
    this.conn.stop();
    this.poller.stop();
  }

  switchTab(id: string): void {
    if (this.readOnly || !this.state || id === this.state.active_tab) return;
    this.conn.send({ type: 'switch_tab', payload: { tab_id: id } });
  }

  switchProject(id: string): void {
    if (this.readOnly || !this.state || id === '' || id === activeProjectOf(this.state)) return;
    this.conn.send({ type: 'switch_project', payload: { project_id: id } });
  }

  takeControl(): void {
    if (this.readOnly) return;
    this.conn.send({ type: 'take_control' });
  }

  input(paneId: string, s: string): void {
    if (this.readOnly) return;
    this.conn.sendInput(paneId, s);
  }

  // paneShown puts the pane's terminal into el. A read-only tab attaches no
  // input handler.
  paneShown(paneId: string, el: HTMLElement): void {
    const x = this.xterms.get(paneId);
    if (!x) return;
    x.attach(el);
    if (!this.readOnly) x.onData((d) => this.input(paneId, d));
    this.shown.set(paneId, { el, w: 0, h: 0 });
    if (this.focusPending === paneId) {
      this.focusPending = '';
      x.focus();
    }
    this.scheduleLayout();
  }

  paneHidden(paneId: string): void {
    this.shown.delete(paneId);
    this.xterms.get(paneId)?.detach();
  }

  // measure records a pane box's size in px; layout follows in one batch.
  measure(paneId: string, boxW: number, boxH: number): void {
    const sh = this.shown.get(paneId);
    if (!sh) return;
    sh.w = boxW;
    sh.h = boxH;
    this.scheduleLayout();
  }

  // measureArea records the whole pane area, the viewport this tab reports.
  measureArea(w: number, h: number): void {
    this.area = { w, h };
    this.scheduleLayout();
  }

  private onWelcome(w: WebWelcome): void {
    this.welcome = w;
    this.readOnly = w.rights === 'read-only';
    this.banner = null;
    this.fresh = false;
    for (const id of this.shown.keys()) {
      const x = this.xterms.get(id);
      if (!x) continue;
      // Rights can change across a reconnect; a handler that became
      // read-only is replaced by one that does nothing.
      x.onData(this.readOnly ? () => {} : (d) => this.input(id, d));
    }
  }

  private onMessage(m: Message): void {
    switch (m.type) {
      case 'workspace_state':
        this.applyState(m.payload);
        return;
      case 'pane_sizes': {
        const panes = (m.payload as { panes?: unknown } | null)?.panes;
        if (!Array.isArray(panes)) return;
        this.daemonSizes.fromPaneSizes(panes as PaneSize[]);
        this.scheduleLayout();
        return;
      }
      case 'list_panes_resp': {
        const panes = (m.payload as { panes?: unknown } | null)?.panes;
        const states = this.poller.response(m.id, Array.isArray(panes) ? (panes as PaneInfo[]) : []);
        this.agentStates = Object.fromEntries(states);
        return;
      }
      case 'set_active_pane': {
        // The daemon switches its active tab before it sends this, and its
        // next state shows that tab; the pane is focused once it is shown.
        const id = (m.payload as { pane_id?: unknown } | null)?.pane_id;
        if (typeof id === 'string') this.focus(id);
        return;
      }
      case 'pane_event':
        this.poller.paneEvent();
        return;
      default:
        // hello_resp, errors, highlight_pane and the rest are not shown.
        return;
    }
  }

  private applyState(payload: unknown): void {
    const s = parseWorkspaceState(payload);
    if (!s) {
      if (this.stateRev.malformed()) this.conn.send({ type: 'state_req' });
      return;
    }
    const verdict = this.stateRev.accept(s);
    if (verdict === 'drop') return;
    if (verdict === 'apply-new-run') {
      // A restarted daemon numbers sizes from zero and knows nothing we sent.
      this.daemonSizes.forget();
      this.sizer.reset();
    }
    this.daemonSizes.fromState(s.panes);
    const ids = s.panes.map((p) => p.id);
    this.terminals.sync(ids);
    const live = new Set(ids);
    for (const id of [...this.xterms.keys()]) {
      if (live.has(id)) continue;
      this.xterms.delete(id);
      this.fonts.delete(id);
      this.shown.delete(id);
    }
    this.terminals.stateApplied(verdict === 'apply-new-run');
    this.fresh = true;
    this.state = s;
    this.poller.stateApplied();
    this.scheduleLayout();
  }

  // resetLink runs on every reconnect: the socket that comes next starts with
  // no sizes, no numbering, and terminals that reset at its first state.
  private resetLink(): void {
    this.sizer.reset();
    this.daemonSizes.forget();
    this.stateRev.forget();
    this.terminals.reconnecting();
    this.poller.stop();
    this.fresh = false;
  }

  private onClosed(code: number, reason: string, retrying: boolean): void {
    if (code === 1008) {
      // The key was refused (the connection already cleared it).
      this.resetLink();
      this.state = null;
      this.welcome = null;
      this.banner = null;
      this.view = 'login';
      return;
    }
    if (!retrying) this.poller.stop();
    this.banner = bannerFor(code, reason, retrying);
  }

  private focus(paneId: string): void {
    const x = this.shown.has(paneId) ? this.xterms.get(paneId) : undefined;
    if (x) {
      this.focusPending = '';
      x.focus();
    } else {
      this.focusPending = paneId;
    }
  }

  private scheduleLayout(): void {
    if (this.layoutTimer !== undefined) return;
    this.layoutTimer = window.setTimeout(() => {
      this.layoutTimer = undefined;
      this.layout();
    }, 0);
  }

  // layout sizes every shown terminal. A follower takes the daemon's grid and
  // fits the font to the box, or draws at the box size at the base font while
  // the daemon has no size yet, and sends nothing. A confirmed master lays out
  // at the base font and sends the grids in one batch through the Sizer.
  private layout(): void {
    const s = this.state;
    const w = this.welcome;
    if (!s || !w) return;
    const cell = this.cellSize();
    const follower = isFollower(s.size_master, w.client_id, this.readOnly);
    const visible = new Map<string, { cols: number; rows: number }>();
    for (const [id, sh] of this.shown) {
      const x = this.xterms.get(id);
      if (!x || sh.w <= 0 || sh.h <= 0) continue;
      const grid = follower ? this.daemonSizes.get(id) : undefined;
      if (grid) {
        x.resize(grid.cols, grid.rows);
        const px = cell
          ? fitFontSize(grid.cols, grid.rows, sh.w, sh.h, cell.w / BASE_FONT, cell.h / BASE_FONT)
          : BASE_FONT;
        this.setFont(id, x, px);
        continue;
      }
      this.setFont(id, x, BASE_FONT);
      if (!cell) continue;
      const g = gridFor(sh.w, sh.h, cell.w, cell.h);
      x.resize(g.cols, g.rows);
      if (!follower) visible.set(id, g);
    }
    if (!cell) {
      if (this.shown.size > 0 && this.cellRetries < CELL_RETRIES) {
        this.cellRetries++;
        window.setTimeout(() => this.scheduleLayout(), CELL_RETRY_MS);
      }
      return;
    }
    if (this.area.w <= 0 || this.area.h <= 0) return;
    this.win = windowCells(this.area.w, this.area.h, cell.w, cell.h);
    if (!this.fresh) return;
    this.sizer.update({
      myId: w.client_id,
      readOnly: this.readOnly,
      sizeMaster: s.size_master,
      paintable: this.win.cols > 0,
      visible,
      masterConfirmed: s.size_master === w.client_id,
    });
    this.sizer.geometry(this.win.cols, this.win.rows);
  }

  // The cell size at BASE_FONT, from a drawn terminal still at that font.
  private cellSize(): { w: number; h: number } | undefined {
    if (this.cell) return this.cell;
    for (const id of this.shown.keys()) {
      if (this.fonts.get(id) !== BASE_FONT) continue;
      const m = this.xterms.get(id)?.measureCell();
      if (m && m.width > 0 && m.height > 0) {
        this.cell = { w: m.width, h: m.height };
        return this.cell;
      }
    }
    return undefined;
  }

  private setFont(id: string, x: XtermPane, px: number): void {
    if (this.fonts.get(id) === px) return;
    x.setFontSize(px);
    this.fonts.set(id, px);
  }

  // attach carries the first shown pane's grid and the viewport in cells;
  // 0 where nothing is measured yet, and the daemon keeps its own default.
  private attachSizes(): AttachSizes {
    let cols = 0;
    let rows = 0;
    const first = this.placed[0];
    const sh = first ? this.shown.get(first.id) : undefined;
    if (sh && this.cell && sh.w > 0 && sh.h > 0) {
      const g = gridFor(sh.w, sh.h, this.cell.w, this.cell.h);
      cols = g.cols;
      rows = g.rows;
    }
    return { cols, rows, winCols: this.win.cols, winRows: this.win.rows };
  }
}
