package sbx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// Attaching to a sandbox Quil did not create.
//
// Another tool can own a sandbox's whole life — create, refresh, remove —
// while panes still run in it. Quil then only starts the pane's own command
// with `sbx exec`, the same way it does for a profile's sandbox, and never
// creates, mounts into or removes anything. There is no profile and no
// approval, because nothing from the repository decides anything: the user
// picks the sandbox by name in the create dialog, and the daemon checks that
// the pane's folder is one the sandbox already mounts writable.
//
// Measured against sbx 0.48: `sbx ls --json` lists every sandbox with its
// workspaces (the primary one first, read-only ones marked ":ro"), and
// `sbx exec` starts a stopped sandbox before it runs the command.

// Workspace is one folder a sandbox mounts, at the same path as on the host.
type Workspace struct {
	Path     string `json:"path"`
	ReadOnly bool   `json:"read_only,omitempty"`
}

// Listed is one sandbox as `sbx ls --json` reports it.
type Listed struct {
	Name       string      `json:"name"`
	Status     string      `json:"status,omitempty"`
	Workspaces []Workspace `json:"workspaces,omitempty"`
}

// ListSandboxes returns every sandbox with its workspaces.
func (c Client) ListSandboxes(ctx context.Context) ([]Listed, error) {
	out, err := c.output(ctx, "ls", "--json")
	if hint := Hint(out); hint != "" {
		return nil, errors.New(hint)
	}
	if err != nil {
		return nil, fmt.Errorf("sbx ls: %w%s", err, tail(out))
	}
	return ParseListJSON(out)
}

// listRow is the on-the-wire shape. Status is spelled "status" by sbx 0.48;
// "state" is accepted as well, as is a list wrapped in {"sandboxes": [...]}.
type listRow struct {
	Name       string   `json:"name"`
	Status     string   `json:"status"`
	State      string   `json:"state"`
	Workspaces []string `json:"workspaces"`
}

// ParseListJSON reads `sbx ls --json`. The output is stdout and stderr
// together, so the JSON is looked for from the first line that opens it; a
// notice sbx prints first is skipped. A row with a name Quil cannot use is
// left out, never repaired: the name reaches sbx argv as a positional.
func ParseListJSON(out string) ([]Listed, error) {
	body, ok := jsonStart(out)
	if !ok {
		if strings.TrimSpace(out) == "" {
			return nil, nil
		}
		return nil, errors.New("sbx ls --json: no JSON in the answer")
	}
	var rows []listRow
	if err := json.Unmarshal([]byte(body), &rows); err != nil {
		var wrapped struct {
			Sandboxes []listRow `json:"sandboxes"`
		}
		if err2 := json.Unmarshal([]byte(body), &wrapped); err2 != nil {
			return nil, fmt.Errorf("sbx ls --json: %w", err)
		}
		rows = wrapped.Sandboxes
	}
	var res []Listed
	for _, r := range rows {
		if !ValidName(r.Name) {
			continue
		}
		l := Listed{Name: r.Name, Status: strings.ToLower(r.Status)}
		if l.Status == "" {
			l.Status = strings.ToLower(r.State)
		}
		for _, w := range r.Workspaces {
			l.Workspaces = append(l.Workspaces, parseWorkspace(w))
		}
		res = append(res, l)
	}
	return res, nil
}

// jsonStart returns out from the first line that starts a JSON value.
func jsonStart(out string) (string, bool) {
	out = strings.ReplaceAll(out, "\r\n", "\n")
	for i := 0; i < len(out); {
		line := strings.TrimLeft(out[i:], " \t")
		if strings.HasPrefix(line, "[") || strings.HasPrefix(line, "{") {
			return line, true
		}
		nl := strings.IndexByte(out[i:], '\n')
		if nl < 0 {
			break
		}
		i += nl + 1
	}
	return "", false
}

// parseWorkspace splits sbx's "path[:ro|:rw]" spelling.
func parseWorkspace(s string) Workspace {
	switch {
	case strings.HasSuffix(s, ":ro"):
		return Workspace{Path: strings.TrimSuffix(s, ":ro"), ReadOnly: true}
	case strings.HasSuffix(s, ":rw"):
		return Workspace{Path: strings.TrimSuffix(s, ":rw")}
	}
	return Workspace{Path: s}
}

