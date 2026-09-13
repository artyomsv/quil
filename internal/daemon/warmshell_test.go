package daemon

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	apty "github.com/artyomsv/quil/internal/pty"
)

// scriptedWarmSession keeps reads blocked until the test feeds bytes or closes
// the session. All shared observations use channels or atomics; fakeSession's
// startup records are read only after the started channel synchronizes them.
type scriptedWarmSession struct {
	fakeSession
	chunks      chan []byte
	writes      chan string
	started     chan struct{}
	closed      chan struct{}
	readEntered chan struct{}
	once        sync.Once
	startErr    error
	writeErr    error
	shortWrite  bool
	blockWrite  bool
	response    []byte
	readers     atomic.Int64
	overlap     atomic.Bool
	closeCalls  atomic.Int64
	waitCalls   atomic.Int64
}

func newScriptedWarmSession() *scriptedWarmSession {
	return &scriptedWarmSession{
		chunks: make(chan []byte, 128), writes: make(chan string, 1),
		started: make(chan struct{}), closed: make(chan struct{}),
		readEntered: make(chan struct{}, 128),
	}
}

func (s *scriptedWarmSession) Start(cmd string, args ...string) error {
	s.fakeSession.Start(cmd, args...)
	close(s.started)
	return s.startErr
}

func (s *scriptedWarmSession) Read(buf []byte) (int, error) {
	if s.readers.Add(1) != 1 {
		s.overlap.Store(true)
	}
	defer s.readers.Add(-1)
	select {
	case s.readEntered <- struct{}{}:
	default:
	}
	select {
	case <-s.closed:
		return 0, io.EOF
	case data := <-s.chunks:
		if data == nil {
			return 0, io.EOF
		}
		if len(data) > len(buf) {
			panic("test chunk exceeds read buffer")
		}
		return copy(buf, data), nil
	}
}

func (s *scriptedWarmSession) Write(data []byte) (int, error) {
	s.writes <- string(data)
	if s.blockWrite {
		<-s.closed
		return 0, io.ErrClosedPipe
	}
	if s.writeErr != nil {
		return 0, s.writeErr
	}
	if s.shortWrite {
		return len(data) - 1, nil
	}
	if s.response != nil {
		s.chunks <- append([]byte(nil), s.response...)
	}
	return len(data), nil
}

func (s *scriptedWarmSession) Close() error {
	s.closeCalls.Add(1)
	s.once.Do(func() { close(s.closed) })
	return nil
}

func (s *scriptedWarmSession) WaitExit() int {
	s.waitCalls.Add(1)
	<-s.closed
	return 0
}

func warmTestPool(t *testing.T, cfg warmPoolShellConfig, size int, factory func() apty.Session) *warmShellPool {
	t.Helper()
	prev := newSessionFn
	newSessionFn = func(cols, rows int) apty.Session {
		if cols != 80 || rows != 24 {
			t.Errorf("pool dimensions = %dx%d, want 80x24", cols, rows)
		}
		return factory()
	}
	t.Cleanup(func() { newSessionFn = prev })
	p := newWarmShellPool(cfg, size)
	t.Cleanup(p.Stop) // join the worker before restoring the constructor seam
	return p
}

