# Remote Daemon Phase 3 — Remote-Correct UI

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Registry:** RD-020 … RD-028 — see `docs/roadmap/remote-daemon.md` § Work registry.

**Prerequisites:** RD-004 (discovery packages take a `context.Context`). Phase 2 need not be complete, but see the revision points below.

> ### Revision points
>
> This plan was written before Phase 2 shipped, at the user's request. Three
> things it assumes are not yet settled. Re-read this box before starting, and
> revise rather than trusting the text around it:
>
> 1. **Async dialog + reconnect interaction.** Every RPC here is a round trip
>    that can be in flight when the link drops. Phase 2's input freeze must
>    swallow dialog keys too, and an in-flight browse response arriving after a
>    reconnect must be dropped. Task 2 assumes the generation counter from
>    RD-015 exists and reuses it. If Phase 2 landed a different staleness
>    mechanism, use that one instead of adding a second.
> 2. **Timeout budget.** The 3 s local timeout the session picker uses is tuned
>    for a local socket. Over ssh, a first RPC after an idle period pays TCP
>    plus auth on Windows. Task 1 proposes 8 s; confirm against measured
>    reconnect latency from Phase 2's manual testing.
> 3. **RD-026 and RD-027 depend on open questions 1 and 2**, which are not
>    answered. Both tasks state the decision they need before their first step.

**Goal:** Every surface that reads a filesystem reads the *server's* filesystem.

**Architecture:** Four pure discovery functions already exist and already take a directory. Phase 3 moves the *call* to the daemon and adds a request/response IPC pair per surface, following `MsgClaudeSessionsReq`/`MsgClaudeSessionsResp` exactly: worker goroutine, single-flight atomic, verbatim echo of the request key so the client can drop stale answers. The TUI's dialogs become async — they render a pending state and fill in on response.

**Tech Stack:** Go 1.25, Bubble Tea v2, stdlib. `gopkg.in/yaml.v3` already present for kubeconfig.

## Global Constraints

- Module path `github.com/artyomsv/quil`; Go 1.25.
- Docker-only build/test: `./scripts/dev.sh build|test|vet|test-race`.
- Production isolation: never touch `~/.quil/`; dev work uses `./quil-dev.exe`.
- Commit subjects imperative, ≤72 chars, cite the RD id. No AI/model/vendor attribution.
- **Every new IPC response echoes its request key verbatim.** Never cleaned, resolved, or normalised — the echo is the client's staleness check, and daemon-side normalisation makes a legitimate request look permanently stale. This is a rule the codebase already learned the hard way; see `ClaudeSessionsRespPayload`.
- **Every new handler that does file I/O runs on a worker goroutine behind a single-flight atomic.** A handler that blocks the dispatch goroutine freezes that client's entire connection.
- **Response payloads must be incapable of exceeding `maxFrameSize` by construction** — cap entry counts and set a `Truncated` flag, do not rely on directories being small.
- Local mode must keep working unchanged. Every RPC has a local fast path: when the daemon is local the answer is identical, so the same code path is used rather than branching on remote mode. This means the RPCs are exercised in ordinary local development, which is the only way they will be tested often enough to stay correct.

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/ipc/protocol.go` | four new message-type pairs and their payloads |
| `internal/daemon/browse.go` | **new** — directory-listing handler |
| `internal/daemon/discover.go` | **new** — git + kube discovery handlers |
| `internal/daemon/plugins.go` | **new** — plugin availability handler |
| `internal/tui/browse_client.go` | **new** — TUI-side request/response plumbing for all four |
| `internal/tui/dialog.go` | setup dialog becomes async |
| `internal/tui/recentcwd.go` | per-target storage |
| `cmd/quil/status.go` | `quil status` over the transport (RD-026) |
| `internal/tui/update.go` | remote-mode update controls (RD-027) |

---

## Task 1 (RD-020a): Directory-listing RPC — protocol and daemon

**Why this is first:** every other picker keys off a CWD. Git discovery, kube namespace defaults, the Claude session list and `recent-cwds.json` all take a directory that today comes from the local browser. Fix the directory and most of the rest follows.

**Note on the Claude session list.** It is already remote-correct — `handleClaudeSessionsReq` runs daemon-side and scans the daemon's disk. What is wrong is only the CWD handed to it. No session-listing RPC is needed; this task fixes it.

**Files:**
- Modify: `internal/ipc/protocol.go`
- Create: `internal/daemon/browse.go`, `internal/daemon/browse_test.go`
- Modify: `internal/daemon/daemon.go` (dispatch case, single-flight field)

**Interfaces:**
- Produces:
  ```go
  const (
      MsgBrowseDirReq  = "browse_dir_req"
      MsgBrowseDirResp = "browse_dir_resp"
  )

  type BrowseDirReqPayload struct {
      Path string `json:"path"` // "" means the daemon's default CWD
  }

  type BrowseEntry struct {
      Name  string `json:"name"`
      IsDir bool   `json:"is_dir"`
  }

  type BrowseDirRespPayload struct {
      Path      string        `json:"path"`   // echoes the REQUEST verbatim
      Resolved  string        `json:"resolved"` // absolute, cleaned; what to display and commit
      Parent    string        `json:"parent,omitempty"`
      Entries   []BrowseEntry `json:"entries"`
      Truncated bool          `json:"truncated,omitempty"`
      Error     string        `json:"error,omitempty"`
  }

  const MaxBrowseEntries = 500
  ```
  `Path` and `Resolved` are separate on purpose: `Path` is the staleness key and must echo verbatim; `Resolved` is the usable answer. Merging them would break the echo contract the moment the daemon cleaned a trailing slash.

- [ ] **Step 1: Write the failing test**

Create `internal/daemon/browse_test.go`:

```go
package daemon

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
)

