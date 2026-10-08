<script lang="ts">
  import { untrack } from 'svelte';
  import type { App } from '../lib/app.svelte';
  import { trapFocus } from '../lib/focustrap';
  import { stillShown } from '../lib/panels';
  import { sanitizeRemoteText } from '../lib/sanitize';

  interface Props {
    app: App;
    // Set: rename this project; unset: a new project.
    projectId?: string;
  }

  let { app, projectId }: Props = $props();
  const existing = $derived(app.state?.projects.find((p) => p.id === projectId));
  let name = $state(untrack(() => existing?.name ?? ''));
  let root = $state(untrack(() => existing?.root_dir ?? ''));
  let busy = $state(false);
  let field: HTMLInputElement | undefined = $state();
  // The panel this form belongs to: a late answer closes only it.
  const mine = untrack(() => app.panel);
  const title = $derived(projectId ? 'Rename project' : 'New project');

  $effect(() => {
    field?.focus();
    field?.select();
  });

  async function submit(e: SubmitEvent): Promise<void> {
    e.preventDefault();
    const n = name.trim();
    if (n === '' || busy) return;
    busy = true;
    const out = projectId
      ? await app.renameProject(projectId, n)
      : await app.newProject(n, root, () => stillShown(mine, app.panel));
    busy = false;
    if (out.ok) app.closePanelIf(mine);
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
    aria-label={title}
    tabindex="-1"
    use:trapFocus
    onclick={(e) => e.stopPropagation()}
    onkeydown={onKey}
  >
    <h2>{title}</h2>
    <form onsubmit={submit}>
      <label>Name <input type="text" bind:value={name} bind:this={field} /></label>
      {#if !projectId}
        <!-- A text field with the daemon's recent folders: the daemon expands
             ~ and checks the path. -->
        <label
          >Folder (on the daemon's machine)
          <input type="text" list="project-recent" bind:value={root} placeholder="empty: the daemon's default" />
        </label>
        <datalist id="project-recent">
          {#each app.state?.recent_cwds ?? [] as d (d)}<option value={d}>{sanitizeRemoteText(d)}</option>{/each}
        </datalist>
      {/if}
      <div class="buttons">
        <button type="button" onclick={() => app.closePanel()}>Cancel</button>
        <button type="submit" class="primary" disabled={name.trim() === '' || busy}>{projectId ? 'Rename' : 'Create'}</button>
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
    min-width: 320px;
    max-width: 520px;
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

  input {
    width: 100%;
    box-sizing: border-box;
    padding: 4px 6px;
    border: 1px solid #3a3f4b;
    background: #111318;
    color: inherit;
    font: inherit;
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
