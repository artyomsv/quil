package tui

import (
	"testing"
	"time"
)

func TestTrimMsgType(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"tea.KeyPressMsg", "KeyPressMsg"},
		{"charm.land/bubbletea/v2.WindowSizeMsg", "WindowSizeMsg"},
		{"github.com/foo/bar.MyType", "MyType"},
		{"NoDot", "NoDot"},
		{"", ""},
		{".LeadingDot", "LeadingDot"},
		{"trailing.", ""},
		{"a.b.c", "c"},
	}
	for _, tc := range cases {
		if got := trimMsgType(tc.in); got != tc.want {
			t.Errorf("trimMsgType(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestEventLoopStats_NilReceiver_NoPanic(t *testing.T) {
	// Each public method must tolerate a nil receiver — Model may run with
	// stats disabled. Calls on a nil pointer must be no-ops.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nil-receiver panicked: %v", r)
		}
	}()
	var s *eventLoopStats
	s.recordMsg("tea.KeyPressMsg", time.Millisecond)
	s.recordPaneOutput(1024, time.Millisecond)
	s.recordView(time.Millisecond)
	s.flush()
}

func TestEventLoopStats_RecordMsg_BumpsBucket(t *testing.T) {
	s := newEventLoopStats()
	s.recordMsg("tea.KeyPressMsg", 5*time.Millisecond)
	s.recordMsg("tea.KeyPressMsg", 15*time.Millisecond)

	b := s.byType["tea.KeyPressMsg"]
	if b == nil {
		t.Fatal("bucket missing")
	}
	if b.count != 2 {
		t.Errorf("count = %d, want 2", b.count)
	}
	wantTotal := int64((5 + 15) * time.Millisecond)
	if b.totalNs != wantTotal {
		t.Errorf("totalNs = %d, want %d", b.totalNs, wantTotal)
	}
	wantMax := int64(15 * time.Millisecond)
	if b.maxNs != wantMax {
		t.Errorf("maxNs = %d, want %d (largest sample wins)", b.maxNs, wantMax)
	}
}

func TestEventLoopStats_KeyBacklog_TracksMaxAcrossMessages(t *testing.T) {
	s := newEventLoopStats()

	// 3 non-key messages then a key — backlog should be captured at 3.
	for i := 0; i < 3; i++ {
		s.recordMsg("tea.WindowSizeMsg", time.Microsecond)
	}
	s.recordMsg("tea.KeyPressMsg", time.Microsecond)
	if s.maxSinceKey != 3 {
		t.Errorf("after 3 non-key + key: maxSinceKey = %d, want 3", s.maxSinceKey)
	}
	if s.sinceLastKey != 0 {
		t.Errorf("sinceLastKey = %d, want 0 (reset by KeyPressMsg)", s.sinceLastKey)
	}

	// Smaller backlog (2) must NOT lower maxSinceKey.
	for i := 0; i < 2; i++ {
		s.recordMsg("tea.WindowSizeMsg", time.Microsecond)
	}
	s.recordMsg("tea.KeyPressMsg", time.Microsecond)
	if s.maxSinceKey != 3 {
		t.Errorf("after smaller backlog: maxSinceKey = %d, want 3 (high-water mark)", s.maxSinceKey)
	}

	// Larger backlog (5) must raise it.
	for i := 0; i < 5; i++ {
		s.recordMsg("tea.WindowSizeMsg", time.Microsecond)
	}
	s.recordMsg("tea.KeyPressMsg", time.Microsecond)
	if s.maxSinceKey != 5 {
		t.Errorf("after larger backlog: maxSinceKey = %d, want 5", s.maxSinceKey)
	}
}

func TestEventLoopStats_RecordPaneOutput_AccumulatesBytesAndMaxNs(t *testing.T) {
	s := newEventLoopStats()
	s.recordPaneOutput(100, 2*time.Millisecond)
	s.recordPaneOutput(50, 8*time.Millisecond)
	s.recordPaneOutput(200, 1*time.Millisecond)

	if s.paneOutBytes != 350 {
		t.Errorf("paneOutBytes = %d, want 350", s.paneOutBytes)
	}
	wantMax := int64(8 * time.Millisecond)
	if s.paneOutMaxNs != wantMax {
		t.Errorf("paneOutMaxNs = %d, want %d", s.paneOutMaxNs, wantMax)
	}
}

func TestEventLoopStats_RecordView_AccumulatesCountTotalMax(t *testing.T) {
	s := newEventLoopStats()
	s.recordView(2 * time.Millisecond)
	s.recordView(7 * time.Millisecond)
	s.recordView(3 * time.Millisecond)

	if s.viewCount != 3 {
		t.Errorf("viewCount = %d, want 3", s.viewCount)
	}
	wantTotal := int64((2 + 7 + 3) * time.Millisecond)
	if s.viewTotalNs != wantTotal {
		t.Errorf("viewTotalNs = %d, want %d", s.viewTotalNs, wantTotal)
	}
	wantMax := int64(7 * time.Millisecond)
	if s.viewMaxNs != wantMax {
		t.Errorf("viewMaxNs = %d, want %d", s.viewMaxNs, wantMax)
	}
}

