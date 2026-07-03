package daemon

import (
	"testing"

	"github.com/artyomsv/quil/internal/hookevents"
)

// Tests for the model/context extraction in emitHookEvent: turn-boundary
// hook events (claude Stop/PostCompact, opencode session.idle) carry
// data.model + data.context_tokens, which the daemon mirrors onto the pane
// (runtime-only) so the workspace snapshot can deliver them to clients that
// attach between turns.

func modelPayload(paneID, model, tokens string) hookevents.Payload {
	return hookevents.Payload{
		V:         hookevents.SchemaVersion,
		TsMs:      1,
		PaneID:    paneID,
		Source:    hookevents.SourceClaude,
		HookEvent: "Stop",
		Title:     "Reply ready",
		Severity:  hookevents.SeverityWarning,
		Data:      map[string]string{"model": model, "context_tokens": tokens},
	}
}

func TestEmitHookEvent_StoresModelUsage(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	pane, err := d.session.CreatePane(tab.ID, "")
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}

	d.emitHookEvent(modelPayload(pane.ID, "claude-opus-4-8", "601002"))

	pane.PluginMu.Lock()
	model, tokens := pane.LastModel, pane.LastContextTokens
	pane.PluginMu.Unlock()
	if model != "claude-opus-4-8" || tokens != 601002 {
		t.Fatalf("pane model=%q tokens=%d, want claude-opus-4-8/601002", model, tokens)
	}
}

func TestEmitHookEvent_InvalidTokens_LeavesFieldsUntouched(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	pane, err := d.session.CreatePane(tab.ID, "")
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	d.emitHookEvent(modelPayload(pane.ID, "claude-opus-4-8", "601002"))
	// A later event with a malformed count must not clobber the good value.
	d.emitHookEvent(modelPayload(pane.ID, "claude-opus-4-8", "not-a-number"))
	d.emitHookEvent(modelPayload(pane.ID, "claude-opus-4-8", "-5"))

	pane.PluginMu.Lock()
	model, tokens := pane.LastModel, pane.LastContextTokens
	pane.PluginMu.Unlock()
	if model != "claude-opus-4-8" || tokens != 601002 {
		t.Fatalf("pane model=%q tokens=%d, want prior valid values retained", model, tokens)
	}
}

func TestEmitHookEvent_NoModelData_LeavesFieldsEmpty(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	pane, err := d.session.CreatePane(tab.ID, "")
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	p := modelPayload(pane.ID, "", "")
	p.Data = nil
	d.emitHookEvent(p)

	pane.PluginMu.Lock()
	model, tokens := pane.LastModel, pane.LastContextTokens
	pane.PluginMu.Unlock()
	if model != "" || tokens != 0 {
		t.Fatalf("pane model=%q tokens=%d, want empty", model, tokens)
	}
}
