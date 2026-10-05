import type { ClientInfo, PluginDef, SavedInstance } from './client';
import type { Message, PaneSpec, Placement, SplitPaneReq } from './protocol';
import type { Outcome } from './requests';
import { sanitizeRemoteText } from './sanitize';

// CreateDialog is the browser's create-pane dialog as a pure state machine,
// mirroring internal/tui/dialog.go: category → plugin (greyed per daemon) →
// saved instances / their form → setup fields in the TUI's order (folder,
// kube context, toggles, worktree, sandbox, sign-in, resume) → placement.
// It sends nothing itself: the view runs each daemon list through Requests
// and files the answer here, and sends the request takeRequest hands out.
//
// One deliberate difference from the TUI: the resume row is hidden while a
// new branch is chosen. The daemon looks for the transcript in the new
// checkout, which does not exist yet, so no listed session could be resumed.

export type Step = 'category' | 'plugin' | 'instances' | 'form' | 'setup' | 'placement' | 'done';
export type ListKind = 'plugins' | 'folders' | 'repos' | 'kube' | 'sessions' | 'worktrees' | 'sandbox';

export interface ListState {
  status: 'idle' | 'scanning' | 'ready' | 'empty' | 'failed';
  error?: string;
  // A failed list (a single-flight refusal included) can be asked again.
  retry?: boolean;
  reply?: Message;
}

export interface DialogOpen {
  mode: 'pane' | 'new_tab' | 'replace';
  targetPaneId: string;
  tabId: string;
  projectId: string;
  defaultCwd: string;
}

// The sign-in row's choices onto (auth, claude_config), as
// internal/tui/sandbox_field.go sandboxSignInFields: token never shares.
const SIGN_IN: Record<string, { auth: string; claude_config: string }> = {
  shared: { auth: 'browser', claude_config: 'shared' },
  token: { auth: 'token', claude_config: 'own' },
  browser: { auth: 'browser', claude_config: 'own' },
};

export const SIGN_IN_CHOICES: { value: string; label: string; detail: string }[] = [
  { value: 'browser', label: 'Browser', detail: 'sign in in this container · full subscription' },
  { value: 'shared', label: 'Shared', detail: 'shares hooks, MCP servers, history · sign in once' },
  { value: 'token', label: 'Token', detail: 'no sign-in · saves a token every later Claude uses' },
];

// The answer field that holds each list's items; an answer without any is
// 'empty', never a failure.
const ITEMS: Record<Exclude<ListKind, 'sandbox'>, string> = {
  plugins: 'plugins',
  folders: 'entries',
  repos: 'repos',
  kube: 'contexts',
  sessions: 'sessions',
  worktrees: 'worktrees',
};

export const NEED_IMAGE = 'Enter a container image, or turn the sandbox off';
export const NEED_FULL_RIGHTS = 'Saved instances start with their own arguments, which need full rights';
export const NEED_BRANCH = 'Enter a branch name';

// Mirrors internal/gitworktree validate.go: branchRejected, maxBranchLen and
// the Windows device names, checked on every platform there too.
const BRANCH_REJECTED = ' ~^:?*[\\\x7f';
const MAX_BRANCH_LEN = 255;
const WINDOWS_RESERVED = new Set([
  'con', 'prn', 'aux', 'nul',
  'com1', 'com2', 'com3', 'com4', 'com5', 'com6', 'com7', 'com8', 'com9',
  'lpt1', 'lpt2', 'lpt3', 'lpt4', 'lpt5', 'lpt6', 'lpt7', 'lpt8', 'lpt9',
]);

