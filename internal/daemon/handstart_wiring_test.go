package daemon

import (
	"context"
	"github.com/artyomsv/quil/internal/claudesessions"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/plugin"
	apty "github.com/artyomsv/quil/internal/pty"
)

// The whole chain, through the real spawn: a pane spawned by spawnPane must end
// up holding the token its own shell was started with, and a marker carrying
// THAT token must convert it.
//
// Every other detection test hand-assigns pane.handStart.token, which is
// exactly the assertion that cannot catch the two bugs this feature already
// shipped: the pool dropping Intercept and TryClaim dropping the token. Both
// left every piece correct in isolation and the feature dead in production,
// with no log line anywhere.
func TestSpawnPane_BindsTheTokenTheShellWasStartedWith(t *testing.T) {
	// The terminal plugin's command is $SHELL, and shellinit injects nothing
	// for /bin/sh — which is what a container has. Nothing is armed there, by
	// design, so the test has to ask for a shell the feature covers.
	t.Setenv("SHELL", "/bin/zsh")
	d := newTestDaemon(t)
	for _, p := range d.registry.All() {
		p.Available = true
	}
	tab := d.session.CreateTab("t")
	pane := mustPane(t, d, tab.ID)
	pane.Type = "terminal"
	pane.CWD = t.TempDir()

	// The conversion at the end of this test respawns the pane through
	// newSessionFn, which newTestDaemon points at a bare fakeSession — and
	// fakeSession.WaitExit returns 0 AT ONCE. A converted pane whose agent
	// exits cleanly is correctly returned to its shell, so the conversion
	// undid itself before the assertion below could read it: the pane was
	// back to "terminal", the feature working exactly as designed.
	//
	// replyRecorder already carries the fix for the shell half of this and
	// says so in its own doc comment; the agent half was left on the default.
	// It is a coin flip between the assertion and the exit goroutine, not a
	// rare interleaving — it reproduced on the first of 30 local -race runs
	// and turned CI red on a commit that touched no Go code at all.
	prevNew := newSessionFn
	newSessionFn = func(cols, rows int) apty.Session { return newReplyRecorder(t) }
	t.Cleanup(func() { newSessionFn = prevNew })

	sess := newReplyRecorder(t)
	if err := d.spawnPane(pane, sess, false); err != nil {
		t.Fatalf("spawn: %v", err)
	}

	pane.PluginMu.Lock()
	bound := pane.handStart.token
	pane.PluginMu.Unlock()
	if bound == "" {
		t.Fatal("spawnPane bound no interception token, so every authentic marker " +
			"from this pane's shell would be dropped as a forgery")
	}

	// The shell must have been told the SAME token, or the two ends disagree.
	var exported string
	for _, kv := range sess.env {
		if v, ok := strings.CutPrefix(kv, "QUIL_INTERCEPT_TOKEN="); ok {
			exported = v
		}
	}
	if exported != bound {
		t.Fatalf("shell got token %q but the pane was bound to %q", exported, bound)
	}
	if !strings.Contains(strings.Join(sess.env, " "), "QUIL_INTERCEPT=") {
		t.Fatalf("shell was started without a name list: %v", sess.env)
	}

	// And a marker carrying it converts, end to end.
	sess.drain()
	d.detectHandStart(pane, pane.ID, []byte(handStartOSC+"cmd;"+bound+";claude;"+pane.CWD+";;;"+"\x1b\\"), time.Now())
	if got := sess.drain(); got != handStartReplyConvert {
		t.Fatalf("answered %q, want %q — the chain is broken somewhere between "+
			"the spawn and the detector", got, handStartReplyConvert)
	}
	// Under PluginMu, like the token read above: Pane.Type is written there by
	// every path that owns it, and this bare read is what the race detector
	// reported alongside the failure above. Keeping the lock means the next
	// change to the fixture cannot quietly reintroduce the report.
	pane.PluginMu.Lock()
	gotType := pane.Type
	pane.PluginMu.Unlock()
	if gotType != "claude-code" {
		t.Fatalf("pane type = %q, want claude-code", gotType)
	}
}

