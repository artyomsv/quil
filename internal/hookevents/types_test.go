package hookevents

import (
	"errors"
	"testing"
)

func TestPayload_Validate_Happy(t *testing.T) {
	t.Parallel()
	p := Payload{
		V:         SchemaVersion,
		PaneID:    "pane-abc",
		Source:    SourceClaude,
		HookEvent: "PermissionRequest",
		Title:     "Needs approval: Bash",
		Severity:  SeverityWarning,
	}
	if err := p.Validate(); err != nil {
		t.Errorf("happy-path payload should validate; got %v", err)
	}
}

func TestPayload_Validate_Errors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		mut  func(p *Payload)
		want error
	}{
		{"wrong schema version", func(p *Payload) { p.V = 99 }, ErrSchemaVersion},
		{"missing pane id", func(p *Payload) { p.PaneID = "" }, ErrMissingPaneID},
		{"missing hook event", func(p *Payload) { p.HookEvent = "" }, ErrEmptyHookEvent},
		{"missing title", func(p *Payload) { p.Title = "" }, ErrMissingTitle},
		{"unknown severity", func(p *Payload) { p.Severity = "fatal" }, ErrUnknownSeverity},
		{"unknown source", func(p *Payload) { p.Source = "cursor" }, ErrUnknownSource},
	}

	base := Payload{
		V:         SchemaVersion,
		PaneID:    "pane-abc",
		Source:    SourceClaude,
		HookEvent: "Stop",
		Title:     "Reply ready",
		Severity:  SeverityInfo,
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := base
			tt.mut(&p)
			err := p.Validate()
			if !errors.Is(err, tt.want) {
				t.Errorf("Validate: got %v, want %v", err, tt.want)
			}
		})
	}
}

func TestPayload_Validate_EmptySeverityAllowed(t *testing.T) {
	// Some hooks fire without an explicit severity; the daemon treats it as
	// info downstream. The validator must accept the empty string.
	t.Parallel()
	p := Payload{
		V:         SchemaVersion,
		PaneID:    "pane-abc",
		Source:    SourceOpenCode,
		HookEvent: "session.idle",
		Title:     "Reply ready",
		Severity:  "",
	}
	if err := p.Validate(); err != nil {
		t.Errorf("empty severity should be allowed; got %v", err)
	}
}
