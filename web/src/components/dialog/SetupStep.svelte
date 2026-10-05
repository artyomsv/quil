<script lang="ts">
  import type { App } from '../../lib/app.svelte';
  import { type Act, type DialogView, type ListKind, SIGN_IN_CHOICES } from '../../lib/dialog';
  import { sanitizeRemoteText } from '../../lib/sanitize';
  import FolderField from './FolderField.svelte';

  interface Props {
    app: App;
    view: DialogView;
    act: Act;
    onbrowse: (path: string, child?: string) => void;
    onfolder: (cwd: string) => void;
    onretry: (kind: ListKind) => void;
    // onworktree runs after the worktree choice changed: the session list is
    // scoped to the folder that choice settles on.
    onworktree: () => void;
  }

  let { app, view, act, onbrowse, onfolder, onretry, onworktree }: Props = $props();

  // Not a path: every listed worktree is an absolute path.
  const NEW_BRANCH = 'new-branch';
  let reason = $state('');
  // The branch field's own message, set on Enter (Continue checks again).
  let branchMsg = $state('');

  interface KubeList {
    contexts?: { name: string; namespace?: string; current?: boolean }[];
  }
  interface WorktreeList {
    repo?: boolean;
    root?: string;
    worktrees?: { path: string; branch?: string; main?: boolean; bare?: boolean; prunable?: boolean; detached?: boolean }[];
  }
  interface SessionList {
    sessions?: { id: string; title?: string; modified_ms?: number; in_use_pane_id?: string }[];
  }

  const contexts = $derived((view.lists.kube.reply?.payload as KubeList | undefined)?.contexts ?? []);
  const wt = $derived((view.lists.worktrees.reply?.payload as WorktreeList | undefined) ?? {});
  const worktrees = $derived((wt.worktrees ?? []).filter((w) => !w.bare && !w.prunable));
  const sessions = $derived((view.lists.sessions.reply?.payload as SessionList | undefined)?.sessions ?? []);
  const worktreeChoice = $derived(view.newBranchMode ? NEW_BRANCH : view.existingWorktree);

  function pickWorktree(v: string): void {
    branchMsg = '';
    if (v === NEW_BRANCH) act((d) => d.chooseNewBranch());
    else act((d) => d.chooseWorktree(v));
    onworktree();
  }

  function when(ms: number | undefined): string {
    return ms ? new Date(ms).toLocaleString() : '';
  }

  function cont(): void {
    reason = act((d) => d.continueSetup()) ?? '';
  }
</script>

