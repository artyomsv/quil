package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/plugin"
)

// textPane builds a pane of the given width with feed written through the
// emulator, the way PTY output arrives.
func textPane(t *testing.T, cols, rows int, feed string) *PaneModel {
	t.Helper()
	pane := NewPaneModel("p1", testRingBufSize)
	pane.ResizeVT(cols, rows)
	pane.AppendOutput([]byte(feed))
	return pane
}

// selectLines selects whole rows first..last of pane.
func selectLines(pane *PaneModel, first, last int) *Selection {
	sb := pane.vt.ScrollbackLen()
	return &Selection{
		PaneID: pane.ID,
		Anchor: SelectionAnchor{Col: 0, Line: sb + first},
		Cursor: SelectionAnchor{Col: pane.vt.Width() - 1, Line: sb + last},
	}
}

func TestWordBoundsAt(t *testing.T) {
	pane := textPane(t, 40, 3, "run ./cmd/quil/main.go (done). x")
	tests := []struct {
		name   string
		col    int
		want   string
		wantOK bool
	}{
		{"plain word", 1, "run", true},
		{"path stays whole", 10, "./cmd/quil/main.go", true},
		{"brackets end a word, trailing dot dropped", 25, "done", true},
		{"single char", 31, "x", true},
		{"space is not a word", 3, "", false},
		{"bracket is not a word", 23, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sb := pane.vt.ScrollbackLen()
			start, end, ok := wordBoundsAt(pane, sb, tt.col)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			sel := &Selection{Anchor: SelectionAnchor{Col: start, Line: sb}, Cursor: SelectionAnchor{Col: end, Line: sb}}
			if got := extractText(pane, sel); got != tt.want {
				t.Errorf("word = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractText_LineBreaks(t *testing.T) {
	tests := []struct {
		name string
		feed string
		want string
	}{
		{
			// An app (Claude Code) word-wrapped a paragraph and indented the
			// continuation: the copy is one line again.
			name: "word wrap rejoins with one space",
			feed: "  alpha beta gamma\r\n  deltaepsilon",
			want: "alpha beta gamma deltaepsilon",
		},
		{
			// The shared margin goes; the nested item keeps its extra indent.
			name: "common indent removed, relative indent kept",
			feed: "  list:\r\n    - one\r\n\r\n  end",
			want: "list:\n  - one\n\nend",
		},
		{
			// Each wide glyph fills two cells; the empty second cell is not
			// a space in the copy.
			name: "wide characters copy without gaps",
			feed: "abc你好def 👍 end",
			want: "abc你好def 👍 end",
		},
		{
			// A CJK word is two columns per rune: 你好世界 needs 8 columns.
			// 10 are left, which is enough for 4 narrow runes plus the
			// separator and slack (9) but not for 8 columns (13).
			name: "wide first word measured in columns",
			feed: "alpha beta\r\n你好世界",
			want: "alpha beta 你好世界",
		},
		{
			name: "terminal wrap at last column joins with no space",
			feed: "abcdefghijklmnopqrstuvwxy",
			want: "abcdefghijklmnopqrstuvwxy",
		},
		{
			// Codex wraps right up to the last column and indents the
			// continuation: one space at the join, not three.
			name: "full row then indented row is an app wrap",
			feed: "  alpha beta gammas.\r\n  delta", // row 1 is exactly 20 wide
			want: "alpha beta gammas. delta",
		},
		{
			// The terminal wrapping exactly at a space: that space is text.
			name: "full row then one-space row is a terminal wrap",
			feed: "abcdefghijklmnopqrst uvw",
			want: "abcdefghijklmnopqrst uvw",
		},
		{
			name: "short line keeps its newline",
			feed: "short\r\nnext",
			want: "short\nnext",
		},
		{
			name: "list item after a full line keeps its newline",
			feed: "alpha beta gamma d\r\n- item",
			want: "alpha beta gamma d\n- item",
		},
		{
			name: "numbered item after a full line keeps its newline",
			feed: "alpha beta gamma de\r\n2. item",
			want: "alpha beta gamma de\n2. item",
		},
		{
			name: "blank line keeps its newline",
			feed: "alpha beta gamma d\r\n\r\nnext",
			want: "alpha beta gamma d\n\nnext",
		},
		{
			// Enough room was left for the next word, so the app did not
			// wrap here — the break is real.
			name: "next word would have fit",
			feed: "alpha beta\r\ngo",
			want: "alpha beta\ngo",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pane := textPane(t, 20, 5, tt.feed)
			last := lastContentLine(pane) - pane.vt.ScrollbackLen()
			if got := extractText(pane, selectLines(pane, 0, last)); got != tt.want {
				t.Errorf("extractText = %q, want %q", got, tt.want)
			}
		})
	}
}

// A drag started on the first letter of an indented paragraph, not at the
// margin, must still drop the margin from every later line.
func TestExtractText_DedentWhenDragStartsPastTheMargin(t *testing.T) {
	tests := []struct {
		name     string
		feed     string
		startCol int
		agent    bool // pane runs an AI agent (copyText's agentReply)
		want     string
	}{
		{
			name:     "starts on the first letter",
			feed:     "  one\r\n\r\n  two\r\n    nested",
			startCol: 2,
			want:     "one\n\ntwo\n  nested",
		},
		{
			// The start column is deeper than the margin: only the margin
			// the later lines share comes off them.
			name:     "starts mid-line",
			feed:     "  one two\r\n  three",
			startCol: 6,
			want:     "two\nthree",
		},
		{
			// Codex: a reply opens with "• " and indents the rest by two.
			name:     "agent reply marker counts at its text",
			feed:     "• one\r\n\r\n  two\r\n  - three",
			agent:    true,
			startCol: 0,
			want:     "• one\n\ntwo\n- three",
		},
		{
			// The same glyph in an ordinary pane is a list bullet, and the
			// indent under it is nesting (review finding on #227).
			name:     "unicode bullet list keeps its nesting",
			feed:     "• parent\r\n  • child",
			startCol: 0,
			want:     "• parent\n  • child",
		},
		{
			// A markdown list marker is not a margin: the child's indent is
			// its nesting and must survive the copy (review finding on #227).
			name:     "nested list keeps its indent",
			feed:     "- parent\r\n  - child",
			startCol: 0,
			want:     "- parent\n  - child",
		},
		{
			// Unindented output: a mid-line start removes nothing.
			name:     "no margin",
			feed:     "$ echo hi\r\nhi",
			startCol: 7,
			want:     "hi\nhi",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pane := textPane(t, 20, 6, tt.feed)
			sel := selectLines(pane, 0, lastContentLine(pane)-pane.vt.ScrollbackLen())
			sel.Anchor.Col = tt.startCol
			if got := copyText(pane, sel, tt.agent); got != tt.want {
				t.Errorf("extractText = %q, want %q", got, tt.want)
			}
		})
	}
}

// pinClickClock makes double-click detection deterministic.
func pinClickClock(t *testing.T) {
	t.Helper()
	fixed := time.Unix(1_000_000, 0)
	prev := clickNow
	clickNow = func() time.Time { return fixed }
	t.Cleanup(func() { clickNow = prev })
}

// paneCellScreen returns the screen coordinates of content cell (col, row)
// in the single pane of m's active tab.
func paneCellScreen(m Model, col, row int) (int, int) {
	return m.projectSidebarWidth() + 1 + col, 1 + 1 + row
}

func clickAt(m Model, x, y int) Model {
	next, _ := m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	m = next.(Model)
	next, _ = m.Update(tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft})
	return next.(Model)
}

func TestUpdate_DoubleClick_SelectsWord(t *testing.T) {
	pinClickClock(t)
	m := pasteTestModel(&fakeSender{})
	pane := m.curTabs()[0].ActivePaneModel()
	pane.AppendOutput([]byte("echo hello-world done"))

	x, y := paneCellScreen(m, 8, 0) // inside "hello-world"
	m = clickAt(m, x, y)
	if m.selection != nil {
		t.Fatalf("single click selected %+v; want no selection", m.selection)
	}
	m = clickAt(m, x, y)
	if m.selection == nil {
		t.Fatal("double click left no selection")
	}
	if got := extractText(pane, m.selection); got != "hello-world" {
		t.Errorf("selected %q, want %q", got, "hello-world")
	}

	// A third click selects the sentence; a fourth starts over.
	m = clickAt(m, x, y)
	if got := extractText(pane, m.selection); got != "echo hello-world done" {
		t.Errorf("triple click selected %q, want the whole sentence", got)
	}
	m = clickAt(m, x, y)
	if m.selection != nil {
		t.Errorf("fourth click kept selection %+v", m.selection)
	}
}

func TestSentenceBounds(t *testing.T) {
	tests := []struct {
		name string
		text string
		at   string // a substring whose first rune is clicked
		want string
		ok   bool
	}{
		{"middle sentence", "One here. Two is here! Three?", "is", "Two is here!", true},
		{"first sentence", "One here. Two.", "One", "One here.", true},
		{"no terminator at end", "One. Two runs on", "runs", "Two runs on", true},
		{"decimals and versions do not split", "Pi is 3.14 in v1.75.0 builds. Next.", "in", "Pi is 3.14 in v1.75.0 builds.", true},
		{"closing quote stays with its sentence", `She said "Go." Then left.`, "said", `She said "Go."`, true},
		{"ellipsis ends a sentence", "Wait... Go on.", "Go", "Go on.", true},
		{"gap between sentences picks the next", "One.  Two.", " Two", "Two.", true},
		{"trailing space picks the last", "One. Two.   ", "   ", "Two.", true},
		{"leading marker is dropped", "• Reply text. More.", "Reply", "Reply text.", true},
		{"lone marker is kept", "-", "-", "-", true},
		{"blank", "    ", " ", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runes := []rune(tt.text)
			units := make([]string, len(runes))
			for i, r := range runes {
				units[i] = string(r)
			}
			idx := len([]rune(tt.text[:strings.Index(tt.text, tt.at)]))
			start, end, ok := sentenceBounds(units, idx)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if ok {
				if got := string(runes[start:end]); got != tt.want {
					t.Errorf("sentence = %q, want %q", got, tt.want)
				}
			}
		})
	}
}

