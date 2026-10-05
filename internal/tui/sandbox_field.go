package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/plugin"
)

// The create dialog's sandbox row.
//
// One row that is both a switch and a text field: space toggles the sandbox on
// and off, and while it is on, typing edits the image. Two rows would be the
// obvious alternative and would cost a row of dialog height for a field that is
// meaningless when the switch is off — and this dialog is already one row from
// the bottom of a 24-row terminal.

// sandboxImageMax bounds what the field will hold. Docker's own reference limit
// is 255; anything longer is a paste accident.
const sandboxImageMax = 255

// renderSetupSandboxField draws the row.
func (m Model) renderSetupSandboxField(focused bool) string {
	box := "[ ]"
	if m.sandboxOn {
		box = "[x]"
	}
	prefix := "  "
	style := dialogNormal
	if focused {
		prefix = "> "
		style = dialogSelected
	}

	var b strings.Builder
	b.WriteString(prefix + style.Render(box+" Run in a Docker container") + "\n")

	if !m.sandboxOn {
		if focused {
			b.WriteString("    " + dialogSubtle.Render("space to enable — the agent is confined to this checkout"))
		}
		return b.String()
	}

	// The image is the ONE thing the user supplies, and there is no default:
	// Quil publishes no image, so a blank field has to say so rather than
	// silently substituting something.
	val := m.sandboxImage
	if val == "" {
		val = dialogSubtle.Render("(image required)")
	} else {
		val = truncateToWidth(sanitizeRemoteText(val), m.setupTextWidth()-10)
	}
	b.WriteString("    " + dialogValStyle.Render("image: ") + val)
	// The error outranks the hint, and replaces it: the hint only repeats keys
	// the footer already carries, while this says why Continue did nothing.
	// Drawn whether or not the row is focused — the refusal moves the cursor
	// here, but a later Tab away must not silently drop the explanation.
	if m.sandboxErr != "" {
		b.WriteString("\n    " + dialogErrorStyle.Render(
			truncateToWidth(m.sandboxErr, m.setupTextWidth()-setupRowIndent)))
	} else if focused {
		b.WriteString("\n    " + dialogSubtle.Render("type to edit — space toggles off"))
	}
	return b.String()
}

// setupSandboxFieldIndex is the cursor index of the sandbox row, or the
// unchanged cursor when the row is not shown.
//
// Derived from setupFieldKind rather than recomputed, so it cannot drift from
// the walk the renderer and the key handler use.
func (m Model) setupSandboxFieldIndex(p *plugin.PanePlugin) int {
	for i := 0; i < m.setupFieldCount(p); i++ {
		if k, _ := m.setupFieldKind(p, i); k == "sandbox" {
			return i
		}
	}
	return m.setupFieldCursor
}

// handleSandboxFieldKey edits the row. Reports whether it consumed the key.
//
// It consumes printable text ONLY while the sandbox is on, so space keeps its
// toggle meaning when off and Tab/Enter always reach the dialog.
// Any key the row CONSUMES changed the row, so the last refusal no longer
// describes it. Clearing here rather than in each branch of editSandboxRow
// means a branch added later cannot forget to — the wrapper is the only way in.
func (m *Model) handleSandboxFieldKey(msg tea.KeyPressMsg) bool {
	if !m.editSandboxRow(msg) {
		return false
	}
	m.sandboxErr = ""
	return true
}

func (m *Model) editSandboxRow(msg tea.KeyPressMsg) bool {
	switch msg.String() {
	// Both spellings: a real keypress arrives as "space" while a pasted or
	// synthesised one can arrive as " ". The toggle-row precedent in
	// dialog.go matches the same pair.
	case " ", "space":
		m.sandboxOn = !m.sandboxOn
		if m.sandboxOn && m.sandboxImage == "" {
			m.sandboxImage = m.sandboxImageDefault(m.createPaneDialogDest())
		}
		return true
	case "backspace":
		if m.sandboxOn && m.sandboxImage != "" {
			r := []rune(m.sandboxImage)
			m.sandboxImage = string(r[:len(r)-1])
		}
		return m.sandboxOn
	}
	if !m.sandboxOn {
		return false
	}
	// isPrintableText is the same gate the command palette uses, so a control
	// character or a bracketed-paste marker cannot reach the field.
	if t := msg.Text; t != "" && isPrintableText(t) {
		if len([]rune(m.sandboxImage))+len([]rune(t)) <= sandboxImageMax {
			m.sandboxImage += t
		}
		return true
	}
	return false
}

// sandboxSubmitError explains why the dialog cannot submit, or "" when it can.
//
// An empty image is refused rather than defaulted. There is no image Quil can
// honestly pick — it publishes none, and guessing one would run the user's
// agent in a container they never chose.
func (m Model) sandboxSubmitError() string {
	if !m.sandboxOn {
		return ""
	}
	if strings.TrimSpace(m.sandboxImage) == "" {
		return "Enter a container image, or turn the sandbox off"
	}
	return ""
}

