package sbx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Step budgets. A host script may warm a dependency cache and an env create
// may pull an image, so both are minutes, not seconds; the read-only probes
// are short because a hung sbx daemon must not hold a pane for long.
const (
	probeTimeout       = 15 * time.Second
	prepareHostTimeout = 30 * time.Minute
	createTimeout      = 20 * time.Minute
	postCreateTimeout  = 20 * time.Minute
	mountTimeout       = 30 * time.Second
)

// Runner runs one program. Every sbx call and the host script go through it,
// so tests drive the lifecycle without sbx installed.
type Runner interface {
	// Run runs argv[0] with argv[1:] in dir (the process's own directory when
	// empty), writing stdout and stderr to w.
	Run(ctx context.Context, dir string, w io.Writer, argv ...string) error
}

// ExecRunner is the real Runner.
type ExecRunner struct{}

// Run implements Runner. A relative argv[0] containing a separator is
// resolved against dir by os/exec; a bare name is looked up on PATH.
func (ExecRunner) Run(ctx context.Context, dir string, w io.Writer, argv ...string) error {
	if len(argv) == 0 {
		return errors.New("empty command")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Stdout = w
	cmd.Stderr = w
	cmd.Stdin = nil
	hideWindow(cmd)
	return cmd.Run()
}

// Binary resolves the sbx CLI, or reports it missing with the install command
// for this OS.
func Binary(goos string) (string, error) {
	p, err := exec.LookPath("sbx")
	if err != nil {
		return "", fmt.Errorf("sbx is not installed. %s", InstallHint(goos))
	}
	return p, nil
}

// InstallHint is the one-line install instruction per host OS. Docker
// Sandboxes support macOS 14+ on Apple silicon, Windows 11 x64 (with the
// Windows Hypervisor Platform feature) and Ubuntu 24.04+ (KVM, user in the
// kvm group).
func InstallHint(goos string) string {
	switch goos {
	case "darwin":
		return "Install it with: brew install docker/tap/sbx (macOS 14+, Apple silicon)"
	case "windows":
		return "Install it with: winget install Docker.sbx (Windows 11 x64, Windows Hypervisor Platform enabled)"
	default:
		return "Install it from https://docs.docker.com/ai/sandboxes/ (Ubuntu 24.04+, KVM, user in the kvm group)"
	}
}

// Client is the sbx lifecycle for one daemon.
type Client struct {
	Bin    string
	Runner Runner
}

func (c Client) run(ctx context.Context, dir string, w io.Writer, args ...string) error {
	return c.Runner.Run(ctx, dir, w, append([]string{c.Bin}, args...)...)
}

// output runs a short read-only sbx command and returns its combined output.
func (c Client) output(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	var buf bytes.Buffer
	err := c.run(ctx, "", &buf, args...)
	return buf.String(), err
}

// Version asks sbx for its version, which doubles as "the CLI runs at all".
func (c Client) Version(ctx context.Context) (string, error) {
	out, err := c.output(ctx, "version")
	if err != nil {
		return "", fmt.Errorf("sbx version: %w%s", err, tail(out))
	}
	line := strings.TrimSpace(out)
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = strings.TrimSpace(line[:i])
	}
	return line, nil
}

// Sandbox is one row of `sbx ls`.
type Sandbox struct {
	Name   string
	Status string
}

// List returns the existing sandboxes. A "not authenticated" answer is
// turned into the instruction that fixes it.
func (c Client) List(ctx context.Context) ([]Sandbox, error) {
	out, err := c.output(ctx, "ls")
	if hint := Hint(out); hint != "" {
		return nil, errors.New(hint)
	}
	if err != nil {
		return nil, fmt.Errorf("sbx ls: %w%s", err, tail(out))
	}
	return ParseList(out), nil
}

// Exists reports whether a sandbox with this name exists.
// It asks `sbx ls --quiet`, which prints one name per line: the table form
// shifts its columns when AGENT or PORTS is empty. The first field of each
// line is taken and a header line is skipped, so a table answer from a build
// that ignores --quiet still reads correctly.
func (c Client) Exists(ctx context.Context, name string) (bool, error) {
	out, err := c.output(ctx, "ls", "--quiet")
	if hint := Hint(out); hint != "" {
		return false, errors.New(hint)
	}
	if err != nil {
		return false, fmt.Errorf("sbx ls: %w%s", err, tail(out))
	}
	for _, l := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n") {
		f := strings.Fields(l)
		if len(f) == 0 || f[0] == "SANDBOX" {
			continue
		}
		if f[0] == name {
			return true, nil
		}
	}
	return false, nil
}