func TestBrowseDirResponse_ListsDirsAndFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got := browseDirResponse(ipc.BrowseDirReqPayload{Path: root})

	if got.Error != "" {
		t.Fatalf("Error = %q, want empty", got.Error)
	}
	if got.Path != root {
		t.Errorf("Path = %q, want the request echoed verbatim (%q)", got.Path, root)
	}
	var names []string
	for _, e := range got.Entries {
		names = append(names, e.Name)
	}
	if len(names) != 2 {
		t.Fatalf("entries = %v, want 2", names)
	}
}

// The echo is the staleness key. A trailing separator must survive it.
func TestBrowseDirResponse_EchoesPathVerbatim(t *testing.T) {
	root := t.TempDir()
	req := ipc.BrowseDirReqPayload{Path: root + string(filepath.Separator)}

	got := browseDirResponse(req)

	if got.Path != req.Path {
		t.Errorf("Path = %q, want %q — daemon-side normalisation makes a live request look stale",
			got.Path, req.Path)
	}
	if got.Resolved == "" {
		t.Error("Resolved is empty; the cleaned path belongs there, not in Path")
	}
}

// A response must be incapable of overflowing the frame.
func TestBrowseDirResponse_CapsEntries(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < MaxBrowseEntries+50; i++ {
		if err := os.Mkdir(filepath.Join(root, "d"+strconv.Itoa(i)), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}

	got := browseDirResponse(ipc.BrowseDirReqPayload{Path: root})

	if len(got.Entries) > MaxBrowseEntries {
		t.Errorf("entries = %d, want <= %d", len(got.Entries), MaxBrowseEntries)
	}
	if !got.Truncated {
		t.Error("Truncated not set on a capped listing")
	}
}

// A missing directory is an answer, not a transport failure.
func TestBrowseDirResponse_MissingDir_ReportsError(t *testing.T) {
	got := browseDirResponse(ipc.BrowseDirReqPayload{
		Path: filepath.Join(t.TempDir(), "nope"),
	})
	if got.Error == "" {
		t.Error("Error is empty for a missing directory")
	}
	if got.Path == "" {
		t.Error("Path echo dropped on the error path; the client cannot match the response")
	}
}
```

- [ ] **Step 2: Run to verify it fails** — `undefined: browseDirResponse`.

```bash
docker run --rm -v "$(pwd -W 2>/dev/null || pwd)":/src -v quil-gomod:/go/pkg/mod -w //src golang:1.25-alpine \
  go test ./internal/daemon/ -run TestBrowseDir -v
```

- [ ] **Step 3: Add the protocol types**

Append to the const block and the payload section of `internal/ipc/protocol.go`, next to the Claude-session pair, using the interface definitions above verbatim.

- [ ] **Step 4: Implement the handler**

Create `internal/daemon/browse.go`:

```go
package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
)