// validateBranch is gitworktree.ValidateBranch, same order and same words,
// so the dialog refuses what the daemon would refuse, beside the field.
// '' when the name can be used.
export function validateBranch(name: string): string {
  if (name.trim() === '') return 'branch name is empty';
  if (name.startsWith('-')) return 'branch name may not start with "-" — it would read as a flag';
  if (name.startsWith('/') || name.endsWith('/')) return 'branch name may not start or end with "/"';
  if (name.includes('//')) return 'branch name may not contain "//"';
  if (name.includes('..')) return 'branch name may not contain ".."';
  if (name.includes('@{')) return 'branch name may not contain "@{"';
  if (name === '@') return 'branch name may not be "@"';
  if (name.endsWith('.')) return 'branch name may not end with "."';
  if (new TextEncoder().encode(name).length > MAX_BRANCH_LEN) return `branch name is longer than ${MAX_BRANCH_LEN} characters`;
  for (const part of name.split('/')) {
    if (part.startsWith('.')) return 'no part of a branch name may start with "."';
    if (part.endsWith('.lock')) return 'no part of a branch name may end in ".lock"';
    const dot = part.indexOf('.');
    if (WINDOWS_RESERVED.has((dot >= 0 ? part.slice(0, dot) : part).toLowerCase())) {
      return `"${part}" is a reserved device name on Windows`;
    }
  }
  for (const ch of name) {
    const c = ch.codePointAt(0) ?? 0;
    if (c < 0x20 || BRANCH_REJECTED.includes(ch)) return `branch name may not contain ${JSON.stringify(ch)}`;
  }
  return '';
}

// enforceGroups is internal/tui/dialog.go enforceToggleGroups: winner >= 0
// turns off the other members of its group; -1 keeps the LAST checked member
// of each group (an initial state with two defaults on).
export function enforceGroups(toggles: { group?: string }[], states: boolean[], winner: number): void {
  if (winner >= 0) {
    const g = toggles[winner]?.group;
    if (!g) return;
    toggles.forEach((t, i) => {
      if (i !== winner && t.group === g) states[i] = false;
    });
    return;
  }
  const last = new Map<string, number>();
  toggles.forEach((t, i) => {
    if (t.group && states[i]) last.set(t.group, i);
  });
  toggles.forEach((t, i) => {
    if (t.group && states[i] && last.get(t.group) !== i) states[i] = false;
  });
}

function needsSetup(p: PluginDef | null): boolean {
  return !!p && (!!p.prompts_cwd || (p.toggles?.length ?? 0) > 0 || p.discover === 'kube' || !!p.sessions);
}

function hasForm(p: PluginDef | null | undefined): boolean {
  return (p?.form_fields?.length ?? 0) > 0;
}

// availableFrom builds the availability map from a plugin_list_req answer.
// A daemon that did not answer (too old for the request) keeps every plugin
// offered — the TUI's rule: a wrong offer fails loudly at spawn, a wrong
// grey-out hides a working tool silently.
export function availableFrom(info: ClientInfo, out: Outcome): Record<string, boolean> {
  const listed = out.ok ? (out.reply?.payload as { plugins?: { name?: unknown; available?: unknown }[] } | undefined)?.plugins : undefined;
  if (!Array.isArray(listed) || listed.length === 0) {
    return Object.fromEntries(info.plugins.map((p) => [p.name, true]));
  }
  const avail: Record<string, boolean> = {};
  for (const p of listed) {
    if (typeof p.name === 'string') avail[p.name] = p.available === true;
  }
  return avail;
}

export class CreateDialog {
  step: Step = 'category';
  category = '';
  plugin: PluginDef | null = null;
  instanceId = '';
  // The instance the form edits; '' for a new one.
  editing = '';
  error = '';
  cwd: string;
  toggles: boolean[] = [];
  kubeContext = '';
  existingWorktree = '';
  // The repository a new branch is cut from, as worktree_list_req reported
  // it. Shown in the worktree row only; the daemon resolves the repository
  // from cwd itself (R-A), so it is never sent.
  worktreeRoot = '';
  // "New branch…" is chosen in the worktree row; newBranch is its name.
  newBranchMode = false;
  newBranch = '';
  sandboxOn = false;
  sandboxImage: string;
  signIn: string;
  resumeId = '';
  // The folder the session list was answered for: a pick from a list of
  // another folder is dropped at submit.
  listedSessionsFor = '';
  request: SplitPaneReq | null = null;
  // Availability per plugin on THIS daemon (plugin_list_req); a plugin the
  // daemon did not list is unavailable there (spec §5.2, the TUI's rule).
  // Every plugin is offered until the daemon answers, and when it cannot.
  available: Record<string, boolean>;
  lists: Record<ListKind, ListState> = {
    plugins: { status: 'idle' },
    folders: { status: 'idle' },
    repos: { status: 'idle' },
    kube: { status: 'idle' },
    sessions: { status: 'idle' },
    worktrees: { status: 'idle' },
    sandbox: { status: 'idle' },
  };
  // One number per list: an answer to an older request than the newest is
  // dropped (the user moved on while it was in flight).
  private readonly tokens: Record<ListKind, number> = {
    plugins: 0,
    folders: 0,
    repos: 0,
    kube: 0,
    sessions: 0,
    worktrees: 0,
    sandbox: 0,
  };
  // The request was handed out (takeRequest); it is handed out once.
  private taken = false;

