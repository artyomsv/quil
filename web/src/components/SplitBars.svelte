<script lang="ts">
  import type { App } from '../lib/app.svelte';
  import { type Bar, ratioFromPointer, splitBars } from '../lib/splitbars';
  import { activeTree } from '../lib/view';

  interface Props {
    app: App;
    area: HTMLDivElement | undefined;
  }

  let { app, area }: Props = $props();
  const tree = $derived(activeTree(app.state, app.dragPreview));
  const bars = $derived(splitBars(tree));
  const pct = (v: number): string => `${v * 100}%`;

  // Pointer-down pins the tab's tree and revision (SplitDrag); the bar keeps
  // the pointer until release, which sends one layout write.
  function down(e: PointerEvent, bar: Bar): void {
    const s = app.state;
    const tab = s?.tabs.find((t) => t.id === s.active_tab);
    if (!s || !tab || !tree || !app.editable || e.button !== 0) return;
    if (!app.drag.begin(tab.id, tree, tab.layout_rev, bar.path)) return;
    e.preventDefault();
    (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
  }

  function move(e: PointerEvent, bar: Bar): void {
    const r = area?.getBoundingClientRect();
    if (!r || r.width <= 0 || r.height <= 0) return;
    app.drag.move(ratioFromPointer(bar, (e.clientX - r.left) / r.width, (e.clientY - r.top) / r.height));
  }
</script>

{#if app.editable}
  {#each bars as bar (bar.path)}
    <div
      class="split-bar"
      class:v={bar.dir === 0}
      class:h={bar.dir === 1}
      role="separator"
      aria-orientation={bar.dir === 0 ? 'vertical' : 'horizontal'}
      style:left={bar.dir === 0 ? pct(bar.rect.x + bar.rect.w * bar.ratio) : pct(bar.rect.x)}
      style:top={bar.dir === 1 ? pct(bar.rect.y + bar.rect.h * bar.ratio) : pct(bar.rect.y)}
      style:width={bar.dir === 0 ? '6px' : pct(bar.rect.w)}
      style:height={bar.dir === 1 ? '6px' : pct(bar.rect.h)}
      onpointerdown={(e) => down(e, bar)}
      onpointermove={(e) => {
        if (e.buttons) move(e, bar);
      }}
      onpointerup={() => app.drag.end()}
      onpointercancel={() => app.drag.cancel()}
    ></div>
  {/each}
{/if}

<style>
  .split-bar {
    position: absolute;
    z-index: 5;
    touch-action: none;
  }

  .split-bar.v {
    transform: translateX(-3px);
    cursor: col-resize;
  }

  .split-bar.h {
    transform: translateY(-3px);
    cursor: row-resize;
  }

  .split-bar:hover {
    background: rgb(61 111 216 / 40%);
  }
</style>