// Triple-click follows a sentence across the rows it wrapped onto, the way
// a Claude Code or Codex paragraph fills the pane.
func TestUpdate_TripleClick_SelectsWrappedSentence(t *testing.T) {
	pinClickClock(t)
	m := pasteTestModel(&fakeSender{})
	pane := m.curTabs()[0].ActivePaneModel()
	long := "This second sentence is deliberately long so that it runs past the right edge of the pane and wraps onto the next row."
	pane.AppendOutput([]byte("  First one. " + long + " Third one.\r\n\r\n  Next paragraph."))

	x, y := paneCellScreen(m, 20, 0) // inside "This second"
	for range 3 {
		m = clickAt(m, x, y)
	}
	if m.selection == nil {
		t.Fatal("triple click left no selection")
	}
	if got := extractText(pane, m.selection); got != long {
		t.Errorf("selected %q, want %q", got, long)
	}
}

func TestNotesEditor_SelectSentenceAt(t *testing.T) {
	ne, err := NewNotesEditor(t.TempDir(), "pane-s", "Shell", 40, 10)
	if err != nil {
		t.Fatalf("NewNotesEditor: %v", err)
	}
	ne.HandlePaste("First bit. Second bit here! Third.")
	if !ne.SelectSentenceAt(0, 13) {
		t.Fatal("SelectSentenceAt returned false")
	}
	if got := ne.ExtractSelection(); got != "Second bit here!" {
		t.Errorf("selected %q, want %q", got, "Second bit here!")
	}
}

