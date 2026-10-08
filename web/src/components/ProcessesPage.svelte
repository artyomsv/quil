<script lang="ts">
  import { onDestroy, onMount, untrack } from 'svelte';
  import type { App } from '../lib/app.svelte';
  import { trapFocus } from '../lib/focustrap';
  import { flatten, Poller, type ProcRow } from '../lib/processes';
  import type { QuilProcInfo } from '../lib/protocol';
  import { sanitizeRemoteText } from '../lib/sanitize';
  import { paneName } from '../lib/view';

  interface Props {
    app: App;
  }

  let { app }: Props = $props();
  let rows = $state.raw<ProcRow[]>([]);
  let quil = $state.raw<QuilProcInfo[]>([]);
  let err = $state('');
  let loaded = $state(false);
  let box: HTMLDivElement | undefined = $state();
  const clock = {
    setTimeout: (f: () => void, ms: number) => window.setTimeout(f, ms),
    clearTimeout: (h: unknown) => window.clearTimeout(h as number),
    now: () => performance.now(),
  };
  const poller = new Poller(
    clock,
    () => app.resourceReport(),
    () => document.hidden,
  );
  const label = (id: string): string => {
    const p = app.state?.panes.find((x) => x.id === id);
    return p ? paneName(p) : id;
  };
  poller.onReport = (r) => {
    err = '';
    loaded = true;
    rows = flatten(r, label, app.refusalFor('admin'));
    quil = r.quil ?? [];
  };
  poller.onError = (t) => (err = t);
  const onVis = (): void => poller.visible();

  onMount(() => {
    document.addEventListener('visibilitychange', onVis);
    poller.start();
    box?.focus();
  });
  onDestroy(() => {
    document.removeEventListener('visibilitychange', onVis);
    poller.stop();
  });

  // Back from the kill confirm (which sat on top): the list takes the
  // keyboard again, so Escape closes it.
  const shown = $derived(app.panel?.kind === 'processes');
  $effect(() => {
    if (shown) untrack(() => box?.focus());
  });

  const mb = (b: number): string => `${(b / 1048576).toFixed(1)} MB`;
  const cpu = (c: number): string => (c < 0 ? '—' : `${c.toFixed(0)}%`);

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
    aria-label="Processes"
    tabindex="-1"
    bind:this={box}
    use:trapFocus
    onclick={(e) => e.stopPropagation()}
    onkeydown={onKey}
  >
    <h2>Processes</h2>
    {#if err}<p class="err">{sanitizeRemoteText(err)}</p>{/if}
    {#if !loaded && !err}<p>Loading…</p>{/if}
    <table>
      <thead><tr><th>Pane</th><th>Process</th><th>PID</th><th>Memory</th><th>CPU</th><th></th></tr></thead>
      <tbody>
        {#each rows as r (r.paneId + ':' + r.pid)}
          <tr>
            <td>{r.paneLabel}</td>
            <td style:padding-left="{(Math.max(1, r.depth) - 1) * 12 + 4}px">{sanitizeRemoteText(r.name)}</td>
            <td>{r.pid}</td>
            <td>{mb(r.rss)}</td>
            <td>{cpu(r.cpu)}</td>
            <td>
              <button
                type="button"
                disabled={r.killable !== ''}
                title={r.killable || 'Kill this process'}
                onclick={() => app.openPanel({ kind: 'kill', paneId: r.paneId, pid: r.pid, startMs: r.startMs, name: r.name })}
                >Kill</button
              >
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
    {#if quil.length > 0}
      <h3>Quil processes</h3>
      <table>
        <tbody>
          {#each quil as q (q.pid)}
            <tr>
              <td>{sanitizeRemoteText(q.role)}</td>
              <td>{sanitizeRemoteText(q.exe_name)}</td>
              <td>{q.pid}</td>
              <td>{sanitizeRemoteText(q.version)}{q.stale ? ' (stale)' : ''}</td>
              <td>{cpu(q.cpu_pct)}</td>
            </tr>
          {/each}
        </tbody>
      </table>
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
    width: min(860px, 96vw);
    max-height: 84vh;
    overflow: auto;
    padding: 12px;
    background: #1b1e26;
    border: 1px solid #2a2e37;
    color: #e6e8ee;
  }

  h2,
  h3 {
    margin: 0 0 8px;
    font-size: 15px;
  }

  table {
    width: 100%;
    border-collapse: collapse;
    font-size: 13px;
  }

  th,
  td {
    text-align: left;
    padding: 2px 4px;
    border-bottom: 1px solid #2a2e37;
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
    padding: 2px 8px;
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
