// Shared comparison matrix for /vs/* pages.
//
// Single source of truth: edit this file once and every comparison
// page updates. Each row is a feature with per-product support level
// (yes / no / partial) and an optional note explaining nuance.

export type Support = "yes" | "no" | "partial";

export interface CompareRow {
  feature: string;
  quil: Support;
  tmux: Support;
  zellij: Support;
  wezterm: Support;
  screen: Support;
  /** Optional footnote rendered below the row in the matrix. */
  note?: string;
}

export const competitorMatrix: CompareRow[] = [
  {
    feature: "Session persistence while the multiplexer server is running",
    quil: "yes",
    tmux: "yes",
    zellij: "yes",
    wezterm: "yes",
    screen: "yes",
  },
  {
    feature: "Survives a full host reboot",
    quil: "yes",
    tmux: "no",
    zellij: "partial",
    wezterm: "no",
    screen: "no",
    note: "Quil's defining capability. Zellij is the one honest partial: session serialization is on by default, so after a reboot it brings back the layout, the tabs and the working directories, and offers each pane's command behind a “Press ENTER to run” prompt. What it does not bring back is the work — no running processes, no scrollback unless you turned that on, and no idea an AI session ever existed. tmux, WezTerm and Screen lose the session outright; tmux-resurrect gets tmux to roughly where Zellij already is.",
  },
  {
    feature: "AI session auto-resume (Claude Code, OpenCode, Codex)",
    quil: "yes",
    tmux: "no",
    zellij: "no",
    wezterm: "no",
    screen: "no",
  },
  {
    feature: "Remote attach: local client, sessions on another host",
    quil: "yes",
    tmux: "no",
    zellij: "no",
    wezterm: "yes",
    screen: "no",
    note: "tmux, Zellij and Screen run their client ON the remote — you ssh in first, and a dropped link means re-attaching by hand. WezTerm's mux domains and Quil's `quil --remote <host>` both keep the client local and reconnect by themselves. Quil opens no port on the server (it is one `ssh -T` channel) and installs itself on a bare host.",
  },
  {
    feature: "Agent state per group, visible at a glance",
    quil: "yes",
    tmux: "no",
    zellij: "no",
    wezterm: "no",
    screen: "no",
    note: "Not \"does it have a sidebar\" — the claim is that the multiplexer knows an agent is mid-turn, versus waiting on a permission prompt, versus finished. That needs a feed from the agent itself; Quil reads Claude Code and OpenCode hook events. Every project row carries a roll-up of its agents and keeps updating while you are looking at a different project, which is the point: the one that got stuck is the one you are not watching.",
  },
  {
    feature: "Several hosts' sessions side by side in one client",
    quil: "yes",
    tmux: "no",
    zellij: "no",
    wezterm: "yes",
    screen: "no",
    note: "WezTerm's mux domains genuinely do this — you can attach several and mix their tabs in one window. tmux, Zellij and Screen run the client on the remote, so a second host means a second terminal. Quil dials each host over its own `ssh -T` channel, gives each its own reconnect state, and groups their projects in the same sidebar; one daemon dying leaves the rest interactive.",
  },
  {
    feature: "Typed panes (Terminal / AI / SSH / Webhook)",
    quil: "yes",
    tmux: "no",
    zellij: "no",
    wezterm: "no",
    screen: "no",
  },
  {
    feature: "Plugin system",
    quil: "yes",
    tmux: "partial",
    zellij: "yes",
    wezterm: "yes",
    screen: "no",
    note: "Quil uses declarative TOML. Zellij uses WASM. WezTerm uses Lua. tmux uses shell scripts.",
  },
  {
    feature: "Mouse support",
    quil: "yes",
    tmux: "yes",
    zellij: "yes",
    wezterm: "yes",
    screen: "no",
  },
  {
    feature: "Ghost buffers (last 500 lines instant on reconnect)",
    quil: "yes",
    tmux: "no",
    zellij: "no",
    wezterm: "partial",
    screen: "no",
  },
  {
    feature: "Command palette (fuzzy action + pane launcher)",
    quil: "yes",
    tmux: "partial",
    zellij: "partial",
    wezterm: "yes",
    screen: "no",
    note: "WezTerm's is built in (Ctrl+Shift+P, frecency-ranked). tmux has a command prompt but it is not fuzzy. Zellij's comes from community plugins (cmd-pal), not the core.",
  },
  {
    feature: "Search every pane's scrollback from one prompt",
    quil: "yes",
    tmux: "no",
    zellij: "no",
    wezterm: "no",
    screen: "no",
    note: "Every other tool searches one pane at a time in copy mode — including WezTerm, whose palette is command-only. Across panes, tmux needs the third-party tmux-grep plugin. In Quil it is the same box as the command palette: type, and matching panes appear under `Found in panes` with a preview and a match count.",
  },
  {
    feature: "MCP server for AI agents",
    quil: "yes",
    tmux: "no",
    zellij: "no",
    wezterm: "no",
    screen: "no",
  },
  {
    feature: "Notification center + idle analysis",
    quil: "yes",
    tmux: "no",
    zellij: "no",
    wezterm: "no",
    screen: "no",
  },
  {
    feature: "Pane notes editor (Alt+E)",
    quil: "yes",
    tmux: "no",
    zellij: "no",
    wezterm: "no",
    screen: "no",
  },
  {
    feature: "Windows native (no WSL)",
    quil: "yes",
    tmux: "no",
    zellij: "no",
    wezterm: "yes",
    screen: "no",
  },
  {
    feature: "Declarative keybindings (config file)",
    quil: "yes",
    tmux: "yes",
    zellij: "yes",
    wezterm: "yes",
    screen: "yes",
  },
];

