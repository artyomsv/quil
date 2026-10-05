<script lang="ts">
  import type { App } from '../lib/app.svelte';
  import { sanitizeRemoteText } from '../lib/sanitize';

  interface Props {
    app: App;
  }

  let { app }: Props = $props();
  let list: HTMLUListElement | undefined = $state();

  // notification.focus bumps notifyFocus: the first card takes the focus.
  $effect(() => {
    if (app.notifyFocus > 0) list?.querySelector('button')?.focus();
  });

  const count = (e: { data?: Record<string, string> }): string => {
    const n = Number(e.data?.count ?? '1');
    return Number.isFinite(n) && n > 1 ? ` ×${n}` : '';
  };
</script>

<aside class="notify" aria-label="Notifications">
  <div class="head">
    <span>Notifications</span>
    {#if !app.readOnly && app.events.length > 0}
      <button class="all" onclick={() => app.dismissEvent('')}>Dismiss all</button>
    {/if}
  </div>
  <ul bind:this={list}>
    {#each app.events as e (e.id)}
      <li class="card {e.severity === 'error' || e.severity === 'warning' ? e.severity : ''}">
        <button class="jump" onclick={() => app.jumpToEvent(e)}>
          <span class="title">{sanitizeRemoteText(e.title)}{count(e)}</span>
          <span class="pane">{sanitizeRemoteText(e.pane_name || e.pane_id)}</span>
          {#if e.message}<span class="msg">{sanitizeRemoteText(e.message)}</span>{/if}
        </button>
        {#if !app.readOnly}
          <button class="dismiss" aria-label="Dismiss" title="Dismiss" onclick={() => app.dismissEvent(e.id)}>×</button>
        {/if}
      </li>
    {:else}
      <li class="empty">No notifications</li>
    {/each}
  </ul>
</aside>

<style>
  .notify {
    width: 280px;
    flex: none;
    display: flex;
    flex-direction: column;
    border-left: 1px solid #2a2e37;
    background: #111318;
  }

  .head {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 6px 10px;
    color: #9aa0ad;
    font-weight: 600;
  }

  ul {
    list-style: none;
    margin: 0;
    padding: 0;
    overflow-y: auto;
    flex: 1;
  }

  .card {
    display: flex;
    border-bottom: 1px solid #1f232c;
  }

  button {
    border: 0;
    background: none;
    color: inherit;
    font: inherit;
    cursor: pointer;
    text-align: left;
  }

  .jump {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    padding: 6px 10px;
    gap: 2px;
  }

  .title {
    color: #e6e8ee;
  }

  .pane,
  .msg {
    color: #9aa0ad;
    font-size: 12px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    max-width: 100%;
  }

  .dismiss {
    padding: 0 10px;
    color: #9aa0ad;
  }

  .card.error .title {
    color: #f08a8a;
  }

  .card.warning .title {
    color: #e8a24f;
  }

  .all {
    font-size: 12px;
    color: #9aa0ad;
    font-weight: normal;
  }

  .empty {
    padding: 10px;
    color: #9aa0ad;
  }
</style>
