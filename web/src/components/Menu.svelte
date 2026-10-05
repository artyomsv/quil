<script module lang="ts">
  // At most one menu is open on the page: opening one closes the other.
  let closeOpen: (() => void) | null = null;
</script>

<script lang="ts">
  import type { MenuItem } from '../lib/menu';

  interface Props {
    label: string;
    items: MenuItem[];
  }

  let { label, items }: Props = $props();
  let open = $state(false);
  let opener: HTMLButtonElement | undefined = $state();
  let list: HTMLUListElement | undefined = $state();
  let pos = $state({ top: 0, right: 0 });

  const shut = (): void => {
    open = false;
  };

  function toggle(e: MouseEvent): void {
    e.stopPropagation();
    if (open) {
      shut();
      return;
    }
    if (closeOpen && closeOpen !== shut) closeOpen();
    closeOpen = shut;
    // Fixed to the viewport under the button: the pane area and the tab bar
    // clip what overflows them.
    const r = opener?.getBoundingClientRect();
    if (r) pos = { top: r.bottom, right: Math.max(0, window.innerWidth - r.right) };
    open = true;
  }

  function close(): void {
    shut();
    opener?.focus();
  }

  // The keyboard moves through the items while the menu is open.
  $effect(() => {
    if (open) list?.querySelector<HTMLButtonElement>('button:not(:disabled)')?.focus();
  });

  function onKey(e: KeyboardEvent): void {
    if (e.key === 'Escape' || e.key === 'Tab') {
      e.stopPropagation();
      if (e.key === 'Escape') e.preventDefault();
      close();
      return;
    }
    if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return;
    e.preventDefault();
    const buttons = Array.from(list?.querySelectorAll<HTMLButtonElement>('button:not(:disabled)') ?? []);
    if (buttons.length === 0) return;
    const at = buttons.indexOf(document.activeElement as HTMLButtonElement);
    const next = e.key === 'ArrowDown' ? at + 1 : at - 1;
    buttons[(next + buttons.length) % buttons.length]?.focus();
  }

  function pick(e: MouseEvent, it: MenuItem): void {
    e.stopPropagation();
    shut();
    it.run();
  }
</script>

<svelte:window onclick={shut} />
<span class="menu">
  <button
    class="menu-button"
    aria-haspopup="menu"
    aria-expanded={open}
    aria-label={label}
    title={label}
    bind:this={opener}
    onclick={toggle}>⋯</button
  >
  {#if open}
    <ul
      role="menu"
      tabindex="-1"
      data-modal
      style:top="{pos.top}px"
      style:right="{pos.right}px"
      bind:this={list}
      onkeydown={onKey}
    >
      {#each items as it, i (i)}
        <li role="none">
          <button role="menuitem" disabled={it.disabled} onclick={(e) => pick(e, it)}
            >{it.label}{#if it.key}<span class="key" aria-hidden="true">{it.key}</span>{/if}</button
          >
        </li>
      {/each}
    </ul>
  {/if}
</span>

<style>
  .menu {
    position: relative;
    flex: none;
  }

  .menu-button {
    border: 0;
    background: none;
    color: #9aa0ad;
    cursor: pointer;
    font: inherit;
    padding: 0 4px;
  }

  ul {
    position: fixed;
    z-index: 20;
    max-height: 70vh;
    overflow-y: auto;
    margin: 0;
    padding: 4px 0;
    list-style: none;
    background: #1b1e26;
    border: 1px solid #2a2e37;
    min-width: 160px;
  }

  li button {
    display: block;
    width: 100%;
    padding: 4px 12px;
    border: 0;
    background: none;
    color: #e6e8ee;
    text-align: left;
    font: inherit;
    white-space: nowrap;
    cursor: pointer;
  }

  li button:hover:not(:disabled),
  li button:focus-visible {
    background: #2a2e37;
    outline: none;
  }

  .key {
    float: right;
    margin-left: 24px;
    color: #9aa0ad;
    font-family: monospace;
    font-size: 12px;
  }

  li button:disabled {
    color: #5c6170;
    cursor: default;
  }
</style>
