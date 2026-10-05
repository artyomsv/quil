<script lang="ts">
  import { untrack } from 'svelte';
  import type { App } from '../lib/app.svelte';
  import type { MenuItem } from '../lib/menu';
  import type { AgentDot } from '../lib/view';
  import Confirm from './Confirm.svelte';
  import Menu from './Menu.svelte';
  import Prompt from './Prompt.svelte';

  interface Props {
    app: App;
    paneId: string;
    name: string;
    spawnError: string;
    muted: boolean;
    worktreeOwned: boolean;
    agent: AgentDot;
  }

  let { app, paneId, name, spawnError, muted, worktreeOwned, agent }: Props = $props();
  // The dialog aims at the active pane, so this pane becomes it first.
  function createFrom(mode: 'pane' | 'replace'): void {
    app.setActivePane(paneId);
    app.openCreate(mode);
  }
  const items: MenuItem[] = $derived([
    { label: 'Split right', run: () => app.splitQuick(paneId, 'right') },
    { label: 'Split below', run: () => app.splitQuick(paneId, 'below') },
    { label: 'New pane…', run: () => createFrom('pane') },
    { label: 'Replace…', run: () => createFrom('replace') },
    { label: 'Rename…', run: () => app.startRenamePane(paneId) },
    { label: muted ? 'Unmute' : 'Mute', run: () => app.setMuted(paneId, !muted) },
    { label: 'Restart', run: () => app.restartPane(paneId) },
    ...app.tabBar
      .filter((t) => !t.active)
      .map((t) => ({ label: `Move to ${t.name || '—'}`, run: () => app.movePane(paneId, t.id) })),
    { label: 'Close…', run: () => app.askClosePane(paneId) },
  ]);
  // A dialog renders only while it could still send: live and not read-only.
  const ask = $derived(app.editable && app.paneAsk?.paneId === paneId ? app.paneAsk.kind : null);
  let host: HTMLDivElement | undefined = $state();
  // Every workspace_state hands the pane new prop objects with the same
  // values. The effect reads these deriveds, which change only with the
  // value, so a state frame does not detach the terminal and rebuild its
  // WebGL renderer.
  const id = $derived(paneId);
  const failed = $derived(spawnError !== '');

  // The terminal moves into host while the pane is shown and out again when
  // it is not; a pane with a spawn error shows the error instead.
  $effect(() => {
    const el = host;
    const pane = id;
    if (!el || failed) return;
    untrack(() => app.paneShown(pane, el));
    const ro = new ResizeObserver((entries) => {
      const r = entries[0]?.contentRect;
      if (r) app.measure(pane, r.width, r.height);
    });
    ro.observe(el);
    return () => {
      ro.disconnect();
      untrack(() => app.paneHidden(pane));
    };
  });
</script>

<!-- Focus anywhere in the pane (its terminal, its menu) makes it this tab's
     active pane. -->
<div class="pane" class:active={app.activePane === paneId} onfocusin={() => app.setActivePane(paneId)}>
  <div class="title">
    <span class="dot {agent}" title="Agent: {agent}"></span>
    <span class="name">{name}</span>
    {#if muted}<span class="mark" title="Muted">muted</span>{/if}
    {#if app.editable}<Menu label="Pane menu" {items} />{/if}
  </div>
  {#if spawnError}
    <p class="error">{spawnError}</p>
  {:else}
    <div class="term" bind:this={host}></div>
  {/if}
</div>
{#if ask === 'rename'}
  <Prompt
    title="Rename pane"
    value={name}
    submitLabel="Rename"
    onsubmit={(v) => {
      app.paneAsk = null;
      app.renamePane(paneId, v);
    }}
    oncancel={() => (app.paneAsk = null)}
  />
{:else if ask === 'close'}
  <Confirm
    title="Close pane"
    body={`Close ${name}?`}
    confirmLabel="Close"
    checkLabel={worktreeOwned ? 'Also remove its worktree' : undefined}
    onconfirm={(rm) => {
      app.paneAsk = null;
      app.closePane(paneId, rm);
    }}
    oncancel={() => (app.paneAsk = null)}
  />
{/if}

<style>
  .pane {
    display: flex;
    flex-direction: column;
    width: 100%;
    height: 100%;
    border: 1px solid #2a2e37;
    background: #000;
  }

  .pane.active {
    border-color: #3d6fd8;
  }

  .title {
    flex: none;
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 1px 6px;
    background: #1b1e26;
    color: #9aa0ad;
    font-size: 12px;
  }

  /* The agent dot: the same colours as the sidebar's. */
  .dot {
    flex: none;
    width: 7px;
    height: 7px;
    border-radius: 50%;
    background: #4a4f5c;
  }

  .dot.working {
    background: #4f9be8;
  }

  .dot.blocked {
    background: #e8a24f;
  }

  .dot.idle {
    background: #5cc27a;
  }

  .name {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .mark {
    flex: none;
    color: #5c6170;
  }

  .term {
    flex: 1;
    min-height: 0;
    overflow: hidden;
  }

  .error {
    margin: 8px;
    color: #f08a8a;
    white-space: pre-wrap;
  }
</style>
