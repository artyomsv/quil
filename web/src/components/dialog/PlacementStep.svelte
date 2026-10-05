<script lang="ts">
  import type { DialogView } from '../../lib/dialog';
  import type { Placement } from '../../lib/protocol';

  interface Props {
    view: DialogView;
    onpick: (p: Placement) => void;
  }

  let { view, onpick }: Props = $props();

  const LABELS: Partial<Record<Placement, string>> = { right: 'Right', below: 'Below', replace: 'Replace' };
  const HINTS: Partial<Record<Placement, string>> = {
    right: 'beside the active pane',
    below: 'under the active pane',
    replace: 'in place of the active pane, keeping its slot',
  };
</script>

<div class="list">
  {#each view.placements as p (p)}
    <button class="row" aria-label={LABELS[p] ?? p} onclick={() => onpick(p)}>
      {LABELS[p] ?? p}<span class="hint">{HINTS[p] ?? ''}</span>
    </button>
  {/each}
</div>

<style>
  .list {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }

  .row {
    display: flex;
    gap: 10px;
    align-items: baseline;
    text-align: left;
    padding: 6px 10px;
    border: 1px solid #3a3f4b;
    background: none;
    color: inherit;
    font: inherit;
    cursor: pointer;
  }

  .row:hover,
  .row:focus-visible {
    border-color: #3d6fd8;
  }

  .hint {
    color: #7d8392;
    font-size: 12px;
  }
</style>
