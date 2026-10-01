// The home-page tour's story, in one place. The tour (src/tour/scenario.js,
// in the browser) and the #use-cases text (server-rendered) both read this
// file, so the card and the text cannot disagree.
//
// Copy rule (spec §C.3): every key, command and count here matches the docs
// in this repository. The `read` links are checked against the repository on
// every build (scripts/check-dist.mjs, rule 6).
import { GITHUB_BLOB as GH } from "./seo";

export type UseCaseId = "agents" | "orchestrator" | "remote" | "sandbox" | "too-much" | "remember";

export interface TourChapter {
  id: string;
  /** Stop-bar label. */
  short: string;
  /** Upper-case label above the title. */
  persona: string;
  title: string;
  /** The tour card's paragraph. */
  line: string;
  /** [key or command, what it does] */
  keys: [string, string][];
}

export interface UseCase extends TourChapter {
  id: UseCaseId;
  /** Paragraphs for #use-cases only; backticks mark code. */
  body: string[];
  read: { label: string; href: string }[];
}

export const tourIntro: TourChapter = {
  id: "one-terminal",
  short: "start",
  persona: "ONE TERMINAL",
  title: "It starts with one terminal.",
  line: "AI coding went parallel. The terminal still expects one person typing one command.",
  keys: [["quil", "start it"]],
};

export const tourOutro: TourChapter = {
  id: "this",
  short: "end",
  persona: "ONE WORKSPACE",
  title: "One terminal became this.",
  line: "",
  keys: [],
};

