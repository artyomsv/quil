# Remote Daemon Auto-Install Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `quil --remote <host>` provisions or upgrades the remote daemon over the SSH connection it already has, instead of failing with a manual multi-step chore.

**Architecture:** The laptop downloads the *remote* platform's release archive from GitHub, verifies its checksum locally, and streams it over one ssh call into a small embedded POSIX script that re-verifies and installs it. Detection keys on the ssh exit code (127 / 126), which requires `stdioConn` to reap its child for status. The resolved absolute remote path is persisted per-destination so later launches bypass PATH entirely.

**Tech Stack:** Go 1.25, stdlib only for the new package. Reuses `internal/update` (already parameterized by target GOOS/GOARCH) and `internal/transport`.

**Spec:** `docs/superpowers/specs/2026-07-28-remote-auto-install-design.md`

## Global Constraints

- **Never `sudo`, never escalate.** If a target directory is not writable by the connecting user, fall back to `~/.local/bin` and report the shadowing. A password prompt over a non-tty ssh channel cannot be answered.
- **Never install without explicit consent.** `--yes` exists for scripted use only. Prompts name host, resolved path, version, and (upgrade only) that the daemon stops.
- **Every ssh invocation goes through `internal/transport`,** which applies `forcedSSHOptions`, `livenessSSHOptions`, `ConnectTimeout`, and the leading-`-` destination rejection. A destination beginning with `-` is parsed by ssh as an option and `-oProxyCommand=` executes a local command (CVE-2017-1000117 class). No new code path may build its own `exec.Command("ssh", …)`.
- **Every value interpolated into a remote command string passes through `shellSingleQuote`.** ssh joins command args with spaces and applies no quoting of its own.
- **Verify the archive checksum locally before a byte is sent**, and again remotely after receipt.
- **Remote-controlled text reaching the terminal keeps `terminalSanitizer`.**
- **Supported remotes: linux/amd64, linux/arm64, darwin/amd64, darwin/arm64 only.** Anything else is an explicit refusal at probe time, never a guess.
- **Embedded shell scripts must be LF.** `.gitattributes` `*.sh text eol=lf` covers them by extension; a byte-level test is still required (this bug class has shipped from this repo before and is invisible from Windows).
- Go conventions per `~/.claude/rules/go-conventions.md`; tests per `~/.claude/rules/go-testing.md`. Tabs in Go, 2 spaces in TOML.
- Dev-environment isolation per `.claude/rules/dev-environment.md`: build with `./scripts/dev.sh`, never touch `~/.quil/`, never run `kill-daemon`/`reset-daemon`.

---

### Task 1: ssh exit-code capture in `stdioConn`

**Files:**
- Modify: `internal/transport/stdioconn.go`
- Test: `internal/transport/exitcode_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: `transport.LinkStatus` gains `ExitCode() int` (`-1` = not exited). Task 7 reads it after `Close()`.

- [ ] **Step 1: Write the failing test**

Uses the standard helper-process pattern so it is portable to Windows (no `sh -c 'exit 127'`).

```go
package transport

import (
	"os"
	"os/exec"
	"strconv"
	"testing"
)

// TestHelperExit is not a real test: it is re-executed as a child process by
// startHelperConn and exits with the code the parent asked for.
func TestHelperExit(t *testing.T) {
	code := os.Getenv("QUIL_HELPER_EXIT")
	if code == "" {
		return
	}
	n, _ := strconv.Atoi(code)
	os.Exit(n)
}

// startHelperConn wires a child process into a stdioConn the same way SSH()
// does, so the reap path under test is the production one.
func startHelperConn(t *testing.T, exitCode int) *stdioConn {
	t.Helper()
	childIn, parentWrite, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	parentRead, childOut, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestHelperExit")
	cmd.Env = append(os.Environ(), "QUIL_HELPER_EXIT="+strconv.Itoa(exitCode))
	cmd.Stdin = childIn
	cmd.Stdout = childOut
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	childIn.Close()
	childOut.Close()
	return newStdioConn(cmd, parentRead, parentWrite, "helper")
}

