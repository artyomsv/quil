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

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/plugin"
	apty "github.com/artyomsv/quil/internal/pty"
	"github.com/artyomsv/quil/internal/shellinit"
)

const maxWarmShellPoolSize = 8

// Copied at construction; seam-swapping tests set these before starting workers.
var warmShellFillTimeout = 15 * time.Second
var warmShellRetryDelay = time.Second

var warmPowerShellQuotes = strings.NewReplacer("'", "''", "\u2018", "\u2018\u2018", "\u2019", "\u2019\u2019", "\u201a", "\u201a\u201a", "\u201b", "\u201b\u201b")

type warmPoolShellConfig struct {
	Cmd  string
	Args []string
	Env  []string
	// Intercept names the agent binaries a claimed shell should shadow. It is
	// held here rather than baked into Env because the other half of the pair,
	// the token, is minted per shell in fill.
	Intercept []string
}

// warmShellPool uses one immutable shell configuration. Reloading the terminal
// plugin does not update it: a changed shell bypasses pooling until daemon restart.
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
	fillTimeout  time.Duration
	retryDelay   time.Duration
	stopTimeout  time.Duration
	cleanup      sync.WaitGroup
	mu           sync.Mutex
	filling      *warmPoolSession
}

// newShellPoolFor runs after Start has loaded user plugin overrides.
func newShellPoolFor(cfg config.Config, registry *plugin.Registry) *warmShellPool {
	if shellCfg := shellinit.Configure(registry.Get("terminal").Command.Cmd, config.QuilDir(), nil, ""); shellCfg != nil {
		// The names are constant for the daemon's lifetime; the token is not,
		// so Configure is asked for neither and fill supplies both.
		var intercept []string
		if cfg.Agents.HandStartedPolicy() != config.HandStartedOff {
			intercept = handStartNames(registry.All())
		}
		return newWarmShellPool(warmPoolShellConfig{
			Cmd: shellCfg.Cmd, Args: shellCfg.Args, Env: shellCfg.Env, Intercept: intercept,
		}, cfg.Daemon.WarmShellPoolSize)
	}
	return newWarmShellPool(warmPoolShellConfig{}, 0)
}

func (p *warmShellPool) servesShell(cmd string) bool {
	return p == nil || p.size <= 0 || p.cfg.Cmd == cmd
}

func newWarmShellPool(cfg warmPoolShellConfig, size int) *warmShellPool {
	p := &warmShellPool{claimTimeout: 500 * time.Millisecond, fillTimeout: warmShellFillTimeout, retryDelay: warmShellRetryDelay, stopTimeout: time.Second}
	if size <= 0 {
		return p
	}
	if size > maxWarmShellPoolSize {
		log.Printf("warm shell pool: size %d exceeds ceiling; using %d", size, maxWarmShellPoolSize)
		size = maxWarmShellPoolSize
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
	// Copied field by field so the pool owns its slices. EVERY field must be
	// named here: one left out is silently dropped, which is how Intercept was
	// lost — the pool started, the shells started, and nothing was armed.
	p.cfg = warmPoolShellConfig{
		Cmd:       cfg.Cmd,
		Args:      append([]string(nil), cfg.Args...),
		Env:       append([]string(nil), cfg.Env...),
		Intercept: append([]string(nil), cfg.Intercept...),
	}
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
		delay := p.retryDelay
		for {
			select {
			case <-p.stop:
				return
			default:
			}
			s := &warmPoolSession{Session: newSessionFn(80, 24), tok: newInterceptToken()}
			s.SetEnv(append(append([]string(nil), p.cfg.Env...),
				shellinit.InterceptEnv(p.cfg.Intercept, s.tok)...))
			s.SetCWD(os.TempDir())
			err := s.Start(p.cfg.Cmd, p.cfg.Args...)
			var readerDone <-chan struct{}
			if err == nil {
				// Publish only after Start: Close must not race the platform
				// session's initialization of its PTY handles.
				p.mu.Lock()
				select {
				case <-p.stop:
					p.mu.Unlock()
					p.retire(s, nil)
					return
				default:
					p.filling = s
				}
				p.mu.Unlock()
				_, readerDone, err = p.awaitOperation(p.fillTimeout, func() ([]byte, error) {
					return readWarmPrompt(s, "")
				})
				p.mu.Lock()
				p.filling = nil
				p.mu.Unlock()
			}
			if err == nil {
				select {
				case <-p.stop:
					p.retire(s, readerDone)
					return
				case p.ready <- s:
				}
				break
			}
			retired := p.retire(s, readerDone)
			select {
			case <-p.stop:
				return // shutdown's own Close must not produce a failure log
			default:
			}
			log.Printf("warm shell pool: prepare shell: %v", err)
			timer := time.NewTimer(delay)
			delay = nextWarmRetryDelay(delay)
			select {
			case <-p.stop:
				timer.Stop()
				return
			case <-timer.C:
			}
			// Do not accumulate failed children if their platform cleanup wedges.
			select {
			case <-p.stop:
				return
			case <-retired:
			}
		}
	}
}

func nextWarmRetryDelay(delay time.Duration) time.Duration {
	if delay >= 15*time.Second {
		return 30 * time.Second
	}
	return delay * 2
}

