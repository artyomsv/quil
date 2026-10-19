<script lang="ts">
  import { onMount } from 'svelte';
  import type { App } from '../lib/app.svelte';
  import { trapFocus } from '../lib/focustrap';
  import { sanitizeRemoteText } from '../lib/sanitize';

  interface Props {
    app: App;
  }

  let { app }: Props = $props();
  let box: HTMLDivElement | undefined = $state();
  const tip = $derived(app.client?.tip ?? null);
  // "Don't show again" writes config.toml: full rights (spec 5d §4.7).
  const why = $derived(app.refusalFor('admin'));
  onMount(() => box?.focus());
</script>

<!-- No Escape on purpose: a page-load notice with two explicit answers, as
     the TUI's dialog needs a key too. -->
<div class="backdrop" data-modal role="presentation">
  <div class="box" role="dialog" aria-modal="true" aria-label="Welcome notice" tabindex="-1" bind:this={box} use:trapFocus>
    <h2>Welcome to Quil</h2>
    {#if tip}
      <h3>{sanitizeRemoteText(tip.title)}</h3>
      <ul>
        {#each tip.items as it, i (i)}<li>{sanitizeRemoteText(it)}</li>{/each}
      </ul>
    {/if}
    <div class="buttons">
      <button type="button" disabled={why !== ''} title={why || 'Stop showing this notice'} onclick={() => app.dismissWelcome(true)}
        >Don't show again</button
      >
      <button type="button" onclick={() => app.dismissWelcome(false)}>OK</button>
    </div>
  </div>
</div>

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    z-index: 40;
    background: rgb(0 0 0 / 50%);
    display: grid;
    place-items: center;
  }
  .box {
    width: min(480px, 94vw);
    padding: 12px;
    background: #1b1e26;
    border: 1px solid #2a2e37;
    color: #e6e8ee;
  }
  h2 {
    margin: 0 0 8px;
    font-size: 15px;
  }
  h3 {
    margin: 8px 0 4px;
    font-size: 13px;
  }
  .buttons {
    display: flex;
    gap: 8px;
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
  button:disabled {
    opacity: 0.5;
  }
</style>
