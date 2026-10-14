<script lang="ts">
  import { onMount } from 'svelte';
  import type { App } from '../lib/app.svelte';
  import { trapFocus } from '../lib/focustrap';
  import { KIND_LABELS } from '../lib/logs';
  import { sanitizeRemoteText } from '../lib/sanitize';

  interface Props {
    app: App;
  }

  let { app }: Props = $props();
  let box: HTMLDivElement | undefined = $state();
  // The gateway binary's newest release (spec 5d §4.6); never auto-opened.
  const w = $derived(app.client?.whats_new ?? null);
  onMount(() => box?.focus());

  function onKey(e: KeyboardEvent): void {
    if (e.key !== 'Escape') return;
    e.preventDefault();
    e.stopPropagation();
    app.closePanel();
  }
</script>

<div class="backdrop" data-modal role="presentation" onclick={() => app.closePanel()}>
  <div
    class="box"
    role="dialog"
    aria-modal="true"
    aria-label="What's New"
    tabindex="-1"
    bind:this={box}
    use:trapFocus
    onclick={(e) => e.stopPropagation()}
    onkeydown={onKey}
  >
    <h2>What's New</h2>
    {#if w === null}
      <p class="note">No release notes in this build.</p>
    {:else}
      <p class="note">quil {sanitizeRemoteText(w.version)} · {sanitizeRemoteText(w.date)}</p>
      <ul>
        {#each w.items as it, i (i)}
          <li><span class="kind">{KIND_LABELS[it.kind] ?? it.kind}</span> {sanitizeRemoteText(it.text)}</li>
        {/each}
      </ul>
    {/if}
    <div class="buttons"><button type="button" onclick={() => app.closePanel()}>Close</button></div>
  </div>
</div>

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    z-index: 30;
    background: rgb(0 0 0 / 50%);
    display: grid;
    place-items: center;
  }
  .box {
    width: min(560px, 94vw);
    max-height: 80vh;
    overflow: auto;
    padding: 12px;
    background: #1b1e26;
    border: 1px solid #2a2e37;
    color: #e6e8ee;
  }
  h2 {
    margin: 0 0 8px;
    font-size: 15px;
  }
  .note,
  .kind {
    color: #9aa0ad;
  }
  .kind {
    display: inline-block;
    min-width: 6em;
  }
  .buttons {
    display: flex;
    justify-content: flex-end;
    margin-top: 8px;
  }
  button {
    padding: 3px 10px;
    border: 1px solid #3a3f4b;
    background: none;
    color: inherit;
    font: inherit;
    cursor: pointer;
  }
</style>
