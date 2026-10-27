package sbx

import (
	"bytes"
	"context"
	"fmt"
	"strings"
)

// What Quil shares with a sandbox so that claude's hooks can reach it.
//
// A hook is a program claude runs on each event; Quil's writes a line into a
// spool file the daemon reads. In a docker sandbox the daemon mounts a folder
// of its own for that. An sbx sandbox is a VM another tool may own, and Quil
// creates, mounts into and removes nothing of the USER's — but a folder Quil
// owns, shared at its own host path, widens nothing: the agent gains a
// place to write event lines, and the daemon reads it through os.Root.
//
// `sbx mount` applies to a RUNNING sandbox only (measured on 0.48: a stopped
// one answers 409), so the probe below runs first — it starts the sandbox as
// `sbx exec` always does — and reports which Linux build of the hook binary
// the VM can run.

// MountReadOnlyArgs is `sbx mount <name> <dir>:<dir>:ro`: the host folder at
// the same path inside the VM, read-only. The hook binary is shared this
// way: the agent runs it and must not be able to replace it.
func MountReadOnlyArgs(name, dir string) []string {
	return []string{"mount", name, dir + ":" + dir + ":ro"}
}

// MountReadOnly adds a host folder to the sandbox read-only, at the same
// path. Idempotent like Mount.
func (c Client) MountReadOnly(ctx context.Context, name, dir string) error {
	ctx, cancel := context.WithTimeout(ctx, mountTimeout)
	defer cancel()
	var buf bytes.Buffer
	if err := c.run(ctx, "", &buf, MountReadOnlyArgs(name, dir)...); err != nil {
		return fmt.Errorf("sbx mount %s (read-only): %w%s", dir, err, errorLine(buf.String()))
	}
	return nil
}

// ArchArgs asks the VM its machine type.
func ArchArgs(name string) []string {
	return []string{"exec", name, "uname", "-m"}
}

// GoArch maps `uname -m` output to the GOARCH of a release build, or false
// for a machine no release is built for.
func GoArch(uname string) (string, bool) {
	switch strings.TrimSpace(uname) {
	case "aarch64", "arm64":
		return "arm64", true
	case "x86_64", "amd64":
		return "amd64", true
	}
	return "", false
}

// Arch reports the GOARCH the sandbox's VM runs, starting the sandbox when
// it is stopped. A failed exec is an error; an unknown machine type is too,
// naming it.
func (c Client) Arch(ctx context.Context, name string) (string, error) {
	if !ValidName(name) {
		return "", fmt.Errorf("invalid sandbox name")
	}
	ctx, cancel := context.WithTimeout(ctx, attachProbeTimeout)
	defer cancel()
	var buf bytes.Buffer
	if err := c.run(ctx, "", &buf, ArchArgs(name)...); err != nil {
		return "", fmt.Errorf("sbx exec uname: %w%s", err, tail(buf.String()))
	}
	// The last non-empty line: a notice that sbx started the sandbox may
	// come first.
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	m := strings.TrimSpace(lines[len(lines)-1])
	arch, ok := GoArch(m)
	if !ok {
		return "", fmt.Errorf("sandbox %s runs on %q, for which no quil release is built", name, m)
	}
	return arch, nil
}
