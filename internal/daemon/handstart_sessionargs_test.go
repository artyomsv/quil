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
func TestInstanceArgsNameSession(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"the failing command", []string{"--resume", "2139fd1f", "--dangerously-skip-permissions", "--chrome"}, true},
		{"short resume", []string{"-r", "abc"}, true},
		{"explicit id", []string{"--session-id", "abc"}, true},
		{"continue", []string{"--continue"}, true},
		{"short continue", []string{"-c"}, true},
		{"opencode session", []string{"--session", "abc"}, true},
		{"codex subcommand", []string{"resume", "abc"}, true},
		{"toggles only", []string{"--chrome", "--dangerously-skip-permissions"}, false},
		{"no args", nil, false},
		// "resume" as a prompt word, not the subcommand.
		{"resume later in the line", []string{"--chrome", "resume"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := instanceArgsNameSession(tc.args); got != tc.want {
				t.Fatalf("instanceArgsNameSession(%v) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}

// On restore the hook has recorded where the conversation actually went, so the
// typed id is stale and the recorded one is authoritative. Only the session
// selector goes: the toggles the user typed beside it are theirs.
func TestStripSessionArgs(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want string
	}{
		{"the failing command", []string{"--resume", "2139fd1f", "--dangerously-skip-permissions", "--chrome"},
			"--dangerously-skip-permissions --chrome"},
		{"continue takes no value", []string{"--continue", "--chrome"}, "--chrome"},
		{"codex subcommand", []string{"resume", "abc", "--search"}, "--search"},
		{"nothing to strip", []string{"--chrome"}, "--chrome"},
		// A dangling selector must not swallow the toggle after it.
		{"malformed resume", []string{"--resume", "--chrome"}, "--chrome"},
		{"everything stripped", []string{"--resume", "abc"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := strings.Join(stripSessionArgs(tc.in), " "); got != tc.want {
				t.Fatalf("stripSessionArgs(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
