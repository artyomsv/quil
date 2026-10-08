<script lang="ts">
  import type { App } from '../lib/app.svelte';
  import type { MenuItem } from '../lib/menu';
  import { sanitizeRemoteText } from '../lib/sanitize';
  import Menu from './Menu.svelte';

  interface Props {
    app: App;
    projectId: string;
    group: string;
  }

  let { app, projectId, group }: Props = $props();
  // The TUI's project menu, as far as the browser serves it.
  const items: MenuItem[] = $derived([
    { label: 'Rename…', run: () => app.openPanel({ kind: 'project_rename', projectId }) },
    ...(app.state?.groups ?? [])
      .filter((g) => g !== group)
      .map((g) => ({ label: `Move to ${sanitizeRemoteText(g)}`, run: () => app.fileProject(projectId, g) })),
    { label: 'Move to new group…', run: () => app.openPanel({ kind: 'group_new', projectId }) },
    ...(group ? [{ label: 'Remove from group', run: () => app.fileProject(projectId, '') }] : []),
    {
      label: 'Remove project…',
      run: () => app.openPanel({ kind: 'project_remove', projectId }),
      key: app.keyFor('project.destroy'),
    },
  ]);
</script>

<Menu label="Project menu" {items} onclose={() => app.focusActiveSoon()} />