  constructor(
    public info: ClientInfo,
    // null: the daemon has not answered plugin_list_req yet (listed('plugins')
    // files it); every plugin is offered meanwhile.
    available: Record<string, boolean> | null,
    readonly open: DialogOpen,
  ) {
    this.cwd = open.defaultCwd;
    this.sandboxImage = info.sandbox.image_default;
    this.signIn = SIGN_IN[info.sandbox.sign_in_default] ? info.sandbox.sign_in_default : 'browser';
    this.available = available ?? availableFrom(info, { ok: false, code: 'idle', error: '' });
  }

  // categories in the gateway's order (the TUI's), only those with a plugin;
  // a category no order names follows, by its key.
  get categories(): { key: string; label: string }[] {
    const keys = new Set(this.info.plugins.map((p) => p.category));
    const known = this.info.categories.filter((c) => keys.has(c.key));
    const extra = [...keys]
      .filter((k) => !known.some((c) => c.key === k))
      .sort()
      .map((k) => ({ key: k, label: k }));
    return [...known, ...extra];
  }

  // pluginsIn lists a category: available first, then by display name
  // (createPaneCategories).
  pluginsIn(category: string): PluginDef[] {
    return this.info.plugins
      .filter((p) => p.category === category)
      .sort(
        (a, b) =>
          Number(!!this.available[b.name]) - Number(!!this.available[a.name]) || a.display_name.localeCompare(b.display_name),
      );
  }

  get placements(): Placement[] {
    return this.open.mode === 'replace' ? ['replace'] : ['right', 'below', 'replace'];
  }

  // Standard rights may save instances (spec §4.2); starting one, which
  // carries its raw arguments, is full-only at the daemon.
  get canSaveInstances(): boolean {
    return this.info.rights === 'full' || this.info.rights === 'standard';
  }

  get canLaunchInstances(): boolean {
    return this.info.rights === 'full';
  }

  get needsSetup(): boolean {
    return needsSetup(this.plugin);
  }

  get showKube(): boolean {
    return this.plugin?.discover === 'kube';
  }

  get showFolder(): boolean {
    return !!this.plugin?.prompts_cwd;
  }

  get showWorktree(): boolean {
    return !!this.plugin?.prompts_cwd;
  }

  get showSandbox(): boolean {
    return (
      !!this.plugin?.prompts_cwd &&
      this.lists.sandbox.status === 'ready' &&
      (this.lists.sandbox.reply?.payload as { available?: boolean } | undefined)?.available === true
    );
  }

  get showSignIn(): boolean {
    return this.showSandbox && this.sandboxOn && !!this.plugin?.uses_claude_auth;
  }

  // A resume list is scoped to the folder the pane runs in; see the note at
  // the top about a new branch.
  get showSession(): boolean {
    return !!this.plugin?.sessions && !this.sandboxOn && !this.newBranchMode;
  }

  // The folder the pane will run in: an existing worktree, else the folder.
  get spawnDir(): string {
    return this.existingWorktree || this.cwd;
  }

  // The branches the worktree listing reported (a new branch must not be one).
  get branches(): string[] {
    const b = (this.lists.worktrees.reply?.payload as { branches?: unknown } | undefined)?.branches;
    return Array.isArray(b) ? b.filter((x): x is string => typeof x === 'string') : [];
  }

