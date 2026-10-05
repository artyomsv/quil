<script lang="ts">
  import { TAB_COLORS, tabColorCss } from '../lib/actions';
  import type { App } from '../lib/app.svelte';
  import type { MenuItem } from '../lib/menu';
  import Confirm from './Confirm.svelte';
  import Menu from './Menu.svelte';
  import Prompt from './Prompt.svelte';

  interface Props {
    app: App;
  }

  let { app }: Props = $props();

  function menuFor(id: string): MenuItem[] {
    return [
      { label: 'Rename…', run: () => app.startRenameTab(id) },
      ...TAB_COLORS.map((c) => ({ label: `Colour: ${c.label}`, run: () => app.setTabColor(id, c.value) })),
      { label: 'Close…', run: () => app.askCloseTab(id) },
    ];
  }

  // The tab an open rename or close dialog is about, with the name it shows.
  const asked = $derived.by(() => {
    const a = app.tabAsk;
    if (!a || !app.editable) return null;
    const tab = app.tabBar.find((t) => t.id === a.tabId) ?? app.sidebar.flatMap((p) => p.tabs).find((t) => t.id === a.tabId);
    return { ...a, name: tab?.name ?? '' };
  });
</script>

<header>
  <div class="tabs">
    {#each app.tabBar as tab (tab.id)}
      <span class="tab-item" class:active={tab.active} style:border-left-color={tabColorCss(tab.color)}>
        <button
          class="tab"
          class:active={tab.active}
          disabled={app.readOnly}
          onclick={() => app.switchTab(tab.id)}
          ondblclick={() => app.startRenameTab(tab.id)}
        >
          {tab.name || '—'}
        </button>
        {#if app.editable}
          <Menu label="Tab menu" items={menuFor(tab.id)} />
          <button class="close" aria-label="Close tab {tab.name || '—'}" title="Close tab" onclick={() => app.askCloseTab(tab.id)}>×</button>
        {/if}
      </span>
    {/each}
  </div>
  {#if app.readOnly}
    <span class="badge">read-only</span>
  {:else if app.state && !app.isMaster}
    <button class="control" onclick={() => app.takeControl()}>Take control</button>
  {/if}
</header>
{#if asked?.kind === 'rename'}
  <Prompt
    title="Rename tab"
    value={asked.name}
    submitLabel="Rename"
    onsubmit={(v) => {
      const id = app.tabAsk?.tabId ?? '';
      app.tabAsk = null;
      app.renameTab(id, v);
    }}
    oncancel={() => (app.tabAsk = null)}
  />
{:else if asked?.kind === 'close'}
  <Confirm
    title="Close tab"
    body={`Close ${asked.name || 'this tab'} and every pane in it?`}
    confirmLabel="Close"
    onconfirm={() => {
      const id = app.tabAsk?.tabId ?? '';
      app.tabAsk = null;
      app.closeTab(id);
    }}
    oncancel={() => (app.tabAsk = null)}
  />
{/if}

<style>
  header {
    display: flex;
    align-items: stretch;
    gap: 8px;
    min-height: 32px;
    border-bottom: 1px solid #2a2e37;
    background: #111318;
  }

  .tabs {
    flex: 1;
    min-width: 0;
    display: flex;
    overflow-x: auto;
  }

  .tab-item {
    flex: none;
    display: flex;
    align-items: center;
    max-width: 260px;
    border-left: 3px solid transparent;
    border-right: 1px solid #2a2e37;
  }

  .tab-item.active {
    background: #1b1e26;
  }

  .tab {
    flex: 1;
    min-width: 0;
    align-self: stretch;
    padding: 0 12px;
    border: 0;
    background: none;
    color: #9aa0ad;
    font: inherit;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    cursor: pointer;
  }

  .tab.active {
    color: #e6e8ee;
  }

  .tab:disabled {
    cursor: default;
  }

  .close {
    flex: none;
    margin-right: 4px;
    padding: 0 4px;
    border: 0;
    background: none;
    color: #9aa0ad;
    font: inherit;
    cursor: pointer;
  }

  .badge {
    align-self: center;
    margin-right: 10px;
    padding: 2px 8px;
    border-radius: 10px;
    background: #2f3442;
    color: #c4c8d2;
    font-size: 12px;
  }

  .control {
    align-self: center;
    margin-right: 8px;
    padding: 3px 10px;
    border: 1px solid #3d6fd8;
    border-radius: 4px;
    background: none;
    color: #9dbaf5;
    font: inherit;
    cursor: pointer;
  }
</style>