// sandboxSpec is what the create payload carries, or nil.
//
// Nil unless the user turned the row on. That keeps every other create
// byte-identical on the wire and takes no new branch anywhere in the daemon —
// the same property the worktree spec has, and for the same reason.
//
// The sign-in fields always carry the choice the row DISPLAYS, untouched or
// not. An empty Auth used to mean "follow the daemon's config", but for a
// remote project that is another machine's config, so the row could show one
// mode while the pane got the other. The daemon gates every Claude-auth use on
// the plugin, so a codex or opencode pane carrying these fields is unaffected.
func (m Model) sandboxSpec() *ipc.SandboxSpec {
	if !m.sandboxOn {
		return nil
	}
	image := strings.TrimSpace(m.sandboxImage)
	if image == "" {
		// Unreachable through the dialog: submitSetupDialog refuses first.
		// Returning nil rather than an empty spec means a future caller that
		// skips that check creates an ordinary pane instead of asking the
		// daemon to run `docker run ""`.
		return nil
	}
	auth, claudeConfig := sandboxSignInFields(m.effectiveSandboxSignIn())
	return &ipc.SandboxSpec{Image: image, Auth: auth, ClaudeConfig: claudeConfig}
}

// resetSandboxField clears the row's state.
//
// Called when the create dialog OPENS, never on a close path. Three exits skip
// the teardown — the step-0 Esc, the instance-delete detour, and the split
// step's early refusals — and a sandbox flag surviving one of them would put
// the next plain create in a container the user did not ask for. The dialog's
// other fields learned this the same way.
func (m *Model) resetSandboxField(dest string) {
	m.sandboxOn = false
	m.sandboxImage = m.sandboxImageDefault(dest)
	// Cleared with the rest: a choice made for the LAST pane must not silently
	// govern the next one, which may be opened for the opposite reason.
	m.sandboxSignIn = ""
	m.sandboxErr = ""
	// Pin the capability answer for the life of the dialog. It is fetched
	// asynchronously and re-probed on a timer, so reading it live would add
	// or remove a row under a cursor the key handler is already holding an
	// index into.
	m.sandboxDialogAvail = m.sandboxAvailableFor(dest)
	m.sandboxDialogReason = m.sandboxUnavailableReason(dest)
}

// renderSetupSandboxUnavailable draws the one-line explanation that replaces
// the row when the daemon reported an unusable engine.
//
// Not a focusable field: it takes no cursor index, so setupFieldCount and
// setupFieldKind are untouched and cannot drift from the renderer's own walk.
//
// It draws NOTHING when the reason is empty, which is the destination that has
// not answered yet. "Docker unavailable" would be a claim about an engine
// nobody has looked at, and the answer normally lands at attach — long before
// any dialog opens.
func (m Model) renderSetupSandboxUnavailable() string {
	if m.sandboxDialogReason == "" {
		return ""
	}
	// Truncated as ONE string against the indent the row is drawn at, rather
	// than budgeting the reason separately: the prefix is fixed text, so
	// subtracting a hand-counted constant for it is a second place to get the
	// arithmetic wrong. A docker error can be several lines long, and this
	// dialog is already one row from the bottom of a 24-row terminal.
	const indent = "    "
	line := "Docker sandbox unavailable: " + m.sandboxDialogReason
	return indent + dialogSubtle.Render(truncateToWidth(line, m.setupTextWidth()-len(indent)))
}

// --- the sign-in row ---
//
// Its own focusable field rather than more keys on the switch row, because it
// is a THIRD kind of control: the switch takes space, the image takes typing,
// and a choice needs neither of those meanings. Shown only while the sandbox is
// ON, since it describes how that container authenticates.

// sandboxAuthChoices are the choices the row offers, in display order. A
// choice is NOT an auth mode: sandboxSignInFields maps it onto the two wire
// fields, so "shared" can never reach SandboxSpec.Auth, where the daemon would
// drop it as unknown.
//
// Browser leads because it is the default and the only one of the three that
// changes nothing outside the pane. Shared's detail names the trust domain it
// joins: every Shared pane mounts one config directory, so any of them can
// plant a hook or MCP server the others run. Token's detail names the reach
// rather than only the loss: the sign-in it skips saves a credential into the
// user's environment, which every process started afterwards inherits — so
// the cost lands on ORDINARY panes as well as this one, and the row is the
// last place to say so before it does.
var sandboxAuthChoices = []struct{ choice, label, detail string }{
	{"browser", "Browser", "sign in in this container · full subscription"},
	// Cost first and short: the F1 page has ~50 cells for this line, and a
	// warning cut to "they share ho…" states no cost at all.
	{"shared", "Shared", "shares hooks, MCP servers, history · sign in once"},
	{"token", "Token", "no sign-in · saves a token every later Claude uses"},
}