  // newBranchError is the TUI's validateNewBranch: '' when the name can be
  // used, else the words to show beside the field. Run on Enter in the field
  // and again on Continue, so neither route lets a bad name through.
  get newBranchError(): string {
    if (this.newBranch.trim() === '') return NEED_BRANCH;
    const bad = validateBranch(this.newBranch);
    if (bad !== '') return bad;
    if (this.branches.includes(this.newBranch)) return `branch ${sanitizeRemoteText(this.newBranch)} already exists`;
    return '';
  }

  pickCategory(key: string): void {
    this.category = key;
    this.step = 'plugin';
    this.error = '';
  }

  pickPlugin(name: string): boolean {
    const p = this.info.plugins.find((x) => x.name === name);
    if (!p || !this.available[name]) return false;
    if (hasForm(p) && !this.canSaveInstances && !this.canLaunchInstances) {
      this.error = NEED_FULL_RIGHTS;
      return false;
    }
    this.error = '';
    // Setup choices belong to the plugin they were made for: a worktree,
    // sandbox, kube context or session picked for one plugin and abandoned
    // with Back must not ride along with the next. The folder stays.
    if (this.plugin?.name !== p.name) {
      this.toggles = [];
      this.kubeContext = '';
      this.existingWorktree = '';
      this.newBranchMode = false;
      this.newBranch = '';
      this.sandboxOn = false;
      this.resumeId = '';
    }
    this.plugin = p;
    this.instanceId = '';
    if (hasForm(p)) {
      this.editing = '';
      this.step = (this.info.instances[name]?.length ?? 0) > 0 || !this.canSaveInstances ? 'instances' : 'form';
      return true;
    }
    this.enterSetupOrSplit();
    return true;
  }

  // pickInstance starts the pane with a saved instance; a login without full
  // rights is told so here rather than refused by the daemon after the send.
  pickInstance(id: string): boolean {
    if (!this.canLaunchInstances) {
      this.error = NEED_FULL_RIGHTS;
      return false;
    }
    this.instanceId = id;
    this.error = '';
    this.enterSetupOrSplit();
    return true;
  }

  // newInstance and editInstance open the form.
  newInstance(): void {
    this.editing = '';
    this.error = '';
    this.step = 'form';
  }

  editInstance(id: string): void {
    this.editing = id;
    this.error = '';
    this.step = 'form';
  }

  // instanceSaved is called after the form's POST /api/instances answered:
  // a new instance goes on to setup, as in the TUI — or, without the rights
  // to start it, back to the list, saying why.
  instanceSaved(id: string): void {
    if (this.canLaunchInstances) {
      this.pickInstance(id);
      return;
    }
    this.instancesChanged(this.info);
    this.error = `Saved. ${NEED_FULL_RIGHTS}`;
  }

  // instancesChanged takes a fresh instance list (after an edit or delete)
  // and returns to the list, or to the form when none is left.
  instancesChanged(info: ClientInfo): void {
    this.info = info;
    const name = this.plugin?.name ?? '';
    this.editing = '';
    this.error = '';
    this.step = (info.instances[name]?.length ?? 0) > 0 || !this.canSaveInstances ? 'instances' : 'form';
  }

  private enterSetupOrSplit(): void {
    const p = this.plugin;
    if (p && needsSetup(p)) {
      this.toggles = (p.toggles ?? []).map((t) => !!t.default);
      enforceGroups(p.toggles ?? [], this.toggles, -1);
      this.sandboxOn = false;
      this.resumeId = '';
      this.step = 'setup';
      return;
    }
    this.advance();
  }

  setToggle(i: number, on: boolean): void {
    this.toggles[i] = on;
    if (on) enforceGroups(this.plugin?.toggles ?? [], this.toggles, i);
  }

  // folderChanged forgets what belonged to the old folder: its worktree
  // choice and its session pick.
  folderChanged(cwd: string): void {
    if (cwd === this.cwd) return;
    this.cwd = cwd;
    this.existingWorktree = '';
    this.newBranchMode = false;
    this.newBranch = '';
    this.worktreeRoot = '';
    this.resumeId = '';
  }

  // chooseWorktree picks the worktree row: '' (none — this folder), a listed
  // worktree's path, or (chooseNewBranch) a new branch.
  chooseWorktree(path: string): void {
    this.existingWorktree = path;
    this.newBranchMode = false;
    this.newBranch = '';
  }

