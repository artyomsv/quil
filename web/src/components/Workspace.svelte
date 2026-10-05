<script lang="ts">
  import type { App } from '../lib/app.svelte';
  import Banner from './Banner.svelte';
  import CreatePaneDialog from './CreatePaneDialog.svelte';
  import Notice from './Notice.svelte';
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
    {#if app.notice}
      <Notice text={app.notice} onclose={() => (app.notice = null)} />
    {/if}
    {#if app.banner}
      <Banner text={app.banner.text} retrying={app.banner.retrying} />
    {/if}
    <!-- Always present, even before the first state: its size is the window
         this tab reports in attach. -->
    <PaneArea {app} />
    {#if app.dialog && app.client && app.editable}
      <!-- Keyed: opening it again (another pane, another mode) starts over. -->
      {#key app.dialog}
        <CreatePaneDialog {app} />
      {/key}
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
</style>
