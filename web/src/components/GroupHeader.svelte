<script lang="ts">
  import type { App } from '../lib/app.svelte';
  import type { MenuItem } from '../lib/menu';
  import { sanitizeRemoteText } from '../lib/sanitize';
  import Menu from './Menu.svelte';

  interface Props {
    app: App;
    name: string;
    collapsed: boolean;
    ontoggle: () => void;
  }

  let { app, name, collapsed, ontoggle }: Props = $props();
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
  <button class="toggle" aria-expanded={!collapsed} onclick={ontoggle}>{collapsed ? '▸' : '▾'} {sanitizeRemoteText(name)}</button>
  {#if app.editable}<Menu label="Group menu" {items} onclose={() => app.focusActiveSoon()} />{/if}
</div>

<style>
  .head {
    display: flex;
    align-items: center;
    padding: 4px 6px 0;
  }

  .toggle {
    flex: 1;
    border: 0;
    background: none;
    color: #6b7180;
    font: inherit;
    font-size: 12px;
    text-align: left;
    cursor: pointer;
  }
</style>
