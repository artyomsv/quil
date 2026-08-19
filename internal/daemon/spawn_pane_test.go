package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/plugin"
)

// fakeSession records PTY method calls without spawning a real process.
// Used to verify that spawnPane applies CWD before Start.
type fakeSession struct {
	cwd       string
	env       []string
	started   bool
	startCmd  string
	startArgs []string
	cwdSetAt  int // call ordinal when SetCWD was invoked
	startedAt int // call ordinal when Start was invoked
	callSeq   int
	resizes   [][2]uint16 // recorded (rows, cols) Resize calls
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

func (f *fakeSession) Read(buf []byte) (int, error)   { return 0, fmt.Errorf("not implemented") }
func (f *fakeSession) Write(data []byte) (int, error) { return 0, fmt.Errorf("not implemented") }
func (f *fakeSession) Resize(rows, cols uint16) error {
	f.resizes = append(f.resizes, [2]uint16{rows, cols})
	return nil
}
func (f *fakeSession) Close() error  { return nil }
func (f *fakeSession) Pid() int      { return 0 }
func (f *fakeSession) WaitExit() int { return 0 }

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

// loadRegistryWithOpencodeClaimingClaude builds an opencode plugin that ALSO
// declares the claude sessions source. Legal TOML — registry.go validates the
// value but never asserts that only one plugin may claim it — and it is the
// exact shape that makes spawnPane's dispatch arms overlap.
func loadRegistryWithOpencodeClaimingClaude(t *testing.T) *plugin.Registry {
	t.Helper()
	dir := t.TempDir()
	toml := "[plugin]\n" +
		"name = \"opencode\"\n" +
		"display_name = \"OpenCode\"\n" +
		"category = \"ai\"\n" +
		"schema_version = 1\n" +
		"[command]\n" +
		"cmd = \"echo\"\n" +
		"prompts_cwd = true\n" + // required alongside sessions
		"sessions = \"claude\"\n"
	if err := os.WriteFile(filepath.Join(dir, "opencode.toml"), []byte(toml), 0o600); err != nil {
		t.Fatalf("write plugin toml: %v", err)
	}
	reg := plugin.NewRegistry()
	if err := reg.LoadFromDir(dir); err != nil {
		t.Fatalf("LoadFromDir: %v", err)
	}
	return reg
}

// spawnPane's env/args dispatch used to be `switch pane.Type`, disjoint by
// construction. It is now a switch over predicates, and any plugin file may
// legally set sessions = "claude" — so an opencode plugin that does would take
// the claude arm and be handed `--settings <path>`, a flag opencode does not
// have, while skipping the session read it needs.
//
// opencode is therefore tested FIRST, and this pins that ordering: nothing else
// in the suite runs an opencode-typed pane through the real dispatch.
func TestSpawnPane_OpencodeArmWinsOverAClaimedClaudeSource(t *testing.T) {
	d := &Daemon{
		registry: loadRegistryWithOpencodeClaimingClaude(t),
		session:  NewSessionManager(4096),
		cfg:      config.Default(),
	}
	fake := &fakeSession{}
	pane := &Pane{ID: "test-pane", Type: "opencode"}

	if err := d.spawnPane(pane, fake, false); err != nil {
		t.Fatalf("spawnPane: %v", err)
	}

	for _, a := range fake.startArgs {
		if a == "--settings" {
			t.Fatalf("opencode pane was handed claude's --settings; args=%v", fake.startArgs)
		}
	}
}
