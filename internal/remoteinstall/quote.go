package remoteinstall

import (
	"fmt"
	"strings"
)

// Remote shells a recorded binary can be quoted for. "" (POSIX) is the value
// every record made before Windows support carries, so it must stay POSIX.
const (
	ShellPOSIX      = ""
	ShellCmd        = "cmd"
	ShellPowerShell = "powershell"
)

// windowsRefused are characters no recorded Windows path may contain, for ANY
// Windows shell: quotes end a quoted string (PowerShell also treats U+2018–
// U+201B as quotes — internal/daemon/warmshell.go measured command execution
// from that), and %, ^, &, |, <, >, ! are cmd metacharacters (! when delayed
// expansion is on). Under a PowerShell default shell sshd wraps the command in
// -c "…", so " breaks that shell too.
const windowsRefused = "\"'\u2018\u2019\u201a\u201b%^&|<>!"

// CheckRemotePathWindows accepts an absolute drive path only. A UNC path is
// refused: recording it would make every attach an SMB fetch with implicit
// authentication.
func CheckRemotePathWindows(what, p string) error {
	if len(p) < 3 || p[1] != ':' || p[2] != '\\' ||
		!((p[0] >= 'A' && p[0] <= 'Z') || (p[0] >= 'a' && p[0] <= 'z')) {
		return fmt.Errorf("%s %q is not an absolute drive path (C:\\…)", what, p)
	}
	if i := strings.IndexAny(p, windowsRefused); i >= 0 {
		return fmt.Errorf("%s %q contains %q, which cannot be quoted safely for a Windows shell", what, p, p[i:i+1])
	}
	if i := strings.IndexFunc(p, isControl); i >= 0 {
		return fmt.Errorf("%s contains a control character at byte %d; refusing to use it", what, i)
	}
	return nil
}

// QuoteCommand builds the remote command that runs binary with fixed literal
// args under the host's default ssh shell. Only binary varies; args are
// literals like --stdio, so they are joined unquoted.
func QuoteCommand(shell, binary string, args ...string) (string, error) {
	tail := ""
	if len(args) > 0 {
		tail = " " + strings.Join(args, " ")
	}
	switch shell {
	case ShellPOSIX:
		return ShellSingleQuote(binary) + tail, nil
	case ShellCmd:
		if err := CheckRemotePathWindows("remote quil path", binary); err != nil {
			return "", err
		}
		return `"` + binary + `"` + tail, nil
	case ShellPowerShell:
		if err := CheckRemotePathWindows("remote quil path", binary); err != nil {
			return "", err
		}
		// The check already refused every quote; doubling stays as defence
		// in depth, with the same replacer warmshell.go uses.
		return "& '" + psQuotes.Replace(binary) + "'" + tail, nil
	default:
		return "", fmt.Errorf("unknown remote shell %q", shell)
	}
}

var psQuotes = strings.NewReplacer("'", "''", "\u2018", "\u2018\u2018", "\u2019", "\u2019\u2019", "\u201a", "\u201a\u201a", "\u201b", "\u201b\u201b")

// quoteArg quotes one argument for the host's default ssh shell. Unlike
// QuoteCommand's literal args, this one varies — the extract command's
// staging directory — so it is quoted rather than joined.
func quoteArg(shell, s string) (string, error) {
	switch shell {
	case ShellPOSIX:
		return ShellSingleQuote(s), nil
	case ShellCmd:
		if err := CheckRemotePathWindows("remote path", s); err != nil {
			return "", err
		}
		return `"` + s + `"`, nil
	case ShellPowerShell:
		if err := CheckRemotePathWindows("remote path", s); err != nil {
			return "", err
		}
		return "'" + psQuotes.Replace(s) + "'", nil
	default:
		return "", fmt.Errorf("unknown remote shell %q", shell)
	}
}
