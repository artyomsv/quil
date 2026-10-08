<script lang="ts">
  import type { App } from '../lib/app.svelte';
  import type { MenuItem } from '../lib/menu';
  import Menu from './Menu.svelte';

  interface Props {
    app: App;
  }

  let { app }: Props = $props();
  // The TUI's About menu, reduced to what the browser serves (spec §2). Each
  // later screen adds its row here.
  const items: MenuItem[] = $derived([
    {
      label: 'Shortcuts',
      run: () => {
        app.panel = null;
        app.openKeyList();
      },
    },
    // Viewing processes is an act-class request (the daemon's table); kill
    // inside the page needs full rights.
    { label: 'Processes', run: () => app.openPanel({ kind: 'processes' }), disabled: app.refusalFor('act') !== '' },
    { label: 'Plugins', run: () => app.openPanel({ kind: 'plugins' }) },
    { label: 'Update', run: () => app.openPanel({ kind: 'update' }) },
  ]);

  // A pick runs before the menu reports closing; a row that opened another
  // panel keeps it, so only the menu's own panel is closed here.
  function closed(): void {
    if (app.panel?.kind === 'help') app.closePanel();
  }
</script>

<Menu auto label="Help" {items} onclose={closed} />
