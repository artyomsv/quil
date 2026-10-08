<script lang="ts">
  import { onMount } from 'svelte';
  import type { App } from '../lib/app.svelte';
  import { trapFocus } from '../lib/focustrap';
  import type { PluginAvail } from '../lib/protocol';
  import type { Outcome } from '../lib/requests';
  import { sanitizeRemoteText } from '../lib/sanitize';

  interface Props {
    app: App;
  }

  let { app }: Props = $props();
  let list = $state.raw<PluginAvail[] | null>(null);
  let err = $state('');
  let busy = $state(false);
  let box: HTMLDivElement | undefined = $state();
  const why = $derived(app.refusalFor('admin'));

  function take(o: Outcome): void {
    const p = (o.reply?.payload ?? null) as { plugins?: PluginAvail[] } | null;
    if (!o.ok || !p) {
      err = o.ok ? 'bad answer' : o.error;
      return;
    }
    err = '';
    list = [...(p.plugins ?? [])].sort((a, b) => a.name.localeCompare(b.name));
  }

  async function reload(): Promise<void> {
    busy = true;
    take(await app.reloadPlugins());
    busy = false;
  }

  onMount(() => {
    box?.focus();
    void app.daemonList('plugin_list_req', {}).then(take);
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
    aria-label="Plugins"
    tabindex="-1"
    bind:this={box}
    use:trapFocus
    onclick={(e) => e.stopPropagation()}
    onkeydown={onKey}
  >
    <h2>Plugins on this daemon</h2>
    {#if err}<p class="err">{sanitizeRemoteText(err)}</p>{/if}
    {#if list === null && !err}<p>Loading…</p>{/if}
    <ul>
      {#each list ?? [] as p (p.name)}
        <li>
          <span>{sanitizeRemoteText(p.name)}</span><span class:off={!p.available}
            >{p.available ? 'available' : 'not found on this daemon'}</span
          >
        </li>
      {/each}
    </ul>
    <p class="note">Plugin files are edited in the TUI (F1 → Plugins).</p>
    <div class="buttons">
      {#if why}<span class="why">{why}</span>{/if}
      <button type="button" disabled={why !== '' || busy} title={why || 'Read the plugin files again'} onclick={reload}
        >{busy ? 'Reloading…' : 'Reload'}</button
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

  li {
    display: flex;
    justify-content: space-between;
    padding: 2px 0;
    border-bottom: 1px solid #2a2e37;
  }

  .off,
  .why,
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
