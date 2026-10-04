<script lang="ts">
  interface Props {
    title: string;
    body: string;
    confirmLabel: string;
    checkLabel?: string;
    onconfirm: (checked: boolean) => void;
    oncancel: () => void;
  }

  let { title, body, confirmLabel, checkLabel, onconfirm, oncancel }: Props = $props();
  let checked = $state(false);
  let ok: HTMLButtonElement | undefined = $state();
  // The confirm button holds the focus, so Enter confirms.
  $effect(() => ok?.focus());
</script>

<div class="backdrop" data-modal role="presentation" onclick={oncancel}>
  <div
    class="dialog"
    role="dialog"
    aria-modal="true"
    aria-label={title}
    tabindex="-1"
    onclick={(e) => e.stopPropagation()}
    onkeydown={(e) => {
      if (e.key === 'Escape') {
        e.stopPropagation();
        oncancel();
      }
    }}
  >
    <h2>{title}</h2>
    <p>{body}</p>
    {#if checkLabel}
      <label><input type="checkbox" bind:checked /> {checkLabel}</label>
    {/if}
    <div class="buttons">
      <button onclick={oncancel}>Cancel</button>
      <button bind:this={ok} class="primary" onclick={() => onconfirm(checked)}>{confirmLabel}</button>
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

  p {
    overflow-wrap: anywhere;
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

  .primary {
    border-color: #3d6fd8;
    color: #9dbaf5;
  }
</style>