export const useCases: UseCase[] = [
  {
    id: "agents",
    short: "agents",
    persona: "AGENTS, SIDE BY SIDE",
    title: "Run Claude Code, Codex and OpenCode at once.",
    line: "Each agent gets its own pane, folder and conversation. Give one its own git worktree and they never touch each other's files.",
    body: [
      "Split a pane with `Alt+Shift+H` (side by side) or `Alt+Shift+V` (stacked) and type `claude`, `codex` or `opencode` in the new shell. Quil turns the pane into that agent's pane, so it tracks the session and can resume it later.",
      "For an agent that needs its own checkout, open the pane with `Ctrl+N` and pick Worktree → `+ new branch…`. The agent works on its own branch in its own folder, and the other agents never see its edits.",
    ],
    keys: [["claude", "typed in a shell"], ["Ctrl+N", "new pane"], ["Alt+Shift+V", "split"]],
    read: [
      { label: "Hand-started agents", href: `${GH}docs/features.md#hand-started-agents` },
      { label: "Spawn a pane in a worktree", href: `${GH}docs/features.md#spawn-a-pane-in-a-worktree` },
      { label: "AI session resume", href: `${GH}docs/features.md#ai-session-resume` },
    ],
  },
  {
    id: "orchestrator",
    short: "orchestrator",
    persona: "AN AGENT THAT RUNS THE OTHERS",
    title: "Let one agent hand out the work.",
    line: "Through quil mcp an agent opens panes, starts other agents, gives them tasks and reads what they did.",
    body: [
      "Add Quil to your AI client as an MCP server; the command is `quil mcp`. The agent can then list panes, read their output, open new panes and agents, and hand out work with `delegate_task`.",
      "Open a whole team at once from the command palette (`Alt+Shift+P` → New from template). When a delegated task ends, Quil types a `[quil task …]` line into the orchestrator's prompt, so it knows without polling.",
    ],
    keys: [["Alt+Shift+P", "New from template"], ["delegate_task", "one of 36 MCP tools"]],
    read: [
      { label: "Wiring Quil into your AI client", href: `${GH}docs/mcp.md#wiring-quil-into-your-ai-client` },
      { label: "Delegating work to another pane", href: `${GH}docs/mcp.md#delegating-work-to-another-pane` },
      { label: "The 36 tools", href: `${GH}docs/mcp.md#the-36-tools` },
      { label: "Shipped templates and limits", href: `${GH}docs/workspace-templates.md#shipped-templates-and-limits` },
    ],
  },
  {
    id: "remote",
    short: "remote",
    persona: "THE WORK RUNS ELSEWHERE",
    title: "Close the laptop. The agents keep going.",
    line: "Put the work on a server. Quil connects over ssh, opens no port, and reconnects by itself.",
    body: [
      "`quil --remote <host>` runs the Quil daemon on the server and only the screen on your laptop. It travels over ssh, so no port is opened. Close the lid and the panes keep working; when the link comes back, Quil reconnects on its own.",
      "Or keep your local projects and add the server as one more project (`Alt+Shift+N` → Remote (ssh)). Quil installs itself on the host when needed, and every folder picker reads the server's disk, not yours.",
    ],
    keys: [["Alt+Shift+N", "Remote (ssh)"], ["quil --remote gpu01", "whole session"]],
    read: [
      { label: "Remote daemon over SSH", href: `${GH}docs/features.md#remote-daemon-over-ssh` },
      { label: "When the link drops", href: `${GH}docs/features.md#when-the-link-drops` },
      { label: "Projects on another machine", href: `${GH}docs/features.md#projects-on-another-machine` },
      { label: "Windows remotes over SSH", href: `${GH}docs/remote-windows.md` },
    ],
  },
  {
    id: "sandbox",
    short: "sandbox",
    persona: "AN AGENT IN A BOX",
    title: "Skip permissions. Inside a box.",
    line: "Run an agent with no confirmations inside Docker. It sees the checkout. Your keys and other projects are not there.",
    body: [
      "Tick `Run in a Docker container` in an agent's setup dialog. The pane runs in its own container that mounts the checkout, with the repository's history, hooks and config read-only, no `--privileged` and no added capabilities.",
      "Quil publishes no image: you build it once on your machine with `scripts/sandbox-image.sh`. Closing the pane removes the container; the commits it made stay in your repository.",
    ],
    keys: [["Run in a Docker container", "setup dialog"], ["scripts/sandbox-image.sh", "build the image"]],
    read: [
      { label: "Using it", href: `${GH}docs/sandbox-panes.md#using-it` },
      { label: "Building the image", href: `${GH}docs/sandbox-panes.md#building-the-image` },
      { label: "What the sandbox does and does not bound", href: `${GH}docs/sandbox-panes.md#what-the-sandbox-does-and-does-not-bound` },
    ],
  },
  {
    id: "too-much",
    short: "too much",
    persona: "TOO MUCH GOING ON",
    title: "See who needs you. Find anything.",
    line: "One key jumps to the agent that has waited longest. One search reads the scrollback of every pane.",
    body: [
      "The sidebar lists every project and what each agent is doing: ▲ waiting on you, a spinner while it works, ✓ when it finished while you were elsewhere. `Alt+Shift+A` jumps to the agent that has waited longest.",
      "`Alt+Shift+P` opens the command palette. It finds actions, tabs and projects, and it also searches the scrollback of every loaded pane; Enter goes to the match. `Alt+N` opens the notification center.",
    ],
    keys: [["Alt+Shift+A", "waiting longest"], ["Alt+Shift+P", "search everything"]],
    read: [
      { label: "Projects", href: `${GH}docs/features.md#projects` },
      { label: "Command palette", href: `${GH}docs/features.md#command-palette` },
      { label: "Notification center", href: `${GH}docs/features.md#notification-center` },
      { label: "Desktop notifications", href: `${GH}docs/features.md#desktop-notifications` },
    ],
  },
  {
    id: "remember",
    short: "reboot",
    persona: "THE WORKSPACE REMEMBERS",
    title: "Reboot. Nothing lost.",
    line: "Tabs, splits, folders, history, notes and agent conversations come back. The panes on gpu01 never stopped.",
    body: [
      "Quil keeps the workspace on disk under `~/.quil` while you work. After a restart, run `quil`: tabs, splits, working folders, pane notes and the last screen of every pane come back, and each agent resumes its own conversation (`claude --resume`, `codex resume`, `opencode --session`).",
      "Panes on a remote host never stopped at all; Quil attaches to them again. Leave yourself a note on any pane with `Alt+E`; it survives the restart too.",
    ],
    keys: [["quil", "after any restart"], ["Alt+E", "pane notes"]],
    read: [
      { label: "Reboot-proof sessions", href: `${GH}docs/features.md#reboot-proof-sessions` },
      { label: "AI session resume", href: `${GH}docs/features.md#ai-session-resume` },
      { label: "Pane notes", href: `${GH}docs/features.md#pane-notes` },
      { label: "How auto-resume works (blog)", href: "/blog/resume-claude-code-session-after-reboot/" },
    ],
  },
];

/** Scroll length of each chapter in viewport heights: intro, the six stops, outro. */
export const tourWeights = [1.1, 1.7, 1.9, 1.7, 1.6, 1.8, 2.0, 1.0];

/** Chapter ids in tour order; TourStage renders one scroll mark (#tour-<id>) per id. */
export const tourChapterIds = [tourIntro.id, ...useCases.map((u) => u.id), tourOutro.id];
