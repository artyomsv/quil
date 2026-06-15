// Built-in plugins catalog shown on /plugins. Data is sourced
// directly from calyx/docs/plugin-reference.md and the default
// plugin TOML files shipped with the binary.

export interface PluginEntry {
  slug: string;
  name: string;
  kind: "built-in" | "community";
  /** True when the plugin ships in Quil but isn't yet considered production-stable.
   *  Rendered as an extra BETA badge alongside the kind tag on /plugins. */
  beta?: boolean;
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
    slug: "opencode",
    name: "OpenCode",
    kind: "built-in",
    beta: true,
    description:
      "An AI session pane that runs [opencode](https://opencode.ai), the second production AI integration alongside Claude Code. A setup dialog asks for the working directory; sessions resume across daemon restarts to the exact conversation (not just the most recent in CWD) via a small JS plugin Quil registers through opencode's plugin runtime. The user's own opencode plugins, agents, and modes remain active — Quil's plugin is additive.",
    spawnExample:
      '# opencode.toml — relevant fields\n[plugin]\nname = "opencode"\nschema_version = 1\n\n[command]\ncmd = "opencode"\nprompts_cwd = true\n\n[[command.toggles]]\nname = "print_logs"\nlabel = "Print logs to stderr (debug)"\nargs_when_on = ["--print-logs"]\ndefault = false\n\n[persistence]\nstrategy = "session_scrape"\nresume_args = ["--continue"]',
    features: [
      "Setup dialog (Ctrl+N → AI → OpenCode) browses the filesystem starting from the active pane's OSC 7 working directory, same UX as Claude Code.",
      "Session-id rotation tracked via opencode's first-class plugin runtime — Quil writes a small JS plugin to `$QUIL_HOME/opencodehook/` and injects it via the `OPENCODE_CONFIG_CONTENT` env var per spawn, with **zero writes** into `~/.config/opencode/`.",
      "On daemon restart the pane respawns with `opencode --session <id>` so each pane reattaches to its own conversation — including any rotation from `/new`, fork, or compaction during the previous run.",
      "If the recorded id is missing or fails shape validation, the pane falls back to `opencode --continue` (most-recent in CWD) — never to an empty restore.",
      "`OPENCODE_CONFIG_CONTENT` merges with the user's existing opencode config, so any user-installed plugins, agents, and modes remain active inside Quil-spawned opencode panes.",
      "`--pure` deliberately not exposed as a toggle: it disables external plugins (including Quil's tracker). Run opencode outside Quil if you need pure mode.",
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
  {
    slug: "lazygit",
    name: "Lazygit",
    kind: "built-in",
    description:
      "A git UI pane backed by [lazygit](https://github.com/jesseduffield/lazygit). Open it as an ordinary pane from Ctrl+N → Tools, or toggle it as a full-tab overlay over any pane with Alt+G — pointed at the git repository of whatever directory the active pane is working in. Offered only when the lazygit binary is found on PATH.",
    spawnExample:
      '# lazygit.toml — relevant fields\n[plugin]\nname = "lazygit"\nschema_version = 1\n\n[command]\ncmd = "lazygit"\ndetect = "lazygit --version"\nprompts_cwd = true\ndiscover = "git"          # list nearby repos in the setup dialog\n\n[persistence]\nstrategy = "rerun"\nghost_buffer = false',
    features: [
      "Alt+G toggles a per-tab lazygit overlay for the repo resolved from the active pane's working directory; press it again to hide — the process keeps running, so re-opening is instant with its UI state intact.",
      'discover = "git" turns the setup-dialog directory step into a repo picker: the enclosing repository plus one-level subfolders (up to ten), with a Browse… fallback to the plain directory browser.',
      "When several repositories are found near the pane, a picker lets you choose which one to open.",
      "Repo discovery is a pure filesystem walk that canonicalises paths and rejects UNC/device paths, so an untrusted working directory can't steer it onto a network share.",
      "Overlays are ephemeral — one per tab, excluded from workspace snapshots, recreated with one keypress, and auto-destroyed when you quit lazygit (q).",
    ],
  },
  {
    slug: "k9s",
    name: "k9s",
    kind: "built-in",
    description:
      "A Kubernetes cluster TUI backed by [k9s](https://github.com/derailed/k9s). Open it as an ordinary pane from Ctrl+N → Tools — it connects to whatever cluster your kubeconfig points at (KUBECONFIG / ~/.kube/config). Offered only when the k9s binary is found on PATH. Cross-platform: Windows, macOS, Linux.",
    spawnExample:
      '# k9s.toml — relevant fields\n[plugin]\nname = "k9s"\nschema_version = 2\n\n[command]\ncmd = "k9s"\ndetect = "k9s version"\nprompts_cwd = false       # k9s is cluster-scoped, not directory-scoped\ndiscover = "kube"         # context pick-list in the setup dialog\n\n[[command.toggles]]\nname = "readonly"\nlabel = "Read-only (disable all cluster-modifying commands)"\nargs_when_on = ["--readonly"]\ndefault = false\n\n[persistence]\nstrategy = "rerun"\nghost_buffer = false',
    features: [
      "Opens as a normal pane (Ctrl+N → Tools → k9s) — a long-lived monitoring view you can split alongside other panes, not an overlay.",
      'discover = "kube" gives the setup dialog a context pick-list: "Default context" (your kubeconfig current-context) plus the contexts found in KUBECONFIG / ~/.kube/config, with the current one marked. The choice is pinned via --context.',
      "Cluster connection comes from the standard kubeconfig resolution (KUBECONFIG env, then ~/.kube/config) — no working-directory prompt.",
      "Read-only toggle appends --readonly so the pane can browse a cluster without exposing any mutating commands; a start-on-Pods toggle opens k9s directly on the pods view.",
      "Binary-gated: the entry is greyed out in Ctrl+N when k9s is not installed. On daemon restart the pane re-runs k9s and reconnects (rerun strategy, no stale-frame replay).",
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
