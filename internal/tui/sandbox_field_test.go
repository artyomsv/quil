package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/plugin"
)

// promptsCWDPlugin is the shape that can show the row: the sandbox mounts the
// checkout the CWD step settles on.
func promptsCWDPlugin() *plugin.PanePlugin {
	return &plugin.PanePlugin{
		Name:    "claude-code",
		Command: plugin.CommandConfig{PromptsCWD: true},
	}
}

// The answer describes the DAEMON's machine — the container runs where the
// daemon runs. One shared answer would be the 2026-09-03 bug again, where a
// single registry served every destination and the last host to reply spoke
// for all of them.
func TestSandboxAvailableFor_IsPerDestination(t *testing.T) {
	var m Model
	m.applySandboxCap("", ipc.SandboxCapRespPayload{Available: true})
	m.applySandboxCap("remote", ipc.SandboxCapRespPayload{Error: "docker not available"})

	if !m.sandboxAvailableFor("") {
		t.Error("the local daemon's own answer was not filed")
	}
	if m.sandboxAvailableFor("remote") {
		t.Error("a remote daemon without docker reads as available")
	}
	// A destination that has never answered is unavailable, NOT a fallback to
	// some other machine's answer: the client cannot see any daemon's Docker
	// engine itself, and a wrong offer means a pane that dies at spawn.
	if m.sandboxAvailableFor("never-asked") {
		t.Error("a silent destination reads as available")
	}
}

// The row is gated on the PINNED answer, so a capability response landing
// mid-dialog cannot add or remove a row under a live cursor.
func TestShowSandboxField_UsesThePinnedAnswer(t *testing.T) {
	var m Model
	p := promptsCWDPlugin()

	m.applySandboxCap("", ipc.SandboxCapRespPayload{Available: true})
	if m.showSandboxField(p) {
		t.Error("the row appeared before the dialog pinned an answer")
	}
	m.resetSandboxField("")
	if !m.showSandboxField(p) {
		t.Error("the row is absent after pinning an available answer")
	}
	// A later answer must not change the open dialog.
	m.applySandboxCap("", ipc.SandboxCapRespPayload{Error: "engine stopped"})
	if !m.showSandboxField(p) {
		t.Error("a late answer removed a row from an open dialog")
	}
}

// A plugin that never asks for a directory has nothing to mount.
func TestShowSandboxField_NeedsPromptsCWD(t *testing.T) {
	var m Model
	m.applySandboxCap("", ipc.SandboxCapRespPayload{Available: true})
	m.resetSandboxField("")

	if m.showSandboxField(&plugin.PanePlugin{Name: "terminal"}) {
		t.Error("the row is offered for a plugin with no CWD step")
	}
}

// setupFieldCount and setupFieldKind must agree, or the cursor lands on a row
// the renderer never draws.
func TestSetupFieldKind_SandboxSitsBetweenWorktreeAndSession(t *testing.T) {
	var m Model
	m.applySandboxCap("", ipc.SandboxCapRespPayload{Available: true})
	m.resetSandboxField("")

	p := promptsCWDPlugin()
	p.Command.Sessions = "claude"

	var kinds []string
	for i := 0; i < m.setupFieldCount(p); i++ {
		k, _ := m.setupFieldKind(p, i)
		kinds = append(kinds, k)
	}
	want := []string{"cwd", "worktree", "sandbox", "session", "continue"}
	if strings.Join(kinds, ",") != strings.Join(want, ",") {
		t.Errorf("field order = %v, want %v", kinds, want)
	}
}

// With the row hidden the walk must be exactly what it was before this
// feature: every existing dialog keeps its layout.
func TestSetupFieldKind_UnchangedWhenTheRowIsHidden(t *testing.T) {
	var m Model // nothing pinned: unavailable
	p := promptsCWDPlugin()
	p.Command.Sessions = "claude"

	var kinds []string
	for i := 0; i < m.setupFieldCount(p); i++ {
		k, _ := m.setupFieldKind(p, i)
		kinds = append(kinds, k)
	}
	want := []string{"cwd", "worktree", "session", "continue"}
	if strings.Join(kinds, ",") != strings.Join(want, ",") {
		t.Errorf("field order = %v, want %v", kinds, want)
	}
}

