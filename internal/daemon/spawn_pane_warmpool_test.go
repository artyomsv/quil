package daemon

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/plugin"
	apty "github.com/artyomsv/quil/internal/pty"
	"github.com/artyomsv/quil/internal/sandbox"
	"github.com/artyomsv/quil/internal/shellinit"
)

// warmSpawnPool uses the real pool worker and constructor seam. Its first shell
// reaches a prompt and can confirm the requested CWD; replacements stay in fill
// until Stop closes them. No real shell is launched.
func warmSpawnPool(t *testing.T, d *Daemon, cwd string) (*scriptedWarmSession, apty.Session) {
	t.Helper()
	s := newScriptedWarmSession()
	s.chunks <- []byte("\x1b]133;A\x07")
	s.response = []byte("\x1b]133;A\x07" + warmOSC7(cwd))
	cfg := shellinit.Configure("bash", config.QuilDir())
	var calls atomic.Int64
	d.shellPool = warmTestPool(t, warmPoolShellConfig{Cmd: cfg.Cmd, Args: cfg.Args, Env: cfg.Env}, 1, func() apty.Session {
		if calls.Add(1) == 1 {
			return s
		}
		return newScriptedWarmSession()
	})
	warmReady(t, d.shellPool, 1)
	// Remember the exact parked session, including the pool's Close wrapper.
	// This does not claim it or return a vacancy to the refill worker.
	parked := <-d.shellPool.ready
	d.shellPool.ready <- parked
	return s, parked
}

func TestSpawnPane_ClaimsWarmShellWithoutStartingAnotherProcess(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	d := New(config.Default())
	if d.shellPool != nil {
		t.Fatal("New constructed the pool before user plugin overrides are loaded")
	}
	d.registry.Get("terminal").Command.Cmd = "bash"
	pane := &Pane{ID: "warm-hit", Type: "terminal", CWD: t.TempDir(), Cols: 137, Rows: 43}
	s, parked := warmSpawnPool(t, d, pane.CWD)
	cold := &fakeSession{}
	if err := d.spawnPane(pane, cold, false); err != nil {
		t.Fatalf("spawnPane: %v", err)
	}
	pane.PluginMu.Lock()
	installed, generation := pane.PTY, pane.ptyGen
	pane.PluginMu.Unlock()
	if installed != nil {
		t.Cleanup(func() { installed.Close() })
	}
	claimed, ok := installed.(*warmPoolSession)
	if !ok || claimed.Session != parked {
		t.Fatalf("installed PTY %T is not the claimed pool session", installed)
	}
	if cold.started || cold.callSeq != 0 || len(cold.env) != 0 {
		t.Fatal("pool hit configured or started the unused cold PTY")
	}
	// The scripted shell's Start would also panic on a second call: its
	// started channel is already closed. CWD must still record the fill-time
	// directory; relocating the live process uses Write, never SetCWD.
	if s.callSeq != 2 || s.cwdSetAt >= s.startedAt {
		t.Fatalf("warm shell startup repeated: sequence=%d cwd=%d start=%d", s.callSeq, s.cwdSetAt, s.startedAt)
	}
	if generation != 1 {
		t.Fatalf("PTY generation = %d, want 1 from the common install tail", generation)
	}
	if len(s.writes) != 1 {
		t.Fatal("pool shell was not relocated through the claim handshake")
	}
	if s.sizeAtWrite.Load() != 43<<16|137 {
		t.Fatal("spawnPane discarded the pane's initial size on a warm claim")
	}
}