func TestUpdate_DoubleClick_DifferentCellsIsTwoClicks(t *testing.T) {
	pinClickClock(t)
	m := pasteTestModel(&fakeSender{})
	m.curTabs()[0].ActivePaneModel().AppendOutput([]byte("echo hello"))

	x, y := paneCellScreen(m, 1, 0)
	m = clickAt(m, x, y)
	m = clickAt(m, x+6, y)
	if m.selection != nil {
		t.Errorf("clicks on two cells selected %+v", m.selection)
	}
}

func TestUpdate_DoubleClick_SlowClicksAreTwoClicks(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	prev := clickNow
	clickNow = func() time.Time { return now }
	t.Cleanup(func() { clickNow = prev })

	m := pasteTestModel(&fakeSender{})
	m.curTabs()[0].ActivePaneModel().AppendOutput([]byte("echo hello"))
	x, y := paneCellScreen(m, 1, 0)
	m = clickAt(m, x, y)
	now = now.Add(doubleClickWindow + time.Millisecond)
	m = clickAt(m, x, y)
	if m.selection != nil {
		t.Errorf("slow second click selected %+v", m.selection)
	}
}

func TestNotesEditor_SelectWordAt(t *testing.T) {
	ne, err := NewNotesEditor(t.TempDir(), "pane-w", "Shell", 40, 10)
	if err != nil {
		t.Fatalf("NewNotesEditor: %v", err)
	}
	ne.HandlePaste("see ./a/b.txt now.")
	if !ne.SelectWordAt(0, 6) {
		t.Fatal("SelectWordAt on a word returned false")
	}
	if got := ne.ExtractSelection(); got != "./a/b.txt" {
		t.Errorf("selected %q, want %q", got, "./a/b.txt")
	}
	if !ne.SelectWordAt(0, 15) {
		t.Fatal("SelectWordAt on the last word returned false")
	}
	if got := ne.ExtractSelection(); got != "now" {
		t.Errorf("selected %q, want %q", got, "now")
	}
	ne.ClearSelection()
	if ne.SelectWordAt(0, 3) {
		t.Error("SelectWordAt on a space returned true")
	}
	if ne.HasSelection() {
		t.Error("SelectWordAt on a space left a selection")
	}
}

