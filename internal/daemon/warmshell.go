package daemon

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	apty "github.com/artyomsv/quil/internal/pty"
)

type warmPoolShellConfig struct {
	Cmd  string
	Args []string
	Env  []string
}

// warmShellPool uses one immutable shell configuration. Reloading the terminal
// plugin does not update it: the old shell remains in use until daemon restart.
// Parked shells have no reader after their first prompt; unsolicited idle output
// can therefore fill their PTY buffer. Claim failure falls back to a fresh spawn.
type warmShellPool struct {
	cfg          warmPoolShellConfig
	size         int
	ready        chan apty.Session
	vacant       chan struct{}
	stop         chan struct{}
	wg           sync.WaitGroup
	once         sync.Once
	powershell   bool
	claimTimeout time.Duration // set before claims; tests may shorten the bound
	mu           sync.Mutex
	filling      *warmPoolSession
}

func newWarmShellPool(cfg warmPoolShellConfig, size int) *warmShellPool {
	p := &warmShellPool{claimTimeout: 500 * time.Millisecond}
	if size <= 0 {
		return p
	}
	// Decide quoting once, from the command that will actually be started.
	name := strings.TrimSuffix(strings.ToLower(filepath.Base(strings.ReplaceAll(cfg.Cmd, "\\", "/"))), ".exe")
	switch name {
	case "pwsh", "powershell":
		p.powershell = true
	case "bash", "zsh":
	default:
		return p // these are the only shells with the required Quil prompt hooks
	}
	p.cfg = warmPoolShellConfig{Cmd: cfg.Cmd, Args: append([]string(nil), cfg.Args...), Env: append([]string(nil), cfg.Env...)}
	p.size = size
	p.ready = make(chan apty.Session, size)
	p.vacant = make(chan struct{}, size)
	p.stop = make(chan struct{})
	for i := 0; i < size; i++ {
		p.vacant <- struct{}{}
	}
	p.wg.Add(1)
	go p.fill()
	return p
}