  chooseNewBranch(): void {
    this.existingWorktree = '';
    this.newBranchMode = true;
  }

  // continueSetup returns '' or the reason the setup cannot be submitted.
  continueSetup(): string {
    if (this.sandboxOn && this.sandboxImage.trim() === '') return NEED_IMAGE;
    if (this.showFolder && this.cwd.trim() === '') return 'Choose a folder';
    if (this.newBranchMode) {
      const bad = this.newBranchError;
      if (bad !== '') return bad;
    }
    if (this.resumeId !== '' && this.listedSessionsFor !== this.spawnDir) this.resumeId = '';
    this.advance();
    return '';
  }

  private advance(): void {
    this.error = '';
    if (this.open.mode === 'new_tab') {
      this.done(this.build('new_tab'));
      return;
    }
    this.step = 'placement';
  }

  private done(req: SplitPaneReq): void {
    this.request = req;
    this.taken = false;
    this.step = 'done';
  }

  submit(placement: Placement): SplitPaneReq | null {
    if (this.step !== 'placement') return null;
    if (!this.placements.includes(placement)) return null;
    this.done(this.build(placement));
    return this.request;
  }

  // takeRequest hands out the finished request exactly once: whatever the
  // view does afterwards (a list answer landing late, a second click), the
  // same create never goes out twice.
  takeRequest(): SplitPaneReq | null {
    if (this.step !== 'done' || this.taken || !this.request) return null;
    this.taken = true;
    return this.request;
  }

  // refused takes the dialog back to the step the request was made from, to
  // show why and let the user change it.
  refused(error: string): void {
    this.error = sanitizeRemoteText(error);
    this.request = null;
    this.taken = false;
    this.step = this.open.mode === 'new_tab' ? (this.needsSetup ? 'setup' : hasForm(this.plugin) ? 'instances' : 'plugin') : 'placement';
  }

  private build(placement: Placement): SplitPaneReq {
    const p = this.plugin as PluginDef;
    const pane: PaneSpec = { type: p.name, cwd: this.spawnDir };
    const on = (p.toggles ?? []).filter((_, i) => this.toggles[i]).map((t) => t.name);
    if (on.length > 0) pane.toggles = on;
    if (this.instanceId) pane.instance_id = this.instanceId;
    if (p.discover === 'kube' && this.kubeContext) pane.kube_context = this.kubeContext;
    // R-A: a new branch is {branch} — the daemon resolves the repository from
    // cwd; an existing worktree is {existing_path} (and the cwd, which the
    // page always sends). continueSetup has checked the branch name. Only a
    // plugin whose setup shows these rows sends them.
    if (this.showWorktree && this.existingWorktree) pane.worktree = { existing_path: this.existingWorktree };
    else if (this.showWorktree && this.newBranchMode) pane.worktree = { branch: this.newBranch };
    if (p.prompts_cwd && this.sandboxOn) pane.sandbox = { image: this.sandboxImage.trim(), ...(SIGN_IN[this.signIn] ?? SIGN_IN.browser) };
    if (this.showSession && this.resumeId) pane.resume_session_id = this.resumeId;
    if (placement === 'new_tab') {
      return { tab_id: this.open.tabId, placement, new_tab: { name: '', project_id: this.open.projectId }, pane };
    }
    // A tab with no pane to aim at: the daemon takes its first leaf.
    if (this.open.targetPaneId === '') return { tab_id: this.open.tabId, placement, pane };
    return { target_pane_id: this.open.targetPaneId, placement, pane };
  }

  // back returns one step; false at the first step (the view closes).
  back(): boolean {
    this.error = '';
    switch (this.step) {
      case 'plugin':
        this.step = 'category';
        return true;
      case 'instances':
        this.step = 'plugin';
        return true;
      case 'form':
        this.step = (this.info.instances[this.plugin?.name ?? '']?.length ?? 0) > 0 ? 'instances' : 'plugin';
        return true;
      case 'setup':
        this.step = hasForm(this.plugin) ? 'instances' : 'plugin';
        return true;
      case 'placement':
        this.step = this.needsSetup ? 'setup' : hasForm(this.plugin) ? 'instances' : 'plugin';
        return true;
      default:
        return false;
    }
  }

