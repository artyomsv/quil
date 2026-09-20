package daemon

import (
	"bytes"
	"crypto/subtle"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/logger"
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
//
// Taken from each agent's own `--help` output rather than guessed, and PER
// AGENT with no shared list. A word that is a subcommand for one tool is an
// ordinary prompt for another: `help` is a real codex subcommand, while
// `claude help` and `claude version` are neither — claude 2.1.x lists 22
// commands and neither word is among them, so both open a session and answer
// the word as the prompt it is. Measured, not inferred; an earlier revision of
// this list asserted that `claude help` printed usage, and it does not.
var handStartNonSession = map[string]map[string]bool{
	// claude-cli 2.1.x
	"claude": setOf("agents", "auth", "auto-mode", "config", "doctor",
		"gateway", "import", "install", "kill", "logs", "mcp", "migrate-installer",
		"plugin", "plugins", "project", "respawn", "rm", "setup-token", "stop",
		"ultrareview", "update", "upgrade"),
	// codex-cli 0.155.x. `resume` is absent deliberately: it IS a session.
	"codex": setOf("agents", "app", "app-server", "apply", "completion", "debug",
		"doctor", "exec", "help", "login", "logout", "mcp", "plugin",
		"remote-control", "review", "sandbox", "update"),
	// opencode. `attach` and `pr` are absent: both end in an interactive TUI.
	"opencode": setOf("acp", "agent", "auth", "completion", "debug", "export",
		"github", "import", "mcp", "models", "providers", "run", "serve", "stats",
		"uninstall", "upgrade", "web"),
}

func setOf(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}

// handStartNoConvert are interactive launches that must still run as typed.
//
// They are NOT "not a session" — saying so on the card would be a lie the user
// can check. `claude attach <id>` opens a background session in this terminal,
// which is as interactive as anything here; it is refused because the session
// it joins is one claude already owns elsewhere, so a respawn under Quil's own
// --settings would be a second client of a conversation the background agent
// is still driving. opencode's `attach` and `pr` end in a TUI and convert
// normally, which is why this is claude-only.
var handStartNoConvert = map[string]map[string]bool{
	"claude": setOf("attach"),
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

	base := handStartBase(m.Name)
	deny := handStartNonSession[base]
	noConvert := handStartNoConvert[base]
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
		case i == 0 && !strings.HasPrefix(a, "-") && noConvert[a]:
			return handStartDecision{handStartRun, a + " joins a session another agent already owns"}
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

// retirePaneSessionRecords deletes the agent session records filed under a
// pane's id, so the next spawn cannot resume a conversation that belongs to a
// pane that no longer exists.
//
// Conversion needs this because ownsRecord means "a child of THIS pane already
// ran, so it wrote whatever record sits under its id" — and a converting pane
// has ptyGen > 0 because its SHELL ran, which wrote nothing. Without retiring,
// a stale `codex-<id>.id` left by a destroyed pane whose id was recycled would
// be appended to the user's own `resume <id>`, producing argv naming two
// different sessions; and a stale claude `<id>.id` would REPLACE the id the
// user typed, resuming a conversation they did not ask for.
//
// Shares its file list with cleanupPaneArtifacts by construction: a record kind
// added there and missed here would be exactly the stale file this prevents.
func retirePaneSessionRecords(paneID string) {
	for _, name := range paneSessionRecordNames(paneID) {
		p := filepath.Join(config.SessionsDir(), name)
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("pane %s: retire stale session record %s: %v", paneID, name, err)
		}
	}
}

// paneSessionRecordNames is every per-pane session file the hook producers
// write. One list, two callers.
func paneSessionRecordNames(paneID string) []string {
	return []string{
		paneID + ".id",
		paneID + ".transcript",
		paneID + ".settings.json",
		"opencode-" + paneID + ".id",
		"codex-" + paneID + ".id",
	}
}

// convertAtLaunch reopens a terminal pane as the agent the user just typed.
//
// It runs SYNCHRONOUSLY on the output goroutine that read the marker, and that
// is load-bearing. The shell returns 0 the moment it sees the reply, and its
// prompt hooks immediately write OSC 133;D, 133;A and OSC 7 into the OLD PTY.
// Restarting here means those bytes arrive against a superseded generation and
// are dropped by the existing check; deferring the restart to another goroutine
// would let that D reach detectOSC133Exit and report a command completion for a
// command that never ran.
//
// The caller has already answered the shell. Nothing here writes to the pane.
func (d *Daemon) convertAtLaunch(pane *Pane, target *plugin.PanePlugin, m handStartMarker) bool {
	cwd := d.resolveHandStartCWD(pane, m)

	// A converting pane's own records were written by nothing — its shell wrote
	// no session — so any file under its id is stale by definition.
	retirePaneSessionRecords(pane.ID)

	pane.PluginMu.Lock()
	prevType := pane.Type
	pane.Type = target.Name
	pane.CWD = cwd
	// The user's argv REPLACES the plugin's own args, exactly as the create
	// dialog's toggles do — resolveSpawnArgs has always replaced rather than
	// merged, so this is the dialog's behaviour and not a new rule. With no
	// typed arguments the plugin's own args apply, which is what a bare
	// `claude` should mean.
	if len(m.Args) > 0 {
		pane.InstanceArgs = append([]string(nil), m.Args...)
	} else {
		pane.InstanceArgs = nil
	}
	// One shot: make the next spawn treat this pane as owning no record, since
	// the ptyGen that would otherwise say it does was raised by the shell.
	pane.handStart.disownRecords = true
	// The pane converted FROM a terminal, so a clean exit of the agent can put
	// the shell back rather than leaving a dead agent pane behind.
	pane.ConvertedFromTerminal = prevType
	// Disarm: the converted pane runs the agent directly, not a shell, so no
	// further marker can legitimately come from it.
	pane.handStart.token = ""
	pane.PluginMu.Unlock()

	log.Printf("pane %s: converting %s to %s (args=%v, cwd=%s)", pane.ID, prevType, target.Name, m.Args, cwd)

	if !d.restartPaneInPlace(pane) {
		// The pane is left as the agent type with its SpawnError showing, which
		// is the same state a failed Ctrl+N reaches and the same thing Alt+R
		// retries. Reverting to a terminal here would hide why it failed.
		log.Printf("pane %s: conversion spawn failed", pane.ID)
		return false
	}
	d.broadcastState()
	d.requestSnapshot()
	return true
}

// resolveHandStartCWD prefers the shell's own $PWD over the pane's recorded one.
//
// Pane.CWD is written by the TUI's OSC 7 handler, so a pane driven with no
// client attached — or one between attaches — carries a stale value, and
// spawning the agent there would silently start it in the wrong project. The
// shell's value is validated the same way any client-supplied path is.
func (d *Daemon) resolveHandStartCWD(pane *Pane, m handStartMarker) string {
	if m.CWD != "" {
		if resolved, err := filepath.EvalSymlinks(m.CWD); err == nil {
			if st, err := os.Stat(resolved); err == nil && st.IsDir() {
				return resolved
			}
		}
	}
	pane.PluginMu.Lock()
	defer pane.PluginMu.Unlock()
	return pane.CWD
}

// detectHandStart answers any authenticated marker in a chunk of PTY output.
//
// Every authenticated, fresh marker ends in EXACTLY ONE of: a "run" reply, a
// conversion, or silence because the pane is gone. Two answers would type eight
// stray bytes into whatever is running; none would cost the user a second of
// dead air. A test pins that discipline.
//
// Runs on the output goroutine for this pane, and conversion runs synchronously
// from here — see convertAtLaunch for why that ordering is load-bearing.
func (d *Daemon) detectHandStart(pane *Pane, paneID string, data []byte) {
	pane.PluginMu.Lock()
	bound := pane.handStart.token
	prev := pane.handStartTail
	pane.PluginMu.Unlock()
	// The introducer cannot appear without the feature having armed this pane,
	// so the common case costs one Index over the chunk and nothing else.
	if bound == "" && len(prev) == 0 {
		return
	}

	payloads, tail := scanHandStart(prev, data)
	pane.PluginMu.Lock()
	pane.handStartTail = tail
	pane.PluginMu.Unlock()
	if len(payloads) == 0 {
		return
	}

	arrival := time.Now()
	for _, payload := range payloads {
		m, ok := parseHandStart(payload)
		if !ok {
			logger.Debug("pane %s: unparseable hand-start marker (%d bytes)", paneID, len(payload))
			continue
		}
		logger.Debug("pane %s: hand-start marker name=%q args=%d cwd=%q", paneID, m.Name, len(m.Args), m.CWD)
		if !m.tokenMatches(bound) {
			// Output from somewhere else — an ssh remote printing into the
			// pane, a pasted log — cannot trigger a conversion, because it
			// never had this shell's token.
			//
			// Rate-limited rather than silent: a hostile or noisy source would
			// own the log file, but a mismatch that is never reported is
			// indistinguishable from no marker at all, and that blind spot
			// cost a live debugging session.
			d.logHandStartMismatch(pane, paneID, m.Name, bound)
			continue
		}
		if !m.fresh(time.Now(), arrival) {
			// The shell has already given up and run the binary. Answering now
			// would put eight characters into a live agent's composer.
			log.Printf("pane %s: hand-start marker for %q arrived too late to answer", paneID, m.Name)
			continue
		}
		d.answerHandStart(pane, paneID, m)
	}
}

// answerHandStart resolves one authenticated marker to its single outcome.
func (d *Daemon) answerHandStart(pane *Pane, paneID string, m handStartMarker) {
	policy := d.cfg.Agents.HandStartedPolicy()
	target := handStartTargets(d.registry.All())[handStartBase(m.Name)]

	decision := classifyHandStart(target, m, daemonHasEnv)
	if policy != config.HandStartedConvert {
		// adopt and notify both run the binary as typed; they differ only in
		// what happens afterwards, which is Part C's business, not this one's.
		decision = handStartDecision{handStartRun, "hand_started = " + policy}
	}

	if decision.Class == handStartConvert {
		// Tell the shell to step aside FIRST. It is waiting with a deadline,
		// and the spawn below can take longer than that deadline — a reply sent
		// after the restart would arrive at the agent, not the shell.
		if !d.replyHandStart(pane, handStartReplyConvert) {
			return
		}
		d.convertAtLaunch(pane, target, m)
		return
	}

	d.replyHandStart(pane, handStartReplyRun)
	if decision.Reason != "" {
		log.Printf("pane %s: running %s as typed — %s", paneID, m.Name, decision.Reason)
	}
	if target == nil || policy == config.HandStartedOff {
		// "off" means say nothing, and a command that is not one of Quil's
		// agents is not this feature's business.
		return
	}

	// The agent is running as typed. What Quil can still do depends on the
	// policy and on whether the agent keeps a session store a third party can
	// read — which, for now, only claude does.
	if policy == config.HandStartedAdopt || policy == config.HandStartedConvert {
		if target.UsesClaudeSessions() {
			d.adoptClaudeSession(pane, target, m)
			return
		}
	}
	d.emitHandStartCard(pane, "agent_untracked", "info",
		"Session not tracked",
		untrackedMessage(m.Name, decision.Reason))
}

// untrackedMessage says what happened and what to do, in that order. A card
// that only says something is wrong is the state #221 was already in.
func untrackedMessage(name, reason string) string {
	msg := name + " is running, but Quil is not tracking its session, so it will " +
		"not resume after a restart."
	if reason != "" {
		msg += " Reason: " + reason + "."
	}
	return msg + " Ctrl+N opens a pane of that type with full tracking."
}

// replyHandStart writes the eight-byte answer to the pane's child, answering
// whether it was accepted for delivery.
//
// Through EnqueueInput, the ordered per-pane writer every keystroke uses: a
// direct PTY write from this goroutine would block forever against a child that
// has stopped reading stdin, which is the wedge shape this daemon already has
// scars from.
func (d *Daemon) replyHandStart(pane *Pane, reply string) bool {
	return pane.EnqueueInput([]byte(reply))
}

// logHandStartMismatch reports an authenticated-looking marker whose token is
// not this pane's, at most once an hour per pane.
func (d *Daemon) logHandStartMismatch(pane *Pane, paneID, name, bound string) {
	const cooldown = time.Hour
	now := time.Now()
	pane.PluginMu.Lock()
	report := now.Sub(pane.handStartMismatchAt) >= cooldown
	if report {
		pane.handStartMismatchAt = now
	}
	pane.PluginMu.Unlock()
	if !report {
		return
	}
	// Neither token is printed. The bound one is a live credential for this
	// pane, and the marker's is whatever the writer chose.
	log.Printf("pane %s: hand-start marker for %q carried a token this pane does not hold (armed=%v) — ignoring",
		paneID, name, bound != "")
}