// ParseList reads the table `sbx ls` prints:
//
//	SANDBOX         AGENT   STATUS   PORTS   WORKSPACE
//	my-app          claude  running          /home/me/my-app
//
// Columns are located by their offsets in the header line rather than by
// splitting on spaces, because AGENT and PORTS can be empty and would shift
// every field after them.
func ParseList(out string) []Sandbox {
	lines := strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n")
	header := -1
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "SANDBOX") {
			header = i
			break
		}
	}
	if header < 0 {
		return nil
	}
	h := lines[header]
	statusAt := strings.Index(h, "STATUS")
	var res []Sandbox
	for _, l := range lines[header+1:] {
		f := strings.Fields(l)
		if len(f) == 0 {
			continue
		}
		s := Sandbox{Name: f[0]}
		if statusAt >= 0 && statusAt < len(l) {
			if sf := strings.Fields(l[statusAt:]); len(sf) > 0 {
				s.Status = sf[0]
			}
		}
		res = append(res, s)
	}
	return res
}

// policyReady reports whether the global network policy was initialised.
// sbx refuses every env create until it is.
func (c Client) policyReady(ctx context.Context) error {
	out, _ := c.output(ctx, "policy", "ls")
	if hint := Hint(out); hint != "" {
		return errors.New(hint)
	}
	return nil
}

// Hint turns a known sbx failure into the instruction that fixes it, or ""
// when the output matches none.
func Hint(out string) string {
	low := strings.ToLower(out)
	switch {
	// sbx 0.42 said "Not authenticated to Docker"; 0.47 says "not signed in
	// to Docker".
	case strings.Contains(low, "not authenticated"), strings.Contains(low, "not signed in"):
		return "sbx is not signed in to Docker. Run: sbx login"
	case strings.Contains(low, "network policy has not been initialized"),
		strings.Contains(low, "policy has not been initialized"),
		strings.Contains(low, "not been initialized"):
		return "the sbx global network policy is not set. Run once: sbx policy init balanced"
	case strings.Contains(low, "resolve kits") && strings.Contains(low, "invalid reference"):
		return "sbx read a kit path as an image reference: local kit sources must start with ./ or ../"
	}
	return ""
}

// CreateArgs is `sbx env create <file> --name <name> <approve>`, where
// approve is the flag that applies the plan without asking (see
// ApproveFlag).
func CreateArgs(p Profile, approve string) []string {
	return []string{"env", "create", p.EnvFile, "--name", p.Name, approve}
}

// ApproveFlag picks the "apply without asking" flag from `sbx env create
// --help`. sbx renamed it between releases: 0.47 has -y/--auto-approve, 0.48
// has -f/--force. Without one, create waits on a prompt nobody can answer.
// The newer spelling wins when the help names both; an unreadable help gets
// the newer spelling too.
func ApproveFlag(help string) string {
	if !strings.Contains(help, "--force") && strings.Contains(help, "--auto-approve") {
		return "--auto-approve"
	}
	return "--force"
}

// PostCreateArgs runs one set-up step inside the sandbox.
func PostCreateArgs(p Profile, step []string) []string {
	args := []string{"exec"}
	if p.PostCreateEnvFile != "" {
		args = append(args, "--env-file", p.PostCreateEnvFile)
	}
	args = append(args, p.Name)
	return append(args, step...)
}

// ExecArgs is the pane's own command: an interactive `sbx exec` running the
// agent inside the sandbox. workdir is omitted when empty, and sbx then starts
// in the sandbox's primary workspace. env entries are KEY=VALUE.
func ExecArgs(name, workdir string, env []string, cmd string, args []string) []string {
	out := []string{"exec", "-it"}
	if workdir != "" {
		out = append(out, "-w", workdir)
	}
	for _, e := range env {
		out = append(out, "-e", e)
	}
	out = append(out, name, cmd)
	return append(out, args...)
}

// MountArgs is `sbx mount <name> <dir>`: dir bind-mounted into the sandbox at
// the same path.
func MountArgs(name, dir string) []string {
	return []string{"mount", name, dir}
}