/**
 * A row in a competitor-specific matrix (used for the AI-agent-orchestrator
 * comparisons herdr / aoe, whose feature axis differs from the classic
 * multiplexers). `them` is the competitor's support level.
 */
export interface VsRow {
  feature: string;
  quil: Support;
  them: Support;
  note?: string;
}

export interface CompetitorInfo {
  slug: "tmux" | "zellij" | "wezterm" | "screen" | "herdr" | "aoe" | "ghostty";
  name: string;
  description: string;
  positioning: string;
  keyStrength: string;
  keyGap: string;
  migrationNote: string;
  /**
   * Optional competitor-specific matrix. When present it replaces the shared
   * `competitorMatrix` on that page — used for herdr/aoe, which are direct
   * agent-orchestrator rivals rather than classic multiplexers.
   */
  matrix?: VsRow[];
  /** Optional override for the "where Quil takes over" headline. */
  quilHeadline?: string;
  /**
   * Optional <title> override, used verbatim (ComparePage suppresses the
   * brand suffix, so "Quil vs tmux · Quil" can't happen).
   *
   * The default stem is "Quil vs <name>", which targets a phrase almost
   * nobody types — the query with the demand is "<name> alternative".
   * Set this where a competitor has a real alternative-seeking audience;
   * leave it unset for herdr/aoe, whose names are searched directly.
   */
  seoTitle?: string;
  /**
   * Optional override for the meta description. The shared template's
   * default opens "Looking for a <name> alternative?", which is right for
   * every rival and wrong for Ghostty — an emulator Quil runs INSIDE. A
   * page that claims otherwise ranks us for a query we should not win and
   * tells a visitor something untrue in the search result itself.
   */
  seoDescription?: string;
  /** Per-page FAQ — 3-4 Q&A pairs. */
  faq: { question: string; answer: string }[];
}

