<script lang="ts">
  import { onDestroy, onMount } from 'svelte';
  import type { App } from '../lib/app.svelte';
  import { CreateDialog, type DialogOpen, type DialogView, type ListKind, viewOf } from '../lib/dialog';
  import type { SplitPaneReq } from '../lib/protocol';
  import { sanitizeRemoteText } from '../lib/sanitize';
  import type { Placement } from '../lib/protocol';
  import InstanceStep from './dialog/InstanceStep.svelte';
  import PlacementStep from './dialog/PlacementStep.svelte';
  import PluginStep from './dialog/PluginStep.svelte';
  import SetupStep from './dialog/SetupStep.svelte';

  interface Props {
    app: App;
  }

  let { app }: Props = $props();

  // The dialog's state machine is a plain class; every change goes through
  // act(), which takes a fresh view of it for the template.
  let dlg: CreateDialog | null = null;
  let view = $state.raw<DialogView | null>(null);
  // The dialog this component was opened for, and whether it has gone: an
  // answer that lands after it closed must not touch a NEWER dialog.
  let opened: DialogOpen | null = null;
  let closed = false;
  let root: HTMLDivElement | undefined = $state();

  onDestroy(() => {
    closed = true;
  });

  // act changes the machine and takes a fresh view. The machine hands its
  // finished request out once (takeRequest), so nothing that runs later — a
  // list answer that lands after the submit — can send it again.
  function act<T>(f: (d: CreateDialog) => T): T | undefined {
    const d = dlg;
    if (!d || closed) return undefined;
    const r = f(d);
    view = viewOf(d);
    const req = d.takeRequest();
    if (req) void finish(req);
    return r;
  }

  // close closes this dialog, never one opened after it.
  function close(): void {
    if (!closed && app.dialog === opened) app.closeDialog();
  }

  const TITLES: Record<string, string> = {
    category: 'New pane — type',
    plugin: 'New pane — plugin',
    instances: 'New pane — saved instances',
    form: 'New pane — instance',
    setup: 'New pane — setup',
    placement: 'New pane — placement',
    done: 'New pane — creating…',
  };
  const title = $derived(view ? (view.mode === 'replace' ? TITLES[view.step]?.replace('New pane', 'Replace pane') : TITLES[view.step]) : 'New pane');

  onMount(() => {
    const info = app.client;
    opened = app.dialog;
    if (!info || !opened) return;
    dlg = new CreateDialog(info, null, opened);
    view = viewOf(dlg);
    // Availability is this daemon's answer (spec §5.2); a daemon too old to
    // answer keeps every plugin offered.
    void ask('plugins', 'plugin_list_req', {});
  });

  $effect(() => {
    if (view && root && !root.contains(document.activeElement)) {
      root.querySelector<HTMLElement>('button:not(:disabled), input, select')?.focus();
    }
  });

  // ask runs one daemon list for the dialog and files its answer; an answer
  // that arrives after a newer request of its kind is dropped.
  async function ask(kind: ListKind, type: string, payload: unknown): Promise<void> {
    const n = act((d) => d.scanning(kind));
    if (n === undefined) return;
    const out = await app.daemonList(type, payload);
    act((d) => d.listed(kind, out, n));
  }

  // browse lists a folder (child descends into one of its entries) and makes
  // the daemon's resolved path the dialog's folder: the join, `~` and
  // symlinks belong to the machine holding the disk.
  async function browse(path: string, child = ''): Promise<void> {
    const n = act((d) => d.scanning('folders'));
    if (n === undefined) return;
    const out = await app.daemonList('browse_dir_req', { path, child });
    act((d) => d.listed('folders', out, n));
    const resolved = out.ok ? (out.reply?.payload as { resolved?: unknown } | undefined)?.resolved : undefined;
    if (dlg && dlg.lists.folders.reply === out.reply && typeof resolved === 'string' && resolved !== '') setFolder(resolved, false);
  }

  // setFolder makes cwd the dialog's folder and asks again every list scoped
  // to it.
  function setFolder(cwd: string, list: boolean): void {
    const d = dlg;
    if (!d) return;
    const changed = cwd !== d.cwd;
    act((x) => x.folderChanged(cwd));
    if (list) void browse(cwd);
    if (changed || d.lists.worktrees.status === 'idle') folderLists();
  }

  function folderLists(): void {
    const d = dlg;
    if (!d?.plugin) return;
    if (d.showWorktree) void ask('worktrees', 'worktree_list_req', { path: d.cwd });
    if (d.plugin.discover === 'git') void ask('repos', 'git_repos_req', { cwd: d.cwd });
    sessionsList();
  }

  function sessionsList(): void {
    const d = dlg;
    if (d?.plugin?.sessions && d.spawnDir !== '') void ask('sessions', 'claude_sessions_req', { cwd: d.spawnDir });
  }

  // enteredSetup asks for every list the setup rows show.
  function enteredSetup(): void {
    const d = dlg;
    if (!d?.plugin || d.step !== 'setup') return;
    if (d.plugin.prompts_cwd) {
      void browse(d.cwd);
      void ask('sandbox', 'sandbox_cap_req', {});
    }
    if (d.plugin.discover === 'kube') void ask('kube', 'kube_ctx_req', {});
    folderLists();
  }

  // retry asks a failed list again.
  function retry(kind: ListKind): void {
    const d = dlg;
    if (!d) return;
    switch (kind) {
      case 'plugins':
        void ask('plugins', 'plugin_list_req', {});
        return;
      case 'folders':
        void browse(d.cwd);
        return;
      case 'kube':
        void ask('kube', 'kube_ctx_req', {});
        return;
      case 'sandbox':
        void ask('sandbox', 'sandbox_cap_req', {});
        return;
      case 'sessions':
        sessionsList();
        return;
      case 'repos':
        if (d.plugin?.discover === 'git') void ask('repos', 'git_repos_req', { cwd: d.cwd });
        return;
      case 'worktrees':
        void ask('worktrees', 'worktree_list_req', { path: d.cwd });
        return;
      default:
        folderLists();
    }
  }

  function pickPlugin(name: string): void {
    act((d) => d.pickPlugin(name));
    enteredSetup();
  }

  function pickInstance(id: string): void {
    act((d) => d.pickInstance(id));
    enteredSetup();
  }

  function back(): void {
    const went = act((d) => d.back());
    if (!went) close();
  }

  function submit(p: Placement): void {
    act((d) => d.submit(p));
  }

  // refreshInstances reloads /api/client after an instance write, so the
  // list shows what instances.json now holds.
  async function refreshInstances(): Promise<boolean> {
    if (!(await app.refreshClient())) return false;
    const info = app.client;
    if (info && dlg) dlg.info = info;
    return true;
  }

  // finish sends the request. sendSplit makes the answered pane active and
  // focuses it (a preparing worktree's placeholder is followed by the pane
  // that replaces it), so closing only has to put the dialog away.
  async function finish(req: SplitPaneReq): Promise<void> {
    const out = await app.sendSplit(req);
    // A timeout is "still working": the pane comes when it is ready, and
    // the banner says so.
    if (out.ok || (!out.ok && out.code === 'timeout')) {
      close();
      closed = true;
      return;
    }
    act((x) => x.refused(out.error));
  }
