<script lang="ts">
  interface Props {
    text: string;
    onclose: () => void;
  }

  let { text, onclose }: Props = $props();
</script>

<div class="notice" role="status">
  <span>{text}</span>
  <button aria-label="Dismiss" onclick={onclose}>×</button>
</div>

<style>
  .notice {
    display: flex;
    gap: 8px;
    align-items: center;
    padding: 4px 10px;
    background: #3a2f1b;
    color: #f0d79a;
    font-size: 13px;
  }

  .notice span {
    flex: 1;
  }

  .notice button {
    border: 0;
    background: none;
    color: inherit;
    cursor: pointer;
    font: inherit;
  }
</style>