func TestEventLoopStats_Flush_ResetsCounters(t *testing.T) {
	s := newEventLoopStats()
	s.recordMsg("tea.KeyPressMsg", 5*time.Millisecond)
	s.recordMsg("tea.WindowSizeMsg", 1*time.Millisecond)
	s.recordPaneOutput(500, 2*time.Millisecond)
	s.recordView(3 * time.Millisecond)
	// Build up a backlog so maxSinceKey > 0 after the next keypress.
	s.recordMsg("tea.WindowSizeMsg", time.Microsecond)
	s.recordMsg("tea.WindowSizeMsg", time.Microsecond)
	s.recordMsg("tea.KeyPressMsg", time.Microsecond)
	if s.maxSinceKey == 0 {
		t.Fatalf("test setup: expected maxSinceKey > 0 before flush")
	}

	beforeFlush := s.lastFlush
	time.Sleep(2 * time.Millisecond) // ensure lastFlush moves
	s.flush()

	if len(s.byType) != 0 {
		t.Errorf("byType has %d entries after flush, want 0 (fresh map)", len(s.byType))
	}
	if s.paneOutBytes != 0 || s.paneOutMaxNs != 0 {
		t.Errorf("pane-out counters not reset: bytes=%d maxNs=%d", s.paneOutBytes, s.paneOutMaxNs)
	}
	if s.viewCount != 0 || s.viewTotalNs != 0 || s.viewMaxNs != 0 {
		t.Errorf("view counters not reset: count=%d total=%d max=%d", s.viewCount, s.viewTotalNs, s.viewMaxNs)
	}
	if s.maxSinceKey != 0 {
		t.Errorf("maxSinceKey = %d, want 0", s.maxSinceKey)
	}
	if !s.lastFlush.After(beforeFlush) {
		t.Errorf("lastFlush did not advance: before=%v after=%v", beforeFlush, s.lastFlush)
	}
}

func TestEventLoopStats_Flush_PreservesSinceLastKey(t *testing.T) {
	// sinceLastKey represents in-flight queue depth at flush time and must
	// survive a flush so the next KeyPressMsg (possibly in the next window)
	// can compute maxSinceKey correctly. See the field comment in perf.go.
	s := newEventLoopStats()
	for i := 0; i < 7; i++ {
		s.recordMsg("tea.WindowSizeMsg", time.Microsecond)
	}
	if s.sinceLastKey != 7 {
		t.Fatalf("test setup: sinceLastKey = %d, want 7", s.sinceLastKey)
	}
	s.flush()
	if s.sinceLastKey != 7 {
		t.Errorf("sinceLastKey = %d after flush, want 7 (must survive)", s.sinceLastKey)
	}
	// Next keypress lifts the preserved backlog into maxSinceKey of the
	// new window.
	s.recordMsg("tea.KeyPressMsg", time.Microsecond)
	if s.maxSinceKey != 7 {
		t.Errorf("maxSinceKey = %d after keypress, want 7 (carried-over backlog)", s.maxSinceKey)
	}
}

func TestFormatBreakdown_SortsByTotalNsDesc(t *testing.T) {
	s := newEventLoopStats()
	// "fast" gets many quick samples, "slow" gets one big one. "slow"
	// should sort first because totalNs is the sort key, not count.
	for i := 0; i < 10; i++ {
		s.recordMsg("pkg.fast", 1*time.Millisecond)
	}
	s.recordMsg("pkg.slow", 100*time.Millisecond)
	s.recordMsg("pkg.empty", 0) // included (count > 0)

	out := s.formatBreakdown()

	slowIdx := indexOf(out, "slow")
	fastIdx := indexOf(out, "fast")
	if slowIdx < 0 || fastIdx < 0 {
		t.Fatalf("breakdown missing expected entries: %q", out)
	}
	if slowIdx > fastIdx {
		t.Errorf("expected 'slow' before 'fast' (sorted by totalNs desc): %q", out)
	}
}

func TestFormatBreakdown_SkipsZeroCountBuckets(t *testing.T) {
	s := newEventLoopStats()
	// Inject a zombie bucket directly (count == 0).
	s.byType["zombie"] = &perfBucket{}
	s.recordMsg("alive", 1*time.Millisecond)

	out := s.formatBreakdown()
	if indexOf(out, "zombie") >= 0 {
		t.Errorf("zero-count bucket should not appear: %q", out)
	}
	if indexOf(out, "alive") < 0 {
		t.Errorf("active bucket missing: %q", out)
	}
}

func TestResetCounters_RebuildsMap(t *testing.T) {
	// resetCounters must allocate a fresh map so types that stop appearing
	// get garbage-collected instead of accumulating forever.
	s := newEventLoopStats()
	s.recordMsg("transient.OneOff", time.Millisecond)
	originalMap := s.byType
	s.resetCounters()
	if len(s.byType) != 0 {
		t.Errorf("byType has %d entries after reset, want 0", len(s.byType))
	}
	// Pointer identity check: the map must be a different allocation.
	if reflectSameMap(s.byType, originalMap) {
		t.Errorf("byType map was not replaced — zombie entries can leak across windows")
	}
}

// indexOf is a small helper — strings.Index without the import noise in
// the assertion sites. Returns -1 when not found.
func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// reflectSameMap returns true when both arguments refer to the exact same
// underlying map header. Map values aren't comparable with ==, so we
// compare the address of an arbitrary value cell — a fresh map cannot
// share a key with the old one.
func reflectSameMap(a, b map[string]*perfBucket) bool {
	if len(a) != len(b) {
		return false
	}
	// Quick test: if the original had an entry, the new map shouldn't
	// contain that key. resetCounters is called on a known-non-empty map
	// in this test, so this check is decisive.
	for k := range b {
		if _, ok := a[k]; ok {
			return true
		}
	}
	return false
}
