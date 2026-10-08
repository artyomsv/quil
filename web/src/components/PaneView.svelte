<script lang="ts">
  import { untrack } from 'svelte';
  import type { App } from '../lib/app.svelte';
  import type { MenuItem } from '../lib/menu';
  import type { AgentDot } from '../lib/view';
  import AgentDotView from './AgentDot.svelte';
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
    // A tab's overlay (lazygit, hunk): no pane menu, never the active pane.
    overlay?: boolean;
  }

  let { app, paneId, name, spawnError, muted, worktreeOwned, agent, overlay = false }: Props = $props();
  // The dialog aims at the active pane, so this pane becomes it first.
  function createFrom(mode: 'pane' | 'replace'): void {
    app.setActivePane(paneId);
    app.openCreate(mode);
  }
  const items: MenuItem[] = $derived([
    { label: 'Split right', run: () => app.splitQuick(paneId, 'right'), key: app.keyFor('pane.split_h') },
    { label: 'Split below', run: () => app.splitQuick(paneId, 'below'), key: app.keyFor('pane.split_v') },
    { label: 'New pane…', run: () => createFrom('pane'), key: app.keyFor('builtin.new_pane') },
    { label: 'Replace…', run: () => createFrom('replace') },
    { label: 'Rename…', run: () => app.startRenamePane(paneId), key: app.keyFor('pane.rename') },
    { label: 'Notes…', run: () => app.openNotes(paneId), key: app.keyFor('pane.notes_toggle') },
    { label: 'Input history…', run: () => app.openHistory(paneId), key: app.keyFor('pane.command_history') },
    { label: muted ? 'Unmute' : 'Mute', run: () => app.setMuted(paneId, !muted), key: app.keyFor('pane.mute') },
    { label: 'Restart', run: () => app.restartPane(paneId), key: app.keyFor('pane.restart') },
    ...app.tabBar
      .filter((t) => !t.active)
      .map((t) => ({ label: `Move to ${t.name || '—'}`, run: () => app.movePane(paneId, t.id) })),
    { label: 'Close…', run: () => app.askClosePane(paneId), key: app.keyFor('pane.close') },
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
<div class="pane" class:active={overlay || app.activePane === paneId} onfocusin={() => app.setActivePane(paneId)}>
  <div class="title">
    <AgentDotView state={agent} title="Agent: {agent}" />
    <span class="name">{name}</span>
    {#if muted}<span class="mark" title="Muted">muted</span>{/if}
    {#if app.editable && !overlay}<Menu label="Pane menu" {items} onclose={() => app.focusActiveSoon()} />{/if}
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
      app.closePaneAsk();
      app.renamePane(paneId, v);
    }}
    oncancel={() => app.closePaneAsk()}
  />
{:else if ask === 'close'}
  <Confirm
    title="Close pane"
    body={`Close ${name}?`}
    confirmLabel="Close"
    checkLabel={worktreeOwned ? 'Also remove its worktree' : undefined}
    onconfirm={(rm) => {
      app.closePaneAsk();
      app.closePane(paneId, rm);
    }}
    oncancel={() => app.closePaneAsk()}
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
