<script lang="ts">
  import { onMount } from 'svelte';
  import type { App } from '../lib/app.svelte';
  import { trapFocus } from '../lib/focustrap';
  import type { ClaudeSessionDetailResp } from '../lib/protocol';
  import { sanitizeBlock, sanitizeRemoteText } from '../lib/sanitize';

  interface Props {
    app: App;
    cwd: string;
    sessionId: string;
  }

  let { app, cwd, sessionId }: Props = $props();
  let d = $state.raw<ClaudeSessionDetailResp | null>(null);
  let err = $state('');
  let retry = $state(false);
  let box: HTMLDivElement | undefined = $state();
  // The daemon reads one session at a time (claudesessions.go); this answer,
  // like no answer at all, is worth a retry.
  const BUSY = 'another session read is already running';

  async function load(): Promise<void> {
    err = '';
    retry = false;
    d = null;
    const o = await app.sessionDetail(cwd, sessionId);
    const p = (o.reply?.payload ?? null) as ClaudeSessionDetailResp | null;
    if (p?.error) {
      err = p.error;
      retry = p.error.includes(BUSY);
    } else if (!o.ok || !p) {
      err = o.ok ? 'bad answer' : o.error;
      retry = true;
    } else d = p;
  }

  const when = (ms?: number): string => (ms ? new Date(ms).toLocaleString() : '—');

  onMount(() => {
    box?.focus();
    void load();
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
    aria-label="Session details"
    tabindex="-1"
    bind:this={box}
    use:trapFocus
    onclick={(e) => e.stopPropagation()}
    onkeydown={onKey}
  >
    <h2>Session {sanitizeRemoteText(sessionId.slice(0, 8))}</h2>
    {#if err}
      <p class="err">{sanitizeRemoteText(err)}</p>
      {#if retry}<button type="button" onclick={load}>Retry</button>{/if}
    {:else if !d}
      <p>Loading…</p>
    {:else}
      <dl>
        <dt>First prompt</dt>
        <dd><pre>{sanitizeBlock(d.first_prompt ?? '')}</pre></dd>
        <dt>Last prompt</dt>
        <dd><pre>{sanitizeBlock(d.last_prompt ?? '')}</pre></dd>
        <dt>Prompts</dt>
        <dd>{d.user_prompts ?? 0}</dd>
        <dt>Started</dt>
        <dd>{when(d.started_ms)}</dd>
        <dt>Modified</dt>
        <dd>{when(d.modified_ms)}</dd>
        <dt>Size</dt>
        <dd>{Math.round((d.size_bytes ?? 0) / 1024)} KiB</dd>
      </dl>
    {/if}
    <div class="buttons"><button type="button" onclick={() => app.closePanel()}>Close</button></div>
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
    width: min(680px, 94vw);
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

  dl {
    display: grid;
    grid-template-columns: auto 1fr;
    gap: 4px 12px;
    margin: 0;
  }

  dt {
    color: #9aa0ad;
  }

  dd {
    margin: 0;
  }

  pre {
    margin: 0;
    white-space: pre-wrap;
    font: 12px/1.4 monospace;
  }

  .err {
    color: #e5a0a0;
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
