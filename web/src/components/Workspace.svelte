<script lang="ts">
  import type { App } from '../lib/app.svelte';
  import Banner from './Banner.svelte';
  import PaneArea from './PaneArea.svelte';
  import Sidebar from './Sidebar.svelte';
  import TabBar from './TabBar.svelte';

  interface Props {
    app: App;
  }

  let { app }: Props = $props();
</script>

<div class="workspace">
  <Sidebar {app} />
  <div class="main">
    <TabBar {app} />
    {#if app.banner}
      <Banner text={app.banner.text} retrying={app.banner.retrying} />
    {/if}
    {#if app.state}
      <PaneArea {app} />
    {:else if !app.banner}
      <p class="empty">Connecting…</p>
    {/if}
  </div>
</div>

<style>
  .workspace {
    display: flex;
    height: 100%;
  }

  .main {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
  }

  .empty {
    margin: 16px;
    color: #9aa0ad;
  }
</style>
