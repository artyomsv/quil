<script lang="ts">
  import { onMount } from 'svelte';
  import type { App } from '../lib/app.svelte';
  import { trapFocus } from '../lib/focustrap';
  import { sanitizeRemoteText } from '../lib/sanitize';
  import { downloadRefusal, stageResult, updateLine } from '../lib/update';

  interface Props {
    app: App;
  }

  let { app }: Props = $props();
  let result = $state('');
  let box: HTMLDivElement | undefined = $state();
  const connect = $derived(app.client?.connect === true);
  const why = $derived(downloadRefusal(app.state?.update, app.rights, connect));

  onMount(() => {
    box?.focus();
    // Opening the TUI's About asks for a check too; the daemon rate-limits
    // it, and ignores it on a dev build or with checking off. A remote
    // daemon is not asked (internal/tui/update.go).
    if (app.refusalFor('admin') === '' && !connect) app.fire('update_check_req', {});
  });

  async function download(): Promise<void> {
    result = 'Downloading…';
    result = stageResult(await app.stageUpdate());
  }

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
    aria-label="Update"
    tabindex="-1"
    bind:this={box}
    use:trapFocus
    onclick={(e) => e.stopPropagation()}
    onkeydown={onKey}
  >
    <h2>Update</h2>
    <p>{sanitizeRemoteText(updateLine(app.state?.update, app.daemonVersion))}</p>
    <p class="dim">Gateway {sanitizeRemoteText(app.welcome?.version || 'unknown')}</p>
    <p class="dim">The new version is applied the next time a TUI starts on that machine. The browser cannot apply it.</p>
    {#if why}<p class="dim">Download: {why}</p>{/if}
    {#if result}<p role="status">{sanitizeRemoteText(result)}</p>{/if}
    <div class="buttons">
      <button
        type="button"
        disabled={why !== '' || app.stageBusy}
        title={why || 'Download the new version on the daemon machine'}
        onclick={download}>Download</button
      >
      <button type="button" onclick={() => app.closePanel()}>Close</button>
    </div>
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
    padding: 12px;
    background: #1b1e26;
    border: 1px solid #2a2e37;
    color: #e6e8ee;
  }

  h2 {
    margin: 0 0 8px;
    font-size: 15px;
  }

  .dim {
    color: #9aa0ad;
  }

  .buttons {
    display: flex;
    gap: 8px;
    justify-content: flex-end;
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
    cursor: default;
  }
</style>
