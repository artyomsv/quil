package daemon

import (
	"fmt"
	"testing"

	"github.com/artyomsv/quil/internal/plugin"
)

// fakeSession records PTY method calls without spawning a real process.
// Used to verify that spawnPane applies CWD before Start.
type fakeSession struct {
	cwd        string
	env        []string
	started    bool
	startCmd   string
	startArgs  []string
	cwdSetAt   int // call ordinal when SetCWD was invoked
	startedAt  int // call ordinal when Start was invoked
	callSeq    int
}

func (f *fakeSession) SetCWD(dir string) {
	f.callSeq++
	f.cwd = dir
	f.cwdSetAt = f.callSeq
}

func (f *fakeSession) SetEnv(env []string) {
	f.env = append(f.env, env...)
}

func (f *fakeSession) Start(cmd string, args ...string) error {
	f.callSeq++
	f.started = true
	f.startCmd = cmd
	f.startArgs = args
	f.startedAt = f.callSeq
	return nil
}

func (f *fakeSession) Read(buf []byte) (int, error)  { return 0, fmt.Errorf("not implemented") }
func (f *fakeSession) Write(data []byte) (int, error) { return 0, fmt.Errorf("not implemented") }
func (f *fakeSession) Resize(rows, cols uint16) error  { return nil }
func (f *fakeSession) Close() error                    { return nil }
func (f *fakeSession) Pid() int                        { return 0 }
func (f *fakeSession) WaitExit() int                   { return 0 }

func TestSpawnPane_SetsCWDBeforeStart(t *testing.T) {
	d := &Daemon{
		registry: plugin.NewRegistry(),
		session:  NewSessionManager(4096),
	}

	tests := []struct {
		name    string
		cwd     string
		wantCWD string
	}{
		{
			name:    "non-empty CWD is applied",
			cwd:     "/tmp/test-dir",
			wantCWD: "/tmp/test-dir",
		},
		{
			name:    "empty CWD is applied (no-op on both platforms)",
			cwd:     "",
			wantCWD: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeSession{}
			pane := &Pane{
				ID:   "test-pane",
				Type: "terminal",
				CWD:  tt.cwd,
			}

			err := d.spawnPane(pane, fake, false)
			if err != nil {
				t.Fatalf("spawnPane returned error: %v", err)
			}

			if fake.cwd != tt.wantCWD {
				t.Errorf("SetCWD: got %q, want %q", fake.cwd, tt.wantCWD)
			}

			if !fake.started {
				t.Fatal("Start was never called")
			}

			if fake.cwdSetAt == 0 {
				t.Fatal("SetCWD was never called")
			}

			if fake.cwdSetAt >= fake.startedAt {
				t.Errorf("SetCWD (call %d) must be called before Start (call %d)",
					fake.cwdSetAt, fake.startedAt)
			}
		})
	}
}