func (p *warmShellPool) fill() {
	defer p.wg.Done()
	for {
		select {
		case <-p.stop:
			return
		case <-p.vacant:
		}
		// A vacant slot includes its in-flight spawn, so size bounds all idle
		// shells, not just the sessions already parked in ready.
		for {
			select {
			case <-p.stop:
				return
			default:
			}
			s := &warmPoolSession{Session: newSessionFn(80, 24)}
			s.SetEnv(p.cfg.Env)
			s.SetCWD(os.TempDir())
			err := s.Start(p.cfg.Cmd, p.cfg.Args...)
			if err == nil {
				// Publish only after Start: Close must not race the platform
				// session's initialization of its PTY handles.
				p.mu.Lock()
				select {
				case <-p.stop:
					p.mu.Unlock()
					s.Close()
					return
				default:
					p.filling = s
				}
				p.mu.Unlock()
				_, err = readWarmPrompt(s, "")
				p.mu.Lock()
				p.filling = nil
				p.mu.Unlock()
			}
			if err == nil {
				select {
				case <-p.stop:
					s.Close()
					return
				case p.ready <- s:
				}
				break
			}
			s.Close()
			log.Printf("warm shell pool: prepare shell: %v", err)
			timer := time.NewTimer(time.Second)
			select {
			case <-p.stop:
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}
}

func (p *warmShellPool) TryClaim(cwd string) (apty.Session, bool) {
	// nil is a valid receiver: New() leaves d.shellPool nil until Start()
	// builds the real one from the fully-loaded registry (see daemon.go), and
	// tests that construct a Daemon via New() alone never call Start() at
	// all. Mirrors the existing d.broadcast nil-guard for d.server.
	if p == nil {
		return nil, false
	}
	var s apty.Session
	select {
	case s = <-p.ready:
		p.vacant <- struct{}{}
	default:
		return nil, false
	}
	// Serialize WaitGroup admission with Stop. Stop closes stop before taking
	// mu, so no new claim can join after it begins waiting for existing work.
	p.mu.Lock()
	select {
	case <-p.stop:
		p.mu.Unlock()
		s.Close()
		return nil, false
	default:
		p.wg.Add(1)
	}
	p.mu.Unlock()
	defer p.wg.Done()
	// Control characters are keystrokes to an interactive shell even inside
	// quotes (in particular CR/LF would submit an incomplete command).
	if cwd == "" || strings.IndexFunc(cwd, func(r rune) bool { return r < 32 || r == 127 }) >= 0 {
		s.Close()
		return nil, false
	}
	command := "cd '" + strings.ReplaceAll(cwd, "'", "'\\''") + "'\r"
	if p.powershell {
		command = "Set-Location -LiteralPath '" + strings.ReplaceAll(cwd, "'", "''") + "'\r"
	}
	var suffix []byte
	var err error
	done := make(chan struct{})
	go func() {
		defer close(done)
		var n int
		n, err = s.Write([]byte(command))
		if err == nil && n != len(command) {
			err = io.ErrShortWrite
		}
		if err == nil {
			suffix, err = readWarmPrompt(s, cwd)
		}
	}()
	timer := time.NewTimer(p.claimTimeout)
	defer timer.Stop()
	select {
	case <-done:
		if err == nil {
			select {
			case <-p.stop:
			default:
				// The confirmation reader has returned. Keep bytes after OSC 7:
				// one Read may contain both its terminator and the visible prompt.
				return &warmPoolSession{Session: s, pending: suffix}, true
			}
		}
	case <-timer.C:
	case <-p.stop:
	}
	// No failed claim is ever exposed to a pane. Close releases its blocked
	// Read/Write; join before returning so it cannot outlive the test seam.
	s.Close()
	<-done
	return nil, false
}

func (p *warmShellPool) Stop() {
	// See the matching comment on TryClaim: nil is a valid, no-op receiver.
	if p == nil || p.size <= 0 {
		return
	}
	p.once.Do(func() {
		close(p.stop)
		p.mu.Lock()
		s := p.filling
		p.mu.Unlock()
		if s != nil {
			s.Close() // wake a fill reader still waiting for the initial prompt
		}
		p.drain()
		p.wg.Wait()
		// A ready send can win its select concurrently with stop closing.
		p.drain()
	})
}

func (p *warmShellPool) drain() {
	for {
		select {
		case s := <-p.ready:
			s.Close()
		default:
			return
		}
	}
}

// warmPoolSession preserves the prompt's unread suffix and makes ownership
// changes safe to clean up more than once. Read still has exactly one owner.
type warmPoolSession struct {
	apty.Session
	pending  []byte
	once     sync.Once
	closeErr error
}

func (s *warmPoolSession) Read(buf []byte) (int, error) {
	if len(s.pending) > 0 {
		n := copy(buf, s.pending)
		s.pending = s.pending[n:]
		return n, nil
	}
	return s.Session.Read(buf)
}

func (s *warmPoolSession) Close() error {
	s.once.Do(func() {
		s.closeErr = s.Session.Close()
		// Unclaimed sessions never reach streamPTYOutput's exit watcher.
		// WaitExit also releases the Windows process handle after exit.
		s.Session.WaitExit()
	})
	return s.closeErr
}

// readWarmPrompt scans OSCs using the same Index/IndexAny idiom as
// detectOSC133Exit, retaining partial sequences across Reads. Empty cwd means
// fill: return at the first A and discard all startup output already read.
// Claim requires A followed by OSC 7, ignoring zsh's earlier chpwd OSC 7.
func readWarmPrompt(s apty.Session, cwd string) ([]byte, error) {
	var data []byte
	seenPrompt := false
	buf := make([]byte, 4096)
	for {
		n, err := s.Read(buf)
		if err != nil {
			return nil, err
		}
		data = append(data, buf[:n]...)
		for {
			idx := bytes.Index(data, []byte("\x1b]"))
			if idx < 0 {
				if len(data) > 1 {
					data = data[len(data)-1:] // keep a possible split ESC
				}
				break
			}
			data = data[idx:]
			rest := data[2:]
			end := bytes.IndexAny(rest, "\x07\x1b")
			if end < 0 || (rest[end] == '\x1b' && end+1 == len(rest)) {
				if len(data) > 64*1024 {
					return nil, fmt.Errorf("warm shell: unterminated OSC exceeds 64 KiB")
				}
				break
			}
			terminator := 1
			if rest[end] == '\x1b' {
				if rest[end+1] != '\\' {
					data = rest[end:]
					continue
				}
				terminator = 2
			}
			payload := string(rest[:end])
			data = rest[end+terminator:]
			if payload == "133;A" {
				if cwd == "" {
					return nil, nil
				}
				seenPrompt = true
			} else if seenPrompt && strings.HasPrefix(payload, "7;") {
				if !warmCWDMatches(strings.TrimPrefix(payload, "7;"), cwd) {
					return nil, fmt.Errorf("warm shell: directory confirmation mismatch")
				}
				return append([]byte(nil), data...), nil
			}
		}
	}
}

func warmCWDMatches(uri, cwd string) bool {
	if !strings.HasPrefix(uri, "file://") {
		return false
	}
	// Quil's scripts emit literal paths, not percent-encoded URLs. Splitting
	// off the host preserves spaces, #, ?, and literal % sequences in names.
	rest := strings.TrimPrefix(uri, "file://")
	i := strings.IndexByte(rest, '/')
	if i < 0 {
		return false
	}
	got := rest[i:]
	if runtime.GOOS == "windows" {
		if len(got) >= 3 && got[0] == '/' && got[2] == ':' {
			got = got[1:]
		}
		return strings.EqualFold(filepath.Clean(got), filepath.Clean(cwd))
	}
	return filepath.Clean(got) == filepath.Clean(cwd)
}
