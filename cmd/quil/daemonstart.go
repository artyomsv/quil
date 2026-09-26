package main

// daemonName is the daemon binary's name without extension: "quild", or the
// variant's name baked in by -X main.daemonBinary ("quild-dev", "quild-debug").
func daemonName() string {
	if daemonBinary != "" {
		return daemonBinary
	}
	return "quild"
}
