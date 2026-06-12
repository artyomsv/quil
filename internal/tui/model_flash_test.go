package tui

import (
	"testing"
	"time"
)

func TestSetFlash_SetsTextAndExpiry(t *testing.T) {
	t.Parallel()
	m := &Model{}
	m.setFlash("no git repo here")
	if m.flashText != "no git repo here" {
		t.Errorf("flashText = %q", m.flashText)
	}
	if !m.flashUntil.After(time.Now()) {
		t.Error("flashUntil must be in the future")
	}
}
