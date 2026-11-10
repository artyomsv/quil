package daemon

import (
	"os"
	"reflect"
	"testing"

	"github.com/artyomsv/quil/internal/claudehook"
	"github.com/artyomsv/quil/internal/codexhook"
	"github.com/artyomsv/quil/internal/plugin"
)

// Issue #288: a terminal pane where the user typed `claude --continue` is
// converted to a claude pane with InstanceArgs ["--continue"]. Restore always
// stripped that flag; restart (Alt+R) kept it and appended `--resume <own>`
// beside it. Claude obeyed `--continue` — the newest session in the directory,
// which was ANOTHER pane's — so two panes ended up on one transcript.

const typedRestartID = "3b8836ad-9233-4253-85f2-8b1ef95f53a8"

// The pure argv rule: --resume names the session, so a typed selector beside
// it is dropped and the toggles typed with it stay.
func TestResolveSpawnArgs_RestartResumeDropsTypedContinue(t *testing.T) {
	pane := &Pane{
		ID:           "pane-4779a4b0",
		InstanceArgs: []string{"--continue", "--chrome"},
		PluginState:  map[string]string{"session_id": typedRestartID},
	}
	got := resolveSpawnArgs(claudeResumePlugin(), pane, false, true, typedRestartID, claimAny)
	want := []string{"--chrome", "--resume", typedRestartID}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("resolveSpawnArgs:\n  got:  %v\n  want: %v", got, want)
	}
}

// The reported case through spawnPane: the hook recorded the pane's own
// session and its transcript exists.
func TestSpawnPane_RestartWithTypedContinueResumesItsOwnSession(t *testing.T) {
	d := newTestDaemon(t)
	withClaudePlugin(t, d)
	stubResumeSeams(t, claudehook.SessionRecord{ID: typedRestartID, TranscriptPath: transcriptFor(typedRestartID)}, probeOnly(transcriptFor(typedRestartID)))

	pane := &Pane{
		ID:           "pane-4779a4b0",
		Type:         "claude-code",
		InstanceArgs: []string{"--continue", "--chrome"},
		PluginState:  map[string]string{"session_id": typedRestartID},
	}
	fs := &fakeSession{}
	if err := d.spawnPane(pane, fs, false); err != nil {
		t.Fatalf("spawnPane: %v", err)
	}

	if !argsHavePair(fs.startArgs, "--resume", typedRestartID) {
		t.Errorf("start args = %q, want --resume %s", fs.startArgs, typedRestartID)
	}
	if argsHaveFlag(fs.startArgs, "--continue") {
		t.Errorf("start args = %q, want no --continue: claude would open the newest session in the directory", fs.startArgs)
	}
	if !argsHaveFlag(fs.startArgs, "--chrome") {
		t.Errorf("start args = %q, want the typed toggle --chrome kept", fs.startArgs)
	}
	// Stripped per spawn, never persisted: emptying InstanceArgs would switch
	// the override off and bring the plugin's own args back.
	if want := []string{"--continue", "--chrome"}; !reflect.DeepEqual(pane.InstanceArgs, want) {
		t.Errorf("InstanceArgs = %v, want %v unchanged", pane.InstanceArgs, want)
	}
}

// The pure matrix for preassign_id restarts, including a selector that comes
// from the PLUGIN's own args rather than the pane's.
func TestResolveSpawnArgs_RestartTypedPreassignMatrix(t *testing.T) {
	withPluginArgs := claudeResumePlugin()
	withPluginArgs.Command.Args = []string{"--continue"}

	tests := []struct {
		name       string
		plugin     *plugin.PanePlugin
		args       []string
		ownsRecord bool
		resumeID   string
		want       []string
	}{
		{"recorded, no transcript", claudeResumePlugin(), []string{"--continue"}, true, "", []string{"--session-id", typedRestartID}},
		{"nothing recorded keeps the typed flag", claudeResumePlugin(), []string{"--continue"}, false, "", []string{"--continue"}},
		{"plugin args carry the selector", withPluginArgs, nil, true, typedRestartID, []string{"--resume", typedRestartID}},
		{"override of plugin args stays an override", withPluginArgs, []string{"--continue"}, true, typedRestartID, []string{"--resume", typedRestartID}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pane := &Pane{
				ID:           "pane-4779a4b0",
				InstanceArgs: tt.args,
				PluginState:  map[string]string{"session_id": typedRestartID},
			}
			got := resolveSpawnArgs(tt.plugin, pane, false, tt.ownsRecord, tt.resumeID, claimAny)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("resolveSpawnArgs:\n  got:  %v\n  want: %v", got, tt.want)
			}
		})
	}
}

