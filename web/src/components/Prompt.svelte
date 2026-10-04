<script lang="ts">
  import { untrack } from 'svelte';

  interface Props {
    title: string;
    value: string;
    submitLabel: string;
    onsubmit: (value: string) => void;
    oncancel: () => void;
  }

  let { title, value: initial, submitLabel, onsubmit, oncancel }: Props = $props();
  // The field starts from the value the dialog opened with; later prop
  // changes (a state frame renaming the same thing) do not overwrite typing.
  let value = $state(untrack(() => initial));
  let field: HTMLInputElement | undefined = $state();
  $effect(() => {
    field?.focus();
    field?.select();
  });

  function submit(e: SubmitEvent): void {
    e.preventDefault();
    const v = value.trim();
    if (v !== '') onsubmit(v);
  }
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
    <form onsubmit={submit}>
      <input type="text" aria-label={title} bind:value bind:this={field} />
      <div class="buttons">
        <button type="button" onclick={oncancel}>Cancel</button>
        <button type="submit" class="primary" disabled={value.trim() === ''}>{submitLabel}</button>
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
