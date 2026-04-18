package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/artyomsv/quil/internal/logger"
)

// Tunable thresholds. Per-event slow-path logs use Debug so they don't
// flood Info; the periodic flush summary stays at Info.
const (
	// flushInterval is the minimum wall-clock gap between aggregate
	// summary lines.
	flushInterval = 5 * time.Second

	// slowUpdateThreshold is the per-message Update duration above which
	// a Debug line is emitted. 50ms picked empirically for Windows
	// ConPTY workloads where occasional 20-30ms updates are normal.
	slowUpdateThreshold = 50 * time.Millisecond

	// slowPaneOutputThreshold is the per-chunk vt.Write duration above
	// which a Debug line is emitted.
	slowPaneOutputThreshold = 10 * time.Millisecond

	// slowViewThreshold is the per-frame View duration above which a
	// Debug line is emitted.
	slowViewThreshold = 30 * time.Millisecond

	// keyBacklogWarnThreshold is the number of non-key messages observed
	// ahead of a KeyPressMsg above which a Debug line is emitted.
	// Empirically: terminal tools emit bursts of ~10-15 output messages
	// per keystroke under load; >20 is an outlier worth surfacing.
	keyBacklogWarnThreshold = 20
)

// eventLoopStats measures Bubble Tea event-loop throughput to diagnose
// typing responsiveness. Lives on Model as a pointer so mutations persist
// across value-receiver copies.
//
// Threading: every method (recordMsg, recordPaneOutput, recordView, flush)
// MUST be called from Bubble Tea's program goroutine — Update and View are
// serialized there in v2, so no synchronization is needed. Calls from a
// `tea.Cmd` background goroutine would race silently.
//
// The `window=...` label on the flush summary reflects wall-clock duration
// since the previous flush, not necessarily the span samples cover. After
// a long idle gap the label can read e.g. `window=35s` while all samples
// are from the last second of activity. The averages and counters remain
// correct; only the time label is loose.
type eventLoopStats struct {
	lastFlush time.Time

	byType map[string]*perfBucket

	paneOutBytes int64
	paneOutMaxNs int64

	viewCount   int64
	viewTotalNs int64
	viewMaxNs   int64

	// Non-key messages processed since the last KeyPressMsg — proxy for
	// queue backlog. Large values while typing mean keystroke messages are
	// queuing behind output messages.
	//
	// sinceLastKey deliberately survives flush(): it represents the
	// in-flight queue depth at flush time, which is meaningful information
	// the next keypress (possibly in the next window) needs to compute
	// maxSinceKey correctly.
	sinceLastKey int64
	maxSinceKey  int64
}

type perfBucket struct {
	count   int64
	totalNs int64
	maxNs   int64
}

func newEventLoopStats() *eventLoopStats {
	return &eventLoopStats{
		lastFlush: time.Now(),
		byType:    make(map[string]*perfBucket),
	}
}

// recordMsg records the duration of a single Update call.
// Caller must be on the Bubble Tea program goroutine.
func (s *eventLoopStats) recordMsg(msgType string, d time.Duration) {
	if s == nil {
		return
	}
	b := s.byType[msgType]
	if b == nil {
		b = &perfBucket{}
		s.byType[msgType] = b
	}
	ns := d.Nanoseconds()
	b.count++
	b.totalNs += ns
	if ns > b.maxNs {
		b.maxNs = ns
	}

	if msgType == "tea.KeyPressMsg" {
		if s.sinceLastKey > s.maxSinceKey {
			s.maxSinceKey = s.sinceLastKey
		}
		if s.sinceLastKey > keyBacklogWarnThreshold {
			logger.Debug("event-loop backlog at keypress: %d non-key msgs queued ahead", s.sinceLastKey)
		}
		s.sinceLastKey = 0
	} else {
		s.sinceLastKey++
	}

	if d >= slowUpdateThreshold {
		logger.Debug("slow update: type=%s dur=%s", msgType, d)
	}

	if time.Since(s.lastFlush) >= flushInterval {
		s.flush()
	}
}

// recordPaneOutput records a single PTY-output chunk's vt.Write duration.
// Caller must be on the Bubble Tea program goroutine.
func (s *eventLoopStats) recordPaneOutput(bytes int, d time.Duration) {
	if s == nil {
		return
	}
	s.paneOutBytes += int64(bytes)
	if ns := d.Nanoseconds(); ns > s.paneOutMaxNs {
		s.paneOutMaxNs = ns
	}
	if d >= slowPaneOutputThreshold {
		logger.Debug("slow pane-output: bytes=%d vt.Write=%s", bytes, d)
	}
}

// recordView records a single View() call's duration.
// Caller must be on the Bubble Tea program goroutine.
func (s *eventLoopStats) recordView(d time.Duration) {
	if s == nil {
		return
	}
	ns := d.Nanoseconds()
	s.viewCount++
	s.viewTotalNs += ns
	if ns > s.viewMaxNs {
		s.viewMaxNs = ns
	}
	if d >= slowViewThreshold {
		logger.Debug("slow view: dur=%s", d)
	}
}

// flush emits one aggregate summary line and resets all counters.
// Caller must be on the Bubble Tea program goroutine.
func (s *eventLoopStats) flush() {
	if s == nil {
		return
	}
	window := time.Since(s.lastFlush)
	breakdown := s.formatBreakdown()

	var viewAvg time.Duration
	if s.viewCount > 0 {
		viewAvg = time.Duration(s.viewTotalNs / s.viewCount)
	}

	logger.Info("perf window=%s | view(n=%d avg=%s max=%s) | pane-out(bytes=%d max-vt=%s) | key-backlog-max=%d | %s",
		window.Round(time.Millisecond),
		s.viewCount, viewAvg, time.Duration(s.viewMaxNs),
		s.paneOutBytes, time.Duration(s.paneOutMaxNs),
		s.maxSinceKey,
		breakdown,
	)

	s.resetCounters()
}

// formatBreakdown sorts active buckets by total time desc and renders them
// as a single space-separated string.
func (s *eventLoopStats) formatBreakdown() string {
	type row struct {
		name string
		b    *perfBucket
	}
	rows := make([]row, 0, len(s.byType))
	for k, v := range s.byType {
		if v.count == 0 {
			continue
		}
		rows = append(rows, row{k, v})
	}
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].b.totalNs > rows[j].b.totalNs
	})
	parts := make([]string, 0, len(rows))
	for _, r := range rows {
		avg := time.Duration(r.b.totalNs / r.b.count)
		parts = append(parts, fmt.Sprintf("%s(n=%d avg=%s max=%s)",
			trimMsgType(r.name), r.b.count, avg, time.Duration(r.b.maxNs)))
	}
	return strings.Join(parts, " ")
}

// resetCounters zeros every accumulator and rebuilds the bucket map so
// types that stop appearing get garbage-collected instead of accumulating
// zombie entries forever. sinceLastKey is intentionally preserved — see
// the field comment.
func (s *eventLoopStats) resetCounters() {
	s.byType = make(map[string]*perfBucket, len(s.byType))
	s.paneOutBytes = 0
	s.paneOutMaxNs = 0
	s.viewCount = 0
	s.viewTotalNs = 0
	s.viewMaxNs = 0
	s.maxSinceKey = 0
	s.lastFlush = time.Now()
}

// trimMsgType strips the package-qualified prefix from a Go type string,
// keeping only the type name itself ("tea.KeyPressMsg" → "KeyPressMsg").
func trimMsgType(s string) string {
	if i := strings.LastIndex(s, "."); i >= 0 {
		return s[i+1:]
	}
	return s
}
