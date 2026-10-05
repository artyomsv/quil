<script lang="ts">
  import type { App } from '../../lib/app.svelte';
  import { displayAddr, type SavedInstance } from '../../lib/client';
  import type { Act, DialogView } from '../../lib/dialog';
  import { sanitizeRemoteText } from '../../lib/sanitize';
  import Confirm from '../Confirm.svelte';

  interface Props {
    app: App;
    view: DialogView;
    act: Act;
    onpick: (id: string) => void;
    onrefresh: () => Promise<boolean>;
  }

  let { app, view, act, onpick, onrefresh }: Props = $props();

  let values = $state<Record<string, string>>({});
  let formKey = '';
  let error = $state('');
  let saving = $state(false);
  let deleting = $state<SavedInstance | null>(null);

  // The form starts from the instance it edits, else from each field's
  // default; it is filled again whenever the form opens for another one.
  $effect(() => {
    const key = `${view.step}|${view.editing}`;
    if (key === formKey) return;
    formKey = key;
    error = '';
    const editing = view.instances.find((i) => i.id === view.editing);
    const next: Record<string, string> = {};
    for (const f of view.plugin?.form_fields ?? []) next[f.name] = editing ? (editing.fields[f.name] ?? '') : (f.default ?? '');
    values = next;
  });

  const missing = $derived((view.plugin?.form_fields ?? []).filter((f) => f.required && (values[f.name] ?? '').trim() === ''));

  async function save(e: SubmitEvent): Promise<void> {
    e.preventDefault();
    const plugin = view.plugin?.name ?? '';
    if (!plugin || missing.length > 0 || saving) return;
    saving = true;
    error = '';
    const fields: Record<string, string> = {};
    for (const [k, v] of Object.entries(values)) if (v.trim() !== '') fields[k] = v.trim();
    // The TUI names an instance by its "name" field, "unnamed" without one.
    const input = { plugin, name: fields.name || 'unnamed', fields };
    const api = app.instancesApi();
    const editing = view.editing;
    const r = editing ? await api.update({ ...input, id: editing }) : await api.create(input);
    saving = false;
    if (r.error) {
      error = r.error;
      return;
    }
    await onrefresh();
    if (!editing && r.instance) {
      // Without full rights the instance is saved but not started.
      const id = r.instance.id;
      if (view.canLaunch) onpick(id);
      else act((d) => d.instanceSaved(id));
      return;
    }
    act((d) => d.instancesChanged(d.info));
  }

  async function remove(si: SavedInstance): Promise<void> {
    deleting = null;
    const r = await app.instancesApi().remove(view.plugin?.name ?? '', si.id);
    if (r.error) {
      error = r.error;
      return;
    }
    await onrefresh();
    act((d) => d.instancesChanged(d.info));
  }
</script>

{#if view.step === 'instances'}
  {#if !view.canLaunch}
    <p class="note">This login may save instances; starting one needs full rights.</p>
  {/if}
  <div class="list">
    {#if view.canSave}
      <button class="row" onclick={() => act((d) => d.newInstance())}>+ New instance</button>
    {/if}
    {#each view.instances as si (si.id)}
      <div class="item">
        <button class="row pick" onclick={() => onpick(si.id)}>
          <span>{sanitizeRemoteText(si.name)}</span>
          <span class="addr">{sanitizeRemoteText(displayAddr(si))}</span>
        </button>
        {#if view.canSave}
          <button class="small" aria-label="Edit {sanitizeRemoteText(si.name)}" onclick={() => act((d) => d.editInstance(si.id))}>Edit</button>
          <button class="small" aria-label="Delete {sanitizeRemoteText(si.name)}" onclick={() => (deleting = si)}>Delete</button>
        {/if}
      </div>
    {/each}
  </div>
{:else}
  <form onsubmit={save}>
    {#each view.plugin?.form_fields ?? [] as f (f.name)}
      <label>
        <span>{sanitizeRemoteText(f.label || f.name)}{f.required ? ' *' : ''}</span>
        <input type="text" bind:value={values[f.name]} autocomplete="off" />
      </label>
    {/each}
    <div class="buttons">
      <button type="submit" class="primary" disabled={missing.length > 0 || saving || !view.canSave}>
        {view.editing ? 'Save' : 'Save and continue'}
      </button>
    </div>
  </form>
{/if}
{#if error}
  <p class="error" role="alert">{error}</p>
{/if}
{#if deleting}
  {@const si = deleting}
  <Confirm
    title="Delete instance"
    body={`Delete ${sanitizeRemoteText(si.name)} from instances.json?`}
    confirmLabel="Delete"
    onconfirm={() => void remove(si)}
    oncancel={() => (deleting = null)}
  />
{/if}

<style>
  .list {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }

  .item {
    display: flex;
    gap: 4px;
  }

  .row {
    flex: 1;
    display: flex;
    gap: 10px;
    align-items: baseline;
    text-align: left;
    padding: 6px 10px;
    border: 1px solid #3a3f4b;
    background: none;
    color: inherit;
    font: inherit;
    cursor: pointer;
  }

  .row:hover,
  .row:focus-visible {
    border-color: #3d6fd8;
  }

  .addr {
    color: #7d8392;
    font-size: 12px;
    overflow-wrap: anywhere;
  }

  .small {
    padding: 2px 8px;
    border: 1px solid #3a3f4b;
    background: none;
    color: #9aa0ad;
    font: inherit;
    font-size: 12px;
    cursor: pointer;
  }

  form {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }

  label {
    display: flex;
    flex-direction: column;
    gap: 2px;
    font-size: 12px;
    color: #9aa0ad;
  }

  input {
    padding: 4px 6px;
    border: 1px solid #3a3f4b;
    background: #111318;
    color: #e6e8ee;
    font: inherit;
    font-size: 13px;
  }

  .buttons {
    display: flex;
    justify-content: flex-end;
  }

  .primary {
    padding: 4px 12px;
    border: 1px solid #3d6fd8;
    background: none;
    color: #9dbaf5;
    font: inherit;
    cursor: pointer;
  }

  .primary:disabled {
    cursor: default;
    opacity: 0.5;
  }

  .note {
    margin: 0 0 8px;
    color: #7d8392;
    font-size: 12px;
  }

  .error {
    color: #f08a8a;
    overflow-wrap: anywhere;
  }
</style>
