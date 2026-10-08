<script lang="ts">
  import type { App } from '../lib/app.svelte';
  import { type ProjectItem, sidebarSections } from '../lib/view';
  import AgentDot from './AgentDot.svelte';
  import GroupHeader from './GroupHeader.svelte';
  import ProjectMenu from './ProjectMenu.svelte';

  interface Props {
    app: App;
  }

  let { app }: Props = $props();
  // The daemon's groups first, then the ungrouped projects (and the
  // "Other tabs" bucket), as the TUI's sidebar shows them.
  const sections = $derived(sidebarSections(app.sidebar, app.state?.groups ?? []));
</script>

<nav>
  {#each sections as sec (sec.group)}
    <div class:group={sec.group !== ''}>
      {#if sec.group !== ''}
        <GroupHeader {app} name={sec.group} collapsed={app.collapsedGroups.has(sec.group)} ontoggle={() => app.toggleGroup(sec.group)} />
      {/if}
      {#if sec.group === '' || !app.collapsedGroups.has(sec.group)}
        {#each sec.projects as project (project.id)}
          {@render projectBlock(project)}
        {/each}
      {/if}
    </div>
  {:else}
    <p class="empty">—</p>
  {/each}
</nav>

{#snippet projectBlock(project: ProjectItem)}
    <section>
      <div class="row">
        <button
          class="project"
          class:active={project.active}
          disabled={app.readOnly || project.id === ''}
          onclick={() => app.switchProject(project.id)}
        >
          {project.name || '—'}
        </button>
        {#if app.editable && project.id !== ''}<ProjectMenu {app} projectId={project.id} group={project.group} />{/if}
      </div>
      <ul>
        {#each project.tabs as tab (tab.id)}
          <li>
            <button class="tab" class:active={tab.active} disabled={app.readOnly} onclick={() => app.switchTab(tab.id)}>
              <span class="name">{tab.name || '—'}</span>
              {#if tab.unseen}<span class="unread" aria-hidden="true" title="finished while away">•</span>{/if}
              <span class="dots">
                {#each tab.dots as dot (dot.id)}
                  <AgentDot state={dot.state} title="{dot.name}: {dot.state}" />
                {/each}
              </span>
            </button>
          </li>
        {/each}
      </ul>
    </section>
{/snippet}

<style>
  nav {
    width: 220px;
    flex: none;
    overflow-y: auto;
    border-right: 1px solid #2a2e37;
    background: #111318;
    padding: 6px 0;
  }

  button {
    display: flex;
    align-items: center;
    gap: 6px;
    width: 100%;
    border: 0;
    background: none;
    color: inherit;
    font: inherit;
    text-align: left;
    cursor: pointer;
  }

  button:disabled {
    cursor: default;
  }

  .project {
    padding: 6px 10px;
    color: #9aa0ad;
    font-weight: 600;
  }

  .project.active {
    color: #e6e8ee;
  }

  .row {
    display: flex;
    align-items: center;
    padding-right: 4px;
  }

  ul {
    list-style: none;
    margin: 0;
    padding: 0;
  }

  .tab {
    padding: 4px 10px 4px 20px;
  }

  .tab.active {
    background: #232733;
  }

  .name {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .unread {
    flex: none;
    color: #5cc27a;
  }

  .dots {
    display: flex;
    gap: 3px;
  }

  .empty {
    margin: 10px;
    color: #9aa0ad;
  }
</style>
