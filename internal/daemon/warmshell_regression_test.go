package daemon

import (
	"bytes"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/plugin"
	apty "github.com/artyomsv/quil/internal/pty"
)

// These tests exercise the same helper Start calls after loading the registry,
// including TOML decoding. Hardcoding zero at its constructor call must fail.
func TestNewShellPoolFor_ConfigAndRegistry(t *testing.T) {
	tests := []struct {
		name string
		toml string
		cmd  string
		want int
	}{
		{"Default", "", "bash", 1},
		{"absent key", "[daemon]\nauto_start = false\n", "bash", 1},
		{"configured", "[daemon]\nwarm_shell_pool_size = 3\n", "bash", 3},
		{"disabled", "[daemon]\nwarm_shell_pool_size = 0\n", "bash", 0},
		{"negative", "[daemon]\nwarm_shell_pool_size = -1\n", "bash", 0},
		{"oversized", "[daemon]\nwarm_shell_pool_size = 1000\n", "bash", 8},
		{"extreme", "[daemon]\nwarm_shell_pool_size = 2147483647\n", "bash", 8},
		{"registry override", "", "zsh", 1},
		{"no integration script", "", "sh", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("QUIL_HOME", t.TempDir())
			cfg := config.Default()
			if tt.toml != "" {
				path := filepath.Join(t.TempDir(), "config.toml")
				if err := os.WriteFile(path, []byte(tt.toml), 0o600); err != nil {
					t.Fatal(err)
				}
				var err error
				cfg, err = config.Load(path)
				if err != nil {
					t.Fatal(err)
				}
			}
			registry := plugin.NewRegistry()
			registry.Get("terminal").Command.Cmd = tt.cmd
			var calls atomic.Int64
			prev := newSessionFn
			newSessionFn = func(cols, rows int) apty.Session {
				calls.Add(1)
				s := newScriptedWarmSession()
				s.chunks <- []byte("\x1b]133;A\x07")
				return s
			}
			t.Cleanup(func() { newSessionFn = prev })
			p := newShellPoolFor(cfg, registry)
			t.Cleanup(p.Stop)
			if p.size != tt.want || cap(p.ready) != tt.want {
				t.Fatalf("pool size/capacity = %d/%d, want %d", p.size, cap(p.ready), tt.want)
			}
			warmReady(t, p, tt.want)
			if tt.want > 0 {
				s := <-p.ready
				p.ready <- s
				if got := s.(*warmPoolSession).Session.(*scriptedWarmSession).startCmd; got != tt.cmd {
					t.Fatalf("pool started %q, want registry command %q", got, tt.cmd)
				}
			}
			p.Stop()
			if calls.Load() != int64(tt.want) {
				t.Fatalf("spawned %d shells, want %d", calls.Load(), tt.want)
			}
		})
	}
}

