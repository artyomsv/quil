<script lang="ts">
  import { onMount } from 'svelte';
  import type { App } from '../lib/app.svelte';
  import type { SettingRow, SettingsView } from '../lib/files';
  import { trapFocus } from '../lib/focustrap';
  import { sanitizeRemoteText } from '../lib/sanitize';
  import { appliesLabel, parseInput } from '../lib/settings';

  interface Props {
    app: App;
  }

  let { app }: Props = $props();
  type Tab = 'general' | 'notifications' | 'sandbox' | 'keys';
  const TABS: { id: Tab; label: string }[] = [
    { id: 'general', label: 'General' },
    { id: 'notifications', label: 'Notifications' },
    { id: 'sandbox', label: 'Sandbox' },
    { id: 'keys', label: 'Keys' },
  ];
  let tab = $state<Tab>('general');
  let view = $state.raw<SettingsView | null>(null);
  let loadErr = $state('');
  let rowErr = $state.raw<Record<string, string>>({});
  // A text field's value while it is being typed; cleared on save or refusal.
  let draft = $state.raw<Record<string, string>>({});
  let busy = $state('');
  let box: HTMLDivElement | undefined = $state();
  const connect = $derived(app.client?.connect ?? false);
  // Writes need full rights and a live link; standard may read the values.
  const why = $derived(app.refusalFor('admin'));

  async function load(): Promise<void> {
    const r = await app.filesApi().settings();
    if (r.ok) {
      view = r.value;
      loadErr = '';
    } else {
      loadErr = r.error;
    }
  }

  async function save(row: { key: string; kind: SettingRow['kind'] }, raw: string | boolean): Promise<void> {
    if (why) return;
    const v = parseInput(row.kind, raw);
    if (v === null) {
      rowErr = { ...rowErr, [row.key]: row.kind === 'int' ? 'must be a whole number' : 'must be a number' };
      draft = { ...draft, [row.key]: '' };
      return;
    }
    busy = row.key;
    const r = await app.filesApi().setSetting(row.key, v);
    busy = '';
    const { [row.key]: _d, ...restDraft } = draft;
    draft = restDraft;
    if (r.ok) {
      view = r.value;
      const { [row.key]: _e, ...restErr } = rowErr;
      rowErr = restErr;
      app.afterSettingSaved(row.key, r.value);
    } else {
      // The row shows the stored value again, with the check's text beside it.
      rowErr = { ...rowErr, [row.key]: r.error };
    }
  }

  function shown(row: SettingRow): string {
    return draft[row.key] ?? String(row.value);
  }

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
    aria-label="Settings"
    tabindex="-1"
    bind:this={box}
    use:trapFocus
    onclick={(e) => e.stopPropagation()}
    onkeydown={onKey}
  >
    <h2>Settings</h2>
    {#if connect}<p class="note">Settings of this machine (where quil web runs).</p>{/if}
    <p class="note">Saving rewrites config.toml. Comments in the file are not kept.</p>
    <div class="tabs" role="tablist">
      {#each TABS as t (t.id)}
        <button type="button" role="tab" aria-selected={tab === t.id} onclick={() => (tab = t.id)}>{t.label}</button>
      {/each}
    </div>
    {#if loadErr}<p class="err">{sanitizeRemoteText(loadErr)}</p>{/if}
    {#if why}<p class="why">{why}</p>{/if}
    {#if view && tab !== 'keys'}
      <table>
        <tbody>
          {#each view.rows.filter((r) => r.group === tab) as row (row.key)}
            <tr>
              <th scope="row"><label for={`set-${row.key}`}>{row.label}</label></th>
              <td>
                {#if row.kind === 'bool'}
                  <input
                    id={`set-${row.key}`}
                    type="checkbox"
                    checked={row.value === true}
                    disabled={why !== '' || busy !== ''}
                    onchange={(e) => void save(row, (e.currentTarget as HTMLInputElement).checked)}
                  />
                {:else if row.kind === 'enum'}
                  <select
                    id={`set-${row.key}`}
                    value={String(row.value)}
                    disabled={why !== '' || busy !== ''}
                    onchange={(e) => void save(row, (e.currentTarget as HTMLSelectElement).value)}
                  >
                    {#each row.choices ?? [] as c (c)}<option value={c}>{c}</option>{/each}
                  </select>
                {:else}
                  <input
                    id={`set-${row.key}`}
                    type="text"
                    value={shown(row)}
                    disabled={why !== '' || busy !== ''}
                    oninput={(e) => (draft = { ...draft, [row.key]: (e.currentTarget as HTMLInputElement).value })}
                    onkeydown={(e) => {
                      if (e.key === 'Enter') void save(row, (e.currentTarget as HTMLInputElement).value);
                    }}
                    onblur={(e) => {
                      if (draft[row.key] !== undefined) void save(row, (e.currentTarget as HTMLInputElement).value);
                    }}
                  />
                {/if}
                {#if rowErr[row.key]}<span class="err">{sanitizeRemoteText(rowErr[row.key] ?? '')}</span>{/if}
              </td>
              <td class="applies">{appliesLabel(row, connect)}</td>
            </tr>
          {/each}
        </tbody>
      </table>
      {#if tab === 'general'}
        <button
          type="button"
          disabled={why !== ''}
          title={why || 'Edit templates.toml'}
          onclick={() => app.openFileEditor({ kind: 'templates' }, 'Templates')}>Templates…</button
        >
      {/if}
    {:else if view && tab === 'keys'}
      {@const k = view.keys}
      {#if k.error}<p class="err">{k.error}</p>{/if}
      <p class="note">Saving rewrites bindings.toml. Comments in it are not kept.</p>
      <label
        >Preset
        <select
          value={k.preset}
          disabled={why !== '' || busy !== '' || !!k.error}
          onchange={(e) => void save({ key: 'keys.preset', kind: 'string' }, (e.currentTarget as HTMLSelectElement).value)}
        >
          {#each k.presets as p (p)}<option value={p}>{p}</option>{/each}
        </select></label
      >
      {#if rowErr['keys.preset']}<span class="err">{sanitizeRemoteText(rowErr['keys.preset'])}</span>{/if}
      {#if k.prefix_used}
        <label
          >Prefix
          <input
            type="text"
            placeholder={`${k.preset_prefix} (preset default)`}
            value={draft['keys.prefix'] ?? k.prefix}
            disabled={why !== '' || busy !== '' || !!k.error}
            oninput={(e) => (draft = { ...draft, 'keys.prefix': (e.currentTarget as HTMLInputElement).value })}
            onkeydown={(e) => {
              if (e.key === 'Enter') void save({ key: 'keys.prefix', kind: 'string' }, (e.currentTarget as HTMLInputElement).value);
            }}
          /></label
        >
        {#if rowErr['keys.prefix']}<span class="err">{sanitizeRemoteText(rowErr['keys.prefix'])}</span>{/if}
      {:else}
        <p class="note">This preset uses no prefix.</p>
      {/if}
      <p class="note">Applies now in this page.</p>
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
    width: min(720px, 94vw);
    max-height: 86vh;
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
  .tabs {
    display: flex;
    gap: 4px;
    margin: 8px 0;
  }
  .tabs button[aria-selected='true'] {
    border-color: #6b8afd;
  }
  table {
    width: 100%;
    border-collapse: collapse;
  }
  th {
    text-align: left;
    font-weight: normal;
    padding: 3px 8px 3px 0;
  }
  td {
    padding: 3px 0;
  }
  .applies,
  .note,
  .why {
    color: #9aa0ad;
  }
  .err {
    color: #e5a0a0;
    margin-left: 8px;
  }
  .buttons {
    display: flex;
    justify-content: flex-end;
    margin-top: 8px;
  }
  button,
  input,
  select {
    padding: 2px 8px;
    border: 1px solid #3a3f4b;
    background: none;
    color: inherit;
    font: inherit;
  }
  button:disabled,
  input:disabled,
  select:disabled {
    opacity: 0.5;
  }
</style>