func TestHandleSandboxFieldKey_SpaceToggles(t *testing.T) {
	var m Model
	if !m.handleSandboxFieldKey(tea.KeyPressMsg{Code: ' ', Text: " "}) {
		t.Fatal("space was not consumed")
	}
	if !m.sandboxOn {
		t.Error("space did not enable the sandbox")
	}
	m.handleSandboxFieldKey(tea.KeyPressMsg{Code: ' ', Text: " "})
	if m.sandboxOn {
		t.Error("space did not disable the sandbox")
	}
}

// Typing must not reach the field while the switch is off, or the row would
// swallow keys that mean nothing there.
func TestHandleSandboxFieldKey_TypingOnlyWhileOn(t *testing.T) {
	var m Model
	if m.handleSandboxFieldKey(tea.KeyPressMsg{Code: 'a', Text: "a"}) {
		t.Error("a keystroke was consumed while the sandbox is off")
	}
	if m.sandboxImage != "" {
		t.Errorf("image changed while off: %q", m.sandboxImage)
	}

	m.sandboxOn = true
	for _, r := range "alpine" {
		m.handleSandboxFieldKey(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	if m.sandboxImage != "alpine" {
		t.Errorf("image = %q, want alpine", m.sandboxImage)
	}
	m.handleSandboxFieldKey(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if m.sandboxImage != "alpin" {
		t.Errorf("after backspace image = %q, want alpin", m.sandboxImage)
	}
}

// Tab and Enter must always reach the dialog, or the row traps the cursor.
func TestHandleSandboxFieldKey_DoesNotSwallowNavigation(t *testing.T) {
	var m Model
	m.sandboxOn = true
	for _, k := range []tea.KeyPressMsg{
		{Code: tea.KeyTab},
		{Code: tea.KeyEnter},
		{Code: tea.KeyEsc},
	} {
		if m.handleSandboxFieldKey(k) {
			t.Errorf("%v was consumed by the sandbox row", k)
		}
	}
}

func TestHandleSandboxFieldKey_BoundsTheImage(t *testing.T) {
	var m Model
	m.sandboxOn = true
	m.sandboxImage = strings.Repeat("x", sandboxImageMax)
	m.handleSandboxFieldKey(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if len(m.sandboxImage) != sandboxImageMax {
		t.Errorf("image grew past the cap: %d", len(m.sandboxImage))
	}
}

// There is no image Quil can honestly pick: it publishes none. Defaulting one
// would run the user's agent in a container they never chose.
func TestSandboxSubmitError_RefusesAnEmptyImage(t *testing.T) {
	var m Model
	if m.sandboxSubmitError() != "" {
		t.Error("an off sandbox reported an error")
	}
	m.sandboxOn = true
	if m.sandboxSubmitError() == "" {
		t.Error("an empty image was accepted")
	}
	m.sandboxImage = "   "
	if m.sandboxSubmitError() == "" {
		t.Error("a whitespace-only image was accepted")
	}
	m.sandboxImage = "alpine"
	if got := m.sandboxSubmitError(); got != "" {
		t.Errorf("a valid image was refused: %s", got)
	}
}

// nil keeps every other create byte-identical on the wire and takes no new
// branch anywhere in the daemon.
func TestSandboxSpec_NilUnlessEnabled(t *testing.T) {
	var m Model
	if m.sandboxSpec() != nil {
		t.Error("a spec was produced with the row off")
	}
	m.sandboxOn = true
	if m.sandboxSpec() != nil {
		t.Error("a spec was produced with no image")
	}
	m.sandboxImage = "  alpine:3  "
	spec := m.sandboxSpec()
	if spec == nil || spec.Image != "alpine:3" {
		t.Errorf("spec = %+v, want a trimmed alpine:3", spec)
	}
}

// Three dialog exits skip the teardown, so a flag surviving one would put the
// NEXT plain create in a container the user did not ask for.
func TestResetSandboxField_ClearsTheSwitch(t *testing.T) {
	var m Model
	m.sandboxOn = true
	m.sandboxImage = "left-over"
	m.resetSandboxField("")

	if m.sandboxOn {
		t.Error("the switch survived a reset")
	}
	if m.sandboxSpec() != nil {
		t.Error("a stale spec survived a reset")
	}
}

// --- the on switch ---
//
// Everything above tests the row's LOGIC against a Model whose answer was
// filed by hand. None of it reaches the wiring, and the wiring is where this
// feature broke in production: requestSandboxCap had a caller, so a
// "dead knob" sweep found nothing, but the caller was attachToDest — the
// POST-RECONNECT reattach. A freshly started TUI never runs it.
//
// The consequence was silent and total. resetSandboxField PINS the answer when
// the dialog opens, and the dialog's own request lands after the pin, so with
// nothing asked at attach the first Ctrl+N pinned "unavailable" and hid the row
// on a machine with a perfectly healthy engine. Only the second open worked.

// sandboxCapReqs counts the capability requests that reached the wire.
func sandboxCapReqs(sent []*ipc.Message) int {
	n := 0
	for _, msg := range sent {
		if msg.Type == ipc.MsgSandboxCapReq {
			n++
		}
	}
	return n
}

// The FIRST attach must ask. This is the path a normal launch takes.
func TestAttachAllDests_AsksForTheSandboxCapability(t *testing.T) {
	t.Parallel()
	conn := newFakeConn()
	m := &Model{cfg: config.Default(), client: conn, projects: oneProject(NewTabModel("tab-1", "one"))}

	runCmd(m.attachAllDests())

	if n := sandboxCapReqs(conn.sent); n != 1 {
		t.Errorf("the first attach sent %d sandbox_cap_req, want 1 — without it the "+
			"first Ctrl+N pins an empty answer and hides the row on a working engine", n)
	}
}

// And the reconnect attach must keep asking. A daemon that restarted may have
// gained or lost its engine while the link was down, and attachAllDests never
// runs for that flow because finishReconnect sets m.attached[dest].
func TestAttachToDest_AsksForTheSandboxCapability(t *testing.T) {
	t.Parallel()
	conn := newFakeConn()
	m := &Model{cfg: config.Default(), client: conn, projects: oneProject(NewTabModel("tab-1", "one"))}

	runCmd(m.attachToDest(""))

	if n := sandboxCapReqs(conn.sent); n != 1 {
		t.Errorf("the reconnect attach sent %d sandbox_cap_req, want 1", n)
	}
}

// attachAllDests reruns on every WindowSizeMsg. Asking again for a destination
// already attached would put one probe per resize on the daemon's queue.
func TestAttachAllDests_DoesNotReaskForAnAttachedDest(t *testing.T) {
	t.Parallel()
	conn := newFakeConn()
	m := &Model{
		cfg:      config.Default(),
		client:   conn,
		projects: oneProject(NewTabModel("tab-1", "one")),
		attached: map[string]bool{"": true},
	}

	runCmd(m.attachAllDests())

	if n := sandboxCapReqs(conn.sent); n != 0 {
		t.Errorf("an already-attached destination was probed %d times on a resize round", n)
	}
}

// answerSandboxCapReqs plays the daemon: it answers the requests that actually
// REACHED IT and nothing else.
//
// Calling applySandboxCap unconditionally instead would make the assertion
// below hold whether or not anything was ever asked — the tautology this
// package has been bitten by before. Driving the answer off the send log is
// what ties the row back to the wiring.
// The reply is filed under the key the ROUTER uses for that connection, which
// is what Router.pump stamps on receive — so the sent stamp goes back through
// routeDest. Skipping that step files the local daemon's answer under the
// "\x00local" sentinel stampDest wrote, where no reader looks.
func answerSandboxCapReqs(m *Model, sent []*ipc.Message) {
	for _, msg := range sent {
		if msg.Type != ipc.MsgSandboxCapReq {
			continue
		}
		dest, _ := routeDest(msg.Origin)
		m.applySandboxCap(dest, ipc.SandboxCapRespPayload{Available: true})
	}
}

// The end-to-end shape of the production bug, driven through the two real entry
// points in order: attach, the daemon's reply, then the dialog's pin. Asserting
// on the ROW rather than on the request keeps this failing for the original
// defect even if a future refactor moves where the asking happens.
func TestFirstDialogOpenAfterAttach_OffersTheRow(t *testing.T) {
	t.Parallel()
	conn := newFakeConn()
	m := &Model{cfg: config.Default(), client: conn, projects: oneProject(NewTabModel("tab-1", "one"))}

	runCmd(m.attachAllDests())
	answerSandboxCapReqs(m, conn.sent)
	// Now the user presses Ctrl+N for the FIRST time. resetSandboxField PINS
	// the answer, so anything asked from here on is for the NEXT open.
	m.resetSandboxField("")

	if !m.showSandboxField(promptsCWDPlugin()) {
		t.Error("the first Ctrl+N after attach did not offer the sandbox row on a " +
			"daemon with a working engine")
	}
}

// A daemon that reported an unusable engine must SAY so. The row simply
// vanishing is indistinguishable from the feature not existing — which is
// exactly how the attach-wiring defect above presented to a user.
func TestSetupDialog_ExplainsWhyTheSandboxRowIsAbsent(t *testing.T) {
	t.Parallel()
	var m Model
	m.pluginRegistry = plugin.NewRegistry()
	m.width, m.height = 120, 40
	m.applySandboxCap("", ipc.SandboxCapRespPayload{Error: "docker: engine not reachable"})
	m.resetSandboxField("")

	if m.showSandboxField(promptsCWDPlugin()) {
		t.Fatal("precondition: the row must be hidden for this test to mean anything")
	}
	line := m.renderSetupSandboxUnavailable()
	if !strings.Contains(line, "engine not reachable") {
		t.Errorf("the dialog drew %q; the daemon's reason never reached the user", line)
	}
}

// A destination that has not answered yet gets NO line. Claiming Docker is
// unavailable before anything looked at it is a confidently wrong answer, and
// with the attach-time request in place this window is very short.
func TestSetupDialog_SaysNothingBeforeTheDaemonHasAnswered(t *testing.T) {
	t.Parallel()
	var m Model
	m.width, m.height = 120, 40
	m.resetSandboxField("")

	if line := m.renderSetupSandboxUnavailable(); line != "" {
		t.Errorf("drew %q for a destination that has not answered", line)
	}
}

// The explanation must not be a FIELD. If it were counted, the cursor would
// land on a row setupFieldKind knows nothing about.
func TestSetupFieldCount_UnavailableExplanationIsNotAField(t *testing.T) {
	t.Parallel()
	p := promptsCWDPlugin()

	var silent Model
	silent.resetSandboxField("")

	var explained Model
	explained.applySandboxCap("", ipc.SandboxCapRespPayload{Error: "engine stopped"})
	explained.resetSandboxField("")

	if a, b := silent.setupFieldCount(p), explained.setupFieldCount(p); a != b {
		t.Errorf("field count %d with no reason vs %d with one — the explanation "+
			"is being counted as a focusable row", a, b)
	}
}

// --- the refusal has to be VISIBLE ---
//
// The submit check wrote its message into worktreeErr, which the dialog paints
// only while the worktree NAME EDITOR is open. On a dialog with a settled
// worktree — the ordinary case — Continue therefore stored a refusal and drew
// nothing: the key read as dead, with no way to discover what was missing.
//
// These drive the REAL key handler and assert on the RENDERED frame, because
// every part of that defect was downstream of the logic. sandboxSubmitError
// was correct the whole time.

// sandboxKeyModel is a Model sitting in the setup dialog with the cursor on the
// sandbox row and the switch on. Built from a real registry so
// pluginRegistry.Get returns a plugin and the field walk is the real one.
func sandboxKeyModel(t *testing.T) Model {
	t.Helper()
	dir := t.TempDir()
	toml := "[plugin]\nname = \"claude-code\"\ndisplay_name = \"Claude Code\"\ncategory = \"ai\"\n\n" +
		"[command]\ncmd = \"true\"\nprompts_cwd = true\n"
	if err := os.WriteFile(filepath.Join(dir, "claude-code.toml"), []byte(toml), 0o644); err != nil {
		t.Fatalf("write toml: %v", err)
	}
	r := plugin.NewRegistry()
	if err := r.LoadFromDir(dir); err != nil {
		t.Fatalf("LoadFromDir: %v", err)
	}

	m := Model{
		pluginRegistry: r,
		selectedPlugin: "claude-code",
		dialog:         dialogCreatePaneSetup,
		cwdBrowseDir:   "/repo",
	}
	m.width, m.height = 120, 40
	m.applySandboxCap("", ipc.SandboxCapRespPayload{Available: true})
	m.resetSandboxField("")
	m.sandboxOn = true
	m.setupFieldCursor = m.setupSandboxFieldIndex(r.Get("claude-code"))
	return m
}

// Continue with no image must SAY so, on screen. Asserting on the frame rather
// than on the error field is the whole point: the field was always set.
func TestSetupDialog_EmptyImageRefusalIsDrawn(t *testing.T) {
	m := sandboxKeyModel(t)

	upd, _ := m.handleCreatePaneSetupKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	frame := upd.(Model).renderCreatePaneSetupDialog()

	if !strings.Contains(frame, "Enter a container image") {
		t.Errorf("Continue was refused but the frame never says why:\n%s", frame)
	}
}

// Typing reaches the field through the real dialog key path. The row is the
// only text input that is also a switch, so this is where a routing mistake
// would land.
func TestSetupDialog_TypingReachesTheImageField(t *testing.T) {
	m := sandboxKeyModel(t)

	for _, r := range "alpine:3" {
		upd, _ := m.handleCreatePaneSetupKey(tea.KeyPressMsg{Code: r, Text: string(r)})
		m = upd.(Model)
	}
	if m.sandboxImage != "alpine:3" {
		t.Errorf("image = %q after typing through the dialog, want alpine:3", m.sandboxImage)
	}
}

// A refusal describes the row as it was. Editing the row makes it stale, so it
// must go — otherwise the dialog shows a red error beside an image that is now
// perfectly valid.
func TestSetupDialog_TypingClearsTheRefusal(t *testing.T) {
	m := sandboxKeyModel(t)

	upd, _ := m.handleCreatePaneSetupKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = upd.(Model)
	if m.sandboxErr == "" {
		t.Fatal("precondition: Continue must have been refused")
	}

	upd, _ = m.handleCreatePaneSetupKey(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if got := upd.(Model).sandboxErr; got != "" {
		t.Errorf("the refusal survived a keystroke: %q", got)
	}
}

// Switching the row off resolves the refusal too — "or turn the sandbox off"
// is half of what the message offers.
func TestSetupDialog_TogglingOffClearsTheRefusal(t *testing.T) {
	m := sandboxKeyModel(t)

	upd, _ := m.handleCreatePaneSetupKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = upd.(Model)
	if m.sandboxErr == "" {
		t.Fatal("precondition: Continue must have been refused")
	}

	upd, _ = m.handleCreatePaneSetupKey(tea.KeyPressMsg{Code: ' ', Text: " "})
	got := upd.(Model)
	if got.sandboxErr != "" {
		t.Errorf("the refusal survived switching the row off: %q", got.sandboxErr)
	}
	if got.sandboxOn {
		t.Error("space did not switch the row off")
	}
}

// The refusal moves the cursor to the row that has to change. Continue can be
// pressed from any field, and an error painted on a row scrolled out of the
// user's attention is barely better than no error.
func TestSetupDialog_RefusalMovesTheCursorToTheRow(t *testing.T) {
	m := sandboxKeyModel(t)
	p := m.pluginRegistry.Get("claude-code")
	m.setupFieldCursor = 0 // the CWD field

	upd, _ := m.handleCreatePaneSetupKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := upd.(Model)
	if k, _ := got.setupFieldKind(p, got.setupFieldCursor); k != "sandbox" {
		t.Errorf("cursor landed on %q after the refusal, want the sandbox row", k)
	}
}

// --- the sign-in choice ---

// The row exists only while the switch is on: it describes how THAT container
// authenticates, and a choice with no container is meaningless.
func TestShowSandboxAuthField_FollowsTheSwitch(t *testing.T) {
	t.Parallel()
	var m Model
	m.applySandboxCap("", ipc.SandboxCapRespPayload{Available: true})
	m.resetSandboxField("")
	p := promptsCWDPlugin()

	if m.showSandboxAuthField(p) {
		t.Error("the sign-in row is offered with the sandbox switched off")
	}
	m.sandboxOn = true
	if !m.showSandboxAuthField(p) {
		t.Error("the sign-in row is absent with the sandbox switched on")
	}
}

// setupFieldCount, setupFieldKind and the renderer's own walk must agree, or
// the cursor lands on a row nothing draws.
func TestSetupFieldKind_SandboxAuthSitsUnderTheSwitch(t *testing.T) {
	t.Parallel()
	var m Model
	m.applySandboxCap("", ipc.SandboxCapRespPayload{Available: true})
	m.resetSandboxField("")
	m.sandboxOn = true

	p := promptsCWDPlugin()
	p.Command.Sessions = "claude"

	var kinds []string
	for i := 0; i < m.setupFieldCount(p); i++ {
		k, _ := m.setupFieldKind(p, i)
		kinds = append(kinds, k)
	}
	// The session picker is hidden while the sandbox is on (a fresh container
	// has no prior sessions), which is why it is absent here.
	want := []string{"cwd", "worktree", "sandbox", "sandboxauth", "continue"}
	if strings.Join(kinds, ",") != strings.Join(want, ",") {
		t.Errorf("field order = %v, want %v", kinds, want)
	}
}

// An untouched row follows [sandbox] auth, so merely opening the dialog cannot
// override a configured default.
func TestEffectiveSandboxAuth_FollowsTheConfigUntilPicked(t *testing.T) {
	t.Parallel()
	var m Model
	m.cfg = config.Default()
	m.cfg.Sandbox.Auth = "browser"

	if got := m.effectiveSandboxAuth(); got != "browser" {
		t.Errorf("untouched row shows %q, want the configured browser", got)
	}
	m.sandboxAuth = "token"
	if got := m.effectiveSandboxAuth(); got != "token" {
		t.Errorf("after picking, row shows %q, want token", got)
	}
}

// Arrows and space move between the two modes; typing must not.
func TestHandleSandboxAuthFieldKey(t *testing.T) {
	t.Parallel()
	var m Model
	m.cfg = config.Default() // browser by default

	if got := m.effectiveSandboxAuth(); got != "browser" {
		t.Fatalf("the untouched row shows %q, want the browser default — a row that "+
			"opens on the token would offer it to someone who never asked", got)
	}
	if !m.handleSandboxAuthFieldKey(tea.KeyPressMsg{Code: tea.KeyRight}) {
		t.Fatal("right was not consumed")
	}
	if m.effectiveSandboxAuth() != "token" {
		t.Errorf("right gave %q, want token", m.effectiveSandboxAuth())
	}
	m.handleSandboxAuthFieldKey(tea.KeyPressMsg{Code: ' ', Text: " "})
	if m.effectiveSandboxAuth() != "browser" {
		t.Errorf("space did not cycle back, got %q", m.effectiveSandboxAuth())
	}

	// Navigation and submit must always reach the dialog.
	for _, k := range []tea.KeyPressMsg{{Code: tea.KeyTab}, {Code: tea.KeyEnter}, {Code: tea.KeyEsc}} {
		if m.handleSandboxAuthFieldKey(k) {
			t.Errorf("%v was consumed by the sign-in row", k)
		}
	}
	// A value is not typed here.
	if m.handleSandboxAuthFieldKey(tea.KeyPressMsg{Code: 'x', Text: "x"}) {
		t.Error("a printable key was consumed by a two-way choice")
	}
}

// The pane carries the chosen mode, or the daemon cannot honour it.
func TestSandboxSpec_CarriesTheChosenAuth(t *testing.T) {
	t.Parallel()
	var m Model
	m.sandboxOn = true
	m.sandboxImage = "img"

	if spec := m.sandboxSpec(); spec == nil || spec.Auth != "" {
		t.Errorf("untouched spec Auth = %+v, want empty so the daemon follows its config", spec)
	}
	m.sandboxAuth = "browser"
	if spec := m.sandboxSpec(); spec == nil || spec.Auth != "browser" {
		t.Errorf("spec = %+v, want the chosen browser mode", spec)
	}
}

// A mode picked for the LAST pane must not govern the next, which may be
// opened for the opposite reason.
func TestResetSandboxField_ClearsTheAuthChoice(t *testing.T) {
	t.Parallel()
	var m Model
	m.sandboxAuth = "browser"
	m.resetSandboxField("")
	if m.sandboxAuth != "" {
		t.Errorf("the auth choice survived a reset: %q", m.sandboxAuth)
	}
}

// The sign-in choice is Claude Code's. Codex keeps its own ~/.codex
// credentials and opencode its own again, so offering them a
// token-or-browser radio describes an agent the user did not pick — and the
// daemon ignores it for them anyway, which is the worse half: a control that
// changes nothing.
func TestShowSandboxAuthField_OnlyForClaudeCode(t *testing.T) {
	t.Parallel()
	var m Model
	m.applySandboxCap("", ipc.SandboxCapRespPayload{Available: true})
	m.resetSandboxField("")
	m.sandboxOn = true

	for _, name := range []string{"codex", "opencode"} {
		p := &plugin.PanePlugin{Name: name, Command: plugin.CommandConfig{PromptsCWD: true}}
		if m.showSandboxAuthField(p) {
			t.Errorf("the Claude sign-in choice is offered for %s", name)
		}
		// The sandbox switch itself must STAY: codex and opencode are
		// supported in a container, only their credentials are their own.
		if !m.showSandboxField(p) {
			t.Errorf("the sandbox row disappeared for %s; the container is still supported", name)
		}
	}
	if !m.showSandboxAuthField(promptsCWDPlugin()) {
		t.Error("the choice vanished for claude-code")
	}
}

// The RENDERER's own walk, asserted on the drawn frame.
//
// This is the third enumeration, and until now nothing exercised it: the
// fixture built a plugin named "probe" while showSandboxAuthField requires
// claude-code, so the sign-in row was absent from every frame any test had
// ever produced. Three separate mutations of the renderer survived the whole
// package — the guard forced false, the row rendering "", and the fieldIdx++
// dropped.
//
// The consequence of drift is not cosmetic: Tab lands the cursor on a row
// nobody draws, ←/→ silently change the auth mode, and [Continue] paints as
// focused while setupFieldKind reports "sandboxauth".
func TestRenderSetupDialog_DrawsTheSignInRow(t *testing.T) {
	m := sandboxKeyModel(t)
	frame := m.renderCreatePaneSetupDialog()

	for _, want := range []string{"Sign in", "Token", "Browser"} {
		if !strings.Contains(frame, want) {
			t.Errorf("the frame does not contain %q — the sign-in row is not drawn:\n%s", want, frame)
		}
	}
	// The selected mode must be marked, or the row shows two identical options.
	if !strings.Contains(frame, "(•)") {
		t.Errorf("no mode is marked as selected:\n%s", frame)
	}
}

// Every focusable row must be REACHABLE by the cursor and drawn when focused.
//
// Walks the real field list and renders at each index, which is what catches a
// renderer that advances fieldIdx differently from setupFieldKind: the caret
// would land on the wrong row, or on none.
func TestRenderSetupDialog_EveryFieldIndexDrawsItsCaret(t *testing.T) {
	m := sandboxKeyModel(t)
	p := m.pluginRegistry.Get("claude-code")

	for i := 0; i < m.setupFieldCount(p); i++ {
		m.setupFieldCursor = i
		kind, _ := m.setupFieldKind(p, i)
		frame := m.renderCreatePaneSetupDialog()
		if !strings.Contains(frame, "> ") {
			t.Errorf("cursor index %d (%s) draws no caret — the renderer's walk disagrees "+
				"with setupFieldKind:\n%s", i, kind, frame)
		}
	}
}
