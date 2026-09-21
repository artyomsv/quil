package daemon

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/config"
)

// Every authenticated, fresh marker must end in EXACTLY ONE outcome: a reply,
// or a conversion. Two answers type eight stray bytes into whatever is running;
// none costs the user a second of dead air waiting for a reply that never
// comes. Both failures are silent in production, which is why this is pinned.
func TestDetectHandStart_ExactlyOneAnswer(t *testing.T) {
	cases := []struct {
		name      string
		payload   func(tok string) string
		bound     func(tok string) string
		policy    string
		wantReply string
	}{
		{
			name:      "a bare launch converts",
			payload:   func(tok string) string { return "cmd;" + tok + ";claude;;;;" },
			bound:     func(tok string) string { return tok },
			wantReply: handStartReplyConvert,
		},
		{
			name:      "the #221 command line converts",
			payload:   func(tok string) string { return "cmd;" + tok + ";claude;;;;--resume,abc,--enable-auto-mode" },
			bound:     func(tok string) string { return tok },
			wantReply: handStartReplyConvert,
		},
		{
			name:      "a refused shape runs as typed",
			payload:   func(tok string) string { return "cmd;" + tok + ";claude;;;;mcp,list" },
			bound:     func(tok string) string { return tok },
			wantReply: handStartReplyRun,
		},
		{
			name:      "an unknown command runs as typed",
			payload:   func(tok string) string { return "cmd;" + tok + ";notanagent;;;;" },
			bound:     func(tok string) string { return tok },
			wantReply: handStartReplyRun,
		},
		{
			// Output from an ssh remote, a pasted log or a web page never had
			// this shell's token, so it cannot move the pane.
			name:      "a forged marker is ignored entirely",
			payload:   func(string) string { return "cmd;WRONGTOKEN;claude;;;;" },
			bound:     func(tok string) string { return tok },
			wantReply: "",
		},
		{
			name:      "a pane that was never armed ignores everything",
			payload:   func(tok string) string { return "cmd;" + tok + ";claude;;;;" },
			bound:     func(string) string { return "" },
			wantReply: "",
		},
		{
			name:      "hand_started = off runs as typed",
			payload:   func(tok string) string { return "cmd;" + tok + ";claude;;;;" },
			bound:     func(tok string) string { return tok },
			policy:    config.HandStartedOff,
			wantReply: handStartReplyRun,
		},
		{
			name:      "hand_started = notify runs as typed",
			payload:   func(tok string) string { return "cmd;" + tok + ";claude;;;;" },
			bound:     func(tok string) string { return tok },
			policy:    config.HandStartedNotify,
			wantReply: handStartReplyRun,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const tok = "TESTTOKEN"
			d, pane, rec := handStartFixture(t, tc.policy)
			pane.handStart.token = tc.bound(tok)

			d.detectHandStart(pane, pane.ID, []byte(handStartOSC+tc.payload(tok)+"\x1b\\"), time.Now())

			got := rec.drain()
			switch {
			case tc.wantReply == "":
				if got != "" {
					t.Fatalf("answered %q for a marker that should have been ignored", got)
				}
			case got != tc.wantReply:
				t.Fatalf("answered %q, want %q", got, tc.wantReply)
			}
		})
	}
}

// A marker past the cutoff is not answered at all: the shell has already run
// the binary, and eight characters would land in a live agent's composer.
func TestDetectHandStart_StaleMarkerIsNotAnswered(t *testing.T) {
	d, pane, rec := handStartFixture(t, "")
	pane.handStart.token = "TOK"

	// A timestamp far in the past, which is how a marker looks when the daemon
	// was blocked on a lock while the shell's deadline ran out.
	d.detectHandStart(pane, pane.ID, []byte(handStartOSC+"cmd;TOK;claude;;1000000000.0;;"+"\x1b\\"), time.Now())

	if got := rec.drain(); got != "" {
		t.Fatalf("answered %q for a marker the shell had already given up on", got)
	}
}

// Two markers in one chunk get one answer each, not one between them and not
// three.
func TestDetectHandStart_OneAnswerPerMarker(t *testing.T) {
	d, pane, rec := handStartFixture(t, config.HandStartedOff)
	pane.handStart.token = "TOK"

	chunk := handStartOSC + "cmd;TOK;claude;;;;" + "\x1b\\" + handStartOSC + "cmd;TOK;claude;;;;" + "\x1b\\"
	d.detectHandStart(pane, pane.ID, []byte(chunk), time.Now())

	got := rec.drain()
	if want := strings.Repeat(handStartReplyRun, 2); got != want {
		t.Fatalf("answered %q, want %q", got, want)
	}
}

