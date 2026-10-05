<script lang="ts">
  import type { DialogView } from '../../lib/dialog';
  import { sanitizeRemoteText } from '../../lib/sanitize';

  interface Props {
    view: DialogView;
    onpick: (name: string) => void;
  }

  let { view, onpick }: Props = $props();

  // Only a plain http(s) link is offered: a plugin file is text the user
  // may have copied from anywhere.
  function safeHomepage(url: string | undefined): string {
    return url && /^https?:\/\//i.test(url) ? url : '';
  }
</script>

<div class="list">
  {#each view.plugins as p (p.name)}
    {@const ok = !!view.available[p.name]}
    <div class="item">
      <button class="row" disabled={!ok} onclick={() => onpick(p.name)}>{sanitizeRemoteText(p.display_name || p.name)}</button>
      {#if !ok}
        <span class="why">
          not installed on this machine
          {#if safeHomepage(p.homepage)}
            · <a href={safeHomepage(p.homepage)} target="_blank" rel="noreferrer">{sanitizeRemoteText(p.homepage ?? '')}</a>
          {/if}
        </span>
      {:else if p.description}
        <span class="why">{sanitizeRemoteText(p.description)}</span>
      {/if}
    </div>
  {/each}
</div>

<style>
  .list {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }

  .item {
    display: flex;
    flex-direction: column;
    gap: 2px;
  }

  .row {
    text-align: left;
    padding: 6px 10px;
    border: 1px solid #3a3f4b;
    background: none;
    color: inherit;
    font: inherit;
    cursor: pointer;
  }

  .row:hover:not(:disabled),
  .row:focus-visible {
    border-color: #3d6fd8;
  }

  .row:disabled {
    cursor: default;
    opacity: 0.5;
  }

  .why {
    padding-left: 10px;
    color: #7d8392;
    font-size: 12px;
    overflow-wrap: anywhere;
  }

  a {
    color: #9dbaf5;
  }
</style>