</script>

<div class="backdrop" data-modal role="presentation" onclick={close}>
  <div
    class="dialog"
    role="dialog"
    aria-modal="true"
    aria-label="New pane"
    tabindex="-1"
    bind:this={root}
    onclick={(e) => e.stopPropagation()}
    onkeydown={(e) => {
      if (e.key === 'Escape') {
        e.stopPropagation();
        e.preventDefault();
        back();
      }
    }}
  >
    <h2>{title}</h2>
    {#if !view}
      <p class="muted">Loading plugins…</p>
    {:else if view.step === 'category'}
      <div class="list">
        {#each view.categories as c (c.key)}
          <button class="row" onclick={() => act((d) => d.pickCategory(c.key))}>{c.label}</button>
        {/each}
      </div>
    {:else if view.step === 'plugin'}
      <PluginStep {view} onpick={pickPlugin} onretry={retry} />
    {:else if view.step === 'instances' || view.step === 'form'}
      <InstanceStep {app} {view} {act} onpick={pickInstance} onrefresh={refreshInstances} />
    {:else if view.step === 'setup'}
      <SetupStep
        {app}
        {view}
        {act}
        onbrowse={browse}
        onfolder={(cwd) => setFolder(cwd, true)}
        onretry={retry}
        onworktree={sessionsList}
      />
    {:else if view.step === 'placement'}
      <PlacementStep {view} onpick={submit} />
    {:else}
      <p class="muted">Creating the pane…</p>
    {/if}
    {#if view && view.step !== 'setup' && view.error}
      <p class="error" role="alert">{sanitizeRemoteText(view.error)}</p>
    {/if}
    <div class="buttons">
      <button onclick={back}>{view && view.step !== 'category' && view.step !== 'done' ? 'Back' : 'Cancel'}</button>
    </div>
  </div>
</div>

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    z-index: 30;
    background: rgb(0 0 0 / 50%);
    display: grid;
    place-items: center;
  }

  .dialog {
    width: min(560px, calc(100vw - 32px));
    max-height: calc(100vh - 32px);
    overflow-y: auto;
    box-sizing: border-box;
    padding: 16px;
    background: #1b1e26;
    border: 1px solid #2a2e37;
    color: #e6e8ee;
  }

  h2 {
    margin: 0 0 10px;
    font-size: 15px;
  }

  .list {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }

  .row {
    text-align: left;
    padding: 6px 10px;
    border: 1px solid #3a3f4b;
    background: none;
    color: inherit;
    font: inherit;
    cursor: pointer;
  }

  .row:hover,
  .row:focus-visible {
    border-color: #3d6fd8;
  }

  .muted {
    color: #9aa0ad;
  }

  .error {
    color: #f08a8a;
    overflow-wrap: anywhere;
  }

  .buttons {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
    margin-top: 12px;
  }

  .buttons button {
    padding: 4px 12px;
    border: 1px solid #3a3f4b;
    background: none;
    color: inherit;
    font: inherit;
    cursor: pointer;
  }
</style>
