# Memory Reporting Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Report per-pane, per-tab, and total memory consumption across three layers (daemon Go-heap, PTY child RSS, TUI local) via an F1 dialog tree, a status bar segment, and two MCP tools.

**Architecture:** Daemon owns a 5-second `memreport.Collector` that snapshots Go-heap + PTY RSS per pane into an atomic pointer. TUI polls via a new `memory_report_req/resp` IPC pair every 5 seconds, merges its own VT + notes approximation at render time, and exposes the data through a tree dialog + status segment. MCP gets two new tools (`get_memory_report`, `get_pane_memory`) that return daemon-only layers.

**Tech Stack:** Go 1.25, `charm.land/bubbletea/v2`, `charm.land/lipgloss/v2`, `golang.org/x/sys/windows` (already in go.mod via ConPTY), MCP SDK (`github.com/modelcontextprotocol/go-sdk`). No new dependencies.

**Spec:** `docs/superpowers/specs/2026-04-20-memory-reporting-design.md`

---

## Working Environment

- All Go commands run inside Docker — host has no Go toolchain. See
  `.claude/rules/dev-environment.md`.
- Full suite: `./scripts/dev.sh test` (runs `go test ./...`).
- Vet: `./scripts/dev.sh vet`.
- Targeted test (single package or pattern), use this directly:

  ```bash
  docker run --rm -v "E:/Projects/Stukans/Prototypes/calyx:/src" \
    -v quil-gomod:/go/pkg/mod -w //src golang:1.25-alpine \
    go test -run TestName ./internal/memreport/
  ```

  Replace `TestName` and the package path per task. All subsequent task steps
  reference this command template as "targeted test command".

- Never run `./scripts/kill-daemon.*` or `./scripts/reset-daemon.*` — they
  target the production `~/.quil/` daemon. See
  `.claude/rules/dev-environment.md`.

- Commit style: conventional commits (`feat(memreport): ...`,
  `test(memreport): ...`, `fix(daemon): ...`, `docs: ...`). Subject under 72
  characters. No AI coauthor line — follow repo history.

---

## File Structure

**New files:**

| File | Responsibility |
|---|---|
| `internal/memreport/human.go` | `HumanBytes(n uint64) string` — auto-scaled formatting |
| `internal/memreport/human_test.go` | Table tests for `HumanBytes` |
| `internal/memreport/types.go` | `PaneMem`, `Snapshot` value types (no behaviour) |
| `internal/memreport/collector.go` | `Collector` struct, `Run`, `Latest`, Go-heap calc |
| `internal/memreport/collector_test.go` | Unit tests for collector |
| `internal/memreport/procrss_linux.go` | `procRSSBatch` via `/proc/<pid>/status` |
| `internal/memreport/procrss_linux_test.go` | Linux-gated test |
| `internal/memreport/procrss_darwin.go` | `procRSSBatch` via `ps` shell-out |
| `internal/memreport/procrss_darwin_test.go` | Darwin-gated test |
| `internal/memreport/procrss_windows.go` | `procRSSBatch` via `GetProcessMemoryInfo` |
| `internal/memreport/procrss_windows_test.go` | Windows-gated test |
| `internal/memreport/procrss_stub.go` | `//go:build !linux && !darwin && !windows` fallback returning zeros |
| `internal/daemon/memory_ipc_test.go` | Integration: daemon + IPC round-trip |
| `internal/tui/memory.go` | Dialog tree model, status-bar helpers, TUI-local calc |
| `internal/tui/memory_test.go` | Tree flatten + render tests |

**Modified files:**

| File | Change |
|---|---|
| `internal/ipc/protocol.go` | Add `MsgMemoryReportReq`/`Resp` constants + payload types |
| `internal/daemon/daemon.go` | Construct `Collector`, start goroutine, register IPC handler |
| `internal/tui/dialog.go` | Add `"Memory"` About item + `dialogMemory` screen |
| `internal/tui/model.go` | `memoryTickMsg`, `lastMemSnap`, status-bar segment |
| `internal/tui/editor.go` | `TextEditor.ApproxBytes()` |
| `cmd/quil/mcp_tools.go` | Register `get_memory_report` + `get_pane_memory` |
| `.claude/CLAUDE.md` | Document Memory dialog entry |

---

## Task 1: `HumanBytes` helper

**Files:**
- Create: `internal/memreport/human.go`
- Test: `internal/memreport/human_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/memreport/human_test.go`:

```go
package memreport

import "testing"

func TestHumanBytes(t *testing.T) {
	tests := []struct {
		name string
		in   uint64
		want string
	}{
		{"zero", 0, "0 B"},
		{"bytes", 512, "512 B"},
		{"kb_boundary_low", 1023, "1023 B"},
		{"kb_boundary_high", 1024, "1.0 KB"},
		{"kb_mid", 1536, "1.5 KB"},
		{"mb", 4_400_000, "4.2 MB"},
		{"gb", 1_503_238_553, "1.4 GB"},
		{"tb_cap", 1_500_000_000_000, "1.4 TB"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := HumanBytes(tt.in)
			if got != tt.want {
				t.Errorf("HumanBytes(%d) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run targeted test command with `-run TestHumanBytes ./internal/memreport/`.
Expected: FAIL — package does not exist yet.

- [ ] **Step 3: Write minimal implementation**

Create `internal/memreport/human.go`:

```go
// Package memreport collects per-pane memory snapshots for the daemon and
// exposes a human-readable formatter used by the TUI, status bar, and MCP
// tools.
package memreport

import "fmt"

// HumanBytes renders a byte count using the largest unit whose integer part
// is non-zero. One decimal place for values ≥ 1 KB, no fractional part for
// raw bytes. Output is ASCII-safe (no multibyte characters).
func HumanBytes(n uint64) string {
	const (
		kb = 1024
		mb = 1024 * kb
		gb = 1024 * mb
		tb = 1024 * gb
	)
	switch {
	case n >= tb:
		return fmt.Sprintf("%.1f TB", float64(n)/float64(tb))
	case n >= gb:
		return fmt.Sprintf("%.1f GB", float64(n)/float64(gb))
	case n >= mb:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(mb))
	case n >= kb:
		return fmt.Sprintf("%.1f KB", float64(n)/float64(kb))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run targeted test command with `-run TestHumanBytes ./internal/memreport/`.
Expected: PASS (8/8 cases).

- [ ] **Step 5: Commit**

```bash
git add internal/memreport/human.go internal/memreport/human_test.go
git commit -m "feat(memreport): add HumanBytes auto-scaled byte formatter"
```

---

## Task 2: Snapshot value types

**Files:**
- Create: `internal/memreport/types.go`

This task has no test — the file is declarations only. Compilation is the check.

- [ ] **Step 1: Write the types**

Create `internal/memreport/types.go`:

```go
package memreport

import "time"

// PaneMem is a single pane's daemon-side memory accounting. TUI-side bytes
// (VT grid, notes editor) are computed by the TUI at render time and are not
// part of this struct.
type PaneMem struct {
	PaneID      string
	TabID       string
	GoHeapBytes uint64
	PTYRSSBytes uint64
	Total       uint64 // GoHeapBytes + PTYRSSBytes
}

// Snapshot is the collector's output, refreshed every ~5 s. Readers must
// treat it as immutable — the collector replaces the pointer atomically
// rather than mutating in place.
type Snapshot struct {
	At    time.Time
	Panes []PaneMem // sorted by Total desc
	Total uint64    // sum of Panes[*].Total
}
```

- [ ] **Step 2: Verify it compiles**

Run: `./scripts/dev.sh vet`
Expected: no errors.

- [ ] **Step 3: Commit**

```bash
git add internal/memreport/types.go
git commit -m "feat(memreport): add PaneMem and Snapshot value types"
```

---

## Task 3: Linux `procRSSBatch`

**Files:**
- Create: `internal/memreport/procrss_linux.go`
- Test: `internal/memreport/procrss_linux_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/memreport/procrss_linux_test.go`:

```go
//go:build linux

package memreport

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestProcRSSBatch_Linux_SelfPID(t *testing.T) {
	self := os.Getpid()
	got := procRSSBatch([]int{self})
	rss, ok := got[self]
	if !ok {
		t.Fatalf("procRSSBatch did not return an entry for self pid %d", self)
	}
	if rss == 0 {
		t.Errorf("procRSSBatch(self) = 0, want > 0")
	}
	if rss > 5*1024*1024*1024 { // 5 GB ceiling sanity
		t.Errorf("procRSSBatch(self) = %d, unexpectedly large", rss)
	}
}

func TestProcRSSBatch_Linux_Child(t *testing.T) {
	cmd := exec.Command("sleep", "2")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	// Give /proc/<pid>/status a moment to populate VmRSS.
	time.Sleep(50 * time.Millisecond)

	got := procRSSBatch([]int{cmd.Process.Pid})
	rss := got[cmd.Process.Pid]
	if rss == 0 {
		t.Errorf("procRSSBatch(child) = 0, want > 0")
	}
}

func TestProcRSSBatch_Linux_NonexistentPID(t *testing.T) {
	got := procRSSBatch([]int{2_147_483_647}) // very high PID unlikely to exist
	if rss, ok := got[2_147_483_647]; ok && rss != 0 {
		t.Errorf("nonexistent PID got rss=%d, want missing or 0", rss)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run targeted test command with `-run TestProcRSSBatch_Linux ./internal/memreport/`.
Expected: FAIL — `procRSSBatch` undefined.

- [ ] **Step 3: Write minimal implementation**

Create `internal/memreport/procrss_linux.go`:

```go
//go:build linux

package memreport

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// procRSSBatch reads /proc/<pid>/status for each pid and returns a map from
// pid to resident set size in bytes. PIDs whose status cannot be read (e.g.,
// the process exited between the pane's ExitCode check and the RSS read)
// are omitted from the map — callers treat missing entries as zero.
func procRSSBatch(pids []int) map[int]uint64 {
	out := make(map[int]uint64, len(pids))
	for _, pid := range pids {
		if rss, ok := readVmRSS(pid); ok {
			out[pid] = rss
		}
	}
	return out
}

// readVmRSS parses the VmRSS line of /proc/<pid>/status. VmRSS is reported
// in kilobytes per `man 5 proc`.
func readVmRSS(pid int) (uint64, bool) {
	path := fmt.Sprintf("/proc/%d/status", pid)
	f, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "VmRSS:") {
			continue
		}
		// Format: "VmRSS:\t    1234 kB"
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0, false
		}
		kb, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0, false
		}
		return kb * 1024, true
	}
	return 0, false
}
```

- [ ] **Step 4: Run test to verify it passes**

Run targeted test command with `-run TestProcRSSBatch_Linux ./internal/memreport/`.
Expected: PASS (3 cases) on Linux runner. On non-Linux host, the file is
skipped by build tags — no test runs.

- [ ] **Step 5: Commit**

```bash
git add internal/memreport/procrss_linux.go internal/memreport/procrss_linux_test.go
git commit -m "feat(memreport): add Linux procRSSBatch via /proc/<pid>/status"
```

---

## Task 4: Darwin `procRSSBatch`

**Files:**
- Create: `internal/memreport/procrss_darwin.go`
- Test: `internal/memreport/procrss_darwin_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/memreport/procrss_darwin_test.go`:

```go
//go:build darwin

