package daemon

import (
	"fmt"
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/ringbuf"
)

func TestScanPaneMatches(t *testing.T) {
	for _, tc := range []struct {
		name      string
		raw       string
		lowerTerm string
		wantN     int
		wantExc   string
		wantTrunc bool
	}{
		{"no match", "hello world\nfoo bar\n", "zzz", 0, "", false},
		{"single", "alpha\nconnection refused\nbeta\n", "refused", 1, "connection refused", false},
		{"case insensitive", "ERROR: Connection Refused now\n", "refused", 1, "ERROR: Connection Refused now", false},
		{"excerpt is last match", "err one\nmid\nerr two\n", "err", 2, "err two", false},
		{"whitespace collapsed", "a\t\t err   here \n", "err", 1, "a err here", false},
		{"ansi stripped", "\x1b[31mred error\x1b[0m line\n", "error", 1, "red error line", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, exc, trunc := scanPaneMatches([]byte(tc.raw), tc.lowerTerm)
			if n != tc.wantN {
				t.Errorf("matches = %d, want %d", n, tc.wantN)
			}
			if exc != tc.wantExc {
				t.Errorf("excerpt = %q, want %q", exc, tc.wantExc)
			}
			if trunc != tc.wantTrunc {
				t.Errorf("truncated = %v, want %v", trunc, tc.wantTrunc)
			}
		})
	}
}

func TestScanPaneMatches_Cap(t *testing.T) {
	var sb []byte
	total := maxPaneMatches + 50
	for i := 0; i < total; i++ {
		sb = append(sb, []byte(fmt.Sprintf("needle line %d\n", i))...)
	}
	n, exc, trunc := scanPaneMatches(sb, "needle")
	if n != maxPaneMatches || !trunc {
		t.Errorf("cap: matches=%d truncated=%v, want %d,true", n, trunc, maxPaneMatches)
	}
	wantExc := fmt.Sprintf("needle line %d", total-1)
	if exc != wantExc {
		t.Errorf("excerpt = %q, want %q (must be the most-recent match, not the one at the cap)", exc, wantExc)
	}
}

func TestSearchPanes_AcrossTabs(t *testing.T) {
	d := newTestDaemon(t)
	mkPane := func(id, tabID, content string) *Pane {
		p := &Pane{ID: id, TabID: tabID, Type: "terminal", OutputBuf: ringbuf.NewRingBuffer(8192)}
		p.OutputBuf.Write([]byte(content))
		return p
	}
	d.session.RestoreTab(
		&Tab{ID: "tab-0000000a", Name: "A", Panes: []string{"pane-0000000a"}},
		[]*Pane{mkPane("pane-0000000a", "tab-0000000a", "boot ok\nconnection refused twice\nconnection refused\n")},
	)
	d.session.RestoreTab(
		&Tab{ID: "tab-0000000b", Name: "B", Panes: []string{"pane-0000000b"}},
		[]*Pane{mkPane("pane-0000000b", "tab-0000000b", "all good here\n")},
	)

	hits, trunc := d.searchPanes("refused")
	if trunc {
		t.Errorf("truncated = true, want false")
	}
	if len(hits) != 1 {
		t.Fatalf("hits = %d, want 1", len(hits))
	}
	if hits[0].PaneID != "pane-0000000a" || hits[0].Matches != 2 {
		t.Errorf("hit = %+v, want pane-0000000a x2", hits[0])
	}
	if hits[0].Excerpt != "connection refused" {
		t.Errorf("excerpt = %q, want last match", hits[0].Excerpt)
	}
}

func TestSearchPanes_SortsByMatchCountThenPaneID(t *testing.T) {
	d := newTestDaemon(t)
	mkPane := func(id, tabID, content string) *Pane {
		p := &Pane{ID: id, TabID: tabID, Type: "terminal", OutputBuf: ringbuf.NewRingBuffer(8192)}
		p.OutputBuf.Write([]byte(content))
		return p
	}
	// pane-0000000z has the most matches, so it must sort first even though its
	// ID is lexicographically last. pane-0000000a and pane-0000000b tie on match
	// count, so the lower pane ID must come first between them.
	d.session.RestoreTab(
		&Tab{ID: "tab-0000000d", Name: "D", Panes: []string{"pane-0000000z", "pane-0000000b", "pane-0000000a"}},
		[]*Pane{
			mkPane("pane-0000000z", "tab-0000000d", "err\nerr\nerr\n"),
			mkPane("pane-0000000b", "tab-0000000d", "err\n"),
			mkPane("pane-0000000a", "tab-0000000d", "err\n"),
		},
	)

	hits, trunc := d.searchPanes("err")
	if trunc {
		t.Errorf("truncated = true, want false")
	}
	if len(hits) != 3 {
		t.Fatalf("hits = %d, want 3: %+v", len(hits), hits)
	}
	wantOrder := []string{"pane-0000000z", "pane-0000000a", "pane-0000000b"}
	for i, want := range wantOrder {
		if hits[i].PaneID != want {
			t.Errorf("hits[%d].PaneID = %q, want %q (full order: %+v)", i, hits[i].PaneID, want, hits)
		}
	}
	if hits[0].Matches != 3 {
		t.Errorf("hits[0].Matches = %d, want 3", hits[0].Matches)
	}
}

