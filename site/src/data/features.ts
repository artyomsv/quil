// Feature catalog driving the home feature grid and the /features
// deep dive. Bullets come from the Quil README lines 27-95, PRD
// section 4, and ROADMAP milestones M1-M13.

export type IconName =
  | "refresh-ccw"
  | "sparkles"
  | "layers"
  | "puzzle"
  | "bell"
  | "monitor"
  | "notebook-pen"
  | "zap"
  | "terminal"
  | "save"
  | "code"
  | "book-open"
  | "key-round"
  | "heart-pulse"
  | "layout-panel-left";

export interface Feature {
  slug: string;
  icon: IconName;
  title: string;
  blurb: string;
  detail: string[];
  category: "persistence" | "interaction" | "ai" | "extensibility" | "observability";
  /** Optional CDN screenshot (…-800.webp) rendered beside the feature on /features. */
  image?: string;
  /**
   * Maturity marker rendered next to the title on /features. Omit for shipped,
   * stable features — the absence of a badge is the default claim.
   *
   * "beta" means usable for real work but with documented limits the user
   * should read before relying on it. Set it honestly: a badge that overstates
   * readiness costs more trust than the feature gains.
   */
  badge?: "beta";
}

export const features: Feature[] = [
  // --- Persistence ---------------------------------------------------
  {
    slug: "reboot-proof-sessions",
    image: "https://cdn.stukans.com/quil/screenshots/pane-restoration-800.webp",
    icon: "refresh-ccw",
    title: "Reboot-proof sessions",
    blurb:
      "Workspaces survive full host reboots. Type `quil` after a restart and everything snaps back.",
    category: "persistence",
    detail: [
      "Continuous snapshot of tabs, panes, layout, and working directories to ~/.quil/workspace.json.",
      "Ghost buffers render the last 500 lines of each pane instantly while shells re-initialise. Large buffers are sent in 8 KB chunks with 2 ms yield between each to prevent input starvation.",
      "Pane split tree is serialised to JSON and restored on reconnect — same horizontal/vertical nesting, same ratios.",
      "Target boot-to-productive time: under 30 seconds.",
    ],
  },
  {
    slug: "live-cwd-tracking",
    icon: "layout-panel-left",
    title: "Live CWD tracking",
    blurb:
      "Pane borders show the shell's current directory in real time — no config, no manual hooks.",
    category: "persistence",
    detail: [
      "Auto-injects OSC 7 hooks into bash, zsh, and PowerShell at spawn time.",
      "Fish emits OSC 7 natively, no injection needed.",
      "The directory shown on the pane border updates on every cd, pushd, and popd.",
    ],
  },

  // --- AI ------------------------------------------------------------
  {
    slug: "ai-session-resume",
    icon: "sparkles",
    title: "AI session resume",
    blurb:
      "Claude Code conversations resume automatically after a reboot. No copy-paste, no context rebuild.",
    category: "ai",
    detail: [
      "Each AI pane gets a UUID at creation time. On restart Quil runs `claude --resume <session-id>` automatically.",
      "Works for any AI tool that exposes a session ID — Claude Code (production) and OpenCode (beta) today, more to come.",
      "For tools without a session ID, plugins can fall back to regex scraping the last state or replaying a command.",
    ],
  },
  {
    slug: "resume-session-picker",
    icon: "book-open",
    title: "Pick a past session when you open a pane",
    blurb:
      "New Claude Code pane, but continue yesterday's conversation. The setup dialog lists the sessions for that folder — titled with your own first prompt.",
    category: "ai",
    detail: [
      "The Session field lists every Claude Code session recorded for the folder you picked, newest first, each row showing a relative age and the first prompt you typed in it.",
      "Press i on a session for details: when it started, when it was last touched, how many prompts you typed, and the last prompt you left it on — the thing that actually identifies a conversation, and which appears nowhere else. Arrow keys re-read for the next session so you can compare candidates.",
      "Beats running --resume inside the pane: you get real titles and ages without waiting for Claude to boot its own picker, and Quil knows which session the pane is on from the first instant — so restore, input history, and the model/context readout are correct immediately.",
      "Sessions already open in another pane are shown greyed and cannot be picked — two claude processes on one transcript would overwrite each other's history.",
      "The field stays collapsed to a single line until you Tab onto it, and the listing is only fetched when you do, so creating an ordinary fresh pane costs nothing.",
      "Opt-in per pane type via `[command] sessions = \"claude\"`; picking a session spawns `claude --resume <id>` with your permission-mode toggles still applied.",
    ],
  },
  {
    slug: "mcp-server",
    image: "https://cdn.stukans.com/quil/screenshots/claude-code-quil-mcp-800.webp",
    icon: "zap",
    title: "MCP server for AI agents",
    blurb:
      "Run `quil mcp` and an AI agent can list panes, read output, send keystrokes, and snapshot your workspace.",
    category: "ai",
    detail: [
      "18 tools exposed over the Model Context Protocol (Anthropic's open standard for AI tool use).",
      "Tools include: list/create/destroy panes, read pane output, send keys, switch tabs, screenshot a pane, watch the notification queue, query memory usage per pane, and more.",
      "Lets any MCP-capable client (Claude Desktop, Claude Code, Cursor) reach directly into your running Quil session.",
    ],
  },
  {
    slug: "clipboard-image-paste",
    icon: "save",
    title: "Clipboard image paste",
    blurb:
      "Press paste on a screenshot and Quil decodes it, saves a PNG, and types the file path into the active pane — works around Claude Code's broken Windows clipboard reader.",
    category: "ai",
    detail: [
      "Win32 DIB / DIBV5 reader handles screenshots from Snipping Tool, Win+Shift+S, and other capture apps.",
      "Decodes 24bpp BI_RGB and 32bpp BI_BITFIELDS, including the all-zero-alpha promotion that catches apps which leave the alpha channel uninitialised.",
      "Files land in ~/.quil/paste/quil-paste-<ts>-<rand>.png with owner-only 0o600 / 0o700 permissions and an 8-byte crypto/rand suffix so a co-tenant can't enumerate them.",
      "Sidesteps the upstream Claude Code Windows clipboard image bug (anthropics/claude-code#32791) — any AI tool with file-reading tools picks the file up via the typed path.",
      "Three paste keys: Ctrl+V (default), Ctrl+Alt+V, and F8. F8 is the recommended Windows trigger because Windows Terminal eats Ctrl+V before it reaches the TUI.",
    ],
  },

  {
    slug: "work-indicators",
    icon: "bell",
    title: "Agent work indicators",
    blurb:
      "A spinner shows which AI panes are mid-turn; when one finishes or waits for your input, it stays marked green until you actually look at it.",
    category: "ai",
    detail: [
      "Work state is derived entirely from the agent's own hook events (Claude Code hooks, OpenCode plugin bus) — no output polling, no heuristics.",
      "While an agent works, a spinner animates on the pane border and its tab label.",
      "When a turn completes — or the agent parks on a permission prompt or a question — the pane border turns green and the tab label of a background tab turns green with it.",
      "No timer: the green mark persists until you focus that exact pane (click it, Alt+Arrow onto it, or switch to its tab). With several agent panes split in one tab, the border pinpoints which one needs you.",
      "A crash never shows a green mark — process exit clears the spinner without claiming the turn finished.",
    ],
  },

  {
    slug: "model-context-readout",
    icon: "sparkles",
    title: "Model and context readout",
    blurb:
      "The status bar shows which model an AI pane is on and how much context it has burned — without asking the agent or reading its header.",
    category: "ai",
    detail: [
      "AI panes show the model id and context-window token count from the last completed turn, e.g. `opus-4.8 · 612k ctx`.",
      "Deliberately tokens, not a percentage: neither Claude Code nor OpenCode records the window size in its data, and a Claude session may be running at 200k or at 1M — a percentage would be a guess presented as a fact.",
      "Right after a compaction the pane reads `· compacting` until the next turn reports the reduced size. The transcript doesn't contain the new count yet at that moment, so showing the old one would be worse than showing nothing.",
      "Rides the same hook-event stream as the work indicators — no polling, no extra IPC, and it keeps working while the pane is muted.",
    ],
  },

  {
    slug: "input-history",
    image: "https://cdn.stukans.com/quil/screenshots/input-history-800.webp",
    icon: "book-open",
    title: "Input history",
    blurb:
      "AI panes bury your prompt under a wall of output. Alt+Shift+I lists every prompt you submitted — open one full-text and copy it back.",
    category: "ai",
    detail: [
      "Alt+Shift+I opens a per-pane list of your past prompts as 3-line previews, newest first; Enter opens the full text in a read-only viewer you can scroll and copy from.",
      "Captured from the agent's own UserPromptSubmit hook, not keystroke scraping — multiline prompts, pastes, and edits are recorded exactly as submitted.",
      "Persists across daemon restarts at ~/.quil/history/<pane>.jsonl (64 KiB per entry, ring-trimmed to the last 200) and is removed when the pane is destroyed.",
      "Opt-in per pane type via `[command] record_history = true` (enabled for Claude Code); other pane types show an empty state. OpenCode support is planned.",
    ],
  },

  // --- Interaction ---------------------------------------------------
  {
    slug: "projects",
    icon: "layout-panel-left",
    image: "https://cdn.stukans.com/quil/screenshots/projects_1_main-800.webp",
    title: "Projects, and a sidebar that watches all of them",
    blurb:
      "Group tabs into named projects with their own root directory. The left sidebar shows every project's agents at once — so one that finished or got stuck while you were elsewhere is visible from where you already are.",
    category: "interaction",
    detail: [
      "A project owns its tabs and remembers which one you left it on. Ctrl+T files a new tab into the project you're on, and switching project switches the whole tab bar. Alt+Shift+N creates one, Alt+P fuzzy-finds, Alt+O bounces between the last two, Alt+Shift+←/→ cycles.",
      "The sidebar (Alt+Shift+S) is a reserved left column, not an overlay. Each project row carries a roll-up of its agents — `◐N` working, `⚠N` blocked waiting on you — and those counts keep updating for projects in the background, which is the entire point.",
      "Under the active project, each pane gets its own glyph: `◐` working (with `⋯N` outstanding subagents), `⚠` blocked on you and the tool it's asking about, `○` idle, `✓` finished while you weren't looking. Click any row to jump there.",
      "Blocked is a separate state from finished. The agents' hook events always carried the distinction — Notification, PermissionRequest, permission.ask — but the old UI only needed \"mark unseen\", so they were collapsed into one. A permission prompt and a completed turn now look different, because they need different things from you.",
      "Alt+Shift+A jumps to whichever agent has been blocked longest, anywhere in the workspace, and cycles on repeated presses. Oldest-first rather than sidebar order: with several agents running, the one waiting longest is the one costing you time.",
      "Each pane row also shows the checkout it sits in — branch, `wt` for a linked worktree, `↑N`/`↓N` against upstream. Refreshed on a background ticker and cached per checkout, so ten panes in one repository cost one git invocation. `git status --porcelain` is deliberately not among them: it's the one call that can take seconds on a large repo without fsmonitor. A probe that doesn't answer keeps its last value and marks it stale rather than guessing.",
      "Existing workspaces migrate on first load into a single project named Default, tab order preserved. No prompt, no data loss, nothing to opt into.",
    ],
  },
  {
    slug: "typed-panes",
    image: "https://cdn.stukans.com/quil/screenshots/claude-code-setup-dialog-800.webp",
    icon: "layers",
    title: "Typed panes",
    blurb:
      "Terminals are not all the same. Quil understands pane types and gives each one context-aware behaviour — including a per-spawn setup dialog with directory browser and runtime checkboxes.",
    category: "interaction",
    detail: [
      "Nine built-in pane types: Terminal, Terminal (keeps content on squeeze), Claude Code, OpenCode (beta), SSH, Stripe, lazygit, k9s, lazysql.",
      "Each type has its own resume strategy, error handler, and status line.",
      "Pane setup dialog (opt-in via plugin TOML): a directory browser pre-filled with the active pane's CWD plus one checkbox per declared `[[command.toggles]]` entry. claude-code uses both — picks up the project's `.claude/` context automatically and offers a `Dangerously skip permissions` toggle for unattended runs.",
      "The directory step remembers where you've been: the last five folders you actually opened a pane in are offered as a one-keystroke quick pick (deleted ones filtered out), and for git-aware pane types the repositories discovered near the active pane take priority. Browse… always drops to the full picker.",
      "Toggle state rides through the existing `InstanceArgs` IPC field and survives daemon restarts; no IPC schema changes.",
      "User-definable additional types via TOML plugin files in ~/.quil/plugins/.",
    ],
  },
  {
    slug: "tmux-splits",
    image: "https://cdn.stukans.com/quil/screenshots/tui-panes-800.webp",
    icon: "layout-panel-left",
    title: "tmux-style splits with spatial navigation",
    blurb:
      "Arbitrarily nested horizontal and vertical splits with mouse hit-testing AND directional Alt+Arrow pane navigation that picks the closest neighbour, not the next leaf in the tree.",
    category: "interaction",
    detail: [
      "Binary split tree, each split with its own direction and ratio.",
      "Click any pane to focus it; the scroll wheel traverses terminal history.",
      "Over a pane running something that handles its own mouse input — claude-code, opencode, vim, htop, lazygit — the wheel scrolls that program's viewport instead. Those apps run on the alternate screen and never fill Quil's scrollback, so the wheel is forwarded to them as the mouse sequence they expect. The daemon is what detects this, so it stays correct even when you reattach to a session that was already running.",
      "Click the scrollbar to jump the thumb; click-and-drag scrolls continuously. The hit zone is three cells wide so off-by-one clicks register as scroll instead of text selection.",
      "Spatial pane navigation: Alt+Left/Right/Up/Down focuses the closest neighbour in that direction. Three tie-breakers (gap, perpendicular overlap, perpendicular center distance) match tmux/vim/iTerm muscle memory.",
      "Drag any tab in the tab bar to reorder it — intermediate tabs slide one slot at a time. A click without motion still switches tabs. The active tab is prefixed with `* ` so it's visible at a glance even when colored.",
      "Tab and Shift+Tab are deliberately NOT bound globally — they fall through to the PTY so shell completion and Claude Code's mode-cycling work naturally. Splits live on Alt+Shift+H / Alt+Shift+V to keep Alt+V free for Claude Code's image paste.",
      "Focus mode (Ctrl+E) expands the active pane full-screen while others keep running in the background.",
    ],
  },
  {
    slug: "mouse-pane-resize",
    image: "https://cdn.stukans.com/quil/screenshots/pane-resize-800.webp",
    icon: "layout-panel-left",
    title: "Mouse drag-resize splits",
    blurb:
      "Grab any border between panes and drag — the split follows your mouse, every nested pane keeps its minimum size, and the child processes see exactly one resize when you let go.",
    category: "interaction",
    detail: [
      "Click-and-drag any split border; the affected panes show a highlight while the drag is active.",
      "Works on arbitrarily nested layouts: the drag is clamped so every pane in both subtrees keeps the 10×4 minimum — not just the two panes touching the border.",
      "The grab zone is wider than the drawn line, and the drawn line always wins over the scrollbar where they overlap — no pixel hunting.",
      "PTY resize and layout persistence fire once, on mouse release — mid-drag the borders move locally, so TUI apps (claude-code, vim, htop) never see resize churn and never repaint mid-drag.",
      "The new ratios ride the existing workspace snapshot, so a drag-resized layout survives daemon restarts and reboots.",
      "Companion pane type — Terminal (keeps content on squeeze): the same shell on an AI-pane-style window-sized canvas, for log tails and watch loops where content survival matters more than width-perfect formatting.",
    ],
  },
  {
    slug: "command-palette",
    image: "https://cdn.stukans.com/quil/screenshots/command-palette-search-1-800.webp",
    icon: "zap",
    title: "Command palette + content search",
    blurb:
      "Alt+Shift+P opens a fuzzy-find launcher for every action and every pane — and as you type it also searches the scrollback of all your panes.",
    category: "interaction",
    detail: [
      "Type a fragment of the intent (split, restart, backend) and the list filters live by fuzzy score; Enter runs the highlighted command, Esc closes.",
      "Entries are grouped under section headers — Go to pane, Tabs, Pane, System — with navigation first (jumping to a pane or tab is the most common reason to open it); headers disappear once you type. Panes are listed by tab.pane index and type so duplicates are easy to tell apart.",
      "Content search is built in — no separate mode. Start typing and, alongside the filtered commands, a Found in panes section lists every pane whose scrollback contains your text (case-insensitive), with a match count and a preview of the most recent hit. Arrow to a match and press Enter to jump straight to that pane. Great for 'which pane had that error / URL / container id?'",
      "It searches each pane's loaded output buffer and never wakes a dormant pane, so it's fast even across many tabs. Because Quil restores panes lazily, a pane you haven't opened yet this session may not show up in results until you visit it.",
      "Every command dispatches into the same handler its keybinding uses — a launcher, not a second code path — and each row shows its shortcut, so the palette teaches the bindings as you use it. Rows that don't apply grey out (input history without an AI pane, lazygit without the binary). Configurable via command_palette; Ctrl+Shift+P is opt-in since many terminals intercept it.",
    ],
  },
  {
    slug: "pane-context-menu",
    image: "https://cdn.stukans.com/quil/screenshots/mouse-right-click-menu-800.webp",
    icon: "layout-panel-left",
    title: "Right-click pane menu",
    blurb:
      "Right-click any pane for a per-pane action menu — history, focus, notes, lazygit, rename, mute, attention pin, restart, close — no keybinding memorization required.",
    category: "interaction",
    detail: [
      "Right-click a pane (or press Alt+A for the active pane) to open a popup targeting the pane under the cursor — its border lights up so there's never doubt which pane the actions will hit, and the menu header shows the pane's name.",
      "Nine actions in three groups: view (input history, enter/exit focus mode, notes, lazygit), pane settings (rename, mute notifications, mark attention), and destructive (restart, close — both keep their confirmation dialogs).",
      "Hover highlights the row under the mouse; arrow keys / j / k navigate; unavailable actions grey out (input history without an AI pane, lazygit without the binary installed).",
      "Mark attention pins a green border that survives focusing the pane — a manual \"don't let me forget this one\" flag, cleared only by unmarking.",
      "Right-click with a text selection active still copies it — the menu only opens on a plain right-click.",
    ],
  },
  {
    slug: "pane-notes",
    image: "https://cdn.stukans.com/quil/screenshots/focus-with-notes-800.webp",
    icon: "notebook-pen",
    title: "Pane notes",
    blurb:
      "Alt+E opens a plain-text editor beside any pane. Notes save automatically and travel with the workspace.",
    category: "interaction",
    detail: [
      "Markdown-compatible plain text, rendered as the pane loses focus.",
      "30-second debounce auto-save, Ctrl+S for explicit save.",
      "Side-by-side layout so you can take notes while the pane keeps producing output.",
    ],
  },

  // --- Extensibility -------------------------------------------------
  {
    slug: "plugin-system",
    icon: "puzzle",
    title: "TOML plugin system",
    blurb:
      "Declare a new pane type in a single TOML file. No compilation, no restart, hot-reload on save.",
    category: "extensibility",
    detail: [
      "Plugin definitions live in ~/.quil/plugins/<name>.toml.",
      "Sections: [plugin], [spawn], [keys], [resume], [error], [status] — each optional.",
      "Declarative config means no shell scripting footguns; the daemon validates the TOML at load time.",
    ],
  },
  {
    slug: "plugin-auto-upgrade",
    icon: "refresh-ccw",
    title: "Plugin auto-upgrade",
    blurb:
      "When Quil ships new plugin features, a side-by-side merge dialog lets you reconcile your config with the new defaults — no silent breakage, no lost customizations.",
    category: "extensibility",
    detail: [
      "Each embedded default plugin carries a `schema_version` number. On startup, Quil compares your on-disk version with the shipped default.",
      "If yours is older, a full-screen split view opens: your config on the left (editable), the new default on the right (read-only). Diff highlighting shows red for your custom lines and green for new additions.",
      "Ctrl+C / Ctrl+V to copy lines from the default into your config, Ctrl+S to save, F5 to accept the full default. Esc is blocked — migration must be resolved before the workspace loads.",
      "Multiple stale plugins get a tab bar. Each must be resolved independently.",
    ],
  },
  {
    slug: "lazygit-overlay",
    image: "https://cdn.stukans.com/quil/screenshots/lazygit-integration-800.webp",
    icon: "code",
    title: "Lazygit overlay",
    blurb:
      "Press Alt+G to drop a full-tab git UI over any pane — pointed at the repository of whatever directory that pane is working in.",
    category: "extensibility",
    detail: [
      "Alt+G toggles a per-tab lazygit overlay for the repository resolved from the active pane's working directory; press it again to hide — the process keeps running, so re-opening is instant with lazygit's UI state intact.",
      "Repositories are discovered automatically near the pane (the enclosing repo plus one level down); when several are found, a picker lets you choose which to open.",
      "Also available as an ordinary pane via Ctrl+N → Tools → Lazygit, where the setup dialog lists the same discovered repositories with a Browse… fallback.",
      "Overlays are ephemeral — one per tab, never persisted, recreated with one keypress, and auto-destroyed when you quit lazygit. Offered only when the lazygit binary is on PATH.",
    ],
  },
  {
    slug: "k9s-clusters",
    icon: "layers",
    title: "k9s for Kubernetes",
    blurb:
      "Open k9s as a pane (Ctrl+N → Tools → k9s) to drive your Kubernetes cluster, with a context picker sourced from the kubeconfig on the machine the daemon runs on.",
    category: "extensibility",
    detail: [
      "k9s opens as an ordinary pane (not an overlay — it's a long-lived monitoring view you can split alongside your other panes).",
      "The setup dialog lists the contexts from KUBECONFIG / ~/.kube/config on the machine the daemon runs on (current one marked) and pins the pane to your choice via --context; \"Default context\" uses that kubeconfig's current-context. In a remote session those are the server's clusters, not your laptop's.",
      "A read-only toggle (--readonly) lets you browse a cluster with all mutating commands disabled, and a start-on-Pods toggle jumps straight to the pods view.",
      "Cross-platform (Windows, macOS, Linux). Offered only when the k9s binary is on PATH — otherwise it shows greyed in Ctrl+N with a link to install it. Re-runs and reconnects on daemon restart.",
    ],
  },
  {
    slug: "lazysql-databases",
    icon: "layers",
    title: "lazysql for databases",
    blurb:
      "Open lazysql as a pane (Ctrl+N → Tools → lazysql) to browse and query MySQL, PostgreSQL, SQLite, and MSSQL from its connection manager.",
    category: "extensibility",
    detail: [
      "lazysql opens as an ordinary pane into its own connection manager — split it alongside your app and AI panes.",
      "By design there's no Quil-side connection picker: lazysql's only launch argument is a full connection string with embedded credentials, so Quil never reads its config or injects a DSN. Credential handling stays inside lazysql (which supports ${env:VAR} substitution to keep passwords out of its config).",
      "A read-only toggle (--read-only) opens a session with data modification disabled.",
      "Cross-platform (Windows, macOS, Linux). Offered only when the lazysql binary is on PATH — otherwise greyed in Ctrl+N with a link to install it. Re-runs on daemon restart.",
    ],
  },

  // --- Observability -------------------------------------------------
  {
    slug: "notification-center",
    icon: "bell",
    title: "Notification center",
    blurb:
      "Quil detects when a pane exits, errors, or goes idle and surfaces it in a dedicated sidebar.",
    category: "observability",
    detail: [
      "Daemon-side event queue with pattern-matching idle analysis.",
      "Process exit detection with exit-code extraction.",
      "Optional sidebar surfaces notifications without interrupting focused work.",
    ],
  },
  {
    slug: "desktop-notifications",
    icon: "bell",
    title: "Desktop notifications",
    blurb:
      "When an agent parks for input while you are in another window, Windows raises a toast. Click it and Quil is already on that project, tab and pane.",
    category: "observability",
    // Windows-only today — a real limit worth reading before relying on this,
    // which is what the badge is for.
    badge: "beta",
    detail: [
      "Fires on the same two states the project sidebar already marks: a pane parked waiting on you (▲) and a turn that finished while you were away (✓). Not on every event — the notification sidebar remains the full log.",
      "Only while the terminal is unfocused. Quil reads terminal focus reporting (DEC 1004), so a toast never interrupts you while you are looking at the pane it is about.",
      "Clicking the toast routes to the exact pane via a registered `quil://` handler — the same jump the attention queue (Alt+Shift+A) performs, including switching project and tab.",
      "One toast per pane, so six agents finishing at once give you six independently clickable toasts rather than a storm of duplicates — a toast only fires on a state change, and a pane already showing one cannot get a second.",
      "Answering a prompt withdraws its toast from Action Center, so the notification surface never keeps claiming attention you have already given.",
      "Opt-in registration: `quil notify setup` writes a Start Menu shortcut and a `quil://` handler, prints exactly what it wrote, and `quil notify setup --remove` is a true inverse. Nothing is written as a side effect of a config flag.",
      "Toggle live from F1 → Settings or `[notification.desktop]` in config.toml. The Settings row reports whether registration is actually in place rather than just echoing the flag.",
      "Windows only. macOS and Linux have no transport that supports click-to-route, so they are deliberately not faked.",
    ],
  },
  {
    slug: "memory-reporting",
    icon: "book-open",
    title: "Memory reporting",
    blurb:
      "Per-pane memory accounting in the status bar and a collapsible breakdown dialog (F1 → Memory).",
    category: "observability",
    detail: [
      "Daemon-side 5 s collector snapshots Go-heap (output ring buffer + ghost snapshot + plugin state) and PTY child resident memory per pane.",
      "Cross-platform RSS: /proc/<pid>/status on Linux, ps -o rss= batched on Darwin, GetProcessMemoryInfo on Windows.",
      "Status bar gains a `mem <n>` segment refreshed every 5 s; F1 → Memory opens a tab/pane tree with expand/collapse and notes-editor byte accounting.",
      "Two MCP tools — `get_memory_report` (per-tab totals + grand total) and `get_pane_memory` (single-pane detail) — expose the layers for external agents.",
    ],
  },
  {
    slug: "auto-update",
    icon: "refresh-ccw",
    title: "Updates without leaving the app",
    blurb:
      "Quil notices a new release, stages it in the background, and installs it on your say-so — no re-running the install script, no manual binary swap.",
    category: "observability",
    detail: [
      "The daemon checks for new releases shortly after startup and once a day after that; the status bar shows `↑ v1.42.0 [ready]` once one is staged, and F1 → Check for updates runs a real check on demand.",
      "Applying swaps both binaries and restarts — your tabs, panes, and AI sessions come back from the workspace snapshot exactly as they do after any restart.",
      "Downloads are checksum-verified before anything is swapped, and the previous binaries are kept as a backup that is restored automatically if the swap fails halfway.",
      "Two settings under `[update]` in config.toml, both in the Settings dialog: `check` (look for releases) and `auto` (download in the background so applying is instant). Turn both off and Quil never contacts GitHub.",
      "Dev and debug builds have the pipeline compiled out entirely — a release binary applied over `quil-dev` would strip its dev-mode wiring and silently point the next launch at your production data directory.",
    ],
  },
  {
    slug: "version-handshake",
    icon: "refresh-ccw",
    title: "Client/daemon version handshake",
    blurb:
      "Upgrade in one step. The client checks the running daemon's version on attach and self-heals when they drift.",
    category: "observability",
    detail: [
      "TUI handshakes with the daemon before attaching. Older daemon → prompt, gracefully stop, auto-spawn the matching daemon from alongside the TUI binary.",
      "Newer daemon than client → TUI refuses to attach and points to the releases page (avoids subtle protocol drift bugs).",
      "Dev/debug builds and unstamped local builds skip the check.",
      "Backed by a new IPC pair (MsgVersionReq/MsgVersionResp) and a shared `internal/version/` package with proper semver comparison — no more lexical-ordering traps with 1.10.0 vs 1.9.0.",
    ],
  },
  {
    slug: "self-healing-daemon",
    icon: "heart-pulse",
    title: "Self-healing daemon",
    blurb:
      "A wedged AI pane can't freeze your workspace, and `quil restart` recovers anything in one command — tabs and AI sessions restored from the last snapshot.",
    category: "observability",
    detail: [
      "`quil restart` stops the daemon with bounded escalation (graceful IPC shutdown with final snapshot → SIGTERM → force-kill, each tier timed out so even a deadlocked daemon can't stall it), cleans stale pid/socket files, starts fresh, and reopens the TUI.",
      "`quil status` answers \"is it actually healthy?\" before you reach for restart: daemon state, pid, version, environment, uptime, and a per-tab/pane breakdown with each pane's state and memory. `--json` for scripts, and exit codes that distinguish healthy from not-running from wedged — so it works in a monitoring loop, not just by eye.",
      "Prints the target environment first — production (~/.quil) vs dev (QUIL_HOME) — so you can never kill the wrong daemon. PID-reuse guard: a recorded PID is only signaled if it actually belongs to a quild binary.",
      "Per-pane input isolation: every pane's stdin is written by its own goroutine behind a bounded queue. A process that stops reading input costs you a 'Pane not accepting input' sidebar warning on that one pane — everything else stays interactive. Alt+R restarts the stuck pane in place with its AI session resumed.",
      "Liveness watchdog: if no workspace snapshot completes for 2 minutes, the daemon writes a full goroutine stack dump to quild.log — a wedge becomes a precise bug report instead of a silent freeze.",
    ],
  },
  {
    slug: "leveled-logging",
    icon: "book-open",
    title: "Leveled logging + in-app log viewer",
    blurb:
      "A single `[logging] level` setting controls 152 existing log call sites and the new debug helpers. F1 opens read-only viewers for the client, daemon, and MCP logs.",
    category: "observability",
    detail: [
      "`internal/logger` wraps Go's stdlib `slog` and bridges every existing `log.Printf` call site at info level — old and new code respect one filter.",
      "Flip `[logging] level = \"debug\"` in config.toml to trace clipboard pipeline, per-key handler decisions, and Win32 image read step-by-step.",
      "F1 → About → View client log / daemon log / MCP logs opens a read-only TextEditor viewing the tail (256 KB) of each file. Symlink-rejecting via os.Lstat plus a re-stat through the open handle defeats TOCTOU swap.",
      "Alt+Up / Alt+Down jump the cursor by `[ui] log_viewer_page_lines` (default 40, configurable). The same `TextEditor.ReadOnly` flag is now available for any other look-but-don't-touch dialog.",
      "Hot-path Debug calls pre-check `slog.Enabled` so the fmt.Sprintf is skipped entirely when filtered out — important for the per-keystroke trace.",
      "The shared TextEditor now supports Ctrl+C (copy selection), Ctrl+Y (delete line), and Ctrl+X (cut) — used across log viewers, plugin TOML editor, pane notes, and the migration dialog.",
    ],
  },

  {
    slug: "remote-daemon-ssh",
    icon: "key-round",
    title: "Remote daemon over SSH",
    badge: "beta",
    blurb:
      "`quil --remote gpu01` puts the panes on another machine. They keep running when the laptop sleeps — the TUI is only a viewer.",
    category: "persistence",
    detail: [
      "No network port is opened on the remote host. Quil runs `ssh -T <host> \"quil --stdio\"` and speaks its normal protocol over that one channel — so a bastion behind ProxyJump, a Tailscale or WireGuard address, and a box on the public internet all work with no extra setup.",
      "The server does not need Quil installed first. Point `--remote` at a bare machine and it offers to install one, then attaches. Your laptop downloads the release for the *remote's* platform, verifies the checksum locally, and pushes it over the SSH connection you already have — so a cluster node with no route to GitHub is provisioned just as easily as one with. Linux and macOS, amd64 and arm64; nothing is installed without an explicit yes.",
      "The destination goes to `ssh` verbatim, so your ~/.ssh/config keeps working unchanged: Host aliases, ProxyJump, ControlMaster, per-host keys, hardware tokens, and SSH certificates all apply.",
      "Quil forces off what the remote never needs — agent forwarding, X11, port forwarding, local-command execution — and bounds both ends of the connection's life so a dead host fails in seconds rather than hanging. Host-key policy is left to your config, because forcing it could only weaken it.",
      "Every command that manages a daemon's lifecycle refuses under --remote rather than silently acting on your laptop, and the status bar carries [remote <host>] so the machine you are driving is never ambiguous.",
      "A dropped link is a pause, not an ending. Close the lid, lose wifi, switch networks — an amber bar names the host, counts the attempts, and shows what ssh actually said, while retries back off from half a second to at most thirty and never stop. When the host answers again the panes come back with their contents intact, because nothing ever stopped running.",
      "Keystrokes are dropped while the link is down rather than queued: a key typed at a dead connection would otherwise arrive minutes later in a live agent session, answering a question that had already moved on. ctrl+q stays live throughout, for a host that is not coming back.",
      "Dialogs that browse a filesystem read the server's disk, not yours. The working-directory picker, `~`, relative paths, drive and root listings, and git-repository discovery all ask the daemon — before that, `Alt+G` could report no repository in a directory where the agent in that very pane answered `git status` with the branch name. Names and paths coming back from a host you may not control are stripped of terminal escapes and of the invisible characters that reverse text direction, so a folder name cannot scramble the dialog or read as something other than what it is.",
      "Kube contexts, plugin availability and the recent-directories list follow the same rule: they describe the server. The recent list is kept per host, and whether a remembered directory still exists is asked of the daemon — checking it locally dropped every server path, so the list rendered empty, which is indistinguishable from a feature nobody had used.",
      "Full-screen tools run on the remote the same as locally. `k9s`, `lazysql` and `lazygit` open against the server's clusters, databases and repositories — an SSH connection carries no terminal type, and without one those tools exit within milliseconds, which reads as a pane that crashed rather than a missing variable. Panes that keep no scrollback, such as OpenCode, are asked to repaint when you reattach instead of showing an empty rectangle in front of a process that never stopped running.",
      "Beta limits: plugin definitions still come from your local machine, the daemon checks which tools are installed only at startup and on plugin reload (so a tool installed on the server mid-session stays greyed until then), and the remote must be Linux or macOS — a running Windows executable cannot be replaced in place, which makes the upgrade half of provisioning impossible there.",
    ],
  },

  {
    slug: "multi-daemon",
    image: "https://cdn.stukans.com/quil/screenshots/projects_2_with_remote-800.webp",
    icon: "key-round",
    title: "Several machines in one window",
    badge: "beta",
    blurb:
      "A project belongs to the daemon that holds its files — so projects from your laptop and from a build host sit side by side in the same sidebar, in one TUI process.",
    category: "persistence",
    detail: [
      "`quil --remote <host>` binds a whole TUI to one daemon. This does not: the client holds connections to the local daemon and any number of remote ones at once, and their projects appear as siblings in the same sidebar. Working against a laptop and a GPU box is no longer two terminals.",
      "Add a host without relaunching. Tick Remote (ssh) in the New Project dialog, give a user and host, and press Enter on the Host row — Quil dials it and then browses *that* machine's filesystem for the root directory. A root directory lives on exactly one machine, so connecting before submitting is the order that makes sense.",
      "A host you can't attach to is provisioned from that dialog rather than from a shell: no Quil there installs it, and a daemon older than your client is upgraded — which stops that daemon and respawns its panes from the saved workspace, so the status line says so while it runs. Each is attempted once per host per session. The one case it won't fix is a remote daemon *newer* than your client: pushing your own build would downgrade a machine other people may share, so it names the client upgrade instead.",
      "The dialog's one message line is coloured by what it means, not by the fact that something was reported: red only when the host cannot be reached at all, amber while a dial or an install is under way, green once the link is up. Provisioning a machine used to arrive in the same red as a failure, which read as an error that then somehow succeeded.",
      "One host holds one project. A daemon must have at least one tab and a tab must belong to a project, so a host always arrives already holding one — naming a project there renames that one rather than adding a second, and whatever was already running on the machine ends up under the name you chose. The local daemon is unaffected: it holds as many projects as you like.",
      "A host that already has several folds them into the one you name. Press Enter and the form states what it will do — how many projects the host has, what the result is called, how many tabs move, and that nothing is closed — and a second Enter does it; editing the name in between re-describes instead of acting on what you moved away from. Every tab moves onto the surviving project and the emptied records are dropped, so no pane or running command is touched. That is the way to tidy a host connected before this rule existed, where reconnecting and re-creating the project that seemed to vanish left another row behind each time.",
      "The fold leaves the surviving project's root directory alone. The dialog fills that field in by itself as soon as the directory listing arrives, so it usually holds wherever the daemon happens to start rather than anywhere you chose — writing that over a root you had picked would be a change nobody asked for. Rename moves one, and it opens with the project's own root already in the field.",
      "The host is remembered under `[[destinations]]` and attached at every launch. Disconnect it from the sidebar's right-click menu and it stops nothing on the far end — the remote daemon keeps every pane alive, and reconnecting restores the same workspace. That is worth knowing before you read a missing row as a deletion: the projects come back on the next connect.",
      "Each destination carries its own reconnect state: its own backoff ladder, its own amber banner naming the host and what ssh actually said. One daemon dying no longer ends the session — the others stay live and interactive.",
      "Beta limits worth knowing before you rely on it: a destination unreachable at *launch* never starts a reconnect ladder (no connection means no reader, so nothing reports a loss — relaunch to pick it up), background destinations dial non-interactively so a first-time host key must be accepted once with `ssh <dest>` or `quil remote setup <dest>` first, plugin availability is a single registry fed by whichever daemon answered last, and the recent-directories list is one per client rather than one per host.",
    ],
  },

  // --- Developer experience -------------------------------------------
  {
    slug: "build-variants",
    icon: "key-round",
    title: "Three build variants",
    blurb:
      "Production, dev, and debug binaries — each self-contained with the right log level and data directory baked in at compile time.",
    category: "interaction",
    detail: [
      "`quil.exe` / `quild.exe` — production build, stripped symbols, normal log level, data in ~/.quil/.",
      "`quil-dev.exe` / `quild-dev.exe` — auto dev mode (data in .quil/ next to the binary), debug logging, finds its matching `quild-dev` daemon. Just double-click — no --dev flag or env vars needed.",
      "`quil-debug.exe` / `quild-debug.exe` — debug logging against the production data directory. Useful for diagnosing issues in the live workspace.",
      "Each variant auto-starts its matching daemon. `./scripts/dev.sh build` produces all 6 binaries in one Docker run.",
    ],
  },

  // --- Cross-platform ------------------------------------------------
  {
    slug: "cross-platform",
    icon: "monitor",
    title: "Cross-platform from day one",
    blurb:
      "Native Linux, macOS, and Windows support. No WSL required.",
    category: "interaction",
    detail: [
      "PTY via creack/pty on Unix, ConPTY on Windows.",
      "On Windows 10, Quil bundles Microsoft's OpenConsole (MIT) and hosts panes through it. The Windows 10 inbox console re-serializes an app's incremental screen updates incorrectly — typing `Hello` in claude-code showed `H ello`. Windows 11's built-in host is unaffected and is left alone; if the bundle is missing, Quil falls back to the inbox host rather than failing to start.",
      "IPC via Unix domain sockets on Linux/macOS, Named Pipes on Windows.",
      "Pre-built binaries for linux/amd64, linux/arm64, darwin/amd64, darwin/arm64, windows/amd64.",
    ],
  },
];

/** Home page feature grid — 6 most important features, shown as cards.
 *  Currently unused: index.astro picks four hand-written topFeatures
 *  rather than reading from this list. Kept for the legacy /docs page
 *  and any future grid view that wants the canonical home selection. */
export const homeFeatures: Feature["slug"][] = [
  "reboot-proof-sessions",
  "ai-session-resume",
  "typed-panes",
  "clipboard-image-paste",
  "plugin-system",
  "cross-platform",
];

export const featureCategories: Record<Feature["category"], string> = {
  persistence: "Persistence",
  interaction: "Interaction",
  ai: "AI integration",
  extensibility: "Extensibility",
  observability: "Observability",
};
