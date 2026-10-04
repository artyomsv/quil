<script lang="ts">
  import type { App } from '../lib/app.svelte';

  interface Props {
    app: App;
  }

  let { app }: Props = $props();
</script>

<header>
  <div class="tabs">
    {#each app.tabBar as tab (tab.id)}
      <button class="tab" class:active={tab.active} disabled={app.readOnly} onclick={() => app.switchTab(tab.id)}>
        {tab.name || '—'}
      </button>
    {/each}
  </div>
  {#if app.readOnly}
    <span class="badge">read-only</span>
  {:else if app.state && !app.isMaster}
    <button class="control" onclick={() => app.takeControl()}>Take control</button>
  {/if}
</header>

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

  .tab {
    flex: none;
    max-width: 200px;
    padding: 0 12px;
    border: 0;
    border-right: 1px solid #2a2e37;
    background: none;
    color: #9aa0ad;
    font: inherit;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    cursor: pointer;
  }

  .tab.active {
    background: #1b1e26;
    color: #e6e8ee;
  }

  .tab:disabled {
    cursor: default;
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
