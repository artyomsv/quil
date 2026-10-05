<script lang="ts">
  import type { AgentDot } from '../lib/view';

  // The agent-state dot: one source for its colours, used by the pane title
  // and the sidebar.
  interface Props {
    state: AgentDot;
    title: string;
  }

  let { state, title }: Props = $props();
</script>

<span class="dot {state}" {title}></span>

<style>
  .dot {
    flex: none;
    width: 7px;
    height: 7px;
    border-radius: 50%;
    background: #4a4f5c;
  }

  .dot.working {
    background: #4f9be8;
  }

  .dot.blocked {
    background: #e8a24f;
  }

  .dot.idle {
    background: #5cc27a;
  }
</style>
