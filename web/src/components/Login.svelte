<script lang="ts">
  import type { App } from '../lib/app.svelte';

  interface Props {
    app: App;
  }

  let { app }: Props = $props();
  let code = $state('');
  let error = $state('');
  let busy = $state(false);

  async function submit(e: SubmitEvent): Promise<void> {
    e.preventDefault();
    if (busy || code.trim() === '') return;
    busy = true;
    error = '';
    error = await app.login(code);
    busy = false;
  }
</script>

<main>
  <form onsubmit={submit}>
    <h1>Quil</h1>
    <label for="code">Login code from the quil web terminal</label>
    <input
      id="code"
      bind:value={code}
      autocomplete="off"
      autocapitalize="characters"
      spellcheck="false"
      placeholder="XXXXX-XXXXX"
      disabled={busy}
    />
    <button type="submit" disabled={busy || code.trim() === ''}>Log in</button>
    {#if error}
      <p class="error" role="alert">{error}</p>
    {/if}
  </form>
</main>

<style>
  main {
    height: 100%;
    display: flex;
    align-items: center;
    justify-content: center;
  }

  form {
    display: flex;
    flex-direction: column;
    gap: 10px;
    width: min(320px, 100% - 32px);
  }

  h1 {
    margin: 0 0 8px;
    font-size: 22px;
    font-weight: 600;
  }

  label {
    color: #9aa0ad;
  }

  input {
    font: 16px ui-monospace, 'Cascadia Mono', Menlo, monospace;
    letter-spacing: 1px;
    padding: 8px 10px;
    border: 1px solid #3a3f4b;
    border-radius: 4px;
    background: #0e1013;
    color: inherit;
  }

  button {
    padding: 8px 10px;
    border: 0;
    border-radius: 4px;
    background: #3d6fd8;
    color: #fff;
    font: inherit;
    cursor: pointer;
  }

  button:disabled {
    opacity: 0.5;
    cursor: default;
  }

  .error {
    margin: 0;
    color: #f08a8a;
  }
</style>
