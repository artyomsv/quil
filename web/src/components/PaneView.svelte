<script lang="ts">
  import { untrack } from 'svelte';
  import type { App } from '../lib/app.svelte';

  interface Props {
    app: App;
    paneId: string;
    name: string;
    spawnError: string;
  }

  let { app, paneId, name, spawnError }: Props = $props();
  let host: HTMLDivElement | undefined = $state();

  // The terminal moves into host while the pane is shown and out again when
  // it is not; a pane with a spawn error shows the error instead.
  $effect(() => {
    const el = host;
    const id = paneId;
    if (!el || spawnError) return;
    untrack(() => app.paneShown(id, el));
    const ro = new ResizeObserver((entries) => {
      const r = entries[0]?.contentRect;
      if (r) app.measure(id, r.width, r.height);
    });
    ro.observe(el);
    return () => {
      ro.disconnect();
      untrack(() => app.paneHidden(id));
    };
  });
</script>

<div class="pane">
  <div class="title">{name}</div>
  {#if spawnError}
    <p class="error">{spawnError}</p>
  {:else}
    <div class="term" bind:this={host}></div>
  {/if}
</div>

<style>
  .pane {
    display: flex;
    flex-direction: column;
    width: 100%;
    height: 100%;
    border: 1px solid #2a2e37;
    background: #000;
  }

  .title {
    flex: none;
    padding: 1px 6px;
    background: #1b1e26;
    color: #9aa0ad;
    font-size: 12px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .term {
    flex: 1;
    min-height: 0;
    overflow: hidden;
  }

  .error {
    margin: 8px;
    color: #f08a8a;
    white-space: pre-wrap;
  }
</style>
