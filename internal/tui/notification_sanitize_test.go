package tui

import (
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
)

// TestNotificationCenter_View_SanitizesHostileTitles closes the gap that every
// other pane-sourced string in the sidebar already covers: sidebar.go routes
// its values through sanitizeRemoteText, notification.go rendered e.Title raw.
//
// A card title is not ours. It arrives from a pane's own child via the hook
// spool, and the card is composited straight into the frame by overlayRight
// with no VT emulator in between — so a title is one of the few strings in
// Quil that reaches the user's terminal without anything parsing it first.
// An agent running inside a pane knows QUIL_PANE_ID and QUIL_HOOK_HOME from
// its own environment and the spool is same-user writable, so this is reachable
// without any privilege at all.
//
// The C1 and bidi rows carry LITERAL U+009B / U+202E bytes rather than \u
// escapes, because those two are the payload — a rewrite that flattens them to
// ASCII leaves the row asserting nothing while still passing. If you edit this
// file, check the bytes survived (`cat -A` shows them as M-BM-^[ and M-bM-^@M-.).
func TestNotificationCenter_View_SanitizesHostileTitles(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		title   string
		mustNot string
		whatFor string
	}{
		{
			name:    "CSI clear-screen",
			title:   "Turn failed: \x1b[2J\x1b[Hgotcha",
			mustNot: "\x1b[2J",
			whatFor: "an escape that clears the user's screen",
		},
		{
			name:    "OSC 52 clipboard write",
			title:   "Turn failed: \x1b]52;c;aGVsbG8=\x07",
			mustNot: "\x1b]52",
			whatFor: "an OSC sequence that writes the user's clipboard",
		},
		{
			// U+009B, the C1 CSI introducer. internal/tui/oscfilter.go exists
			// because a raw C1 byte in a UTF-8 stream is treated as a control
			// by the emulator, so it must not survive here either.
			name:    "C1 CSI introducer",
			title:   "Turn failed: 2J",
			mustNot: "",
			whatFor: "the C1 CSI introducer",
		},
		{
			// U+202E is PRINTABLE, so a C0-only filter passes it straight
			// through while it reverses everything rendered after it. This is
			// the case that makes a dedicated sanitiser necessary.
			name:    "bidi override",
			title:   "Turn failed: ‮gnihton‬",
			mustNot: "‮",
			whatFor: "a printable bidi override that reverses the rendered line",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			nc := NewNotificationCenter(30, 50)
			nc.AddEvent(ipc.PaneEventPayload{
				ID:       "evt-1",
				PaneID:   "pane-1",
				Type:     "hook.claude.StopFailure",
				Title:    tt.title,
				Severity: "warning",
			})
			nc.visible = true

			out := nc.View(24)
			if strings.Contains(out, tt.mustNot) {
				t.Errorf("rendered card still carries %s (%q)", tt.whatFor, tt.mustNot)
			}
		})
	}
}

// TestNotificationCenter_View_KeepsOrdinaryTitleText guards the other
// direction: sanitising must not eat the text the card exists to show. Without
// this, deleting the title from the render entirely would pass every case
// above.
func TestNotificationCenter_View_KeepsOrdinaryTitleText(t *testing.T) {
	t.Parallel()
	nc := NewNotificationCenter(30, 50)
	nc.AddEvent(ipc.PaneEventPayload{
		ID:       "evt-1",
		PaneID:   "pane-1",
		Type:     "hook.claude.StopFailure",
		Title:    "Turn failed: API Error 500",
		Severity: "warning",
	})
	nc.visible = true

	out := nc.View(24)
	if !strings.Contains(out, "API Error 500") {
		t.Errorf("sanitising must not drop ordinary title text; got:\n%s", out)
	}
}
