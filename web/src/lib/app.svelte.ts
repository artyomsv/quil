import { cwdForSplit, nextTabColor, projectRootOf, quickSplit } from './actions';
import { askedTabShown, pickActive, successorOf, unseenToClear } from './activepane';
import { AgentStatePoller } from './agentstate';
import { attachRefusedBanner, bannerFor, type BannerState, isLoginRequired } from './banner';
import {
  type ClientInfo,
  createInstance,
  deleteInstance,
  type InstanceInput,
  loadClient,
  updateInstance,
  type WriteResult,
} from './client';
import { type AttachSizes, type Clock, Connection, type SocketLike } from './connection';
import type { DialogOpen } from './dialog';
import { type QuilTestHook, shouldRegisterE2EHook } from './e2ehook';
import { type FetchLike, hasSession, postLogin, sessionGone } from './login';
import { NOT_SENT, PasteFlow } from './paste';
import type { Message, PaneInfo, PaneSize, SplitPaneReq, WebWelcome, WorkspaceState } from './protocol';
import { type Outcome, Requests, STILL_WORKING } from './requests';
import { cellFromProbe, DaemonSizes, fitFontSize, gridFor, isFollower, Sizer, windowCells } from './sizing';
import { SplitDrag } from './splitbars';
import { StateRev } from './staterev';
import { LOGIN_KEY, routedStorage, SafeStorage } from './storage';
import { TerminalStore } from './terminals';
import {
  activeProjectOf,
  activeTabOf,
  type LayoutPreview,
  parseWorkspaceState,
  placedPanes,
  sidebarModel,
  tabBarModel,
} from './view';
import { BASE_FONT, createXtermPane, FONT_FAMILY, type XtermPane } from './xterm';

// How often, and how many times, layout retries measuring a cell from a drawn
// terminal. The budget starts over on every zoom change or return to view.
const CELL_RETRY_MS = 100;
const CELL_RETRIES = 20;
const PROBE_CHARS = 32;

