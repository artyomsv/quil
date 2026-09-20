package daemon

import (
	"bytes"
	"crypto/subtle"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/artyomsv/quil/internal/plugin"
)

// Hand-started agent interception, daemon side. See
// docs/superpowers/specs/2026-09-19-hand-started-claude-detection-design.md.
//
// The shell function writes ESC ] 7770 ; <payload> ESC \ to its tty before the
// agent binary execs, and waits up to a second for an eight-byte answer. This
// file turns those bytes back into a decision.

const (
	// handStartOSC is the marker's introducer. 7770 is unassigned, and
	// oscfilter passes unknown OSC sequences through to the emulator, which
	// does not paint them.
	handStartOSC = "\x1b]7770;"
	// handStartMaxMarker bounds the retained tail. The shell caps its payload
	// at 2048 bytes; the slack covers the introducer, the terminator and a
	// marker split across two reads.
	handStartMaxMarker = 2200
	// handStartReplyRun and handStartReplyConvert are both exactly eight bytes.
	// The shell reads a fixed length rather than a line because LF is not Enter
	// under ConPTY, and because a terminated reply arriving late would submit
	// itself as a prompt into the agent it failed to intercept.
	handStartReplyRun     = "quil:run"
	handStartReplyConvert = "quil:cnv"
	// handStartCutoff is how stale a marker may be and still be answered. The
	// shell waits a second; past this the daemon says nothing at all, because
	// the shell has already run the binary and eight bytes typed into a live
	// agent's composer is the failure being avoided.
	handStartCutoff = 700 * time.Millisecond
	// handStartArrivalCutoff is the tighter bound used when the shell could not
	// supply a timestamp (bash 4.x has no EPOCHREALTIME), where the daemon can
	// only measure from its own arrival time and has already lost the transit.
	handStartArrivalCutoff = 400 * time.Millisecond
	// handStartMaxArgs bounds the argv a marker may carry.
	handStartMaxArgs = 64
)

// handStartMarker is one authenticated launch attempt.
type handStartMarker struct {
	Token string
	// Name is the word the user typed, matched against handStartTargets.
	Name string
	// CWD is the shell's own $PWD. Pane.CWD is written by the TUI's OSC 7
	// handler, so a pane driven with no client attached has a stale one; the
	// shell's value is authoritative and is validated like any client path.
	CWD string
	// At is the shell's timestamp. Zero when the shell could not supply one.
	At time.Time
	// EnvNames are the NAMES of agent-prefixed variables set in that shell.
	// Never values: the marker is echoed into the pane's output and persisted
	// in its ghost buffer.
	EnvNames []string
	// Args is the argv after the command word.
	Args []string
	// Oversize is set when the shell reported it could not fit the payload.
	Oversize bool
}

// handStartClass is what the daemon decided to do about a marker.
type handStartClass int

const (
	// handStartRun answers "run the binary as typed". Every refusal is this.
	handStartRun handStartClass = iota
	// handStartConvert answers "step aside, the pane is being reopened".
	handStartConvert
)

// handStartDecision pairs the class with the reason, which the user-facing card
// repeats. A refusal the user cannot see the reason for is indistinguishable
// from the feature not working.
type handStartDecision struct {
	Class  handStartClass
	Reason string
}

