package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
)

const driftReason = `unknown toggle "driftx" for plugin claude-code (see list_plugins)`

// A refused create's flash lasted flashDuration (3 s), like any other flash,
// and was gone before its reason could be read (manual retest, PR #256). An
// error flash now lasts longer, scaled by its length, and a routine message
// that arrives meanwhile does not take the bar from it. The clock is frozen:
// this binary's flashDuration is 10 ms, far below a loaded CI runner's jitter.
func TestCreateRefused_FlashOutlastsARoutineFlash(t *testing.T) {
	m, conn := rawArgsModel(t, ipc.RightsFull)
	t0 := time.Now()
	m.now = func() time.Time { return t0 }
	m, sent := submitOrdinaryCreate(t, m, conn, 0)
	m = refusalArrives(t, m, sent, roDest, driftReason)

	refusal := m.flashText
	if !strings.Contains(refusal, "driftx") {
		t.Fatalf("flash = %q, want the daemon's reason", refusal)
	}
	ttl := m.flashUntil.Sub(t0)
	if ttl < 2*flashDuration {
		t.Errorf("the refusal lasts %v, want at least twice flashDuration (%v)", ttl, 2*flashDuration)
	}
	if ttl <= errorFlashTTL("x") {
		t.Errorf("the refusal lasts %v, no longer than a one-cell error flash: not scaled by its length", ttl)
	}

	// A routine answer arrives while the refusal is on screen.
	upToDate := stageUpdateRespMsg{Resp: ipc.StageUpdateRespPayload{Error: "already up to date"}}
	m = roUpdate(t, m, upToDate)
	if m.flashText != refusal {
		t.Fatalf("a routine flash replaced the refusal: %q", m.flashText)
	}

	// flashCmd's tick fires after flashDuration: the refusal stays drawn, and
	// the expiry waits out the rest of it.
	m.now = func() time.Time { return t0.Add(flashDuration) }
	m.width = 172
	next, cmd := m.Update(flashExpireMsg{})
	m = next.(Model)
	if cmd == nil {
		t.Error("the expiry of a live error flash scheduled nothing: the flash would never be cleared")
	}
	if bar := m.renderStatusBar(); !strings.Contains(bar, "pane not created") {
		t.Errorf("after flashDuration the status bar %q no longer shows the refusal", bar)
	}

	// Past its TTL it goes, and routine flashes show again.
	m.now = func() time.Time { return t0.Add(ttl) }
	m = roUpdate(t, m, flashExpireMsg{})
	if m.flashText != "" {
		t.Errorf("flash = %q after its TTL, want cleared", m.flashText)
	}
	m = roUpdate(t, m, upToDate)
	if !strings.Contains(m.flashText, "up to date") {
		t.Errorf("flash = %q, want the routine message once the refusal expired", m.flashText)
	}
}

// A newer refusal replaces an older one: the latest answer is the one the
// user is waiting for.
func TestSetErrorFlash_ReplacesAnyFlash(t *testing.T) {
	t.Parallel()
	t0 := time.Now()
	m := &Model{now: func() time.Time { return t0 }}
	m.setErrorFlash("first refusal")
	m.setErrorFlash("second refusal")
	if m.flashText != "second refusal" {
		t.Errorf("flash = %q, want the newer refusal", m.flashText)
	}
	m.setFlash("routine")
	if m.flashText != "second refusal" {
		t.Errorf("flash = %q, a routine flash replaced a live error flash", m.flashText)
	}
}

func TestErrorFlashTTL_Bounds(t *testing.T) {
	t.Parallel()
	if got := errorFlashTTL(""); got != 2*flashDuration {
		t.Errorf("empty text: %v, want %v", got, 2*flashDuration)
	}
	if got := errorFlashTTL(strings.Repeat("x", 100)); got != 3*flashDuration {
		t.Errorf("100 cells: %v, want %v", got, 3*flashDuration)
	}
	if got := errorFlashTTL(strings.Repeat("x", 10000)); got != 5*flashDuration {
		t.Errorf("10000 cells: %v, want the cap %v", got, 5*flashDuration)
	}
}
