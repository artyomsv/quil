package daemon

import (
	"strings"
	"testing"

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
			d, pane := handStartFixture(t, tc.policy)
			pane.handStart.token = tc.bound(tok)

			d.detectHandStart(pane, pane.ID, []byte(handStartOSC+tc.payload(tok)+"\x1b\\"))

			got := pane.drainInput()
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
	d, pane := handStartFixture(t, "")
	pane.handStart.token = "TOK"

	// A timestamp far in the past, which is how a marker looks when the daemon
	// was blocked on a lock while the shell's deadline ran out.
	d.detectHandStart(pane, pane.ID, []byte(handStartOSC+"cmd;TOK;claude;;1000000000.0;;"+"\x1b\\"))

	if got := pane.drainInput(); got != "" {
		t.Fatalf("answered %q for a marker the shell had already given up on", got)
	}
}

// Two markers in one chunk get one answer each, not one between them and not
// three.
func TestDetectHandStart_OneAnswerPerMarker(t *testing.T) {
	d, pane := handStartFixture(t, config.HandStartedOff)
	pane.handStart.token = "TOK"

	chunk := handStartOSC + "cmd;TOK;claude;;;;" + "\x1b\\" + handStartOSC + "cmd;TOK;claude;;;;" + "\x1b\\"
	d.detectHandStart(pane, pane.ID, []byte(chunk))

	got := pane.drainInput()
	if want := strings.Repeat(handStartReplyRun, 2); got != want {
		t.Fatalf("answered %q, want %q", got, want)
	}
}

// The tail must carry a split marker across chunks, or a launch that happens to
// straddle a read costs a second and converts nothing.
func TestDetectHandStart_AnswersASplitMarker(t *testing.T) {
	d, pane := handStartFixture(t, config.HandStartedOff)
	pane.handStart.token = "TOK"

	full := handStartOSC + "cmd;TOK;claude;;;;" + "\x1b\\"
	d.detectHandStart(pane, pane.ID, []byte(full[:10]))
	if got := pane.drainInput(); got != "" {
		t.Fatalf("answered %q on a half-received marker", got)
	}
	d.detectHandStart(pane, pane.ID, []byte(full[10:]))
	if got := pane.drainInput(); got != handStartReplyRun {
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
func handStartFixture(t *testing.T, policy string) (*Daemon, *Pane) {
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
	pane.inputOnce.Do(func() { pane.inputCh = make(chan []byte, 16) })
	return d, pane
}

// drainInput answers everything the detector enqueued for this pane's child,
// concatenated in order.
func (p *Pane) drainInput() string {
	var b strings.Builder
	for {
		select {
		case data := <-p.inputCh:
			b.Write(data)
		default:
			return b.String()
		}
	}
}