// The tail must carry a split marker across chunks, or a launch that happens to
// straddle a read costs a second and converts nothing.
func TestDetectHandStart_AnswersASplitMarker(t *testing.T) {
	d, pane, rec := handStartFixture(t, config.HandStartedOff)
	pane.handStart.token = "TOK"

	full := handStartOSC + "cmd;TOK;claude;;;;" + "\x1b\\"
	d.detectHandStart(pane, pane.ID, []byte(full[:10]), time.Now())
	if got := rec.drain(); got != "" {
		t.Fatalf("answered %q on a half-received marker", got)
	}
	d.detectHandStart(pane, pane.ID, []byte(full[10:]), time.Now())
	if got := rec.drain(); got != handStartReplyRun {
		t.Fatalf("answered %q after the marker completed, want %q", got, handStartReplyRun)
	}
}

// handStartFixture builds a daemon with a claude-code plugin registered and a
// terminal pane whose input queue is captured rather than written to a PTY.
//
// The input writer goroutine is deliberately NOT started: EnqueueInput pushes
// onto inputCh and nothing drains it, so drainInput can read exactly what the
// detector decided to send. Consuming inputOnce is what keeps EnsureInputWriter
// from starting the real writer underneath.
func handStartFixture(t *testing.T, policy string) (*Daemon, *Pane, *replyRecorder) {
	t.Helper()
	d := newTestDaemon(t)
	if policy != "" {
		d.cfg.Agents.HandStarted = policy
	}
	// Availability is set at startup by running each plugin's detect command,
	// which a test daemon never does — and handStartTargets excludes anything
	// unavailable, so without this every marker falls through to "run".
	for _, p := range d.registry.All() {
		p.Available = true
	}
	pane := &Pane{ID: "pane-handstart", Type: "terminal"}
	// The reply is written DIRECTLY to the PTY, not queued: the queue's writer
	// resolves pane.PTY when it drains, and a conversion replaces that PTY
	// microseconds later. Capturing the queue instead of the PTY is what let
	// the race through review in the first place, so the fixture captures what
	// production actually writes to.
	rec := newReplyRecorder(t)
	pane.PTY = rec
	// The recorder is handed back rather than re-read from pane.PTY, because a
	// conversion REPLACES that field — reading it afterwards would find the new
	// agent's session and report no reply at all. Holding the original is the
	// only way to assert the shell was answered before its PTY was swapped.
	return d, pane, rec
}

// replyRecorder is a PTY that records what the daemon wrote to the child.
type replyRecorder struct {
	fakeSession
	mu      sync.Mutex
	written []byte
	alive   chan struct{}
}

// WaitExit blocks, then reports a KILLED exit.
//
// Blocking, because fakeSession's returns 0 at once: that makes the spawned
// "agent" look like one that exited cleanly, and a converted pane that exits
// cleanly is correctly returned to its shell — so the conversion undid itself
// before any assertion could see it. The revert is covered through the real
// exit path in handstart_wiring_test.go instead.
//
// Killed rather than 0, because the unblock happens in t.Cleanup, i.e. AFTER
// the test body. A zero exit there runs returnToShellOnCleanExit, which
// restarts the pane, which calls newSessionFn — a package-level seam that by
// then belongs to whichever test runs next. A test that swaps it to capture
// spawn dimensions then reads values this pane wrote. Cross-test state escaping
// through a fixture's teardown, and invisible in any single test.
func (r *replyRecorder) WaitExit() int {
	<-r.alive
	return 130
}

func (r *replyRecorder) Write(data []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.written = append(r.written, data...)
	return len(data), nil
}

// drain answers everything the daemon wrote to the child, in order, and
// clears the record.
func (r *replyRecorder) drain() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := string(r.written)
	r.written = nil
	return out
}

// newReplyRecorder builds a recorder whose child stays alive for the test.
func newReplyRecorder(t *testing.T) *replyRecorder {
	t.Helper()
	r := &replyRecorder{alive: make(chan struct{})}
	t.Cleanup(func() { close(r.alive) })
	return r
}