// sandboxSignInFields maps a row choice onto (Auth, ClaudeConfig) for the
// wire. Token sends "own": a token pane opened from the dialog never enters
// the shared directory, so that directory only ever serves browser panes and
// its sign-in mode stamp cannot flip back and forth between the two.
func sandboxSignInFields(choice string) (auth, claudeConfig string) {
	switch choice {
	case "shared":
		return string(config.SandboxAuthBrowser), config.SandboxClaudeConfigShared
	case "token":
		return string(config.SandboxAuthToken), config.SandboxClaudeConfigOwn
	}
	return string(config.SandboxAuthBrowser), config.SandboxClaudeConfigOwn
}

// defaultSandboxSignIn is the choice a config selects (config.DefaultSignIn).
// The F1 → Settings → Sandbox page and the web dialog read the same method.
func defaultSandboxSignIn(c config.SandboxConfig) string { return c.DefaultSignIn() }

// showSandboxAuthField reports whether the setup dialog offers the sign-in row.
//
// Gated on the switch being ON, so the field list changes shape with it — which
// is safe here and nowhere else in this dialog: the switch and this row are
// adjacent, and toggling the switch is what moves the cursor onto or off it.
func (m Model) showSandboxAuthField(p *plugin.PanePlugin) bool {
	// Claude Code only: the choices it offers are a CLAUDE_CODE_OAUTH_TOKEN
	// and a Claude browser sign-in, own or shared. Codex keeps its own
	// ~/.codex credentials and opencode its own again, so the choice would
	// describe an agent the user did not pick — and the daemon ignores it for
	// them anyway.
	return m.showSandboxField(p) && m.sandboxOn && p.UsesClaudeAuth()
}

// effectiveSandboxSignIn is the choice the row DISPLAYS: the user's pick, or
// the configured default while they have not picked. sandboxSpec sends exactly
// this, so the row never shows a choice the pane would not actually get.
func (m Model) effectiveSandboxSignIn() string {
	if m.sandboxSignIn != "" {
		return m.sandboxSignIn
	}
	return defaultSandboxSignIn(m.cfg.Sandbox)
}

// renderSetupSandboxAuthField draws the choice.
func (m Model) renderSetupSandboxAuthField(focused bool) string {
	prefix, style := "  ", dialogNormal
	if focused {
		prefix, style = "> ", dialogSelected
	}
	cur := m.effectiveSandboxSignIn()

	var b strings.Builder
	b.WriteString(prefix + style.Render("Sign in") + "  ")
	for i, c := range sandboxAuthChoices {
		if i > 0 {
			b.WriteString("  ")
		}
		mark := "( ) "
		if c.choice == cur {
			mark = "(•) "
		}
		b.WriteString(dialogValStyle.Render(mark + c.label))
	}
	if focused {
		// The detail of the SELECTED choice only: the trade is what the user
		// needs to see, and more lines of it would push Continue off a short
		// terminal — the constraint every field in this dialog obeys.
		// The key hint gives way to the detail when both do not fit: the
		// detail is the trade the user is choosing, the keys are in the footer.
		budget := m.setupTextWidth() - setupRowIndent
		for _, c := range sandboxAuthChoices {
			if c.choice == cur {
				line := c.detail + " — ←/→ or space to change"
				if lipgloss.Width(line) > budget {
					line = c.detail
				}
				b.WriteString("\n    " + dialogSubtle.Render(truncateToWidth(line, budget)))
			}
		}
	}
	return b.String()
}

// handleSandboxAuthFieldKey moves between the choices. Reports whether it
// consumed the key.
//
// Left/right and space, not typing: this is a choice, not a value. Tab and
// Enter are deliberately absent so navigation and submit always reach the
// dialog, the same rule the switch row follows.
func (m *Model) handleSandboxAuthFieldKey(msg tea.KeyPressMsg) bool {
	switch msg.String() {
	case "left", "h":
		m.stepSandboxAuth(-1)
		return true
	case "right", "l", " ", "space":
		m.stepSandboxAuth(1)
		return true
	}
	return false
}

// stepSandboxAuth cycles the selection, wrapping.
func (m *Model) stepSandboxAuth(delta int) {
	m.sandboxSignIn = stepSandboxChoice(m.effectiveSandboxSignIn(), delta)
	// Any edit of the sandbox rows invalidates the last refusal, exactly as
	// the switch row's edits do.
	m.sandboxErr = ""
}

// stepSandboxChoice is the choice delta steps away from cur, wrapping. Shared
// with the F1 → Settings → Sandbox page.
func stepSandboxChoice(cur string, delta int) string {
	idx := 0
	for i, c := range sandboxAuthChoices {
		if c.choice == cur {
			idx = i
			break
		}
	}
	n := len(sandboxAuthChoices)
	return sandboxAuthChoices[((idx+delta)%n+n)%n].choice
}
