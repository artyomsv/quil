import { cwdForSplit, nextTabColor, projectRootOf, quickSplit, splitAnswer } from './actions';
import { askedTabShown, jumpStep, type PendingJump, pickActive, resolveJump, SeenMarks, successorOf, UnseenAsks } from './activepane';
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
import { NATIVE, TUI_ONLY, VIEW_ONLY } from './keys/actions';
import { IS_MAC, isEditable } from './keys/chord';
import { buildTables, KeyEngine, type WebKeymap } from './keys/engine';
import { keyFor, keyTarget } from './keys/labels';
import { type Dir, neighbour } from './keys/nav';
import { type FetchLike, hasSession, postLogin, sessionGone } from './login';
import { NotificationStore, type NotifyInfo, type PaneEvent, parsePaneEvent } from './notifications';
import { OverlayClaim, type OverlayInfo, type OverlayKind, overlayOf, overlayRepoChoice, overlayToggle } from './overlay';
import { HISTORY_TIMEOUT_MS } from './history';
import { NOTE_LOAD_TIMEOUT_MS, type NoteIO, NoteSession } from './notes';
import { buildPalette, type PaletteRow } from './palette';
import { GroupRenames, newProjectPlan } from './projects';
import { type Panel, panelTargetGone } from './panels';
import { REPORT_TIMEOUT_MS } from './processes';
import { NOT_SENT, PasteFlow } from './paste';
import type {
  CreateFromTemplateReq,
  CreateFromTemplateResp,
  CreateProjectResp,
  Message,
  PaneInfo,
  PaneSize,
  SplitPaneReq,
  VersionResp,
  WebWelcome,
  WorkspaceState,
} from './protocol';
import { sanitizeRemoteText } from './sanitize';
import { TEMPLATE_TOO_OLD, templateGate } from './template';
import { type Outcome, Requests, STILL_WORKING } from './requests';
import { type MsgClass, refusal, type Rights, rightsOf } from './rights';
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
// The sidebar's collapsed group names, a JSON list in local storage.
const COLLAPSED_KEY = 'quil.groups.collapsed';
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
  // a key open the SAME dialog. At most one of each is open.
  paneAsk = $state.raw<{ kind: 'rename' | 'close'; paneId: string } | null>(null);
  tabAsk = $state.raw<{ kind: 'rename' | 'close'; tabId: string } | null>(null);
  // GET /api/client: plugin definitions, saved instances, sandbox defaults
  // and the keymap. Loaded on every dialog open and every attach.
  client = $state.raw<ClientInfo | null>(null);
  // The open create-pane dialog, if any.
  dialog = $state.raw<DialogOpen | null>(null);
  // The notification list, the key list, the project sidebar, and
  // the pending-prefix line.
  notifyOpen = $state(false);
  // Bumped by notification.focus; the panel focuses its list.
  notifyFocus = $state(0);
  keyListOpen = $state(false);
  // The one 5c dialog open (palette, F1 menu, history, project forms, …).
  panel = $state.raw<Panel | null>(null);
  rights = $derived<Rights>(rightsOf(this.welcome));
  // The open notes editor. Not a Panel: it survives a lost link (spec §6).
  // notesTick is bumped on every change of the session's plain fields.
  notes = $state.raw<NoteSession | null>(null);
  // From version_req at each attach: the gated requests the daemon handles
  // (null = not asked or no answer) and its version ('' = unknown).
  daemonRequests = $state.raw<string[] | null>(null);
  daemonVersion = $state('');
  // A stage_update_req is out (one at a time).
  stageBusy = $state(false);
  notesTick = $state(0);
  sidebarOpen = $state(true);
  keyHint = $state('');
  keymap = $state.raw<WebKeymap | null>(null);
  events = $state.raw<PaneEvent[]>([]);
  // Panes a pane_seen cleared that no later state has settled (R-7).
  seenPanes = $state.raw<ReadonlySet<string>>(new Set());
  private readonly seenMarks = new SeenMarks();
  // Per tab: the overlay pane this page shows. Never one this page did not
  // ask for: another client swapping the slot to another tool hides it here.
  overlayShown = $state.raw<Record<string, string>>({});
  // An open repository picker for an overlay (Alt+G / Alt+D with several
  // repositories under the active pane's folder).
  repoPick = $state.raw<{ tab: string; kind: OverlayKind; repos: string[]; hide: string } | null>(null);
  activeTabId = $derived(activeTabOf(this.state));
  activeProjectId = $derived(activeProjectOf(this.state));
  sidebar = $derived(sidebarModel(this.state, this.agentStates, this.seenPanes));
  tabBar = $derived(tabBarModel(this.state, this.agentStates, this.seenPanes));
  // The active tab's overlay while this page shows it.
  overlay = $derived(this.overlayVisibleFor(this.state, this.activeTabId, this.overlayShown));
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
  // The sidebar's collapsed groups, per browser (a view choice, as in the
  // TUI, where it is per client).
  collapsedGroups = $state.raw<ReadonlySet<string>>(this.loadCollapsed());
  // One group rename in flight per group; groupBusy mirrors it for the UI.
  readonly groupRenames = new GroupRenames();
  groupBusy = $state.raw<ReadonlySet<string>>(new Set());
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
  private readonly unseenAsked = new UnseenAsks();
  // The last output generation seen per pane: a higher one is a restart.
  private readonly gens = new Map<string, bigint>();
  private noticeTimer: number | undefined;
  // The preparing placeholder a create was answered with, until the pane
  // that replaces it arrives.
  private followFocus = '';
  private keys: KeyEngine | null = null;
  // Tabs with an overlay create in flight.
  private readonly overlayBusy = new Set<string>();
  private readonly store = new NotificationStore(null);
  // What this socket last told the daemon it shows (overlay_visible).
  private readonly overlayClaim = new OverlayClaim();
  // A notification jump to another tab's pane, until the state showing that
  // tab arrives.
  private pendingJump: PendingJump | null = null;

  constructor() {
    this.groupRenames.onChange = () => (this.groupBusy = this.groupRenames.inFlight);
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
        keymapPreset: () => this.keymap?.preset ?? '',
      };
      (window as unknown as { __quilTest?: QuilTestHook }).__quilTest = hook;
    }
  }

  // boot shows the workspace when the server still knows this browser's
  // session and the page still holds its key; otherwise the login form.
  async boot(): Promise<void> {
    // Capture phase: the engine sees a key before xterm's textarea does.
    document.addEventListener('keydown', this.onKeyDown, true);
    window.addEventListener('blur', this.onBlur);
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
    document.removeEventListener('keydown', this.onKeyDown, true);
    window.removeEventListener('blur', this.onBlur);
    this.keys?.cancel();
    this.conn.stop();
    this.poller.stop();
    if (this.layoutTimer !== undefined) window.clearTimeout(this.layoutTimer);
    if (this.retryTimer !== undefined) window.clearTimeout(this.retryTimer);
    this.layoutTimer = undefined;
    this.retryTimer = undefined;
    this.unwatch?.();
    this.unwatch = undefined;
  }

  // A tab the user picks ends a notification jump still waiting for its own
  // tab (jumpToEvent sets the jump again after its own switch).
  switchTab(id: string): void {
    this.pendingJump = null;
    if (this.readOnly || !this.state || id === this.state.active_tab) return;
    this.conn.send({ type: 'switch_tab', payload: { tab_id: id } });
  }

  switchProject(id: string): void {
    this.pendingJump = null;
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
      case 'pane_event': {
        this.poller.paneEvent();
        const e = parsePaneEvent(m.payload);
        if (e && this.store.add(e, this.skipCtx())) this.events = this.store.visible();
        return;
      }
      case 'event_dismissed': {
        const id = (m.payload as { event_id?: unknown } | null)?.event_id;
        if (typeof id === 'string') {
          this.store.dismiss(id);
          this.events = this.store.visible();
        }
        return;
      }
      case 'pane_seen': {
        const p = m.payload as { pane_id?: unknown; rev?: unknown } | null;
        const id = p?.pane_id;
        if (typeof id !== 'string') return;
        if (this.seenMarks.seen(id, typeof p?.rev === 'number' ? p.rev : undefined)) this.seenPanes = this.seenMarks.ids();
        return;
      }
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
    for (const id of [...this.gens.keys()]) if (!live.has(id)) this.gens.delete(id);
    this.terminals.stateApplied(verdict === 'apply-new-run');
    this.fresh = true;
    const prev = this.state;
    this.state = s;
    this.live = true;
    // A pending prefix belongs to the tab it was typed in.
    if (prev && prev.active_tab !== s.active_tab) this.keys?.cancel();
    // A state built after a clear carries the daemon's unseen value (R-7);
    // one built before it may still carry the mark (SeenMarks).
    if (this.seenMarks.stateApplied(s.rev, verdict === 'apply-new-run')) this.seenPanes = this.seenMarks.ids();
    // An overlay that left the state is no longer shown here (spec §7).
    const shown: Record<string, string> = {};
    for (const [tab, id] of Object.entries(this.overlayShown)) if (overlayOf(s, tab)?.id === id) shown[tab] = id;
    if (Object.keys(shown).length !== Object.keys(this.overlayShown).length) this.overlayShown = shown;
    // A tab or project change takes one overlay off the screen and may put
    // another on; the first state on a new socket claims it again.
    this.reportOverlay();
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
    // A repository picker belongs to the tab it was opened in.
    if (this.repoPick && this.repoPick.tab !== s.active_tab) this.repoPick = null;
    // A 5c dialog about a pane, tab, project or group this state no longer
    // holds closes, with a notice (spec §6).
    if (this.panel && panelTargetGone(this.panel, s)) {
      this.panel = null;
      this.showNotice('Closed: what it was about is gone');
    }
    // The open note follows its pane's note_rev; a closed pane keeps the
    // editor open with its text (spec §4.2).
    if (this.notes) {
      const n = this.notes;
      const p = s.panes.find((x) => x.id === n.paneId);
      if (!p) n.paneClosed();
      else {
        n.linkBack();
        n.frameRev(p.note_rev);
      }
    }
    // A notification jump finishes once the state shows its tab.
    const jump = resolveJump(this.pendingJump, s, placed, browserClock.now());
    this.pendingJump = jump.keep;
    if (jump.activate !== '') {
      this.setActivePane(jump.activate);
      this.focus(jump.activate);
    }
    this.unseenAsked.stateApplied(s);
    this.clearUnseen();
    this.applyDaemonGrids();
    // A pane that this state does not place (another tab, an overlay this
    // page does not show) will not be shown, so a focus waiting for it is
    // dropped.
    if (
      this.focusPending !== '' &&
      this.focusPending !== this.overlay?.id &&
      !placedPanes(s).some((p) => p.id === this.focusPending)
    ) {
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
    // Before the requests fail: the editor marks its own save abandoned
    // rather than reading "connection lost" as a refusal.
    this.notes?.linkLost();
    this.requests.reconnecting();
    this.pasteFlow.reconnecting();
    this.drag.linkLost();
    this.gens.clear();
    // Asks the old socket carried get no answer now; the next state asks
    // again for a mark still set.
    this.unseenAsked.reset();
    this.pendingJump = null;
    // The daemon drops this socket's overlay claims with its connection.
    this.overlayClaim.forget();
  }

  // closeAsks closes an open rename or close dialog: what it would send can
  // no longer go out (no live state, or no rights).
  private closeAsks(): void {
    this.paneAsk = null;
    this.tabAsk = null;
    this.dialog = null;
    this.repoPick = null;
    // Every 5c Panel closes too; the notes editor is not a Panel (spec §6).
    this.panel = null;
  }

  // closePaneAsk and closeTabAsk end a rename or close dialog, by its own
  // buttons or Escape, and give the keyboard back to the terminal (spec §5.5).
  closePaneAsk(): void {
    this.paneAsk = null;
    this.focusActiveSoon();
  }

  closeTabAsk(): void {
    this.tabAsk = null;
    this.focusActiveSoon();
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
    if (paneId !== this.activePane) this.keys?.cancel();
    // An overlay takes the keys while it is shown, but it is never the
    // active pane: splits, closes and the overlay's own repo follow the pane
    // under it.
    if (this.state?.panes.some((p) => p.id === paneId && p.overlay)) return;
    this.activePane = paneId;
    this.clearUnseen();
  }

  private clearUnseen(): void {
    const s = this.state;
    if (!s || !this.editable) return;
    const id = this.unseenAsked.next(s, this.activePane);
    if (!id) return;
    // A refused or unanswered ask is forgotten, so the next state asks again.
    void this.requests.request('update_pane', { pane_id: id, unseen: false }).then((o) => this.unseenAsked.answered(id, o.ok));
  }

  // sendSplit sends split_pane_req and makes the answered pane active. A
  // worktree create answers preparing with its placeholder; the final pane
  // replaces it in a later state. A pane that exists but did not start is
  // done, not refused: its notice is shown and the dialog closes.
  async sendSplit(req: SplitPaneReq): Promise<Outcome> {
    const out = await this.act('split_pane_req', req, STILL_WORKING);
    const a = splitAnswer(out);
    if (a?.notice) this.showNotice(a.notice);
    if (a && a.paneId) {
      this.activePane = a.paneId;
      this.focus(a.paneId);
      if (a.preparing) this.followFocus = a.paneId;
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

  // A dialog opening drops a pending prefix, as openKeyList does.
  askClosePane(paneId: string): void {
    this.keys?.cancel();
    if (this.editable) this.paneAsk = { kind: 'close', paneId };
  }

  startRenamePane(paneId: string): void {
    this.keys?.cancel();
    if (this.editable) this.paneAsk = { kind: 'rename', paneId };
  }

  askCloseTab(tabId: string): void {
    this.keys?.cancel();
    if (this.editable) this.tabAsk = { kind: 'close', tabId };
  }

  startRenameTab(tabId: string): void {
    this.keys?.cancel();
    if (this.editable) this.tabAsk = { kind: 'rename', tabId };
  }

  // cycleTabColor moves the tab to the next TAB_COLORS entry (wrapping to
  // "none"), as the TUI's tab-color key does.
  cycleTabColor(tabId: string): void {
    this.setTabColor(tabId, nextTabColor(this.state?.tabs.find((t) => t.id === tabId)?.color ?? ''));
  }

  // refreshClient loads GET /api/client into `client`. The dialog calls it
  // on every open (instances and plugin files may have changed), and attached
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

  // clientLoaded runs after every successful load: it installs the key
  // tables and the notification filter.
  private clientLoaded(info: ClientInfo): void {
    this.keysFrom(info.keymap, info.notifications);
  }

  // keysFrom installs the keymap and notification filter /api/client sent.
  keysFrom(km: WebKeymap | null, notify: NotifyInfo | null): void {
    this.store.setInfo(notify);
    this.events = this.store.visible();
    if (!km) return;
    this.keymap = km;
    if (this.keys) this.keys.setKeymap(km);
    else this.keys = new KeyEngine(buildTables(km), km.timeout_ms, browserClock, () => this.updateKeyHint());
    this.updateKeyHint();
  }

  // The line under the tab bar: a dropped sequence's notice, else the
  // prefix typed so far.
  private updateKeyHint(): void {
    const k = this.keys;
    this.keyHint = !k ? '' : k.hint || (k.pending.length > 0 ? `${k.pending.join(' ')} …` : '');
  }

  private overlayVisibleFor(s: WorkspaceState | null, tab: string, shown: Record<string, string>): OverlayInfo | null {
    const o = overlayOf(s, tab);
    return o && shown[tab] === o.id ? o : null;
  }

  // The pane keys go to: the shown overlay, else the active pane.
  private keyPane(): string {
    return keyTarget(this.overlay?.id, this.activePane).pane;
  }

  // keyFor is the key that runs an action in this browser, for menus.
  keyFor(id: string): string {
    return keyFor(this.keymap, id);
  }

  private rawKeysOf(paneId: string): ReadonlySet<string> {
    const type = this.state?.panes.find((p) => p.id === paneId)?.type || 'terminal';
    const p = this.client?.plugins.find((x) => x.name === type);
    return new Set(p?.raw_keys ?? []);
  }

  // onKeyDown is the page's one key listener, in the capture phase so it
  // runs before the focused terminal sees the key. A dialog, a menu, the key
  // list or a form field owns the keyboard while it is open.
  private readonly onKeyDown = (e: KeyboardEvent): void => {
    if (this.view !== 'workspace' || !this.keys) return;
    const modal = this.keyListOpen || document.querySelector('[data-modal]') !== null || isEditable(e.target);
    const target = keyTarget(this.overlay?.id, this.activePane);
    const ev = {
      key: e.key,
      code: e.code,
      ctrlKey: e.ctrlKey,
      altKey: e.altKey,
      shiftKey: e.shiftKey,
      metaKey: e.metaKey,
      isComposing: e.isComposing,
      altGraph: e.getModifierState('AltGraph'),
      mac: IS_MAC,
    };
    const d = this.keys.handle(ev, {
      modalOpen: modal,
      activePaneId: target.pane,
      rawKeys: this.rawKeysOf(target.pane),
      overlay: target.overlay,
    });
    this.updateKeyHint();
    if (d.kind === 'pass') return;
    if (d.kind === 'action' && NATIVE.has(d.id)) return;
    e.preventDefault();
    e.stopPropagation();
    if (d.kind === 'action') this.runAction(d.id);
    else if (d.kind === 'builtin') this.runBuiltin(d.id);
  };

  private readonly onBlur = (): void => this.keys?.cancel();

  private runBuiltin(id: string): void {
    if (id === 'help') this.openPanel({ kind: 'help' });
    else if (id === 'new_pane') {
      if (this.readOnly) this.showNotice('read-only connection — that action is disabled');
      else this.openCreate('pane');
    }
  }

  openKeyList(): void {
    this.keys?.cancel();
    this.keyListOpen = true;
  }

  closeKeyList(): void {
    this.keyListOpen = false;
    this.focusActive();
  }

  // openPanel shows one 5c dialog; a pending key prefix is dropped first.
  openPanel(p: Panel): void {
    this.keys?.cancel();
    this.panel = p;
  }

  closePanel(): void {
    this.panel = null;
    this.focusActiveSoon();
  }

  // fire sends a message the daemon never answers.
  fire(type: string, payload: unknown): void {
    if (!this.requests.fire(type, payload)) this.showNotice('Not connected — nothing was sent');
  }

  // refusalFor is why a control of class c is greyed now, '' when it may run.
  refusalFor(c: MsgClass): string {
    return refusal(this.rights, c, this.live);
  }

  // paletteExtraRows are the rows later screens add to the palette's Tabs,
  // Projects, Pane and System sections, in that order.
  paletteExtraRows(): PaletteRow[][] {
    const noPane = this.activePane === '' ? 'no active pane' : '';
    const pane: PaletteRow[] = [
      {
        label: 'Toggle notes',
        detail: this.keyFor('pane.notes_toggle'),
        keywords: ['note', 'notes', 'editor'],
        run: { action: 'pane.notes_toggle' },
        disabled: noPane,
      },
      {
        label: 'Input history',
        detail: this.keyFor('pane.command_history'),
        keywords: ['history', 'prompts', 'input'],
        run: { action: 'pane.command_history' },
        disabled: this.refusalFor('act') || noPane,
      },
    ];
    const act = this.refusalFor('act');
    const projectCount = this.state?.projects.length ?? 0;
    const tabs: PaletteRow[] = [
      {
        label: 'New from template',
        keywords: ['template', 'workspace', 'agents'],
        run: { panel: { kind: 'template' } },
        disabled:
          act ||
          templateGate(this.daemonRequests) ||
          (this.client?.templates_error ?? '') ||
          ((this.client?.templates.length ?? 0) === 0 ? 'no templates' : ''),
      },
      {
        label: 'Move tab to project…',
        keywords: ['tab', 'move', 'project'],
        run: { panel: { kind: 'move_tab', tabId: this.activeTabId } },
        disabled: act || (this.activeTabId === '' ? 'no active tab' : projectCount < 2 ? 'no other project' : ''),
      },
    ];
    const noProject = this.activeProjectId === '' ? 'no active project' : '';
    const projects: PaletteRow[] = [
      {
        label: 'New project',
        detail: this.keyFor('project.new'),
        keywords: ['project', 'create', 'new'],
        run: { action: 'project.new' },
        disabled: act,
      },
      {
        label: 'Rename project',
        keywords: ['project', 'rename'],
        run: { panel: { kind: 'project_rename', projectId: this.activeProjectId } },
        disabled: act || noProject,
      },
      {
        label: 'Remove project…',
        detail: this.keyFor('project.destroy'),
        keywords: ['project', 'remove', 'delete', 'close'],
        run: { action: 'project.destroy' },
        disabled: act || noProject,
      },
    ];
    const system: PaletteRow[] = [
      {
        label: 'Processes',
        keywords: ['process', 'processes', 'memory', 'mem', 'ram', 'cpu', 'kill'],
        run: { panel: { kind: 'processes' } },
        disabled: act,
      },
      { label: 'Plugins', keywords: ['plugin', 'plugins', 'reload'], run: { panel: { kind: 'plugins' } } },
      { label: 'Update', keywords: ['update', 'version', 'upgrade'], run: { panel: { kind: 'update' } } },
    ];
    return [tabs, projects, pane, system];
  }

  // resourceReport asks for the process trees (the Processes page; the
  // daemon's collector runs only while these keep coming).
  resourceReport(): Promise<Outcome> {
    return this.requests.request('resource_report_req', { with_trees: true }, { timeoutMs: REPORT_TIMEOUT_MS });
  }

  killProcess(paneId: string, pid: number, startMs: number): Promise<Outcome> {
    return this.act('kill_process_req', { pane_id: paneId, pid, start_ms: startMs });
  }

  // reloadPlugins: reload_plugins has no answer; the plugin_list_req sent
  // after it on the same socket is answered after the reload ran (one
  // connection's messages run in order).
  reloadPlugins(): Promise<Outcome> {
    this.fire('reload_plugins', {});
    return this.requests.request('plugin_list_req', {});
  }

  // stageUpdate downloads a release on the daemon's machine, one at a time;
  // the daemon may take minutes (updateCheckTimeout is 10 min).
  async stageUpdate(): Promise<Outcome> {
    if (this.stageBusy) return { ok: false, code: 'busy', error: 'a download is already running' };
    this.stageBusy = true;
    try {
      return await this.requests.request('stage_update_req', {}, { timeoutMs: 600_000 });
    } finally {
      this.stageBusy = false;
    }
  }

  // openNotes opens the pane's note. Reading is a view; the editor is
  // read-only below standard rights (spec §4.2). One editor at a time.
  openNotes(paneId: string): void {
    this.keys?.cancel();
    if (this.notes && this.notes.paneId === paneId) return;
    if (this.notes) {
      if (this.notes.close() === 'wait') {
        this.showNotice('Close the open note first — it has unsaved text');
        return;
      }
      this.notes = null;
    }
    const io: NoteIO = {
      get: (id) => this.requests.request('note_get', { pane_id: id }, { timeoutMs: NOTE_LOAD_TIMEOUT_MS }),
      set: (id, text, base) => this.requests.request('note_set', { pane_id: id, text, base_rev: base }),
    };
    const n = new NoteSession(paneId, io, browserClock, this.rights === 'read-only');
    n.onChange = () => this.notesTick++;
    n.onClosed = () => {
      if (this.notes === n) this.notes = null;
      this.focusActiveSoon();
    };
    this.notes = n;
    n.load();
  }

  private loadCollapsed(): ReadonlySet<string> {
    try {
      const v: unknown = JSON.parse(this.storage.getItem(COLLAPSED_KEY) ?? '[]');
      return new Set(Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : []);
    } catch {
      return new Set();
    }
  }

  toggleGroup(name: string): void {
    const next = new Set(this.collapsedGroups);
    if (next.has(name)) next.delete(name);
    else next.add(name);
    this.collapsedGroups = next;
    this.storage.setItem(COLLAPSED_KEY, JSON.stringify([...next]));
  }

  // newProject follows the TUI's rules (lib/projects.ts): adopt the lone
  // bootstrap project, refuse a second project on a --connect host or a name
  // already taken, else create one and switch to it.
  async newProject(name: string, rootDir: string): Promise<Outcome> {
    const s = this.state;
    if (!s) return { ok: false, code: 'offline', error: 'not connected' };
    const plan = newProjectPlan(s, this.client?.connect === true, name);
    if (plan.kind === 'refuse') {
      this.showNotice(plan.text);
      return { ok: false, code: 'refused', error: plan.text };
    }
    if (plan.kind === 'adopt') {
      // An empty folder keeps the adopted project's own root: update_project
      // has no unchanged-value guard, so "" would erase it (projectdialog.go).
      const own = s.projects.find((p) => p.id === plan.projectId)?.root_dir ?? '';
      return this.act('update_project', { project_id: plan.projectId, name, root_dir: rootDir.trim() || own, adopt_bootstrap: true });
    }
    const out = await this.act('create_project_req', { name, root_dir: rootDir.trim() });
    const id = (out.reply?.payload as CreateProjectResp | undefined)?.project_id;
    if (out.ok && id) this.switchProject(id);
    return out;
  }

  renameProject(id: string, name: string): void {
    const p = this.state?.projects.find((x) => x.id === id);
    if (p) void this.act('update_project', { project_id: id, name, root_dir: p.root_dir });
  }

  removeProject(id: string): void {
    void this.act('destroy_project', { project_id: id });
  }

  async groupOp(op: 'create' | 'rename' | 'delete', name: string, newName?: string): Promise<Outcome> {
    if (op === 'rename' && !this.groupRenames.start(name)) {
      this.showNotice('A rename of this group is still waiting for the daemon');
      return { ok: false, code: 'busy', error: 'busy' };
    }
    try {
      return await this.act('group_op', newName === undefined ? { op, name } : { op, name, new_name: newName });
    } finally {
      if (op === 'rename') this.groupRenames.end(name);
    }
  }

  // fileProject puts a project in a group ('' = out of every group); the
  // daemon creates a group name it does not have yet.
  fileProject(projectId: string, group: string): void {
    void this.act('set_project_group', { project_id: projectId, group });
  }

  moveTab(tabId: string, projectId: string): void {
    void this.act('move_tab', { tab_id: tabId, project_id: projectId });
  }

  // openHistory opens the input-history dialog for a pane.
  openHistory(paneId: string): void {
    const p = this.state?.panes.find((x) => x.id === paneId);
    if (!p) return;
    this.openPanel({ kind: 'history', paneId, paneType: p.type || 'terminal' });
  }

  historyList(paneId: string): Promise<Outcome> {
    return this.requests.request('pane_history_req', { pane_id: paneId }, { timeoutMs: HISTORY_TIMEOUT_MS });
  }

  historyEntry(paneId: string, tsMs: number): Promise<Outcome> {
    return this.requests.request('pane_history_entry_req', { pane_id: paneId, ts_ms: tsMs }, { timeoutMs: HISTORY_TIMEOUT_MS });
  }

  sessionDetail(cwd: string, sessionId: string): Promise<Outcome> {
    return this.requests.request('claude_session_detail_req', { cwd, session_id: sessionId });
  }

  // closeNotes is the editor's Close / Escape: it closes now when nothing is
  // at risk, else after the save (or stays, offering the choices).
  closeNotes(): void {
    const n = this.notes;
    if (!n) return;
    if (n.close() === 'closed') {
      this.notes = null;
      this.focusActiveSoon();
    }
  }

  // discardNotes closes the editor dropping its text (the confirmed
  // "Discard and close").
  discardNotes(): void {
    this.notes?.stop();
    this.notes = null;
    this.focusActiveSoon();
  }

  paletteRows(): PaletteRow[] {
    const s = this.state;
    if (!s) return [];
    return buildPalette({
      state: s,
      activeProject: this.activeProjectId,
      activePane: this.activePane,
      keyFor: (id) => this.keyFor(id),
      refusal: (c) => this.refusalFor(c),
      extra: this.paletteExtraRows(),
    });
  }

  // runPaletteRow runs a chosen row through the same handler as its key.
  runPaletteRow(r: PaletteRow): void {
    if (!r.run || r.disabled) return;
    this.panel = null;
    const run = r.run;
    if ('action' in run) {
      if (run.action === 'builtin.new_pane') this.runBuiltin('new_pane');
      else this.runAction(run.action);
    } else if ('goPane' in run) this.goToPane(run.goPane);
    else if ('switchTab' in run) this.switchTab(run.switchTab);
    else if ('switchProject' in run) this.switchProject(run.switchProject);
    else this.openPanel(run.panel);
    if (this.panel === null) this.focusActiveSoon();
  }

  // goToPane shows the pane's tab and makes the pane active, through the
  // notification jump's path: a pane in another tab is activated once the
  // state showing that tab arrives (resolveJump in applyState).
  goToPane(paneId: string): void {
    const p = this.state?.panes.find((x) => x.id === paneId);
    if (!p) return;
    const placed = this.placed.map((x) => x.id);
    const step = jumpStep(this.state, this.activeTabId, placed, p.tab_id, paneId, this.readOnly, browserClock.now());
    if (step.switch !== '') this.switchTab(step.switch);
    // After the switch, which clears any older jump.
    this.pendingJump = step.pending;
    if (step.activate !== '') {
      this.setActivePane(step.activate);
      this.focus(step.activate);
    }
  }

  // searchPanes is the palette's search in pane output (view class).
  searchPanes(q: string): Promise<Outcome> {
    return this.requests.request('pane_search_req', { query: q }, { timeoutMs: 3000, timeoutText: 'search timed out' });
  }

  // focusActiveSoon is focusActive once the current key event is over. An
  // Enter that submitted a dialog or picked a menu item still has its
  // keypress to come, and a terminal focused now would take it as a typed
  // Enter — running whatever was half typed at the prompt. A dialog or menu
  // opened meanwhile (a pick that opens a rename) keeps the focus.
  focusActiveSoon(): void {
    window.setTimeout(() => {
      if (document.querySelector('[data-modal]') === null) this.focusActive();
    }, 0);
  }

  // focusActive gives the keyboard to the shown overlay, else the active
  // pane: after a dialog or a menu closes, typing reaches the terminal again.
  focusActive(): void {
    const id = this.keyPane();
    if (id) this.focus(id);
  }

  private runAction(id: string): void {
    const label = this.keymap?.actions.find((a) => a.id === id)?.label ?? id;
    if (TUI_ONLY.has(id)) {
      this.showNotice(`${label}: available in the TUI`);
      return;
    }
    if (this.readOnly && !VIEW_ONLY.has(id)) {
      this.showNotice('read-only connection — that action is disabled');
      return;
    }
    const pane = this.activePane;
    const tab = this.activeTabId;
    const sw = /^tab\.switch_([1-9])$/.exec(id);
    if (sw) {
      const t = this.tabBar[Number(sw[1]) - 1];
      if (t) this.switchTab(t.id);
      return;
    }
    switch (id) {
      case 'notification.toggle':
        this.notifyOpen = !this.notifyOpen;
        return;
      case 'notification.focus':
        this.notifyOpen = true;
        this.notifyFocus++;
        return;
      case 'sidebar.toggle':
        this.sidebarOpen = !this.sidebarOpen;
        return;
      case 'system.shortcuts':
        this.openKeyList();
        return;
      case 'app.command_palette':
        this.openPanel({ kind: 'palette' });
        return;
      case 'pane.command_history':
        if (pane) this.openHistory(pane);
        return;
      case 'project.new':
        this.openPanel({ kind: 'project_new' });
        return;
      case 'project.destroy':
        if (this.activeProjectId) this.openPanel({ kind: 'project_remove', projectId: this.activeProjectId });
        return;
      case 'pane.notes_toggle':
        if (pane) {
          if (this.notes?.paneId === pane) this.closeNotes();
          else this.openNotes(pane);
        }
        return;
      case 'project.picker':
        this.openPanel({ kind: 'projects' });
        return;
      case 'client.take_control':
        this.takeControl();
        return;
      case 'pane.toggle_lazygit':
        void this.toggleOverlay('lazygit');
        return;
      case 'pane.toggle_hunk':
        void this.toggleOverlay('hunk');
        return;
      case 'pane.split_h':
        if (pane) this.splitQuick(pane, 'right');
        return;
      case 'pane.split_v':
        if (pane) this.splitQuick(pane, 'below');
        return;
      case 'pane.close':
        if (pane) this.askClosePane(pane);
        return;
      case 'pane.restart':
        if (pane) this.restartPane(pane);
        return;
      case 'pane.rename':
        if (pane) this.startRenamePane(pane);
        return;
      case 'pane.mute':
        if (pane) this.setMuted(pane, this.state?.panes.find((p) => p.id === pane)?.muted !== true);
        return;
      case 'tab.new':
        this.openCreate('new_tab');
        return;
      case 'tab.close':
        if (tab) this.askCloseTab(tab);
        return;
      case 'tab.rename':
        if (tab) this.startRenameTab(tab);
        return;
      case 'tab.cycle_color':
        if (tab) this.cycleTabColor(tab);
        return;
      case 'tab.next':
        this.switchTabBy(1);
        return;
      case 'tab.prev':
        this.switchTabBy(-1);
        return;
      case 'pane.next':
        this.focusPaneBy(1);
        return;
      case 'pane.prev':
        this.focusPaneBy(-1);
        return;
      case 'pane.left':
      case 'pane.right':
      case 'pane.up':
      case 'pane.down':
        this.focusPaneDir(id.slice(5) as Dir);
        return;
      case 'pane.scroll_page_up':
        this.xterms.get(this.keyPane())?.scrollPages(-1);
        return;
      case 'pane.scroll_page_down':
        this.xterms.get(this.keyPane())?.scrollPages(1);
        return;
      default:
        this.showNotice(`${label}: not available here`);
    }
  }

  private switchTabBy(d: number): void {
    const tabs = this.tabBar;
    const i = tabs.findIndex((t) => t.active);
    if (tabs.length < 2 || i < 0) return;
    this.switchTab(tabs[(i + d + tabs.length) % tabs.length]!.id);
  }

  // Pane moves focus the pane too, so typing goes where the border shows.
  private focusPaneBy(d: number): void {
    const ids = this.placed.map((p) => p.id);
    if (ids.length < 2) return;
    const i = ids.indexOf(this.activePane);
    this.moveTo(ids[(i + d + ids.length) % ids.length]!);
  }

  private focusPaneDir(dir: Dir): void {
    const next = neighbour(this.placed, this.activePane, dir);
    if (next) this.moveTo(next);
  }

  private moveTo(paneId: string): void {
    this.setActivePane(paneId);
    if (!this.overlay) this.focus(paneId);
  }

  // toggleOverlay is spec §5.4: show this page's overlay of that kind, hide
  // it, or ask the daemon to reuse or replace the tab's one slot.
  async toggleOverlay(kind: OverlayKind): Promise<void> {
    const s = this.state;
    const tab = this.activeTabId;
    if (!s || !tab) return;
    // One create per tab at a time: a second Alt+G while the daemon works
    // would ask for the slot twice.
    if (this.overlayBusy.has(tab)) return;
    const step = overlayToggle(overlayOf(s, tab), this.overlayShown[tab], kind, this.editable);
    if (step.do === 'show' || step.do === 'hide') {
      this.setOverlayShown(tab, step.id, step.do === 'show');
      return;
    }
    if (step.do === 'refuse') {
      this.showNotice(this.readOnly ? 'read-only connection — that action is disabled' : 'Not connected — nothing was changed');
      return;
    }
    const existing = step.existing !== '' ? overlayOf(s, tab) : null;
    // No cwd means the pane has not reported one yet. Asking the daemon
    // would have it substitute its OWN default directory, so an overlay
    // could open on an unrelated repository; the TUI treats it as "no
    // repository" the same way. Decided before anything on screen changes.
    const cwd = s.panes.find((p) => p.id === this.activePane)?.cwd ?? '';
    let candidates: string[] = [];
    if (cwd !== '') {
      this.overlayBusy.add(tab);
      try {
        // Requests never rejects: every end is an Outcome.
        const repos = await this.requests.request('git_repos_req', { cwd });
        if (!repos.ok) {
          this.showNotice(`${kind}: ${repos.error}`);
          return;
        }
        const list = (repos.reply?.payload as { repos?: unknown } | undefined)?.repos;
        candidates = Array.isArray(list) ? list.filter((r): r is string => typeof r === 'string' && r !== '') : [];
      } finally {
        this.overlayBusy.delete(tab);
      }
      // The page may have moved on while the daemon looked.
      if (this.activeTabId !== tab) return;
    }
    const choice = overlayRepoChoice(candidates, existing);
    switch (choice.do) {
      case 'show':
        if (existing) this.setOverlayShown(tab, existing.id, true);
        return;
      case 'none':
        this.showNotice('no git repo here');
        return;
      case 'pick':
        this.keys?.cancel();
        this.repoPick = { tab, kind, repos: choice.repos, hide: step.hide };
        return;
      case 'create':
        await this.createOverlay(tab, kind, choice.repo, step.hide);
    }
  }

  // pickRepo runs the picker's choice; closeRepoPick ends the picker.
  pickRepo(repo: string): void {
    const p = this.repoPick;
    this.repoPick = null;
    if (!p || this.activeTabId !== p.tab || !this.editable) return;
    void this.createOverlay(p.tab, p.kind, repo, p.hide);
  }

  closeRepoPick(): void {
    this.repoPick = null;
    this.focusActiveSoon();
  }

  // createOverlay asks for the tab's overlay slot on repo; the daemon reuses
  // the slot for the same tool and repository and replaces it otherwise.
  // hide is this page's shown overlay of the other tool, hidden as the
  // request goes out.
  private async createOverlay(tab: string, kind: OverlayKind, repo: string, hide: string): Promise<void> {
    if (this.overlayBusy.has(tab)) return;
    this.overlayBusy.add(tab);
    try {
      if (hide) this.setOverlayShown(tab, hide, false);
      const r = await this.requests.request('split_pane_req', {
        tab_id: tab,
        placement: 'overlay',
        overlay_kind: kind,
        pane: { type: kind, cwd: repo },
      });
      const a = splitAnswer(r);
      const id = a?.paneId ?? '';
      if (!id) {
        this.showNotice(`${kind}: ${r.ok ? 'no pane in the answer' : r.error}`);
        return;
      }
      if (a?.notice) this.showNotice(`${kind}: ${a.notice}`);
      // The page may have moved on while the daemon worked.
      if (this.activeTabId === tab) this.setOverlayShown(tab, id, true);
    } finally {
      this.overlayBusy.delete(tab);
    }
  }

  // setOverlayShown shows or hides the tab's overlay on this page, and tells
  // the daemon (overlay_visible drives its idle reaper, not other clients).
  private setOverlayShown(tab: string, paneId: string, v: boolean): void {
    const next = { ...this.overlayShown };
    if (v) next[tab] = paneId;
    else delete next[tab];
    this.overlayShown = next;
    this.keys?.cancel();
    this.reportOverlay();
    if (v) this.focus(paneId);
    else this.focusActive();
  }

  // reportOverlay tells the daemon which overlay is on screen now, if that
  // changed since this socket last said (OverlayClaim). Run after every
  // toggle, every state (the active tab or project may have moved) and
  // every attach.
  private reportOverlay(): void {
    const s = this.state;
    if (!s || !this.live) return;
    const on = this.overlayVisibleFor(s, activeTabOf(s), this.overlayShown)?.id ?? '';
    const ids = new Set(s.panes.map((p) => p.id));
    for (const m of this.overlayClaim.reconcile(on, this.readOnly, (id) => ids.has(id))) this.conn.send(m);
  }

  // dismissEvent needs a socket that can send: a dismissal while the link is
  // down would be lost, and the card would come back with the next list.
  dismissEvent(id: string): void {
    if (!this.editable) return;
    this.conn.send({ type: 'dismiss_event', payload: { event_id: id } });
  }

  // jumpToEvent shows the event's tab and makes its pane active; a pane
  // that is gone (a closed pane's card) leaves the active pane alone. A pane
  // in another tab is made active once the state showing that tab arrives
  // (resolveJump in applyState), never before.
  jumpToEvent(e: PaneEvent): void {
    const placed = this.placed.map((p) => p.id);
    const step = jumpStep(this.state, this.activeTabId, placed, e.tab_id, e.pane_id, this.readOnly, browserClock.now());
    if (step.switch !== '') this.switchTab(step.switch);
    // After the switch, which clears any older jump.
    this.pendingJump = step.pending;
    if (step.activate !== '') {
      this.setActivePane(step.activate);
      this.focus(step.activate);
    }
  }

  private skipCtx(): { muted: (id: string) => boolean; activePaneId: string } {
    const panes = new Map((this.state?.panes ?? []).map((p) => [p.id, p]));
    return { muted: (id: string) => panes.get(id)?.muted === true, activePaneId: this.activePane };
  }

  // rebuildNotifications runs after every (re)attach: the daemon's queue is
  // the truth, and dismissals missed while away are not replayed.
  // Live events and dismissals that arrive while the list is on its way are
  // replayed over it (NotificationStore.beginRebuild).
  async rebuildNotifications(): Promise<void> {
    const gen = this.store.beginRebuild();
    const r = await this.requests.request('get_notifications_req', {});
    const raw = r.ok ? (r.reply?.payload as { events?: unknown } | undefined)?.events : undefined;
    if (!r.ok) {
      this.store.abortRebuild(gen);
      return;
    }
    const list = (Array.isArray(raw) ? raw : []).map(parsePaneEvent).filter((e): e is PaneEvent => e !== null);
    if (this.store.rebuild(list, this.skipCtx(), gen)) this.events = this.store.visible();
  }

  // openDialog opens the create-pane dialog once /api/client has answered;
  // nothing opens on a read-only or not-live page.
  async openDialog(open: DialogOpen): Promise<void> {
    this.keys?.cancel();
    if (!this.editable) {
      // The notice every other refused action shows: a key that did nothing
      // at all reads as a broken key.
      this.showNotice(this.readOnly ? 'read-only connection — that action is disabled' : 'Not connected — nothing was changed');
      return;
    }
    if (!(await this.refreshClient())) return;
    // The page may have lost its link or its rights while the answer came.
    if (this.editable) this.dialog = open;
  }

  // openCreate opens the dialog for the active tab: a new pane next to the
  // active pane, a replace of it, or a new tab. Menus and keys use
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
  // applied (ConnectionEvents.onAttached): it reloads the keymap and filter
  // (clientLoaded → keysFrom), rebuilds the notification list, and claims
  // the overlay still on screen (the attach's state already did; a no-op
  // then, kept so the claim never depends on that order).
  private attached(): void {
    this.reportOverlay();
    void this.refreshClient();
    void this.rebuildNotifications();
    void this.askVersion();
  }

  // askVersion learns, once per attach, the daemon's version and the gated
  // requests it handles (templates, the Update page).
  private async askVersion(): Promise<void> {
    const o = await this.requests.request('version_req', {});
    const p = (o.reply?.payload ?? null) as VersionResp | null;
    this.daemonRequests = o.ok && p ? (p.requests ?? []) : null;
    this.daemonVersion = o.ok && p ? sanitizeRemoteText(p.version) : '';
  }

  async createFromTemplate(req: CreateFromTemplateReq): Promise<Outcome> {
    const out = await this.act('create_from_template_req', req, TEMPLATE_TOO_OLD);
    const p = out.reply?.payload as CreateFromTemplateResp | undefined;
    const first = p?.pane_ids?.[0];
    if (out.ok && first) {
      if (p?.preparing_worktree) this.followFocus = first;
      this.goToPane(first);
    }
    return out;
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