package memreport

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestProcRSSBatch_Darwin_SelfPID(t *testing.T) {
	self := os.Getpid()
	got := procRSSBatch([]int{self})
	rss, ok := got[self]
	if !ok {
		t.Fatalf("procRSSBatch did not return an entry for self pid %d", self)
	}
	if rss == 0 {
		t.Errorf("procRSSBatch(self) = 0, want > 0")
	}
}

func TestProcRSSBatch_Darwin_BatchMultiple(t *testing.T) {
	c1 := exec.Command("sleep", "2")
	c2 := exec.Command("sleep", "2")
	if err := c1.Start(); err != nil {
		t.Fatalf("start c1: %v", err)
	}
	if err := c2.Start(); err != nil {
		t.Fatalf("start c2: %v", err)
	}
	defer func() {
		_ = c1.Process.Kill()
		_ = c2.Process.Kill()
		_ = c1.Wait()
		_ = c2.Wait()
	}()
	time.Sleep(50 * time.Millisecond)

	got := procRSSBatch([]int{c1.Process.Pid, c2.Process.Pid})
	if got[c1.Process.Pid] == 0 || got[c2.Process.Pid] == 0 {
		t.Errorf("expected non-zero RSS for both children, got %v", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

On Darwin, run targeted test command with `-run TestProcRSSBatch_Darwin ./internal/memreport/`.
Expected: FAIL — function undefined.

(If the runner is not Darwin, skip and check that the file still compiles
across platforms via `./scripts/dev.sh vet`.)

- [ ] **Step 3: Write minimal implementation**

Create `internal/memreport/procrss_darwin.go`:

```go
//go:build darwin

package memreport

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// procRSSBatch invokes `ps -o pid=,rss= -p <pid,pid,...>` with a 2 s
// timeout. RSS is reported in kilobytes; we convert to bytes. PIDs missing
// from the output are omitted from the returned map.
func procRSSBatch(pids []int) map[int]uint64 {
	if len(pids) == 0 {
		return map[int]uint64{}
	}
	parts := make([]string, len(pids))
	for i, p := range pids {
		parts[i] = strconv.Itoa(p)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ps", "-o", "pid=,rss=", "-p", strings.Join(parts, ","))
	output, err := cmd.Output()
	if err != nil {
		return map[int]uint64{}
	}

	out := make(map[int]uint64, len(pids))
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		pid, err1 := strconv.Atoi(fields[0])
		kb, err2 := strconv.ParseUint(fields[1], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		out[pid] = kb * 1024
	}
	return out
}
```

- [ ] **Step 4: Run test to verify it passes**

On Darwin, run targeted test command with `-run TestProcRSSBatch_Darwin ./internal/memreport/`.
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/memreport/procrss_darwin.go internal/memreport/procrss_darwin_test.go
git commit -m "feat(memreport): add Darwin procRSSBatch via ps shell-out"
```

---

## Task 5: Windows `procRSSBatch`

**Files:**
- Create: `internal/memreport/procrss_windows.go`
- Test: `internal/memreport/procrss_windows_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/memreport/procrss_windows_test.go`:

```go
//go:build windows

package memreport

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestProcRSSBatch_Windows_SelfPID(t *testing.T) {
	self := os.Getpid()
	got := procRSSBatch([]int{self})
	rss := got[self]
	if rss == 0 {
		t.Errorf("procRSSBatch(self) = 0, want > 0")
	}
}

func TestProcRSSBatch_Windows_Child(t *testing.T) {
	// `timeout /t 3 /nobreak` is a Windows sleep equivalent.
	cmd := exec.Command("cmd", "/c", "timeout", "/t", "3", "/nobreak")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	time.Sleep(100 * time.Millisecond)

	got := procRSSBatch([]int{cmd.Process.Pid})
	if got[cmd.Process.Pid] == 0 {
		t.Errorf("procRSSBatch(child) = 0, want > 0")
	}
}

func TestProcRSSBatch_Windows_InvalidPID(t *testing.T) {
	got := procRSSBatch([]int{2_147_483_647})
	if rss := got[2_147_483_647]; rss != 0 {
		t.Errorf("invalid PID got rss=%d, want 0 (missing)", rss)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

On Windows, run targeted test command with `-run TestProcRSSBatch_Windows ./internal/memreport/`.
Expected: FAIL — function undefined.

- [ ] **Step 3: Write minimal implementation**

Create `internal/memreport/procrss_windows.go`:

```go
//go:build windows

package memreport

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// processMemoryCounters mirrors PROCESS_MEMORY_COUNTERS from psapi.h.
// Only WorkingSetSize is consumed here.
type processMemoryCounters struct {
	CB                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
}

var (
	modpsapi                    = windows.NewLazySystemDLL("psapi.dll")
	procGetProcessMemoryInfo    = modpsapi.NewProc("GetProcessMemoryInfo")
	processQueryLimitedInfoFlag = uint32(0x1000) // PROCESS_QUERY_LIMITED_INFORMATION
)

func procRSSBatch(pids []int) map[int]uint64 {
	out := make(map[int]uint64, len(pids))
	for _, pid := range pids {
		if rss, ok := getWorkingSet(uint32(pid)); ok {
			out[pid] = rss
		}
	}
	return out
}

func getWorkingSet(pid uint32) (uint64, bool) {
	h, err := windows.OpenProcess(processQueryLimitedInfoFlag, false, pid)
	if err != nil {
		return 0, false
	}
	defer windows.CloseHandle(h)

	var pmc processMemoryCounters
	pmc.CB = uint32(unsafe.Sizeof(pmc))
	r, _, _ := procGetProcessMemoryInfo.Call(
		uintptr(h),
		uintptr(unsafe.Pointer(&pmc)),
		uintptr(pmc.CB),
	)
	if r == 0 {
		return 0, false
	}
	return uint64(pmc.WorkingSetSize), true
}
```

- [ ] **Step 4: Run test to verify it passes**

On Windows, run targeted test command with `-run TestProcRSSBatch_Windows ./internal/memreport/`.
Expected: PASS (3 cases).

- [ ] **Step 5: Commit**

```bash
git add internal/memreport/procrss_windows.go internal/memreport/procrss_windows_test.go
git commit -m "feat(memreport): add Windows procRSSBatch via GetProcessMemoryInfo"
```

---

## Task 6: Stub `procRSSBatch` for unsupported platforms

**Files:**
- Create: `internal/memreport/procrss_stub.go`

This satisfies the compiler on any GOOS not covered by the three previous
files (e.g., freebsd). No test — the file is one function returning an empty
map.

- [ ] **Step 1: Write the stub**

Create `internal/memreport/procrss_stub.go`:

```go
//go:build !linux && !darwin && !windows

package memreport

// procRSSBatch is a no-op on platforms without a dedicated implementation.
// The daemon reports 0 PTY RSS for every pane on such platforms.
func procRSSBatch(pids []int) map[int]uint64 {
	return map[int]uint64{}
}
```

- [ ] **Step 2: Verify it compiles cross-platform**

Run: `./scripts/dev.sh vet`
Expected: no errors.

- [ ] **Step 3: Commit**

```bash
git add internal/memreport/procrss_stub.go
git commit -m "feat(memreport): add procRSSBatch stub for unsupported platforms"
```

---

## Task 7: Collector core (Go-heap only, no ticker yet)

**Files:**
- Create: `internal/memreport/collector.go`
- Create: `internal/memreport/collector_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/memreport/collector_test.go`:

```go
package memreport

import (
	"testing"

	ringbuf "github.com/artyomsv/quil/internal/ringbuf"
)

// paneView is a minimal interface the collector uses to read pane state;
// the daemon's *daemon.Pane satisfies it. We use a simple fake here to
// avoid pulling daemon into a testing dep.
type paneView struct {
	ID          string
	TabID       string
	OutputBuf   *ringbuf.RingBuffer
	GhostSnap   []byte
	PluginState map[string]string
	PID         int
	Alive       bool
}

func (p *paneView) paneID() string  { return p.ID }
func (p *paneView) tabID() string   { return p.TabID }
func (p *paneView) outputBuf() *ringbuf.RingBuffer { return p.OutputBuf }
func (p *paneView) ghostSnap() []byte { return p.GhostSnap }
func (p *paneView) pluginState() map[string]string { return p.PluginState }
func (p *paneView) pid() int         { return p.PID }
func (p *paneView) alive() bool      { return p.Alive }

func TestCollector_GoHeapOnly(t *testing.T) {
	rb := ringbuf.NewRingBuffer(1024)
	rb.Write([]byte("hello world")) // 11 bytes
	p := &paneView{
		ID:        "p1",
		TabID:     "t1",
		OutputBuf: rb,
		GhostSnap: make([]byte, 100),
		PluginState: map[string]string{
			"session_id": "abc",
		},
		PID:   0,
		Alive: false,
	}
	snap := collectFrom([]paneSource{p}, func(pids []int) map[int]uint64 { return nil })
	if len(snap.Panes) != 1 {
		t.Fatalf("got %d panes, want 1", len(snap.Panes))
	}
	// 11 (OutputBuf) + 100 (GhostSnap) + len("session_id")=10 + len("abc")=3 = 124
	if got := snap.Panes[0].GoHeapBytes; got != 124 {
		t.Errorf("GoHeapBytes = %d, want 124", got)
	}
	if snap.Panes[0].PTYRSSBytes != 0 {
		t.Errorf("exited pane RSS = %d, want 0", snap.Panes[0].PTYRSSBytes)
	}
	if snap.Total != 124 {
		t.Errorf("Total = %d, want 124", snap.Total)
	}
}

func TestCollector_TotalAndSort(t *testing.T) {
	mk := func(id string, heap uint64) paneSource {
		rb := ringbuf.NewRingBuffer(int(heap) + 16)
		rb.Write(make([]byte, heap))
		return &paneView{
			ID: id, TabID: "t1", OutputBuf: rb, Alive: false,
		}
	}
	snap := collectFrom([]paneSource{mk("small", 10), mk("big", 1000), mk("mid", 100)},
		func(pids []int) map[int]uint64 { return nil })
	if len(snap.Panes) != 3 {
		t.Fatalf("got %d panes", len(snap.Panes))
	}
	if snap.Panes[0].PaneID != "big" || snap.Panes[2].PaneID != "small" {
		t.Errorf("sort order wrong: %+v", snap.Panes)
	}
	if snap.Total != 1110 {
		t.Errorf("Total = %d, want 1110", snap.Total)
	}
}

func TestCollector_LatestBeforeRun(t *testing.T) {
	c := &Collector{}
	if snap := c.Latest(); snap != nil {
		t.Errorf("Latest() before Run() = %+v, want nil", snap)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run targeted test command with `-run TestCollector ./internal/memreport/`.
Expected: FAIL — types and functions undefined.

- [ ] **Step 3: Write the collector**

Create `internal/memreport/collector.go`:

```go
package memreport

import (
	"context"
	"sort"
	"sync/atomic"
	"time"

	ringbuf "github.com/artyomsv/quil/internal/ringbuf"
)

// paneSource is the minimal view the collector needs over a daemon pane.
// Keeping this as an interface lets us unit-test without pulling in the
// daemon package (which would create a cycle).
type paneSource interface {
	paneID() string
	tabID() string
	outputBuf() *ringbuf.RingBuffer
	ghostSnap() []byte
	pluginState() map[string]string
	pid() int
	alive() bool
}

// PaneLister is implemented by *daemon.SessionManager; the collector calls
// it each tick to enumerate current panes.
type PaneLister interface {
	SnapshotPanes() []paneSource
}

// Collector periodically scans a SessionManager and stores an atomic
// snapshot of per-pane memory usage.
type Collector struct {
	lister PaneLister
	every  time.Duration
	last   atomic.Pointer[Snapshot]
	busy   atomic.Bool
}

// NewCollector constructs a Collector but does not start it. Call Run in a
// goroutine.
func NewCollector(lister PaneLister, every time.Duration) *Collector {
	if every <= 0 {
		every = 5 * time.Second
	}
	return &Collector{lister: lister, every: every}
}

// Run blocks until ctx is cancelled, performing one collection up front so
// Latest() is never nil afterwards, then on every tick.
func (c *Collector) Run(ctx context.Context) {
	c.collect()
	t := time.NewTicker(c.every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.collect()
		}
	}
}

// Latest returns the most recent Snapshot, or nil if Run has not yet
// completed its first pass.
func (c *Collector) Latest() *Snapshot {
	return c.last.Load()
}

func (c *Collector) collect() {
	// If a previous tick is still running (unlikely at 5 s cadence, but
	// possible under heavy load), skip this tick.
	if !c.busy.CompareAndSwap(false, true) {
		return
	}
	defer c.busy.Store(false)

	panes := c.lister.SnapshotPanes()
	snap := collectFrom(panes, procRSSBatch)
	c.last.Store(&snap)
}

// collectFrom is the pure core, exported for testing via the paneSource
// abstraction. rssFn is injected so tests can stub procRSSBatch.
func collectFrom(panes []paneSource, rssFn func([]int) map[int]uint64) Snapshot {
	// Gather alive PIDs for a single batched RSS query.
	alivePIDs := make([]int, 0, len(panes))
	for _, p := range panes {
		if p.alive() && p.pid() > 0 {
			alivePIDs = append(alivePIDs, p.pid())
		}
	}
	rss := rssFn(alivePIDs)
	if rss == nil {
		rss = map[int]uint64{}
	}

	result := Snapshot{
		At:    time.Now(),
		Panes: make([]PaneMem, 0, len(panes)),
	}

	for _, p := range panes {
		heap := uint64(0)
		if buf := p.outputBuf(); buf != nil {
			heap += uint64(len(buf.Bytes()))
		}
		heap += uint64(len(p.ghostSnap()))
		for k, v := range p.pluginState() {
			heap += uint64(len(k) + len(v))
		}

		var paneRSS uint64
		if p.alive() && p.pid() > 0 {
			paneRSS = rss[p.pid()]
		}

		pm := PaneMem{
			PaneID:      p.paneID(),
			TabID:       p.tabID(),
			GoHeapBytes: heap,
			PTYRSSBytes: paneRSS,
			Total:       heap + paneRSS,
		}
		result.Panes = append(result.Panes, pm)
		result.Total += pm.Total
	}

	sort.Slice(result.Panes, func(i, j int) bool {
		return result.Panes[i].Total > result.Panes[j].Total
	})
	return result
}
```

- [ ] **Step 4: Run test to verify it passes**

Run targeted test command with `-run TestCollector ./internal/memreport/`.
Expected: PASS (3 cases).

- [ ] **Step 5: Commit**

```bash
git add internal/memreport/collector.go internal/memreport/collector_test.go
git commit -m "feat(memreport): add Collector with atomic snapshot and per-pane Go-heap"
```

---

## Task 8: Collector ticker + concurrency test

**Files:**
- Modify: `internal/memreport/collector_test.go`

The collector already has `Run`. This task adds a focused test on the ticker
and the atomic pointer under concurrent readers.

- [ ] **Step 1: Write the failing test**

Append to `internal/memreport/collector_test.go`:

```go
import (
	"context"
	"sync"
	"time"
)

type stubLister struct {
	panes []paneSource
}

func (s *stubLister) SnapshotPanes() []paneSource { return s.panes }

func TestCollector_RunPopulatesLatest(t *testing.T) {
	l := &stubLister{panes: nil}
	c := NewCollector(l, 50*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)

	// First collection runs synchronously at Run entry; Latest should
	// become non-nil almost immediately.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if c.Latest() != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Latest() never became non-nil")
}

func TestCollector_ConcurrentReaders(t *testing.T) {
	l := &stubLister{panes: nil}
	c := NewCollector(l, 10*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)

	// Wait for first snapshot.
	for i := 0; i < 50 && c.Latest() == nil; i++ {
		time.Sleep(10 * time.Millisecond)
	}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = c.Latest()
			}
		}()
	}
	wg.Wait()
}
```

- [ ] **Step 2: Run with race detector**

Run: `./scripts/dev.sh test-race`
Expected: PASS on the two new tests, no race warnings.

- [ ] **Step 3: Commit**

```bash
git add internal/memreport/collector_test.go
git commit -m "test(memreport): add Run lifecycle and concurrent-reader tests"
```

---

## Task 9: IPC protocol additions

**Files:**
- Modify: `internal/ipc/protocol.go:45-66` (add new constants in the
  request-response block) and append new payload types near the notification
  payloads

- [ ] **Step 1: Add the constants**

In `internal/ipc/protocol.go`, inside the request-response block (after the
notification constants), add:

```go
	// Memory reporting
	MsgMemoryReportReq  = "memory_report_req"
	MsgMemoryReportResp = "memory_report_resp"
```

- [ ] **Step 2: Add the payload types**

At the end of `internal/ipc/protocol.go`, before `NewMessage`, add:

```go
// Memory reporting payloads

type MemoryReportReqPayload struct{}

// PaneMemInfo is the wire form of a single pane's daemon-side memory.
// TUI-local memory is not part of the wire format — the TUI merges its own
// values at render time.
type PaneMemInfo struct {
	PaneID      string `json:"pane_id"`
	TabID       string `json:"tab_id"`
	GoHeapBytes uint64 `json:"go_heap_bytes"`
	PTYRSSBytes uint64 `json:"pty_rss_bytes"`
	TotalBytes  uint64 `json:"total_bytes"`
}

type MemoryReportRespPayload struct {
	SnapshotAt int64         `json:"snapshot_at"` // Unix nanoseconds
	Panes      []PaneMemInfo `json:"panes"`
	Total      uint64        `json:"total"`
}
```

- [ ] **Step 3: Verify it compiles**

Run: `./scripts/dev.sh vet`
Expected: no errors.

- [ ] **Step 4: Commit**

```bash
git add internal/ipc/protocol.go
git commit -m "feat(ipc): add memory_report_req/resp message types and payloads"
```

---

## Task 10: Daemon wiring — adapt paneSource, start collector, register handler

**Files:**
- Modify: `internal/daemon/daemon.go`
- Modify: `internal/daemon/session.go` (add `SnapshotPanes` method + adapter)

- [ ] **Step 1: Write the failing integration test first**

Create `internal/daemon/memory_ipc_test.go`:

```go
package daemon_test

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/daemon"
	"github.com/artyomsv/quil/internal/ipc"
)