<div class="rows">
  {#if view.showFolder}
    <FolderField {app} {view} {onbrowse} {onfolder} {onretry} />
  {/if}

  {#if view.showKube}
    <div class="row">
      <label class="name">
        Kube context
        <select value={view.kubeContext} onchange={(e) => act((d) => (d.kubeContext = e.currentTarget.value))}>
          <option value="">Default (the kubeconfig's current context)</option>
          {#each contexts as c (c.name)}
            <option value={c.name}>{sanitizeRemoteText(c.name)}{c.current ? ' ●' : ''}{c.namespace ? ` (${sanitizeRemoteText(c.namespace)})` : ''}</option>
          {/each}
        </select>
      </label>
      {@render status('kube', 'no kube contexts found')}
    </div>
  {/if}

  {#if (view.plugin?.toggles ?? []).length > 0}
    <fieldset class="row">
      <legend>Options</legend>
      {#each view.plugin?.toggles ?? [] as t, i (t.name)}
        <label class="check">
          <input type="checkbox" checked={view.toggles[i] ?? false} onchange={(e) => act((d) => d.setToggle(i, e.currentTarget.checked))} />
          {sanitizeRemoteText(t.label || t.name)}
          {#if t.group}<span class="caption">(one of {sanitizeRemoteText(t.group)})</span>{/if}
        </label>
      {/each}
    </fieldset>
  {/if}

  {#if view.showWorktree}
    <div class="row">
      {#if view.lists.worktrees.status === 'scanning'}
        <span class="caption">Worktrees: scanning…</span>
      {:else if view.lists.worktrees.status === 'failed'}
        <span class="failed">Worktrees: failed: {sanitizeRemoteText(view.lists.worktrees.error ?? '')}</span>
        <button class="chip" onclick={() => onretry('worktrees')}>Retry</button>
      {:else if wt.repo}
        <label class="name">
          Worktree
          <select value={worktreeChoice} onchange={(e) => pickWorktree(e.currentTarget.value)}>
            <option value="">None — this folder</option>
            {#each worktrees as w (w.path)}
              <option value={w.path}>{sanitizeRemoteText(w.branch || (w.detached ? 'detached' : '?'))}{w.main ? ' (main)' : ''} — {sanitizeRemoteText(w.path)}</option>
            {/each}
            <option value={NEW_BRANCH}>New branch…</option>
          </select>
        </label>
        {#if worktreeChoice === NEW_BRANCH}
          <label class="name">
            New branch{view.worktreeRoot ? ` off ${sanitizeRemoteText(view.worktreeRoot)}` : ''}
            <input
              type="text"
              value={view.newBranch}
              autocomplete="off"
              spellcheck="false"
              oninput={(e) => {
                branchMsg = '';
                act((d) => (d.newBranch = e.currentTarget.value));
              }}
              onkeydown={(e) => {
                if (e.key === 'Enter') {
                  e.preventDefault();
                  branchMsg = view.newBranchError;
                }
              }}
            />
          </label>
          {#if branchMsg}
            <span class="failed" role="alert">{branchMsg}</span>
          {/if}
        {/if}
      {:else if view.lists.worktrees.status !== 'idle'}
        <span class="caption">Worktree: not a git repository</span>
      {/if}
    </div>
  {/if}

  {#if view.showSandbox}
    <div class="row">
      <label class="check">
        <input type="checkbox" checked={view.sandboxOn} onchange={(e) => act((d) => (d.sandboxOn = e.currentTarget.checked))} />
        Run in a Docker sandbox
      </label>
      {#if view.sandboxOn}
        <label class="name">
          Container image
          <input
            type="text"
            value={view.sandboxImage}
            autocomplete="off"
            spellcheck="false"
            oninput={(e) => act((d) => (d.sandboxImage = e.currentTarget.value))}
          />
        </label>
      {/if}
    </div>
  {:else if view.plugin?.prompts_cwd}
    <div class="row">
      {#if view.lists.sandbox.status === 'scanning'}
        <span class="caption">Sandbox: checking Docker…</span>
      {:else if view.lists.sandbox.status === 'failed'}
        <span class="caption">Sandbox unavailable: <span class="failed">{sanitizeRemoteText(view.lists.sandbox.error ?? '')}</span></span>
        <button class="chip" onclick={() => onretry('sandbox')}>Retry</button>
      {:else if view.lists.sandbox.status === 'ready'}
        <span class="caption">Sandbox: Docker is not available on this machine</span>
      {/if}
    </div>
  {/if}

  {#if view.showSignIn}
    <fieldset class="row">
      <legend>Sign-in</legend>
      {#each SIGN_IN_CHOICES as c (c.value)}
        <label class="check">
          <input type="radio" name="sandbox-sign-in" value={c.value} checked={view.signIn === c.value} onchange={() => act((d) => (d.signIn = c.value))} />
          {c.label}<span class="caption">{c.detail}</span>
        </label>
      {/each}
    </fieldset>
  {/if}

  {#if view.showSession}
    <fieldset class="row">
      <legend>Resume a Claude session</legend>
      <label class="check">
        <input type="radio" name="resume" checked={view.resumeId === ''} onchange={() => act((d) => (d.resumeId = ''))} />
        Start a new session
      </label>
      {#each sessions as s (s.id)}
        <label class="check" class:busy={!!s.in_use_pane_id}>
          <input
            type="radio"
            name="resume"
            disabled={!!s.in_use_pane_id}
            checked={view.resumeId === s.id}
            onchange={() => act((d) => (d.resumeId = s.id))}
          />
          <span class="title">{sanitizeRemoteText(s.title || s.id)}</span>
          <span class="caption">{when(s.modified_ms)}{s.in_use_pane_id ? ' · in use' : ''}</span>
        </label>
      {/each}
      {@render status('sessions', 'no earlier sessions for this folder')}
    </fieldset>
  {/if}

  {#if reason || view.error}
    <p class="error" role="alert">{sanitizeRemoteText(reason || view.error)}</p>
  {/if}
  <div class="buttons">
    <button class="primary" onclick={cont}>Continue</button>
  </div>
</div>

{#snippet status(kind: ListKind, none: string)}
  {#if view.lists[kind].status === 'scanning'}
    <span class="caption">scanning…</span>
  {:else if view.lists[kind].status === 'empty'}
    <span class="caption">{none}</span>
  {:else if view.lists[kind].status === 'failed'}
    <span class="failed">failed: {sanitizeRemoteText(view.lists[kind].error ?? '')}</span>
    <button class="chip" onclick={() => onretry(kind)}>Retry</button>
  {/if}
{/snippet}

<style>
  .rows {
    display: flex;
    flex-direction: column;
    gap: 10px;
  }

  .row {
    display: flex;
    flex-direction: column;
    gap: 4px;
    margin: 0;
    padding: 0;
    border: 0;
  }

  legend {
    padding: 0;
    margin-bottom: 2px;
    font-size: 12px;
    color: #9aa0ad;
  }

  .name {
    display: flex;
    flex-direction: column;
    gap: 2px;
    font-size: 12px;
    color: #9aa0ad;
  }

  .check {
    display: flex;
    gap: 6px;
    align-items: baseline;
    font-size: 13px;
  }

  .check.busy {
    opacity: 0.5;
  }

  .title {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  input[type='text'],
  select {
    padding: 4px 6px;
    border: 1px solid #3a3f4b;
    background: #111318;
    color: #e6e8ee;
    font: inherit;
    font-size: 13px;
  }

  .caption {
    color: #7d8392;
    font-size: 12px;
  }

  .failed {
    color: #f08a8a;
    font-size: 12px;
    overflow-wrap: anywhere;
  }

  .chip {
    align-self: flex-start;
    padding: 2px 8px;
    border: 1px solid #3a3f4b;
    background: none;
    color: #c4c8d2;
    font: inherit;
    font-size: 12px;
    cursor: pointer;
  }

  .error {
    margin: 0;
    color: #f08a8a;
    overflow-wrap: anywhere;
  }

  .buttons {
    display: flex;
    justify-content: flex-end;
  }

  .primary {
    padding: 4px 12px;
    border: 1px solid #3d6fd8;
    background: none;
    color: #9dbaf5;
    font: inherit;
    cursor: pointer;
  }
</style>
