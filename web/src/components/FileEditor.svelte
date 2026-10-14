<script lang="ts">
  import { untrack } from 'svelte';
  import type { App } from '../lib/app.svelte';
  import type { EditorSession } from '../lib/editor';
  import { trapFocus } from '../lib/focustrap';
  import { sanitizeRemoteText } from '../lib/sanitize';

  interface Props {
    app: App;
    e: EditorSession;
  }

  let { app, e }: Props = $props();
  let area: HTMLTextAreaElement | undefined = $state();
  let discardArmed = $state(false);
  // The session's plain fields, read again on every editorTick.
  const v = $derived.by(() => {
    void app.editorTick;
    return {
      text: e.text,
      loading: e.loading,
      loadError: e.loadError,
      dirty: e.dirty,
      saving: e.saving,
      conflict: e.conflict,
      saveError: e.saveError,
      linkDown: e.linkDown,
      viewOnly: e.viewOnly,
      readOnly: e.readOnly(),
      hasDefault: e.defaultText !== '',
    };
  });
  const status = $derived(
    v.viewOnly
      ? `Read-only: ${app.refusalFor('admin') || 'view only'}`
      : v.linkDown
        ? 'Not connected — read-only until the link is back'
        : v.saving
          ? 'Saving…'
          : v.saveError
            ? `Not saved: ${v.saveError}`
            : v.dirty
              ? 'Unsaved'
              : 'Saved',
  );
  // The text gets the focus once, when the file has loaded.
  const loading = $derived(v.loading);
  $effect(() => {
    if (!loading) untrack(() => area?.focus());
  });

  function close(): void {
    if (e.close() === 'ask' && !discardArmed) {
      discardArmed = true;
      return;
    }
    app.closeEditor();
  }

  function onKey(ev: KeyboardEvent): void {
    if ((ev.ctrlKey || ev.metaKey) && ev.key.toLowerCase() === 's') {
      // The browser's own Ctrl+S would save the page.
      ev.preventDefault();
      ev.stopPropagation();
      void e.save();
      return;
    }
    if (ev.key === 'Escape') {
      ev.preventDefault();
      ev.stopPropagation();
      close();
    }
  }
</script>

<div class="backdrop" data-modal role="presentation" onclick={close}>
  <div
    class="box"
    role="dialog"
    aria-modal="true"
    aria-label={e.title}
    tabindex="-1"
    use:trapFocus
    onclick={(ev) => ev.stopPropagation()}
    onkeydown={onKey}
  >
    <h2>{e.title}</h2>
    {#if app.client?.connect}<p class="note">Settings of this machine (where quil web runs).</p>{/if}
    {#if app.client?.connect && e.target.kind === 'templates'}<p class="note">The daemon's machine has its own templates.toml.</p>{/if}
    {#if v.loading}
      <p>Loading…</p>
    {:else if v.loadError}
      <p class="err">{sanitizeRemoteText(v.loadError)}</p>
    {:else}
      <textarea
        bind:this={area}
        spellcheck="false"
        value={v.text}
        readonly={v.readOnly}
        aria-label={`${e.title} text`}
        oninput={(ev) => e.edit((ev.currentTarget as HTMLTextAreaElement).value)}
      ></textarea>
    {/if}
    {#if v.conflict}
      <div class="bar" role="alert">
        Changed on disk.
        <button type="button" onclick={() => e.takeTheirs()}>Reload</button>
        <button type="button" onclick={() => void e.save(true)}>Overwrite</button>
      </div>
    {/if}
    <div class="buttons">
      <span class="status" role="status">{status}</span>
      {#if e.target.kind === 'templates' && v.hasDefault}
        <button type="button" disabled={v.readOnly} onclick={() => e.restoreDefault()}>Restore default</button>
      {/if}
      <button type="button" disabled={v.readOnly || !v.dirty || v.saving || v.conflict !== null} onclick={() => void e.save()}
        >Save</button
      >
      <button type="button" onclick={close}>{discardArmed ? 'Discard and close' : 'Close'}</button>
    </div>
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
    width: min(900px, 96vw);
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
  textarea {
    flex: 1;
    width: 100%;
    resize: none;
    font: 13px/1.4 ui-monospace, monospace;
    background: #12141a;
    color: inherit;
    border: 1px solid #2a2e37;
    tab-size: 4;
  }
  .bar {
    margin-top: 6px;
    color: #e5c07b;
  }
  .note,
  .status {
    color: #9aa0ad;
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
  .status {
    margin-right: auto;
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
