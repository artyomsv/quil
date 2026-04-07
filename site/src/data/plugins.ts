// Built-in plugins catalog shown on /plugins. Data is sourced
// directly from calyx/docs/plugin-reference.md and the default
// plugin TOML files shipped with the binary.

export interface PluginEntry {
  slug: string;
  name: string;
  kind: "built-in" | "community";
  description: string;
  spawnExample: string;
  features: string[];
  reference?: string;
}

export const plugins: PluginEntry[] = [
  {
    slug: "terminal",
    name: "Terminal",
    kind: "built-in",
    description:
      "The default shell pane. Runs your system shell (bash, zsh, PowerShell, fish) with OSC 7 auto-injection so pane borders display the live working directory.",
    spawnExample: 'spawn = ["${SHELL:-bash}", "-l"]',
    features: [
      "Runs $SHELL or /bin/bash as fallback",
      "OSC 7 hook auto-injection on bash / zsh / PowerShell (fish emits natively)",
      "500-line ghost buffer replay on reconnect",
      "Full mouse + keyboard support",
    ],
  },
  {
    slug: "claude-code",
    name: "Claude Code",
    kind: "built-in",
    description:
      "An AI session pane that runs Anthropic's Claude Code CLI with a per-pane UUID so sessions auto-resume on reboot. Ghost buffers show the last 500 lines while the session is restoring.",
    spawnExample: 'spawn = ["claude", "--session-id", "{{uuid}}"]\nresume = ["claude", "--resume", "{{uuid}}"]',
    features: [
      "UUID assigned at pane creation time, stored in workspace.json",
      "Auto-resume via `claude --resume <uuid>` on reboot",
      "Idle-state detection surfaces to the notification center",
      "Context-aware status line shows token usage + model name",
    ],
  },
  {
    slug: "ssh",
    name: "SSH",
    kind: "built-in",
    description:
      "A persistent SSH tunnel pane. On reboot Quil re-runs the original SSH command with the same host, port, and forwarding rules.",
    spawnExample: 'spawn = ["ssh", "-o", "ServerAliveInterval=30", "{{host}}"]',
    features: [
      "Stores host, port, and forwarding arguments in workspace.json",
      "Auto-reconnects on disconnect via ServerAliveInterval",
      "Error handler pattern-matches `Connection refused` and `Host key verification failed`",
      "Status line shows connection state + latency",
    ],
  },
  {
    slug: "stripe",
    name: "Stripe CLI",
    kind: "built-in",
    description:
      "A webhook listener pane that runs `stripe listen` with a configurable forward URL. Quil captures the webhook signing secret from the output and exposes it in the pane status line.",
    spawnExample: 'spawn = ["stripe", "listen", "--forward-to", "{{forward}}"]',
    features: [
      "Forward URL stored per pane so the exact `stripe listen` invocation restores",
      "Webhook signing secret extracted from output and surfaced in status line",
      "Error handler pattern-matches common auth failures",
      "Resume strategy: re-spawn with the same forward URL on reboot",
    ],
  },
];

export const pluginAuthoringRef =
  "https://github.com/artyomsv/quil/blob/master/docs/plugin-reference.md";

/** Sample TOML snippet shown on the /plugins page. Plain enough for
 *  a first-time reader to copy, modify, and drop into ~/.quil/plugins/
 *  without reading the full reference. */
export const samplePluginToml = `# ~/.quil/plugins/webhook.toml
[plugin]
name = "webhook"
type = "webhook"
description = "Receives incoming HTTPS webhooks via smee.io"

[spawn]
command = ["smee", "--url", "{{webhook_url}}", "--path", "/hook"]
cwd = "{{project_root}}"

[resume]
# Just re-run the original spawn command — smee is stateless
strategy = "respawn"

[status]
# Grep for the smee URL in the output and surface it on the pane tab
pattern = 'Forwarding (https://smee.io/[a-zA-Z0-9]+)'
template = "{1}"

[error]
# Catch auth failures and mark the pane as errored
pattern = '(?i)(unauthorized|403 forbidden)'
severity = "error"
`;
