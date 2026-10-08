<script lang="ts">
  import type { App } from '../lib/app.svelte';
  import { groupMembersShown, type ProjectItem, sidebarSections } from '../lib/view';
  import AgentDot from './AgentDot.svelte';
  import GroupHeader from './GroupHeader.svelte';
  import ProjectMenu from './ProjectMenu.svelte';

  interface Props {
    app: App;
  }

  let { app }: Props = $props();
  // The ungrouped projects (and the "Other tabs" bucket) first, then the
  // daemon's groups, as the TUI's sidebar shows them.
  const sections = $derived(sidebarSections(app.sidebar, app.state?.groups ?? []));
</script>

<nav>
  {#each sections as sec (sec.group)}
    {#if sec.group === ''}
      <div class="ungrouped">
        {#each sec.projects as project (project.id)}
          {@render projectBlock(project)}
        {/each}
      </div>
    {:else}
      {@const collapsed = app.collapsedGroups.has(sec.group)}
      <!-- A group is one framed block: its header, then its members indented
           behind a rule, so where it starts and ends is visible. -->
      <div class="group" class:collapsed>
        <GroupHeader {app} name={sec.group} count={sec.projects.length} {collapsed} ontoggle={() => app.toggleGroup(sec.group)} />
        <div class="members">
          {#each groupMembersShown(sec, collapsed) as project (project.id)}
            {@render projectBlock(project)}
          {:else}
            <p class="none">{sec.projects.length === 0 ? 'no projects' : `${sec.projects.length} hidden`}</p>
          {/each}
        </div>
      </div>
    {/if}
  {:else}
    <p class="empty">—</p>
  {/each}
</nav>

{#snippet projectBlock(project: ProjectItem)}
  {@const folded = app.collapsedProjects.has(project.id)}
  <section class="project-block" class:active={project.active}>
    <div class="row">
      <button
        class="fold"
        aria-expanded={!folded}
        aria-label="{folded ? 'Show' : 'Hide'} the tabs of {project.name || 'this project'}"
        title={folded ? 'Show tabs' : 'Hide tabs'}
        onclick={() => app.toggleProject(project.id)}>{folded ? '▸' : '▾'}</button
      >
      <button
        class="project"
        class:active={project.active}
        disabled={app.readOnly || project.id === ''}
        onclick={() => app.switchProject(project.id)}
      >
        <span class="name">{project.name || '—'}</span>
        {#if folded}<span class="count">{project.tabs.length}</span>{/if}
      </button>
      {#if app.editable && project.id !== ''}<ProjectMenu {app} projectId={project.id} group={project.group} />{/if}
    </div>
    {#if !folded}
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
    {/if}
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

  /* A group: a band for the header, then the members behind a left rule. */
  .group {
    margin: 8px 0 4px;
    border-top: 1px solid #2a2e37;
    background: #14171d;
  }

  .members {
    margin: 0 0 4px 10px;
    border-left: 2px solid #2f3a52;
  }

  .group.collapsed .members {
    border-left-style: dashed;
  }

  /* A project: its row, then its tabs; a thin line between projects. */
  .project-block {
    border-bottom: 1px solid #1d2028;
    padding-bottom: 2px;
  }

  .project-block.active {
    background: #171b24;
  }

  .row {
    display: flex;
    align-items: center;
    padding-right: 4px;
  }

  .fold {
    flex: none;
    width: 18px;
    justify-content: center;
    padding: 6px 0 6px 4px;
    color: #6b7180;
    font-size: 11px;
  }

  .project {
    flex: 1;
    min-width: 0;
    padding: 6px 6px 6px 2px;
    color: #9aa0ad;
    font-weight: 600;
  }

  .project.active {
    color: #e6e8ee;
  }

  .count {
    flex: none;
    color: #6b7180;
    font-size: 11px;
    font-weight: 400;
  }

  ul {
    list-style: none;
    margin: 0;
    padding: 0;
  }

  .tab {
    padding: 3px 10px 3px 24px;
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

  .none {
    margin: 2px 10px 4px;
    color: #6b7180;
    font-size: 12px;
  }

  .empty {
    margin: 10px;
    color: #9aa0ad;
  }
</style>