// Paste in notes mode goes to whichever side has focus. The terminal's own
// Ctrl+V arrives as tea.PasteMsg, which used to go to the editor always.
func TestUpdate_PasteMsg_NotesModeFollowsFocus(t *testing.T) {
	for _, paneFocused := range []bool{true, false} {
		name := "editor focused"
		if paneFocused {
			name = "pane focused"
		}
		t.Run(name, func(t *testing.T) {
			fake := &fakeSender{}
			m := pasteTestModel(fake)
			ne, err := NewNotesEditor(t.TempDir(), "p1", "Shell", 40, 10)
			if err != nil {
				t.Fatalf("NewNotesEditor: %v", err)
			}
			m.notesMode = true
			m.notesEditor = ne
			m.notesPaneFocused = paneFocused

			next, _ := m.Update(tea.PasteMsg{Content: "pasted"})
			m = next.(Model)

			inEditor := m.notesEditor.Content() == "pasted"
			if paneFocused {
				if len(fake.sent) != 1 {
					t.Errorf("pane got %d sends, want 1", len(fake.sent))
				}
				if inEditor {
					t.Error("paste landed in the editor while the pane had focus")
				}
			} else {
				if len(fake.sent) != 0 {
					t.Errorf("pane got %d sends while the editor had focus", len(fake.sent))
				}
				if !inEditor {
					t.Errorf("editor content = %q, want %q", m.notesEditor.Content(), "pasted")
				}
			}
		})
	}
}

// Ctrl+U / Ctrl+K reach the notes editor through Update, the way a real key
// press does, and kill to the line start / end. Ctrl+U at the end of a line
// clears it.
func TestUpdate_NotesEditor_CtrlUAndCtrlK(t *testing.T) {
	tests := []struct {
		name string
		code rune
		col  int
		want string
	}{
		{"ctrl+u at end clears the line", 'u', 11, ""},
		{"ctrl+u mid-line keeps the rest", 'u', 6, "world"},
		{"ctrl+k mid-line keeps the start", 'k', 5, "hello"},
		{"ctrl+u at start changes nothing", 'u', 0, "hello world"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := pasteTestModel(&fakeSender{})
			ne, err := NewNotesEditor(t.TempDir(), "p1", "Shell", 40, 10)
			if err != nil {
				t.Fatalf("NewNotesEditor: %v", err)
			}
			ne.HandlePaste("hello world\nsecond")
			ne.SetCursor(0, tt.col)
			m.notesMode = true
			m.notesEditor = ne

			next, _ := m.Update(tea.KeyPressMsg{Code: tt.code, Mod: tea.ModCtrl})
			m = next.(Model)

			want := tt.want + "\nsecond"
			if got := m.notesEditor.Content(); got != want {
				t.Errorf("content = %q, want %q", got, want)
			}
		})
	}
}

// Windows Terminal delivers a bracketed paste with every newline as a bare
// CR. The editor used to delete CRs, which joined the paste into one line —
// and Ctrl+U at its end then emptied the whole paste.
func TestUpdate_NotesPaste_KeepsLineBreaks(t *testing.T) {
	tests := []struct {
		name, paste, want string
	}{
		{"bare CR (Windows Terminal)", "one\rtwo\r\rfour", "one\ntwo\n\nfour"},
		{"CRLF is one break, not two", "one\r\ntwo", "one\ntwo"},
		{"LF unchanged", "one\ntwo", "one\ntwo"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := pasteTestModel(&fakeSender{})
			ne, err := NewNotesEditor(t.TempDir(), "p1", "Shell", 40, 10)
			if err != nil {
				t.Fatalf("NewNotesEditor: %v", err)
			}
			m.notesMode = true
			m.notesEditor = ne

			next, _ := m.Update(tea.PasteMsg{Content: tt.paste})
			m = next.(Model)
			if got := m.notesEditor.Content(); got != tt.want {
				t.Errorf("content = %q, want %q", got, tt.want)
			}
		})
	}
}

