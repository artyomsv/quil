<script lang="ts">
  import { onMount, untrack } from 'svelte';
  import type { App } from '../lib/app.svelte';
  import { copyText } from '../lib/clipboard';
  import { trapFocus } from '../lib/focustrap';
  import { entryTitle, HistoryFlow, historySupported, type HistoryView } from '../lib/history';
  import { sanitizeRemoteText } from '../lib/sanitize';

  interface Props {
    app: App;
    paneId: string;
    paneType: string;
  }

  let { app, paneId, paneType }: Props = $props();
  let view = $state.raw<HistoryView>({ state: 'loading' });
  let copyMsg = $state('');
  let box: HTMLDivElement | undefined = $state();
  // The dialog is keyed on its panel: pane and type are fixed for its life.
  const flow = untrack(
    () =>
      new HistoryFlow(
        { list: () => app.historyList(paneId), entry: (ts) => app.historyEntry(paneId, ts) },
        historySupported(paneType, app.client?.plugins ?? []),
      ),
  );
  flow.onChange = () => {
    view = flow.view;
    copyMsg = '';
  };

  onMount(() => {
    flow.open();
    box?.focus();
  });

  function onKey(e: KeyboardEvent): void {
    if (e.key !== 'Escape') return;
    e.preventDefault();
    e.stopPropagation();
    if (!flow.back()) app.closePanel();
  }

  async function copy(text: string): Promise<void> {
    copyMsg = (await copyText(text)) || 'Copied';
  }
</script>

<div class="backdrop" data-modal role="presentation" onclick={() => app.closePanel()}>
  <div
    class="box"
    role="dialog"
    aria-modal="true"
    aria-label="Input history"
    tabindex="-1"
    bind:this={box}
    use:trapFocus
    onclick={(e) => e.stopPropagation()}
    onkeydown={onKey}
  >
    {#if view.state === 'entry'}
      <h2>{view.title}</h2>
      <pre>{view.text}</pre>
      <div class="buttons">
        {#if copyMsg}<span>{copyMsg}</span>{/if}
        <button type="button" onclick={() => view.state === 'entry' && copy(view.text)}>Copy</button>
        <button type="button" onclick={() => flow.back()}>Back</button>
      </div>
    {:else}
      <h2>Input history</h2>
      {#if view.state === 'unsupported'}
        <p>No input history for this pane type</p>
      {:else if view.state === 'loading'}
        <p>Loading…</p>
      {:else if view.state === 'error'}
        <p class="err">{sanitizeRemoteText(view.text)}</p>
        <button type="button" onclick={() => flow.list()}>Retry</button>
      {:else if view.entries.length === 0}
        <p>No input history yet</p>
      {:else}
        <ul>
          {#each view.entries as e (e.ts_ms)}
            <li>
              <button type="button" onclick={() => flow.pick(e.ts_ms)}
                >{entryTitle(e.ts_ms).slice(8)} — {sanitizeRemoteText(e.preview)}</button
              >
            </li>
          {/each}
        </ul>
      {/if}
      <div class="buttons"><button type="button" onclick={() => app.closePanel()}>Close</button></div>
    {/if}
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
    width: min(760px, 94vw);
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

  ul {
    list-style: none;
    margin: 0;
    padding: 0;
  }

  li button {
    width: 100%;
    text-align: left;
    padding: 3px 6px;
    border: 0;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  li button:hover,
  li button:focus {
    background: #2a3550;
  }

  pre {
    white-space: pre-wrap;
    font: 13px/1.4 monospace;
    background: #111318;
    padding: 8px;
  }

  .err {
    color: #e5a0a0;
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
</style>