  // scanning marks a list as asked and returns the request's number, which
  // listed() checks.
  scanning(kind: ListKind): number {
    this.lists[kind] = { status: 'scanning' };
    return ++this.tokens[kind];
  }

  // listed files one daemon list's answer. A single-flight refusal or any
  // failure offers a retry; an answer with nothing in it is 'empty', never
  // shown as a failure (and vice versa). An answer to an older request than
  // the newest of its kind is dropped. Error text comes from a daemon the
  // user may not control, so it is kept sanitized.
  listed(kind: ListKind, out: Outcome, token?: number): void {
    if (token !== undefined && token !== this.tokens[kind]) return;
    if (kind === 'plugins') this.available = availableFrom(this.info, out);
    if (!out.ok) {
      this.lists[kind] = { status: 'failed', error: sanitizeRemoteText(out.error), retry: true };
      return;
    }
    const p = (out.reply?.payload ?? {}) as Record<string, unknown>;
    let empty = false;
    if (kind !== 'sandbox') {
      const items = p[ITEMS[kind]];
      empty = !Array.isArray(items) || items.length === 0;
      // A folder with no subfolders still offers its roots to climb to.
      if (kind === 'folders' && Array.isArray(p.roots) && p.roots.length > 0) empty = false;
    }
    this.lists[kind] = { status: empty ? 'empty' : 'ready', reply: out.reply };
    if (kind === 'sessions') this.listedSessionsFor = typeof p.cwd === 'string' ? p.cwd : '';
    if (kind === 'worktrees') this.worktreeRoot = typeof p.root === 'string' ? p.root : '';
  }
}

// DialogView is a plain copy of everything the view draws: the class is not
// reactive, so the component takes a fresh view after every change.
export interface DialogView {
  mode: DialogOpen['mode'];
  step: Step;
  category: string;
  categories: { key: string; label: string }[];
  plugins: PluginDef[];
  available: Record<string, boolean>;
  plugin: PluginDef | null;
  instances: SavedInstance[];
  editing: string;
  canSave: boolean;
  canLaunch: boolean;
  error: string;
  cwd: string;
  toggles: boolean[];
  kubeContext: string;
  existingWorktree: string;
  newBranchMode: boolean;
  newBranch: string;
  newBranchError: string;
  worktreeRoot: string;
  sandboxOn: boolean;
  sandboxImage: string;
  signIn: string;
  resumeId: string;
  lists: Record<ListKind, ListState>;
  placements: Placement[];
  showFolder: boolean;
  showKube: boolean;
  showWorktree: boolean;
  showSandbox: boolean;
  showSignIn: boolean;
  showSession: boolean;
}

export function viewOf(d: CreateDialog): DialogView {
  return {
    mode: d.open.mode,
    step: d.step,
    category: d.category,
    categories: d.categories,
    plugins: d.pluginsIn(d.category),
    available: { ...d.available },
    plugin: d.plugin,
    instances: d.plugin ? [...(d.info.instances[d.plugin.name] ?? [])] : [],
    editing: d.editing,
    canSave: d.canSaveInstances,
    canLaunch: d.canLaunchInstances,
    error: d.error,
    cwd: d.cwd,
    toggles: [...d.toggles],
    kubeContext: d.kubeContext,
    existingWorktree: d.existingWorktree,
    newBranchMode: d.newBranchMode,
    newBranch: d.newBranch,
    newBranchError: d.newBranchMode ? d.newBranchError : '',
    worktreeRoot: d.worktreeRoot,
    sandboxOn: d.sandboxOn,
    sandboxImage: d.sandboxImage,
    signIn: d.signIn,
    resumeId: d.resumeId,
    lists: { ...d.lists },
    placements: d.placements,
    showFolder: d.showFolder,
    showKube: d.showKube,
    showWorktree: d.showWorktree,
    showSandbox: d.showSandbox,
    showSignIn: d.showSignIn,
    showSession: d.showSession,
  };
}

// Act is how a dialog view changes the state machine: it runs f and takes a
// fresh view (undefined before the dialog exists).
export type Act = <T>(f: (d: CreateDialog) => T) => T | undefined;