func warmWait(t *testing.T, what string, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func warmReady(t *testing.T, p *warmShellPool, count int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for len(p.ready) != count {
		if time.Now().After(deadline) {
			t.Fatalf("ready sessions = %d, want %d", len(p.ready), count)
		}
		time.Sleep(time.Millisecond)
	}
}

func warmOSC7(cwd string) string {
	path := filepath.ToSlash(cwd)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return "\x1b]7;file://host" + path + "\x1b\\"
}

func TestWarmShellPool_FillWaitsForPrompt(t *testing.T) {
	s := newScriptedWarmSession()
	cfg := warmPoolShellConfig{Cmd: "bash", Args: []string{"--rcfile", "init.sh"}, Env: []string{"A=B"}}
	p := warmTestPool(t, cfg, 1, func() apty.Session { return s })
	warmWait(t, "first Read", s.readEntered)
	if len(p.ready) != 0 {
		t.Fatal("session parked before first prompt")
	}
	if s.cwd != os.TempDir() || s.startCmd != cfg.Cmd || !reflect.DeepEqual(s.startArgs, cfg.Args) || !reflect.DeepEqual(s.env, cfg.Env) {
		t.Fatalf("spawn config: cwd=%q cmd=%q args=%v env=%v", s.cwd, s.startCmd, s.startArgs, s.env)
	}
	if s.cwdSetAt >= s.startedAt {
		t.Fatal("CWD must be set before Start")
	}
	s.chunks <- []byte("startup\x1b]133;A")
	warmWait(t, "split terminator Read", s.readEntered)
	if len(p.ready) != 0 {
		t.Fatal("session parked before the marker terminator")
	}
	s.chunks <- []byte("\x1b\\initial prompt")
	warmReady(t, p, 1)
	if s.readers.Load() != 0 {
		t.Fatal("fill reader still active after parking")
	}
	p.Stop()
	if s.closeCalls.Load() != 1 || s.waitCalls.Load() != 1 {
		t.Fatalf("cleanup: Close=%d WaitExit=%d, want 1 each", s.closeCalls.Load(), s.waitCalls.Load())
	}
}

func TestWarmShellPool_TryClaim(t *testing.T) {
	for _, cmd := range []string{"bash", "zsh", "pwsh.exe", "powershell.exe"} {
		t.Run(cmd, func(t *testing.T) {
			cwd := filepath.Join(t.TempDir(), "O'Brien #100%25 ?")
			s := newScriptedWarmSession()
			// An early zsh chpwd OSC 7 is not the confirmation. The prompt
			// tail is in the SAME Read as the matching OSC 7, and must survive.
			s.response = []byte("echoed cd\r\n" + warmOSC7(t.TempDir()) + "\x1b]133;A\x07" + warmOSC7(cwd) + "fresh prompt> ")
			s.chunks <- []byte("\x1b]133;A\x07old prompt")
			var created atomic.Int64
			p := warmTestPool(t, warmPoolShellConfig{Cmd: cmd}, 1, func() apty.Session {
				if created.Add(1) == 1 {
					return s
				}
				return newScriptedWarmSession()
			})
			warmReady(t, p, 1)
			claimed, ok := p.TryClaim(cwd)
			if !ok || claimed == nil {
				t.Fatal("matching directory was not claimed")
			}
			t.Cleanup(func() { claimed.Close() })
			want := "cd '" + strings.ReplaceAll(cwd, "'", "'\\''") + "'\r"
			if cmd == "pwsh.exe" || cmd == "powershell.exe" {
				want = "Set-Location -LiteralPath '" + strings.ReplaceAll(cwd, "'", "''") + "'\r"
			}
			if got := <-s.writes; got != want || strings.Contains(got, "\n") {
				t.Fatalf("Write = %q, want %q", got, want)
			}
			buf := make([]byte, 128)
			n, err := claimed.Read(buf)
			if err != nil || string(buf[:n]) != "fresh prompt> " {
				t.Fatalf("handoff output = %q, %v", buf[:n], err)
			}
			if s.overlap.Load() || s.readers.Load() != 0 {
				t.Fatal("confirmation reader overlaps the new owner")
			}
			p.Stop()
			if s.closeCalls.Load() != 0 {
				t.Fatal("Stop closed a successfully claimed shell")
			}
		})
	}
}

func TestWarmShellPool_TryClaimFailure(t *testing.T) {
	for _, scenario := range []string{"mismatch", "timeout", "read error", "write error", "short write", "blocked write", "control character"} {
		t.Run(scenario, func(t *testing.T) {
			s := newScriptedWarmSession()
			cwd := t.TempDir()
			switch scenario {
			case "mismatch":
				s.response = []byte("\x1b]133;A\x07" + warmOSC7(t.TempDir()))
			case "write error":
				s.writeErr = io.ErrClosedPipe
			case "short write":
				s.shortWrite = true
			case "blocked write":
				s.blockWrite = true
			case "control character":
				cwd += "\rwhoami"
			}
			// Manually park a session to isolate claims from refill behavior.
			p := &warmShellPool{ready: make(chan apty.Session, 1), vacant: make(chan struct{}, 1), stop: make(chan struct{}), claimTimeout: 20 * time.Millisecond}
			p.ready <- s
			t.Cleanup(func() { s.Close() })
			if scenario == "read error" {
				s.chunks <- nil
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				if claimed, ok := p.TryClaim(cwd); ok || claimed != nil {
					t.Error("failed confirmation handed off a shell")
				}
			}()
			warmWait(t, "failed claim", done)
			if s.closeCalls.Load() != 1 || s.readers.Load() != 0 {
				t.Fatalf("failed claim: closes=%d readers=%d", s.closeCalls.Load(), s.readers.Load())
			}
			if scenario == "control character" && len(s.writes) != 0 {
				t.Fatal("control character written to interactive shell")
			}
		})
	}
}

func TestWarmShellPool_EmptyAndDisabled(t *testing.T) {
	for _, size := range []int{0, -1} {
		var calls atomic.Int64
		p := warmTestPool(t, warmPoolShellConfig{Cmd: "bash"}, size, func() apty.Session {
			calls.Add(1)
			return newScriptedWarmSession()
		})
		if s, ok := p.TryClaim("/tmp"); ok || s != nil {
			t.Fatal("disabled pool claimed a shell")
		}
		p.Stop()
		p.Stop()
		if calls.Load() != 0 || p.ready != nil || p.stop != nil {
			t.Fatal("disabled pool initialized a worker")
		}
	}
	p := &warmShellPool{ready: make(chan apty.Session, 1)}
	// No worker or other channels: the empty select must return on its own.
	done := make(chan struct{})
	go func() {
		defer close(done)
		if s, ok := p.TryClaim("/tmp"); ok || s != nil {
			t.Error("empty pool claimed a shell")
		}
	}()
	warmWait(t, "empty claim", done)
}

func TestWarmShellPool_StopClosesParkedAndFillingSessions(t *testing.T) {
	for _, promptCount := range []int{0, 2, 3} {
		t.Run(string(rune('0'+promptCount))+" prompts", func(t *testing.T) {
			created := make(chan *scriptedWarmSession, 4)
			var count atomic.Int64
			p := warmTestPool(t, warmPoolShellConfig{Cmd: "bash"}, 3, func() apty.Session {
				s := newScriptedWarmSession()
				if count.Add(1) <= int64(promptCount) {
					s.chunks <- []byte("\x1b]133;A\x07")
				}
				created <- s
				return s
			})
			want := promptCount + 1
			if want > 3 {
				want = 3
			}
			var sessions []*scriptedWarmSession
			for i := 0; i < want; i++ {
				select {
				case s := <-created:
					warmWait(t, "fill Read", s.readEntered)
					sessions = append(sessions, s)
				case <-time.After(3 * time.Second):
					t.Fatal("pool did not fill")
				}
			}
			warmReady(t, p, promptCount)
			var wg sync.WaitGroup
			for i := 0; i < 8; i++ {
				wg.Add(1)
				go func() { defer wg.Done(); p.Stop() }()
			}
			done := make(chan struct{})
			go func() { wg.Wait(); close(done) }()
			warmWait(t, "concurrent Stop", done)
			if len(p.ready) != 0 || count.Load() != int64(want) {
				t.Fatalf("after Stop: ready=%d constructed=%d", len(p.ready), count.Load())
			}
			for _, s := range sessions {
				if s.closeCalls.Load() != 1 || s.waitCalls.Load() != 1 || s.readers.Load() != 0 {
					t.Fatalf("Stop: closes=%d waits=%d readers=%d", s.closeCalls.Load(), s.waitCalls.Load(), s.readers.Load())
				}
			}
		})
	}
}

func TestWarmShellPool_StartErrorBacksOffAndStops(t *testing.T) {
	created := make(chan *scriptedWarmSession, 16)
	var calls atomic.Int64
	p := warmTestPool(t, warmPoolShellConfig{Cmd: "bash"}, 1, func() apty.Session {
		calls.Add(1)
		s := newScriptedWarmSession()
		s.startErr = errors.New("spawn failed")
		select {
		case created <- s:
		default:
		}
		return s
	})
	var s *scriptedWarmSession
	select {
	case s = <-created:
	case <-time.After(3 * time.Second):
		t.Fatal("pool never attempted Start")
	}
	warmWait(t, "failed Start", s.started)
	warmWait(t, "failed session cleanup", s.closed)
	time.Sleep(30 * time.Millisecond)
	if calls.Load() != 1 || len(p.ready) != 0 {
		t.Fatal("Start failure retried without backoff or parked a failed shell")
	}
	done := make(chan struct{})
	go func() { p.Stop(); close(done) }()
	warmWait(t, "Stop during backoff", done)
}

func TestWarmShellPool_RefillsAfterClaim(t *testing.T) {
	cwd := t.TempDir()
	var count atomic.Int64
	p := warmTestPool(t, warmPoolShellConfig{Cmd: "bash"}, 1, func() apty.Session {
		count.Add(1)
		s := newScriptedWarmSession()
		s.chunks <- []byte("\x1b]133;A\x07")
		s.response = []byte("\x1b]133;A\x07" + warmOSC7(cwd))
		return s
	})
	warmReady(t, p, 1)
	claimed, ok := p.TryClaim(cwd)
	if !ok {
		t.Fatal("initial shell was not claimed")
	}
	t.Cleanup(func() { claimed.Close() })
	warmReady(t, p, 1)
	p.Stop()
	if count.Load() != 2 {
		t.Fatalf("constructed %d shells, want initial shell plus one replacement", count.Load())
	}
}

func TestWarmShellPool_StopCancelsAndJoinsClaim(t *testing.T) {
	s := newScriptedWarmSession()
	p := &warmShellPool{
		size: 1, ready: make(chan apty.Session, 1), vacant: make(chan struct{}, 1),
		stop: make(chan struct{}), claimTimeout: time.Minute,
	}
	p.ready <- &warmPoolSession{Session: s}
	t.Cleanup(p.Stop)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if claimed, ok := p.TryClaim("/target"); ok || claimed != nil {
			t.Error("claim succeeded during shutdown")
		}
	}()
	warmWait(t, "confirmation Read", s.readEntered)
	stopped := make(chan struct{})
	go func() { p.Stop(); close(stopped) }()
	warmWait(t, "Stop with an active claim", stopped)
	warmWait(t, "canceled claim", done)
	if s.closeCalls.Load() != 1 || s.readers.Load() != 0 {
		t.Fatalf("canceled claim: closes=%d readers=%d", s.closeCalls.Load(), s.readers.Load())
	}
}