export const competitors: Record<CompetitorInfo["slug"], CompetitorInfo> = {
  // Ghostty is the one entry here that is NOT a rival: it is a terminal
  // emulator, the window Quil is drawn into. It earns a /vs/ page anyway
  // because it is the most-searched terminal in the category, because its
  // author now runs a multiplexer company (Superlogical, announced
  // 2026-07-30), and because "ghostty tmux" / "does ghostty keep my
  // session" is real demand this page can answer honestly.
  //
  // The page must never imply Ghostty is a competitor. That is also why
  // this is the only entry that sets `seoDescription` — the shared
  // template's default opens "Looking for a <name> alternative?", which
  // would be a lie here and would rank us for a query we should not win.
  ghostty: {
    slug: "ghostty",
    name: "Ghostty",
    seoTitle: "Quil vs Ghostty — the terminal, and what runs inside it",
    seoDescription:
      "Ghostty is a terminal emulator. Quil is a multiplexer that runs inside one. They stack rather than compete — see which job each does, and what Ghostty alone cannot keep when the window closes.",
    description:
      "A fast, feature-rich terminal emulator written in Zig by Mitchell Hashimoto, with platform-native UI and GPU-accelerated rendering. macOS and Linux, MIT-licensed, 1.3.1 current. It draws the window — it does not keep the work.",
    positioning:
      "This is the one comparison on this site whose honest answer is “use both”. Ghostty draws the window: GPU-accelerated glyphs, native tabs and splits, scrollback search, and the deepest shell integration of any emulator. Quil runs inside that window and keeps the work: a daemon owns the panes, snapshots them to disk, and hands them back after a reboot with the AI sessions still attached. Quit Ghostty and its splits are gone. Quit Ghostty with Quil inside it and nothing stopped — reopen, type quil, and the panes are still running. Different layers of the same stack.",
    keyStrength:
      "The best window you can put Quil in. GPU-accelerated rendering with a Metal backend on macOS and OpenGL on Linux, a SIMD-optimised terminal parser, Unicode 17 grapheme clustering, the Kitty graphics protocol, in-window scrollback search, native scrollbars, a large built-in theme catalogue, and prompt-aware shell integration that lets you jump between prompts, select a command's whole output, and click inside a prompt to move the cursor.",
    keyGap:
      "Ghostty is an emulator, so nothing it draws outlives it. Quit the app and every split, every running command and every AI conversation goes with it — its window restore is macOS-only and brings back windows and tabs, not processes. It has no server, so there is no detach, no reattach, and no way to reach a session on another machine from a local client. It has no notion of an AI agent, so it cannot tell you which one is mid-turn and which one is parked on a permission prompt. And there is no official Windows build at all.",
    migrationNote:
      "There is nothing to migrate — Quil is not a replacement for Ghostty and never wanted to be. Install Quil, keep Ghostty as your terminal, and run `quil` inside it. Ghostty keeps doing fonts, colours and GPU compositing; Quil takes over panes, persistence and AI sessions. If you already use Ghostty's own splits, the adjustment is that Quil's splits are the ones that survive — so make Quil's the outer layer and let Ghostty hold a single full-size surface.",
    quilHeadline: "The window closes. The work doesn't.",
    matrix: [
      { feature: "Draws the terminal itself (GPU, fonts, ligatures)", quil: "no", them: "yes", note: "Deliberate on both sides. Quil is a TUI with no renderer of its own — it runs inside Ghostty, WezTerm, Windows Terminal, iTerm2 or a bare Linux console. Ghostty is where that work belongs, and it is very good at it." },
      { feature: "Work survives quitting the terminal app", quil: "yes", them: "no", note: "Quil's panes are owned by a background daemon, not by the window. Ghostty's splits are windows; closing the app ends the processes in them." },
      { feature: "Survives a full host reboot", quil: "yes", them: "no", note: "Ghostty's `window-save-state` is macOS-only and restores windows and tabs, not the programs that were running in them. Quil snapshots the whole workspace continuously, so a reboot returns the projects, tabs, split layout, working directories and scrollback, and resumes the Claude Code, OpenCode or Codex conversation by session id. A plain terminal pane comes back as a fresh shell in its own directory — an arbitrary foreground command is not resumed, and no multiplexer resumes one." },
      { feature: "AI session auto-resume (Claude Code, OpenCode, Codex)", quil: "yes", them: "no" },
      { feature: "Agent state at a glance (working / blocked / done)", quil: "yes", them: "no", note: "Quil reads hook events from the agent itself, so it knows the difference between mid-turn and parked on a permission prompt. An emulator has no feed for that." },
      { feature: "Typed panes (Terminal / AI / SSH / tools)", quil: "yes", them: "no" },
      { feature: "MCP server an AI assistant can drive", quil: "yes", them: "no", note: "Ghostty is scriptable on macOS through AppleScript and Apple Shortcuts, which is a genuinely nice control surface — but it is not a protocol Claude Desktop, Cursor or VS Code speak, and it is macOS-only." },
      { feature: "Remote attach: client local, sessions on another host", quil: "yes", them: "no", note: "Ghostty wraps `ssh` to set up the remote environment correctly, which is useful and not the same thing. It has no server, so there is nothing to attach to." },
      { feature: "Several hosts' sessions side by side in one client", quil: "yes", them: "no" },
      { feature: "Tabs and splits", quil: "yes", them: "yes", note: "Ghostty 1.3 added split drag-and-drop, zoom preservation across navigation, editable tab titles and per-surface working-directory inheritance. Both tools do this well; only one of them keeps the result." },
      { feature: "Command palette", quil: "yes", them: "yes", note: "Ghostty's is `cmd+shift+p` / `ctrl+shift+p` and reaches every keybind action, bound or not. Quil's `Alt+Shift+P` also fuzzy-finds panes, tabs and projects." },
      { feature: "Search scrollback", quil: "yes", them: "partial", note: "Ghostty added in-window search in 1.3 and runs it on its own thread — but it searches the focused surface. Quil's search runs daemon-wide and returns matches from every pane at once, with a preview and a match count per pane." },
      { feature: "Prompt-aware navigation (jump to prompt, select a command's output)", quil: "no", them: "yes", note: "An honest gap, and one we intend to close. Quil already injects the OSC 133 marks that make this possible and currently reads only the command-finished one. Ghostty turns the same marks into prompt jumping, output selection and click-to-move-cursor." },
      { feature: "Native Windows (no WSL)", quil: "yes", them: "no", note: "Ghostty ships macOS and Linux builds only. Native Windows exists solely as community forks and a separate derivative, Noctty. Quil ships a native Windows binary on ConPTY with bundled OpenConsole for Windows 10." },
      { feature: "Themes with light/dark auto-switch", quil: "no", them: "yes", note: "Ghostty carries a large theme catalogue and can pair a light and a dark one. Quil ships no presets on purpose — it asks the terminal for its real foreground and background over OSC 10/11 and renders against those, so it inherits Ghostty's theme instead of fighting it." },
      { feature: "Desktop notification when a long command finishes", quil: "partial", them: "yes", note: "Ghostty's fires only past a configurable duration, so a fast command stays silent. Quil raises Windows toasts on agent attention states and routes a click back to the pane, but has no duration floor and no macOS or Linux transport yet." },
      { feature: "Per-pane Docker sandbox for an AI agent", quil: "yes", them: "no" },
      { feature: "Git worktree per tab", quil: "yes", them: "no" },
    ],
    faq: [
      {
        question: "Is Quil a Ghostty alternative?",
        answer:
          "No, and we would rather you did not treat it as one. Ghostty is a terminal emulator — it owns the window, the font rendering and the GPU. Quil is a multiplexer and workflow orchestrator that runs inside a terminal emulator. The nearest true comparison is Quil vs tmux or Quil vs Zellij. Against Ghostty the right question is not which to pick, it is which layer you were missing.",
      },
      {
        question: "Does Quil run inside Ghostty?",
        answer:
          "Yes, and it is a good pairing. Quil needs a terminal with PTY support and sensible colour handling, which describes Ghostty exactly. Ghostty also implements the Kitty keyboard protocol, which is what lets Quil distinguish key combinations that older terminals collapse. Run Ghostty as a single full-size surface and let Quil own the splits, so the splits are the ones that survive a reboot.",
      },
      {
        question: "Ghostty already has tabs and splits. Do I still need Quil?",
        answer:
          "If you only need tabs and splits for the length of one sitting, no — Ghostty's are excellent and cost you nothing extra. You need Quil when the sitting ends: when you close the laptop, reboot for an update, or lose the SSH link, and want the same panes, the same scrollback and the same Claude Code conversation back. Ghostty's splits are windows and end with the app. Quil's panes are owned by a daemon and do not.",
      },
      {
        question: "Ghostty's author is building a multiplexer. Should I wait for it?",
        answer:
          "Mitchell Hashimoto announced Superlogical on 30 July 2026, and its first product is a server-side terminal multiplexer built on libghostty, with web, macOS and iOS clients and live session sharing. We think it will be very good. As of September 2026 nothing has shipped, no timeline is public, and the announcement deliberately withholds features and pricing. Quil is available now, is Apache-2.0, runs natively on Windows, and needs no account and no server you do not own.",
      },
      {
        question: "Does Ghostty run on Windows?",
        answer:
          "Not officially. Ghostty ships macOS and Linux builds; native Windows support exists only as community forks and a separate derivative called Noctty. If you are on Windows, pair Quil with Windows Terminal or WezTerm — Quil itself is native there, using ConPTY with a bundled OpenConsole on Windows 10, and needs no WSL.",
      },
    ],
  },

  tmux: {
    slug: "tmux",
    name: "tmux",
    seoTitle: "Quil vs tmux — a tmux alternative that survives reboots",
    description:
      "The de-facto Unix terminal multiplexer. Server-side sessions, scriptable plugins, steep learning curve, shipped by default on most distros alongside screen.",
    positioning:
      "tmux is great for what it does — a stable, battle-tested server multiplexer built in 2007 — but it was never designed to survive a host reboot, understand AI coding sessions, or treat different panes as different types of work. Quil is for the problem tmux doesn't solve.",
    keyStrength:
      "Ubiquity, stability, and the largest plugin ecosystem of any multiplexer. If you need a standard tool on a standard Unix host, tmux is still the answer.",
    keyGap:
      "Zero persistence across host reboots. You can bolt on tmux-resurrect or tmux-continuum, but even those only restore layout and working directories — not AI session state, not running processes, not scrollback.",
    migrationNote:
      "Coming from tmux? Quil uses familiar keybindings (Ctrl+T new tab, Alt+H / Alt+V to split). Everything's remappable in ~/.quil/config.toml so you can reuse your tmux muscle memory verbatim.",
    faq: [
      {
        question: "Can I run Quil and tmux side by side?",
        answer:
          "Yes. They don't share sockets or state, so you can experiment with Quil without touching your tmux setup. Some users run Quil for AI-heavy projects and tmux for traditional admin sessions.",
      },
      {
        question: "Does Quil read my tmux sessions?",
        answer:
          "Not currently — Quil maintains its own workspace state under ~/.quil/. A tmux import helper is on the future roadmap.",
      },
      {
        question: "Is Quil a tmux replacement?",
        answer:
          "It depends on what you use tmux for. If you need reboot persistence + AI session continuity, yes. If you need a headless multiplexer for classical server administration, tmux remains the right tool.",
      },
      {
        question: "Is Quil a good tmux alternative for AI coding agents?",
        answer:
          "For AI-heavy work, yes. tmux keeps sessions alive while its server runs, but it loses everything on a host reboot and has no concept of an AI session. Quil survives a full reboot, auto-resumes Claude Code and OpenCode sessions with the current (post-rotation) session id, and exposes an MCP server so an AI assistant can read and drive the panes directly. For classic headless server administration, tmux is still the better fit.",
      },
      {
        question: "What is the best tmux alternative that survives a reboot?",
        answer:
          "tmux, WezTerm and GNU Screen lose the session when the host reboots — their persistence only lasts while the server process is alive. Zellij is the exception, and a partial one: it serializes the session by default, so a reboot returns the layout, the tabs and the directories, with each command waiting behind a “Press ENTER to run” prompt. Nothing is still running, and an AI session comes back as a command line to re-type. Quil was built for that second half: its daemon snapshots the whole workspace to disk continuously, so after a reboot you type one command and the layout, working directories, scrollback, and AI sessions come back in under 30 seconds.",
      },
    ],
  },

  zellij: {
    slug: "zellij",
    name: "Zellij",
    seoTitle: "Quil vs Zellij — what a reboot does to each",
    description:
      "Modern Rust terminal multiplexer with a friendly UX, WASM plugins, and sane defaults. Released in 2021.",
    positioning:
      "Zellij is the closest competitor on UX — both tools prioritise gentle defaults and a modern feel. Where they diverge: Zellij is a multiplexer first, Quil is a workflow orchestrator first. Zellij is also the only classic multiplexer that gives you anything back after a reboot, so the honest question is not whether a session returns but what returns with it — Zellij rebuilds the layout and waits for you to re-run each command, while Quil brings back the layout, the directories and the scrollback and resumes the AI conversation attached to them by session id.",
    keyStrength:
      "Modern UX, clean WASM plugin model, excellent defaults, discoverable status bar. Zellij users rarely need to read a manual.",
    keyGap:
      "Zellij serializes a session to its cache folder by default, so a reboot gives you the shape back — tabs, panes, directories, and each pane's command waiting behind a “Press ENTER to run” prompt. It does not give you the work back: nothing is still running, scrollback is off unless you enabled it, and an AI session is just a command line to re-type. Quil brings back the layout, the directories and the scrollback, and resumes the Claude Code, OpenCode or Codex conversation with its current session id. A plain shell pane returns as a fresh shell in its own directory — no multiplexer resumes an arbitrary foreground command.",
    migrationNote:
      "Zellij users will feel at home in Quil — both tools use Alt-based keys and avoid prefix chords by default. The main adjustment is Quil's typed panes (Terminal / AI / SSH / etc.), which Zellij doesn't have.",
    faq: [
      {
        question: "Is Quil just Zellij with AI support?",
        answer:
          "It's more than that. Quil's core bet is that a workflow orchestrator is a different tool from a multiplexer. Typed panes, ghost buffers, the MCP server, and the pane notes editor all follow from that bet. Zellij has chosen a different scope.",
      },
      {
        question: "Can Quil use Zellij plugins?",
        answer:
          "No. Zellij plugins are WASM modules using Zellij's host API. Quil plugins are TOML declarations. The two models are intentionally different — Quil's is simpler but less dynamic, Zellij's is more powerful but harder to write.",
      },
      {
        question: "Both tools are in Rust, right?",
        answer:
          "Zellij is Rust. Quil is Go — specifically Go 1.25, with Bubble Tea v2 for the TUI. Different ecosystems, same aesthetic goals.",
      },
    ],
  },

  wezterm: {
    slug: "wezterm",
    name: "WezTerm",
    seoTitle: "Quil vs WezTerm — what survives when the terminal closes",
    description:
      "A GPU-accelerated cross-platform terminal emulator with built-in multiplexer features, written in Rust, extensible via Lua. Released by Wez Furlong.",
    positioning:
      "WezTerm is a terminal emulator that happens to have multiplexer features. Quil is a workflow orchestrator that happens to render terminals. The category is different: WezTerm cares about glyph rendering, ligatures, and GPU compositing. Quil cares about what happens when you reboot.",
    keyStrength:
      "Best-in-class rendering, GPU-accelerated, ligature support, Lua extensibility, built-in SSH multiplexer. WezTerm is one of the most polished terminal emulators available.",
    keyGap:
      "No reboot persistence, no AI session awareness, no typed panes. WezTerm's multiplexer is an escape hatch for when you need tabs without a separate multiplexer — it's not the core of the product.",
    migrationNote:
      "Keep WezTerm as your terminal emulator and run Quil inside it. The two are complementary: WezTerm handles rendering (fonts, ligatures, GPU acceleration), Quil handles workflow (persistence, AI sessions, typed panes).",
    faq: [
      {
        question: "Can I use WezTerm and Quil together?",
        answer:
          "Yes, and we recommend it. WezTerm renders Quil beautifully, and Quil's persistence layer complements WezTerm's lack of reboot survival.",
      },
      {
        question: "Does Quil have its own terminal emulator?",
        answer:
          "No. Quil is a TUI that runs inside whatever terminal emulator you already use — WezTerm, Alacritty, Kitty, Windows Terminal, Ghostty, iTerm2, the Linux console — anything with PTY support works.",
      },
      {
        question: "Why not just use WezTerm's multiplexer?",
        answer:
          "WezTerm's multiplexer is fine for tabs and splits but doesn't survive a reboot or understand pane types. If those features don't matter to you, WezTerm alone is plenty.",
      },
    ],
  },

  screen: {
    slug: "screen",
    name: "GNU Screen",
    seoTitle: "Quil vs GNU Screen — persistence in 2026",
    description:
      "The original Unix terminal multiplexer, first released in 1987. Still shipped by default on most Unix distributions.",
    positioning:
      "Screen is what every serious Unix admin learned first. It's stable, tiny, and still works on systems where tmux isn't installed. But it's a product of its era: no mouse support, no modern plugin model, no AI awareness, no reboot persistence, and a config syntax from another century.",
    keyStrength:
      "Ships by default on virtually every Unix host. Minimal dependencies. Works on systems where you can't install anything else. A legitimate fallback when you SSH into a hardened server.",
    keyGap:
      "1987-era UX. No mouse support by default. Config syntax that nobody enjoys writing. Zero AI integration. No reboot persistence. No typed panes.",
    migrationNote:
      "If you're still on screen for everyday work, any modern multiplexer is a straight upgrade. Quil's sweet spot is if you want the modern UX and also the reboot-proof persistence that even tmux doesn't give you.",
    faq: [
      {
        question: "Is Quil smaller than screen?",
        answer:
          "No. Quil ships as two binaries (quil + quild) totalling around 40 MB. Screen is a single ~1 MB binary. If binary size is your constraint, screen wins.",
      },
      {
        question: "Can Quil replace screen on a headless server?",
        answer:
          "Technically yes, but Quil is designed for interactive developer workflows. For pure detach-and-reattach on a headless host, screen and tmux are better targeted.",
      },
      {
        question: "Is screen actively maintained?",
        answer:
          "Yes, but slowly. Version 5.0 shipped in 2024 — the first major release since 2014.",
      },
      {
        question: "What is a modern alternative to GNU Screen?",
        answer:
          "Any current multiplexer — tmux, Zellij, or Quil — is a straight upgrade from Screen's 1987-era UX, mouse-less defaults, and dated config syntax. Quil goes further than the others by surviving a full host reboot and auto-resuming AI coding sessions, which even tmux doesn't do. If you only need detach-and-reattach on a headless server, tmux is the closest like-for-like replacement.",
      },
      {
        question: "Does Quil run on Windows, unlike GNU Screen?",
        answer:
          "Yes. GNU Screen is Unix-only. Quil ships a native Windows binary using ConPTY — no WSL required — alongside its Linux and macOS builds, so it's a cross-platform alternative to Screen for developers who also work on Windows.",
      },
    ],
  },

  herdr: {
    slug: "herdr",
    name: "herdr",
    description:
      "A Rust terminal multiplexer purpose-built for AI coding agents — 'the runtime your coding agents live on.' Single binary, its own PTY and VT engine, a socket API agents can drive, and a language-agnostic plugin system. Quil's closest philosophical twin.",
    positioning:
      "herdr and Quil made almost the same bet: build your own multiplexer, keep it a single lightweight binary, and make it agent-aware. herdr is further ahead on agent breadth (24+ agents detected, one-command native integrations for 18 of them) and on scriptable plugins; Quil is further ahead on speaking MCP, the protocol AI assistants already understand, and on the Windows edges herdr still excludes. The honest read: herdr is the stronger Unix-first agent fleet manager today, Quil is the stronger MCP-native one.",
    keyStrength:
      "Agent breadth and extensibility. Detects 24+ agents from bundled TOML screen manifests that update themselves from herdr.dev, takes an authoritative state feed from installed lifecycle hooks where an agent offers one, installs those integrations with one command, exposes a full socket API + CLI with a subscribable event stream that agents and scripts can drive, and runs language-agnostic plugins with actions, event hooks and link handlers from a marketplace.",
    keyGap:
      "There is no first-party MCP server — herdr's own pitch is that agents talk to each other through it without one, so they drive it through a bespoke socket API and an installed skill instead of a protocol assistants already speak; the community has since wrapped that socket in third-party MCP bridges, which is a real answer but not one herdr ships or supports. On Windows — generally available now, no longer beta — it still has no live server handoff, no direct terminal attach and no clipboard image bridge in local native panes. It has no pane-notes editor and no per-pane memory reporting.",
    migrationNote:
      "herdr uses a tmux-style prefix (Ctrl+B) where Quil uses direct Alt-based keys. Both keep agents alive on detach, restore AI sessions, and hold several machines in one window. Pick Quil if your assistant should drive the multiplexer over MCP with no glue, or if you work on Windows 10 and want bundled OpenConsole and clipboard image paste. Pick herdr if you drive many different agents, want a shell CLI and an event stream to script it from, or want plugins you can write in any language.",
    quilHeadline: "MCP-native. Windows 10 handled properly. Notes + memory built in.",
    matrix: [
      { feature: "Own multiplexer + PTY (not a tmux wrapper)", quil: "yes", them: "yes" },
      { feature: "Survives a full host reboot", quil: "yes", them: "yes" },
      { feature: "AI session auto-resume", quil: "yes", them: "yes", note: "herdr restores native sessions for most of the agents it integrates with; Quil for Claude Code, OpenCode and Codex." },
      { feature: "Native Windows (no WSL)", quil: "yes", them: "partial", note: "herdr calls native Windows generally available now — it is no longer a beta, and what is left is a list of named exclusions rather than a maturity gap: no direct terminal attach, no live server handoff, and no clipboard image bridge in local native panes. Quil's remaining edge is that last one — a Win32 DIB→PNG clipboard image-paste proxy — plus bundled OpenConsole so Windows 10's inbox conhost cannot mangle an agent's rendering. Neither tool lets a Windows machine act as a remote host others attach to." },
      { feature: "MCP server for AI agents", quil: "yes", them: "no", note: "herdr ships no first-party MCP server — it exposes a bespoke newline-delimited JSON socket API plus a CLI and an installed agent skill instead. Third-party bridges that wrap that socket in MCP now exist, so an MCP client can reach herdr; they are community projects, versioned and supported separately from herdr itself." },
      { feature: "Pane notes editor", quil: "yes", them: "no" },
      { feature: "Per-pane memory reporting", quil: "yes", them: "no" },
      { feature: "Screen-content agent detection (no hooks)", quil: "partial", them: "yes", note: "Quil pattern-matches idle only. herdr evaluates bundled TOML manifests against the live screen, the pane title and OSC progress sequences to classify idle / working / blocked, refreshes those manifests from herdr.dev without a new binary, and lets you override one locally — but adding a genuinely new agent still needs a herdr release, because process detection is compiled in." },
      { feature: "Breadth of agents detected", quil: "partial", them: "yes", note: "Quil: 3 deep (Claude Code, OpenCode, Codex) + tools. herdr: 24+ detected, 18 with a one-command integration installer." },
      { feature: "Subscribable event stream for scripts", quil: "partial", them: "yes", note: "herdr's socket API has events.subscribe — a long-lived push stream of workspace, tab, pane, layout and worktree lifecycle events. Quil's equivalent is narrower: watch_notifications blocks for sidebar-worthy events, and task delegation reports its own completion, but there is no general subscription to pane or layout changes." },
      { feature: "One-command agent integration installer", quil: "no", them: "yes" },
      { feature: "Git worktree-per-session", quil: "yes", them: "yes", note: "Quil opens a tab straight onto a new worktree — the pane is a placeholder with a spinner while git worktree add runs, so a slow monorepo checkout can never be mistaken for an agent started in the wrong tree — and closing that tab or pane offers to remove the worktree, naming what it holds and refusing to call a dirty one clean." },
      { feature: "Executable plugins (actions / event hooks / link handlers)", quil: "partial", them: "yes", note: "Quil plugins are declarative TOML pane types; herdr runs any-language plugins." },
      { feature: "Plugin marketplace", quil: "no", them: "yes" },
      { feature: "General CLI to script the multiplexer", quil: "no", them: "yes", note: "Quil scripts via MCP (for AI); herdr adds a shell CLI for humans." },
      { feature: "Remote SSH thin-client attach", quil: "yes", them: "yes", note: "`quil --remote <host>` since v1.44; opens no port, reconnects on its own after a dropped link, and installs itself on a bare server." },
      { feature: "Several hosts in one window at once", quil: "yes", them: "yes", note: "Both do this now, and the shape is nearly identical: each machine keeps its own server, one client holds them all, and losing one host does not disconnect the others. Quil tags each project with the machine that owns it and gives each host its own reconnect state; herdr saves an SSH machine once and switches between it and Local in the sidebar, streaming pane screens only for the selected machine while the rest keep reporting workspace info, agent state and notifications." },
      { feature: "Named sessions / live server handoff", quil: "no", them: "yes" },
      { feature: "Sound + desktop notifications", quil: "partial", them: "yes", note: "Quil raises real Windows toasts when an agent parks on a prompt or finishes a turn, and clicking one routes you to the pane that sent it. Still missing: sound, and macOS/Linux — herdr does all three, and can hand the notification to the outer terminal as well as the OS." },
      { feature: "Themes with light/dark auto-switch", quil: "partial", them: "yes", note: "Quil ships no theme presets — it asks the terminal for its real foreground and background (OSC 10/11) and renders against those, so it follows your terminal instead of theming itself. herdr carries named themes and switches between a light and a dark one when the terminal reports the change." },
    ],
    faq: [
      {
        question: "Is herdr basically Quil in Rust?",
        answer:
          "Architecturally they're remarkably close — both build their own multiplexer and PTY layer rather than wrapping tmux, both keep agents alive on detach, both restore AI sessions, and both now hold several machines in one client with per-host reconnect. The divergence is emphasis: herdr optimizes for agent breadth and for being scriptable from a shell; Quil optimizes for being an MCP server that AI assistants drive directly, with no bespoke API to teach them.",
      },
      {
        question: "Can herdr run on Windows without WSL?",
        answer:
          "Yes — herdr now calls native Windows generally available, so this is no longer the clear split it once was. What is still unsupported there is specific: no direct terminal attach, no live server handoff, and no clipboard image bridge in local native panes. Quil's edge is narrower than it used to be but concrete: bundled OpenConsole so Windows 10's inbox conhost cannot mangle an agent's output, and a Win32 clipboard image-paste proxy that gets screenshots into a Claude Code pane. To be straight about it, neither tool can use a Windows machine as a remote host you attach to from elsewhere.",
      },
      {
        question: "How do agents control each tool?",
        answer:
          "herdr exposes a Unix-socket API plus a CLI, and ships an installable 'skill' so an agent learns to call it. Quil exposes 35 tools for panes, projects, remote hosts and task delegation over the Model Context Protocol, which Claude Desktop, Cursor, and VS Code speak natively with no glue.",
      },
    ],
  },

  aoe: {
    slug: "aoe",
    name: "Agent of Empires",
    description:
      "A Rust TUI + React web dashboard that manages AI coding agents on top of tmux, with git worktrees, Docker sandboxing, a mobile-first 'structured view', and remote phone access. Backed by the Mozilla.ai community.",
    positioning:
      "Agent of Empires and Quil solve the same problem from opposite ends. AoE wraps tmux and invests everything above it — a browser dashboard, an Agent-Client-Protocol structured view, container sandboxing, and phone access over a tunnel. Quil builds its own multiplexer and invests in the terminal itself — native Windows, an MCP server, pane notes, and a remote mode that puts the panes on a server while the TUI stays on your laptop. On a phone or in a browser AoE is still far ahead; from a terminal, Quil now reaches a remote host without any of that stack.",
    keyStrength:
      "Reach and isolation. A real browser dashboard (installable PWA) with a native structured view of agent state, remote phone access via Tailscale/Cloudflare with QR + passphrase pairing and Web Push, git worktree-per-session and multi-repo workspaces, an in-TUI diff viewer, and Docker/Podman/Apple-Container sandboxing with shared auth volumes.",
    keyGap:
      "It depends on tmux, so native Windows is out (WSL2 only). It has no MCP server (external control is a bespoke HTTP API), no pane-notes editor, and no per-pane memory reporting — and its large surface area is a heavier install than Quil's two binaries.",
    migrationNote:
      "AoE keeps every agent in a tmux session, so you can `tmux attach` to any of them directly, and its dashboard runs in the browser. Quil is a native TUI with its own daemon — no tmux, Docker, or Node required — and runs natively on Windows. If you want the browser/mobile surface today, AoE leads; if you want a lightweight native terminal and an MCP server, Quil is the closer fit.",
    quilHeadline: "Native Windows. MCP-native. No tmux dependency.",
    matrix: [
      { feature: "Runs natively on Windows (no WSL / no tmux)", quil: "yes", them: "no", note: "AoE requires tmux, so Windows is WSL2-only. Quil is native on Windows." },
      { feature: "Survives a full host reboot", quil: "yes", them: "yes", note: "AoE via tmux; Quil via its own snapshot + ghost buffers." },
      { feature: "AI session auto-resume", quil: "yes", them: "yes" },
      { feature: "MCP server for AI agents", quil: "yes", them: "no", note: "AoE exposes a bespoke HTTP REST API; Quil speaks MCP natively." },
      { feature: "Pane notes editor", quil: "yes", them: "no" },
      { feature: "Per-pane memory reporting", quil: "yes", them: "no" },
      { feature: "Remote attach from a native terminal client", quil: "yes", them: "no", note: "`quil --remote <host>` runs the TUI locally against a daemon on the server, opens no port, reconnects on its own, and installs itself on a bare machine. AoE's remote surface is the browser dashboard — for a terminal you ssh in and attach tmux by hand." },
      { feature: "Web dashboard (browser terminal + diffs, PWA)", quil: "no", them: "yes" },
      { feature: "Remote phone access (tunnel + QR/passphrase + Web Push)", quil: "no", them: "yes" },
      { feature: "ACP structured view (plan / tool / approve cards)", quil: "no", them: "yes" },
      { feature: "Git worktree-per-session", quil: "yes", them: "yes", note: "Was a genuine gap until recently. A Quil tab can now open straight onto a new worktree, showing a placeholder pane with a spinner while git worktree add runs so a slow checkout is never mistaken for an agent started in the main tree, and closing the tab or pane offers to remove the worktree — naming what it holds, and refusing to call a dirty one clean." },
      { feature: "Multi-repo workspaces", quil: "yes", them: "yes", note: "Was a genuine gap until v1.47. Quil projects each own a root directory and their own tabs, so several repositories sit side by side in one window — and a project can belong to a different machine, which AoE's workspaces do not span." },
      { feature: "Built-in diff viewer (review + edit)", quil: "no", them: "yes" },
      { feature: "Container sandboxing (Docker/Podman/Apple)", quil: "partial", them: "yes", note: "Quil sandboxes an AI pane in Docker — one container per pane, an image you build yourself, and the mount set as the whole boundary. Podman and Apple Containers are untested. AoE also offers shared auth volumes; Quil's sandbox panes are per-pane by default, because one shared agent config directory merges every sandbox pane into one trust domain." },
      { feature: "Screen-content agent detection (no hooks)", quil: "partial", them: "yes" },
      { feature: "Breadth of agents supported", quil: "partial", them: "yes", note: "Quil: 3 deep (Claude Code, OpenCode, Codex) + tools. AoE: ~13 terminal + 7 ACP." },
      { feature: "Session fork / import from disk", quil: "no", them: "yes" },
      { feature: "Sound + push notifications", quil: "partial", them: "yes", note: "Quil raises real Windows toasts when an agent parks on a prompt or finishes a turn, and clicking one routes you to the pane that sent it. Still missing: sound, macOS/Linux, and anything that reaches a phone — AoE's Web Push does, which is the point of its browser surface." },
      { feature: "Session lifecycle mgmt (auto-stop idle, groups, archive)", quil: "no", them: "yes" },
    ],
    faq: [
      {
        question: "Does Agent of Empires work on Windows?",
        answer:
          "Only through WSL2 — it depends on tmux and POSIX process handling. Quil runs natively on Windows with bundled ConPTY/OpenConsole, so if you're a Windows developer it's the more direct fit.",
      },
      {
        question: "AoE has a web dashboard — does Quil?",
        answer:
          "Not today. AoE's browser dashboard and remote phone access are genuinely ahead here, and both are on Quil's roadmap. Quil's current bet is the native terminal experience plus an MCP server that lets an AI assistant drive the multiplexer directly.",
      },
      {
        question: "Which is lighter to run?",
        answer:
          "Quil ships two Go binaries and needs no tmux, no Node, and no Docker unless you opt a pane into a sandbox container. AoE is a larger stack (tmux + a React app + optional Node ACP workers and containers) in exchange for its web features.",
      },
      {
        question: "Can I sandbox agents in containers with Quil?",
        answer:
          "Yes, in Docker. Tick 'Run in a Docker container' in the pane setup dialog and a Claude Code, Codex or OpenCode pane runs inside its own container, with its checkout bind-mounted in so its edits and commits are real. The mount set is the whole boundary — no --privileged, no --cap-add, no --network flag — and new git objects go to a per-pane store with your repository's own mounted read-only, so no commit on any branch can be deleted from inside. You supply the image; Quil publishes none and pulls none, and scripts/sandbox-image.sh builds and verifies one locally. AoE still leads here: it also covers Podman and Apple Containers, which Quil has not tested, and offers shared auth volumes where Quil's panes authenticate per pane by default.",
      },
    ],
  },
};

