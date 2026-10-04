<script lang="ts">
  import { onMount } from 'svelte';
  import Login from './components/Login.svelte';
  import Workspace from './components/Workspace.svelte';
  import { App as QuilApp } from './lib/app.svelte';

  const app = new QuilApp();

  onMount(() => {
    void app.boot();
    return () => app.stop();
  });
</script>

{#if app.view === 'login'}
  <Login {app} />
{:else if app.view === 'workspace'}
  <Workspace {app} />
{/if}

<style>
  :global(html),
  :global(body) {
    margin: 0;
    height: 100%;
    background: #15171c;
    color: #d6d9e0;
    font-family: system-ui, -apple-system, 'Segoe UI', sans-serif;
    font-size: 14px;
  }

  :global(#app) {
    height: 100%;
  }

  :global(*) {
    box-sizing: border-box;
  }
</style>