// browseTimeout bounds one listing. A network mount that has gone away blocks
// os.ReadDir indefinitely, and this handler holds the single-flight slot.
const browseTimeout = 10 * time.Second

// handleBrowseDirReq answers a directory listing.
//
// Worker goroutine + single-flight, the shape handleClaudeSessionsReq
// established: this is file I/O, and running it on the conn's dispatch
// goroutine would freeze every other message from that client. The rejection
// still echoes the requested path, or the TUI cannot match it and waits out its
// whole timeout on an answer it already has.
func (d *Daemon) handleBrowseDirReq(conn *ipc.Conn, msg *ipc.Message) {
	var req ipc.BrowseDirReqPayload
	_ = msg.DecodePayload(&req)

	if !d.browseScanning.CompareAndSwap(false, true) {
		respondTo(conn, msg.ID, ipc.MsgBrowseDirResp, ipc.BrowseDirRespPayload{
			Path:  req.Path,
			Error: "another directory listing is already running",
		})
		return
	}
	go func() {
		defer d.browseScanning.Store(false)
		respondTo(conn, msg.ID, ipc.MsgBrowseDirResp, browseDirResponse(req))
	}()
}

// browseDirResponse is the pure half: decide → read → cap → echo.
func browseDirResponse(req ipc.BrowseDirReqPayload) ipc.BrowseDirRespPayload {
	ctx, cancel := context.WithTimeout(context.Background(), browseTimeout)
	defer cancel()

	// Path echoes the REQUEST verbatim — it is the client's staleness key, not
	// a statement about what was read. Resolved carries the usable answer.
	out := ipc.BrowseDirRespPayload{Path: req.Path}

	target := req.Path
	if target == "" {
		target = defaultCWD()
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		out.Error = fmt.Sprintf("resolve %s: %v", target, err)
		return out
	}
	out.Resolved = filepath.Clean(abs)
	if parent := filepath.Dir(out.Resolved); parent != out.Resolved {
		out.Parent = parent
	}

	entries, err := os.ReadDir(out.Resolved)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	for _, e := range entries {
		if ctx.Err() != nil {
			out.Truncated = true
			break
		}
		if len(out.Entries) >= MaxBrowseEntries {
			out.Truncated = true
			break
		}
		out.Entries = append(out.Entries, ipc.BrowseEntry{Name: e.Name(), IsDir: e.IsDir()})
	}
	sort.Slice(out.Entries, func(i, j int) bool {
		if out.Entries[i].IsDir != out.Entries[j].IsDir {
			return out.Entries[i].IsDir // directories first, matching the local browser
		}
		return out.Entries[i].Name < out.Entries[j].Name
	})
	return out
}
```

Add `browseScanning atomic.Bool` to the `Daemon` struct beside `sessionScanning`, and the dispatch case beside `MsgClaudeSessionsReq`.

- [ ] **Step 5: Run to verify it passes**
- [ ] **Step 6: Commit**

```bash
git add internal/ipc/protocol.go internal/daemon/browse.go internal/daemon/browse_test.go internal/daemon/daemon.go
git commit -F - <<'EOF'
feat(daemon): serve directory listings over IPC

The pane setup dialog browses the machine running the TUI, which in remote
mode is the wrong disk. This is the daemon half: a capped, context-bounded
listing on a worker goroutine behind a single-flight guard.

Path echoes the request verbatim as the client's staleness key; Resolved
carries the cleaned absolute path. Keeping them separate is what lets the
echo stay byte-exact.