// TestSearchPanes_TruncatedIsPerHit pins that the cap flag rides the HIT, not
// just the payload: a pane with an honest, uncapped count must never be
// labelled "capped" because some other pane in the same result set was.
func TestSearchPanes_TruncatedIsPerHit(t *testing.T) {
	d := newTestDaemon(t)
	var capped []byte
	for i := 0; i < maxPaneMatches+5; i++ {
		capped = append(capped, []byte(fmt.Sprintf("needle %d\n", i))...)
	}
	mkPane := func(id, tabID string, content []byte) *Pane {
		p := &Pane{ID: id, TabID: tabID, Type: "terminal", OutputBuf: ringbuf.NewRingBuffer(1 << 16)}
		p.OutputBuf.Write(content)
		return p
	}
	d.session.RestoreTab(
		&Tab{ID: "tab-0000000e", Name: "E", Panes: []string{"pane-0000000n", "pane-0000000m"}},
		[]*Pane{
			mkPane("pane-0000000n", "tab-0000000e", capped),
			mkPane("pane-0000000m", "tab-0000000e", []byte("needle once\n")),
		},
	)

	hits, truncated := d.searchPanes("needle")
	if !truncated {
		t.Error("payload-level truncated should be set when any pane capped")
	}
	if len(hits) != 2 {
		t.Fatalf("hits = %d, want 2: %+v", len(hits), hits)
	}
	if hits[0].PaneID != "pane-0000000n" || !hits[0].Truncated {
		t.Errorf("capped pane hit = %+v, want pane-0000000n with Truncated=true", hits[0])
	}
	if hits[1].PaneID != "pane-0000000m" || hits[1].Truncated {
		t.Errorf("uncapped pane hit = %+v, want pane-0000000m with Truncated=false", hits[1])
	}
}

// TestPaneSearchResponse_EchoesQueryVerbatim pins the daemon half of the
// staleness seam. The TUI compares the echoed query against its own
// DELIBERATELY UNTRIMMED search term ("/ foo" searches for " foo"), so any
// normalization here makes a whitespace-bearing term look permanently stale:
// applyPaneSearch drops every response and the palette hangs on "Searching…"
// forever. searchPanes still trims internally for matching — that must not leak
// into the echo. The TUI half is TestApplyPaneSearch_AcceptsDaemonEcho
// (internal/tui), over the same terms.
func TestPaneSearchResponse_EchoesQueryVerbatim(t *testing.T) {
	d := newTestDaemon(t)
	p := &Pane{ID: "pane-0000000f", TabID: "tab-0000000f", Type: "terminal", OutputBuf: ringbuf.NewRingBuffer(8192)}
	p.OutputBuf.Write([]byte("boot ok\nconnection refused twice\ntwo words here\n"))
	d.session.RestoreTab(
		&Tab{ID: "tab-0000000f", Name: "F", Panes: []string{"pane-0000000f"}},
		[]*Pane{p},
	)

	for _, q := range []string{"refused", " refused", "refused ", "two words"} {
		t.Run(fmt.Sprintf("%q", q), func(t *testing.T) {
			msg, err := ipc.NewMessage(ipc.MsgPaneSearchReq, ipc.PaneSearchReqPayload{Query: q})
			if err != nil {
				t.Fatalf("NewMessage: %v", err)
			}
			resp := d.paneSearchResponse(msg)
			if resp.Query != q {
				t.Errorf("echoed Query = %q, want %q verbatim (the TUI drops anything else as stale)", resp.Query, q)
			}
			if len(resp.Hits) != 1 {
				t.Errorf("hits = %d, want 1 — internal trimming must still match", len(resp.Hits))
			}
		})
	}
}

func TestSearchPanes_EmptyQuery(t *testing.T) {
	d := newTestDaemon(t)
	if hits, _ := d.searchPanes("   "); hits != nil {
		t.Errorf("blank query should yield no hits, got %+v", hits)
	}
}

func TestSearchPanes_SkipsNilBuffer(t *testing.T) {
	d := newTestDaemon(t)
	d.session.RestoreTab(
		&Tab{ID: "tab-0000000c", Name: "C", Panes: []string{"pane-0000000c"}},
		[]*Pane{{ID: "pane-0000000c", TabID: "tab-0000000c", Type: "terminal"}}, // no OutputBuf
	)
	if hits, _ := d.searchPanes("anything"); len(hits) != 0 {
		t.Errorf("nil OutputBuf must be skipped, got %+v", hits)
	}
}
