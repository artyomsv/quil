package daemon

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/plugin"
)

// redrawKick exists because plugins with ghost_buffer = false get no replay on
// attach, so a reconnecting client is sent nothing for a pane whose process is
// alive and mid-conversation. The rectangle stays blank until the child writes
// something, and a full-screen program has no reason to do that unprompted.
//
// The mechanism is the plugin's declared RedrawKey written to stdin. It is
// opt-in per plugin because it is INPUT, not a signal: a program reading its
// own stdin for data would receive it as data. SIGWINCH would need no opt-in
// and does not work — claude-code emits 0 bytes from its main UI after a
// resize, where vim emits ~5 KB.

// recordingSession captures stdin writes so a test can observe what the redraw
// kick actually delivered to the child.
type recordingSession struct {
	fakeSession
	mu      sync.Mutex
	written []byte
}

func (r *recordingSession) Write(data []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.written = append(r.written, data...)
	return len(data), nil
}

func (r *recordingSession) got() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(r.written)
}

// waitForInput polls until the pane's input writer has drained the queue into
// the PTY. EnqueueInput hands off to a goroutine, so the write is not visible
// synchronously.
func waitForInput(t *testing.T, s *recordingSession, want string) bool {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s.got() == want {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// daemonWithPlugin loads a single plugin from TOML, so these tests also cover
// the redraw_key parsing rather than hand-constructing the struct — a field
// that never reaches the registry would otherwise pass every assertion here.
func daemonWithPlugin(t *testing.T, name, redrawKey string, ghost bool) *Daemon {
	t.Helper()
	dir := t.TempDir()
	redrawLine := ""
	if redrawKey != "" {
		// Written as a TOML escape so this exercises the same decoding path a
		// real plugin file takes.
		redrawLine = "redraw_key = \"\\f\"\n"
		if redrawKey != "\f" {
			t.Fatalf("fixture only models the \\f case, got %q", redrawKey)
		}
	}
	toml := "[plugin]\n" +
		"name = \"" + name + "\"\n" +
		"display_name = \"" + name + "\"\n" +
		"category = \"test\"\n" +
		"schema_version = 1\n" +
		"[command]\n" +
		"cmd = \"echo\"\n" +
		"[persistence]\n" +
		"ghost_buffer = " + map[bool]string{true: "true", false: "false"}[ghost] + "\n" +
		redrawLine
	if err := os.WriteFile(filepath.Join(dir, name+".toml"), []byte(toml), 0o600); err != nil {
		t.Fatalf("write plugin toml: %v", err)
	}
	reg := plugin.NewRegistry()
	if err := reg.LoadFromDir(dir); err != nil {
		t.Fatalf("LoadFromDir: %v", err)
	}
	if reg.Get(name) == nil {
		t.Fatalf("plugin %q not loaded", name)
	}
	return &Daemon{registry: reg, session: NewSessionManager(4096)}
}

func TestRedrawKick_SendsThePluginsDeclaredKey(t *testing.T) {
	d := daemonWithPlugin(t, "claude-code", "\f", false)
	pty := &recordingSession{}
	pane := &Pane{ID: "p1", Type: "claude-code", PTY: pty}
	t.Cleanup(pane.StopInput)

	d.redrawKick(pane, "claude-code")

	if !waitForInput(t, pty, "\f") {
		t.Errorf("child received %q, want %q (Ctrl+L)", pty.got(), "\f")
	}
}

// TestRedrawKick_SilentWithoutAnOptIn is the safety property. Every pane that
// got no replay reaches this call, including plain terminals — and a terminal
// might be sitting in `cat > file` or at a password prompt, where an injected
// form feed is data corruption, not a repaint.
func TestRedrawKick_SilentWithoutAnOptIn(t *testing.T) {
	tests := []struct {
		name      string
		plugin    string
		redrawKey string
		paneType  string
	}{
		{"plugin declares no key", "terminal", "", "terminal"},
		{"pane type is not registered", "terminal", "\f", "some-unknown-plugin"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := daemonWithPlugin(t, tt.plugin, tt.redrawKey, true)
			pty := &recordingSession{}
			pane := &Pane{ID: "p1", Type: tt.paneType, PTY: pty}
			t.Cleanup(pane.StopInput)

			d.redrawKick(pane, tt.paneType)

			// Give the writer goroutine a chance to deliver anything it was
			// wrongly handed, so this cannot pass by being merely early.
			time.Sleep(100 * time.Millisecond)
			if got := pty.got(); got != "" {
				t.Errorf("child received %q, want nothing written", got)
			}
		})
	}
}

// TestPaneSize_ConcurrentResizeAndRead is a race-detector regression guard.
//
// pane.Cols/Rows used to be written just OUTSIDE handleResizePane's PluginMu
// span, and were documented on the struct as "immutable once set". They are
// not: handleResizePane rewrites them on every genuine resize from the
// resizing conn's dispatch goroutine, while attach, lazy spawn, restart,
// screenshot, snapshot, and the PTY output goroutine's resizeKick all read
// them. Six live data races, invisible because no test ran a resize
// concurrently with a read — `go test -race` only reports races it executes.
//
// This fails under -race if the write moves back out of the lock, or if a new
// reader takes the field without it.
func TestPaneSize_ConcurrentResizeAndRead(t *testing.T) {
	d := &Daemon{session: NewSessionManager(4096)}
	pane := &Pane{ID: "p1", PTY: &fakeSession{}}
	d.session.panes["p1"] = pane

	const iterations = 200

	// Built on the test goroutine, not inside the writer below: resizeMsg
	// calls t.Fatalf, and testing.T's failure methods are only valid from the
	// goroutine running the test. Alternating sizes so the same-size guard
	// never short-circuits the write — a guard hit would make this vacuous.
	msgs := make([]*ipc.Message, iterations)
	for i := range msgs {
		msgs[i] = resizeMsg(t, "p1", uint16(100+i%2), 40)
	}

	done := make(chan struct{})

	// Writer: the resizing client's dispatch goroutine.
	go func() {
		defer close(done)
		for _, m := range msgs {
			d.handleResizePane(m)
		}
	}()

	// Reader: stands in for every guarded reader, all of which take the pair
	// the same way via paneSize.
	for i := 0; i < iterations; i++ {
		cols, rows := paneSize(pane)
		if cols != 0 && (cols < 100 || cols > 101 || rows != 40) {
			t.Fatalf("torn read: %dx%d", cols, rows)
		}
	}
	<-done
}
