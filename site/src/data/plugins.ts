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
      "An AI session pane that runs Anthropic's Claude Code CLI. A setup dialog asks for the working directory (so project-specific `.claude/` context is preserved) and offers a `Dangerously skip permissions` checkbox for unattended runs. Sessions resume across daemon restarts.",
    spawnExample:
      '# claude-code.toml — relevant fields\n[plugin]\nname = "claude-code"\nschema_version = 2\n\n[command]\ncmd = "claude"\nprompts_cwd = true\n\n[[command.toggles]]\nname = "skip"\nlabel = "Dangerously skip permissions"\nargs_when_on = ["--dangerously-skip-permissions"]\ndefault = false\n\n[persistence]\nstrategy = "preassign_id"\nresume_args = ["--continue"]',
    features: [
      "Setup dialog (Ctrl+N → AI → Claude Code) browses the filesystem starting from the active pane's OSC 7 working directory. On Windows, backspace at a drive root shows all available drives for cross-drive navigation.",
      "`Dangerously skip permissions` checkbox is off by default; when on, the toggle args persist across daemon restarts (the resume strategy now appends ResumeArgs to InstanceArgs instead of replacing).",
      "Auto-resume on daemon restart via `claude --continue`, with daemon-side `EvalSymlinks` re-resolution closing the spawn-time TOCTOU window.",
      "Idle-state detection surfaces to the notification center.",
      "Pairs with the Win32 clipboard image paste proxy: take a screenshot, press F8 in a Claude Code pane, and the file path is typed in for the AI to read.",
      "Plugin auto-upgrade: when Quil ships a new schema version for claude-code.toml, a side-by-side merge dialog lets you reconcile your customizations with the new defaults on first launch.",
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
 *  without reading the full reference. Schema matches the live shipping
 *  format documented in docs/plugin-reference.md. */
export const samplePluginToml = `# ~/.quil/plugins/webhook.toml
[plugin]
name = "webhook"
display_name = "Webhook listener"
category = "tools"
description = "Receives incoming HTTPS webhooks via smee.io"
schema_version = 1                # bump when TOML structure changes

[command]
cmd = "smee"
detect = "smee --version"
arg_template = ["--url", "{url}", "--path", "/hook"]
prompts_cwd = true               # ask for the working directory at pane creation

[[command.form_fields]]
name = "name"
label = "Name"
required = true

[[command.form_fields]]
name = "url"
label = "Smee URL"
required = true

[persistence]
strategy = "rerun"               # respawn with the same instance args
ghost_buffer = true

[[notification_handlers]]
pattern = 'Forwarding (https?://smee\\.io/\\S+)'
title = "Webhook tunnel ready"
severity = "info"

[[error_handlers]]
pattern = '(?i)(unauthorized|403 forbidden)'
title = "Webhook auth failed"
message = "Check your smee.io credentials and re-run the pane."
action = "dialog"
`;
