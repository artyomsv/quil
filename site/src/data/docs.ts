// /docs/ — the field manual's data. It routes to the docs on GitHub and
// copies none of them. Links are checked on every build (check-dist rule 6).
import { GITHUB_BLOB } from "./seo";

export interface DocLink { file: string; label: string; anchor?: string }
export interface ManualEntry {
  id: string;
  /** One of the six tour stops (ids match src/data/usecases.ts). */
  tour?: boolean;
  persona: string;
  heading: string;
  /** Backticks mark code. */
  steps: string[];
  keys: [string, string][];
  read: DocLink[];
}

export const docHref = (d: DocLink) => GITHUB_BLOB + d.file + (d.anchor ? `#${d.anchor}` : "");

const F = (label: string, anchor: string): DocLink => ({ file: "docs/features.md", label, anchor });
const M = (label: string, anchor: string): DocLink => ({ file: "docs/mcp.md", label, anchor });
const P = (label: string, anchor: string): DocLink => ({ file: "docs/plugin-reference.md", label, anchor });
const T = (label: string, anchor: string): DocLink => ({ file: "docs/workspace-templates.md", label, anchor });
const X = (label: string, anchor: string): DocLink => ({ file: "docs/sandbox-panes.md", label, anchor });
const KB = (label: string, anchor: string): DocLink => ({ file: "docs/keybindings.md", label, anchor });