RD-020
EOF
```

---

## Task 2 (RD-020b, RD-028): Async directory browser in the setup dialog

**The risky task in this phase.** The setup dialog has pinned-height and width-accounting invariants with tests written specifically because they were got wrong before: `TestSetupBoxChrome_MatchesLipglossWrapLimit`, `TestRenderSetup_PickFocusChange_HeightStable`, `TestSetupRowIdleMark_MatchesRowIndent`. Making the field async adds a *pending* state to a renderer whose height must not change.

**Files:**
- Create: `internal/tui/browse_client.go`, `internal/tui/browse_client_test.go`
- Modify: `internal/tui/dialog.go` (`loadBrowseDir`, `initSetupBrowser`, `handleSetupCWDKey`, `renderCreatePaneSetupDialog`)
- Modify: `internal/tui/model.go` (listen dispatch)

**Interfaces:**
- Consumes: `ipc.MsgBrowseDirReq/Resp` from Task 1.
- Produces:
  ```go
  type browseDirMsg struct{ Resp ipc.BrowseDirRespPayload }
  type browseState struct {
      requested string // the path we asked for; matched against the echo
      pending   bool
      err       string
      truncated bool
  }
  func (m Model) requestBrowseDir(path string) tea.Cmd
  func (m *Model) applyBrowseDir(resp ipc.BrowseDirRespPayload)
  ```

- [ ] **Step 1: Write the failing test**

```go
// A response for a directory the user has already left is dropped.
func TestApplyBrowseDir_StaleResponseIgnored(t *testing.T) {
	m := Model{browse: browseState{requested: "/home/a", pending: true}}
	m.applyBrowseDir(ipc.BrowseDirRespPayload{
		Path:     "/home/b",
		Resolved: "/home/b",
		Entries:  []ipc.BrowseEntry{{Name: "stale", IsDir: true}},
	})
	if !m.browse.pending {
		t.Error("stale response cleared the pending state")
	}
	for _, e := range m.cwdBrowseEntries {
		if e == "stale" {
			t.Fatal("stale entries were applied")
		}
	}
}

// The matching response fills the list and clears pending.
func TestApplyBrowseDir_MatchingResponseApplies(t *testing.T) {
	m := Model{browse: browseState{requested: "/home/a", pending: true}}
	m.applyBrowseDir(ipc.BrowseDirRespPayload{
		Path:     "/home/a",
		Resolved: "/home/a",
		Parent:   "/home",
		Entries:  []ipc.BrowseEntry{{Name: "proj", IsDir: true}},
	})
	if m.browse.pending {
		t.Error("pending not cleared")
	}
	if m.cwdBrowseDir != "/home/a" {
		t.Errorf("cwdBrowseDir = %q, want the RESOLVED path", m.cwdBrowseDir)
	}
}

// The dialog's height must not change between pending and loaded. This is the
// invariant TestRenderSetup_PickFocusChange_HeightStable exists to protect, and
// an async field is a new way to break it.
func TestRenderSetup_BrowsePendingHeightStable(t *testing.T) {
	loaded := newSetupDialogModel(t)
	loaded.browse = browseState{requested: "/home/a"}
	loaded.cwdBrowseEntries = []string{"a", "b", "c"}

	pending := newSetupDialogModel(t)
	pending.browse = browseState{requested: "/home/a", pending: true}
	pending.cwdBrowseEntries = nil

	lp := strings.Count(renderCreatePaneSetupDialog(loaded), "\n")
	pp := strings.Count(renderCreatePaneSetupDialog(pending), "\n")
	if lp != pp {
		t.Errorf("height changed with load state: loaded %d rows, pending %d", lp, pp)
	}
}