func TestStdioConn_ExitCode_ReportsChildStatus(t *testing.T) {
	tests := []struct {
		name string
		want int
	}{
		{"command not found", 127},
		{"not executable", 126},
		{"clean exit", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := startHelperConn(t, tt.want)
			// Close is what guarantees the child is reaped — the production
			// callers read ExitCode only after it.
			c.Close()
			if got := c.ExitCode(); got != tt.want {
				t.Errorf("ExitCode() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestStdioConn_ExitCode_UnknownBeforeReap(t *testing.T) {
	c := startHelperConn(t, 0)
	defer c.Close()
	// Not yet closed: the value may legitimately be -1 (still running) or the
	// real code (already reaped by pump). Only -1-or-0 is acceptable; anything
	// else means the field was never initialised.
	if got := c.ExitCode(); got != -1 && got != 0 {
		t.Errorf("ExitCode() = %d before Close, want -1 or 0", got)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `./scripts/dev.sh test`
Expected: FAIL — `c.ExitCode` undefined.

- [ ] **Step 3: Implement**

Add to the `stdioConn` struct, beside `closeOnce`:

```go
	// exitCode is the child's wait status once reaped, or noExitCode while it
	// is still running. Atomic rather than mu-guarded for the same reason as
	// bytesIn: the caller asks precisely when a read has just failed.
	exitCode atomic.Int32
	// reapOnce guards cmd.Wait, which must not be called twice. Both pump()
	// (on pipe EOF) and Close() reach it.
	reapOnce sync.Once
```

```go
// noExitCode marks a child that has not been reaped — still running, still
// connecting, or never started. Distinct from any real exit status.
const noExitCode = -1
```

In `newStdioConn`, before `go c.pump()`:

```go
	c.exitCode.Store(noExitCode)
```

New methods:

```go
// reap waits for the child and records its exit status. Idempotent: exec.Cmd.Wait
// panics on a second call, and both pump() and Close() reach here.
//
// Safe to call Wait from either goroutine because SSH() gives the command
// *os.File stdin/stdout rather than StdinPipe/StdoutPipe, so exec starts no
// copier goroutines and Wait closes none of the parent's descriptors.
func (c *stdioConn) reap() {
	c.reapOnce.Do(func() {
		if c.cmd == nil {
			return
		}
		// The error is the child's non-zero status, which is exactly what we
		// are here to record — not a failure of the wait itself.
		_ = c.cmd.Wait()
		if st := c.cmd.ProcessState; st != nil {
			c.exitCode.Store(int32(st.ExitCode()))
		}
	})
}

// ExitCode reports the child's exit status, or noExitCode if it has not been
// reaped. Read it AFTER Close(), which is what guarantees the reap; reading it
// earlier races a child that is still exiting and reports "still running" for
// one that has already failed.
//
// This is the inverse of LinkErr's requirement — that one must be read BEFORE
// Close, because Close unblocks pump via <-done, which can return without ever
// setting pumpErr.
func (c *stdioConn) ExitCode() int { return int(c.exitCode.Load()) }
```

In `pump()`, replace the single `defer close(c.readCh)` with:

```go
	// LIFO: close(readCh) runs FIRST, unparking any blocked reader, and only
	// then does reap() park this goroutine in Wait. Reversed, a child that
	// closed stdout without exiting would hold every reader blocked until
	// Close killed it.
	defer c.reap()
	defer close(c.readCh)
```

In `Close()`, replace the `Kill`/`Wait` pair with:

```go
		if c.cmd != nil && c.cmd.Process != nil {
			// Kill BEFORE reap. sync.Once.Do blocks the second caller until the
			// first returns, so if pump is already parked in Wait, reaping an
			// unkilled child would hang Close until ssh exited on its own.
			_ = c.cmd.Process.Kill()
			c.reap()
		}
```

Extend the `LinkStatus` interface with:

```go
	// ExitCode reports the child's exit status, or -1 if it has not exited.
	//
	// For an ssh child this separates a remote-side failure from ssh's own:
	// 255 is ssh itself (auth, host key, DNS, connect), 127 is the remote
	// shell failing to find the command, 126 is finding it and failing to
	// execute it. Valid only once the child is reaped — see the note on the
	// implementation about reading it after Close.
	ExitCode() int
```

- [ ] **Step 4: Run tests**

Run: `./scripts/dev.sh test` then `./scripts/dev.sh test-race`
Expected: PASS. The race detector is load-bearing here — two goroutines reach `reap`.

- [ ] **Step 5: Commit**

```bash
git add internal/transport/stdioconn.go internal/transport/exitcode_test.go
git commit -F - <<'EOF'
feat(transport): expose the ssh child's exit code on LinkStatus

Separates a remote-side failure from ssh's own: 255 is ssh (auth, host
key, DNS), 127 is the remote shell failing to find the command, 126 is
finding it and failing to execute it. Reaping is idempotent across pump
and Close, which both need it.
EOF
```

---

### Task 2: pure helpers in `internal/remoteinstall`

**Files:**
- Create: `internal/remoteinstall/shellquote.go`, `platform.go`, `probe.go`, `plan.go`, `classify.go`
- Test: `internal/remoteinstall/shellquote_test.go`, `platform_test.go`, `probe_test.go`, `plan_test.go`, `classify_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces, all consumed by Tasks 4, 6 and 7:
  - `func ShellSingleQuote(s string) string`
  - `type Platform struct{ GOOS, GOARCH string }`
  - `func PlatformFor(unameS, unameM string) (Platform, error)`
  - `type Probe struct { Home string; Platform Platform; ExistingPath string; ExistingDirWritable bool }`
  - `func ParseProbe(out string) (Probe, error)`
  - `type Target struct { Dir string; Shadowed string }`
  - `func PlanTarget(p Probe) Target`
  - `type Remedy int` with `RemedyNone`, `RemedyInstall`, `RemedyReinstall`
  - `func ClassifyExit(exitCode int, established bool) Remedy`

- [ ] **Step 1: Write the failing tests**

```go
package remoteinstall

import "testing"

func TestShellSingleQuote(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"plain", "/usr/local/bin", `'/usr/local/bin'`},
		{"space", "/opt/my apps/quil", `'/opt/my apps/quil'`},
		{"single quote", "/home/o'brien/bin", `'/home/o'\''brien/bin'`},
		{"semicolon", "/tmp; rm -rf /", `'/tmp; rm -rf /'`},
		{"dollar", "$HOME/bin", `'$HOME/bin'`},
		{"empty", "", `''`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ShellSingleQuote(tt.in); got != tt.want {
				t.Errorf("ShellSingleQuote(%q) = %s, want %s", tt.in, got, tt.want)
			}
		})
	}
}

func TestPlatformFor(t *testing.T) {
	tests := []struct {
		name, s, m string
		want       Platform
		wantErr    bool
	}{
		{"ubuntu x86", "Linux", "x86_64", Platform{"linux", "amd64"}, false},
		{"ubuntu arm", "Linux", "aarch64", Platform{"linux", "arm64"}, false},
		{"linux reports amd64", "Linux", "amd64", Platform{"linux", "amd64"}, false},
		{"apple silicon", "Darwin", "arm64", Platform{"darwin", "arm64"}, false},
		{"intel mac", "Darwin", "x86_64", Platform{"darwin", "amd64"}, false},
		{"lowercase", "linux", "X86_64", Platform{"linux", "amd64"}, false},
		{"32-bit pi", "Linux", "armv7l", Platform{}, true},
		{"32-bit x86", "Linux", "i686", Platform{}, true},
		{"freebsd", "FreeBSD", "amd64", Platform{}, true},
		{"windows", "MINGW64_NT-10.0", "x86_64", Platform{}, true},
		{"probe failed", "-", "-", Platform{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := PlatformFor(tt.s, tt.m)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseProbe(t *testing.T) {
	t.Run("fresh host", func(t *testing.T) {
		got, err := ParseProbe("/home/artyom\nLinux\nx86_64\n-\n-\n")
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		want := Probe{
			Home:     "/home/artyom",
			Platform: Platform{"linux", "amd64"},
		}
		if got != want {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})

	t.Run("existing writable install", func(t *testing.T) {
		got, err := ParseProbe("/root\nLinux\naarch64\n/usr/local/bin/quil\nrw\n")
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if got.ExistingPath != "/usr/local/bin/quil" || !got.ExistingDirWritable {
			t.Errorf("got %+v", got)
		}
	})

	t.Run("existing read-only install", func(t *testing.T) {
		got, err := ParseProbe("/home/u\nDarwin\narm64\n/usr/local/bin/quil\nro\n")
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if got.ExistingDirWritable {
			t.Error("ExistingDirWritable = true, want false")
		}
	})

	t.Run("trailing noise is tolerated", func(t *testing.T) {
		// A remote rc file may append a line to stdout despite our best
		// efforts; only the first five lines are contractual.
		if _, err := ParseProbe("/h\nLinux\nx86_64\n-\n-\nMOTD junk\n"); err != nil {
			t.Errorf("err = %v, want nil", err)
		}
	})

	t.Run("short output is an error", func(t *testing.T) {
		if _, err := ParseProbe("/h\nLinux\n"); err == nil {
			t.Error("err = nil, want error")
		}
	})

	t.Run("unsupported platform is an error", func(t *testing.T) {
		if _, err := ParseProbe("/h\nLinux\narmv7l\n-\n-\n"); err == nil {
			t.Error("err = nil, want error")
		}
	})
}

func TestPlanTarget(t *testing.T) {
	tests := []struct {
		name         string
		probe        Probe
		wantDir      string
		wantShadowed string
	}{
		{
			name:    "fresh install goes to ~/.local/bin",
			probe:   Probe{Home: "/home/a"},
			wantDir: "/home/a/.local/bin",
		},
		{
			name:    "writable existing install is replaced in place",
			probe:   Probe{Home: "/home/a", ExistingPath: "/usr/local/bin/quil", ExistingDirWritable: true},
			wantDir: "/usr/local/bin",
		},
		{
			name:         "read-only existing install falls back and reports shadowing",
			probe:        Probe{Home: "/home/a", ExistingPath: "/usr/local/bin/quil", ExistingDirWritable: false},
			wantDir:      "/home/a/.local/bin",
			wantShadowed: "/usr/local/bin/quil",
		},
		{
			name:    "existing install already in ~/.local/bin",
			probe:   Probe{Home: "/home/a", ExistingPath: "/home/a/.local/bin/quil", ExistingDirWritable: true},
			wantDir: "/home/a/.local/bin",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PlanTarget(tt.probe)
			if got.Dir != tt.wantDir {
				t.Errorf("Dir = %q, want %q", got.Dir, tt.wantDir)
			}
			if got.Shadowed != tt.wantShadowed {
				t.Errorf("Shadowed = %q, want %q", got.Shadowed, tt.wantShadowed)
			}
		})
	}
}

func TestClassifyExit(t *testing.T) {
	tests := []struct {
		name        string
		code        int
		established bool
		want        Remedy
	}{
		{"command not found", 127, false, RemedyInstall},
		{"exec format error", 126, false, RemedyReinstall},
		{"ssh own failure", 255, false, RemedyNone},
		{"still connecting", -1, false, RemedyNone},
		{"quil exited cleanly", 0, false, RemedyNone},
		{"bytes arrived, 127 is not ours", 127, true, RemedyNone},
		{"bytes arrived, 126 is not ours", 126, true, RemedyNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyExit(tt.code, tt.established); got != tt.want {
				t.Errorf("ClassifyExit(%d, %v) = %v, want %v", tt.code, tt.established, got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `./scripts/dev.sh test`
Expected: FAIL — package `remoteinstall` does not exist.

- [ ] **Step 3: Implement**

`shellquote.go`:

```go
// Package remoteinstall provisions the quil binaries on a remote host over an
// existing ssh connection: probe what the host is, fetch the matching release
// locally, verify it, and stream it into a small POSIX install script.
package remoteinstall

import "strings"

// ShellSingleQuote wraps s so a POSIX shell reads it as exactly one literal
// word.
//
// Required because ssh joins its command arguments with spaces and applies no
// quoting of its own: the remote shell re-splits whatever it receives. Single
// quotes suppress every form of expansion, so the only character needing care
// is ' itself — closed, escaped, reopened.
func ShellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
```

`platform.go`:

```go
import (
	"fmt"
	"strings"
)

// Platform is a remote host's Go build target.
type Platform struct {
	GOOS   string
	GOARCH string
}

func (p Platform) String() string { return p.GOOS + "/" + p.GOARCH }

// PlatformFor maps `uname -s` and `uname -m` onto a Go build target, accepting
// ONLY combinations quil publishes a release archive for.
//
// Anything else is an error rather than a nearest guess. Pushing a mismatched
// binary produces exit 126 or 127 from the remote shell — 127 being the same
// code as "not installed", which is what makes a wrong guess loop rather than
// fail. 32-bit ARM is the live case: a 64-bit-kernel Raspberry Pi OS reports
// aarch64 from uname -m while its loader is armhf.
func PlatformFor(unameS, unameM string) (Platform, error) {
	var goos string
	switch strings.ToLower(strings.TrimSpace(unameS)) {
	case "linux":
		goos = "linux"
	case "darwin":
		goos = "darwin"
	default:
		return Platform{}, fmt.Errorf("unsupported remote OS %q: quil publishes releases for Linux and macOS", unameS)
	}

	var goarch string
	switch strings.ToLower(strings.TrimSpace(unameM)) {
	case "x86_64", "amd64":
		goarch = "amd64"
	case "aarch64", "arm64":
		goarch = "arm64"
	default:
		return Platform{}, fmt.Errorf("unsupported remote architecture %q: quil publishes amd64 and arm64", unameM)
	}

	return Platform{GOOS: goos, GOARCH: goarch}, nil
}
```

`probe.go` — `ParseProbe` splits on `\n`, trims `\r` (defensive: a remote rc file could emit CRLF even though our own script is LF), requires at least 5 lines, calls `PlatformFor` on lines 2 and 3, treats `-` as absent for lines 4 and 5, and sets `ExistingDirWritable` only when line 5 is exactly `rw`. Extra trailing lines are ignored.

`plan.go`:

```go
// Target is where the binaries will be written on the remote host.
type Target struct {
	// Dir is the directory to install into.
	Dir string
	// Shadowed names an existing install that this one will take precedence
	// over, or "" when there is none. Non-empty only when an existing install
	// could not be replaced in place.
	Shadowed string
}

// PlanTarget picks the install directory.
//
// A fresh install goes to ~/.local/bin: no sudo, and the absolute path is
// persisted afterwards so the non-interactive PATH never has to contain it.
// An upgrade replaces the existing binary in place when its directory is
// writable, which keeps a manually installed /usr/local/bin copy authoritative
// instead of silently leaving two. When it is not writable we fall back rather
// than escalate — sudo cannot be answered over a non-tty ssh channel — and
// report the shadowing so the user is not left wondering which copy runs.
func PlanTarget(p Probe) Target {
	fallback := p.Home + "/.local/bin"
	if p.ExistingPath == "" {
		return Target{Dir: fallback}
	}
	if p.ExistingDirWritable {
		return Target{Dir: path.Dir(p.ExistingPath)}
	}
	return Target{Dir: fallback, Shadowed: p.ExistingPath}
}
```

Use `path` (not `path/filepath`): these are always remote POSIX paths, and `filepath` on a Windows build would split on `\`.

`classify.go` implements the table from the spec.

- [ ] **Step 4: Run tests**

Run: `./scripts/dev.sh test`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/remoteinstall/
git commit -F - <<'EOF'
feat(remoteinstall): pure helpers for remote provisioning

Shell quoting, uname mapping, probe parsing, install-target selection
and ssh exit-code classification. All pure and table-driven; the ssh and
filesystem work lands in a later commit.
EOF
```

---

### Task 3: embedded probe and install scripts

**Files:**
- Create: `internal/remoteinstall/scripts/remote-probe.sh`, `internal/remoteinstall/scripts/remote-install.sh`, `internal/remoteinstall/embed.go`
- Test: `internal/remoteinstall/embed_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `probeScript` and `installScript` (`string` package vars) for Task 4.

- [ ] **Step 1: Write the failing test**

```go
package remoteinstall

import (
	"strings"
	"testing"
)

// The scripts are go:embed'ed and executed by a POSIX shell. A CRLF checkout on
// a Windows build host bakes CR into the binary, and POSIX shells do not treat
// CR as whitespace — `fi\r` is not the `fi` keyword, so the parse dies with
// "syntax error near unexpected token". This exact bug shipped from this repo
// before (internal/shellinit) and is invisible when developing on Windows.
// .gitattributes carries `*.sh text eol=lf`; this asserts it actually held.
func TestEmbeddedScripts_AreLF(t *testing.T) {
	scripts := map[string]string{
		"remote-probe.sh":   probeScript,
		"remote-install.sh": installScript,
	}
	for name, body := range scripts {
		t.Run(name, func(t *testing.T) {
			if body == "" {
				t.Fatal("embedded script is empty")
			}
			if i := strings.IndexByte(body, '\r'); i >= 0 {
				t.Errorf("carriage return at byte %d — .gitattributes eol=lf did not hold", i)
			}
		})
	}
}

func TestEmbeddedScripts_NoShebangDependency(t *testing.T) {
	// Both scripts are executed via `sh`, never by exec'ing the file, so a
	// shebang would be dead weight — and `#!/bin/bash` would be a portability
	// bug on hosts without bash. Assert the first line is not a shebang.
	for name, body := range map[string]string{"probe": probeScript, "install": installScript} {
		t.Run(name, func(t *testing.T) {
			if strings.HasPrefix(body, "#!") {
				t.Error("script starts with a shebang; it is run via sh, not exec'd")
			}
		})
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `./scripts/dev.sh test`
Expected: FAIL — `probeScript` undefined.

- [ ] **Step 3: Write the scripts**

`internal/remoteinstall/scripts/remote-probe.sh` — reports the host, always exits 0:

```sh
# Reports what this host is and whether quil is already installed.
#
# ALWAYS exits 0. A non-zero exit from this command must mean the ssh
# connection failed, not that the probe found nothing — the caller uses the
# exit code to tell those apart.
#
# Output contract: exactly five lines, in order.
#   1  $HOME
#   2  uname -s          (or -)
#   3  uname -m          (or -)
#   4  existing quil path (or -)
#   5  rw | ro | -       (is that path's directory writable)
h=${HOME:-}
printf '%s\n' "$h"
uname -s 2>/dev/null || printf '%s\n' -
uname -m 2>/dev/null || printf '%s\n' -

# Absolute paths FIRST. `command -v quil` alone would miss both of these: this
# runs in a non-interactive shell, which on Debian/Ubuntu returns from
# ~/.bashrc before any PATH line, so ~/.local/bin is usually invisible — the
# very problem this feature exists to solve.
found=
for p in "$h/.local/bin/quil" /usr/local/bin/quil; do
  if [ -x "$p" ]; then
    found=$p
    break
  fi
done
if [ -z "$found" ]; then
  c=$(command -v quil 2>/dev/null)
  if [ -n "$c" ]; then
    found=$c
  fi
fi

if [ -z "$found" ]; then
  printf '%s\n%s\n' - -
else
  printf '%s\n' "$found"
  d=$(dirname "$found")
  if [ -w "$d" ]; then
    printf 'rw\n'
  else
    printf 'ro\n'
  fi
fi
exit 0
```

`internal/remoteinstall/scripts/remote-install.sh` — installs from an archive on stdin:

```sh
# Installs quil from a release archive delivered on stdin.
#
#   $1  target directory
#   $2  expected sha256 of the archive
#
# The archive is on stdin, so this script cannot also arrive there — the caller
# passes it as an argument to `sh -c`.
set -eu
DIR=$1
WANT=$2

TMP=$(mktemp -d)
QT=
DT=
# Removes the staging temp files too: they live inside DIR (so the rename below
# never crosses a filesystem) and would otherwise be left behind on abort.
trap 'rm -rf "$TMP"; [ -z "$QT" ] || rm -f "$QT"; [ -z "$DT" ] || rm -f "$DT"; true' EXIT

cat > "$TMP/quil.tar.gz"

# Re-verify what actually arrived. The caller already checked this archive
# against the release checksums before sending, so a mismatch here means the
# transfer was truncated or corrupted in flight.
GOT=$({ sha256sum "$TMP/quil.tar.gz" 2>/dev/null || shasum -a 256 "$TMP/quil.tar.gz"; } | awk '{print $1}')
if [ "$GOT" != "$WANT" ]; then
  echo "quil-install: archive checksum mismatch (transfer corrupted)" >&2
  exit 2
fi

tar -xzf "$TMP/quil.tar.gz" -C "$TMP"
mkdir -p "$DIR"

# Stage inside DIR, then rename. Each binary lands on a NEW inode: overwriting
# in place with cp reuses the inode, and macOS caches code-signing information
# per vnode — a stale entry makes the kernel SIGKILL the new binary at exec
# time ("Code Signature Invalid"). Same reasoning as scripts/install.sh.
#
# Renaming over a RUNNING binary is safe on Unix: the running process keeps its
# inode. That is what lets an upgrade replace quild while the daemon is still
# shutting down.
QT=$(mktemp "$DIR/.quil.tmp.XXXXXX")
DT=$(mktemp "$DIR/.quild.tmp.XXXXXX")
cp "$TMP/quil" "$QT"
cp "$TMP/quild" "$DT"
chmod 755 "$QT" "$DT"
mv -f "$QT" "$DIR/quil"
mv -f "$DT" "$DIR/quild"
QT=
DT=

echo "quil-install: installed to $DIR"
```

`internal/remoteinstall/embed.go`:

```go
package remoteinstall

import _ "embed"

// probeScript reports the remote host's platform and any existing install.
// Run via `ssh <dest> sh -s` with the script on stdin — it needs no stdin of
// its own, so that is the simplest shape and avoids quoting entirely.
//
//go:embed scripts/remote-probe.sh
var probeScript string

// installScript installs quil from a release archive on stdin.
//
// Passed as an ARGUMENT to `sh -c` rather than on stdin, because stdin is
// carrying the archive. See Command() for the assembled form.
//
//go:embed scripts/remote-install.sh
var installScript string
```

- [ ] **Step 4: Run tests**

Run: `./scripts/dev.sh test`
Expected: PASS.

Then verify the LF guard really held in git, not just in the working tree:

Run: `git check-attr text eol -- internal/remoteinstall/scripts/remote-install.sh`
Expected: `text: set`, `eol: lf`

- [ ] **Step 5: Commit**

```bash
git add internal/remoteinstall/scripts/ internal/remoteinstall/embed.go internal/remoteinstall/embed_test.go
git commit -F - <<'EOF'
feat(remoteinstall): embed the remote probe and install scripts

The probe reports platform and any existing install by absolute path,
since a non-interactive shell usually cannot see ~/.local/bin. The
installer verifies the archive it received and stages each binary onto a
new inode before renaming, matching scripts/install.sh.
EOF
```

---

### Task 4: orchestration — probe, fetch, push

**Files:**
- Create: `internal/remoteinstall/install.go`, `internal/remoteinstall/command.go`
- Create: `internal/transport/run.go`
- Test: `internal/remoteinstall/install_test.go`, `internal/remoteinstall/command_test.go`

**Interfaces:**
- Consumes: Task 2's helpers, Task 3's `probeScript`/`installScript`.
- Produces, consumed by Task 6:
  - `type Runner interface { Run(ctx context.Context, command string, stdin io.Reader, stdout, stderr io.Writer) (int, error) }`
  - `func RunProbe(ctx context.Context, r Runner) (Probe, error)`
  - `type Source struct { Version string; Archive []byte; SHA256 string }`
  - `func FetchRelease(ctx context.Context, version string, p Platform) (Source, error)`
  - `func PackDir(dir string, p Platform) (Source, error)`
  - `func Push(ctx context.Context, r Runner, t Target, src Source) error`
  - `func InstallCommand(t Target, src Source) string`
- `transport.RunSSH(ctx context.Context, dest string, opts SSHOptions, command string, stdin io.Reader, stdout, stderr io.Writer) (int, error)` satisfies `Runner` via a closure in Task 6.

- [ ] **Step 1: Write the failing tests**

```go
package remoteinstall

import (
	"context"
	"io"
	"strings"
	"testing"
)

// fakeRunner records what it was asked to run and replays a canned result.
type fakeRunner struct {
	gotCommand string
	gotStdin   []byte
	stdout     string
	exitCode   int
	err        error
}

func (f *fakeRunner) Run(_ context.Context, command string, stdin io.Reader, stdout, _ io.Writer) (int, error) {
	f.gotCommand = command
	if stdin != nil {
		f.gotStdin, _ = io.ReadAll(stdin)
	}
	if stdout != nil {
		io.WriteString(stdout, f.stdout)
	}
	return f.exitCode, f.err
}

func TestRunProbe_ParsesRemoteReport(t *testing.T) {
	r := &fakeRunner{stdout: "/home/a\nLinux\nx86_64\n-\n-\n"}
	got, err := RunProbe(context.Background(), r)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got.Platform != (Platform{"linux", "amd64"}) {
		t.Errorf("Platform = %+v", got.Platform)
	}
	// The probe travels on stdin, so the command must be the bare `sh -s`.
	if got, want := r.gotCommand, "sh -s"; got != want {
		t.Errorf("command = %q, want %q", got, want)
	}
	if !strings.Contains(string(r.gotStdin), "uname -s") {
		t.Error("probe script was not sent on stdin")
	}
}

func TestRunProbe_NonZeroExitIsAConnectionError(t *testing.T) {
	// The probe script always exits 0, so any non-zero code came from ssh.
	r := &fakeRunner{exitCode: 255}
	if _, err := RunProbe(context.Background(), r); err == nil {
		t.Fatal("err = nil, want error")
	}
}

func TestInstallCommand_QuotesEveryInterpolatedValue(t *testing.T) {
	tgt := Target{Dir: "/home/o'brien/.local/bin"}
	src := Source{SHA256: "abc123"}
	cmd := InstallCommand(tgt, src)

	// The script is an argument, not stdin — stdin carries the archive.
	if !strings.HasPrefix(cmd, "sh -c '") {
		t.Errorf("command does not pass the script to sh -c: %q", cmd)
	}
	// The apostrophe must be escaped, or the remote shell re-splits the word.
	if !strings.Contains(cmd, `'\''brien`) {
		t.Errorf("target dir was not single-quote escaped: %q", cmd)
	}
	if !strings.Contains(cmd, "'abc123'") {
		t.Errorf("hash was not quoted: %q", cmd)
	}
}

func TestPush_SendsArchiveOnStdin(t *testing.T) {
	r := &fakeRunner{}
	src := Source{Archive: []byte("ARCHIVE-BYTES"), SHA256: "deadbeef"}
	if err := Push(context.Background(), r, Target{Dir: "/opt/bin"}, src); err != nil {
		t.Fatalf("err = %v", err)
	}
	if string(r.gotStdin) != "ARCHIVE-BYTES" {
		t.Errorf("stdin = %q, want the archive bytes", r.gotStdin)
	}
}

func TestPush_ReportsRemoteFailure(t *testing.T) {
	// Exit 2 is the install script's checksum-mismatch code.
	r := &fakeRunner{exitCode: 2}
	err := Push(context.Background(), r, Target{Dir: "/opt/bin"}, Source{SHA256: "x"})
	if err == nil {
		t.Fatal("err = nil, want error")
	}
}

func TestPackDir_RejectsMissingBinary(t *testing.T) {
	dir := t.TempDir() // contains neither quil nor quild
	if _, err := PackDir(dir, Platform{"linux", "amd64"}); err == nil {
		t.Fatal("err = nil, want error naming the missing binary")
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `./scripts/dev.sh test`
Expected: FAIL — `RunProbe` undefined.

- [ ] **Step 3: Implement**

`command.go` assembles the remote command strings:

```go
// probeCommand is what runs on the far side for a probe. The script arrives on
// stdin, so nothing is interpolated and nothing needs quoting.
const probeCommand = "sh -s"

// InstallCommand assembles the remote command for an install.
//
// The install script CANNOT travel on stdin — the archive is there — so it is
// passed as an argument to `sh -c` instead. ssh joins its command arguments
// with spaces and applies no quoting of its own, so the whole string is
// assembled and escaped here.
//
// The trailing "quil-install" is $0 for the script, which is what `sh -c`
// consumes before positional arguments begin; without it the target directory
// would silently become $0 and the script would read the hash as $1.
func InstallCommand(t Target, src Source) string {
	return "sh -c " + ShellSingleQuote(installScript) +
		" quil-install " + ShellSingleQuote(t.Dir) +
		" " + ShellSingleQuote(src.SHA256)
}
```

`install.go`:
- `RunProbe` writes `probeScript` to the runner's stdin with `probeCommand`, requires exit 0 (the script always exits 0, so anything else is ssh), and hands stdout to `ParseProbe`.
- `FetchRelease` uses `update.Checker`/`update.FindAssets(rel, p.GOOS, p.GOARCH)` to locate the archive and `checksums.txt`, downloads both, verifies sha256, and returns the bytes plus the verified hash. When `version` is empty it uses the latest release; otherwise it resolves that exact tag.
- `PackDir` reads `quil` and `quild` from a local directory, errors naming whichever is missing, and produces a tar.gz plus its sha256 — so the remote script's verify step is identical for `--from-dir`.
- `Push` runs `InstallCommand` with `bytes.NewReader(src.Archive)` as stdin, and turns a non-zero exit into an error carrying the sanitized remote stderr.

Every error message names the host-side cause, not just the exit code.

`internal/transport/run.go`:

```go
// RunSSH executes one command on a remote host and returns its exit status.
//
// Shares sshArgs with SSH(), so every hardening option, the ConnectTimeout,
// and the leading-'-' destination rejection apply identically. That sharing is
// the point: a second ssh call site that built its own argument vector would
// silently drop the guard against a destination like "-oProxyCommand=...",
// which executes a local command before any network traffic.
//
// Unlike SSH() this is synchronous and reaps the child itself — callers want a
// completed command with an exit status, not a live connection.
func RunSSH(ctx context.Context, dest string, opts SSHOptions, command string,
	stdin io.Reader, stdout, stderr io.Writer) (int, error)
```

It must reject a `dest` beginning with `-` before doing anything else, resolve `ssh` via `exec.LookPath`, set `opts.RemoteCommand = command`, and return `exitErr.ExitCode()` for a non-zero exit rather than treating it as a failure. Wrap `stderr` in `&terminalSanitizer{w: …}` when the caller passes a terminal-bound writer — document that the caller owns that choice.

- [ ] **Step 4: Run tests**

Run: `./scripts/dev.sh test` and `./scripts/dev.sh vet`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/remoteinstall/ internal/transport/run.go
git commit -F - <<'EOF'
feat(remoteinstall): probe, fetch and push over ssh

Probe travels on stdin; the install script travels as an sh -c argument
because stdin carries the archive. Both ssh call sites share sshArgs so
the hardening options and the leading-dash destination rejection cannot
diverge.
EOF
```

---

### Task 5: persist the resolved remote binary path

**Files:**
- Modify: `internal/config/config.go`
- Modify: `cmd/quil/main.go` (remote dial builds `SSHOptions.RemoteCommand` from config)
- Test: `internal/config/remote_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `config.RemoteHost{Binary string}`, `Config.Remote.Hosts map[string]RemoteHost`, and `func (c *Config) SetRemoteBinary(dest, binary string)`. Task 6 calls the setter; `main.go` reads the map.

- [ ] **Step 1: Write the failing test**

```go
func TestConfig_SetRemoteBinary_InitialisesMap(t *testing.T) {
	var cfg Config // zero value: Hosts is nil
	cfg.SetRemoteBinary("gpu01", "/home/a/.local/bin/quil")
	if got := cfg.Remote.Hosts["gpu01"].Binary; got != "/home/a/.local/bin/quil" {
		t.Errorf("Binary = %q", got)
	}
}

func TestConfig_SetRemoteBinary_Overwrites(t *testing.T) {
	var cfg Config
	cfg.SetRemoteBinary("gpu01", "/usr/local/bin/quil")
	cfg.SetRemoteBinary("gpu01", "/home/a/.local/bin/quil")
	if n := len(cfg.Remote.Hosts); n != 1 {
		t.Errorf("Hosts has %d entries, want 1", n)
	}
}

func TestConfig_RemoteHosts_RoundTrip(t *testing.T) {
	// A destination is an arbitrary user string (an ssh_config Host alias),
	// so it must survive a TOML key round-trip including dots and dashes.
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	cfg := Default()
	cfg.SetRemoteBinary("gpu-01.lan", "/opt/bin/quil")
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Remote.Hosts["gpu-01.lan"].Binary != "/opt/bin/quil" {
		t.Errorf("round trip lost the entry: %+v", got.Remote)
	}
}
```

Signatures confirmed against `internal/config/config.go`: `Default() Config`,
`Load(path string) (Config, error)`, `Save(path string, cfg Config) error`.
`SetRemoteBinary` is a pointer method, which is why the test assigns `Default()`
to an addressable variable before calling it.

- [ ] **Step 2: Run to verify it fails**

Run: `./scripts/dev.sh test`
Expected: FAIL — `SetRemoteBinary` undefined.

- [ ] **Step 3: Implement**

```go
// RemoteHost pins how to reach quil on one --remote destination.
type RemoteHost struct {
	// Binary is the absolute path to quil on that host, as resolved by
	// `quil remote setup`. Used verbatim as the ssh remote command, which is
	// what lets it work when the non-interactive PATH cannot see the install
	// directory — the usual case for ~/.local/bin on Debian and Ubuntu.
	Binary string `toml:"binary"`
}

// RemoteConfig holds per-destination remote-daemon settings, keyed by the
// --remote destination string exactly as the user types it.
type RemoteConfig struct {
	Hosts map[string]RemoteHost `toml:"hosts"`
}
```

Add `Remote RemoteConfig \`toml:"remote"\`` to `Config`, and:

```go
// SetRemoteBinary records where quil lives on dest, creating the map on first
// use so callers need not care whether the config predates this section.
func (c *Config) SetRemoteBinary(dest, binary string) {
	if c.Remote.Hosts == nil {
		c.Remote.Hosts = make(map[string]RemoteHost)
	}
	c.Remote.Hosts[dest] = RemoteHost{Binary: binary}
}
```

In `main.go`'s remote dial, build the options:

```go
	opts := transport.SSHOptions{}
	if h, ok := cfg.Remote.Hosts[remoteDest]; ok && h.Binary != "" {
		// Absolute path rather than PATH lookup: `ssh host quil --stdio` runs a
		// non-interactive shell, which on Debian/Ubuntu returns from ~/.bashrc
		// before any PATH line, so ~/.local/bin is invisible there.
		opts.RemoteCommand = remoteinstall.ShellSingleQuote(h.Binary) + " --stdio"
	}
	dialSSH := transport.SSH(remoteDest, opts)
```

- [ ] **Step 4: Run tests**

Run: `./scripts/dev.sh test`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config/ cmd/quil/main.go
git commit -F - <<'EOF'
feat(config): remember the remote quil path per destination

`ssh host quil --stdio` runs a non-interactive shell, which on Debian
and Ubuntu cannot see ~/.local/bin. Recording the absolute path and
using it as the remote command removes PATH from the equation.
EOF
```

---

### Task 6: `quil remote setup` subcommand

**Files:**
- Create: `cmd/quil/remote_setup.go`
- Modify: `cmd/quil/main.go` (subcommand dispatch)
- Test: `cmd/quil/remote_setup_test.go`

**Interfaces:**
- Consumes: Tasks 2, 4, 5.
- Produces, consumed by Task 7:
  - `func runRemoteSetup(dest string, opts setupOptions) error`
  - `type setupOptions struct { FromDir, Version string; Yes bool; Upgrade bool }`
  - `func offerRemoteInstall(dest string, remedy remoteinstall.Remedy) bool` — prompts, installs, returns whether a retry is warranted.
  - `var remoteInstallAttempted bool` — the loop guard.

- [ ] **Step 1: Write the failing test**

```go
func TestRunRemoteSetup_RejectsDashDestination(t *testing.T) {
	// Same guard as parseRemoteFlag: ssh parses a leading '-' as an option, and
	// -oProxyCommand= executes a local command.
	err := runRemoteSetup("-oProxyCommand=touch /tmp/pwned", setupOptions{Yes: true})
	if err == nil {
		t.Fatal("err = nil, want rejection")
	}
}

func TestRunRemoteSetup_RefusesDevBuildWithoutSource(t *testing.T) {
	// A dev build has no matching GitHub release; installing "latest" would
	// produce a version the gate then rejects. Must refuse and point at the
	// two escape hatches rather than guess.
	restore := setIsReleaseForTest(false)
	defer restore()

	err := runRemoteSetup("gpu01", setupOptions{Yes: true})
	if err == nil {
		t.Fatal("err = nil, want refusal")
	}
	for _, want := range []string{"--from-dir", "--version"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %s: %v", want, err)
		}
	}
}

func TestOfferRemoteInstall_LoopGuard(t *testing.T) {
	// A binary that will not execute reports 127 — the same code as "not
	// installed". Without the guard the launch path would install, retry, see
	// 127 again, and offer forever.
	remoteInstallAttempted = true
	defer func() { remoteInstallAttempted = false }()

	if offerRemoteInstall("gpu01", remoteinstall.RemedyInstall) {
		t.Error("offered a second install in the same process")
	}
}

func TestOfferRemoteInstall_IgnoresRemedyNone(t *testing.T) {
	if offerRemoteInstall("gpu01", remoteinstall.RemedyNone) {
		t.Error("offered an install for a non-install failure")
	}
}
```

`setIsReleaseForTest` is a new seam in this file (a swappable `isReleaseFn = versionpkg.IsRelease`), matching the existing `stopDaemonForUpgradeFn` pattern.

- [ ] **Step 2: Run to verify it fails**

Run: `./scripts/dev.sh test`
Expected: FAIL — `runRemoteSetup` undefined.

- [ ] **Step 3: Implement**

`runRemoteSetup` sequence:
1. `validateRemoteDest(dest)` — reuse the existing function, do not reimplement.
2. Resolve the source: `--from-dir` → `PackDir`; else require `isReleaseFn()` or an explicit `--version`, then `FetchRelease`.
3. `RunProbe` via a `Runner` closure over `transport.RunSSH(ctx, dest, transport.SSHOptions{}, …)`. Non-batch, so ssh can still prompt for a passphrase; stderr wrapped in the sanitizer.
4. `PlanTarget(probe)`.
5. Print the consent block and read `[y/N]` unless `--yes`. Upgrade adds a second line naming the daemon stop.
6. Upgrade only: run `<existingPath> daemon stop` over ssh, ignoring a non-zero exit (a daemon that was not running is not an error).
7. `Push`.
8. `cfg.SetRemoteBinary(dest, target.Dir+"/quil")` and save the config.
9. Report the installed path, and the shadowed path when `Target.Shadowed` is non-empty.

`offerRemoteInstall` returns false immediately for `RemedyNone` or when `remoteInstallAttempted` is already set; otherwise it prints the situation, calls `runRemoteSetup`, sets the guard, and returns whether the install succeeded.

Dispatch in `main.go`'s subcommand switch, beside `case "daemon"`:

```go
		case "remote":
			// `quil remote setup <dest>` — distinct from the `--remote <dest>`
			// FLAG, which parseRemoteFlag has already stripped from os.Args by
			// this point. Combining the two is nonsense and is rejected.
			handleRemote()
			return
```

`handleRemote` parses `setup` plus `--from-dir`, `--version`, `--yes`, rejects `remoteMode()`, and prints usage for anything else.

- [ ] **Step 4: Run tests**

Run: `./scripts/dev.sh test` and `./scripts/dev.sh vet`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/quil/remote_setup.go cmd/quil/main.go cmd/quil/remote_setup_test.go
git commit -F - <<'EOF'
feat(cli): add `quil remote setup <host>`

Probes the host, fetches the matching release for its platform, verifies
it locally, and pushes it over one ssh call. --from-dir pushes locally
built binaries, which is the only path available to a dev build since it
has no matching release.
EOF
```

---

### Task 7: offer the install from a failed launch

**Files:**
- Modify: `cmd/quil/version_gate.go`, `cmd/quil/main.go`, `cmd/quil/remote.go`
- Test: `cmd/quil/version_gate_remote_test.go` (extend the existing file)

**Interfaces:**
- Consumes: Tasks 1, 2, 6.
- Produces: no new exports; wires the pieces together.

- [ ] **Step 1: Write the failing test**

```go
func TestGateVersionCheck_OffersInstallOnExit127(t *testing.T) {
	var offered remoteinstall.Remedy
	restore := stubOfferRemoteInstall(func(_ string, r remoteinstall.Remedy) bool {
		offered = r
		return false // decline, so the gate falls through to the report
	})
	defer restore()

	remoteDest = "gpu01"
	defer func() { remoteDest = "" }()
	remoteLinkEstablishedFn = func() bool { return false }
	remoteLinkErrFn = func() error { return errors.New("gpu01: command not found") }
	remoteExitCodeFn = func() int { return 127 }
	defer resetRemoteSeams()

	// … run the gate with a stub client and a stub exitFn …

	if offered != remoteinstall.RemedyInstall {
		t.Errorf("remedy = %v, want RemedyInstall", offered)
	}
}

func TestGateVersionCheck_DoesNotOfferOnSSHFailure(t *testing.T) {
	// Exit 255 is ssh's own failure — auth, host key, DNS. Nothing to install.
	// … same harness, remoteExitCodeFn returns 255 …
	if offered != remoteinstall.RemedyNone {
		t.Errorf("offered an install for an ssh-level failure: %v", offered)
	}
}

func TestGateVersionCheck_ExitCodeReadAfterClose(t *testing.T) {
	// Close is what reaps the ssh child, so the exit code is only final
	// afterwards — the opposite of LinkErr, which Close can clear. Assert the
	// gate reads them in the right order.
	var order []string
	// … stub client whose Close appends "close"; seams append "linkerr"/"exit" …
	want := []string{"linkerr", "close", "exit"}
	if !reflect.DeepEqual(order, want) {
		t.Errorf("order = %v, want %v", order, want)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `./scripts/dev.sh test`
Expected: FAIL — `remoteExitCodeFn` undefined.

- [ ] **Step 3: Implement**

In `cmd/quil/remote.go`, add the seam beside the existing two:

```go
// remoteExitCodeFn reports the ssh child's exit status, or -1 when it has not
// exited. Installed alongside remoteLinkErrFn.
//
// Only meaningful AFTER the connection is closed — Close is what reaps the
// child. That is the opposite of remoteLinkErrFn, which Close can silently
// clear, so the two are read on either side of the same Close call.
var remoteExitCodeFn func() int

func remoteExitCode() int {
	if remoteExitCodeFn == nil {
		return -1
	}
	return remoteExitCodeFn()
}
```

Install it in `main.go` beside `remoteLinkEstablishedFn`, from the same `transport.LinkStatus` type assertion.

Rewrite the gate's pre-switch guard:

```go
	if remoteMode() && !remoteLinkEstablished() {
		// Read BEFORE Close: Close unblocks pump via <-done, which can return
		// without ever setting pumpErr, so LinkErr would go nil.
		linkErr := remoteLinkError()
		client.Close()
		// Read AFTER Close: Close kills and reaps the ssh child, which is what
		// makes the exit status final. Reading it earlier races a child that is
		// still exiting.
		remedy := remoteinstall.ClassifyExit(remoteExitCode(), false)
		if offerRemoteInstallFn(remoteDest, remedy) {
			// The binaries are in place now, but this process is holding a dead
			// connection and half-built state. Re-launching is cleaner than
			// re-dialing in place, and it re-reads the config entry the install
			// just wrote.
			fmt.Fprintf(os.Stderr, "\n  Installed. Run `quil --remote %s` again to attach.\n\n", remoteDest)
			exitFn(0)
			return nil
		}
		reportRemoteLinkFailure(linkErr)
		exitFn(1)
		return nil
	}
```

`offerRemoteInstallFn` is a swappable var defaulting to `offerRemoteInstall`, matching the existing seam pattern.

In the remote version-mismatch arm, replace the unfixable advice with the upgrade offer, keeping the current text as the fallback when the user declines. Note in a comment why the old advice could not work: restarting the same binary reports the same version.

- [ ] **Step 4: Run tests**

Run: `./scripts/dev.sh test`, `./scripts/dev.sh test-race`, `./scripts/dev.sh vet`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/quil/
git commit -F - <<'EOF'
feat(cli): offer remote install from a failed remote launch

Exit 127 from the remote shell means quil is missing, not that the host
is unreachable. A version mismatch now offers an upgrade instead of
advice that cannot work — restarting the same binary reports the same
version.
EOF
```

---

### Task 8: documentation

**Files:**
- Modify: `docs/roadmap/remote-daemon.md`, `docs/features.md`, `CHANGELOG.md`, `.claude/CLAUDE.md`

- [ ] **Step 1: Update the roadmap**

In `docs/roadmap/remote-daemon.md`:
- Move the "`quil` must be on the remote's non-interactive `PATH`" operational note out of the trap list — it is now handled by the persisted absolute path. Keep a shortened form explaining what `remote setup` does about it, because a hand-installed host still hits it.
- Add `quil remote setup` to the Phase 1 shipped list.
- Add a supported-remote-platforms table, and record Windows remote as explicitly out of scope with the running-`.exe` reason.

- [ ] **Step 2: Update the feature catalog**

In `docs/features.md`, extend the remote-daemon section with the one-line setup command and the platform support matrix.

- [ ] **Step 3: Update CHANGELOG**

Add an `[Unreleased]` entry. Describe only what is true — no claim of Windows-remote support, no claim of drift repair.

- [ ] **Step 4: Update the project instructions**

Add a `remote setup` paragraph to the "Remote mode" bullet in `.claude/CLAUDE.md`, covering: the exit-code detection contract, the `Kill`-before-`reap` and `LinkErr`-before-`Close`/`ExitCode`-after-`Close` ordering constraints, the reason the probe and install scripts use different stdin shapes, and the loop guard.

- [ ] **Step 5: Commit**

```bash
git add docs/ CHANGELOG.md .claude/CLAUDE.md
git commit -F - <<'EOF'
docs: cover remote auto-install and its platform limits
EOF
```

---

## Manual verification before merge

Unit tests cannot exercise a real install. Run these against the Ubuntu 24.04 VM at `artyom@192.168.6.12`, using `--from-dir` with `./scripts/dev.sh cross` output since local builds are dev builds:

1. **Fresh install** — remove `/usr/local/bin/quil*` and `~/.local/bin/quil*` on the VM, run `quil --remote 192.168.6.12`, accept the prompt, confirm it installs and the next launch attaches.
2. **PATH independence** — confirm `ssh 192.168.6.12 command -v quil` still fails while `quil --remote` works. This is the whole point of the persisted path.
3. **Upgrade** — install an older version, launch, confirm the mismatch offers an upgrade, accept, confirm the daemon restarts and panes respawn.
4. **Decline** — answer `n`; confirm the original diagnosis is printed and nothing was written to the VM.
5. **Loop guard** — push a deliberately wrong-arch binary via `--from-dir`, confirm the second failure reports "will not execute" rather than re-offering.
6. **ssh-level failure** — point at an unreachable host; confirm no install is offered.