func TestWarmShellPool_ConfirmationAcrossChunks(t *testing.T) {
	cwd := filepath.Join(t.TempDir(), "target")
	stream := "\x1b]133;A\x1b\\" + warmOSC7(cwd)
	for split := 1; split < len(stream); split++ {
		s := newScriptedWarmSession()
		s.chunks <- []byte(stream[:split])
		s.chunks <- []byte(stream[split:] + "prompt")
		s.chunks <- nil // malformed parsing must fail rather than hang this test
		suffix, err := readWarmPrompt(s, cwd)
		if err != nil || string(suffix) != "prompt" {
			t.Fatalf("split %d: suffix=%q err=%v", split, suffix, err)
		}
	}
}

func TestWarmShellPool_ConfirmationRequiresPromptThenValidOSC7(t *testing.T) {
	cwd := t.TempDir()
	for _, data := range []string{
		warmOSC7(cwd), // OSC 7 alone (zsh chpwd) cannot confirm a prompt
		"\x1b]133;AB\x07" + warmOSC7(cwd),
		"\x1b]133;A\x07\x1b]7;file://host\x07",
		"\x1b]133;A\x07\x1b]7;https://host/path\x07",
	} {
		s := newScriptedWarmSession()
		s.chunks <- []byte(data)
		s.chunks <- nil
		if _, err := readWarmPrompt(s, cwd); err == nil {
			t.Errorf("invalid confirmation accepted: %q", data)
		}
	}
}

var _ apty.Session = (*scriptedWarmSession)(nil)
