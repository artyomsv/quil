package daemon

import (
	"crypto/rand"
	"encoding/base64"
	"sort"
	"strings"

	"github.com/artyomsv/quil/internal/plugin"
)

// handStartBase reduces a plugin's command to the word a user would type.
//
// It splits on BOTH separators rather than using filepath.Base, which honours
// only the running OS's. A plugin TOML carrying a Windows path is ordinary —
// it is a hand-edited file that travels — while a Unix binary whose filename
// genuinely contains a backslash is pathological. Deciding by the daemon's own
// GOOS would also mean the Windows shape is never exercised: CI is Linux, so a
// build-tagged answer here would be a rule nothing tests.
func handStartBase(cmd string) string {
	if i := strings.LastIndexAny(cmd, `/\`); i >= 0 {
		cmd = cmd[i+1:]
	}
	return strings.TrimSuffix(strings.ToLower(cmd), ".exe")
}

// handStartTargets answers the plugins whose binary, started by hand in a
// terminal pane's shell, Quil could open as a typed pane instead — keyed by the
// basename the user would actually type.
//
// The set is DERIVED from the spawn switch's three hook arms (daemon.go, the
// `p.Name == "opencode"` / `CodexPluginName` / `UsesClaudeSessions()` cases)
// rather than listed. A user's own plugin with `sessions = "claude"` is covered
// with no change here, which is the whole argument UsesClaudeSessions' doc
// records: derive the capability, do not enumerate the names.
//
// Unavailable plugins are excluded. Converting to a pane whose binary the
// detect command could not find would replace a working hand-started agent
// with a pane that fails to spawn.
func handStartTargets(plugins []*plugin.PanePlugin) map[string]*plugin.PanePlugin {
	if len(plugins) == 0 {
		return nil
	}
	out := map[string]*plugin.PanePlugin{}
	for _, p := range plugins {
		if p == nil || !p.Available || p.Command.Cmd == "" {
			continue
		}
		if !(p.Name == "opencode" || p.Name == plugin.CodexPluginName || p.UsesClaudeSessions()) {
			continue
		}
		// A plugin whose own pane runs a shell would intercept itself: the
		// converted pane's shell would arm the same function and fire again.
		// The loop is structurally impossible while this holds, and a test
		// asserts the intersection is empty.
		if p.Command.ShellIntegration {
			continue
		}
		name := handStartBase(p.Command.Cmd)
		if name == "" {
			continue
		}
		out[name] = p
	}
	return out
}

// handStartNames is handStartTargets' key list, sorted so the value the shell
// receives is stable across daemon restarts — an unstable QUIL_INTERCEPT would
// make every warm shell's environment differ for no reason.
func handStartNames(plugins []*plugin.PanePlugin) []string {
	targets := handStartTargets(plugins)
	names := make([]string, 0, len(targets))
	for n := range targets {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// newInterceptToken mints the value that authenticates one shell's markers.
//
// It is not proof of the user's keyboard — anything running as the user can
// read it, from the process environment, from the pane's ghost buffer, or from
// a raw output frame. What it does is bind a marker to THAT shell in THAT pane,
// so output arriving from anywhere else — an ssh remote printing into the pane,
// a pasted log, a web page rendered in a terminal browser — cannot trigger a
// conversion. The boundary is equivalent to the 0600 socket's, not stronger.
func newInterceptToken() string {
	var b [18]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Never arm on a weak token: an empty one fails the daemon's compare,
		// so the shell runs every agent as typed. That is the pre-feature
		// behaviour, which is the right answer to a broken CSPRNG.
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// handStartState is a pane's interception arming. Kept a struct rather than a
// bare string so the later parts of the feature (adoption, the one-shot record
// disown) have somewhere to live without touching Pane's layout again.
type handStartState struct {
	// token authenticates markers from this pane's shell. Empty disarms.
	token string
	// disownRecords makes the NEXT spawn treat this pane as owning no session
	// record, one shot. A converted pane has ptyGen > 0 because its SHELL ran,
	// which wrote nothing — but ownsRecord reads that as "a child of this pane
	// already wrote the record under its id", so a stale record from a
	// destroyed pane with a recycled id would be resumed on top of the id the
	// user actually typed.
	disownRecords bool
}
