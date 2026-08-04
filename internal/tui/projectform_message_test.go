package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// The form has ONE message line and three kinds of news to put on it. Rendering
// all three as a red ✗ told the user an install in progress had failed —
// reported as "it was installing fine but the message was red, which seemed
// strange".
//
// Asserted on the KIND rather than the escape sequence: the colours are the
// package's existing palette and may be retuned, but a progress message must
// never be classified as a failure.
func TestProjectForm_MessageKindMatchesTheNews(t *testing.T) {
	newModel := func() Model {
		return Model{
			width: 100, height: 30,
			client:             NewRouter(map[string]Client{"": newFakeConn()}),
			projectFormDialing: "gpu01",
			installDestFn:      func(string) error { return nil },
			attached:           map[string]bool{},
		}
	}

	t.Run("a host being provisioned is busy, not failed", func(t *testing.T) {
		m := newModel()
		next, _ := m.Update(destDialedMsg{dest: "gpu01", err: ErrRemoteQuilMissing})
		if got := next.(Model).projectFormMsgKind; got != projectFormMsgBusy {
			t.Errorf("kind = %d, want busy — an install under way is progress, and "+
				"the red ✗ is what the line says when ssh cannot reach the host at all", got)
		}
	})

	t.Run("an out-of-date daemon being upgraded is busy", func(t *testing.T) {
		m := newModel()
		err := fmt.Errorf("%w: gpu01 runs 1.46.3", ErrRemoteVersionMismatch)
		next, _ := m.Update(destDialedMsg{dest: "gpu01", err: err})
		if got := next.(Model).projectFormMsgKind; got != projectFormMsgBusy {
			t.Errorf("kind = %d, want busy", got)
		}
	})

	t.Run("a connected host is a success", func(t *testing.T) {
		m := newModel()
		next, _ := m.Update(destDialedMsg{dest: "gpu01", client: newFakeConn()})
		got := next.(Model)
		if got.projectFormMsgKind != projectFormMsgOK {
			t.Errorf("kind = %d, want ok", got.projectFormMsgKind)
		}
		if !strings.Contains(got.projectFormErr, "gpu01") {
			t.Errorf("message = %q, want the connected host named", got.projectFormErr)
		}
	})

	t.Run("a host that cannot be reached is an error", func(t *testing.T) {
		m := newModel()
		m.installDestFn = nil // nothing to offer, so the failure is reported
		next, _ := m.Update(destDialedMsg{dest: "gpu01", err: errors.New("ssh: connect: no route to host")})
		if got := next.(Model).projectFormMsgKind; got != projectFormMsgError {
			t.Errorf("kind = %d, want error — this is the case the red ✗ is for", got)
		}
	})
}

// A failure arriving after a success must not inherit its colour. The kind and
// the text are one fact, so every writer sets both — this pins that the setters
// are actually used rather than the string being assigned beside a stale kind.
func TestProjectForm_MessageKindDoesNotOutliveItsMessage(t *testing.T) {
	m := Model{
		width: 100, height: 30,
		client:   NewRouter(map[string]Client{"": newFakeConn()}),
		attached: map[string]bool{},
	}

	m.setFormOK("connected to gpu01")
	m.setFormError("name required")
	if m.projectFormMsgKind != projectFormMsgError {
		t.Fatalf("kind = %d after an error followed a success, want error", m.projectFormMsgKind)
	}

	glyph, _ := projectFormMsgStyle(m.projectFormMsgKind)
	if glyph != "✗" {
		t.Errorf("glyph = %q, want ✗ — a validation failure wearing the success "+
			"glyph is worse than the original bug, which at least over-warned", glyph)
	}
}

// The zero value must be the error kind: every pre-existing writer of this line
// reports a validation failure, and a Model built in a test (or a future writer
// that forgets) has to fail loud rather than green.
func TestProjectForm_ZeroKindIsAnError(t *testing.T) {
	var kind projectFormMsgKind
	if kind != projectFormMsgError {
		t.Fatalf("zero kind = %d, want error", kind)
	}
	if glyph, _ := projectFormMsgStyle(kind); glyph != "✗" {
		t.Errorf("zero-kind glyph = %q, want ✗", glyph)
	}
}
