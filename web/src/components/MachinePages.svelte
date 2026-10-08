<script lang="ts">
  import type { App } from '../lib/app.svelte';
  import type { KillProcessResp } from '../lib/protocol';
  import { sanitizeRemoteText } from '../lib/sanitize';
  import Confirm from './Confirm.svelte';
  import PluginsPage from './PluginsPage.svelte';
  import ProcessesPage from './ProcessesPage.svelte';
  import UpdatePage from './UpdatePage.svelte';

  interface Props {
    app: App;
  }

  let { app }: Props = $props();
  const p = $derived(app.panel);
  let killing = $state(false);

  // kill sends the confirmed kill once and goes back to the list; a refusal
  // is a normal answer and says why.
  async function kill(paneId: string, pid: number, startMs: number): Promise<void> {
    if (killing) return;
    killing = true;
    const o = await app.killProcess(paneId, pid, startMs);
    killing = false;
    const r = (o.reply?.payload ?? null) as KillProcessResp | null;
    if (r?.refused) app.showNotice(`Not killed: ${sanitizeRemoteText(r.refused)}`);
    else if (o.ok) app.showNotice('Killed');
    if (app.panel?.kind === 'kill') app.openPanel({ kind: 'processes' });
  }
</script>

<!-- The process list stays mounted under its kill confirm, so it keeps its
     rows and its polling. -->
{#if p?.kind === 'processes' || p?.kind === 'kill'}
  <ProcessesPage {app} />
{/if}
{#if p?.kind === 'plugins'}
  <PluginsPage {app} />
{:else if p?.kind === 'update'}
  <UpdatePage {app} />
{:else if p?.kind === 'kill'}
  {@const k = p}
  <Confirm
    title="Kill process"
    body={`Kill ${sanitizeRemoteText(k.name)} (PID ${k.pid})?`}
    confirmLabel="Kill"
    busy={killing}
    onconfirm={() => void kill(k.paneId, k.pid, k.startMs)}
    oncancel={() => app.openPanel({ kind: 'processes' })}
  />
{/if}
