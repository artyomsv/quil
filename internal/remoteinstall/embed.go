package remoteinstall

import _ "embed"

// probeScript reports the remote host's platform and any existing install.
//
// Delivered on stdin (`ssh <dest> sh -s`). It needs no stdin of its own, so
// that is the simplest shape available and it sidesteps quoting entirely.
//
//go:embed scripts/remote-probe.sh
var probeScript string

// installScript installs quil from a release archive arriving on stdin.
//
// Delivered as an ARGUMENT to `sh -c`, not on stdin — the archive is there.
// That is why this one has to be quoted into the command string while the probe
// does not; see InstallCommand.
//
//go:embed scripts/remote-install.sh
var installScript string

// windowsProbeScript reports a Windows remote's platform and any existing
// install, run via EncodePowerShell rather than sent as-is: a Windows default
// ssh shell (cmd or PowerShell) has no way to receive a script on stdin the
// way `sh -s` does.
//
//go:embed scripts/remote-probe.ps1
var windowsProbeScript string

// windowsPrepareScript creates the install directory and a fresh staging
// directory under it, and prints both along with the host's own tar.exe.
// Windows PowerShell started by Win32-OpenSSH cannot read ssh stdin, so the
// archive cannot reach a PowerShell installer the way it reaches `sh -c`; the
// host's tar.exe reads it instead, into the directory this step made.
//
//go:embed scripts/remote-prepare.ps1
var windowsPrepareScript string

// windowsFinalizeScript verifies every extracted file against its own
// SHA-256 and swaps the set into place, renaming a running .exe aside rather
// than overwriting it — Windows refuses to overwrite a running image, but
// allows the rename.
//
//go:embed scripts/remote-finalize.ps1
var windowsFinalizeScript string
