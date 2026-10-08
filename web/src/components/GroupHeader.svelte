<script lang="ts">
  import type { App } from '../lib/app.svelte';
  import type { MenuItem } from '../lib/menu';
  import { sanitizeRemoteText } from '../lib/sanitize';
  import Menu from './Menu.svelte';

  interface Props {
    app: App;
    name: string;
    // The group's member count, shown as in the TUI ("▾ name (N)").
    count: number;
    collapsed: boolean;
    ontoggle: () => void;
  }

  let { app, name, count, collapsed, ontoggle }: Props = $props();
  const items: MenuItem[] = $derived([
    {
      label: 'Rename…',
      run: () => app.openPanel({ kind: 'group_rename', name }),
      disabled: app.groupBusy.has(name),
      reason: 'a rename of this group is still waiting for the daemon',
    },
    { label: 'Delete…', run: () => app.openPanel({ kind: 'group_delete', name }) },
  ]);
</script>

<div class="head">
  <button class="toggle" aria-expanded={!collapsed} onclick={ontoggle}>
    <span class="chev" aria-hidden="true">{collapsed ? '▸' : '▾'}</span>
    <span class="label">{sanitizeRemoteText(name)}</span>
    <span class="count">({count})</span>
  </button>
  {#if app.editable}<Menu label="Group menu" {items} onclose={() => app.focusActiveSoon()} />{/if}
</div>

<style>
  .head {
    display: flex;
    align-items: center;
    padding: 4px 4px 4px 0;
    background: #1a1e27;
  }

  .toggle {
    flex: 1;
    min-width: 0;
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 2px 6px;
    border: 0;
    background: none;
    color: #8f9bb3;
    font: inherit;
    font-size: 11px;
    font-weight: 600;
    letter-spacing: 0.06em;
    text-transform: uppercase;
    text-align: left;
    cursor: pointer;
  }

  .chev {
    width: 10px;
    flex: none;
  }

  .label {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .count {
    flex: none;
    color: #6b7180;
    font-weight: 400;
  }
</style>