// Mount adds a host folder to the sandbox. sbx applies it to the RUNNING
// sandbox (no restart, the processes already inside keep running), restores
// it after a stop and start, and answers a repeat with success — so it is
// safe to run before every spawn. Its output goes into the error only: on
// success sbx prints one line the pane does not need.
func (c Client) Mount(ctx context.Context, name, dir string) error {
	ctx, cancel := context.WithTimeout(ctx, mountTimeout)
	defer cancel()
	var buf bytes.Buffer
	if err := c.run(ctx, "", &buf, MountArgs(name, dir)...); err != nil {
		return fmt.Errorf("sbx mount %s: %w%s", dir, err, errorLine(buf.String()))
	}
	return nil
}

// Ensure makes the profile's sandbox exist. It reports whether it created one.
//
// An existing sandbox is REUSED as it is: no host script, no create, no
// post-create step. Changing the cage means removing the sandbox
// (`sbx rm <name>`) so the next pane creates it again.
//
// Progress and every step's own output go to w, so the pane shows what ran
// and, on a failure, why.
func (c Client) Ensure(ctx context.Context, p Profile, w io.Writer) (bool, error) {
	exists, err := c.Exists(ctx, p.Name)
	if err != nil {
		return false, err
	}
	if exists {
		fmt.Fprintf(w, "Using existing sbx sandbox %s.\n", p.Name)
		return false, nil
	}
	if st, err := os.Stat(p.EnvFile); err != nil || !st.Mode().IsRegular() {
		return false, fmt.Errorf("sbx env_file %s not found (named by %s)", p.EnvFile, p.Path)
	}
	if err := c.policyReady(ctx); err != nil {
		return false, err
	}

	if len(p.PrepareHost) > 0 {
		fmt.Fprintf(w, "Running the host step: %s\n", strings.Join(p.PrepareHost, " "))
		var hostOut bytes.Buffer
		sctx, cancel := context.WithTimeout(ctx, prepareHostTimeout)
		err := c.Runner.Run(sctx, p.Dir, io.MultiWriter(w, &hostOut), p.PrepareHost...)
		cancel()
		if err != nil {
			return false, fmt.Errorf("prepare_host failed: %w%s — the sandbox was not created", err, errorLine(hostOut.String()))
		}
	}

	help, _ := c.output(ctx, "env", "create", "--help")
	approve := ApproveFlag(help)

	fmt.Fprintf(w, "Creating sbx sandbox %s from %s…\n", p.Name, p.EnvFile)
	var out bytes.Buffer
	cctx, cancel := context.WithTimeout(ctx, createTimeout)
	err = c.run(cctx, p.Dir, io.MultiWriter(w, &out), CreateArgs(p, approve)...)
	cancel()
	if err != nil {
		if hint := Hint(out.String()); hint != "" {
			return false, fmt.Errorf("sbx env create failed: %s", hint)
		}
		// The pane shows only this line once the spawn fails, so it carries
		// sbx's own last line rather than just an exit status.
		return false, fmt.Errorf("sbx env create failed: %w%s", err, errorLine(out.String()))
	}

	for i, step := range p.PostCreate {
		fmt.Fprintf(w, "Running set-up step %d inside the sandbox: %s\n", i+1, strings.Join(step, " "))
		pctx, cancel := context.WithTimeout(ctx, postCreateTimeout)
		err := c.run(pctx, p.Dir, w, PostCreateArgs(p, step)...)
		cancel()
		if err != nil {
			return true, fmt.Errorf("post_create step %d failed: %w — the sandbox exists but is not fully set up; "+
				"remove it with `sbx rm --force %s` and open the pane again", i+1, err, p.Name)
		}
	}
	return true, nil
}

// tail is the last line of a command's output, for an error message.
func tail(out string) string {
	out = strings.TrimSpace(out)
	if out == "" {
		return ""
	}
	if i := strings.LastIndexByte(out, '\n'); i >= 0 {
		out = out[i+1:]
	}
	const max = 200
	if len(out) > max {
		out = out[:max] + "…"
	}
	return ": " + out
}

// errorLine is the line of a failed command's output worth putting in the
// one-line error the pane keeps: the last line mentioning "error", else the
// last line. sbx ends its errors with a "try: ... --help" hint, so the last
// line alone says nothing.
func errorLine(out string) string {
	lines := strings.Split(strings.ReplaceAll(strings.TrimSpace(out), "\r\n", "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.Contains(strings.ToLower(lines[i]), "error") {
			return tail(lines[i])
		}
	}
	return tail(out)
}