// A terminal pane must NOT be armed when the policy is off, or the shell pauses
// for a second before every agent launch and converts nothing.
func TestSpawnPane_PolicyOffArmsNothing(t *testing.T) {
	t.Setenv("SHELL", "/bin/zsh")
	d := newTestDaemon(t)
	d.cfg.Agents.HandStarted = config.HandStartedOff
	for _, p := range d.registry.All() {
		p.Available = true
	}
	tab := d.session.CreateTab("t")
	pane := mustPane(t, d, tab.ID)
	pane.Type = "terminal"
	pane.CWD = t.TempDir()

	sess := newReplyRecorder(t)
	if err := d.spawnPane(pane, sess, false); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	pane.PluginMu.Lock()
	bound := pane.handStart.token
	pane.PluginMu.Unlock()
	if bound != "" {
		t.Error("a token was bound with hand_started = off")
	}
	if strings.Contains(strings.Join(sess.env, " "), "QUIL_INTERCEPT=") {
		t.Errorf("the shell was armed with hand_started = off: %v", sess.env)
	}
}

// returnToShellOnCleanExit has exactly one production call site, and every test
// that reached it called it directly. Deleting the call from the exit path
// would have failed nothing — the same wiring-bypass shape that lets a feature
// exist in the code and not in the product.
func TestOnPaneExit_ReturnsAConvertedPaneToItsShell(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	pane := mustPane(t, d, tab.ID)
	pane.Type = "claude-code"
	pane.ConvertedFromTerminal = "terminal"
	pane.InstanceArgs = []string{"--resume", "abc"}
	pane.PluginState = map[string]string{"session_id": "abc", "transcript_path": "/tmp/abc.jsonl"}
	pane.CWD = t.TempDir()
	pane.PTY = newReplyRecorder(t)

	d.onPaneExitGeneration(pane, 0, 0)

	pane.PluginMu.Lock()
	defer pane.PluginMu.Unlock()
	if pane.Type != "terminal" {
		t.Fatalf("type = %q — the exit path does not call returnToShellOnCleanExit", pane.Type)
	}
	if pane.ConvertedFromTerminal != "" {
		t.Error("the conversion mark survived, so the next clean exit would fire again")
	}
	// The conversation is over. Leaving its id behind made the next bare
	// `claude` in this pane reopen the conversation the user had just closed.
	for _, k := range []string{"session_id", "transcript_path", "resume_session_id"} {
		if v := pane.PluginState[k]; v != "" {
			t.Errorf("PluginState[%q] = %q survived the exit", k, v)
		}
	}
}

// A pane whose agent CRASHED must show that it did, through the real path.
func TestOnPaneExit_LeavesACrashedAgentInPlace(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	pane := mustPane(t, d, tab.ID)
	pane.Type = "claude-code"
	pane.ConvertedFromTerminal = "terminal"
	pane.PTY = newReplyRecorder(t)

	d.onPaneExitGeneration(pane, 1, 0)

	if pane.Type != "claude-code" {
		t.Fatalf("type = %q, want the failed agent left in place with its error", pane.Type)
	}
}

// paneHoldingSession walks every tab by design; the original test put both
// panes in one tab, so a cross-tab holder was never exercised.
func TestClaimAdoptedSession_FindsAHolderInAnotherTab(t *testing.T) {
	d := newTestDaemon(t)
	tabA := d.session.CreateTab("a")
	tabB := d.session.CreateTab("b")

	holder := mustPane(t, d, tabA.ID)
	holder.PluginState = map[string]string{"session_id": "SHARED"}
	claimant := mustPane(t, d, tabB.ID)

	if d.claimAdoptedSession(claimant, "SHARED") {
		t.Fatal("adopted a session held by a pane in a different tab; the next " +
			"restart would resume someone else's conversation into this pane")
	}
}

