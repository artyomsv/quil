package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/artyomsv/quil/internal/config"
)

// Conversion must actually re-type the pane and carry the user's arguments.
// The reply alone proves nothing: a shell told to step aside for a pane that
// then spawns a shell again is the worst outcome of the whole feature.
func TestConvertAtLaunch_RetypesThePaneAndKeepsTheArguments(t *testing.T) {
	d, pane := handStartFixture(t, "")
	pane.handStart.token = "TOK"
	cwd := t.TempDir()
	pane.CWD = cwd

	d.detectHandStart(pane, pane.ID, []byte(handStartOSC+
		"cmd;TOK;claude;"+cwd+";;;--resume,5799ac23-d81a-462f-b176-515f9be6d07c,--enable-auto-mode"+
		"\x1b\\"))

	if got := pane.drainInput(); got != handStartReplyConvert {
		t.Fatalf("reply = %q, want %q", got, handStartReplyConvert)
	}
	if pane.Type != "claude-code" {
		t.Fatalf("pane type = %q, want claude-code", pane.Type)
	}
	want := []string{"--resume", "5799ac23-d81a-462f-b176-515f9be6d07c", "--enable-auto-mode"}
	if len(pane.InstanceArgs) != len(want) {
		t.Fatalf("InstanceArgs = %v, want %v", pane.InstanceArgs, want)
	}
	for i := range want {
		if pane.InstanceArgs[i] != want[i] {
			t.Fatalf("InstanceArgs = %v, want %v", pane.InstanceArgs, want)
		}
	}
	if pane.ConvertedFromTerminal != "terminal" {
		t.Errorf("ConvertedFromTerminal = %q, want terminal", pane.ConvertedFromTerminal)
	}
	// The converted pane runs the agent directly, so no further marker can
	// legitimately come from it. Leaving it armed would let the agent's own
	// output convert the pane again.
	if pane.handStart.token != "" {
		t.Error("the converted pane is still armed for interception")
	}
}

// A bare `claude` must leave InstanceArgs nil so the plugin's own args apply —
// InstanceArgs REPLACE them, so an empty non-nil slice would silently strip a
// user's configured --model.
func TestConvertAtLaunch_BareLaunchKeepsThePluginArgs(t *testing.T) {
	d, pane := handStartFixture(t, "")
	pane.handStart.token = "TOK"
	pane.InstanceArgs = []string{"left", "over"}

	d.detectHandStart(pane, pane.ID, []byte(handStartOSC+"cmd;TOK;claude;;;;"+"\x1b\\"))

	if pane.InstanceArgs != nil {
		t.Fatalf("InstanceArgs = %v, want nil so the plugin's own args apply", pane.InstanceArgs)
	}
}

// The shell's $PWD wins over the pane's recorded CWD. Pane.CWD is written by
// the TUI's OSC 7 handler, so a pane driven with no client attached carries a
// stale value and the agent would start in the wrong project.
func TestConvertAtLaunch_PrefersTheShellsWorkingDirectory(t *testing.T) {
	d, pane := handStartFixture(t, "")
	pane.handStart.token = "TOK"
	pane.CWD = t.TempDir() // stale
	live := t.TempDir()

	d.detectHandStart(pane, pane.ID, []byte(handStartOSC+"cmd;TOK;claude;"+live+";;;"+"\x1b\\"))

	want, err := filepath.EvalSymlinks(live)
	if err != nil {
		t.Fatal(err)
	}
	if pane.CWD != want {
		t.Fatalf("CWD = %q, want the shell's %q", pane.CWD, want)
	}
}

// A directory that no longer exists must not become the spawn CWD — the shell's
// value is validated exactly as any client-supplied path is.
func TestConvertAtLaunch_RejectsAnUnusableWorkingDirectory(t *testing.T) {
	d, pane := handStartFixture(t, "")
	pane.handStart.token = "TOK"
	keep := t.TempDir()
	pane.CWD = keep

	d.detectHandStart(pane, pane.ID, []byte(handStartOSC+"cmd;TOK;claude;/no/such/dir;;;"+"\x1b\\"))

	if pane.CWD != keep {
		t.Fatalf("CWD = %q, want the pane's own %q", pane.CWD, keep)
	}
}

// A converting pane raised ptyGen by running a SHELL, which wrote no session
// record. Without retiring, a stale record from a destroyed pane whose id was
// recycled would be resumed on top of the id the user actually typed.
func TestConvertAtLaunch_RetiresStaleSessionRecords(t *testing.T) {
	d, pane := handStartFixture(t, "")
	pane.handStart.token = "TOK"

	dir := config.SessionsDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	stale := []string{pane.ID + ".id", "codex-" + pane.ID + ".id", pane.ID + ".transcript"}
	for _, name := range stale {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("old-session"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	d.detectHandStart(pane, pane.ID, []byte(handStartOSC+"cmd;TOK;claude;;;;"+"\x1b\\"))

	for _, name := range stale {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("stale record %s survived the conversion", name)
		}
	}
}

// retirePaneSessionRecords and cleanupPaneArtifacts must agree on the file
// list. A record kind added to one and missed by the other is exactly the
// stale file the retirement exists to prevent.
func TestPaneSessionRecordNames_CoversEveryHookProducer(t *testing.T) {
	got := paneSessionRecordNames("P")
	want := map[string]bool{
		"P.id": true, "P.transcript": true, "P.settings.json": true,
		"opencode-P.id": true, "codex-P.id": true,
	}
	if len(got) != len(want) {
		t.Fatalf("names = %v, want %d entries", got, len(want))
	}
	for _, n := range got {
		if !want[n] {
			t.Errorf("unexpected record name %q", n)
		}
	}
}