// codex's `resume --last` must go as a unit: a `--last` left behind lands in
// front of the appended `resume <id>` and codex refuses it.
func TestStripSessionArgs_CodexResumeSelectors(t *testing.T) {
	tests := []struct {
		args []string
		want []string
	}{
		{[]string{"resume", "--last"}, []string{}},
		{[]string{"resume", "--last", "--search"}, []string{"--search"}},
		{[]string{"resume", "--all", restartTestSessionID}, []string{}},
		{[]string{"resume", restartTestSessionID, "--search"}, []string{"--search"}},
		// Selectors are not only adjacent to the subcommand (review M-1).
		{[]string{"resume", restartTestSessionID, "--all"}, []string{}},
		{[]string{"resume", "--model", "gpt-5", "--last"}, []string{"--model", "gpt-5"}},
		{[]string{"resume", "--include-non-interactive", "--last"}, []string{}},
		// A value flag's value is not the session id.
		{[]string{"resume", "-c", "model=gpt-5", restartTestSessionID}, []string{"-c", "model=gpt-5"}},
		// A prompt was the conversation's first message: it is not sent into
		// the reopened conversation again, and a `--` prompt never becomes a
		// flag in front of the appended `resume <id>`.
		{[]string{"resume", "--last", "fix the build"}, []string{}},
		{[]string{"resume", restartTestSessionID, "fix the build", "--search"}, []string{"--search"}},
		{[]string{"resume", "--", restartTestSessionID}, []string{}},
		{[]string{"resume", "--search", "--last", "--", "--help"}, []string{"--search"}},
		// Feature toggles take a value.
		{[]string{"resume", restartTestSessionID, "--enable", "web_search"}, []string{"--enable", "web_search"}},
		{[]string{"resume", "--disable", "x", "--last"}, []string{"--disable", "x"}},
		// Images belong to the dropped prompt, and go with all their values.
		{[]string{"resume", "--last", "-i", "TEST-a.png", "TEST-b.png", "--search"}, []string{"--search"}},
		{[]string{"resume", "--last", "--image=TEST-a.png", "TEST-b.png"}, []string{}},
	}
	for _, tt := range tests {
		if got := stripSessionArgs("codex", tt.args); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("stripSessionArgs(%v) = %v, want %v", tt.args, got, tt.want)
		}
	}
}

// After /clear the hook names a new session with no transcript yet, so no
// --resume is possible. The typed --continue must still go: kept, it was the
// whole argv and opened a sibling's session.
func TestSpawnPane_RestartWithTypedContinueAndNoTranscriptUsesItsOwnID(t *testing.T) {
	d := newTestDaemon(t)
	withClaudePlugin(t, d)
	stubResumeSeams(t, claudehook.SessionRecord{ID: typedRestartID, TranscriptPath: transcriptFor(typedRestartID)}, probeNothing)

	pane := &Pane{
		ID:           "pane-4779a4b0",
		Type:         "claude-code",
		InstanceArgs: []string{"--continue"},
		PluginState:  map[string]string{"session_id": typedRestartID},
	}
	fs := &fakeSession{}
	if err := d.spawnPane(pane, fs, false); err != nil {
		t.Fatalf("spawnPane: %v", err)
	}

	if !argsHavePair(fs.startArgs, "--session-id", typedRestartID) {
		t.Errorf("start args = %q, want --session-id %s", fs.startArgs, typedRestartID)
	}
	if argsHaveFlag(fs.startArgs, "--continue") {
		t.Errorf("start args = %q, want no --continue", fs.startArgs)
	}
}

// The boundary: the first spawn after conversion has no record of its own
// (conversion retired it), so the typed flag is what the user asked for and
// must reach claude unchanged.
func TestSpawnPane_ConvertedFirstSpawnKeepsTypedContinue(t *testing.T) {
	d := newTestDaemon(t)
	withClaudePlugin(t, d)
	stubHookSessionID(t, map[string]string{})

	pane := &Pane{
		ID:           "pane-4779a4b0",
		Type:         "claude-code",
		InstanceArgs: []string{"--continue"},
	}
	pane.handStart.disownRecords = true
	fs := &fakeSession{}
	if err := d.spawnPane(pane, fs, false); err != nil {
		t.Fatalf("spawnPane: %v", err)
	}

	if !argsHaveFlag(fs.startArgs, "--continue") {
		t.Errorf("start args = %q, want the typed --continue on the first spawn", fs.startArgs)
	}
	if argsHaveFlag(fs.startArgs, "--session-id") || argsHaveFlag(fs.startArgs, "--resume") {
		t.Errorf("start args = %q, want no second session selector", fs.startArgs)
	}
	if want := []string{"--continue"}; !reflect.DeepEqual(pane.InstanceArgs, want) {
		t.Errorf("InstanceArgs = %v, want %v kept", pane.InstanceArgs, want)
	}
}

