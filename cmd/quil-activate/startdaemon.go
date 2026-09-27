// Untagged like args.go: argument parsing is logic, and logic lives where CI
// compiles it.
package main

import (
	"errors"
	"fmt"
)

// isStartDaemon reports whether this invocation is the logon task's windowless
// daemon start. It is argv[1] exactly: the registry's URI command always
// begins with --scheme, and a click-injected token can only follow %1, so no
// URI can reach this path.
func isStartDaemon(argv []string) bool {
	return len(argv) > 1 && argv[1] == "start-daemon"
}

// parseStartDaemonArgs reads `--quild <abs> --home <abs>`. Strict, unlike
// parseArgs: this command line is written by `quil daemon install-logon`
// alone, so anything unexpected is refused rather than degraded.
func parseStartDaemonArgs(args []string) (quild, home string, err error) {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--quild", "--home":
			if i+1 >= len(args) {
				return "", "", fmt.Errorf("%s needs a value", args[i])
			}
			if args[i] == "--quild" {
				quild = args[i+1]
			} else {
				home = args[i+1]
			}
			i++
		default:
			return "", "", fmt.Errorf("unexpected argument %q", args[i])
		}
	}
	if quild == "" || home == "" {
		return "", "", errors.New("both --quild and --home are required")
	}
	for what, p := range map[string]string{"quild": quild, "home": home} {
		if len(p) < 3 || p[1] != ':' || (p[2] != '\\' && p[2] != '/') {
			return "", "", fmt.Errorf("%s %q is not an absolute drive path", what, p)
		}
	}
	return quild, home, nil
}
