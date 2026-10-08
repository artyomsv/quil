<script lang="ts">
  import type { App } from '../lib/app.svelte';
  import type { MenuItem } from '../lib/menu';
  import Menu from './Menu.svelte';

  interface Props {
    app: App;
  }

  let { app }: Props = $props();
  // Alt+P: the TUI's project picker, reduced to a switch list.
  const items: MenuItem[] = $derived(
    // sidebarModel already cleaned the names; '' is its "Other tabs" bucket.
    app.sidebar
      .filter((p) => p.id !== '')
      .map((p) => ({
        label: `${p.active ? '• ' : ''}${p.name || '—'}`,
        run: () => app.switchProject(p.id),
        disabled: app.readOnly || p.active,
      })),
  );

  function closed(): void {
    if (app.panel?.kind === 'projects') app.closePanel();
  }
</script>

<Menu auto label="Switch project" {items} onclose={closed} />
