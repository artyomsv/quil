package tui

import (
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/plugin"
	"github.com/artyomsv/quil/internal/sbx"
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
const sandboxImageMax = config.SandboxImageMax

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

	label := " Run in a Docker container"
	if m.sandboxBackendIsSbx() {
		label = " Run in an sbx sandbox"
	}
	var b strings.Builder
	b.WriteString(prefix + style.Render(box+label))
	// The next choice is named only when there is one, so a host with docker
	// alone draws exactly the row it always did.
	if next, ok := m.nextSandboxChoice(1); m.sandboxOn && ok {
		b.WriteString(dialogSubtle.Render("  ←/→ " + truncateToWidth(next.label(), 24)))
	}
	b.WriteString("\n")

	if !m.sandboxOn {
		if focused {
			b.WriteString("    " + dialogSubtle.Render("space to enable — the agent is confined to this checkout"))
		}
		return b.String()
	}

	if m.sandboxBackendIsSbx() {
		// No image: the cage is the project's .quil-sandbox.toml, which the
		// daemon finds from the pane's directory and refuses unless approved —
		// or an existing sandbox another tool owns, which Quil only enters.
		line := "cage: " + sbx.ProfileFile + " in this project (approve: quil sandbox trust)"
		if m.sandboxSbxName != "" {
			line = "attach: " + m.sandboxSbxName + m.sbxStatusSuffix(m.sandboxSbxName) +
				", not changed"
			if !m.sbxAttachFits(m.sandboxSbxName) {
				line = "attach: " + m.sandboxSbxName + m.sbxStatusSuffix(m.sandboxSbxName) +
					", folder checked at start"
			}
		}
		b.WriteString("    " + dialogSubtle.Render(truncateToWidth(line, m.setupTextWidth()-setupRowIndent)))
		if m.sandboxErr != "" {
			b.WriteString("\n    " + dialogErrorStyle.Render(
				truncateToWidth(m.sandboxErr, m.setupTextWidth()-setupRowIndent)))
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
	return m.setupFieldIndex(p, "sandbox")
}

// setupFieldIndex is the cursor index of the first row of kind, or the
// unchanged cursor when no such row is shown.
func (m Model) setupFieldIndex(p *plugin.PanePlugin, kind string) int {
	for i := 0; i < m.setupFieldCount(p); i++ {
		if k, _ := m.setupFieldKind(p, i); k == kind {
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
	case "left", "right":
		// Steps through the choices — Docker, the sbx profile, each existing
		// sandbox — and only when there is more than one: otherwise the
		// arrows are not the row's to take.
		step := 1
		if msg.String() == "left" {
			step = -1
		}
		next, ok := m.nextSandboxChoice(step)
		if !m.sandboxOn || !ok {
			return false
		}
		m.sandboxBackend, m.sandboxSbxName = next.backend, next.name
		return true
	case "backspace":
		if m.sandboxOn && !m.sandboxBackendIsSbx() && m.sandboxImage != "" {
			r := []rune(m.sandboxImage)
			m.sandboxImage = string(r[:len(r)-1])
		}
		return m.sandboxOn
	}
	// An sbx row has no text field: typing is not the row's to take.
	if !m.sandboxOn || m.sandboxBackendIsSbx() {
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
	if m.sandboxBackendIsSbx() {
		// The sandbox can be removed while the dialog is open. Whether it
		// holds the folder is the daemon's to check at spawn: `sbx ls` does
		// not list every mount.
		if m.sandboxSbxName != "" && !m.sbxAttachOffered(m.sandboxSbxName) {
			return "Sandbox " + m.sandboxSbxName + " is not listed by sbx any more; pick another with ←/→"
		}
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
	if m.sandboxBackendIsSbx() {
		// Backend and, attaching, the sandbox name. The daemon refuses an sbx
		// spec carrying an image or a sign-in mode: everything about that
		// cage is the approved profile's, or the existing sandbox's.
		return &ipc.SandboxSpec{Backend: config.SandboxBackendSbx, Name: m.sandboxSbxName}
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
	// The profile, never a remembered sandbox: an existing sandbox is picked
	// for one pane, on purpose, each time.
	m.sandboxSbxName = ""
	m.sbxOrderDir, m.sbxOrderPinned = "", false
	// Pin the capability answer for the life of the dialog. It is fetched
	// asynchronously and re-probed on a timer, so reading it live would add
	// or remove a row under a cursor the key handler is already holding an
	// index into.
	m.sandboxDialogDocker = m.sandboxAvailableFor(dest)
	m.sandboxDialogSbx = m.sbxAvailableFor(dest)
	m.sandboxDialogAvail = m.sandboxDialogDocker || m.sandboxDialogSbx
	m.sandboxDialogReason = m.sandboxUnavailableReason(dest)
	// The configured backend, unless this destination cannot run it and can
	// run the other one.
	m.sandboxBackend = m.cfg.Sandbox.ResolveBackend()
	switch {
	case m.sandboxBackend == config.SandboxBackendSbx && !m.sandboxDialogSbx && m.sandboxDialogDocker:
		m.sandboxBackend = config.SandboxBackendDocker
	case m.sandboxBackend == config.SandboxBackendDocker && !m.sandboxDialogDocker && m.sandboxDialogSbx:
		m.sandboxBackend = config.SandboxBackendSbx
	}
}

// sandboxBackendIsSbx reports whether the row currently targets sbx.
func (m Model) sandboxBackendIsSbx() bool {
	return m.sandboxBackend == config.SandboxBackendSbx
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
	return config.SandboxSignInFields(choice)
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
	// Not for sbx: how that agent signs in is the profile's business.
	return m.showSandboxField(p) && m.sandboxOn && !m.sandboxBackendIsSbx() && p.UsesClaudeAuth()
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

// sandboxChoice is one stop of the row's ←/→ cycle.
type sandboxChoice struct {
	backend string
	name    string // an existing sbx sandbox; "" for docker and the profile
}

func (c sandboxChoice) label() string {
	switch {
	case c.backend != config.SandboxBackendSbx:
		return "Docker"
	case c.name == "":
		return "sbx"
	default:
		return "sbx " + c.name
	}
}

// sandboxChoices is the cycle, in order: Docker, the sbx profile, then each
// existing sandbox that mounts the dialog's folder.
func (m Model) sandboxChoices() []sandboxChoice {
	var out []sandboxChoice
	if m.sandboxDialogDocker {
		out = append(out, sandboxChoice{backend: config.SandboxBackendDocker})
	}
	if m.sandboxDialogSbx {
		out = append(out, sandboxChoice{backend: config.SandboxBackendSbx})
		for _, s := range m.sbxAttachChoices() {
			out = append(out, sandboxChoice{backend: config.SandboxBackendSbx, name: s.Name})
		}
	}
	return out
}

// nextSandboxChoice is the choice step places away from the current one,
// wrapping, and false when there is nothing else to pick. A current choice
// that is no longer offered (the folder changed) steps to the first one —
// checked before the count, or a host with one choice left could never leave
// a sandbox that stopped mounting the folder.
func (m Model) nextSandboxChoice(step int) (sandboxChoice, bool) {
	choices := m.sandboxChoices()
	cur := -1
	for i, c := range choices {
		if c.backend == m.sandboxBackend && c.name == m.sandboxSbxName {
			cur = i
		}
	}
	if cur < 0 && len(choices) > 0 {
		return choices[0], true
	}
	if len(choices) < 2 {
		return sandboxChoice{}, false
	}
	return choices[(cur+step+len(choices))%len(choices)], true
}

// sbxAttachChoices are the existing sandboxes the daemon listed: first the
// ones whose listed folders hold the dialog's folder writable, then the
// rest. The rest stay offered because `sbx ls` does not list a folder added
// later with `sbx mount` — the daemon asks the sandbox itself at spawn.
// Read live rather than pinned: the list fills in when the dialog's own
// capability request is answered, and it changes no row count. Display
// only; the daemon checks the name and the folder again at spawn. A name
// sbx could read as a flag is never offered: it is text from the daemon's
// machine.
func (m Model) sbxAttachChoices() []ipc.SbxSandbox {
	dir := m.setupSpawnDir()
	if m.sbxOrderPinned {
		dir = m.sbxOrderDir
	}
	var fits, rest []ipc.SbxSandbox
	for _, s := range m.destSandbox[m.createPaneDialogDest()].SbxSandboxes {
		if !sbx.ValidName(s.Name) {
			continue
		}
		if sbxListsFolder(s, dir) {
			fits = append(fits, s)
		} else {
			rest = append(rest, s)
		}
	}
	return append(fits, rest...)
}

// sbxListsFolder reports whether the sandbox's listed folders hold dir
// writable — every sandbox while no folder is chosen. The most specific
// folder decides, as in sbx.Listed.Covers; at one path, read-only wins.
func sbxListsFolder(s ipc.SbxSandbox, dir string) bool {
	if dir == "" {
		return true
	}
	best, writable := -1, false
	for _, f := range s.Folders {
		if folderHolds(f, dir) && len(f) > best {
			best, writable = len(f), true
		}
	}
	for _, f := range s.ReadOnlyFolders {
		if folderHolds(f, dir) && len(f) >= best {
			best, writable = len(f), false
		}
	}
	return writable
}

// sbxAttachOffered reports whether name is still one of the attach choices.
func (m Model) sbxAttachOffered(name string) bool {
	return slices.ContainsFunc(m.sbxAttachChoices(), func(s ipc.SbxSandbox) bool { return s.Name == name })
}

// sbxAttachFits reports whether sbx lists the dialog's folder for name.
func (m Model) sbxAttachFits(name string) bool {
	dir := m.setupSpawnDir()
	return slices.ContainsFunc(m.destSandbox[m.createPaneDialogDest()].SbxSandboxes,
		func(s ipc.SbxSandbox) bool { return s.Name == name && sbxListsFolder(s, dir) })
}

// followSbxAttachFolder moves the dialog's folder to the picked sandbox's
// primary workspace when sbx lists none of its writable folders as holding
// the current one. Without it the user had to know to browse there first, and
// the miss surfaced only at spawn, as a refused pane. Called after ←/→ lands
// on a sandbox, never on submit: a folder added with `sbx mount` is not in the
// listing, so the user can browse back and the daemon's probe has the last
// word.
func (m *Model) followSbxAttachFolder() tea.Cmd {
	name := m.sandboxSbxName
	if !m.sandboxOn || name == "" {
		return nil
	}
	if dir := m.setupSpawnDir(); dir != "" && m.sbxAttachFits(name) {
		return nil
	}
	target := m.sbxPrimaryFolder(name)
	if target == "" {
		return nil
	}
	// The cycle keeps the order it had before the first move, or the next
	// arrow would step back to the sandbox just left.
	if !m.sbxOrderPinned {
		m.sbxOrderDir, m.sbxOrderPinned = m.setupSpawnDir(), true
	}
	// The pick lists name folders the sandbox does not hold; the browser
	// shows the one it does.
	m.repoCandidates, m.recentCandidates = nil, nil
	// And the scans that would fill them again are retired: a late answer to
	// either would otherwise commit its own folder over this one (or, timed
	// out, restart the browser elsewhere) while the sandbox stays picked.
	m.recentScan = recentScanState{}
	if m.repoScan.purpose == repoScanPickList {
		m.repoScan = repoScanState{}
	}
	m.onSetupCWDChanged(target)
	// The old listing belongs to the folder just left: Enter on one of its
	// rows would join that name onto the new path. Cleared, not kept, so a
	// browse that fails shows an empty listing rather than a wrong one.
	m.cwdBrowseEntries, m.cwdBrowseParent = nil, ""
	m.cwdBrowseCursor, m.cwdBrowseScroll = 0, 0
	return m.browseTo(target, "", "")
}

// sbxPrimaryFolder is the first writable folder sbx lists for name — the
// sandbox's primary workspace, which `sbx ls` puts first — or "".
func (m Model) sbxPrimaryFolder(name string) string {
	for _, s := range m.destSandbox[m.createPaneDialogDest()].SbxSandboxes {
		if s.Name != name {
			continue
		}
		for _, f := range s.Folders {
			if f != "" {
				return f
			}
		}
	}
	return ""
}

// sbxStatusSuffix is " (running)" and the like, for the attach line.
func (m Model) sbxStatusSuffix(name string) string {
	for _, s := range m.destSandbox[m.createPaneDialogDest()].SbxSandboxes {
		if s.Name == name && s.Status != "" {
			return " (" + sanitizeRemoteText(s.Status) + ")"
		}
	}
	return ""
}

// folderHolds reports whether dir is folder or below it, by text: both are
// the daemon's spellings, which may be another OS's, so neither is resolved
// here. Either separator ends a folder name.
func folderHolds(folder, dir string) bool {
	f := strings.TrimRight(folder, `/\`)
	if f == "" {
		return false
	}
	return dir == f || strings.HasPrefix(dir, f+"/") || strings.HasPrefix(dir, f+`\`)
}
