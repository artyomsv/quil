<script lang="ts">
  import { untrack } from 'svelte';
  import type { App } from '../lib/app.svelte';
  import { groupNameError } from '../lib/projects';
  import { sanitizeRemoteText } from '../lib/sanitize';
  import Confirm from './Confirm.svelte';
  import Menu from './Menu.svelte';
  import Prompt from './Prompt.svelte';
  import ProjectForm from './ProjectForm.svelte';

  interface Props {
    app: App;
  }

  let { app }: Props = $props();
  const p = $derived(app.panel);
  // The panel this mount shows: an answer that comes after a cancel closes
  // only it, never a newer one.
  const mine = untrack(() => app.panel);
  const groups = $derived(app.state?.groups ?? []);

  // removeBody names what a project's removal takes with it.
  function removeBody(projectId: string): string {
    const s = app.state;
    const proj = s?.projects.find((x) => x.id === projectId);
    const tabs = new Set(proj?.tab_ids ?? []);
    const panes = (s?.panes ?? []).filter((x) => tabs.has(x.tab_id) && !x.overlay).length;
    const name = sanitizeRemoteText(proj?.name ?? '');
    return `Remove the project "${name}"? Every tab and pane in this project is destroyed too. (${tabs.size} tabs, ${panes} panes)`;
  }

  // The projects a tab may move to: every one but its own.
  function moveTargets(tabId: string): { id: string; name: string }[] {
    const s = app.state;
    const own = s?.tabs.find((t) => t.id === tabId)?.project_id;
    return (s?.projects ?? []).filter((x) => x.id !== own).map((x) => ({ id: x.id, name: sanitizeRemoteText(x.name) || '—' }));
  }

  function closeMove(): void {
    if (app.panel?.kind === 'move_tab') app.closePanel();
  }
</script>

{#if p?.kind === 'project_new'}
  <ProjectForm {app} />
{:else if p?.kind === 'project_rename'}
  <ProjectForm {app} projectId={p.projectId} />
{:else if p?.kind === 'project_remove'}
  {@const id = p.projectId}
  <Confirm
    title="Remove project"
    body={removeBody(id)}
    confirmLabel="Remove"
    onconfirm={() => {
      app.removeProject(id);
      app.closePanel();
    }}
    oncancel={() => app.closePanel()}
  />
{:else if p?.kind === 'group_new'}
  {@const id = p.projectId}
  <Prompt
    title="New group"
    value=""
    submitLabel="Create"
    onsubmit={(v) => {
      const err = groupNameError(v, groups);
      if (err) app.showNotice(err);
      else {
        app.fileProject(id, v.trim());
        app.closePanel();
      }
    }}
    oncancel={() => app.closePanel()}
  />
{:else if p?.kind === 'group_rename'}
  {@const name = p.name}
  <Prompt
    title="Rename group"
    value={name}
    submitLabel="Rename"
    onsubmit={async (v) => {
      const err = groupNameError(v, groups, name);
      if (err) {
        app.showNotice(err);
        return;
      }
      if (v.trim() === name) {
        app.closePanel();
        return;
      }
      const o = await app.groupOp('rename', name, v.trim());
      if (o.ok) app.closePanelIf(mine);
    }}
    oncancel={() => app.closePanel()}
  />
{:else if p?.kind === 'group_delete'}
  {@const name = p.name}
  <Confirm
    title="Delete group"
    body={`Delete the group "${sanitizeRemoteText(name)}"? Its projects become ungrouped.`}
    confirmLabel="Delete"
    onconfirm={async () => {
      await app.groupOp('delete', name);
      app.closePanelIf(mine);
    }}
    oncancel={() => app.closePanel()}
  />
{:else if p?.kind === 'move_tab'}
  {@const tabId = p.tabId}
  <Menu
    auto
    label="Move tab to project"
    items={moveTargets(tabId).map((x) => ({ label: x.name, run: () => app.moveTab(tabId, x.id) }))}
    onclose={closeMove}
  />
{/if}
