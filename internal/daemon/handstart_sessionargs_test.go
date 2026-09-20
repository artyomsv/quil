package daemon

import (
	"strings"
	"testing"
)

// A hand-started `claude --resume <id>` converts into InstanceArgs carrying
// that flag. Appending the plugin's own `--session-id {session_id}` on top
// produced an argv naming two different sessions, which claude refuses:
//
//	Error: --session-id can only be used with --continue or --resume if
//	--fork-session is also specified.
//
// The pane then died on its first spawn — the conversion "worked" and left the
// user worse off than not having the feature.
//
// The agent argument is the BINARY basename, not the plugin name: the flag map
// is keyed on what the user types.
func TestInstanceArgsNameSession(t *testing.T) {
	cases := []struct {
		name  string
		agent string
		args  []string
		want  bool
	}{
		{"the failing command", "claude", []string{"--resume", "2139fd1f", "--dangerously-skip-permissions", "--chrome"}, true},
		{"short resume", "claude", []string{"-r", "abc"}, true},
		{"explicit id", "claude", []string{"--session-id", "abc"}, true},
		{"continue", "claude", []string{"--continue"}, true},
		{"short continue", "claude", []string{"-c"}, true},
		{"opencode session", "opencode", []string{"--session", "abc"}, true},
		{"codex subcommand", "codex", []string{"resume", "abc"}, true},
		{"toggles only", "claude", []string{"--chrome", "--dangerously-skip-permissions"}, false},
		{"no args", "claude", nil, false},
		{"resume as a prompt word, not the subcommand", "claude", []string{"--chrome", "resume"}, false},

		// The collision the per-agent split exists for. `-c` is claude's
		// --continue and codex's CONFIG OVERRIDE. Reading `codex -c model=x` as
		// a session selector skipped the recorded `resume <id>` on restart and
		// started a new conversation.
		{"codex -c is a config override, not a session", "codex", []string{"-c", "model=gpt-5"}, false},
		{"codex resume still counts", "codex", []string{"resume", "abc", "-c", "model=gpt-5"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := instanceArgsNameSession(tc.agent, tc.args); got != tc.want {
				t.Fatalf("instanceArgsNameSession(%q, %v) = %v, want %v", tc.agent, tc.args, got, tc.want)
			}
		})
	}
}

// An unknown agent gets the union of every agent's selectors. Over-detecting
// only leaves the user's own argument in place; under-detecting appends a
// second selector and produces an invalid command line.
func TestInstanceArgsNameSession_UnknownAgentOverDetects(t *testing.T) {
	if !instanceArgsNameSession("something-else", []string{"--session", "abc"}) {
		t.Fatal("an unknown agent under-detected, which is the unsafe direction")
	}
}

// On restore the hook has recorded where the conversation actually went, so the
// typed id is stale and the recorded one is authoritative. Only the session
// selector goes: the toggles the user typed beside it are theirs.
func TestStripSessionArgs(t *testing.T) {
	cases := []struct {
		name  string
		agent string
		in    []string
		want  string
	}{
		{"the failing command", "claude",
			[]string{"--resume", "2139fd1f", "--dangerously-skip-permissions", "--chrome"},
			"--dangerously-skip-permissions --chrome"},
		{"continue takes no value", "claude", []string{"--continue", "--chrome"}, "--chrome"},
		{"codex subcommand", "codex", []string{"resume", "abc", "--search"}, "--search"},
		{"nothing to strip", "claude", []string{"--chrome"}, "--chrome"},
		// A dangling selector must not swallow the toggle after it.
		{"malformed resume", "claude", []string{"--resume", "--chrome"}, "--chrome"},
		{"everything stripped", "claude", []string{"--resume", "abc"}, ""},
		// The collision again, from the stripping side: removing codex's `-c`
		// would drop the flag and leave its value behind as a stray positional.
		{"codex -c survives", "codex", []string{"-c", "model=gpt-5"}, "-c model=gpt-5"},
		{"codex resume goes, config stays", "codex",
			[]string{"resume", "abc", "-c", "model=gpt-5"}, "-c model=gpt-5"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := strings.Join(stripSessionArgs(tc.agent, tc.in), " "); got != tc.want {
				t.Fatalf("stripSessionArgs(%q, %v) = %q, want %q", tc.agent, tc.in, got, tc.want)
			}
		})
	}
}