/**
 * Display order for the compare navigation. The header dropdown and the
 * footer column both render from this, so adding a /vs/ page is one edit
 * and it appears in both — the header used to carry its own hardcoded
 * copy, which is how herdr and Agent of Empires came to be missing from
 * the dropdown for months while the footer listed all six.
 *
 * Typed as a Record over the slug union rather than a plain array on
 * purpose: a competitor added to `competitors` without a rank here is a
 * build error, not a page that quietly never appears in the nav. That is
 * the exact failure this export exists to end, so it must not be
 * reintroducible by omission.
 */
const compareNavOrder: Record<CompetitorInfo["slug"], number> = {
  herdr: 1,
  aoe: 2,
  tmux: 3,
  zellij: 4,
  wezterm: 5,
  ghostty: 6,
  screen: 7,
};

/** Ordered `{ href, label }` pairs for every /vs/ page. Hrefs carry the
 *  trailing slash — see scripts/check-trailing-slash.mjs for why. */
export const compareNav: { href: string; label: string }[] = (
  Object.keys(competitors) as CompetitorInfo["slug"][]
)
  .sort((a, b) => compareNavOrder[a] - compareNavOrder[b])
  .map((slug) => ({
    href: `/vs/${slug}/`,
    label: `vs ${competitors[slug].name}`,
  }));
