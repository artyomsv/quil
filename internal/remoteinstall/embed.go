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
