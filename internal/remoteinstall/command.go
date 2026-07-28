package remoteinstall

// probeCommand is what runs on the far side for a probe.
//
// The script arrives on stdin, so nothing is interpolated and nothing needs
// quoting — which is why the probe and the install use different shapes.
const probeCommand = "sh -s"

// scriptArg0 is the $0 the install script runs under. `sh -c <script> <arg0>
// <args...>` consumes the word after the script as $0; without it the target
// directory would silently become $0 and the script would read the hash as $1.
const scriptArg0 = "quil-install"

// InstallCommand assembles the remote command for an install.
//
// The install script CANNOT travel on stdin — the archive is there — so it is
// passed as an argument to `sh -c` instead. ssh joins its command arguments
// with spaces and applies no quoting of its own, so the whole string is
// assembled and escaped here rather than left to ssh.
func InstallCommand(t Target, src Source) string {
	return "sh -c " + ShellSingleQuote(installScript) +
		" " + scriptArg0 +
		" " + ShellSingleQuote(t.Dir) +
		" " + ShellSingleQuote(src.SHA256)
}

// DaemonStopCommand asks an existing remote quil to stop its daemon before its
// binary is replaced.
//
// Addressed by absolute path for the same reason the attach command is: a
// non-interactive shell usually cannot see ~/.local/bin.
func DaemonStopCommand(binaryPath string) string {
	return ShellSingleQuote(binaryPath) + " daemon stop"
}
