<script lang="ts">
  import { onMount, tick } from 'svelte';
  import type { App } from '../lib/app.svelte';
  import { copyText } from '../lib/clipboard';
  import type { LogName } from '../lib/files';
  import { trapFocus } from '../lib/focustrap';
  import { LOG_TABS, latestOnly, logBody } from '../lib/logs';

  interface Props {
    app: App;
  }

  let { app }: Props = $props();
  let tab = $state<LogName>('client');
  let body = $state.raw<{ text: string; note: string; truncated: boolean } | null>(null);
  let copied = $state('');
  let pre: HTMLPreElement | undefined = $state();
  let box: HTMLDivElement | undefined = $state();
  const why = $derived(app.refusalFor('admin'));
  const tabs = $derived(LOG_TABS(app.client?.connect ?? false));

  // Only the newest request's answer is shown: a tab clicked while another
  // log was still loading would otherwise show that log under its own name.
  const latest = latestOnly<ReturnType<typeof logBody>>();

  // load shows one log, scrolled to its end (the interesting part).
  async function load(name: LogName): Promise<void> {
    tab = name;
    copied = '';
    if (why) {
      body = { text: '', note: why, truncated: false };
      return;
    }
    body = null;
    const b = await latest(app.filesApi().log(name).then(logBody));
    if (b === null) return;
    body = b;
    await tick();
    if (pre) pre.scrollTop = pre.scrollHeight;
  }

  async function copy(): Promise<void> {
    if (!body?.text) return;
    copied = (await copyText(body.text)) || 'Copied';
  }

  onMount(() => {
    box?.focus();
    void load('client');
  });

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
    aria-label="Logs"
    tabindex="-1"
    bind:this={box}
    use:trapFocus
    onclick={(e) => e.stopPropagation()}
    onkeydown={onKey}
  >
    <h2>Logs</h2>
    <div class="tabs" role="tablist">
      {#each tabs as t (t.name)}
        <button type="button" role="tab" aria-selected={tab === t.name} onclick={() => void load(t.name)}>{t.label}</button>
      {/each}
    </div>
    {#if body === null}
      <p>Loading…</p>
    {:else if body.note}
      <p class="note">{body.note}</p>
    {:else}
      {#if body.truncated}<p class="note">Showing the last 256 KiB.</p>{/if}
      <pre bind:this={pre}>{body.text}</pre>
    {/if}
    <div class="buttons">
      {#if copied}<span class="note">{copied}</span>{/if}
      <button type="button" disabled={why !== ''} onclick={() => void load(tab)}>Refresh</button>
      <button type="button" disabled={!body?.text} onclick={() => void copy()}>Copy</button>
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
    width: min(1000px, 96vw);
    height: min(80vh, 760px);
    display: flex;
    flex-direction: column;
    padding: 12px;
    background: #1b1e26;
    border: 1px solid #2a2e37;
    color: #e6e8ee;
  }
  h2 {
    margin: 0 0 8px;
    font-size: 15px;
  }
  .tabs {
    display: flex;
    gap: 4px;
    margin-bottom: 6px;
  }
  .tabs button[aria-selected='true'] {
    border-color: #6b8afd;
  }
  pre {
    flex: 1;
    margin: 0;
    overflow: auto;
    font: 12px/1.4 ui-monospace, monospace;
    background: #12141a;
    border: 1px solid #2a2e37;
    white-space: pre;
  }
  .note {
    color: #9aa0ad;
  }
  .buttons {
    display: flex;
    gap: 8px;
    justify-content: flex-end;
    align-items: center;
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
    cursor: default;
  }
</style>