// decodeHandStartField reverses the shell's percent-encoding of the three
// characters the marker's own grammar uses.
//
// The encoding exists because $PWD sits between fixed fields: a directory
// containing a semicolon would otherwise shift every field after it, and the
// daemon would spawn in the wrong place or refuse a valid launch.
func decodeHandStartField(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+3], 16, 8); err == nil {
				b.WriteByte(byte(v))
				i += 2
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// parseHandStart decodes one marker payload. It answers ok=false for anything
// it cannot read as a launch, and the caller then answers nothing — an
// unparseable marker is not necessarily ours.
func parseHandStart(payload string) (handStartMarker, bool) {
	// cmd ; token ; name ; pwd ; ts ; envnames ; argv
	parts := strings.Split(payload, ";")
	if len(parts) != 7 || parts[0] != "cmd" {
		return handStartMarker{}, false
	}
	m := handStartMarker{Token: parts[1], Name: parts[2]}
	if m.Token == "" || m.Name == "" {
		return handStartMarker{}, false
	}
	if parts[6] == "!" && parts[3] == "" {
		// The shell could not fit the payload. It said so rather than
		// truncating into a different command than the user typed.
		m.Oversize = true
		return m, true
	}
	m.CWD = decodeHandStartField(parts[3])
	if ts := parts[4]; ts != "" {
		m.At = parseHandStartTime(ts)
	}
	if parts[5] != "" {
		m.EnvNames = strings.Split(parts[5], ",")
	}
	if parts[6] != "" {
		for _, a := range strings.Split(parts[6], ",") {
			if len(m.Args) >= handStartMaxArgs {
				// More argv than any interactive launch carries. Refuse rather
				// than truncate: a clipped argv is a different command.
				return handStartMarker{}, false
			}
			m.Args = append(m.Args, decodeHandStartField(a))
		}
	}
	return m, true
}

// parseHandStartTime reads the two shapes the shells can produce: bash and zsh
// send seconds with a fractional part (EPOCHREALTIME), PowerShell sends integer
// milliseconds. Anything else yields the zero time, and the caller falls back
// to the tighter arrival-based cutoff.
func parseHandStartTime(ts string) time.Time {
	if strings.ContainsAny(ts, ".,") {
		// EPOCHREALTIME uses the locale's radix character, which is a comma in
		// much of Europe — the shell cannot be relied on to emit a period.
		secs, err := strconv.ParseFloat(strings.Replace(ts, ",", ".", 1), 64)
		if err != nil || secs <= 0 {
			return time.Time{}
		}
		return time.UnixMilli(int64(secs * 1000))
	}
	ms, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

// fresh reports whether a marker is recent enough to answer.
func (m handStartMarker) fresh(now, arrival time.Time) bool {
	if m.At.IsZero() {
		return now.Sub(arrival) < handStartArrivalCutoff
	}
	// A shell clock ahead of the daemon's is not evidence of staleness.
	d := now.Sub(m.At)
	if d < 0 {
		d = 0
	}
	return d < handStartCutoff
}

// tokenMatches compares in constant time. The token is not a secret worth
// timing-hardening on its own — anything running as the user can read it — but
// the comparison is on the output path of every terminal pane, and a
// short-circuit here is free to avoid.
func (m handStartMarker) tokenMatches(bound string) bool {
	if bound == "" || m.Token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(m.Token), []byte(bound)) == 1
}

// Subcommands that are not an interactive session. A DENYLIST, so an unknown
// future subcommand is classed as a session and converted: the plugin's own
// binary then runs with the user's argv under hooks, which is harmless. The
// only misclassification possible is a new NON-interactive subcommand, which
// would run inside a pane of that agent's type and exit.
var handStartNonSession = map[string]map[string]bool{
	"claude": {
		"mcp": true, "setup-token": true, "doctor": true, "update": true,
		"install": true, "config": true, "auth": true, "login": true,
		"logout": true, "plugin": true, "plugins": true, "agents": true,
		"migrate-installer": true,
	},
	"codex": {
		"login": true, "logout": true, "exec": true, "apply": true,
		"mcp": true, "completion": true, "debug": true,
	},
	"opencode": {
		"auth": true, "run": true, "serve": true, "upgrade": true,
	},
}

// handStartSessionFlags are the flags that name a session to resume. More than
// one of them is contradictory and is refused rather than resolved.
var handStartSessionFlags = map[string]bool{
	"--resume": true, "-r": true, "--session-id": true,
	"--continue": true, "-c": true, "--session": true,
}

// classifyHandStart decides what to do about an authenticated marker.
//
// Every refusal answers "run": the user's command executes exactly as typed.
// That is the pre-feature behaviour, so a refusal can never be worse than not
// having the feature — which is the property that lets the denylist above be a
// denylist rather than an allowlist.
func classifyHandStart(p *plugin.PanePlugin, m handStartMarker, daemonEnv func(string) (string, bool)) handStartDecision {
	if p == nil {
		return handStartDecision{handStartRun, "no plugin for that command"}
	}
	if m.Oversize {
		return handStartDecision{handStartRun, "the command line was too long to classify"}
	}

	// A name set in the shell AND in the daemon's environment is assumed equal
	// — both came from the login environment. A name the daemon LACKS means the
	// respawn would behave differently: CLAUDE_CONFIG_DIR pointing at another
	// session store is the case that matters, and silently dropping it would
	// start the agent against the wrong data.
	for _, name := range m.EnvNames {
		if name == "" {
			continue
		}
		if _, ok := daemonEnv(name); !ok {
			return handStartDecision{handStartRun, "this shell sets " + name + ", which the daemon does not have"}
		}
	}

	deny := handStartNonSession[handStartBase(m.Name)]
	seen := 0
	for i, a := range m.Args {
		switch {
		case a == "--settings":
			// claudeHookSpawnPrep itself says what happens when the plugin's
			// args already carry one: which file claude honours is unverified,
			// so the hook may not be active. Converting would print a card
			// saying hooks are on, which could be false.
			return handStartDecision{handStartRun, "a typed --settings would contend with Quil's hook settings"}
		case handStartSessionFlags[a]:
			seen++
			if seen > 1 {
				return handStartDecision{handStartRun, "more than one session flag was given"}
			}
		case i == 0 && !strings.HasPrefix(a, "-") && deny[a]:
			return handStartDecision{handStartRun, a + " is not an interactive session"}
		case !handStartArgShapeOK(a):
			return handStartDecision{handStartRun, "an argument could not be validated"}
		}
	}
	return handStartDecision{handStartConvert, ""}
}

// handStartArgShapeOK refuses an argument that could not have been typed at an
// interactive prompt as a single word of a command line. The marker is
// authenticated but the token is not proof of a keyboard — anything running as
// the user holds it — so the argv it carries is shaped before it reaches a
// spawn, exactly as the MCP create path shapes its own.
func handStartArgShapeOK(a string) bool {
	if a == "" || len(a) > 512 {
		return false
	}
	for _, r := range a {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// daemonHasEnv is the production lookup classifyHandStart takes as a parameter
// so a test need not mutate the process environment.
func daemonHasEnv(name string) (string, bool) { return os.LookupEnv(name) }

// scanHandStart pulls complete markers out of a chunk of PTY output, answering
// them and the tail to retain.
//
// A marker split across two reads must not be missed, because it is ANSWERED:
// missing one costs the user a full second of dead air before their agent
// starts. The retained tail mirrors what scanMouseModes already does.
func scanHandStart(prev, data []byte) (payloads []string, tail []byte) {
	buf := data
	if len(prev) > 0 {
		buf = make([]byte, 0, len(prev)+len(data))
		buf = append(append(buf, prev...), data...)
	}
	for {
		i := bytes.Index(buf, []byte(handStartOSC))
		if i < 0 {
			// Retain only a possible partial introducer, never the whole chunk.
			if n := len(handStartOSC) - 1; len(buf) > n {
				buf = buf[len(buf)-n:]
			}
			return payloads, keepHandStartTail(buf)
		}
		buf = buf[i+len(handStartOSC):]
		end, width := handStartTerminator(buf)
		if end < 0 {
			// Retain the INTRODUCER with the partial payload. Keeping only the
			// payload would leave the next chunk with no marker to find, so a
			// launch split at exactly this byte would be missed — and a missed
			// marker is a second of dead air, because the shell is waiting for
			// an answer that never comes.
			return payloads, keepHandStartTail(append([]byte(handStartOSC), buf...))
		}
		payloads = append(payloads, string(buf[:end]))
		buf = buf[end+width:]
	}
}

// handStartTerminator finds ST or BEL, answering its offset and byte width.
func handStartTerminator(buf []byte) (int, int) {
	if i := bytes.Index(buf, []byte("\x1b\\")); i >= 0 {
		if j := bytes.IndexByte(buf, 0x07); j >= 0 && j < i {
			return j, 1
		}
		return i, 2
	}
	if j := bytes.IndexByte(buf, 0x07); j >= 0 {
		return j, 1
	}
	return -1, 0
}

// keepHandStartTail bounds what is carried into the next chunk. A pane emitting
// a lone ESC ] 7770 ; and then megabytes of output must not grow this without
// limit; dropping the tail loses at worst one interception, which costs the
// user a second.
func keepHandStartTail(buf []byte) []byte {
	if len(buf) > handStartMaxMarker {
		return nil
	}
	return append([]byte(nil), buf...)
}