// An error from the daemon is shown in the field, not swallowed.
func TestApplyBrowseDir_ErrorSurfaces(t *testing.T) {
	m := Model{browse: browseState{requested: "/nope", pending: true}}
	m.applyBrowseDir(ipc.BrowseDirRespPayload{Path: "/nope", Error: "no such file or directory"})
	if m.browse.pending {
		t.Error("pending not cleared on error")
	}
	if m.browse.err == "" {
		t.Error("error not recorded")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

- [ ] **Step 3: Implement the client half**

Create `internal/tui/browse_client.go` with `requestBrowseDir` (sends `MsgBrowseDirReq`, sets `browse.requested`/`pending`) and `applyBrowseDir` (drops on `resp.Path != m.browse.requested`, else fills `cwdBrowseEntries`, `cwdBrowseDir = resp.Resolved`, `cwdBrowseParent`, clears pending).

Reserve the pending row so height is stable: render the entry list at a fixed row count, filling with blanks while pending and showing a single dim `Loading…` on the first row. Do **not** collapse the field.

- [ ] **Step 4: Replace the local read**

`loadBrowseDir` currently calls `os.ReadDir` directly. Replace its body with `requestBrowseDir(path)` and move the entry-formatting logic into `applyBrowseDir`. Keep `validateAndNormalizeCWD` for what the user *types*, but the existence check must move daemon-side — the local check is meaningless for a remote path. Submit-time validation becomes: send a browse request for the typed path and treat an `Error` response as invalid.

- [ ] **Step 5: Wire the listen dispatch**

In `listenForMessages`, add:

```go
		case ipc.MsgBrowseDirResp:
			var payload ipc.BrowseDirRespPayload
			if err := msg.DecodePayload(&payload); err != nil {
				log.Printf("decode browse_dir_resp: %v", err)
				return listenContinueMsg{}
			}
			return browseDirMsg{Resp: payload}
```

The `browseDirMsg` branch in `Update` **must** re-arm `m.listenForMessages()`, like `memoryReportMsg` and `historyListMsg`. Omitting it kills the IPC listen loop — this package has shipped that bug before.

- [ ] **Step 6: Add a timeout tick**

Mirror `paletteSearchTimeout`: a local `tea.Tick` (use 8 s, not the picker's 3 s — see revision point 2) that compares against `m.browse.requested` and turns a never-answered request into a diagnosable row. It must **not** re-arm `listenForMessages` — it is a local timer, not an IPC message.

- [ ] **Step 7: Run the full TUI suite**

```bash
docker run --rm -v "$(pwd -W 2>/dev/null || pwd)":/src -v quil-gomod:/go/pkg/mod -w //src golang:1.25-alpine \
  go test ./internal/tui/ -v 2>&1 | tail -40
```

All pre-existing setup-dialog tests must stay green. If `TestRenderSetup_PickFocusChange_HeightStable` fails, the pending state is changing the row count.

- [ ] **Step 8: Commit**

```bash
git add internal/tui/browse_client.go internal/tui/browse_client_test.go internal/tui/dialog.go internal/tui/model.go
git commit -F - <<'EOF'
feat(tui): browse the daemon's filesystem in the setup dialog

The CWD picker read the local disk, so in remote mode every path it offered
was on the wrong machine. It now asks the daemon and matches responses by
the echoed path, dropping answers for a directory the user has left.

The field keeps a fixed row count while loading. Collapsing it would move
every row below it, which is the invariant the pinned-height test guards.

RD-020, RD-028
EOF
```

---

## Task 3 (RD-021, RD-022): Git and kube discovery RPCs

One task: the two are the same shape, share a file, and neither is useful alone.

**Files:**
- Modify: `internal/ipc/protocol.go`
- Create: `internal/daemon/discover.go`, `internal/daemon/discover_test.go`
- Modify: `internal/tui/browse_client.go`, `internal/tui/dialog.go`

**Interfaces:**
- Consumes: `gitdiscover.Candidates(ctx, dir)`, `kubediscover.Contexts(ctx)` — the RD-004 signatures.
- Produces:
  ```go
  const (
      MsgGitRepoReq   = "git_repo_req"
      MsgGitRepoResp  = "git_repo_resp"
      MsgKubeCtxReq   = "kube_ctx_req"
      MsgKubeCtxResp  = "kube_ctx_resp"
  )
  type GitRepoReqPayload  struct{ CWD string `json:"cwd"` }
  type GitRepoRespPayload struct {
      CWD        string   `json:"cwd"`   // verbatim echo
      Candidates []string `json:"candidates"`
      Error      string   `json:"error,omitempty"`
  }
  type KubeCtxReqPayload  struct{}
  type KubeCtxRespPayload struct {
      Contexts []KubeContextInfo `json:"contexts"`
      Current  string            `json:"current,omitempty"`
      Error    string            `json:"error,omitempty"`
  }
  type KubeContextInfo struct {
      Name      string `json:"name"`
      Namespace string `json:"namespace,omitempty"`
  }
  ```
  `KubeCtxReqPayload` is empty and so has no echo key. It is CWD-independent, so there is nothing to go stale — but the response must still be dropped if it arrives after the dialog closed.

- [ ] **Step 1: Write the failing tests** — mirror Task 1's four cases for the git pair (echo verbatim, cap at `maxKubeContexts`/`gitdiscover`'s existing cap of 10, error surfaces, cancelled ctx degrades). For kube, assert the current-context marker survives the round trip.

- [ ] **Step 2: Run to verify they fail**

- [ ] **Step 3: Implement** both handlers in `internal/daemon/discover.go`, each with its own single-flight atomic (`gitScanning`, `kubeScanning`). Separate atomics, not a shared one: sharing would make a kube read fail exactly when a git scan is in flight, which is when the setup dialog opens.

- [ ] **Step 4: Replace the TUI call sites** in `enterSetupOrSplit` (git) and the kube field's population, matching Task 2's request/apply/timeout shape.

- [ ] **Step 5: Run tests**
- [ ] **Step 6: Commit**

```bash
git add internal/ipc/protocol.go internal/daemon/discover.go internal/daemon/discover_test.go internal/tui/
git commit -m "feat(daemon): serve git repo and kube context discovery over IPC (RD-021, RD-022)"
```

---

## Task 4 (RD-023): Plugin availability from the server

**Why:** `Ctrl+N` greys out a plugin based on whether its binary exists on *your* machine. A tool installed only on the server shows unavailable; one installed only locally shows available and then fails to spawn.

**Files:**
- Modify: `internal/ipc/protocol.go`
- Create: `internal/daemon/plugins.go`, `internal/daemon/plugins_test.go`
- Modify: `internal/tui/dialog.go` (create-pane plugin list)

**Interfaces:**
- Produces:
  ```go
  const (
      MsgPluginListReq  = "plugin_list_req"
      MsgPluginListResp = "plugin_list_resp"
  )
  type PluginInfo struct {
      Name      string `json:"name"`
      Available bool   `json:"available"`
      Homepage  string `json:"homepage,omitempty"`
  }
  type PluginListRespPayload struct {
      Plugins []PluginInfo `json:"plugins"`
  }
  ```

- [ ] **Step 1: Write the failing test** — assert the daemon reports availability from its own `exec.LookPath`, and that a plugin the daemon lacks comes back `Available: false` with its `Homepage` intact so the greyed row still links somewhere useful.

- [ ] **Step 2: Run to verify it fails**

- [ ] **Step 3: Implement.** The daemon already owns a `plugin.Registry`; `DetectAvailability` runs there. No single-flight needed — `exec.LookPath` is cheap and the result is cacheable for the daemon's lifetime. Cache it, and invalidate on plugin reload.

- [ ] **Step 4: Consume it in the TUI.** Request once per `Ctrl+N` open. **Decision:** on timeout or error, fall back to the local availability check rather than showing everything as unavailable — a wrong grey-out is worse than a wrong offer, because the offer fails loudly at spawn while the grey-out silently hides a working tool.

- [ ] **Step 5: Run tests**
- [ ] **Step 6: Commit**

```bash
git add internal/ipc/protocol.go internal/daemon/plugins.go internal/daemon/plugins_test.go internal/tui/dialog.go
git commit -m "feat(daemon): report plugin availability from the server (RD-023)"
```

---

## Task 5 (RD-024, RD-025): Per-target recent CWDs and an honest attach payload

**Why together:** both are about local state that silently describes the wrong machine.

- `recent-cwds.json` is a flat list. Attach to a server and it fills with server paths; go back to local work and the picker offers directories that do not exist here.
- `AttachPayload.CWD` sends `os.Getwd()` so the daemon can spawn panes in the client's directory. In remote mode that is a *laptop* path being handed to a server as a spawn directory. `defaultCWD()` validates with `os.Stat` and falls back, so it degrades safely today — but it is a laptop path being tested against server disk, and a coincidental match is worse than the fallback.

**Files:**
- Modify: `internal/tui/recentcwd.go`, `internal/config/config.go` (`RecentCWDsPath`)
- Modify: `cmd/quil/main.go` (attach payload)
- Modify: `internal/tui/recentcwd_test.go`

- [ ] **Step 1: Write the failing tests**

```go
func TestRecentCWDsPath_PerRemoteTarget(t *testing.T) {
	local := config.RecentCWDsPath("")
	remote := config.RecentCWDsPath("gpu01")
	if local == remote {
		t.Fatal("remote and local share one recent-cwds file")
	}
}

// Destinations are user input and reach a filename. "user@host:22" and
// "../../etc/passwd" must both produce a safe, stable basename.
func TestRecentCWDsPath_SanitizesDestination(t *testing.T) {
	for _, dest := range []string{"user@host", "host:22", "../../etc/passwd", "a/b\\c"} {
		got := filepath.Base(config.RecentCWDsPath(dest))
		if strings.ContainsAny(got, `/\:`) || strings.Contains(got, "..") {
			t.Errorf("RecentCWDsPath(%q) basename = %q, unsafe", dest, got)
		}
	}
}

func TestAttachPayload_RemoteMode_OmitsLocalCWD(t *testing.T) {
	got := buildAttachPayload("gpu01", "/home/laptop/project")
	if got.CWD != "" {
		t.Errorf("CWD = %q, want empty — a laptop path is not a server directory", got.CWD)
	}
}
```

- [ ] **Step 2: Run to verify they fail**

- [ ] **Step 3: Implement.** `RecentCWDsPath(dest string)`: empty dest keeps today's `recent-cwds.json` exactly, so local users see no migration. Non-empty produces `recent-cwds-<sanitized>.json`, where sanitisation replaces every character outside `[A-Za-z0-9._-]` with `-` and appends a short hash of the original to keep distinct destinations distinct after collapsing.

Extract `buildAttachPayload(dest, cwd string) ipc.AttachPayload` from the inline construction in `main.go` so it is testable, returning an empty `CWD` when `dest != ""`.

- [ ] **Step 4: Run tests**
- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/tui/recentcwd.go internal/tui/recentcwd_test.go cmd/quil/main.go
git commit -F - <<'EOF'
feat(remote): scope recent directories per host and stop sending local CWD

recent-cwds.json was shared between local and remote sessions, so attaching
to a server filled the local picker with paths that do not exist here.

AttachPayload.CWD carried the laptop's working directory to a server as a
pane spawn directory. defaultCWD() validates it and falls back, so this was
safe — but only by coincidence, and a path that happens to exist on both
machines is exactly the case where the coincidence stops being safe.

RD-024, RD-025
EOF
```

---

## Task 6 (RD-026): `quil status` over the transport

> **Decision required before Step 1** — open question 1. Two answers:
> - **(a) Support it.** `quil status --remote <host>` reports the remote daemon, with the host named in both text and `--json`.
> - **(b) Keep it refused**, and document `ssh <host> quil status` as the answer.
>
> Recommendation: **(a)**, and it is cheap — the daemon already answers a version handshake, and `MsgStatusReq` is a thin addition. What makes it worth doing is the `--json` case: a script that adds `--remote` and silently keeps reporting on the wrong machine is a real failure mode, and (b) prevents it only for as long as the guard is remembered.
>
> If (b) is chosen, close RD-026 as `dropped`, keep the existing guard, and skip to Task 7.

**Files (for answer (a)):**
- Modify: `internal/ipc/protocol.go`, `internal/daemon/daemon.go`, `cmd/quil/status.go`, `cmd/quil/main.go`

- [ ] **Step 1: Write the failing test** — `runStatus` under `--remote` must produce output naming the destination, and the `--json` object must carry a `host` field that is non-empty in remote mode.
- [ ] **Step 2: Run to verify it fails**
- [ ] **Step 3: Add `MsgStatusReq`/`MsgStatusResp`** carrying uptime, pane and tab counts, daemon version and PID.
- [ ] **Step 4: Branch `runStatus`** on `remoteMode()`: dial via `dialRemote`, request, render. Remove the refusal from the `main.go` switch.
- [ ] **Step 5: Update `.claude/CLAUDE.md`** — RD-003 replaced the stale claim with a pointer to RD-026; update it again to describe the shipped behaviour.
- [ ] **Step 6: Commit**

---

## Task 7 (RD-027): Update controls in remote mode

> **Decision required before Step 1** — open question 2. The update banner describes the *remote* daemon's staged version while every apply path writes to *local* disk. Today both are suppressed. Options:
> - **(a) Label them.** Show the banner as "remote daemon update available" and keep apply disabled with an explanation.
> - **(b) Target them.** Make apply drive `quil remote setup <dest>`, which already installs and upgrades over ssh.
>
> Recommendation: **(b)** — the machinery exists and shipped in v1.44.0, so this is wiring rather than new capability, and (a) leaves the user reading about an update they cannot act on. Do (a) first if Phase 3 is running long; it is a strict subset.

**Files:**
- Modify: `internal/tui/update.go`, `cmd/quil/update_apply.go`, `cmd/quil/remote_setup.go`

- [ ] **Step 1: Write the failing test** — in remote mode the About row's label must name the remote host, and the apply path must route to `remote setup` rather than the local swap.
- [ ] **Step 2: Run to verify it fails**
- [ ] **Step 3: Implement** per the chosen answer.
- [ ] **Step 4: Commit**

---

## Verification (whole phase)

- [ ] `./scripts/dev.sh test`, `vet`, `test-race` green.
- [ ] Windows native TUI + daemon suites per the `go test -c` workflow.
- [ ] **Manual, local:** every dialog behaves exactly as before. This matters more than the remote check — the RPCs are on the local path too, and a regression here hits every user.
- [ ] **Manual, remote:** `Ctrl+N` on the test VM offers **server** directories; git discovery finds **server** repos; kube contexts come from the **server** kubeconfig; a plugin installed only on the server is offered, and one installed only locally is greyed.
- [ ] **Manual, remote:** create a pane in a server directory and confirm it spawns there.
- [ ] Update `docs/roadmap/remote-daemon.md`: RD-020…RD-028 statuses; delete the "Filesystem dialogs read the local disk" and "Plugin availability is decided locally" rows from the limits table; record answers to open questions 1 and 2.

## Self-review notes

- **Spec coverage.** Covers the design spec's "Remote-correct RPCs" and "Disabled surfaces in remote mode" sections. The spec lists four RPCs; this plan has four (browse, git, kube, plugins) and explains why a fifth for Claude sessions is not needed.
- **Deliberate omission.** Notes and the F1 log viewer stay local, per the roadmap's "not planned to change". Open question 3 (notes daemon-side) is left unassigned rather than folded in.
- **Placeholder honesty.** Tasks 3, 4, 6 and 7 have step *outlines* rather than full code, unlike Tasks 1, 2 and 5. Tasks 3 and 4 are deliberate — they are structural copies of Task 1, and the plan-writing rule against "similar to Task N" is satisfied by Task 1 containing the full pattern in the same document. Tasks 6 and 7 are gated on unanswered questions, and writing code for an unchosen branch would be worse than saying so.
- **Largest risk.** Task 2. The setup dialog's rendering invariants were each added after a shipped bug, and an async field is a new way to break all of them at once. Run the whole `internal/tui` suite after every step of that task, not just at the end.
