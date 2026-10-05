<script lang="ts">
  import type { App } from '../lib/app.svelte';
  import AgentDot from './AgentDot.svelte';

  interface Props {
    app: App;
  }

  let { app }: Props = $props();
</script>

<nav>
  {#each app.sidebar as project (project.id)}
    <section>
      <button
        class="project"
        class:active={project.active}
        disabled={app.readOnly || project.id === ''}
        onclick={() => app.switchProject(project.id)}
      >
        {project.name || '—'}
      </button>
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
  {:else}
    <p class="empty">—</p>
  {/each}
</nav>

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
