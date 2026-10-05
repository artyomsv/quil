<script lang="ts">
  import type { App } from '../lib/app.svelte';
  import { sanitizeRemoteText } from '../lib/sanitize';
  import PaneView from './PaneView.svelte';
  import SplitBars from './SplitBars.svelte';

  interface Props {
    app: App;
  }

  let { app }: Props = $props();
  let area: HTMLDivElement | undefined = $state();

  $effect(() => {
    const el = area;
    if (!el) return;
    const ro = new ResizeObserver((entries) => {
      const r = entries[0]?.contentRect;
      if (r) app.measureArea(r.width, r.height);
    });
    ro.observe(el);
    return () => ro.disconnect();
  });

  const pct = (v: number): string => `${v * 100}%`;
</script>

<div class="area" bind:this={area}>
  {#each app.placed as p (p.id)}
    <div class="slot" style:left={pct(p.rect.x)} style:top={pct(p.rect.y)} style:width={pct(p.rect.w)} style:height={pct(p.rect.h)}>
      <PaneView
        {app}
        paneId={p.id}
        name={p.name}
        spawnError={p.spawnError}
        muted={p.muted}
        worktreeOwned={p.worktreeOwned}
        agent={p.agent}
      />
    </div>
  {:else}
    {#if app.state}
      <p class="empty">No panes</p>
    {:else if !app.banner}
      <p class="empty">Connecting…</p>
    {/if}
  {/each}
  <SplitBars {app} {area} />
  {#if app.overlay}
    <!-- The tab's overlay covers the pane area while this page shows it. -->
    <div class="slot overlay" style:left="0" style:top="0" style:width="100%" style:height="100%">
      {#key app.overlay.id}
        <PaneView
          {app}
          paneId={app.overlay.id}
          name={sanitizeRemoteText(app.overlay.kind)}
          spawnError=""
          muted={false}
          worktreeOwned={false}
          agent="unknown"
          overlay
        />
      {/key}
    </div>
  {/if}
</div>

<style>
  .area {
    position: relative;
    flex: 1;
    min-height: 0;
    overflow: hidden;
    background: #000;
  }

  .slot {
    position: absolute;
    padding: 1px;
  }

  .overlay {
    z-index: 5;
    background: #000;
  }

  .empty {
    margin: 16px;
    color: #9aa0ad;
  }
</style>