// Covers reports whether dir is inside a WRITABLE workspace, as `sbx ls`
// lists them. The most specific workspace holding dir decides, because a
// read-only folder can be mounted inside a writable one (and the reverse);
// at one path, read-only wins. Both sides are compared as real paths where
// they resolve: a link inside a workspace can lead anywhere, and the agent
// then runs where it leads.
//
// `sbx ls` does not list a folder added later with `sbx mount`, so false is
// not the last word: see WritableInside.
func (l Listed) Covers(dir string) bool {
	d := realOrClean(dir)
	best, writable := -1, false
	for _, w := range l.Workspaces {
		if !filepath.IsAbs(w.Path) {
			continue
		}
		p := realOrClean(w.Path)
		if !ancestorOrSelf(p, d) {
			continue
		}
		switch {
		case len(p) > best:
			best, writable = len(p), !w.ReadOnly
		case len(p) == best && w.ReadOnly:
			writable = false
		}
	}
	return writable
}

func realOrClean(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// ErrNoSandbox reports that the named sandbox does not exist.
var ErrNoSandbox = errors.New("no such sbx sandbox")

// FindListed returns the sandbox called name.
func FindListed(list []Listed, name string) (Listed, error) {
	for _, l := range list {
		if l.Name == name {
			return l, nil
		}
	}
	return Listed{}, fmt.Errorf("%w: %s — create it with the tool that owns it, then restart this pane", ErrNoSandbox, name)
}

// CheckAttach refuses a pane directory the sandbox does not mount writable.
// Without it the agent would start in the sandbox's primary workspace, which
// is not the folder the user opened the pane in.
func CheckAttach(l Listed, dir string) error {
	if l.Covers(dir) {
		return nil
	}
	var rw []string
	for _, w := range l.Workspaces {
		if !w.ReadOnly {
			rw = append(rw, w.Path)
		}
	}
	if len(rw) == 0 {
		return fmt.Errorf("sbx sandbox %s mounts no writable folder", l.Name)
	}
	return fmt.Errorf("%s is not in a writable folder of sbx sandbox %s, which mounts %s", dir, l.Name, strings.Join(rw, ", "))
}

// attachProbeTimeout bounds WritableInside. It may start a stopped sandbox
// first, which takes seconds, so it is longer than a read-only probe.
const attachProbeTimeout = 60 * time.Second

// writableMarker is what the probe prints when the folder is writable; any
// other output (a notice that sbx started the sandbox) is ignored.
const writableMarker = "QUIL_SBX_WRITABLE"

// probeScript tests the folder passed as $1. The folder is an ARGUMENT,
// never script text. `test -w` is access(W_OK), which also fails on a
// read-only mount, so it answers for the effective mount, not the mode bits.
const probeScript = `if [ -d "$1" ] && [ -w "$1" ]; then echo ` + writableMarker + `; fi`

// WritableArgs is the probe's argv.
func WritableArgs(name, dir string) []string {
	return []string{"exec", name, "sh", "-c", probeScript, "sh", dir}
}

// WritableInside asks the sandbox itself whether dir is a writable folder in
// it. This is the fallback when Covers says no: `sbx ls` does not list a
// folder another tool added with `sbx mount`, and only the VM knows its
// effective mounts. It changes nothing; `sbx exec` starts a stopped sandbox,
// which the pane's own exec would do next anyway.
//
// A folder the agent made inside the VM at that path also answers yes. That
// cannot widen the cage — the pane still runs in this sandbox — it only
// means the pane works in the VM's own copy of the folder.
func (c Client) WritableInside(ctx context.Context, name, dir string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, attachProbeTimeout)
	defer cancel()
	var buf bytes.Buffer
	if err := c.run(ctx, "", &buf, WritableArgs(name, dir)...); err != nil {
		return false, fmt.Errorf("sbx exec: %w%s", err, tail(buf.String()))
	}
	for _, l := range strings.Split(buf.String(), "\n") {
		if strings.TrimSpace(l) == writableMarker {
			return true, nil
		}
	}
	return false, nil
}