// Smoke test: boot a daemon on a temp socket, ask for a memory report,
// assert the response is well-formed. Uses the project's existing helper
// patterns — see daemon_test.go for the full set-up idiom.
func TestDaemon_MemoryReportRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	tmp := t.TempDir()
	t.Setenv("QUIL_HOME", tmp)

	cfg := config.Default()
	sockPath := filepath.Join(tmp, "quild.sock")
	d, err := daemon.NewDaemon(cfg, sockPath, "", "test")
	if err != nil {
		t.Fatalf("NewDaemon: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Start(ctx)

	// Wait for socket.
	deadline := time.Now().Add(2 * time.Second)
	var conn net.Conn
	for time.Now().Before(deadline) {
		c, err := net.Dial("unix", sockPath)
		if err == nil {
			conn = c
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if conn == nil {
		t.Fatalf("socket %s never became connectable", sockPath)
	}
	defer conn.Close()

	// Let the collector run at least once.
	time.Sleep(100 * time.Millisecond)

	payload, err := json.Marshal(ipc.MemoryReportReqPayload{})
	if err != nil {
		t.Fatal(err)
	}
	req := &ipc.Message{Type: ipc.MsgMemoryReportReq, ID: "t1", Payload: payload}
	if err := ipc.WriteMessage(conn, req); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Allow a generous deadline for the daemon to respond.
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	resp, err := ipc.ReadMessage(conn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if resp.Type != ipc.MsgMemoryReportResp {
		t.Fatalf("resp type = %s, want %s", resp.Type, ipc.MsgMemoryReportResp)
	}
	if resp.ID != "t1" {
		t.Errorf("resp id = %s, want t1", resp.ID)
	}
	var out ipc.MemoryReportRespPayload
	if err := json.Unmarshal(resp.Payload, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.SnapshotAt == 0 {
		t.Errorf("SnapshotAt = 0, want non-zero")
	}

	// Cleanup — make linter happy.
	_ = os.Remove(sockPath)
}
```

- [ ] **Step 2: Add `SnapshotPanes` adapter on SessionManager**

In `internal/daemon/session.go` (after `type SessionManager struct { ... }`),
add:

```go
// paneSourceAdapter adapts *Pane to the memreport.paneSource interface
// without the memreport package needing to import daemon.
type paneSourceAdapter struct{ p *Pane }

func (a paneSourceAdapter) paneID() string                       { return a.p.ID }
func (a paneSourceAdapter) tabID() string                        { return a.p.TabID }
func (a paneSourceAdapter) outputBuf() *ringbuf.RingBuffer       { return a.p.OutputBuf }
func (a paneSourceAdapter) ghostSnap() []byte                    { return a.p.GhostSnap }
func (a paneSourceAdapter) pluginState() map[string]string {
	a.p.PluginMu.Lock()
	defer a.p.PluginMu.Unlock()
	// Return a shallow copy so the collector can read safely after the
	// lock is released.
	out := make(map[string]string, len(a.p.PluginState))
	for k, v := range a.p.PluginState {
		out[k] = v
	}
	return out
}
func (a paneSourceAdapter) pid() int {
	if a.p.PTY == nil {
		return 0
	}
	return a.p.PTY.Pid()
}
func (a paneSourceAdapter) alive() bool {
	return a.p.ExitCode == nil
}

// PaneSourcesLocked returns an unordered slice of adapters, one per live
// pane. Callers must not retain the returned slice beyond the current
// collection cycle — the Pane pointers it holds may be mutated.
func (sm *SessionManager) PaneSources() []memreport.PaneSource {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	out := make([]memreport.PaneSource, 0, len(sm.panes))
	for _, p := range sm.panes {
		out = append(out, paneSourceAdapter{p: p})
	}
	return out
}
```

NB: this requires `memreport.PaneSource` (exported). Rename the interface in
`internal/memreport/collector.go` from lower-case `paneSource` to exported
`PaneSource` AND change its methods to exported names (`PaneID`, `TabID`,
`OutputBuf`, `GhostSnap`, `PluginState`, `PID`, `Alive`). Update
`collect_test.go` and `collectFrom` signatures accordingly.

Also update `SnapshotPanes()` (that was my earlier signature) — we call it
`PaneSources()` instead to match Go getter style.

Update Collector's `PaneLister` interface correspondingly:

```go
type PaneLister interface {
	PaneSources() []PaneSource
}
```

And its `collect()` body:
```go
panes := c.lister.PaneSources()
```

(Do this rename as part of this task so the daemon wiring compiles.)

- [ ] **Step 3: Wire the collector into the daemon**

In `internal/daemon/daemon.go`:

1. Add import for `memreport "github.com/artyomsv/quil/internal/memreport"`.
2. Add a field to `type Daemon struct`:
   ```go
   memReport *memreport.Collector
   ```
3. In `NewDaemon`, after the session manager is constructed, add:
   ```go
   d.memReport = memreport.NewCollector(d.session, 5*time.Second)
   ```
4. In `Start`, after launching the other background goroutines, add:
   ```go
   go d.memReport.Run(ctx)
   ```
5. Add a handler in the IPC dispatch switch. Locate the handler block in
   `daemon.go` (alongside the other `Msg*Req` cases) and add:
   ```go
   case ipc.MsgMemoryReportReq:
       d.handleMemoryReportReq(msg, conn)
   ```
6. Add the handler function at the bottom of `daemon.go`:

   ```go
   func (d *Daemon) handleMemoryReportReq(msg *ipc.Message, conn *ipc.Conn) {
       snap := d.memReport.Latest()
       resp := ipc.MemoryReportRespPayload{}
       if snap != nil {
           resp.SnapshotAt = snap.At.UnixNano()
           resp.Total = snap.Total
           resp.Panes = make([]ipc.PaneMemInfo, len(snap.Panes))
           for i, p := range snap.Panes {
               resp.Panes[i] = ipc.PaneMemInfo{
                   PaneID:      p.PaneID,
                   TabID:       p.TabID,
                   GoHeapBytes: p.GoHeapBytes,
                   PTYRSSBytes: p.PTYRSSBytes,
                   TotalBytes:  p.Total,
               }
           }
       }
       out, err := ipc.NewMessage(ipc.MsgMemoryReportResp, resp)
       if err != nil {
           log.Printf("memory_report_resp marshal: %v", err)
           return
       }
       out.ID = msg.ID
       if err := conn.Write(out); err != nil {
           log.Printf("memory_report_resp write: %v", err)
       }
   }
   ```

   (Match the `conn.Write` / `ipc.WriteMessage` pattern used by the
   neighbouring handlers — if they use `ipc.WriteMessage(conn.Conn(), out)`
   use the same.)

- [ ] **Step 4: Run the integration test**

Run targeted test command with `-run TestDaemon_MemoryReportRoundTrip ./internal/daemon/`.
Expected: PASS.

- [ ] **Step 5: Run the full suite**

Run: `./scripts/dev.sh test`
Expected: PASS (existing tests unaffected; new tests green).

- [ ] **Step 6: Commit**

```bash
git add internal/daemon/daemon.go internal/daemon/session.go \
        internal/daemon/memory_ipc_test.go \
        internal/memreport/collector.go internal/memreport/collector_test.go
git commit -m "feat(daemon): wire memreport.Collector and MsgMemoryReportReq handler"
```

---

## Task 11: TextEditor.ApproxBytes

**Files:**
- Modify: `internal/tui/editor.go`
- Test: add a small test in `internal/tui/editor_test.go` (or create the file
  if it doesn't yet exist)

- [ ] **Step 1: Write the failing test**

In `internal/tui/editor_test.go` (create if missing; use existing package
`tui`):

```go
package tui

import "testing"

func TestTextEditor_ApproxBytes(t *testing.T) {
	ed := NewTextEditor()
	ed.SetLines([]string{"hello", "world!"})
	// 5 + 6 + 1 newline = 12
	if got := ed.ApproxBytes(); got != 12 {
		t.Errorf("ApproxBytes = %d, want 12", got)
	}

	ed.SetLines(nil)
	if got := ed.ApproxBytes(); got != 0 {
		t.Errorf("ApproxBytes(empty) = %d, want 0", got)
	}
}
```

If `NewTextEditor` or `SetLines` have different names in this codebase, adapt
the test to the real public surface — run
`grep -n "func (e \*TextEditor)" internal/tui/editor.go` first to confirm,
and use whichever method populates the buffer. If `SetLines` is not public,
add it as a tiny helper (one-line `func (e *TextEditor) SetLines(lines []string) { e.lines = lines }`).

- [ ] **Step 2: Run test to verify it fails**

Run targeted test command with `-run TestTextEditor_ApproxBytes ./internal/tui/`.
Expected: FAIL — `ApproxBytes` undefined.

- [ ] **Step 3: Add the method**

In `internal/tui/editor.go`, near the other `TextEditor` methods, add:

```go
// ApproxBytes returns a lower-bound estimate of the editor's in-memory size.
// The value counts UTF-8 byte length of every line plus one newline per line
// boundary; it does not account for Go slice overhead or unused capacity.
// Used by the Memory dialog — precision is not important, only ordering.
func (e *TextEditor) ApproxBytes() uint64 {
	if e == nil {
		return 0
	}
	var n uint64
	for _, line := range e.lines {
		n += uint64(len(line))
	}
	if len(e.lines) > 1 {
		n += uint64(len(e.lines) - 1) // newlines
	}
	return n
}
```

(If `e.lines` is named differently, use the actual field name — again, grep
first.)

- [ ] **Step 4: Run test to verify it passes**

Run targeted test command with `-run TestTextEditor_ApproxBytes ./internal/tui/`.
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/editor.go internal/tui/editor_test.go
git commit -m "feat(tui): add TextEditor.ApproxBytes for memory accounting"
```

---

## Task 12: TUI memory model — tree flatten + local calc

**Files:**
- Create: `internal/tui/memory.go`
- Create: `internal/tui/memory_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/tui/memory_test.go`:

```go
package tui

import (
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
)

func TestMemoryTree_FlattenAllCollapsed(t *testing.T) {
	resp := ipc.MemoryReportRespPayload{
		Panes: []ipc.PaneMemInfo{
			{PaneID: "p1", TabID: "tA", TotalBytes: 100},
			{PaneID: "p2", TabID: "tA", TotalBytes: 200},
			{PaneID: "p3", TabID: "tB", TotalBytes: 50},
		},
		Total: 350,
	}
	tabOrder := []string{"tA", "tB"}
	tabNames := map[string]string{"tA": "Shell", "tB": "Build"}
	tree := buildMemoryTree(resp, tabOrder, tabNames)

	// All tabs start collapsed — only top-line + tab rows visible.
	rows := tree.flatten()
	// 1 total + 2 tab rows = 3
	if len(rows) != 3 {
		t.Errorf("flatten(collapsed) = %d rows, want 3", len(rows))
	}
	// tA total = 300, tB total = 50. tA must come first (Total desc).
	if rows[1].label != "Shell" || rows[2].label != "Build" {
		t.Errorf("tab order wrong: %q, %q", rows[1].label, rows[2].label)
	}
}

func TestMemoryTree_ExpandTab(t *testing.T) {
	resp := ipc.MemoryReportRespPayload{
		Panes: []ipc.PaneMemInfo{
			{PaneID: "p1", TabID: "tA", TotalBytes: 100},
			{PaneID: "p2", TabID: "tA", TotalBytes: 200},
		},
		Total: 300,
	}
	tree := buildMemoryTree(resp, []string{"tA"}, map[string]string{"tA": "Shell"})
	tree.toggleAt(1) // expand tA
	rows := tree.flatten()
	// 1 total + 1 tab + 2 panes = 4
	if len(rows) != 4 {
		t.Errorf("flatten(expanded) = %d rows, want 4", len(rows))
	}
	// panes sorted by Total desc — p2 first.
	if rows[2].label != "p2" || rows[3].label != "p1" {
		t.Errorf("pane order wrong: %q, %q", rows[2].label, rows[3].label)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run targeted test command with `-run TestMemoryTree ./internal/tui/`.
Expected: FAIL — types/functions undefined.

- [ ] **Step 3: Write the model**

Create `internal/tui/memory.go`:

```go
package tui

import (
	"sort"

	"github.com/artyomsv/quil/internal/ipc"
)

// memoryRow is one visible line in the tree view.
type memoryRow struct {
	kind     memRowKind
	tabID    string
	paneID   string
	label    string
	goHeap   uint64
	ptyRSS   uint64
	tui      uint64
	total    uint64
}

type memRowKind int

const (
	memRowTotal memRowKind = iota
	memRowTab
	memRowPane
)

type memoryTabNode struct {
	id       string
	name     string
	total    uint64
	panes    []ipc.PaneMemInfo // sorted by TotalBytes desc
	expanded bool
}

type memoryTree struct {
	snapshot ipc.MemoryReportRespPayload
	tabs     []*memoryTabNode
}

// buildMemoryTree groups panes by TabID using the supplied tab order and
// names. Tabs missing from tabOrder are appended at the end in TabID order.
func buildMemoryTree(resp ipc.MemoryReportRespPayload, tabOrder []string, tabNames map[string]string) *memoryTree {
	byTab := make(map[string]*memoryTabNode)
	for _, p := range resp.Panes {
		node, ok := byTab[p.TabID]
		if !ok {
			node = &memoryTabNode{id: p.TabID, name: tabNames[p.TabID]}
			if node.name == "" {
				node.name = p.TabID
			}
			byTab[p.TabID] = node
		}
		node.panes = append(node.panes, p)
		node.total += p.TotalBytes
	}
	for _, node := range byTab {
		sort.Slice(node.panes, func(i, j int) bool {
			return node.panes[i].TotalBytes > node.panes[j].TotalBytes
		})
	}

	t := &memoryTree{snapshot: resp}
	seen := make(map[string]bool, len(byTab))
	for _, id := range tabOrder {
		if node, ok := byTab[id]; ok {
			t.tabs = append(t.tabs, node)
			seen[id] = true
		}
	}
	for id, node := range byTab {
		if !seen[id] {
			t.tabs = append(t.tabs, node)
		}
	}
	// Within the "expected" tab order, still sort by total desc so the user
	// sees the biggest consumer first. This reorders both known and unknown
	// tabs uniformly.
	sort.SliceStable(t.tabs, func(i, j int) bool {
		return t.tabs[i].total > t.tabs[j].total
	})
	return t
}

// flatten walks the tree and emits a row per visible line (the grand-total
// row, each tab row, and — for expanded tabs — each pane row).
func (t *memoryTree) flatten() []memoryRow {
	rows := make([]memoryRow, 0, 1+len(t.tabs))
	rows = append(rows, memoryRow{
		kind:  memRowTotal,
		label: "Total",
		total: t.snapshot.Total,
	})
	for _, tab := range t.tabs {
		rows = append(rows, memoryRow{
			kind:  memRowTab,
			tabID: tab.id,
			label: tab.name,
			total: tab.total,
		})
		if tab.expanded {
			for _, p := range tab.panes {
				rows = append(rows, memoryRow{
					kind:   memRowPane,
					tabID:  tab.id,
					paneID: p.PaneID,
					label:  p.PaneID, // caller overrides with pane name at render time
					goHeap: p.GoHeapBytes,
					ptyRSS: p.PTYRSSBytes,
					total:  p.TotalBytes,
				})
			}
		}
	}
	return rows
}

// toggleAt flips the expanded state of the tab at visible-row index i. No-op
// if the row is not a tab row.
func (t *memoryTree) toggleAt(i int) {
	rows := t.flatten()
	if i < 0 || i >= len(rows) || rows[i].kind != memRowTab {
		return
	}
	for _, tab := range t.tabs {
		if tab.id == rows[i].tabID {
			tab.expanded = !tab.expanded
			return
		}
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run targeted test command with `-run TestMemoryTree ./internal/tui/`.
Expected: PASS (2 cases).

- [ ] **Step 5: Commit**

```bash
git add internal/tui/memory.go internal/tui/memory_test.go
git commit -m "feat(tui): add memoryTree model with tab grouping and expand/collapse"
```

---

## Task 13: Memory dialog — F1 item + render + key handling

**Files:**
- Modify: `internal/tui/dialog.go` (add `dialogMemory` to the iota, add
  "Memory" to the About items list near line 530, add render + key handlers)

This task is larger than 5 minutes. Break it into five micro-steps.

- [ ] **Step 1: Add the dialog iota**

Find the `const (` block that declares the `dialog*` iota values in
`internal/tui/dialog.go` and add `dialogMemory` as a new line (positioning is
cosmetic — group alongside similar read-only dialogs like `dialogLogViewer`).

- [ ] **Step 2: Add "Memory" to the F1 About menu**

At the slice near line 530:

```go
items := []string{
    "Settings",
    "Shortcuts",
    "Plugins",
    "Memory",             // NEW
    "View client log",
    "View daemon log",
    "View MCP logs",
}
```

Then locate the matching switch that dispatches the selected item (same
file, the case block that maps an index / label to `m.dialog = ...`). Add:

```go
case "Memory":
    m.openMemoryDialog()
```

- [ ] **Step 3: Add the dialog open + render functions**

Append to `internal/tui/memory.go`:

```go
import (
	"fmt"
	"strings"

	"github.com/artyomsv/quil/internal/memreport"
	tea "charm.land/bubbletea/v2"
)

// memoryDialogState holds the live dialog state on the Model. Fields on
// Model itself (tree, cursor, loading, snapAt) are added in model.go.
type memoryDialogState struct {
	tree    *memoryTree
	cursor  int
	loading bool
}

// memoryReportMsg is emitted when the TUI receives MsgMemoryReportResp.
type memoryReportMsg struct {
	Resp ipc.MemoryReportRespPayload
}

func (m *Model) openMemoryDialog() {
	m.dialog = dialogMemory
	m.mem.loading = true
	m.mem.cursor = 0
	m.mem.tree = nil
	// Trigger immediate fetch — do not wait for the 5 s tick.
	m.pendingMemoryReport = true
}

// applyMemoryReport builds the tree from a fresh response and resets cursor
// bounds.
func (m *Model) applyMemoryReport(resp ipc.MemoryReportRespPayload) {
	order, names := m.tabOrderAndNames()
	m.lastMemResp = &resp
	if m.dialog == dialogMemory {
		m.mem.tree = buildMemoryTree(resp, order, names)
		m.mem.loading = false
		if rows := m.mem.tree.flatten(); m.mem.cursor >= len(rows) {
			m.mem.cursor = len(rows) - 1
		}
		if m.mem.cursor < 0 {
			m.mem.cursor = 0
		}
	}
}

// tabOrderAndNames extracts the data the tree builder needs from the
// current workspace state. Uses the existing model fields; replace the
// accessors with whatever the codebase exposes (grep for "tabOrder" /
// "tabByID").
func (m *Model) tabOrderAndNames() ([]string, map[string]string) {
	order := make([]string, 0, len(m.tabs))
	names := make(map[string]string, len(m.tabs))
	for _, id := range m.tabOrder {
		order = append(order, id)
		if t, ok := m.tabs[id]; ok {
			names[id] = t.Name
		}
	}
	return order, names
}

// handleMemoryDialogKey returns (handled, cmd) after processing a key when
// dialogMemory is active.
func (m *Model) handleMemoryDialogKey(k tea.KeyPressMsg) (bool, tea.Cmd) {
	if m.mem.tree == nil {
		return false, nil
	}
	rows := m.mem.tree.flatten()
	switch k.String() {
	case "esc":
		m.dialog = dialogNone
		return true, nil
	case "up":
		if m.mem.cursor > 0 {
			m.mem.cursor--
		}
		return true, nil
	case "down":
		if m.mem.cursor < len(rows)-1 {
			m.mem.cursor++
		}
		return true, nil
	case "enter", "space", "right":
		m.mem.tree.toggleAt(m.mem.cursor)
		return true, nil
	case "left":
		// Collapse if on an expanded tab; otherwise jump to its tab row.
		if m.mem.cursor < len(rows) && rows[m.mem.cursor].kind == memRowPane {
			for i := m.mem.cursor - 1; i >= 0; i-- {
				if rows[i].tabID == rows[m.mem.cursor].tabID && rows[i].kind == memRowTab {
					m.mem.cursor = i
					return true, nil
				}
			}
		}
		m.mem.tree.toggleAt(m.mem.cursor)
		return true, nil
	case "r", "R":
		m.mem.loading = true
		m.pendingMemoryReport = true
		return true, nil
	}
	return false, nil
}

func (m *Model) renderMemoryDialog() string {
	var b strings.Builder
	b.WriteString(dialogTitle.Render("Memory"))
	b.WriteString("\n")
	if m.mem.loading && m.mem.tree == nil {
		b.WriteString("Loading snapshot...\n")
		return b.String()
	}
	rows := m.mem.tree.flatten()
	for i, row := range rows {
		indicator := "  "
		if row.kind == memRowTab {
			if t := m.mem.tree.findTab(row.tabID); t != nil && t.expanded {
				indicator = "▼ "
			} else {
				indicator = "▶ "
			}
		}
		line := fmt.Sprintf("%s%-28s %12s",
			indicator, row.label, memreport.HumanBytes(row.total))
		if row.kind == memRowPane {
			tuiBytes := m.tuiLocalMem(row.paneID)
			total := row.total + tuiBytes
			line = fmt.Sprintf("%s  %-26s  heap %10s  pty %10s  tui %10s  total %10s",
				"  ", row.label,
				memreport.HumanBytes(row.goHeap),
				memreport.HumanBytes(row.ptyRSS),
				memreport.HumanBytes(tuiBytes),
				memreport.HumanBytes(total))
		}
		if i == m.mem.cursor {
			line = reverseVideo(line) // reuse existing helper; if not present, wrap with lipgloss Reverse
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(dimText("r refresh · enter/←→ expand · esc close"))
	b.WriteString("\n")
	b.WriteString(dimText("TUI column approximates cols×rows×8 + notes buffer; PTY RSS is OS-reported and not comparable across platforms."))
	return b.String()
}

// findTab returns the tab node matching the given ID, or nil.
func (t *memoryTree) findTab(id string) *memoryTabNode {
	for _, tab := range t.tabs {
		if tab.id == id {
			return tab
		}
	}
	return nil
}
```

`reverseVideo` / `dimText` / `dialogTitle` are existing helpers in the tui
package — grep to confirm exact names (`grep -n "dialogTitle" internal/tui/*.go`).
If they differ, adapt.

- [ ] **Step 4: Wire the render switch in `dialog.go`**

Locate the render dispatch near line 262 (`case dialogAbout`, `case
dialogLogViewer` etc.) and add:

```go
case dialogMemory:
    return m.renderMemoryDialog()
```

Also add to the key-handler dispatch near line 488:

```go
case dialogMemory:
    if handled, cmd := m.handleMemoryDialogKey(k); handled {
        return m, cmd
    }
```

- [ ] **Step 5: Run full suite**

The compile check is the main signal — the render path exercises fields and
methods we just added. Run:

```bash
./scripts/dev.sh vet
./scripts/dev.sh test
```

Expected: no vet warnings; tests green.

- [ ] **Step 6: Commit**

```bash
git add internal/tui/memory.go internal/tui/dialog.go
git commit -m "feat(tui): add Memory dialog with tree view under F1 About"
```

---

## Task 14: TUI local memory calc + status-bar tick

**Files:**
- Modify: `internal/tui/model.go`
- Modify: `internal/tui/memory.go` (add `tuiLocalMem`)

This task is also larger than 5 minutes — split into micro-steps.

- [ ] **Step 1: Add `tuiLocalMem` in `memory.go`**

Append to `internal/tui/memory.go`:

```go
// vtCellBytes is a documented approximation of the per-cell cost of the VT
// emulator grid used by the ranking logic in the Memory dialog. The exact
// figure depends on the emulator library's internal representation.
const vtCellBytes = 8

// tuiLocalMem returns an estimate of TUI-side memory attributable to the
// given pane — the VT emulator grid plus any open notes editor buffer.
func (m *Model) tuiLocalMem(paneID string) uint64 {
	var n uint64
	if vt := m.ptyVTFor(paneID); vt != nil {
		n += uint64(vt.Cols()) * uint64(vt.Rows()) * vtCellBytes
	}
	if ne := m.notesEditorFor(paneID); ne != nil {
		n += ne.ApproxBytes()
	}
	return n
}
```

If `ptyVTFor` / `notesEditorFor` don't exist under those names, grep
(`grep -n "ptyVT\|notesEditor" internal/tui/*.go`) and adapt. Add tiny
accessor methods on `Model` if the underlying fields are unexported and no
accessor is defined. Keep them narrow — one line each.

- [ ] **Step 2: Add memory fields + tick on Model**

In `internal/tui/model.go`, inside `type Model struct`:

```go
    mem                 memoryDialogState
    lastMemResp         *ipc.MemoryReportRespPayload
    pendingMemoryReport bool
```

Near the other `tick*Msg` types:

```go
type memoryTickMsg struct{}
```

- [ ] **Step 3: Schedule the tick on Model init**

Find the function that returns the initial `tea.Cmd` list (typically `Init()`
on `Model`) and add a 5 s `tea.Tick`:

```go
tea.Tick(5*time.Second, func(time.Time) tea.Msg { return memoryTickMsg{} }),
```

In the `Update` switch for `memoryTickMsg`:

```go
case memoryTickMsg:
    m.pendingMemoryReport = true
    return m, tea.Batch(
        m.requestMemoryReport(),
        tea.Tick(5*time.Second, func(time.Time) tea.Msg { return memoryTickMsg{} }),
    )
```

Add `requestMemoryReport`:

```go
// requestMemoryReport issues MsgMemoryReportReq to the daemon. The
// response is handled by a case in Update for memoryReportMsg, which is
// produced by the IPC read goroutine.
func (m *Model) requestMemoryReport() tea.Cmd {
	if !m.pendingMemoryReport {
		return nil
	}
	m.pendingMemoryReport = false
	return func() tea.Msg {
		id := fmt.Sprintf("mem-%d", time.Now().UnixNano())
		payload, _ := json.Marshal(ipc.MemoryReportReqPayload{})
		if err := m.ipc.SendWithID(ipc.MsgMemoryReportReq, id, payload); err != nil {
			return nil
		}
		return nil
	}
}
```

(If the existing IPC client's helper is named differently — e.g.
`WriteMessage` or `Send` — use that. Grep `internal/tui/*.go` for how the
MCP status tool sends a request.)

- [ ] **Step 4: Route the response into Update**

Find the IPC dispatch in `model.go` (the big `switch msg.Type` on the
daemon-to-TUI path; other resp types like `MsgListPanesResp` will be there).
Add:

```go
case ipc.MsgMemoryReportResp:
    var payload ipc.MemoryReportRespPayload
    if err := msg.DecodePayload(&payload); err != nil {
        log.Printf("decode memory_report_resp: %v", err)
        return m, nil
    }
    m.applyMemoryReport(payload)
    return m, nil
```

- [ ] **Step 5: Add the status bar segment**

Find the status-bar renderer in `model.go` (grep for `[dev]` — the segment
lives next to the dev indicator). Insert a memory segment:

```go
if m.lastMemResp != nil {
    tuiExtra := uint64(0)
    for _, p := range m.lastMemResp.Panes {
        tuiExtra += m.tuiLocalMem(p.PaneID)
    }
    total := m.lastMemResp.Total + tuiExtra
    segments = append(segments, "mem " + memreport.HumanBytes(total))
}
```

Match whatever pattern `segments` uses in this file. If the renderer is a
series of `sb.WriteString(...)` calls rather than a slice, write the segment
with the same separator convention (` · `, `│`, etc.) used by adjacent
segments.

- [ ] **Step 6: Run full suite**

```bash
./scripts/dev.sh vet
./scripts/dev.sh test
```

Expected: no vet warnings; tests green.

- [ ] **Step 7: Manual smoke test**

Rebuild dev binary and run against the dev daemon per
`.claude/rules/dev-environment.md`:

```bash
./scripts/dev.sh build
# Windows:
./scripts/quil-dev.ps1
# Unix:
./scripts/quil-dev.sh
```

Verify:
- Status bar shows `mem <n> <unit>` after ~5 s.
- F1 → Memory opens the tree dialog showing at least one tab + one pane row.
- Expand/collapse with Enter works.
- R forces an immediate refresh (watch the value tick).
- Esc closes the dialog.

Before reporting, **close the dev TUI cleanly** — do not run
`scripts/kill-daemon` / `scripts/reset-daemon` per dev-environment rule.

- [ ] **Step 8: Commit**

```bash
git add internal/tui/memory.go internal/tui/model.go
git commit -m "feat(tui): poll memory report every 5s and expose status bar segment"
```

---

## Task 15: MCP tool — `get_memory_report`

**Files:**
- Modify: `cmd/quil/mcp_tools.go`

- [ ] **Step 1: Find the existing MCP tool pattern**

Grep for an existing typed-handler tool registration to mirror:
`grep -n "RegisterTool\|GetPaneStatus\|AddTool" cmd/quil/mcp_tools.go`.
Note the exact registration call and the helper that issues an IPC
request-response from the MCP side (likely a `requestResponse` helper).

- [ ] **Step 2: Add the handler**

In `cmd/quil/mcp_tools.go`, add the input/output types:

```go
// GetMemoryReportInput has no parameters — the tool always returns the
// latest snapshot.
type GetMemoryReportInput struct{}

type TabMemSummary struct {
	TabID      string `json:"tab_id"`
	TabName    string `json:"tab_name"`
	PaneCount  int    `json:"pane_count"`
	TotalBytes uint64 `json:"total_bytes"`
	TotalHuman string `json:"total_human"`
}

type MemoryReportOutput struct {
	SnapshotAt  string          `json:"snapshot_at"`
	TotalBytes  uint64          `json:"total_bytes"`
	TotalHuman  string          `json:"total_human"`
	GoHeapBytes uint64          `json:"go_heap_bytes"`
	PTYRSSBytes uint64          `json:"pty_rss_bytes"`
	Tabs        []TabMemSummary `json:"tabs"`
}
```

Add the handler function (adapt to the exact existing pattern for IPC
request correlation — the helper name below is a placeholder):

```go
func (b *mcpBridge) getMemoryReport(ctx context.Context, _ GetMemoryReportInput) (*MemoryReportOutput, error) {
	req := &ipc.Message{Type: ipc.MsgMemoryReportReq}
	resp, err := b.requestResponse(ctx, req, ipc.MsgMemoryReportResp)
	if err != nil {
		return nil, fmt.Errorf("daemon memory_report: %w", err)
	}
	var payload ipc.MemoryReportRespPayload
	if err := resp.DecodePayload(&payload); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	// Aggregate per tab.
	type agg struct {
		name  string
		count int
		total uint64
	}
	tabAgg := make(map[string]*agg)
	tabOrder := []string{}
	var goHeap, ptyRSS uint64

	tabs, err := b.listTabs(ctx) // reuse existing helper to get names
	if err == nil {
		for _, t := range tabs {
			tabAgg[t.ID] = &agg{name: t.Name}
			tabOrder = append(tabOrder, t.ID)
		}
	}

	for _, p := range payload.Panes {
		goHeap += p.GoHeapBytes
		ptyRSS += p.PTYRSSBytes
		a, ok := tabAgg[p.TabID]
		if !ok {
			a = &agg{name: p.TabID}
			tabAgg[p.TabID] = a
			tabOrder = append(tabOrder, p.TabID)
		}
		a.count++
		a.total += p.TotalBytes
	}

	out := &MemoryReportOutput{
		SnapshotAt:  time.Unix(0, payload.SnapshotAt).UTC().Format(time.RFC3339),
		TotalBytes:  payload.Total,
		TotalHuman:  memreport.HumanBytes(payload.Total),
		GoHeapBytes: goHeap,
		PTYRSSBytes: ptyRSS,
	}
	for _, id := range tabOrder {
		a := tabAgg[id]
		out.Tabs = append(out.Tabs, TabMemSummary{
			TabID:      id,
			TabName:    a.name,
			PaneCount:  a.count,
			TotalBytes: a.total,
			TotalHuman: memreport.HumanBytes(a.total),
		})
	}
	return out, nil
}
```

- [ ] **Step 3: Register the tool**

Find the registration block where `list_panes`, `get_pane_status`, etc. are
added to the MCP server. Add (adapt to real API):

```go
mcp.AddTool(server, &mcp.Tool{
    Name:        "get_memory_report",
    Description: "Return a snapshot of daemon-side memory usage: per-tab totals plus grand total. Layers reported: Go-heap (ring buffers + ghost snapshots + plugin state) and PTY child resident memory (OS-reported, not comparable across platforms).",
}, b.getMemoryReport)
```

- [ ] **Step 4: Run full suite**

```bash
./scripts/dev.sh vet
./scripts/dev.sh test
```

Expected: green.

- [ ] **Step 5: Smoke test via `quil mcp`**

Pipe a JSON-RPC call into the MCP bridge manually:

```bash
echo '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_memory_report","arguments":{}}}' | ./quil.exe mcp
```

Expected: response with `snapshot_at`, `total_bytes`, `tabs` populated.

- [ ] **Step 6: Commit**

```bash
git add cmd/quil/mcp_tools.go
git commit -m "feat(mcp): add get_memory_report tool"
```

---

## Task 16: MCP tool — `get_pane_memory`

**Files:**
- Modify: `cmd/quil/mcp_tools.go`

- [ ] **Step 1: Add input/output types**

```go
type GetPaneMemoryInput struct {
	PaneID string `json:"pane_id" jsonschema:"required,description=Pane ID from list_panes"`
}

type PaneMemoryOutput struct {
	SnapshotAt  string `json:"snapshot_at"`
	PaneID      string `json:"pane_id"`
	TabID       string `json:"tab_id"`
	PaneName    string `json:"pane_name"`
	Type        string `json:"type"`
	GoHeapBytes uint64 `json:"go_heap_bytes"`
	PTYRSSBytes uint64 `json:"pty_rss_bytes"`
	TotalBytes  uint64 `json:"total_bytes"`
	TotalHuman  string `json:"total_human"`
	ChildPID    int    `json:"child_pid"`
}
```

- [ ] **Step 2: Add the handler**

```go
func (b *mcpBridge) getPaneMemory(ctx context.Context, in GetPaneMemoryInput) (*PaneMemoryOutput, error) {
	if in.PaneID == "" {
		return nil, fmt.Errorf("pane_id is required")
	}
	req := &ipc.Message{Type: ipc.MsgMemoryReportReq}
	resp, err := b.requestResponse(ctx, req, ipc.MsgMemoryReportResp)
	if err != nil {
		return nil, fmt.Errorf("daemon memory_report: %w", err)
	}
	var payload ipc.MemoryReportRespPayload
	if err := resp.DecodePayload(&payload); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	var found *ipc.PaneMemInfo
	for i := range payload.Panes {
		if payload.Panes[i].PaneID == in.PaneID {
			found = &payload.Panes[i]
			break
		}
	}
	if found == nil {
		return nil, fmt.Errorf("pane not found: %s", in.PaneID)
	}

	// Enrich with pane metadata via the existing pane-status call.
	status, err := b.getPaneStatus(ctx, GetPaneStatusInput{PaneID: in.PaneID})
	paneName, paneType, childPID := "", "", 0
	if err == nil && status != nil {
		paneName = status.Name
		paneType = status.Type
		childPID = status.ChildPID // if that field exists; otherwise 0
	}

	return &PaneMemoryOutput{
		SnapshotAt:  time.Unix(0, payload.SnapshotAt).UTC().Format(time.RFC3339),
		PaneID:      found.PaneID,
		TabID:       found.TabID,
		PaneName:    paneName,
		Type:        paneType,
		GoHeapBytes: found.GoHeapBytes,
		PTYRSSBytes: found.PTYRSSBytes,
		TotalBytes:  found.TotalBytes,
		TotalHuman:  memreport.HumanBytes(found.TotalBytes),
		ChildPID:    childPID,
	}, nil
}
```

If `getPaneStatus` does not expose `ChildPID`, leave `ChildPID: 0` and skip
that enrichment.

- [ ] **Step 3: Register the tool**

```go
mcp.AddTool(server, &mcp.Tool{
    Name:        "get_pane_memory",
    Description: "Return daemon-side memory usage for a single pane. Includes Go-heap bytes, PTY child resident memory, and combined total. Call get_memory_report first to discover pane IDs.",
}, b.getPaneMemory)
```

- [ ] **Step 4: Run full suite**

```bash
./scripts/dev.sh vet
./scripts/dev.sh test
```

Expected: green.

- [ ] **Step 5: Smoke test**

```bash
# Replace <PANE_ID> with a real one from list_panes.
echo '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_pane_memory","arguments":{"pane_id":"<PANE_ID>"}}}' | ./quil.exe mcp
```

Expected: populated response; nonexistent pane returns an error.

- [ ] **Step 6: Commit**

```bash
git add cmd/quil/mcp_tools.go
git commit -m "feat(mcp): add get_pane_memory tool"
```

---

## Task 17: Documentation updates

**Files:**
- Modify: `.claude/CLAUDE.md` (Dialog system bullet + MCP tool count)
- Modify: `CHANGELOG.md` (Unreleased / Added section)

- [ ] **Step 1: Update `.claude/CLAUDE.md`**

Locate the "Dialog system" bullet and append "`dialogMemory`" to the iota
list, plus a note:

```
F1 opens About dialog with 7 items: Settings, Shortcuts, Plugins, Memory, View client log, View daemon log, View MCP logs.
```

Add a new note about the status bar:

```
Status bar segment `mem <n>` updates every 5s from the daemon snapshot, augmented with TUI-local approximations (VT grid + notes editor). Dialog and segment share the same response.
```

Update the MCP tool count near "15 MCP tools":

```
17 MCP tools: ... get_notifications, watch_notifications, get_memory_report, get_pane_memory.
```

- [ ] **Step 2: Update CHANGELOG.md**

Under "## [Unreleased]" (or add the section if missing), append an "Added"
line:

```
- Memory reporting: F1 → Memory dialog shows per-pane / per-tab / total memory across Go-heap, PTY RSS, and TUI layers; status bar gains a `mem` segment updated every 5s. Two new MCP tools, `get_memory_report` and `get_pane_memory`, expose daemon-side layers for external agents.
```

- [ ] **Step 3: Commit**

```bash
git add .claude/CLAUDE.md CHANGELOG.md
git commit -m "docs: document memory reporting dialog, status bar, and MCP tools"
```

---

## Self-Review

Run this check after implementing all tasks:

- **Spec coverage:**
  - Three layers (Go heap, PTY RSS, TUI local) — Tasks 7 (Go heap + collect),
    3/4/5 (PTY RSS), 11 + 14 (TUI local). ✅
  - 5 s cadence — Task 10 (daemon `NewCollector(..., 5s)`), Task 14
    (TUI `tea.Tick(5s)`). ✅
  - F1 → Memory dialog — Task 13. ✅
  - Status bar total — Task 14. ✅
  - Two MCP tools — Tasks 15 + 16. ✅
  - Tree expand/collapse — Task 12 (`toggleAt`), Task 13 (keys). ✅
  - Auto-scaled units — Task 1 (`HumanBytes`) used everywhere. ✅
  - Cross-platform — Tasks 3, 4, 5, 6 (stub). ✅
  - Error handling for exited panes — Task 7 (`alive() == false` → RSS 0). ✅
  - Concurrency safety — Task 8 (race-detector test). ✅
  - Integration test for IPC — Task 10. ✅
  - Caveat note in dialog footer — Task 13 (dimText line). ✅

- **Placeholder scan:**
  - No "TBD" / "TODO" in task steps. ✅
  - Every step that changes code shows the code. ✅
  - Commands have expected output described. ✅

- **Type consistency:**
  - `Snapshot.Panes` is `[]PaneMem` everywhere. ✅
  - `ipc.PaneMemInfo` fields match across Task 9 + Task 10 + Task 12. ✅
  - MCP outputs use `memreport.HumanBytes` for every `*Human` field. ✅
  - Collector uses `PaneSource` (exported) after Task 10 renames it. Task 7
    originally writes `paneSource` lowercase; Task 10 includes the rename
    step explicitly. ✅

No gaps found.

---

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-04-20-memory-reporting.md`. Two execution options:

**1. Subagent-Driven (recommended)** — I dispatch a fresh subagent per task, review between tasks, fast iteration.

**2. Inline Execution** — Execute tasks in this session using executing-plans, batch execution with checkpoints.

Which approach?