// Real line breaks in ordinary shell output must survive the word-wrap
// heuristic, however long the next line's first word is (review finding on
// #227).
func TestExtractText_HardBreaksAreNotAppWraps(t *testing.T) {
	tests := []struct {
		name string
		feed string
		want string
	}{
		{
			name: "short code row before an indented long identifier",
			feed: "if ok:\r\n    very_long_function_name_that_fits()",
			want: "if ok:\n    very_long_function_name_that_fits()",
		},
		{
			// Half full and the next word would not fit, but the next row
			// is indented differently: code, not a paragraph continuation.
			name: "indent change is not a continuation",
			feed: "for item in collection_of_things:\r\n    another_long_identifier_name",
			want: "for item in collection_of_things:\n    another_long_identifier_name",
		},
		{
			// Same indent, but a 2-column row is no wrapped paragraph.
			name: "short row at the same indent",
			feed: "ok\r\nvery_long_identifier_name_that_is_long",
			want: "ok\nvery_long_identifier_name_that_is_long",
		},
		{
			// Same layout with the continuation lined up is still joined.
			name: "lined-up continuation still joins",
			feed: "  the quick brown fox jumps over the\r\n  extraordinarily lazy dog",
			want: "the quick brown fox jumps over the extraordinarily lazy dog",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pane := textPane(t, 40, 5, tt.feed)
			last := lastContentLine(pane) - pane.vt.ScrollbackLen()
			if got := extractText(pane, selectLines(pane, 0, last)); got != tt.want {
				t.Errorf("extractText = %q, want %q", got, tt.want)
			}
		})
	}
}

// paneIsAgent is what turns the reply-marker rule on, so it is decided by the
// plugin's category — the rule the daemon uses for "is this an AI pane" —
// and never by the glyph.
func TestPaneIsAgent_FollowsPluginCategory(t *testing.T) {
	dir := t.TempDir()
	for name, category := range map[string]string{"agent": "ai", "tool": "tools"} {
		toml := "[plugin]\nname = \"" + name + "\"\ncategory = \"" + category + "\"\n\n[command]\ncmd = \"" + name + "\"\n"
		if err := os.WriteFile(filepath.Join(dir, name+".toml"), []byte(toml), 0o644); err != nil {
			t.Fatalf("write toml: %v", err)
		}
	}
	reg := plugin.NewRegistry()
	if err := reg.LoadFromDir(dir); err != nil {
		t.Fatalf("LoadFromDir: %v", err)
	}
	m := Model{pluginRegistry: reg}
	for paneType, want := range map[string]bool{"agent": true, "tool": false, "terminal": false, "unknown": false} {
		if got := m.paneIsAgent(&PaneModel{Type: paneType}); got != want {
			t.Errorf("paneIsAgent(%q) = %v, want %v", paneType, got, want)
		}
	}
	if (Model{}).paneIsAgent(&PaneModel{Type: "agent"}) {
		t.Error("paneIsAgent with no registry = true, want false")
	}
}

// The copy paths hand copyText the pane's agent-ness. Driven through a real
// right-click so deleting that argument at the call site fails here, not
// only in a unit test of copyText.
func TestUpdate_RightClickCopy_ReplyMarkerOnlyInAgentPanes(t *testing.T) {
	dir := t.TempDir()
	toml := "[plugin]\nname = \"agent\"\ncategory = \"ai\"\n\n[command]\ncmd = \"agent\"\n"
	if err := os.WriteFile(filepath.Join(dir, "agent.toml"), []byte(toml), 0o644); err != nil {
		t.Fatalf("write toml: %v", err)
	}
	reg := plugin.NewRegistry()
	if err := reg.LoadFromDir(dir); err != nil {
		t.Fatalf("LoadFromDir: %v", err)
	}

	var copied string
	prev := clipboardWriteText
	clipboardWriteText = func(s string) error { copied = s; return nil }
	t.Cleanup(func() { clipboardWriteText = prev })

	for paneType, want := range map[string]string{
		"agent":    "• one\n\ntwo",   // reply margin removed
		"terminal": "• one\n\n  two", // a bullet list: nesting kept
	} {
		t.Run(paneType, func(t *testing.T) {
			copied = ""
			m := pasteTestModel(&fakeSender{})
			m.pluginRegistry = reg
			pane := m.curTabs()[0].ActivePaneModel()
			pane.Type = paneType
			pane.AppendOutput([]byte("• one\r\n\r\n  two"))
			m.selection = selectLines(pane, 0, 2)

			x, y := paneCellScreen(m, 1, 0)
			_, cmd := m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseRight})
			if cmd == nil {
				t.Fatal("right-click with a selection returned no copy command")
			}
			cmd()
			if copied != want {
				t.Errorf("copied %q, want %q", copied, want)
			}
		})
	}
}
