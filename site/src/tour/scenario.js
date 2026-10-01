// @ts-nocheck — ported from the B2 mock (kit/scenario6.js); plain browser JavaScript on purpose.
/* ==========================================================================
   scenario6.js — the short story: six use cases, pos 0 … 8.

     0 intro     · one terminal
     1 agents    · Claude Code, Codex and OpenCode side by side, worktrees
     2 orchestr. · an agent that hands out the work (quil mcp)
     3 remote    · close the laptop, gpu01 keeps going
     4 sandbox   · skip permissions, inside Docker
     5 too much  · see who needs you, find anything
     6 remember  · reboot, nothing lost
     7 outro     · what one terminal became

   Every Quil string here comes from internal/tui and internal/daemon. The
   repository in most panes is Quil itself (real paths). The other projects
   and all command output are written for the story.
   ========================================================================== */
import K from "./kit.js";
import { SITE } from "../data/seo";
import { tourIntro, useCases, tourOutro } from "../data/usecases";
  const esc = K.esc;

  // Chapter copy lives in src/data/usecases.ts, shared with the server-rendered #use-cases text.
  const chapters = [tourIntro, ...useCases, tourOutro].map((c, n) => ({ n, id: c.id, persona: c.persona, short: c.short, title: c.title, line: c.line, keys: c.keys }));

  /* -------------------------------------------------------------- helpers */
  const sh = (dir) => '<span class="c-path">' + esc(dir) + '</span> <span class="c-prompt">$</span> ';

  function script(kind, opts) {
    opts = opts || {};
    const lines = [], foot = [], states = [];
    let lastIdle = null;
    const b = {
      lines, foot, states,
      L(at, html, cls) { lines.push({ at, html, cls }); return b; },
      clear(at) { lines.push({ clear: at }); return b; },
      state(at, s) { states.push([at, s]); return b; },
      cmd(at, dur, text, dir) {
        if (lastIdle) { lastIdle.until = at; lastIdle = null; }
        lines.push({ at, dur, type: "in", prompt: sh(dir || opts.dir), text, done: at + dur + 0.004 });
        return b;
      },
      idle(at, dir) { lastIdle = { at, html: sh(dir || opts.dir) + '<span class="q-caret"></span>' }; lines.push(lastIdle); return b; },
      out(at, html) { lines.push({ at, html }); return b; },
      welcome(at, cwd) {
        if (kind === "claude-code") lines.push({ at, html: '<div class="bx warn"><span class="c-cc">✻</span> Welcome to <b class="c-white">Claude Code</b>!\n\n  <span class="c-dim">cwd: ' + esc(cwd) + "</span></div>" });
        else if (kind === "codex") lines.push({ at, html: '<div class="bx">&gt;_ <b class="c-white">OpenAI Codex</b>\n\n<span class="c-dim">directory: ' + esc(cwd) + "</span></div>" });
        else lines.push({ at, html: '<span class="c-oc b">opencode</span>  <span class="c-dim">' + esc(cwd) + "</span>" });
        foot.push([at, footFor(kind, opts, null)]);
        return b;
      },
      ask(at, dur, text) {
        foot.push([at, footFor(kind, opts, { typing: { at, dur, text } })]);
        const sub = at + dur + 0.005;
        lines.push({ at: sub, html: youHTML(kind, text) });
        foot.push([sub, footFor(kind, opts, { status: opts.verb || "Working" })]);
        states.push([sub, "working"]);
        return b;
      },
      paste(at, html, verb) { lines.push({ at, html }); foot.push([at, footFor(kind, opts, { status: verb || "Working" })]); states.push([at, "working"]); return b; },
      work(at, verb) { foot.push([at, footFor(kind, opts, { status: verb })]); states.push([at, "working"]); return b; },
      say(at, html) { lines.push({ at, html: sayHTML(kind, html) }); return b; },
      tool(at, name, arg, res) { lines.push({ at, html: toolHTML(kind, name, arg) }); if (res != null) lines.push({ at: at + 0.006, html: resHTML(kind, res) }); return b; },
      res(at, html) { lines.push({ at, html: resHTML(kind, html) }); return b; },
      finish(at, summary, st) { foot.push([at, footFor(kind, opts, { doneMsg: summary })]); states.push([at, st || "done"]); return b; },
      block(at) { states.push([at, "blocked"]); foot.push([at, footFor(kind, opts, {})]); return b; },
    };
    return b;
  }
  function youHTML(kind, text) {
    if (kind === "claude-code") return '<span class="c-gray">&gt; ' + esc(text) + "</span>";
    if (kind === "codex") return '<span class="c-gray">› ' + esc(text) + "</span>";
    return '<span class="c-oc">┃</span> <span class="c-white">' + esc(text) + "</span>";
  }
  function sayHTML(kind, html) {
    if (kind === "claude-code") return '<span class="c-white">⏺</span> ' + html;
    if (kind === "codex") return '<span class="c-dim">•</span> ' + html;
    return html;
  }
  function toolHTML(kind, name, arg) {
    if (kind === "claude-code") return '<span class="c-green">⏺</span> <b class="c-white">' + name + "</b>" + (arg != null ? "(" + arg + ")" : "");
    if (kind === "codex") return '<span class="c-dim">•</span> <b class="c-white">' + name + "</b> " + (arg || "");
    return '<span class="c-dim">→</span> <span class="c-oc">' + name + "</span> " + (arg || "");
  }
  function resHTML(kind, html) {
    if (kind === "claude-code") return '  <span class="c-dim">⎿</span>  ' + html;
    if (kind === "codex") return '  <span class="c-dim">└</span> ' + html;
    return '  <span class="c-dim">' + html + "</span>";
  }
  function footFor(kind, opts, st) {
    st = st || {};
    return function (pos) {
      let typed = "";
      if (st.typing) typed = st.typing.text.slice(0, Math.round(K.seg(pos, st.typing.at, st.typing.at + st.typing.dur) * st.typing.text.length));
      const c = '<span class="q-caret"></span>';
      if (kind === "claude-code") {
        let status = "";
        if (st.status) status = '<span class="c-cc">' + K.spin() + " " + esc(st.status) + '…</span> <span class="c-dim">(esc to interrupt)</span>';
        else if (st.doneMsg) status = '<span class="c-dim">✻ ' + esc(st.doneMsg) + "</span>";
        const mode = opts.bypass ? '  <span class="c-ccd">⏵⏵ bypass permissions on</span> <span class="c-dim">(shift+tab to cycle)</span>' : '  <span class="c-dim">? for shortcuts</span>';
        return '<div class="q-ln">' + (status || " ") + '</div><div class="hr"></div><div class="q-ln"><span class="c-white">&gt;</span> ' + esc(typed) + c + '</div><div class="hr"></div><div class="q-ln">' + mode + "</div>";
      }
      if (kind === "codex") {
        let status = " ";
        if (st.status) status = '<span class="c-cx">' + K.spin() + '</span> <span class="c-white">' + esc(st.status) + '</span> <span class="c-dim">(esc to interrupt)</span>';
        else if (st.doneMsg) status = '<span class="c-dim">' + esc(st.doneMsg) + "</span>";
        const input = typed ? '<span class="c-white">' + esc(typed) + "</span>" + c : c + '<span class="c-dim">Ask Codex to do anything</span>';
        return '<div class="q-ln">' + status + '</div><div class="q-ln"><span class="c-white">›</span> ' + input + '</div><div class="q-ln"><span class="c-dim">  ? for shortcuts</span></div>';
      }
      let status = " ";
      if (st.status) status = '<span class="c-oc">' + K.spin() + '</span> <span class="c-dim">' + esc(st.status) + "…</span>";
      else if (st.doneMsg) status = '<span class="c-dim">' + esc(st.doneMsg) + "</span>";
      return '<div class="q-ln">' + status + '</div><div class="bx"><span class="c-oc">┃</span> ' + esc(typed) + c + '</div><div class="q-ln"><span class="c-oc">build</span> <span class="c-dim">· enter send</span></div>';
    };
  }
  function rng(seed) { let s = seed >>> 0; return () => ((s = (s * 1664525 + 1013904223) >>> 0) / 4294967296); }

  /* ============================================================ WORKSPACE */
  const projects = [
    { id: "quil", name: "quil", root: "~/work/quil", at: -1 },
    { id: "train", name: "train", host: "build@gpu01", root: "~/work/train", at: 3.105,
      link: [[3.6, "retry"], [3.70, null], [6.52, "retry"], [6.64, null]] },
    { id: "store", name: "storefront", root: "~/work/storefront", at: 4.10 },
  ];
  const tabs = [
    { id: "q/1", project: "quil", name: "agents", at: -1, layout: ["h", "q1", ["v", "q2", "q3"]] },
    { id: "q/2", project: "quil", name: "team", at: 2.105, layout: ["h", "o1", ["v", "o2", "o3", "o4"]] },
    { id: "t/1", project: "train", name: "bench", at: 3.105, layout: ["h", "t1", ["v", "t2", "t3"]] },
    { id: "s/1", project: "store", name: "deps", at: 4.10, layout: ["h", "d1", "d2"] },
  ];
  const panes = {};
  const WTQ = "~/work/quil-worktrees/";

  /* -------------------------------------------- 1 · agents, side by side */
  {
    const b = script("claude-code", { dir: "quil", bypass: false, verb: "Tracing the backoff" });
    b.idle(-1);
    b.cmd(0.55, 0.07, "git status");
    b.out(0.63, "On branch master");
    b.out(0.632, "Your branch is up to date with 'origin/master'.");
    b.out(0.634, "");
    b.out(0.636, "nothing to commit, working tree clean");
    b.idle(0.64);
    b.cmd(1.03, 0.05, "claude");
    b.clear(1.10);
    b.welcome(1.10, "~/work/quil");
    b.ask(1.12, 0.08, "fix the flaky reconnect test in internal/transport");
    b.say(1.25, "I'll start with the reconnect loop and its test.");
    b.tool(1.30, "Read", "internal/transport/reconnect.go", "Read 212 lines");
    b.tool(1.42, "Read", "internal/transport/reconnect_test.go", "Read 348 lines");
    b.say(1.58, "The test waits on a real 500 ms timer. Under load the first");
    b.L(1.582, "  retry lands after the assertion. I'll inject the clock.");
    b.tool(1.72, "Update", "internal/transport/reconnect.go", "Updated with 9 additions and 3 removals");
    b.L(1.732, '      <span class="bg-del">- time.Sleep(backoff)</span>');
    b.L(1.734, '      <span class="bg-add">+ r.clock.Sleep(backoff)</span>');
    b.L(4.48, '<div class="bx warn"><b class="c-white">Bash command</b>\n\n  <span class="c-white">go test -count=50 ./internal/transport/</span>\n  <span class="c-dim">Run the transport tests 50 times</span>\n\nDo you want to proceed?\n<span class="c-cc">❯ 1. Yes</span>\n  2. Yes, and don\'t ask again for <b>go test</b> commands\n  3. No, and tell Claude what to do differently <span class="c-dim">(esc)</span></div>');
    b.block(4.50);
    b.work(5.205, "Running the tests");
    b.tool(5.22, "Bash", "go test -count=50 ./internal/transport/", '<span class="c-green">ok</span>  github.com/artyomsv/quil/internal/transport');
    b.say(5.62, "Fixed: 50 runs, no flakes. The clock is injectable now.");
    b.finish(5.64, "Cogitated for 9m 12s");
    panes.q1 = { tab: "q/1", at: -1, type: [[-1, "terminal"], [1.10, "claude-code"]], name: [[-1, "terminal"], [1.10, "claude-code"]],
      cwd: "~/work/quil", git: "master", lines: b.lines, foot: [[-1, ""]].concat(b.foot), states: b.states,
      blockedTool: [[0, "Bash"]], model: [[1.10, "opus-5 · 38k ctx"]] };
  }
  {
    const cwd = WTQ + "feat-palette-tests";
    const b = script("codex", { verb: "Working" });
    b.welcome(1.405, cwd);
    b.ask(1.42, 0.07, "write table tests for the palette's fuzzy scorer");
    b.tool(1.55, "Explored", "");
    b.res(1.552, "Read internal/tui/palette.go, internal/tui/palette_test.go");
    b.tool(1.70, "Edited", "internal/tui/palette_test.go (+64 -2)");
    b.tool(1.86, "Ran", "go test ./internal/tui -run Palette");
    b.res(1.87, '<span class="c-green">ok</span>  github.com/artyomsv/quil/internal/tui');
    b.L(4.68, '<div class="bx">Would you like to run the following command?\n\n  <span class="c-white">$ go test -race ./internal/tui/</span>\n\n<span class="c-white">› 1. Yes, proceed</span>\n  2. Yes, and don\'t ask again for this command\n  3. No, and tell Codex what to do differently</div>');
    b.block(4.70);
    b.L(5.315, '<span class="c-gray">✔ You approved codex to run go test -race ./internal/tui/</span>');
    b.work(5.32, "Working");
    b.tool(5.40, "Ran", "go test -race ./internal/tui/");
    b.res(5.41, '<span class="c-green">ok</span>  github.com/artyomsv/quil/internal/tui');
    b.say(5.64, "Added 14 cases: prefixes, gaps, case, ties. Race-clean.");
    b.finish(5.66, "Worked for 21m 40s");
    panes.q2 = { tab: "q/1", at: 1.40, type: "codex", name: "codex", cwd, git: "feat/palette-tests wt",
      lines: b.lines, foot: b.foot, states: b.states, blockedTool: [[0, "Bash"]] };
  }
  {
    const b = script("opencode", { dir: "quil", verb: "Reading" });
    b.idle(1.55);
    b.cmd(1.57, 0.035, "opencode");
    b.clear(1.615);
    b.welcome(1.615, "~/work/quil");
    b.ask(1.63, 0.07, "how does lazy restore pick which panes spawn first?");
    b.tool(1.74, "Read", "internal/daemon/daemon.go");
    b.tool(1.78, "Read", "internal/daemon/lazy_restore_test.go");
    b.say(1.86, '<span class="c-white">Only the active tab spawns at start. The others load</span>');
    b.L(1.862, '<span class="c-white">their scrollback and wait until the tab is opened,</span>');
    b.L(1.864, '<span class="c-white">unless a pane is marked eager (Alt+Shift+E).</span>');
    b.finish(1.93, "idle");
    b.state(5.0, "idle");
    panes.q3 = { tab: "q/1", at: 1.55, type: [[1.55, "terminal"], [1.615, "opencode"]], name: [[1.55, "terminal"], [1.615, "opencode"]],
      cwd: "~/work/quil", git: "master", lines: b.lines, foot: [[1.55, ""]].concat(b.foot), states: b.states };
  }

  /* -------------------------------------------- 2 · an agent that runs the others */
  const ids = { orch: "pane-2b7d90e1", analyst: "pane-4c1e7a02", dev: "pane-9a0b3f55", impl: "pane-77d2c410" };
  {
    const b = script("claude-code", { bypass: true, verb: "Planning" });
    b.welcome(2.11, "~/work/quil");
    b.paste(2.125, '<span class="c-gray">&gt; [Pasted text #1 +38 lines]</span>', "Planning");
    b.say(2.14, "Team: analyst " + ids.analyst + " · developer " + ids.dev + ". The analyst");
    b.L(2.142, "  finds where quil status prints first.");
    b.L(2.16, '<span class="c-green">⏺</span> <b class="c-white">quil - delegate_task</b> (MCP)(pane_id: "' + ids.analyst + '", prompt: "Find where');
    b.L(2.162, '  quil status prints. Write .team/01-status.md")');
    b.res(2.17, '{"id":"task-51c0a9d3","state":"sent","to_pane_name":"analyst"}');
    b.finish(2.20, "Waiting on the analyst");
    b.state(2.20, "idle");
    b.L(2.30, '<span class="c-gray">&gt; [quil task task-51c0a9d3] pane ' + ids.analyst + " (analyst) done. Last output: ⏺ Wrote</span>");
    b.L(2.302, '<span class="c-gray">  .team/01-status.md Call get_task with task_id=task-51c0a9d3 for the full result, or</span>');
    b.L(2.304, '<span class="c-gray">  read_pane_output on ' + ids.analyst + ".</span>");
    b.work(2.305, "Reading the plan");
    b.tool(2.33, "Read", ".team/01-status.md", "Read 41 lines");
    b.L(2.36, '<span class="c-green">⏺</span> <b class="c-white">quil - create_pane</b> (MCP)(type: "claude-code", name: "status-json",');
    b.L(2.362, '  worktree_branch: "feat/status-json", toggles: ["dangerously_skip_permissions"])');
    b.res(2.37, '{"pane_id":"' + ids.impl + '","preparing_worktree":true}');
    b.L(2.45, '<span class="c-green">⏺</span> <b class="c-white">quil - delegate_task</b> (MCP)(pane_id: "' + ids.impl + '", prompt: "Add --json');
    b.L(2.452, '  to quil status, per .team/01-status.md")');
    b.res(2.46, '{"id":"task-3f9a1c2e","state":"sent"}');
    b.L(2.50, '<span class="c-green">⏺</span> <b class="c-white">quil - delegate_task</b> (MCP)(pane_id: "' + ids.dev + '", prompt: "Write tests');
    b.L(2.502, '  for quil status --json. Report to .team/02-tests.md")');
    b.res(2.51, '{"id":"task-8e44b716","state":"sent"}');
    b.say(2.55, "Two tasks out. I'll check in when they report back.");
    b.finish(2.56, "Waiting on 2 tasks");
    b.state(2.56, "idle");
    b.work(2.61, "Checking progress");
    b.L(2.62, '<span class="c-green">⏺</span> <b class="c-white">quil - read_pane_output</b> (MCP)(pane_id: "' + ids.impl + '", last_lines: 40)');
    b.res(2.63, "⏺ Update(cmd/quil/daemonctl.go) … on track, nothing to change.");
    b.finish(2.66, "Waiting on 2 tasks");
    b.state(2.66, "idle");
    b.L(2.84, '<span class="c-gray">&gt; [quil task task-3f9a1c2e] pane ' + ids.impl + " (status-json) done. Last output: ok</span>");
    b.L(2.842, '<span class="c-gray">  github.com/artyomsv/quil/cmd/quil Call get_task with task_id=task-3f9a1c2e for the full</span>');
    b.L(2.844, '<span class="c-gray">  result, or read_pane_output on ' + ids.impl + ".</span>");
    b.work(2.845, "Routing the review");
    b.say(2.86, "status-json is done. Sending it to the analyst for review.");
    b.L(2.88, '<span class="c-green">⏺</span> <b class="c-white">quil - delegate_task</b> (MCP)(pane_id: "' + ids.analyst + '", prompt: "Review');
    b.L(2.882, '  feat/status-json. Rank findings must-fix / should-fix / consider.")');
    b.res(2.89, '{"id":"task-c02d6e19","state":"sent"}');
    b.finish(2.92, "Waiting on the review");
    b.state(2.92, "idle");
    panes.o1 = { tab: "q/2", at: 2.105, type: "claude-code", name: "orchestrator", cwd: "~/work/quil", git: "master", w: 1.35,
      lines: b.lines, foot: b.foot, states: b.states, model: [[2.11, "opus-5 · 24k ctx"]] };
  }
  {
    const b = script("claude-code", { bypass: true, verb: "Reading" });
    b.welcome(2.106, "~/work/quil");
    b.paste(2.12, '<span class="c-gray">&gt; [Pasted text #1 +24 lines]</span>', "Reading");
    b.say(2.13, "Waiting for the orchestrator's first task.");
    b.finish(2.135, "Ready", "idle");
    b.paste(2.17, '<span class="c-gray">&gt; Find where quil status prints. Write .team/01-status.md</span>', "Finding the status output");
    b.tool(2.19, "Read", "cmd/quil/daemonctl.go", "Read 286 lines");
    b.tool(2.24, "Read", "cmd/quil/main.go", "Read 612 lines");
    b.tool(2.28, "Write", ".team/01-status.md", "Wrote 41 lines");
    b.finish(2.30, "Crunched for 6m 48s");
    b.paste(2.89, '<span class="c-gray">&gt; Review feat/status-json. Rank findings must-fix / should-fix / consider.</span>', "Reviewing");
    b.tool(2.92, "Bash", "git diff master...feat/status-json --stat", "3 files changed, 97 insertions(+), 4 deletions(-)");
    b.tool(5.70, "Write", ".team/03-review.md", "Wrote 33 lines: 0 must-fix, 1 should-fix");
    b.finish(5.80, "Crunched for 24m 10s");
    panes.o2 = { tab: "q/2", at: 2.105, type: "claude-code", name: "analyst", muted: true, cwd: "~/work/quil", git: "master",
      lines: b.lines, foot: b.foot, states: b.states, mcp: [[2.16, 2.2], [2.88, 2.92]] };
  }
  {
    const b = script("codex", { verb: "Working" });
    b.welcome(2.107, "~/work/quil");
    b.paste(2.121, '<span class="c-gray">› [Pasted Content 1,904 chars]</span>', "Working");
    b.say(2.13, "Waiting for the orchestrator's first task.");
    b.finish(2.135, "", "idle");
    b.paste(2.505, '<span class="c-gray">› Write tests for quil status --json. Report to .team/02-tests.md</span>', "Working");
    b.tool(2.53, "Explored", "");
    b.res(2.532, "Read cmd/quil/daemonctl.go, cmd/quil/daemonctl_test.go");
    b.tool(2.62, "Edited", "cmd/quil/daemonctl_test.go (+58 -0)");
    b.tool(2.68, "Ran", "go test ./cmd/quil -run Status");
    b.res(2.69, '<span class="c-red">--- FAIL: TestStatusJSON</span> dial unix /tmp/quil-test.sock: connect: connection refused');
    b.tool(2.78, "Edited", "cmd/quil/daemonctl_test.go (+6 -2)");
    b.tool(2.85, "Ran", "go test ./cmd/quil -run Status");
    b.res(2.86, '<span class="c-green">ok</span>  github.com/artyomsv/quil/cmd/quil');
    b.tool(2.90, "Edited", ".team/02-tests.md (+18 -0)");
    b.finish(2.95, "Worked for 28m 02s");
    panes.o3 = { tab: "q/2", at: 2.105, type: "codex", name: "developer", muted: true, cwd: "~/work/quil", git: "master",
      lines: b.lines, foot: b.foot, states: b.states, mcp: [[2.50, 2.54]] };
  }
  {
    const cwd = WTQ + "feat-status-json";
    const b = script("claude-code", { bypass: true, verb: "Implementing" });
    b.welcome(2.44, cwd);
    b.paste(2.46, '<span class="c-gray">&gt; Add --json to quil status, per .team/01-status.md</span>', "Adding --json");
    b.tool(2.48, "Read", ".team/01-status.md", "Read 41 lines");
    b.tool(2.56, "Update", "cmd/quil/daemonctl.go", "Updated with 31 additions and 4 removals");
    b.tool(2.70, "Bash", "go build ./...", '<span class="c-green">built</span>');
    b.tool(2.79, "Bash", "go test ./cmd/quil/", '<span class="c-green">ok</span>  github.com/artyomsv/quil/cmd/quil');
    b.finish(2.83, "Sautéed for 17m 51s");
    panes.o4 = { tab: "q/2", at: 2.37, type: "claude-code", name: "status-json", cwd, git: "feat/status-json wt",
      preparing: [[2.37, 2.44]], center: [[2.37, '<div class="t">⠋  creating worktree</div>feat/status-json\n<span class="c-dim">this can take a while on a large repository</span>'], [2.44, ""]],
      lines: b.lines, foot: b.foot, states: b.states, mcp: [[2.45, 2.49], [2.62, 2.66]] };
  }

  /* -------------------------------------------- 3 · the work runs elsewhere */
  {
    const b = script("codex", { verb: "Working" });
    b.welcome(3.16, "~/work/train");
    b.ask(3.165, 0.05, "run the eval suite on the new tokenizer; write regressions to reports/");
    b.tool(3.25, "Ran", "python -m eval.run --suite core --tokenizer tok-v2");
    b.res(3.26, "1,204 cases queued on cuda:0");
    b.tool(3.52, "Ran", "python -m eval.compare runs/tok-v1 runs/tok-v2");
    b.res(3.53, "3 regressions: long-context recall, code spans, emoji");
    b.tool(4.30, "Edited", "reports/regressions.md (+48 -0)");
    b.tool(5.10, "Ran", "python -m eval.run --suite long --tokenizer tok-v2");
    b.work(5.11, "Working");
    panes.t1 = { tab: "t/1", at: 3.155, type: "codex", name: "bench", cwd: "~/work/train", git: "tok-v2", w: 1.3,
      lines: b.lines, foot: b.foot, states: b.states.concat([[3.215, "working"]]) };
  }
  {
    const b = script("terminal", { dir: "train" });
    b.idle(3.105);
    b.cmd(3.12, 0.03, "tail -f logs/eval.log", "train");
    const out = [];
    const r = rng(7);
    for (let i = 0; i < 60; i++) {
      const ok = r() > 0.08;
      out.push('<span class="c-dim">case ' + String(412 + i).padStart(4, "0") + "/1204</span> " + (ok ? '<span class="c-green">pass</span>' : '<span class="c-red">FAIL</span>') + ' <span class="c-dim">' + ["recall", "code", "math", "chat", "emoji", "json"][Math.floor(r() * 6)] + "</span>");
    }
    panes.t2 = { tab: "t/1", at: 3.105, type: "terminal", name: "eval.log", cwd: "~/work/train", git: "tok-v2", lines: b.lines, foot: [], states: [],
      ambient: { from: 3.2, every: 700, keep: 40, pool: out } };
  }
  {
    const b = script("terminal", { dir: "train" });
    b.idle(3.235);
    b.cmd(3.245, 0.03, "nvidia-smi dmon -s u", "train");
    b.out(3.28, '<span class="c-dim"># gpu    sm   mem   enc   dec</span>');
    b.out(3.282, '<span class="c-dim"># Idx     %     %     %     %</span>');
    panes.t3 = { tab: "t/1", at: 3.235, type: "terminal", name: "gpu", cwd: "~/work/train", lines: b.lines, foot: [], states: [],
      ambient: { from: 3.29, every: 1000, keep: 30, pool: ["    0    97    61     0     0", "    0    98    62     0     0", "    0    96    61     0     0", "    0    99    63     0     0", '<span class="c-dim"># gpu    sm   mem   enc   dec</span>', "    0    97    62     0     0"] } };
  }

  /* -------------------------------------------- 4 · an agent in a box */
  {
    const b = script("claude-code", { bypass: true, verb: "Upgrading" });
    b.welcome(4.11, "~/work/storefront");
    b.ask(4.12, 0.05, "upgrade every dependency to its latest major and fix what breaks");
    b.tool(4.20, "Bash", "npm outdated", "14 packages outdated");
    b.tool(4.30, "Bash", "npm install react@19 vite@6 vitest@3 zod@4", "added 41 packages, changed 87 packages");
    b.tool(4.44, "Bash", "npm test", '<span class="c-red">✗ 3 failed</span> · 118 passed');
    b.tool(4.62, "Update", "src/forms/schema.ts", "Updated with 5 additions and 5 removals");
    b.tool(5.40, "Bash", "npm test", '<span class="c-green">✓ 121 passed</span>');
    b.tool(5.80, "Bash", 'git commit -am "chore(deps): upgrade to latest majors"', "[chore/deps 4d1c9e2] chore(deps): upgrade to latest majors");
    b.finish(5.85, "Worked for 1h 32m");
    panes.d1 = { tab: "s/1", at: 4.10, type: "claude-code", name: "deps", cwd: "~/work/storefront", git: "chore/deps", w: 1.5, sandbox: true,
      lines: b.lines, foot: b.foot, states: b.states };
  }
  {
    const b = script("terminal", { dir: "~" });
    b.idle(4.39);
    b.cmd(4.41, 0.05, "docker ps --format '{{.ID}}  {{.Image}}  {{.Status}}'", "~");
    b.out(4.47, "8c1f27b0d5e4  quil-sandbox:latest  Up 4 minutes");
    b.idle(4.472, "~");
    b.cmd(4.49, 0.03, "ls -a ~", "~");
    b.out(4.53, '.  ..  <span class="c-blue">.aws</span>  .gitconfig  <span class="c-blue">.ssh</span>  <span class="c-blue">work</span>');
    b.idle(4.532, "~");
    panes.d2 = { tab: "s/1", at: 4.39, type: "terminal", name: "host", cwd: "~", lines: b.lines, foot: [], states: [] };
  }

  /* ============================================================ FOCUS */
  const focus = [
    [-1, ["q/1", "q1"]],
    [1.40, ["q/1", "q2"]],
    [1.55, ["q/1", "q3"]],
    [1.92, ["q/1", "q1"]],
    [2.105, ["q/2", "o1"]],
    [3.105, ["t/1", "t2"]],
    [3.155, ["t/1", "t1"]],
    [3.235, ["t/1", "t3"]],
    [3.30, ["t/1", "t1"]],
    [4.10, ["s/1", "d1"]],
    [4.39, ["s/1", "d2"]],
    [4.56, ["s/1", "d1"]],
    [5.15, ["q/1", "q1"]],
    [5.30, ["q/1", "q2"]],
    [5.63, ["s/1", "d2"]],
    [5.92, ["q/2", "o1"]],
    [7.06, ["q/1", "q1"]],
    [7.30, ["t/1", "t1"]],
    [7.55, ["s/1", "d1"]],
    [7.80, ["q/2", "o1"]],
  ];

  /* ============================================================ STATUS */
  const events = [[0, 0], [1.94, 1], [2.30, 2], [2.83, 4], [2.95, 5], [3.53, 6], [4.50, 8], [4.70, 9], [5.66, 12], [6.05, 0], [6.9, 2]];
  const mem = [[0, "0.1 GB"], [1.2, "0.9 GB"], [2.2, "2.1 GB"], [3.2, "2.4 GB"], [4.2, "3.0 GB"], [6.05, ""], [6.62, "1.8 GB"]];
  const statusExtra = [[0, ""]];
  const chrome = [
    [-1, { side: true, tabs: true, status: true }],
    [6.05, { side: false, tabs: false, status: false }],
    [6.46, { side: false, tabs: false, status: true }],
    [6.48, { side: true, tabs: false, status: true, projects: 1, panes: false }],
    [6.51, { side: true, tabs: false, status: true, projects: 2, panes: false }],
    [6.54, { side: true, tabs: false, status: true, projects: 3, panes: false }],
    [6.57, { side: true, tabs: true, status: true }],
  ];
  const notesOpen = [[5.93, 6.04], [6.86, 7.0]];
  const hideViews = [[3.605, 3.70], [6.05, 6.60]];

  /* ============================================================ OVERLAYS */
  const dlg = (lines) => lines.join("\n");
  const typed = (text, pos, a, b) => text.slice(0, Math.round(K.seg(pos, a, b) * text.length));
  const field = (s, caret) => '<span class="field">' + esc(s) + (caret ? "│" : " ") + "</span>";
  const pad = (s, n) => (s.length >= n ? s.slice(0, n) : s + " ".repeat(n - s.length));
  const PAL_W = 70;
  function palette(query, pos, win, extraRows) {
    const rows = ['<span class="sel">&gt;</span> ' + esc(query) + '<span class="sel">│</span>', ""];
    if (extraRows) rows.push(...extraRows);
    const hits = query ? win.world.search(query, pos) : [];
    const act = win.world.activeAt(pos);
    const actProj = win.world.tabById[act.tab].project;
    if (query) {
      if (hits.length) {
        rows.push('<span class="hdr">FOUND IN PANES</span>');
        hits.slice(0, 3).forEach((h, i) => {
          let label = h.tabIdx + "." + h.paneIdx + " · " + h.type + (h.name ? " · " + h.name : "");
          if (h.project !== actProj) label += " · " + win.world.projById[h.project].name;
          const lead = !extraRows && i === 0 ? '<span class="sel">› ' : "  ";
          const tail = !extraRows && i === 0 ? "</span>" : "";
          rows.push(lead + esc(pad(label, PAL_W - 8)) + tail + '<span class="sub">' + (h.n + "×").padStart(6) + "</span>");
          rows.push('<span class="sub">    ' + esc(h.last.slice(0, PAL_W - 6)) + "</span>");
        });
      } else rows.push('<span class="sub">Searching…</span>');
    }
    rows.push("", '<span class="sub">↑↓ nav · Enter run · Esc close</span>');
    return rows.join("\n");
  }

  const overlays = [
    { id: "setup-codex", from: 1.28, to: 1.40,
      html: (t, pos) => {
        const br = typed("feat/palette-tests", pos, 1.31, 1.36);
        return dlg([
          '<span class="title">Codex — Setup</span>', "",
          "  Working directory:", '  ▸ <span class="sel">~/work/quil</span>', "    Browse…", "",
          "  ( ) Bypass approvals and sandbox (dangerous)",
          "  ( ) Auto: workspace-write sandbox, never ask",
          "  [ ] Web search", "",
          '<span class="sel">&gt; Worktree</span>    <span class="sub">type to search</span>',
          "    off — use the directory above",
          '  <span class="sel">&gt; + new branch…</span>',
          pos > 1.30 ? "    new branch: " + field(br, pos < 1.375) : "    master (current)  …/work/quil",
          pos > 1.30 ? '    <span class="sub">Enter accept   Esc cancel</span>' : "", "",
          "  [ ] Run in a Docker container", "",
          pos > 1.38 ? '<span class="sel">&gt; [Continue]</span>' : "  [Continue]", "",
          '<span class="sub">Tab next field   Space toggle   Enter submit   Esc back</span>',
        ]);
      } },
    { id: "pal-templ", from: 2.02, to: 2.055, layer: "win",
      html: (t, pos, win) => palette(typed("templ", pos, 2.022, 2.04), pos, win, typed("templ", pos, 2.022, 2.04).length >= 3 ? ['<span class="sel">› New from template</span>'] : null) },
    { id: "tmpl", from: 2.055, to: 2.105,
      html: (t, pos) => {
        const task = typed("add a --json flag to quil status", pos, 2.065, 2.09);
        return dlg([
          '<span class="title">New from template</span>', "Project: quil",
          "  Template: agent-team — An orchestrator delegating to an analyst and a developer",
          (pos > 2.06 && pos < 2.092 ? '<span class="sel">&gt; </span>' : "  ") + "Task:",
          "    " + field(task, pos < 2.092),
          (pos >= 2.092 ? '<span class="sel">&gt; </span>' : "  ") + "Directory:",
          "    ~/work/quil", "      &gt; cmd", "        internal", "        docs",
          '    <span class="sub">↑↓ move  Enter descend  ← up  Ctrl+V paste</span>',
          "  New branch: ", "",
          '<span class="sub">Tab: next field · Ctrl+S or Enter: create · Esc: cancel</span>',
        ]);
      } },
    { id: "np-train", from: 3.0, to: 3.105,
      html: (t, pos) => {
        const nm = typed("train", pos, 3.005, 3.02);
        const remote = pos > 3.025, user = typed("build", pos, 3.03, 3.04), host = typed("gpu01", pos, 3.045, 3.055);
        const dialing = pos > 3.058 && pos < 3.075, up = pos >= 3.075;
        return dlg([
          '<span class="title">New Project</span>', "",
          "  Name:", "    " + field(nm, pos < 3.022),
          (remote ? "  [x]" : "  [ ]") + " Remote (ssh)",
          "  User:", "    " + field(user, pos > 3.03 && pos < 3.043),
          "  Host:", "    " + field(host, pos > 3.045 && pos < 3.058) + (up ? '  <span class="green">✓ connected</span>' : dialing ? "" : '  <span class="sub">(required — Enter to connect)</span>'),
          up ? '<span class="sel">&gt; Root directory:</span>' : "  Root directory:",
          up ? "    /home/build/work" : '    <span class="sub">(connect a host above to browse it)</span>',
          up ? '      &gt; <span class="sel">train</span>' : "", up ? "        datasets" : "",
          up ? '    <span class="sub">↑↓ move  Enter descend  ← up  Ctrl+V paste</span>' : "", "",
          dialing ? '  <span class="amber">⟳ connecting to build@gpu01…</span>' : "", "",
          pos > 3.095 ? '<span class="sel">&gt; [ Create ]</span>' : "  [ Create ]", "",
          '<span class="sub">Tab next field    Enter select    Esc cancel</span>',
        ]);
      } },
    { id: "reconnecting", from: 3.605, to: 3.70, cls: "q-center-msg",
      html: () => '<div class="t">train@build@gpu01</div>\n\n<span class="amber">' + K.spin() + " Reconnecting…</span>" },
    { id: "setup-deps", from: 4.02, to: 4.10,
      html: (t, pos) => {
        const on = pos > 4.045, img = typed("quil-sandbox:latest", pos, 4.05, 4.075);
        return dlg([
          '<span class="title">Claude Code — Setup</span>', "",
          "  Working directory:", '  ▸ <span class="sel">~/work/storefront</span>', "    ~/work/quil", "    Browse…", "",
          "  (•) Dangerously skip permissions (no confirmations)",
          "  ( ) Enable auto mode (safer than skipping permissions)",
          "  [ ] Chrome support (Claude in Chrome extension)", "",
          "  Worktree   off", "",
          '<span class="sel">&gt; ' + (on ? "[x]" : "[ ]") + " Run in a Docker container</span>",
          on ? "        image: " + field(img, pos < 4.08) : '    <span class="sub">space to enable — the agent is confined to this checkout</span>',
          on ? "    Sign in  ( ) Browser  (•) Token" : "",
          on ? '    <span class="sub">no sign-in · saves a token every later Claude uses — ←/→ or space to change</span>' : "", "",
          "  Session:  New session   <span class=\"sub\">(Tab here to resume)</span>", "",
          pos > 4.09 ? '<span class="sel">&gt; [Continue]</span>' : "  [Continue]", "",
          '<span class="sub">Tab next field   Space toggle   Enter submit   Esc back</span>',
        ]);
      } },
    { id: "toast", from: 5.04, to: 5.26, layer: "win", cls: "q-toast",
      html: () => '<div class="ico"><svg width="18" height="20" viewBox="0 0 24 26" aria-hidden="true"><path d="M4 3h13l3 3v17H4z" fill="none" stroke="#ff8700" stroke-width="2"/><path d="M8 10h8M8 14h8M8 18h5" stroke="#ff8700" stroke-width="2"/></svg></div><div class="app">Quil</div><div class="t">quil · agents · claude-code</div><div class="m">▲ Waiting for your input (Bash)</div>' },
    { id: "pal-search", from: 5.42, to: 5.625, layer: "win",
      html: (t, pos, win) => palette(typed("quil-sandbox", pos, 5.445, 5.52), pos, win) },
    { id: "quit", from: 6.05, to: 6.14, layer: "win", cls: "q-raw", enter: false,
      html: (t, pos) => '<span class="c-path">~/work/quil</span> <span class="c-prompt">$</span> <span class="c-white">' + typed("sudo reboot", pos, 6.075, 6.12) + '</span><span class="q-caret"></span>' },
    { id: "monday", from: 6.36, to: 6.46, layer: "win", cls: "q-raw", enter: false,
      html: (t, pos) => '<span class="c-path">~</span> <span class="c-prompt">$</span> <span class="c-white">' + typed("quil", pos, 6.385, 6.43) + '</span><span class="q-caret"></span>' },
  ];

  /* notes: the line left on Friday, found again on Monday */
  const fridayNote = ["mon:", "- merge feat/status-json after the analyst's review", "- compare the long eval on gpu01 with friday", "- ship the deps upgrade if ci is green"];
  function notesAt(pos) {
    const all = fridayNote.join("\n");
    const n = pos < 6.5 ? Math.round(K.seg(pos, 5.94, 6.02) * all.length) : all.length;
    const lines = all.slice(0, n).split("\n").map((l) => esc(l));
    const typing = pos < 6.5 && n < all.length;
    if (typing) lines[lines.length - 1] += '<span class="q-caret"></span>';
    return { title: "orchestrator", lines, dirty: typing, foot: pos < 6.5 ? "Tab pane  Ctrl+S save  Esc exit  Alt+E" : "saved 2d ago  Tab pane  Ctrl+S  Esc" };
  }

  /* the reboot: what each pane of the team tab shows while it comes back */
  const aiResume = { o1: ["claude", "7c2e41d0"], o2: ["claude", "e4a8c3b1"], o3: ["codex", "9b1f06aa"], o4: ["claude", "3d90f2c7"] };
  function restoreCtx(id, pos) {
    if (pos < 6.05 || pos >= 6.84) return null;
    const p = panes[id];
    if (!p || p.tab !== "q/2") return pos < 6.60 ? { dark: true } : null;
    if (pos < 6.60) return { dark: true };
    const ctx = { ghost: true };
    const [tool, sid] = aiResume[id];
    if (pos < 6.64) { ctx.center = " "; ctx.label = K.SPIN[0] + " resuming..."; return ctx; }
    if (pos < 6.79) {
      const step = Math.floor(K.seg(pos, 6.64, 6.78) * 4);
      const rows = [
        '<span class="c-green">✓</span> session loaded',
        step >= 1 ? '<span class="c-green">✓</span> history via resume' : " ",
        step >= 2 ? '<span class="c-cyan">' + K.spin() + "</span> resuming " + tool + " · " + sid : " ",
        step >= 3 ? '<span class="c-dim">·</span> waiting for first output' : " ",
      ];
      ctx.center = '<div style="text-align:left">' + rows.join("\n") + "</div>";
      ctx.label = K.SPIN[0] + " resuming...";
      return ctx;
    }
    if (pos >= 6.81) return null;
    ctx.ghostLines = true;
    ctx.label = p.name + " · restored";
    return ctx;
  }

  /* ============================================================ HINTS
     Small notes that point at the thing on screen they explain. Targets:
       {pane}, {label: pane}, {foot: pane}, {line: [pane, text]},
       {row: text} (sidebar), {tab: index}, "status", "palette", "screen". */
  const hints = [
    { from: 0.38, to: 0.99, at: { pane: "q1" }, head: "One pane. One shell.", text: "This is Quil. Everything on this page happens in this window." },
    { from: 1.20, to: 1.99, at: { label: "q1" }, text: "You typed claude in the shell. The pane became a Claude Code pane." },
    { from: 1.44, to: 1.99, at: { row: "feat/palette-tests" }, text: "⎇ wt — Codex works in its own git worktree." },
    { from: 1.66, to: 1.99, at: { tab: 0 }, text: "⠹ on the tab — agents are working in it." },
    { from: 2.13, to: 2.99, at: { pane: "o2" }, text: "[muted] workers stay quiet. Only the orchestrator talks to you." },
    { from: 2.31, to: 2.99, at: { line: ["o1", "[quil task task-51c0a9d3]"] }, text: "When a task ends, Quil types the result back into the orchestrator." },
    { from: 2.45, to: 2.99, at: { pane: "o4" }, text: "Orange border — an agent is using this pane right now." },
    { from: 3.11, to: 3.39, at: { row: "@build@gpu01" }, text: "This project lives on gpu01. Its panes run there." },
    { from: 3.40, to: 3.60, at: "screen", head: "The lid is closed.", text: "gpu01 keeps running every pane." },
    { from: 3.70, to: 3.99, at: { pane: "t1" }, text: "Nothing restarted. The output kept coming while you were away." },
    { from: 4.12, to: 4.99, at: { pane: "d1" }, kind: "box", text: "Docker container: only ~/work/storefront is inside." },
    { from: 4.20, to: 4.99, at: { foot: "d1" }, text: "No confirmations. The worst it can break is the box." },
    { from: 4.54, to: 4.99, at: { pane: "d2" }, text: "Your machine: ~/.ssh and ~/.aws stay out here." },
    { from: 4.98, to: 5.14, at: { row: "▲" }, head: "▲ waiting  ⠹ working  ✓ finished", text: "Every project rolls up its agents in the sidebar." },
    { from: 5.155, to: 5.41, at: { pane: "q1" }, text: "Alt+Shift+A jumped here. This agent waited longest." },
    { from: 5.45, to: 5.62, at: "palette", text: "Searches actions, tabs, projects and every pane's scrollback." },
    { from: 5.64, to: 5.99, at: { line: ["d2", "quil-sandbox:latest"] }, text: "Enter took you to the pane with that container id." },
    { from: 6.62, to: 6.85, at: { label: "o1" }, text: "Each agent resumes its own conversation." },
    { from: 6.62, to: 6.99, at: { row: "train" }, text: "gpu01 never stopped. Nothing to resume there." },
    { from: 6.87, to: 6.99, at: "notes", text: "The note you left on Friday (Alt+E) is still here." },
  ];

  /* ============================================================ KEYS
     What the person at the keyboard presses, for the keystroke overlay. */
  const keys = [
    [0.20, "quil ⏎", "start"],
    [0.55, "git status ⏎", ""],
    [1.03, "claude ⏎", "a shell becomes an agent"],
    [1.28, "Ctrl+N", "new pane"],
    [1.31, "+ new branch…", "worktree"],
    [1.39, "⏎", "create"],
    [1.55, "Alt+Shift+V", "split"],
    [1.57, "opencode ⏎", ""],
    [2.02, "Alt+Shift+P", "palette"],
    [2.05, "⏎", "New from template"],
    [2.10, "Ctrl+S", "create the team"],
    [3.00, "Alt+Shift+N", "new project"],
    [3.025, "Space", "Remote (ssh)"],
    [3.10, "⏎", "create"],
    [4.00, "Ctrl+T", "new tab"],
    [4.045, "Space", "run in Docker"],
    [4.09, "⏎", "continue"],
    [5.15, "Alt+Shift+A", "waiting longest"],
    [5.20, "1 ⏎", "yes"],
    [5.30, "Alt+Shift+A", "next one"],
    [5.31, "1 ⏎", "yes"],
    [5.42, "Alt+Shift+P", "search"],
    [5.62, "⏎", "go to pane"],
    [5.93, "Alt+E", "pane notes"],
    [6.05, "Ctrl+Q", "quit · the workspace stays"],
    [6.075, "sudo reboot ⏎", ""],
    [6.385, "quil ⏎", "Monday"],
  ];

export default {
    version: SITE.software.version,
    chapters, projects, tabs, panes, focus, overlays, events, mem, statusExtra, chrome, notesOpen, hideViews,
    fridayNote, notesAt, restoreCtx, palette, hints, keys,
  };
