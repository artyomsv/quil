<script module lang="ts">
  // At most one menu is open on the page: opening one closes the other.
  let closeOpen: (() => void) | null = null;
</script>

<script lang="ts">
  import { untrack } from 'svelte';
  import type { MenuItem } from '../lib/menu';

  interface Props {
    label: string;
    items: MenuItem[];
    // Runs after Escape or a pick closes the menu; the page points it at the
    // active terminal, so typing goes on there (spec §5.5). Without it the
    // menu button takes the focus back.
    onclose?: () => void;
    // A menu with no button, open from the start and gone when it closes:
    // a picker the page opens itself (the overlay's repository choice).
    auto?: boolean;
  }

  let { label, items, onclose, auto = false }: Props = $props();
  let open = $state(untrack(() => auto));
  let opener: HTMLButtonElement | undefined = $state();
  let list: HTMLUListElement | undefined = $state();
  let pos = $state({ top: 0, right: 0 });

  // An auto menu exists only while open, so closing it in any way ends it.
  const shut = (): void => {
    const was = open;
    open = false;
    if (auto && was) onclose?.();
  };
  if (untrack(() => auto)) {
    if (closeOpen) closeOpen();
    closeOpen = shut;
  }

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

  // close is Escape or Tab: the button takes the focus back, or onclose
  // moves it on.
  function close(escape: boolean): void {
    if (auto) {
      shut();
      return;
    }
    shut();
    if (escape && onclose) onclose();
    else opener?.focus();
  }

  // The keyboard moves through the items while the menu is open.
  $effect(() => {
    if (open) list?.querySelector<HTMLButtonElement>('button:not(:disabled)')?.focus();
  });

  function onKey(e: KeyboardEvent): void {
    if (e.key === 'Escape' || e.key === 'Tab') {
      e.stopPropagation();
      if (e.key === 'Escape') e.preventDefault();
      close(e.key === 'Escape');
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

  // The item runs before the menu reports closing: a pick that opens a
  // dialog has it in place first, and the dialog then takes the focus.
  function pick(e: MouseEvent, it: MenuItem): void {
    e.stopPropagation();
    open = false;
    it.run();
    onclose?.();
  }
</script>

<svelte:window onclick={shut} />
<span class="menu">
  {#if !auto}
    <button
      class="menu-button"
      aria-haspopup="menu"
      aria-expanded={open}
      aria-label={label}
      title={label}
      bind:this={opener}
      onclick={toggle}>⋯</button
    >
  {/if}
  {#if open}
    <ul
      role="menu"
      tabindex="-1"
      aria-label={label}
      data-modal
      class:auto
      style:top={auto ? undefined : `${pos.top}px`}
      style:right={auto ? undefined : `${pos.right}px`}
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

  ul.auto {
    top: 20%;
    left: 50%;
    transform: translateX(-50%);
    max-width: 90vw;
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