// A mismatched token is reported at most once an hour per pane. Silent was
// indistinguishable from "no marker arrived", which is what made two
// whole-feature failures need a live session to find; unthrottled would let a
// noisy source own the log file.
func TestLogHandStartMismatch_RateLimited(t *testing.T) {
	d := newTestDaemon(t)
	pane := &Pane{ID: "pane-x"}

	var out syncBuffer
	prev := log.Writer()
	log.SetOutput(&out)
	t.Cleanup(func() { log.SetOutput(prev) })

	for i := 0; i < 5; i++ {
		d.logHandStartMismatch(pane, pane.ID, "claude", "bound")
	}
	if n := strings.Count(out.String(), "does not hold"); n != 1 {
		t.Fatalf("reported %d times in a row, want exactly 1", n)
	}
	// Neither token may reach the log: the bound one is a live credential.
	if strings.Contains(out.String(), "bound") {
		t.Error("the bound token was printed")
	}
}

// An unrecognised value is treated as the default rather than refused: this is
// a hand-edited file and a typo must not stop the daemon starting.
func TestHandStartedPolicy_UnknownFallsBackToConvert(t *testing.T) {
	for _, in := range []string{"", "coonvert", "yes", "TRUE"} {
		if got := (config.AgentsConfig{HandStarted: in}).HandStartedPolicy(); got != config.HandStartedConvert {
			t.Errorf("policy(%q) = %q, want %q", in, got, config.HandStartedConvert)
		}
	}
	for _, in := range []string{config.HandStartedAdopt, config.HandStartedNotify, config.HandStartedOff} {
		if got := (config.AgentsConfig{HandStarted: in}).HandStartedPolicy(); got != in {
			t.Errorf("policy(%q) = %q, want it preserved", in, got)
		}
	}
}

// Adoption and the untracked card are gated on the launch actually opening a
// session. `claude doctor` opens none, and adopting on its behalf would mark
// the pane as holding a conversation that belongs to something else — a
// transcript written seconds earlier in the same directory is all it takes.
// `claude attach <id>` is worse: it is refused BECAUSE the session belongs to
// another agent, so adopting it records exactly the id the refusal was about.
func TestClassifyHandStart_SessionfulGatesAdoption(t *testing.T) {
	p := &plugin.PanePlugin{Name: "claude-code"}
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"a real launch", nil, true},
		{"a resume", []string{"--resume", "abc"}, true},
		// Refused, but the launch WOULD have been a session — worth adopting.
		{"a typed --settings", []string{"--settings", "/tmp/x"}, true},
		// Refused because it opens no session at all.
		{"doctor", []string{"doctor"}, false},
		{"mcp", []string{"mcp", "list"}, false},
		{"attach", []string{"attach", "abc"}, false},
		{"contradictory flags", []string{"--resume", "a", "--continue"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyHandStart(p, handStartMarker{Name: "claude", Args: tc.args}, noEnv)
			if got.Sessionful != tc.want {
				t.Fatalf("Sessionful = %v, want %v (reason %q)", got.Sessionful, tc.want, got.Reason)
			}
		})
	}
}

// `--resume=<id>` is the same flag as `--resume <id>`. Without normalising it,
// the session guard was bypassed by a second spelling and resolveSpawnArgs
// appended its own --session-id on top — the exact argv claude refuses.
func TestClassifyHandStart_EqualsSpelling(t *testing.T) {
	p := &plugin.PanePlugin{Name: "claude-code"}
	for _, args := range [][]string{
		{"--settings=/tmp/x.json"},
		{"--resume=A", "--session-id=B"},
	} {
		if got := classifyHandStart(p, handStartMarker{Name: "claude", Args: args}, noEnv); got.Class != handStartRun {
			t.Errorf("%v converted; the = spelling bypassed the guard", args)
		}
	}
	if !instanceArgsNameSession("claude", []string{"--resume=A"}) {
		t.Error("--resume=A was not recognised as naming a session")
	}
	// The value travels in the same token, so no following word is consumed.
	if got := strings.Join(stripSessionArgs("claude", []string{"--resume=A", "--chrome"}), " "); got != "--chrome" {
		t.Errorf("stripSessionArgs = %q, want \"--chrome\"", got)
	}
}

