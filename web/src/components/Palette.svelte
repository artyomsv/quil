<script lang="ts">
  import { onDestroy, untrack } from 'svelte';
  import type { App } from '../lib/app.svelte';
  import { trapFocus } from '../lib/focustrap';
  import { filterPalette, type PaletteRow, PaneSearch } from '../lib/palette';
  import { sanitizeRemoteText } from '../lib/sanitize';

  interface Props {
    app: App;
  }

  let { app }: Props = $props();
  let query = $state('');
  let field: HTMLInputElement | undefined = $state();
  // Bumped by the search on every change of its plain fields.
  let tick = $state(0);
  const clock = {
    setTimeout: (fn: () => void, ms: number) => window.setTimeout(fn, ms),
    clearTimeout: (h: unknown) => window.clearTimeout(h as number),
    now: () => performance.now(),
  };
  const search = new PaneSearch(clock, (q) => app.searchPanes(q));
  search.onChange = () => tick++;
  onDestroy(() => search.stop());

  const status = $derived.by(() => {
    void tick;
    return search.status;
  });
  const truncated = $derived.by(() => {
    void tick;
    return search.truncated;
  });
  const all = $derived(app.paletteRows());
  // A hit names a pane by the same label as its "Go to pane" row; a hit for
  // a pane the page cannot label is skipped, as in the TUI.
  const hitRows = $derived.by((): PaletteRow[] => {
    void tick;
    const labels = new Map<string, string>();
    for (const r of all) if (r.run && 'goPane' in r.run) labels.set(r.run.goPane, r.label);
    return search.hits.flatMap((h) => {
      const label = labels.get(h.pane_id);
      if (!label) return [];
      return [
        {
          label,
          detail: `${h.matches}×${h.truncated ? ' capped' : ''}`,
          run: { goPane: h.pane_id },
          excerpt: sanitizeRemoteText(h.excerpt),
        },
      ];
    });
  });
  const rows = $derived([...filterPalette(query.trim() === '' ? '' : query, all), ...hitRows]);
  const selectable = (r: PaletteRow | undefined): boolean => !!r && !r.header && !!r.run && !r.disabled;

  // The cursor starts on the first row that can run, and again after each
  // keystroke; arriving hits and state frames never move it.
  let cursor = $state(untrack(() => Math.max(0, rows.findIndex(selectable))));

  $effect(() => field?.focus());
  $effect(() => {
    search.query(query);
  });

  function oninput(e: Event): void {
    query = (e.currentTarget as HTMLInputElement).value;
    cursor = Math.max(0, rows.findIndex(selectable));
  }

  function move(d: number): void {
    if (rows.length === 0) return;
    let i = cursor;
    for (let n = 0; n < rows.length; n++) {
      i = (i + d + rows.length) % rows.length;
      if (selectable(rows[i])) {
        cursor = i;
        return;
      }
    }
  }

  function onKey(e: KeyboardEvent): void {
    if (e.key === 'Escape') {
      e.preventDefault();
      e.stopPropagation();
      app.closePanel();
    } else if (e.key === 'ArrowDown') {
      e.preventDefault();
      move(1);
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      move(-1);
    } else if (e.key === 'PageDown') {
      e.preventDefault();
      for (let k = 0; k < 10; k++) move(1);
    } else if (e.key === 'PageUp') {
      e.preventDefault();
      for (let k = 0; k < 10; k++) move(-1);
    } else if (e.key === 'Enter') {
      e.preventDefault();
      const r = rows[cursor];
      if (r && selectable(r)) app.runPaletteRow(r);
    }
  }
</script>

<div class="backdrop" data-modal role="presentation" onclick={() => app.closePanel()}>
  <div
    class="box"
    role="dialog"
    aria-modal="true"
    aria-label="Command palette"
    tabindex="-1"
    use:trapFocus
    onclick={(e) => e.stopPropagation()}
    onkeydown={onKey}
  >
    <input
      bind:this={field}
      value={query}
      {oninput}
      role="combobox"
      aria-expanded="true"
      aria-controls="palette-list"
      aria-label="Command"
      placeholder="Type a command or search pane output"
    />
    <ul id="palette-list" role="listbox" aria-label="Commands">
      {#each rows as r, i (i)}
        {#if r.header}
          <li class="header" role="presentation">{r.label}</li>
        {:else}
          <li role="none">
            <button
              type="button"
              role="option"
              tabindex="-1"
              aria-selected={i === cursor}
              aria-disabled={!!r.disabled}
              class:cur={i === cursor}
              class:hit={!!r.excerpt}
              class:off={!!r.disabled}
              title={r.disabled || undefined}
              onclick={() => {
                if (selectable(r)) app.runPaletteRow(r);
              }}
            >
              <span class="label">{r.label}</span>
              {#if r.disabled}<span class="why">{r.disabled}</span>{:else if r.detail}<span class="key">{r.detail}</span>{/if}
              {#if r.excerpt}<span class="excerpt">{r.excerpt}</span>{/if}
            </button>
          </li>
        {/if}
      {/each}
    </ul>
    <p class="status" role="status">
      {#if status === 'searching'}Searching pane output…{:else if status === 'timed_out'}search timed out{:else if status === 'failed'}search failed{/if}
      {#if truncated}more matches not shown{/if}
    </p>
  </div>
</div>

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    z-index: 30;
    background: rgb(0 0 0 / 50%);
    display: grid;
    place-items: start center;
    padding-top: 10vh;
  }

  .box {
    width: min(640px, 92vw);
    max-height: 70vh;
    display: flex;
    flex-direction: column;
    background: #1b1e26;
    border: 1px solid #2a2e37;
    color: #e6e8ee;
  }

  input {
    margin: 8px;
    padding: 6px 8px;
    border: 1px solid #3a3f4b;
    background: #111318;
    color: inherit;
    font: inherit;
  }

  ul {
    list-style: none;
    margin: 0;
    padding: 0 0 6px;
    overflow-y: auto;
  }

  li.header {
    color: #6b7180;
    font-size: 12px;
    padding: 8px 12px 3px;
  }

  button {
    width: 100%;
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    padding: 3px 12px;
    border: 0;
    background: none;
    color: inherit;
    font: inherit;
    text-align: left;
    cursor: pointer;
  }

  button.cur {
    background: #2a3550;
  }

  button.off {
    color: #6b7180;
    cursor: default;
  }

  .label {
    flex: 1;
  }

  .key,
  .why {
    color: #9aa0ad;
    font-size: 12px;
  }

  .excerpt {
    flex-basis: 100%;
    color: #9aa0ad;
    font: 12px monospace;
    white-space: pre;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .status {
    margin: 0;
    padding: 4px 12px;
    min-height: 1.2em;
    color: #9aa0ad;
    font-size: 12px;
  }
</style>