func TestSpawnPane_WarmPoolBypasses(t *testing.T) {
	tests := []struct {
		name        string
		typ         string
		restoring   bool
		integration bool
		custom      string
	}{
		{"restore", "terminal", true, true, ""},
		{"different shell plugin", "terminal-wide", false, true, ""},
		{"AI plugin", "claude-code", false, true, ""},
		{"integration disabled", "terminal", false, false, ""},
		{"unknown plugin fallback", "unknown-plugin", false, true, ""},
		{"reloaded shell", "terminal", false, true, "shell"},
		{"plugin environment", "terminal", false, true, "env"},
		{"plugin history", "terminal", false, true, "history"},
		{"instance arguments", "terminal", false, true, "args"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("QUIL_HOME", t.TempDir())
			d := New(config.Default())
			registerClaudePlugin(t, d)
			d.registry.Get("terminal").Command.Cmd = "bash"
			d.registry.Get("terminal").Command.ShellIntegration = tt.integration
			pane := &Pane{ID: "warm-bypass", Type: tt.typ, CWD: t.TempDir()}
			s, parked := warmSpawnPool(t, d, pane.CWD)
			switch tt.custom {
			case "shell":
				d.registry.Get("terminal").Command.Cmd = "zsh"
			case "env":
				d.registry.Get("terminal").Command.Env = []string{"WARM_TEST=custom"}
			case "history":
				d.registry.Get("terminal").Command.RecordHistory = true
			case "args":
				pane.InstanceArgs = []string{"--login"}
			}
			cold := &fakeSession{}
			if err := d.spawnPane(pane, cold, tt.restoring); err != nil {
				t.Fatalf("spawnPane: %v", err)
			}
			pane.PluginMu.Lock()
			installed := pane.PTY
			pane.PluginMu.Unlock()
			if !cold.started || installed != cold {
				t.Fatal("bypass did not start and install the normal PTY")
			}
			assertWarmShellUntouched(t, d.shellPool, s, parked)
		})
	}
}

func assertWarmShellUntouched(t *testing.T, pool *warmShellPool, s *scriptedWarmSession, parked apty.Session) {
	t.Helper()
	if len(s.writes) != 0 || s.closeCalls.Load() != 0 {
		t.Fatal("bypass attempted to claim or close the warm shell")
	}
	select {
	case got := <-pool.ready:
		pool.ready <- got
		if got != parked {
			t.Fatal("bypass consumed the parked session and replaced it")
		}
	default:
		t.Fatal("bypass consumed the parked session")
	}
}

func TestSpawnPane_SandboxBypassesWarmPool(t *testing.T) {
	for _, typ := range []string{"terminal", sandboxPaneType("terminal")} {
		t.Run(typ, func(t *testing.T) {
			d, pane, _ := sandboxCallsiteFixture(t)
			d.registry = plugin.NewRegistry()
			d.registry.Get("terminal").Command.Cmd = "bash"
			pane.Type = typ
			if typ != "terminal" {
				pane.SandboxImage = "" // prefix alone must also exclude pooling
			}
			prev := sandboxProbeFn
			sandboxProbeFn = func(context.Context) (sandbox.Info, error) {
				return sandbox.Info{ServerVersion: "29.6.2", OSType: "linux", Arch: "amd64"}, nil
			}
			t.Cleanup(func() { sandboxProbeFn = prev })
			s, parked := warmSpawnPool(t, d, pane.CWD)
			cold := &fakeSession{}
			if err := d.spawnPane(pane, cold, false); err != nil {
				t.Fatalf("spawnPane: %v", err)
			}
			if !cold.started || (filepath.Base(cold.startCmd) != "docker" && filepath.Base(cold.startCmd) != "docker.exe") {
				t.Fatalf("sandbox started %q, want the normal Docker path", cold.startCmd)
			}
			assertWarmShellUntouched(t, d.shellPool, s, parked)
		})
	}
}

func TestStop_ClosesWarmShellsOutsideTheSessionManager(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	d := New(config.Default())
	s, _ := warmSpawnPool(t, d, t.TempDir())
	if len(d.session.Tabs()) != 0 {
		t.Fatal("test must contain no pane-owned PTYs")
	}
	done := make(chan struct{})
	go func() { d.Stop(); close(done) }()
	warmWait(t, "daemon Stop", done)
	if s.closeCalls.Load() != 1 || len(d.shellPool.ready) != 0 {
		t.Fatal("daemon Stop did not close and drain its parked shells")
	}
}