// session_scrape has the same hole: a typed selector skipped the recorded
// session entirely on restart. A recorded one now wins; with nothing recorded
// the typed selector stays, since it is the only information there is.
func TestResolveSpawnArgs_RestartTypedSessionScrape(t *testing.T) {
	opencodePlugin := &plugin.PanePlugin{
		Name:        "opencode",
		Command:     plugin.CommandConfig{Cmd: "opencode"},
		Persistence: plugin.PersistenceConfig{Strategy: "session_scrape", ResumeArgs: []string{"--continue"}},
	}
	codexPlugin := &plugin.PanePlugin{
		Name:        plugin.CodexPluginName,
		Command:     plugin.CommandConfig{Cmd: "codex"},
		Persistence: plugin.PersistenceConfig{Strategy: "session_scrape"},
	}
	// A TOML may configure a placeholder template as its fallback; with no
	// record it expands to nothing and must not cost the pane its selector.
	opencodeTemplated := &plugin.PanePlugin{
		Name:        "opencode",
		Command:     plugin.CommandConfig{Cmd: "opencode"},
		Persistence: plugin.PersistenceConfig{Strategy: "session_scrape", ResumeArgs: []string{"--session", "{session_id}"}},
	}
	const typedCodexID = "11111111-2222-4333-8444-555555555555"

	origOpencode, origCodex := readOpencodeSessionIDFn, readCodexSessionFn
	t.Cleanup(func() { readOpencodeSessionIDFn, readCodexSessionFn = origOpencode, origCodex })

	tests := []struct {
		name       string
		plugin     *plugin.PanePlugin
		args       []string
		opencodeID string
		codexID    string
		want       []string
	}{
		{"opencode recorded", opencodePlugin, []string{"--continue", "--print-logs"}, "sess-1234", "", []string{"--print-logs", "--session", "sess-1234"}},
		{"opencode nothing recorded", opencodePlugin, []string{"--continue"}, "", "", []string{"--continue"}},
		{"codex recorded", codexPlugin, []string{"resume", typedCodexID}, "", restartTestSessionID, []string{"resume", restartTestSessionID}},
		{"codex nothing recorded", codexPlugin, []string{"resume", typedCodexID}, "", "", []string{"resume", typedCodexID}},
		{"codex resume --last recorded", codexPlugin, []string{"resume", "--last"}, "", restartTestSessionID, []string{"resume", restartTestSessionID}},
		{"codex selector after the id", codexPlugin, []string{"resume", typedCodexID, "--all"}, "", restartTestSessionID, []string{"resume", restartTestSessionID}},
		{"codex feature toggle keeps its value", codexPlugin, []string{"resume", typedCodexID, "--enable", "web_search"}, "", restartTestSessionID, []string{"--enable", "web_search", "resume", restartTestSessionID}},
		// A trailing greedy -i would swallow the appended `resume <id>` (M-2).
		{"codex trailing image", codexPlugin, []string{"resume", "--last", "-i", "TEST-a.png"}, "", restartTestSessionID, []string{"resume", restartTestSessionID}},
		{"codex literal prompt after --", codexPlugin, []string{"resume", "--last", "--", "--help"}, "", restartTestSessionID, []string{"resume", restartTestSessionID}},
		{"codex selector after an option pair", codexPlugin, []string{"resume", "--model", "gpt-5", "--last"}, "", restartTestSessionID, []string{"--model", "gpt-5", "resume", restartTestSessionID}},
		{"opencode placeholder fallback, nothing recorded", opencodeTemplated, []string{"--continue"}, "", "", []string{"--continue"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			readOpencodeSessionIDFn = func(string) (string, error) {
				if tt.opencodeID == "" {
					return "", os.ErrNotExist
				}
				return tt.opencodeID, nil
			}
			readCodexSessionFn = func(string) (codexhook.SessionRecord, error) {
				return codexhook.SessionRecord{ID: tt.codexID}, nil
			}
			pane := &Pane{ID: "pane-abc", InstanceArgs: tt.args}
			got := resolveSpawnArgs(tt.plugin, pane, false, true, "", claimAny)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("resolveSpawnArgs:\n  got:  %v\n  want: %v", got, tt.want)
			}
		})
	}
}
