package winjob

import "strings"

// DaemonEnv builds the environment a spawned daemon — and so every pane it
// starts — inherits.
//
// QUIL_HOME is set only for a non-default home. Panes inherit the daemon's
// environment, and a production daemon carrying QUIL_HOME makes every `quil`
// run inside a pane believe it is a dev instance ([dev] in its status bar).
//
// lowered additionally drops SSH_CONNECTION, SSH_CLIENT and SSH_TTY: a daemon
// started from an ssh session outlives it, and pane children (git credential
// helpers, editors, clipboard tools) would otherwise believe they run inside
// ssh. Keys compare case-insensitively, as Windows environment names do.
func DaemonEnv(base []string, home string, isDefaultHome, lowered bool) []string {
	out := make([]string, 0, len(base)+1)
	for _, e := range base {
		switch envKey(e) {
		case "QUIL_HOME":
			continue
		case "SSH_CONNECTION", "SSH_CLIENT", "SSH_TTY":
			if lowered {
				continue
			}
		}
		out = append(out, e)
	}
	if !isDefaultHome {
		out = append(out, "QUIL_HOME="+home)
	}
	return out
}

// envKey is the upper-cased name of one KEY=VALUE entry.
func envKey(e string) string {
	k, _, _ := strings.Cut(e, "=")
	return strings.ToUpper(k)
}