// probeCell measures one cell at the terminals' base font from an offscreen
// run of characters, so the page knows its window in cells before any
// terminal is drawn, and when the active tab has none.
function probeCell(): { w: number; h: number } | undefined {
  const el = document.createElement('span');
  el.setAttribute('aria-hidden', 'true');
  el.textContent = 'W'.repeat(PROBE_CHARS);
  const st = el.style;
  st.position = 'absolute';
  st.left = '-10000px';
  st.top = '0';
  st.visibility = 'hidden';
  st.whiteSpace = 'pre';
  st.lineHeight = 'normal';
  st.fontFamily = FONT_FAMILY;
  st.fontSize = `${BASE_FONT}px`;
  document.body.appendChild(el);
  const r = el.getBoundingClientRect();
  el.remove();
  return cellFromProbe(r.width, r.height, PROBE_CHARS);
}

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
//
// Editing (5b) sends id-bearing requests through Requests and shows nothing
// optimistically: a refusal shows a notice, and success shows when the
// daemon's next state does. Every editing control renders only while
// editable (full or standard rights and a live state on this socket).
export class App {
  view = $state<View>('checking');
  welcome = $state.raw<WebWelcome | null>(null);
  state = $state.raw<WorkspaceState | null>(null);
  agentStates = $state.raw<Record<string, string>>({});
  banner = $state.raw<BannerState | null>(null);
  readOnly = $state(false);
  // A short message about an edit that did not happen (or a paste that
  // stopped); it clears itself.
  notice = $state.raw<string | null>(null);
  // The pane this browser tab treats as active: the daemon has no such
  // notion per client, so it is the page's own choice.
  activePane = $state('');
  dragPreview = $state.raw<LayoutPreview | null>(null);
  // A workspace_state has been applied on the current socket.
  live = $state(false);
  // Ask dialogs live on the App, not in the component, so the pane menu and
  // a key (Task 8) open the SAME dialog. At most one of each is open.
  paneAsk = $state.raw<{ kind: 'rename' | 'close'; paneId: string } | null>(null);
  tabAsk = $state.raw<{ kind: 'rename' | 'close'; tabId: string } | null>(null);
  // GET /api/client: plugin definitions, saved instances, sandbox defaults
  // (and, Task 8, the keymap). Loaded on every dialog open.
  client = $state.raw<ClientInfo | null>(null);
  // The open create-pane dialog, if any.
  dialog = $state.raw<DialogOpen | null>(null);
  activeTabId = $derived(activeTabOf(this.state));
  activeProjectId = $derived(activeProjectOf(this.state));
  sidebar = $derived(sidebarModel(this.state, this.agentStates));
  tabBar = $derived(tabBarModel(this.state, this.agentStates));
  placed = $derived(placedPanes(this.state, this.dragPreview, this.agentStates));
  isMaster = $derived(this.welcome !== null && this.state?.size_master === this.welcome.client_id);
  editable = $derived(!this.readOnly && this.live);
  readonly requests: Requests;
  readonly drag: SplitDrag;

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
  // Panes whose grid this tab set from their box at the last layout, as a
  // size master: the daemon is about to apply that grid, so an older size in
  // a frame that crosses the resize does not overwrite it.
  private laidOut = new Set<string>();
  private area = { w: 0, h: 0 };
  private win = { cols: 0, rows: 0 };
  // Pixels per cell at BASE_FONT: from the offscreen probe, and from a drawn
  // terminal once one is, which wins when the two differ.
  private probed: { w: number; h: number } | undefined;
  private measured: { w: number; h: number } | undefined;
  private cellRetries = 0;
  // A workspace_state has been applied on the current socket. Nothing about
  // size goes out before it: the daemon behind a new socket may not know us.
  private fresh = false;
  private focusPending = '';
  private layoutTimer: number | undefined;
  private retryTimer: number | undefined;
  private unwatch: (() => void) | undefined;
  private readonly pasteFlow: PasteFlow;
  // Panes this tab has asked the daemon to clear the unseen mark of; a pane
  // leaves the set when a state shows its mark gone.
  private readonly unseenAsked = new Set<string>();
  // The last output generation seen per pane: a higher one is a restart.
  private readonly gens = new Map<string, bigint>();
  private noticeTimer: number | undefined;
  // The preparing placeholder a create was answered with, until the pane
  // that replaces it arrives.
  private followFocus = '';

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
        onOutput: (f) => {
          const last = this.gens.get(f.paneId);
          if (f.generation !== 0n) {
            if (last !== undefined && last !== 0n && f.generation > last) this.pasteFlow.paneRestarted(f.paneId);
            this.gens.set(f.paneId, f.generation);
          }
          this.terminals.output(f);
        },
        onReconnecting: () => this.resetLink(),
        onClosed: (code, reason, retrying) => this.onClosed(code, reason, retrying),
        onAttached: () => this.attached(),
        onAttachRefused: (text) => {
          this.banner = attachRefusedBanner(text);
        },
      },
      () => this.attachSizes(),
      Math.random,
      () => sessionGone(this.fetchFn),
    );
    this.requests = new Requests((m) => this.conn.trySend(m), browserClock);
    this.drag = new SplitDrag(browserClock, (tabId, layout, baseRev) =>
      this.requests.request('update_layout', { tab_id: tabId, layout, base_rev: baseRev }, { quietMs: 2000 }),
    );
    this.drag.onChange = () => {
      this.dragPreview = this.drag.preview;
    };
    this.pasteFlow = new PasteFlow({
      sendChunk: (paneId, b64) => this.requests.request('pane_input', { pane_id: paneId, data: b64 }),
      sendKeys: (paneId, data) => this.conn.sendInput(paneId, data),
      notice: (t) => this.showNotice(t),
      sleep: (ms) => new Promise((r) => window.setTimeout(r, ms)),
    });
    if (shouldRegisterE2EHook(import.meta.env)) {
      const hook: QuilTestHook = {
        bufferText: (id) => this.xterms.get(id)?.text() ?? '',
        screenLine: (id, row) => this.xterms.get(id)?.screenLine(row) ?? '',
        clientId: () => this.welcome?.client_id ?? '',
        paste: (id, text) => this.paste(id, text),
        activePane: () => this.activePane,
      };
      (window as unknown as { __quilTest?: QuilTestHook }).__quilTest = hook;
    }
  }

  // boot shows the workspace when the server still knows this browser's
  // session and the page still holds its key; otherwise the login form.
  async boot(): Promise<void> {
    this.watchDisplay();
    this.probed = probeCell();
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
    if (this.layoutTimer !== undefined) window.clearTimeout(this.layoutTimer);
    if (this.retryTimer !== undefined) window.clearTimeout(this.retryTimer);
    this.layoutTimer = undefined;
    this.retryTimer = undefined;
    this.unwatch?.();
    this.unwatch = undefined;
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

  // input goes through the paste flow: typed input goes out at once, unless
  // a paste is running, which it waits behind.
  input(paneId: string, s: string): void {
    if (this.readOnly) return;
    this.pasteFlow.input(paneId, s);
  }

  // paneShown puts the pane's terminal into el. A read-only tab attaches no
  // input handler.
  paneShown(paneId: string, el: HTMLElement): void {
    const x = this.xterms.get(paneId);
    if (!x) return;
    x.attach(el);
    x.onFocus(() => this.setActivePane(paneId));
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
    if (this.readOnly) this.closeAsks();
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
    if (this.requests.answer(m)) return;
    switch (m.type) {
      case 'workspace_state':
        this.applyState(m.payload);
        return;
      case 'pane_sizes': {
        const panes = (m.payload as { panes?: unknown } | null)?.panes;
        if (!Array.isArray(panes)) return;
        this.daemonSizes.fromPaneSizes(panes as PaneSize[]);
        this.applyDaemonGrids();
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
      this.laidOut.delete(id);
    }
    this.terminals.stateApplied(verdict === 'apply-new-run');
    this.fresh = true;
    const prev = this.state;
    this.state = s;
    this.live = true;
    for (const t of s.tabs) this.drag.stateArrived(t.id, t.layout_rev);
    const placed = placedPanes(s).map((p) => p.id);
    // A replaced active pane hands the part to the pane in its slot.
    this.activePane = pickActive(successorOf(prev, s, this.activePane) || this.activePane, placed);
    // A preparing worktree's placeholder was focused at the create; the pane
    // that replaces it takes the focus.
    if (this.followFocus !== '') {
      const next = successorOf(prev, s, this.followFocus);
      if (next !== '') this.focus(next);
      if (next !== '' || !s.panes.some((p) => p.id === this.followFocus)) this.followFocus = '';
    }
    // A dialog about a pane or tab this state no longer shows closes.
    if (this.paneAsk && !placed.includes(this.paneAsk.paneId)) this.paneAsk = null;
    if (this.tabAsk && !askedTabShown(s, this.tabAsk.tabId)) this.tabAsk = null;
    for (const id of [...this.unseenAsked]) {
      if (!s.panes.find((p) => p.id === id)?.unseen) this.unseenAsked.delete(id);
    }
    this.clearUnseen();
    this.applyDaemonGrids();
    // A pane that this state does not place (an overlay, another tab) will
    // not be shown, so a focus waiting for it is dropped.
    if (this.focusPending !== '' && !placedPanes(s).some((p) => p.id === this.focusPending)) {
      this.focusPending = '';
    }
    this.poller.stateApplied();
    this.scheduleLayout();
  }

  // applyDaemonGrids gives every terminal, shown or not, the grid the daemon
  // holds for its pane. It runs as each state or pane_sizes is handled, so
  // the resize is queued behind the output before it and ahead of the output
  // after it: a hidden pane parses cursor moves at its real size, which no
  // later resize could repair. A size master's own panes keep the grid its
  // last layout gave them; the daemon is applying that one.
  private applyDaemonGrids(): void {
    const s = this.state;
    const w = this.welcome;
    const follower = !s || !w || isFollower(s.size_master, w.client_id, this.readOnly);
    for (const id of this.xterms.keys()) {
      if (!follower && this.laidOut.has(id)) continue;
      const g = this.daemonSizes.get(id);
      if (g) this.terminals.resize(id, g.cols, g.rows);
    }
  }

  // resetLink runs on every reconnect: the socket that comes next starts with
  // no sizes, no numbering, and terminals that reset at its first state.
  private resetLink(): void {
    this.sizer.reset();
    this.daemonSizes.forget();
    this.stateRev.forget();
    this.terminals.reconnecting();
    this.poller.stop();
    this.laidOut.clear();
    this.fresh = false;
    this.linkLost();
  }

  // linkLost ends everything that belongs to the socket that went away: its
  // requests, a running paste, a border drag, and the editing controls until
  // the next socket's first state.
  private linkLost(): void {
    this.live = false;
    this.closeAsks();
    this.requests.reconnecting();
    this.pasteFlow.reconnecting();
    this.drag.linkLost();
    this.gens.clear();
  }

  // closeAsks closes an open rename or close dialog: what it would send can
  // no longer go out (no live state, or no rights).
  private closeAsks(): void {
    this.paneAsk = null;
    this.tabAsk = null;
    this.dialog = null;
  }

  private onClosed(code: number, reason: string, retrying: boolean): void {
    if (isLoginRequired(code, reason)) {
      // The key was refused (the connection already cleared it).
      this.resetLink();
      this.state = null;
      this.welcome = null;
      this.banner = null;
      this.view = 'login';
      return;
    }
    if (!retrying) {
      this.poller.stop();
      this.linkLost();
    }
    this.banner = bannerFor(code, reason, retrying);
  }

  showNotice(text: string): void {
    this.notice = text;
    if (this.noticeTimer !== undefined) window.clearTimeout(this.noticeTimer);
    this.noticeTimer = window.setTimeout(() => {
      this.notice = null;
      this.noticeTimer = undefined;
    }, 6000);
  }

  // act sends an id-bearing request and shows the refusal; no optimistic UI.
  private async act(type: string, payload: unknown, timeoutText?: string): Promise<Outcome> {
    if (!this.editable) {
      const error = this.readOnly ? 'This page is read-only' : 'Not connected — nothing was changed';
      this.showNotice(error);
      return { ok: false, code: 'offline', error };
    }
    const out = await this.requests.request(type, payload, { timeoutText });
    if (!out.ok) this.showNotice(out.error);
    return out;
  }

  setActivePane(paneId: string): void {
    this.activePane = paneId;
    this.clearUnseen();
  }

  private clearUnseen(): void {
    const s = this.state;
    if (!s || !this.editable) return;
    const id = unseenToClear(s, this.activePane, this.unseenAsked);
    if (!id) return;
    this.unseenAsked.add(id);
    void this.requests.request('update_pane', { pane_id: id, unseen: false });
  }

  // sendSplit sends split_pane_req and makes the answered pane active. A
  // worktree create answers preparing with its placeholder; the final pane
  // replaces it in a later state.
  async sendSplit(req: SplitPaneReq): Promise<Outcome> {
    const out = await this.act('split_pane_req', req, STILL_WORKING);
    const id = (out.ok ? (out.reply?.payload as { pane_id?: string } | undefined)?.pane_id : undefined) ?? '';
    if (id) {
      this.activePane = id;
      this.focus(id);
      if ((out.ok ? (out.reply?.payload as { preparing?: boolean } | undefined)?.preparing : false) === true) this.followFocus = id;
    }
    return out;
  }

  splitQuick(paneId: string, placement: 'right' | 'below'): void {
    if (this.state) void this.sendSplit(quickSplit(this.state, paneId, placement));
  }

  closePane(paneId: string, removeWorktree: boolean): void {
    void this.act('destroy_pane_req', { pane_id: paneId, remove_worktree: removeWorktree });
  }

  renamePane(paneId: string, name: string): void {
    void this.act('update_pane', { pane_id: paneId, name });
  }

  setMuted(paneId: string, muted: boolean): void {
    void this.act('update_pane', { pane_id: paneId, muted });
  }

  restartPane(paneId: string): void {
    void this.act('restart_pane_req', { pane_id: paneId });
  }

  movePane(paneId: string, tabId: string): void {
    void this.act('move_pane', { pane_id: paneId, tab_id: tabId });
  }

  renameTab(tabId: string, name: string): void {
    void this.act('update_tab', { tab_id: tabId, name });
  }

  closeTab(tabId: string): void {
    void this.act('destroy_tab', { tab_id: tabId });
  }

  // An empty colour is the default: update_tab says so with clear_color,
  // since an empty color field means "no change".
  setTabColor(tabId: string, color: string): void {
    void this.act('update_tab', color === '' ? { tab_id: tabId, clear_color: true } : { tab_id: tabId, color });
  }

  paste(paneId: string, text: string): void {
    if (this.editable) this.pasteFlow.input(paneId, text);
    else this.showNotice(this.readOnly ? 'This page is read-only' : `${NOT_SENT}: not connected`);
  }

  askClosePane(paneId: string): void {
    if (this.editable) this.paneAsk = { kind: 'close', paneId };
  }

  startRenamePane(paneId: string): void {
    if (this.editable) this.paneAsk = { kind: 'rename', paneId };
  }

  askCloseTab(tabId: string): void {
    if (this.editable) this.tabAsk = { kind: 'close', tabId };
  }

  startRenameTab(tabId: string): void {
    if (this.editable) this.tabAsk = { kind: 'rename', tabId };
  }

  // cycleTabColor moves the tab to the next TAB_COLORS entry (wrapping to
  // "none"), as the TUI's tab-color key does.
  cycleTabColor(tabId: string): void {
    this.setTabColor(tabId, nextTabColor(this.state?.tabs.find((t) => t.id === tabId)?.color ?? ''));
  }

  // refreshClient loads GET /api/client into `client`. The dialog calls it
  // on every open (instances and plugin files may have changed); Task 8 also
  // calls it once per attach for the keymap. False after a shown error.
  async refreshClient(): Promise<boolean> {
    const r = await loadClient(this.fetchFn, this.storage.getItem(LOGIN_KEY) ?? '');
    if ('error' in r) {
      this.showNotice(r.error);
      return false;
    }
    this.client = r.info;
    this.clientLoaded(r.info);
    return true;
  }

  // clientLoaded runs after every successful load. Task 8 builds the key
  // tables and the notification filter from it here.
  private clientLoaded(_info: ClientInfo): void {}

  // openDialog opens the create-pane dialog once /api/client has answered;
  // nothing opens on a read-only or not-live page.
  async openDialog(open: DialogOpen): Promise<void> {
    if (!this.editable) return;
    if (!(await this.refreshClient())) return;
    // The page may have lost its link or its rights while the answer came.
    if (this.editable) this.dialog = open;
  }

  // openCreate opens the dialog for the active tab: a new pane next to the
  // active pane, a replace of it, or a new tab. Menus and keys (Task 8) use
  // it. The folder starts at the project root (spec §5.2), else the active
  // pane's folder.
  openCreate(mode: DialogOpen['mode']): void {
    const s = this.state;
    if (!s) return;
    const target = mode === 'new_tab' ? '' : this.activePane;
    void this.openDialog({
      mode,
      targetPaneId: target,
      tabId: s.active_tab,
      projectId: this.activeProjectId,
      defaultCwd: projectRootOf(s, s.active_tab) || (this.activePane ? cwdForSplit(s, this.activePane) : ''),
    });
  }

  closeDialog(): void {
    this.dialog = null;
    if (this.activePane) this.focus(this.activePane);
  }

  // daemonList runs one dialog RPC (plugin_list_req, browse_dir_req,
  // git_repos_req, kube_ctx_req, claude_sessions_req, worktree_list_req,
  // sandbox_cap_req, dirs_exist_req) through Requests.
  daemonList(type: string, payload: unknown): Promise<Outcome> {
    return this.requests.request(type, payload);
  }

  // instancesApi writes this machine's instances.json through quil web.
  instancesApi(): {
    create: (i: InstanceInput) => Promise<WriteResult>;
    update: (i: InstanceInput) => Promise<WriteResult>;
    remove: (plugin: string, id: string) => Promise<WriteResult>;
  } {
    const key = this.storage.getItem(LOGIN_KEY) ?? '';
    return {
      create: (i) => createInstance(this.fetchFn, key, i),
      update: (i) => updateInstance(this.fetchFn, key, i),
      remove: (plugin, id) => deleteInstance(this.fetchFn, key, plugin, id),
    };
  }

  // attached runs once per (re)attach, after the attach's own state is
  // applied (ConnectionEvents.onAttached). Task 8 adds the notification
  // store's rebuild as its body.
  private attached(): void {}

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
  // Every grid change goes through the TerminalStore, in order with output.
  private layout(): void {
    const cell = this.cellSize();
    // The window in cells needs only the pane area and a cell, so attach can
    // carry it before any state or terminal exists.
    if (cell && this.area.w > 0 && this.area.h > 0) this.win = windowCells(this.area.w, this.area.h, cell.w, cell.h);
    const s = this.state;
    const w = this.welcome;
    if (!s || !w) return;
    const follower = isFollower(s.size_master, w.client_id, this.readOnly);
    const visible = new Map<string, { cols: number; rows: number }>();
    const laidOut = new Set<string>();
    for (const [id, sh] of this.shown) {
      const x = this.xterms.get(id);
      if (!x || sh.w <= 0 || sh.h <= 0) continue;
      const grid = follower ? this.daemonSizes.get(id) : undefined;
      if (grid) {
        this.terminals.resize(id, grid.cols, grid.rows);
        const px = cell
          ? fitFontSize(grid.cols, grid.rows, sh.w, sh.h, cell.w / BASE_FONT, cell.h / BASE_FONT)
          : BASE_FONT;
        this.setFont(id, x, px);
        continue;
      }
      this.setFont(id, x, BASE_FONT);
      if (!cell) continue;
      const g = gridFor(sh.w, sh.h, cell.w, cell.h);
      this.terminals.resize(id, g.cols, g.rows);
      if (!follower) {
        visible.set(id, g);
        laidOut.add(id);
      }
    }
    this.laidOut = laidOut;
    // A pane this tab no longer lays out takes the daemon's grid again.
    this.applyDaemonGrids();
    if (!this.measured && this.shown.size > 0 && this.cellRetries < CELL_RETRIES && this.retryTimer === undefined) {
      this.cellRetries++;
      this.retryTimer = window.setTimeout(() => {
        this.retryTimer = undefined;
        this.scheduleLayout();
      }, CELL_RETRY_MS);
    }
    if (!cell || this.area.w <= 0 || this.area.h <= 0 || !this.fresh) return;
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

  // The cell size at BASE_FONT: a drawn terminal still at that font refines
  // the probe's answer once, since xterm's own metrics are what it draws with.
  private cellSize(): { w: number; h: number } | undefined {
    if (this.measured) return this.measured;
    for (const id of this.shown.keys()) {
      if (this.fonts.get(id) !== BASE_FONT) continue;
      const m = this.xterms.get(id)?.measureCell();
      if (m && m.width > 0 && m.height > 0) {
        this.measured = { w: m.width, h: m.height };
        return this.measured;
      }
    }
    return this.probed;
  }

  // watchDisplay re-measures the cell when the zoom (devicePixelRatio)
  // changes and when the page comes back into view.
  private watchDisplay(): void {
    if (this.unwatch) return;
    let query: MediaQueryList | undefined;
    const onZoom = (): void => {
      arm();
      this.remeasure();
    };
    const arm = (): void => {
      query?.removeEventListener('change', onZoom);
      query = window.matchMedia(`(resolution: ${window.devicePixelRatio}dppx)`);
      query.addEventListener('change', onZoom);
    };
    const onVisible = (): void => {
      if (document.visibilityState === 'visible') this.remeasure();
    };
    arm();
    document.addEventListener('visibilitychange', onVisible);
    this.unwatch = () => {
      query?.removeEventListener('change', onZoom);
      document.removeEventListener('visibilitychange', onVisible);
    };
  }

  private remeasure(): void {
    this.measured = undefined;
    this.probed = probeCell() ?? this.probed;
    this.cellRetries = 0;
    this.scheduleLayout();
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
    const cell = this.measured ?? this.probed;
    if (sh && cell && sh.w > 0 && sh.h > 0) {
      const g = gridFor(sh.w, sh.h, cell.w, cell.h);
      cols = g.cols;
      rows = g.rows;
    }
    return { cols, rows, winCols: this.win.cols, winRows: this.win.rows };
  }
}
