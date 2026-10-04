<script lang="ts">
  interface Props {
    text: string;
    retrying: boolean;
  }

  let { text, retrying }: Props = $props();
</script>

<div class="banner" class:retrying role="status">{text}</div>

<style>
  .banner {
    padding: 6px 12px;
    background: #5a2a2a;
    color: #fbe3e3;
  }

  .banner.retrying {
    background: #5a4a1f;
    color: #fbf1d6;
  }
</style>
