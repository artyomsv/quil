<script lang="ts">
  import type { App } from '../lib/app.svelte';
  import Banner from './Banner.svelte';
  import CreatePaneDialog from './CreatePaneDialog.svelte';
  import KeyList from './KeyList.svelte';
  import Notice from './Notice.svelte';
  import NotificationPanel from './NotificationPanel.svelte';
  import PaneArea from './PaneArea.svelte';
  import Sidebar from './Sidebar.svelte';
  import TabBar from './TabBar.svelte';

  interface Props {
    app: App;
  }

  let { app }: Props = $props();
</script>

<div class="workspace">
  {#if app.sidebarOpen}
    <Sidebar {app} />
  {/if}
  <div class="main">
    <TabBar {app} />
    {#if app.notice}
      <Notice text={app.notice} onclose={() => (app.notice = null)} />
    {/if}
    {#if app.banner}
      <Banner text={app.banner.text} retrying={app.banner.retrying} />
    {/if}
    {#if app.keyHint}
      <div class="keyhint" role="status">{app.keyHint}</div>
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
  {#if app.notifyOpen}
    <NotificationPanel {app} />
  {/if}
</div>
{#if app.keyListOpen}
  <KeyList {app} />
{/if}

<style>
  .workspace {
    display: flex;
    height: 100%;
  }

  .main {
    position: relative;
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
  }

  /* Floats over the panes: a line that pushed the pane area down would
     resize every terminal on each prefix key. */
  .keyhint {
    position: absolute;
    right: 8px;
    bottom: 8px;
    z-index: 20;
    padding: 2px 8px;
    border: 1px solid #2a2e37;
    background: #1b1e26;
    font: 12px monospace;
    color: #9aa0ad;
    pointer-events: none;
  }
</style>
