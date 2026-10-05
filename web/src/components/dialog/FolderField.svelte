<script lang="ts">
  import type { App } from '../../lib/app.svelte';
  import type { DialogView, ListKind } from '../../lib/dialog';
  import { sanitizeRemoteText } from '../../lib/sanitize';

  interface Props {
    app: App;
    view: DialogView;
    // onbrowse lists path (descending into child) on the daemon; the
    // daemon's resolved path becomes the folder.
    onbrowse: (path: string, child?: string) => void;
    // onfolder makes a typed or picked path the folder.
    onfolder: (cwd: string) => void;
    onretry: (kind: ListKind) => void;
  }

  let { app, view, onbrowse, onfolder, onretry }: Props = $props();

  const MAX_RECENT = 8;

  // The field shows the dialog's folder and follows it when a pick or the
  // daemon's resolved path changes it; typing is committed on Enter or blur.
  let text = $state('');
  let shown = '';
  $effect(() => {
    if (view.cwd !== shown) {
      shown = view.cwd;
      text = view.cwd;
    }
  });

  function commit(listAgain: boolean): void {
    const t = text.trim();
    if (t === '') return;
    if (t !== view.cwd) onfolder(t);
    else if (listAgain) onbrowse(t);
  }

  interface Listing {
    path: string;
    resolved?: string;
    parent?: string;
    entries?: { name: string; is_dir: boolean }[];
    roots?: string[];
    truncated?: boolean;
    roots_truncated?: boolean;
  }

  const listing = $derived((view.lists.folders.reply?.payload ?? null) as Listing | null);
  const base = $derived(listing ? listing.resolved || listing.path : '');
  const dirs = $derived((listing?.entries ?? []).filter((e) => e.is_dir));
  const recent = $derived((app.state?.recent_cwds ?? []).slice(0, MAX_RECENT));
  const repos = $derived(
    view.plugin?.discover === 'git' ? (((view.lists.repos.reply?.payload as { repos?: string[] } | undefined)?.repos ?? []) as string[]) : [],
  );
</script>

<div class="field">
  <label class="name">
    Folder
    <input
      type="text"
      bind:value={text}
      autocomplete="off"
      spellcheck="false"
      onkeydown={(e) => {
        if (e.key === 'Enter') {
          e.preventDefault();
          commit(true);
        }
      }}
      onblur={() => commit(false)}
    />
  </label>

  {#if view.plugin?.discover === 'git'}
    <div class="group">
      <span class="caption">Repositories</span>
      {#if view.lists.repos.status === 'scanning'}
        <span class="caption">scanning…</span>
      {:else if view.lists.repos.status === 'failed'}
        <span class="failed">failed: {sanitizeRemoteText(view.lists.repos.error ?? '')}</span>
        <button class="chip" onclick={() => onretry('repos')}>Retry</button>
      {:else if view.lists.repos.status === 'empty'}
        <span class="caption">no git repositories here</span>
      {/if}
      {#each repos as r (r)}
        <button class="chip" onclick={() => onfolder(r)}>{sanitizeRemoteText(r)}</button>
      {/each}
    </div>
  {/if}

  {#if recent.length > 0}
    <div class="group">
      <span class="caption">Recent</span>
      {#each recent as r (r)}
        <button class="chip" title={sanitizeRemoteText(r)} onclick={() => onfolder(r)}>{sanitizeRemoteText(r)}</button>
      {/each}
    </div>
  {/if}

  <div class="browser">
    {#if view.lists.folders.status === 'scanning'}
      <span class="caption">scanning…</span>
    {:else if view.lists.folders.status === 'failed'}
      <span class="failed">failed: {sanitizeRemoteText(view.lists.folders.error ?? '')}</span>
      <button class="chip" onclick={() => onretry('folders')}>Retry</button>
    {:else if listing}
      {#if listing.parent}
        <button class="dir" aria-label="Up" title={sanitizeRemoteText(listing.parent)} onclick={() => onbrowse(listing.parent ?? '')}>↑ ..</button>
      {/if}
      {#each listing.roots ?? [] as r (r)}
        <button class="dir" onclick={() => onbrowse(r)}>{sanitizeRemoteText(r)}</button>
      {/each}
      {#each dirs as d (d.name)}
        <button class="dir" onclick={() => onbrowse(base, d.name)}>{sanitizeRemoteText(d.name)}</button>
      {/each}
      {#if dirs.length === 0 && (listing.roots ?? []).length === 0}
        <span class="caption">no folders here</span>
      {/if}
      {#if listing.truncated}
        <span class="caption">(list cut short)</span>
      {/if}
      {#if listing.roots_truncated}
        <span class="caption">(some drives did not answer)</span>
      {/if}
    {/if}
  </div>
</div>

<style>
  .field {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }

  .name {
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

  .group {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
    align-items: center;
  }

  .caption {
    color: #7d8392;
    font-size: 12px;
  }

  .failed {
    color: #f08a8a;
    font-size: 12px;
    overflow-wrap: anywhere;
  }

  .chip,
  .dir {
    max-width: 100%;
    padding: 2px 8px;
    border: 1px solid #3a3f4b;
    background: none;
    color: #c4c8d2;
    font: inherit;
    font-size: 12px;
    text-align: left;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    cursor: pointer;
  }

  .chip:hover,
  .dir:hover,
  .chip:focus-visible,
  .dir:focus-visible {
    border-color: #3d6fd8;
  }

  .browser {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
    max-height: 160px;
    overflow-y: auto;
    padding: 4px;
    border: 1px solid #2a2e37;
  }
</style>