func TestWarmShellPool_ServesShell(t *testing.T) {
	tests := []struct {
		name string
		pool *warmShellPool
		cmd  string
		want bool
	}{
		{"nil", nil, "bash", true},
		{"disabled", &warmShellPool{}, "bash", true},
		{"same", &warmShellPool{size: 1, cfg: warmPoolShellConfig{Cmd: "bash"}}, "bash", true},
		{"changed", &warmShellPool{size: 1, cfg: warmPoolShellConfig{Cmd: "bash"}}, "zsh", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.pool.servesShell(tt.cmd); got != tt.want {
				t.Fatalf("servesShell = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWarmShellPool_ClaimGeometry(t *testing.T) {
	tests := []struct {
		name       string
		cols, rows int
		want       int64
	}{
		{"unknown", 0, 0, 0},
		{"zero width", 0, 40, 0},
		{"zero height", 120, 0, 0},
		{"negative", -1, 40, 0},
		{"degenerate", 1, 1, 0},
		{"one row", 120, 1, 1},
		{"one column", 1, 40, 1},
		{"normal", 120, 40, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newScriptedWarmSession()
			cwd := t.TempDir()
			s.response = []byte("\x1b]133;A\x07" + warmOSC7(cwd))
			p := &warmShellPool{size: 1, ready: make(chan apty.Session, 1), vacant: make(chan struct{}, 1), stop: make(chan struct{}), claimTimeout: time.Second}
			p.ready <- &warmPoolSession{Session: s}
			t.Cleanup(p.Stop)
			claimed, ok := p.TryClaim(cwd, tt.cols, tt.rows)
			if !ok {
				t.Fatal("geometry prevented a confirmed claim")
			}
			t.Cleanup(func() { claimed.Close() })
			if s.resizeCalls.Load() != tt.want {
				t.Fatalf("Resize calls = %d, want %d", s.resizeCalls.Load(), tt.want)
			}
			if tt.want > 0 && s.sizeAtWrite.Load() != uint64(tt.rows)<<16|uint64(tt.cols) {
				t.Fatal("Resize must precede Write and preserve row/column order")
			}
		})
	}
}

type blockedWarmCleanup struct {
	*scriptedWarmSession
	phase   string
	entered chan struct{}
	release chan struct{}
}

func (s *blockedWarmCleanup) Close() error {
	if s.phase == "Close" {
		close(s.entered)
		<-s.release
	}
	return s.scriptedWarmSession.Close()
}

func (s *blockedWarmCleanup) WaitExit() int {
	if s.phase == "WaitExit" {
		close(s.entered)
		<-s.release
	}
	return s.scriptedWarmSession.WaitExit()
}

func TestWarmShellPool_BlockedCleanupDoesNotBlockClaimOrStop(t *testing.T) {
	for _, phase := range []string{"Close", "WaitExit"} {
		t.Run(phase, func(t *testing.T) {
			s := &blockedWarmCleanup{scriptedWarmSession: newScriptedWarmSession(), phase: phase, entered: make(chan struct{}), release: make(chan struct{})}
			p := &warmShellPool{size: 1, ready: make(chan apty.Session, 1), vacant: make(chan struct{}, 1), stop: make(chan struct{}), claimTimeout: 20 * time.Millisecond, stopTimeout: 20 * time.Millisecond}
			p.ready <- &warmPoolSession{Session: s}
			done := make(chan struct{})
			t.Cleanup(func() {
				close(s.release)
				warmWait(t, "released claim", done)
				p.Stop()
				p.cleanup.Wait()
			})
			go func() {
				defer close(done)
				if claimed, ok := p.TryClaim("/target", 120, 40); ok || claimed != nil {
					t.Error("unconfirmed shell claimed")
				}
			}()
			warmWait(t, "blocked cleanup entered", s.entered)
			select {
			case <-done:
			case <-time.After(300 * time.Millisecond):
				t.Fatal("claim waited for blocked cleanup")
			}
			stopped := make(chan struct{})
			go func() { p.Stop(); close(stopped) }()
			select {
			case <-stopped:
			case <-time.After(300 * time.Millisecond):
				t.Fatal("Stop waited indefinitely ahead of pane teardown")
			}
		})
	}
}

func TestWarmShellPool_StopBoundsParkedCleanup(t *testing.T) {
	for _, phase := range []string{"Close", "WaitExit"} {
		t.Run(phase, func(t *testing.T) {
			s := &blockedWarmCleanup{scriptedWarmSession: newScriptedWarmSession(), phase: phase, entered: make(chan struct{}), release: make(chan struct{})}
			p := &warmShellPool{size: 1, ready: make(chan apty.Session, 1), stop: make(chan struct{}), stopTimeout: 20 * time.Millisecond}
			p.ready <- &warmPoolSession{Session: s}
			done := make(chan struct{})
			t.Cleanup(func() {
				close(s.release)
				warmWait(t, "released Stop", done)
				p.cleanup.Wait()
			})
			go func() { p.Stop(); close(done) }()
			warmWait(t, "parked cleanup entered", s.entered)
			select {
			case <-done:
			case <-time.After(300 * time.Millisecond):
				t.Fatal("parked cleanup blocked Stop ahead of pane teardown")
			}
			if len(p.ready) != 0 {
				t.Fatal("Stop left a parked session claimable")
			}
		})
	}
}

type warmLogBuffer struct {
	mu      sync.Mutex
	b       bytes.Buffer
	written chan struct{}
}

func (b *warmLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n, err := b.b.Write(p)
	select {
	case b.written <- struct{}{}:
	default:
	}
	return n, err
}

func (b *warmLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func TestWarmShellPool_FillTimeoutAndShutdownLogging(t *testing.T) {
	prevTimeout := warmShellFillTimeout
	warmShellFillTimeout = 20 * time.Millisecond
	t.Cleanup(func() { warmShellFillTimeout = prevTimeout })
	output := warmLogBuffer{written: make(chan struct{}, 1)}
	prevOutput := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(prevOutput) })
	created := make(chan *scriptedWarmSession, 2)
	p := warmTestPool(t, warmPoolShellConfig{Cmd: "bash"}, 1, func() apty.Session {
		s := newScriptedWarmSession()
		created <- s
		return s
	})
	var first *scriptedWarmSession
	select {
	case first = <-created:
	case <-time.After(3 * time.Second):
		t.Fatal("fill never started")
	}
	warmWait(t, "hung rc cleanup", first.closed)
	warmWait(t, "timeout diagnostic", output.written)
	select {
	case <-created:
		t.Fatal("fill timeout retried without backoff")
	case <-time.After(30 * time.Millisecond):
	}
	p.Stop()
	if got := output.String(); strings.Count(got, "prepare shell:") != 1 || !strings.Contains(got, "timed out") {
		t.Fatalf("want one timeout log and no shutdown failure log, got %q", got)
	}
	if first.waitCalls.Load() != 1 || len(p.ready) != 0 {
		t.Fatal("timed-out or stopped fill escaped cleanup")
	}
}

func TestWarmShellPool_ShutdownDoesNotLogFillFailure(t *testing.T) {
	var output warmLogBuffer
	prevOutput := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(prevOutput) })
	s := newScriptedWarmSession()
	p := warmTestPool(t, warmPoolShellConfig{Cmd: "bash"}, 1, func() apty.Session { return s })
	warmWait(t, "fill reader", s.readEntered)
	p.Stop()
	if got := output.String(); strings.Contains(got, "prepare shell:") {
		t.Fatalf("shutdown logged its own Close as a failure: %q", got)
	}
}

