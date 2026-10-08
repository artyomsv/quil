<script lang="ts">
  import { untrack } from 'svelte';
  import type { App } from '../lib/app.svelte';
  import { trapFocus } from '../lib/focustrap';
  import { sanitizeRemoteText } from '../lib/sanitize';

  interface Props {
    app: App;
  }

  let { app }: Props = $props();
  const templates = $derived(app.client?.templates ?? []);
  // The TUI's rows: template, task, directory, branch. The daemon reads the
  // template by name and checks the directory and the task itself.
  let template = $state(untrack(() => app.client?.templates[0]?.name ?? ''));
  let task = $state('');
  let cwd = $state('');
  let branch = $state('');
  let busy = $state(false);
  // The panel this form belongs to: a late answer closes only it.
  const mine = untrack(() => app.panel);
  let err = $state('');
  let first: HTMLSelectElement | undefined = $state();

  $effect(() => first?.focus());

  async function submit(e: SubmitEvent): Promise<void> {
    e.preventDefault();
    if (template === '' || busy) return;
    busy = true;
    err = '';
    // The daemon resolves the typed directory (~ included) before the create.
    const folder = await app.resolveFolder(cwd);
    if ('error' in folder) {
      busy = false;
      err = folder.error;
      return;
    }
    const out = await app.createFromTemplate({
      template,
      task: task.trim() || undefined,
      cwd: folder.dir || undefined,
      branch: branch.trim() || undefined,
      project_id: app.activeProjectId || undefined,
    });
    busy = false;
    const p = out.reply?.payload as { error?: string } | undefined;
    if (out.ok && !p?.error) app.closePanelIf(mine);
    else err = sanitizeRemoteText(p?.error || (out.ok ? '' : out.error));
  }

  function onKey(e: KeyboardEvent): void {
    if (e.key !== 'Escape') return;
    e.stopPropagation();
    app.closePanel();
  }
</script>

<div class="backdrop" data-modal role="presentation" onclick={() => app.closePanel()}>
  <div
    class="dialog"
    role="dialog"
    aria-modal="true"
    aria-label="New from template"
    tabindex="-1"
    use:trapFocus
    onclick={(e) => e.stopPropagation()}
    onkeydown={onKey}
  >
    <h2>New from template</h2>
    <form onsubmit={submit}>
      <label
        >Template
        <select bind:value={template} bind:this={first}>
          {#each templates as t (t.name)}
            <option value={t.name}
              >{sanitizeRemoteText(t.name)}{t.description ? ` — ${sanitizeRemoteText(t.description)}` : ''}</option
            >
          {/each}
        </select>
      </label>
      <label>Task <input type="text" bind:value={task} placeholder="optional" /></label>
      <label
        >Directory (on the daemon's machine)
        <input type="text" list="template-recent" bind:value={cwd} placeholder="empty: the project's folder" />
      </label>
      <datalist id="template-recent">
        {#each app.state?.recent_cwds ?? [] as d (d)}<option value={d}>{sanitizeRemoteText(d)}</option>{/each}
      </datalist>
      <label>Branch <input type="text" bind:value={branch} placeholder="optional: open in a new worktree" /></label>
      {#if err}<p class="err" role="alert">{err}</p>{/if}
      <div class="buttons">
        <button type="button" onclick={() => app.closePanel()}>Cancel</button>
        <button type="submit" class="primary" disabled={template === '' || busy}>{busy ? 'Creating…' : 'Create'}</button>
      </div>
    </form>
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

  .dialog {
    min-width: 360px;
    max-width: 560px;
    padding: 16px;
    background: #1b1e26;
    border: 1px solid #2a2e37;
    color: #e6e8ee;
  }

  h2 {
    margin: 0 0 8px;
    font-size: 15px;
  }

  label {
    display: block;
    margin: 6px 0;
  }

  input,
  select {
    width: 100%;
    box-sizing: border-box;
    padding: 4px 6px;
    border: 1px solid #3a3f4b;
    background: #111318;
    color: inherit;
    font: inherit;
  }

  .err {
    color: #e5a0a0;
  }

  .buttons {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
    margin-top: 12px;
  }

  button {
    padding: 4px 12px;
    border: 1px solid #3a3f4b;
    background: none;
    color: inherit;
    font: inherit;
    cursor: pointer;
  }

  button:disabled {
    cursor: default;
    opacity: 0.5;
  }

  .primary {
    border-color: #3d6fd8;
    color: #9dbaf5;
  }
</style>
