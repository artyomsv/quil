<script lang="ts">
  import type { App } from '../lib/app.svelte';
  import { copyText } from '../lib/clipboard';
  import { trapFocus } from '../lib/focustrap';
  import { MAX_NOTE_BYTES, noteBytes, type NoteSession, saveText } from '../lib/notes';
  import { sanitizeRemoteText } from '../lib/sanitize';

  interface Props {
    app: App;
    n: NoteSession;
  }

  let { app, n }: Props = $props();
  let area: HTMLTextAreaElement | undefined = $state();
  let discardArmed = $state(false);
  let copyMsg = $state('');
  // The session's plain fields, read again on every notesTick.
  const v = $derived.by(() => {
    void app.notesTick;
    return {
      text: n.text,
      loading: n.loading,
      loadError: n.loadError,
      dirty: n.dirty,
      saving: n.saving,
      conflict: n.conflict,
      saveError: n.saveError,
      hold: n.hold,
      viewOnly: n.viewOnly,
      linkDown: n.linkDown,
      paneGone: n.paneGone,
      loadArmed: n.loadArmed,
      closing: n.closing,
      tooLarge: n.tooLarge(),
      bytes: noteBytes(saveText(n.text)),
    };
  });
  const paneName = $derived(sanitizeRemoteText(app.state?.panes.find((p) => p.id === n.paneId)?.name || 'pane'));
  const readOnly = $derived(v.viewOnly || v.loading || v.loadError !== '' || v.linkDown || v.paneGone);
  const status = $derived(
    v.viewOnly
      ? `Read-only: ${app.refusalFor('act') || 'view only'}`
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

  $effect(() => {
    if (!v.loading) area?.focus();
  });

  function onBeforeUnload(e: BeforeUnloadEvent): void {
    if (n.dirty) e.preventDefault();
  }

  function onKey(e: KeyboardEvent): void {
    if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 's') {
      // The browser's own Ctrl+S would save the page.
      e.preventDefault();
      e.stopPropagation();
      n.save(n.conflict);
    } else if (e.key === 'Escape') {
      e.preventDefault();
      e.stopPropagation();
      app.closeNotes();
    }
  }

  async function copy(): Promise<void> {
    copyMsg = (await copyText(n.text)) || 'Copied';
  }

  function discard(): void {
    if (discardArmed) app.discardNotes();
    else discardArmed = true;
  }
</script>

<svelte:window onbeforeunload={onBeforeUnload} />

<div class="backdrop" data-modal role="presentation">
  <div class="box" role="dialog" aria-modal="true" aria-label="Notes — {paneName}" tabindex="-1" use:trapFocus onkeydown={onKey}>
    <h2>Notes — {paneName}</h2>
    {#if v.loading && !v.loadError}
      <p>Loading…</p>
    {:else if v.loadError}
      <div class="bar" role="alert">
        <span>Note not loaded: {v.loadError}</span>
        <button type="button" onclick={() => n.load()}>Retry</button>
      </div>
    {/if}
    {#if v.conflict}
      <div class="bar" role="alert">
        <span>This note was changed elsewhere.</span>
        <button type="button" disabled={v.saving} onclick={() => n.loadTheirs()}
          >{v.loadArmed ? 'Click again to drop your text' : 'Load theirs'}</button
        >
        <button type="button" disabled={v.saving || v.linkDown || v.paneGone} onclick={() => n.save(true)}>Keep mine</button>
        <button type="button" onclick={copy}>Copy my text</button>
      </div>
    {/if}
    {#if v.paneGone}
      <div class="bar" role="alert">
        <span>This pane was closed.</span>
        <button type="button" onclick={copy}>Copy my text</button>
      </div>
    {/if}
    <textarea
      bind:this={area}
      value={v.text}
      readonly={readOnly}
      aria-label="Note text"
      oninput={(e) => n.edit(e.currentTarget.value)}
    ></textarea>
    <div class="foot">
      <span class="status">{status}</span>
      {#if v.bytes > MAX_NOTE_BYTES * 0.9}
        <span class:over={v.tooLarge}>{Math.round(v.bytes / 1024)} / 256 KiB</span>
      {/if}
      {#if copyMsg}<span>{copyMsg}</span>{/if}
      <span class="buttons">
        {#if v.closing && (v.conflict || v.hold || v.paneGone || v.linkDown)}
          <button type="button" onclick={discard}>{discardArmed ? 'Click again to discard' : 'Discard and close'}</button>
        {/if}
        <button type="button" onclick={() => app.closeNotes()}>Close</button>
      </span>
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
    width: min(760px, 94vw);
    height: min(560px, 86vh);
    display: flex;
    flex-direction: column;
    gap: 6px;
    padding: 12px;
    background: #1b1e26;
    border: 1px solid #2a2e37;
    color: #e6e8ee;
  }

  h2 {
    margin: 0;
    font-size: 15px;
  }

  textarea {
    flex: 1;
    resize: none;
    padding: 6px;
    border: 1px solid #3a3f4b;
    background: #111318;
    color: inherit;
    font: 13px/1.4 monospace;
  }

  .bar {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    align-items: center;
    padding: 6px;
    border: 1px solid #8a6d1f;
    background: #2b2412;
  }

  .over {
    color: #e57373;
  }

  .foot {
    display: flex;
    gap: 12px;
    align-items: center;
    color: #9aa0ad;
    font-size: 12px;
  }

  .buttons {
    margin-left: auto;
    display: flex;
    gap: 8px;
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