export const manual: ManualEntry[] = [
  { id: "agents", tour: true, persona: "AGENTS, SIDE BY SIDE",
    heading: "Run Claude Code, Codex and OpenCode at once, each with its own folder and conversation.",
    steps: ["Split the pane: `Alt+Shift+H` side by side, `Alt+Shift+V` stacked.", "Type `claude`, `codex` or `opencode` in the new shell. The pane becomes that agent.", "For its own checkout, use `Ctrl+N` and pick Worktree → `+ new branch…`."],
    keys: [["Alt+Shift+H", "split"], ["Alt+Shift+V", "split"], ["Ctrl+N", "new pane"]],
    read: [F("Hand-started agents", "hand-started-agents"), F("Built-in plugins", "built-in-plugins"), F("Spawn a pane in a worktree", "spawn-a-pane-in-a-worktree"), F("AI session resume", "ai-session-resume")] },
  { id: "orchestrator", tour: true, persona: "AN AGENT THAT RUNS THE OTHERS",
    heading: "Let one agent open panes, start other agents, hand out tasks and read the results.",
    steps: ["Add Quil to your AI client as an MCP server: `quil mcp`.", "Open a team from the palette: `New from template` → agent-team.", "The orchestrator calls `delegate_task`. Each result is typed back into its prompt."],
    keys: [["Alt+Shift+P", "palette"], ["quil mcp", "36 tools"]],
    read: [M("Wiring Quil into your AI client", "wiring-quil-into-your-ai-client"), M("Delegating work to another pane", "delegating-work-to-another-pane"), M("The 36 tools", "the-36-tools"), T("Shipped templates and limits", "shipped-templates-and-limits")] },
  { id: "remote", tour: true, persona: "THE WORK RUNS ELSEWHERE",
    heading: "Run the work on a server. The laptop only shows it, and can close.",
    steps: ["Attach a whole session: `quil --remote gpu01`.", "Or add the host as a project: `Alt+Shift+N` → `Remote (ssh)`. Quil installs itself there when needed.", "If the link drops, the panes keep running and Quil reconnects with backoff."],
    keys: [["quil --remote <host>", ""], ["quil remote setup <host>", ""]],
    read: [F("Remote daemon over SSH", "remote-daemon-over-ssh"), F("When the link drops", "when-the-link-drops"), F("Projects on another machine", "projects-on-another-machine"), { file: "docs/remote-windows.md", label: "Windows remotes over SSH" }] },
  { id: "sandbox", tour: true, persona: "AN AGENT IN A BOX",
    heading: "Give an agent skipped permissions inside a Docker container that only sees its checkout.",
    steps: ["Build the image once: `scripts/sandbox-image.sh`.", "Tick `Run in a Docker container` in the agent's setup dialog.", "Closing the pane removes the container. Its commits stay in your repository."],
    keys: [["scripts/sandbox-image.sh", ""], ["--with codex,opencode", "more agents"]],
    read: [X("Using it", "using-it"), X("Building the image", "building-the-image"), X("What the sandbox does and does not bound", "what-the-sandbox-does-and-does-not-bound")] },
  { id: "too-much", tour: true, persona: "TOO MUCH GOING ON",
    heading: "See which agent needs you, and find any text in any pane.",
    steps: ["Read the sidebar: ▲ waiting on you, ⠹ working, ✓ finished while you were away.", "Jump to the agent that has waited longest: `Alt+Shift+A`.", "Search actions, tabs, projects and every pane's scrollback: `Alt+Shift+P`. Enter goes to the pane."],
    keys: [["Alt+Shift+A", "oldest waiting"], ["Alt+Shift+P", "search"], ["Alt+N", "notifications"]],
    read: [F("Projects", "projects"), F("Command palette", "command-palette"), F("Notification center", "notification-center"), F("Desktop notifications", "desktop-notifications")] },
  { id: "remember", tour: true, persona: "THE WORKSPACE REMEMBERS",
    heading: "Restart the machine and get the same tabs, splits, folders, history and agent conversations back.",
    steps: ["Run `quil` after the restart. The workspace is read from `~/.quil`.", "Agents resume their sessions: `claude --resume`, `codex resume`, `opencode --session`.", "Leave a note on a pane with `Alt+E`. It survives the restart too."],
    keys: [["quil", "start"], ["Alt+E", "pane notes"], ["Alt+R", "restart a pane"]],
    read: [F("Reboot-proof sessions", "reboot-proof-sessions"), F("Lazy restore", "lazy-restore"), F("AI session resume", "ai-session-resume"), F("Pane notes", "pane-notes")] },
  { id: "parallel-builder", persona: "THE PARALLEL BUILDER",
    heading: "Keep a feature, its tests and a bug fix moving at once, with the app, logs and database on one screen.",
    steps: ["Make a project for the repository: `Alt+Shift+N`.", "Give each agent its own worktree from its setup dialog.", "Keep shells for the dev server and logs, and a lazysql pane for the database."],
    keys: [["Alt+Shift+N", "new project"], ["Ctrl+T", "new tab"], ["Alt+1…9", "switch tab"]],
    read: [F("Projects", "projects"), F("lazysql integration", "lazysql-integration"), F("Rearranging a tab's panes", "rearranging-a-tabs-panes")] },
  { id: "workflow-builder", persona: "THE WORKFLOW BUILDER",
    heading: "Use pane types that restore the way their tool needs, and add your own in TOML.",
    steps: ["Pick a type in `Ctrl+N`: a shell, an agent, lazygit, hunk, k9s, lazysql, SSH or Stripe.", "Describe your own type in one file in `~/.quil/plugins/`.", "Load it with `F1 → Plugins → Reload`. Nothing to compile."],
    keys: [["Ctrl+N", "new pane"], ["F1", "plugins"]],
    read: [P("Quick Start", "quick-start"), P("Strategy Reference", "strategy-reference"), F("Pane setup dialog", "pane-setup-dialog")] },
  { id: "prototyper", persona: "THE PROTOTYPER",
    heading: "Try several approaches at once, each in its own worktree, and keep the one that works.",
    steps: ["Open one agent per idea, each on `+ new branch…`.", "Review the winner over the whole tab: `Alt+D` opens hunk.", "Close the rest with `Ctrl+W` and tick `Also delete its worktree`. The branch is kept."],
    keys: [["Alt+D", "hunk"], ["Ctrl+W", "close"]],
    read: [F("Spawn a pane in a worktree", "spawn-a-pane-in-a-worktree"), F("Hunk integration", "hunk-integration"), T("Create a workspace", "create-a-workspace")] },
  { id: "reviewer", persona: "THE REVIEWER",
    heading: "Read what an agent changed, and what you asked it, before anything ships.",
    steps: ["`Alt+D` opens hunk over the tab with the working-tree diff.", "`Alt+G` opens lazygit in the same slot to stage and commit.", "`Alt+Shift+I` lists every prompt you sent to that agent."],
    keys: [["Alt+D", "hunk"], ["Alt+G", "lazygit"], ["Alt+Shift+I", "input history"]],
    read: [F("Hunk integration", "hunk-integration"), F("Lazygit integration", "lazygit-integration"), F("Input history (AI panes)", "input-history-ai-panes")] },
  { id: "on-call", persona: "THE ON-CALL ENGINEER",
    heading: "Keep the cluster, the database, the servers and the webhooks open, and get them back after a restart.",
    steps: ["Open k9s with its read-only toggle on the kube context you need.", "Open lazysql read-only. SSH and Stripe panes re-run their command after a restart.", "Pin a pane you must not forget: `Mark attention` (◆) in the pane menu, `Alt+A`."],
    keys: [["Ctrl+N", "Tools"], ["Alt+A", "pane menu"]],
    read: [F("k9s integration", "k9s-integration"), F("lazysql integration", "lazysql-integration"), P("Strategy Reference", "strategy-reference")] },
  { id: "two-windows", persona: "ONE WORKSPACE, TWO WINDOWS",
    heading: "Attach from the desk and the laptop at the same time, and see the same workspace in both.",
    steps: ["Run `quil` in a second terminal, local or with `--remote`.", "Both windows show the same projects, tabs and panes, and mirror each other's layout.", "The status bar shows `[master]` or `[follower]`. The master sets pane sizes."],
    keys: [["quil", "second window"], ["client.take_control", "bindable"]],
    read: [F("Multi-client sync", "multi-client-sync"), KB("Multi-client", "multi-client")] },
  { id: "windows", persona: "THE WINDOWS DEVELOPER",
    heading: "Use all of it on native Windows, without WSL.",
    steps: ["Install from the Windows zip. Panes run on ConPTY.", "Press `F8` to paste a screenshot into an agent pane as a file path.", "Get toasts that open the pane: `quil notify setup`."],
    keys: [["F8", "paste image"], ["quil notify setup", "toasts"]],
    read: [{ file: "docs/installation.md", label: "Windows", anchor: "windows" }, F("Image paste from clipboard", "image-paste-from-clipboard"), F("Desktop notifications", "desktop-notifications")] },
];

export const shelf: { file: string; what: string }[] = [
  { file: "README.md", what: "What Quil is, install, five keys" },
  { file: "docs/installation.md", what: "Every way to install it" },
  { file: "docs/quick-start.md", what: "First launch, step by step" },
  { file: "docs/features.md", what: "Every capability, by area" },
  { file: "docs/keybindings.md", what: "The keymap, presets, rebinding" },
  { file: "docs/configuration.md", what: "Every key in config.toml" },
  { file: "docs/workspace-templates.md", what: "templates.toml: teams of panes" },
  { file: "docs/mcp.md", what: "The MCP server and its 36 tools" },
  { file: "docs/plugin-reference.md", what: "Writing a pane type in TOML" },
  { file: "docs/sandbox-panes.md", what: "Agents inside Docker" },
  { file: "docs/remote-windows.md", what: "A Windows machine as the remote" },
  { file: "docs/troubleshooting.md", what: "When something is wrong" },
  { file: "docs/architecture.md", what: "Why it is built this way: the ADRs" },
  { file: "docs/roadmap.md", what: "What shipped, what is next" },
  { file: "docs/vision.md", what: "Why Quil exists" },
  { file: "CHANGELOG.md", what: "Every release" },
];
