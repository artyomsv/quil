<script lang="ts">
  import type { App } from '../lib/app.svelte';
  import { TUI_ONLY } from '../lib/keys/actions';
  import { sanitizeRemoteText } from '../lib/sanitize';

  interface Props {
    app: App;
  }

  let { app }: Props = $props();
  let box: HTMLDivElement | undefined = $state();

  // The F1 key list: the keymap's groups in its order, a group it does not
  // name after them.
  const groups = $derived.by(() => {
    const km = app.keymap;
    if (!km) return [];
    const order = [...km.group_order];
    for (const a of km.actions) if (!order.includes(a.group)) order.push(a.group);
    return order.map((g) => ({ name: g, rows: km.actions.filter((a) => a.group === g) })).filter((g) => g.rows.length > 0);
  });

  $effect(() => box?.focus());

  function onKey(e: KeyboardEvent): void {
    if (e.key === 'Escape' || e.key === 'F1') {
      e.preventDefault();
      app.closeKeyList();
    }
  }
</script>

<div class="backdrop" data-modal role="presentation" onclick={() => app.closeKeyList()}>
  <div
    class="box"
    role="dialog"
    aria-modal="true"
    aria-label="Keys"
    tabindex="-1"
    bind:this={box}
    onkeydown={onKey}
    onclick={(e) => e.stopPropagation()}
  >
    <h2>Keys — {sanitizeRemoteText(app.keymap?.preset || 'default')}</h2>
    {#each app.keymap?.conflicts ?? [] as c, i (i)}
      <p class="conflict">! {sanitizeRemoteText(c)}</p>
    {/each}
    {#each app.keymap?.builtins ?? [] as b (b.id)}
      <div class="row">
        <span class="key">{sanitizeRemoteText(b.keys.join(' / ')) || '—'}</span>
        <span>{sanitizeRemoteText(b.label)}</span>
        {#if b.fallback}<span class="note">browser key</span>{/if}
        {#if b.fallback_unavailable}<span class="note warn">web fallback unavailable</span>{/if}
      </div>
    {/each}
    {#each groups as g (g.name)}
      <h3>{sanitizeRemoteText(g.name)}</h3>
      {#each g.rows as a (a.id)}
        <div class="row">
          <span class="key">{sanitizeRemoteText(a.keys.join(' / ')) || '—'}</span>
          <span>{sanitizeRemoteText(a.label)}</span>
          {#if a.fallback}<span class="note">browser key</span>{/if}
          {#if TUI_ONLY.has(a.id)}<span class="note">TUI only</span>{/if}
          {#if a.fallback_unavailable}<span class="note warn">web fallback unavailable</span>{/if}
        </div>
      {/each}
    {/each}
    <p class="foot">Esc closes</p>
  </div>
</div>

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    background: rgb(0 0 0 / 55%);
    display: flex;
    align-items: center;
    justify-content: center;
    z-index: 50;
  }

  .box {
    max-width: min(720px, calc(100vw - 32px));
    max-height: calc(100vh - 32px);
    overflow-y: auto;
    background: #1b1e26;
    border: 1px solid #2a2e37;
    padding: 12px 16px;
    outline: none;
  }

  h2 {
    margin: 0 0 8px;
    font-size: 15px;
  }

  h3 {
    margin: 12px 0 4px;
    font-size: 13px;
    color: #9aa0ad;
  }

  .row {
    display: flex;
    gap: 12px;
    padding: 2px 0;
  }

  .key {
    min-width: 160px;
    font-family: monospace;
    color: #e6e8ee;
  }

  .note {
    color: #9aa0ad;
    font-size: 12px;
  }

  .warn,
  .conflict {
    color: #e8a24f;
  }

  .foot {
    margin: 12px 0 0;
    color: #9aa0ad;
    font-size: 12px;
  }
</style>