func (p *warmShellPool) TryClaim(cwd string, cols, rows int) (apty.Session, bool) {
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
		s.Close() // pool sessions close asynchronously; Stop owns the admitted work
		return nil, false
	default:
		p.wg.Add(1)
	}
	p.mu.Unlock()
	defer p.wg.Done()
	// Control characters are keystrokes to an interactive shell even inside
	// quotes (in particular CR/LF would submit an incomplete command).
	if cwd == "" || strings.IndexFunc(cwd, func(r rune) bool { return r < 32 || r == 127 }) >= 0 {
		p.retire(s, nil)
		return nil, false
	}
	command := "cd '" + strings.ReplaceAll(cwd, "'", "'\\''") + "'\r"
	if p.powershell {
		command = "Set-Location -LiteralPath '" + warmPowerShellQuotes.Replace(cwd) + "'\r"
	}
	suffix, done, err := p.awaitOperation(p.claimTimeout, func() ([]byte, error) {
		// Resize before the echoed command and prompt. Unknown/degenerate
		// dimensions keep the neutral fill geometry, as on the cold path.
		if cols > 0 && rows > 0 && !degenerateSize(cols, rows) {
			if err := s.Resize(uint16(rows), uint16(cols)); err != nil {
				return nil, err
			}
		}
		n, err := s.Write([]byte(command))
		if err == nil && n != len(command) {
			err = io.ErrShortWrite
		}
		if err != nil {
			return nil, err
		}
		return readWarmPrompt(s, cwd)
	})
	if err == nil {
		select {
		case <-p.stop:
		default:
			// Replay OSC 7 as well as its suffix so the new owner learns CWD.
			//
			// The claimed session is re-wrapped, so the token minted in fill
			// must be carried across by hand — it lives on the INNER value and
			// a zero field here reads as "cold spawn". spawnPane would then
			// bind "" to the pane, and detectHandStart returns on an empty
			// token before it parses anything: the shell emits an authentic
			// marker, waits its second, and runs the agent as typed. Same
			// shape as the Intercept field dropped from the pool's config
			// copy, and silent for the same reason.
			return &warmPoolSession{Session: s, pending: suffix, tok: interceptTokenOf(s)}, true
		}
	}
	// Neither Close, WaitExit nor a stuck syscall may hold spawnMu. This
	// operation owns only the discarded session, never a pane's replacement.
	p.retire(s, done)
	return nil, false
}

// awaitOperation bounds Read/Write/Resize without assuming Close unblocks them
// immediately. Its buffered result belongs solely to this operation.
func (p *warmShellPool) awaitOperation(timeout time.Duration, fn func() ([]byte, error)) ([]byte, <-chan struct{}, error) {
	type result struct {
		data []byte
		err  error
	}
	results := make(chan result, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		data, err := fn()
		results <- result{data, err}
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case r := <-results:
		<-done // successful handoff must have exactly one reader
		return r.data, done, r.err
	case <-timer.C:
		return nil, done, fmt.Errorf("warm shell: prompt confirmation timed out")
	case <-p.stop:
		return nil, done, fmt.Errorf("warm shell: pool stopped")
	}
}

// retire is called only by admitted workers, or by Stop after they have joined.
func (p *warmShellPool) retire(s apty.Session, operationDone <-chan struct{}) <-chan struct{} {
	done := make(chan struct{})
	p.cleanup.Add(1)
	go func() {
		defer p.cleanup.Done()
		defer close(done)
		s.Close()
		if wrapped, ok := s.(*warmPoolSession); ok {
			<-wrapped.closed
		}
		if operationDone != nil {
			<-operationDone
		}
	}()
	return done
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
		done := make(chan struct{})
		go func() {
			p.wg.Wait()
			// A ready send can win its select concurrently with stop closing.
			p.drain()
			p.cleanup.Wait()
			close(done)
		}()
		timeout := p.stopTimeout
		if timeout <= 0 {
			timeout = time.Second
		}
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
			// Windows WaitExit can wait forever; pane teardown must still run.
		}
	})
}

func (p *warmShellPool) drain() {
	for {
		select {
		case s := <-p.ready:
			p.retire(s, nil)
		default:
			return
		}
	}
}

// warmPoolSession preserves the prompt's unread suffix and makes ownership
// changes safe to clean up more than once. Read still has exactly one owner.
type warmPoolSession struct {
	apty.Session
	pending []byte
	once    sync.Once
	closed  chan struct{}
	// tok authenticates this shell's OSC 7770 markers. It is minted per SHELL,
	// not per pane: the pool's environment is captured once, pane-less, long
	// before any pane exists, so a pane-keyed token cannot be written here.
	// spawnPane binds whatever token the claimed shell carries to the pane it
	// becomes, and the daemon resolves the pane from the PTY a marker arrives
	// on — a token is never a lookup key.
	tok string
}

// interceptToken answers the token a claimed warm shell was started with, so
// spawnPane can bind it to the pane. TryClaim's signature is unchanged and
// returns apty.Session; this is the seam for recovering the per-shell value
// from it.
func (s *warmPoolSession) interceptToken() string {
	if s == nil {
		return ""
	}
	return s.tok
}

// interceptTokenOf recovers the token from a claimed session, answering "" for
// a cold spawn or a test double. A pane with no token never converts, which is
// the pre-feature behaviour.
func interceptTokenOf(s apty.Session) string {
	if b, ok := s.(interface{ interceptToken() string }); ok {
		return b.interceptToken()
	}
	return ""
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
		s.closed = make(chan struct{})
		go func() {
			defer close(s.closed)
			s.Session.Close()
			// Unclaimed sessions have no streamPTYOutput exit watcher. A
			// nested handoff wrapper must also join the original async reap.
			if wrapped, ok := s.Session.(*warmPoolSession); ok {
				<-wrapped.closed
			} else {
				s.Session.WaitExit()
			}
		}()
	})
	return nil // cleanup is asynchronous, like releasePanes
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
			sequence := data[:2+end+terminator]
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
				replay := append([]byte(nil), sequence...)
				return append(replay, data...), nil
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