// A converted pane must not be convertible again by later markers in the SAME
// chunk. Read once outside the loop, sixty markers in one coalesced chunk drove
// sixty pane restarts.
func TestDetectHandStart_DisarmStopsLaterMarkersInTheSameChunk(t *testing.T) {
	d, pane, rec := handStartFixture(t, "")
	pane.handStart.token = "TOK"

	one := handStartOSC + "cmd;TOK;claude;;;;" + "\x1b\\"
	d.detectHandStart(pane, pane.ID, []byte(one+one+one), time.Now())

	if got := rec.drain(); got != handStartReplyConvert {
		t.Fatalf("answered %q, want exactly one %q", got, handStartReplyConvert)
	}
}

// The subcommand is the first POSITIONAL, not argv[0]. A global option before
// it — `claude --verbose attach <id>` is a valid invocation — walked past an
// index-zero check into conversion, respawning Quil's own claude as a second
// client of a conversation a background agent owns.
func TestClassifyHandStart_SubcommandAfterGlobalOptions(t *testing.T) {
	p := &plugin.PanePlugin{Name: "claude-code"}
	cases := []struct {
		name string
		args []string
		want handStartClass
	}{
		{"attach at the front", []string{"attach", "abc"}, handStartRun},
		{"attach behind a global option", []string{"--verbose", "attach", "abc"}, handStartRun},
		{"doctor behind two", []string{"--verbose", "--debug", "doctor"}, handStartRun},
		// A quoted prompt is ONE token, so it cannot collide with these words.
		{"a prompt that mentions attach", []string{"attach the logs please"}, handStartConvert},
		// Flags only: nothing positional to classify.
		{"flags only", []string{"--chrome", "--dangerously-skip-permissions"}, handStartConvert},
		// A flag's value landing in first-positional position is harmless: an
		// unknown word is a session, which is the direction that keeps working.
		{"a flag value is not a subcommand", []string{"--model", "opus"}, handStartConvert},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyHandStart(p, handStartMarker{Name: "claude", Args: tc.args}, noEnv); got.Class != tc.want {
				t.Fatalf("class = %v (%q), want %v", got.Class, got.Reason, tc.want)
			}
		})
	}
}

func TestSubcommandIndex(t *testing.T) {
	flags := sessionFlagsFor("claude")
	cases := []struct {
		args []string
		want int
	}{
		{nil, -1},
		{[]string{"attach"}, 0},
		{[]string{"--verbose", "attach"}, 1},
		{[]string{"--a", "--b", "x"}, 2},
		{[]string{"--only", "--flags"}, -1},
		// A session selector rules out a subcommand: what follows is prompt.
		{[]string{"--continue", "mcp"}, -1},
		{[]string{"--resume", "abc", "doctor"}, -1},
	}
	for _, tc := range cases {
		if got := subcommandIndex(tc.args, flags); got != tc.want {
			t.Errorf("subcommandIndex(%v) = %d, want %d", tc.args, got, tc.want)
		}
	}
}

// The retries outlive the process they are waiting for. Bound to the PTY run
// the marker came from, so a session that appears in the same directory after
// the intercepted process is gone is not claimed — it belongs to whatever
// started it, and adopting it would deny it to that pane.
func TestAdoptClaudeSession_AbandonsWhenTheRunIsGone(t *testing.T) {
	prev := listSessionsFn
	listSessionsFn = func(context.Context, string) ([]claudesessions.Session, error) {
		return []claudesessions.Session{{ID: "SOMEONE-ELSES", Modified: time.Now()}}, nil
	}
	t.Cleanup(func() { listSessionsFn = prev })
	handStartAdoptDelayForTest(t, time.Millisecond)

	d, pane, _ := handStartFixture(t, config.HandStartedAdopt)
	pane.CWD = t.TempDir()
	pane.ptyGen = 7

	d.adoptClaudeSession(pane, d.registry.Get("claude-code"), handStartMarker{Name: "claude", CWD: pane.CWD})

	// The run ends before the first scan lands.
	pane.PluginMu.Lock()
	pane.ptyGen = 8
	pane.PluginMu.Unlock()

	time.Sleep(200 * time.Millisecond)
	pane.PluginMu.Lock()
	got := pane.PluginState["session_id"]
	pane.PluginMu.Unlock()
	if got != "" {
		t.Fatalf("adopted %q after its run had ended", got)
	}
}