func TestWarmShellPool_FailuresBackOffExponentially(t *testing.T) {
	prevDelay := warmShellRetryDelay
	warmShellRetryDelay = 20 * time.Millisecond
	t.Cleanup(func() { warmShellRetryDelay = prevDelay })
	created := make(chan time.Time, 8)
	p := warmTestPool(t, warmPoolShellConfig{Cmd: "bash"}, 1, func() apty.Session {
		created <- time.Now()
		s := newScriptedWarmSession()
		s.startErr = io.ErrClosedPipe
		return s
	})
	var previous time.Time
	for i := 0; i < 5; i++ {
		select {
		case now := <-created:
			if i > 0 {
				minimum := (20 * time.Millisecond) << (i - 1)
				if elapsed := now.Sub(previous); elapsed < minimum {
					t.Fatalf("retry %d took %v, want at least %v", i, elapsed, minimum)
				}
			}
			previous = now
		case <-time.After(3 * time.Second):
			t.Fatal("retry did not arrive")
		}
	}
	p.Stop()
}

func TestWarmShellPool_RetryDelay(t *testing.T) {
	delay := time.Second
	for _, want := range []time.Duration{2, 4, 8, 16, 30, 30, 30} {
		delay = nextWarmRetryDelay(delay)
		if delay != want*time.Second {
			t.Fatalf("retry delay = %v, want %v", delay, want*time.Second)
		}
	}
}

// Both terminators must be replayed verbatim, including OSC 7 split across reads.
func TestWarmShellPool_ReplaysBELDirectoryConfirmation(t *testing.T) {
	s := newScriptedWarmSession()
	cwd := "/target"
	osc := "\x1b]7;file://host/target\x07"
	s.chunks <- []byte("discard\x1b]133;A\x07" + osc + "prompt")
	s.chunks <- nil
	got, err := readWarmPrompt(s, cwd)
	if err != nil || string(got) != osc+"prompt" {
		t.Fatalf("replay = %q, %v", got, err)
	}
}

var _ io.Writer = (*warmLogBuffer)(nil)
