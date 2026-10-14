<script lang="ts">
  import type { App } from '../lib/app.svelte';
  import { trapFocus } from '../lib/focustrap';
  import type { MigrationFlow } from '../lib/migration';
  import { sanitizeBlock, sanitizeRemoteText } from '../lib/sanitize';

  interface Props {
    app: App;
    m: MigrationFlow;
  }

  let { app, m }: Props = $props();
  let discardArmed = $state(false);
  // The flow's plain fields, read again on every migrationTick.
  const v = $derived.by(() => {
    void app.migrationTick;
    return {
      cur: m.current(),
      idx: m.idx,
      total: m.files.length,
      mine: m.mine,
      error: m.error,
      busy: m.busy,
      readOnly: m.readOnly(),
      done: m.done(),
      dirty: m.dirty,
    };
  });

  function close(): void {
    if (v.dirty && !discardArmed) {
      discardArmed = true;
      return;
    }
    app.closeMigration();
  }

  function onKey(e: KeyboardEvent): void {
    if (e.key !== 'Escape') return;
    e.preventDefault();
    e.stopPropagation();
    close();
  }
</script>

<div class="backdrop" data-modal role="presentation" onclick={close}>
  <div
    class="box"
    role="dialog"
    aria-modal="true"
    aria-label="Plugin update"
    tabindex="-1"
    use:trapFocus
    onclick={(e) => e.stopPropagation()}
    onkeydown={onKey}
  >
    {#if v.done}
      <h2>Plugin update</h2>
      <p>Every plugin file is up to date.</p>
      <div class="buttons"><button type="button" onclick={() => app.closeMigration()}>Close</button></div>
    {:else if v.cur}
      <h2>Plugin update: {sanitizeRemoteText(v.cur.name)} ({v.idx + 1} of {v.total})</h2>
      <p class="note">
        Your file needs schema_version {v.cur.need_version} or higher. Keep your changes and raise the version, or use the new
        default.
      </p>
      {#if app.client?.connect}<p class="note">Settings of this machine (where quil web runs).</p>{/if}
      <div class="split">
        <label
          >Your file
          <textarea
            spellcheck="false"
            value={v.mine}
            readonly={v.readOnly}
            oninput={(e) => m.edit((e.currentTarget as HTMLTextAreaElement).value)}
          ></textarea></label
        >
        <div>
          New default
          <pre>{sanitizeBlock(v.cur.default)}</pre>
        </div>
      </div>
      {#if v.error}<p class="err" role="alert">{sanitizeRemoteText(v.error)}</p>{/if}
      <div class="buttons">
        <button type="button" disabled={v.idx === 0} onclick={() => m.prev()}>Previous</button>
        <button type="button" disabled={v.idx >= v.total - 1} onclick={() => m.next()}>Next</button>
        <button type="button" disabled={v.readOnly} onclick={() => void m.useDefault()}>Use default</button>
        <button type="button" disabled={v.readOnly} onclick={() => void m.saveMine()}>Save mine</button>
        <button type="button" onclick={close}>{discardArmed ? 'Discard and close' : 'Close'}</button>
      </div>
    {/if}
  </div>
</div>

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    z-index: 32;
    background: rgb(0 0 0 / 50%);
    display: grid;
    place-items: center;
  }
  .box {
    width: min(1100px, 96vw);
    max-height: 88vh;
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
  .split {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 8px;
  }
  @media (max-width: 700px) {
    .split {
      grid-template-columns: 1fr;
    }
  }
  textarea,
  pre {
    width: 100%;
    height: 50vh;
    margin: 4px 0 0;
    overflow: auto;
    font: 12px/1.4 ui-monospace, monospace;
    background: #12141a;
    color: inherit;
    border: 1px solid #2a2e37;
    white-space: pre;
  }
  .note {
    color: #9aa0ad;
  }
  .err {
    color: #e5a0a0;
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
    cursor: default;
  }
</style>
