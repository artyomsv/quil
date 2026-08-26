package main

import (
	"sync"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/proctree"
)

// The stat loop is the half of this feature with the lifecycle questions: it
// outlives the call that starts it, and its only exit is a send error. These
// pin the two properties that matter — it uses the DROPPABLE send, and it stops
// when the link is gone rather than spinning at a dead socket.

type fakeStatSender struct {
	mu   sync.Mutex
	msgs []*ipc.Message
	err  error
	// failAfter makes SendDroppable start returning err once this many
	// messages have been accepted.
	failAfter int
	done      chan struct{}
	closeOnce sync.Once
}

func (f *fakeStatSender) SendDroppable(m *ipc.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failAfter > 0 && len(f.msgs) >= f.failAfter {
		f.closeOnce.Do(func() { close(f.done) })
		return f.err
	}
	f.msgs = append(f.msgs, m)
	if f.failAfter == 0 && len(f.msgs) >= 2 {
		f.closeOnce.Do(func() { close(f.done) })
	}
	return nil
}

func (f *fakeStatSender) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.msgs)
}

func (f *fakeStatSender) first() *ipc.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.msgs) == 0 {
		return nil
	}
	return f.msgs[0]
}

// withFastTicks shortens the push interval for the duration of one test.
// statPushInterval is a var precisely so this is possible without spending five
// seconds per tick.
func withFastTicks(t *testing.T, d time.Duration) {
	t.Helper()
	prev := statPushInterval
	statPushInterval = d
	t.Cleanup(func() { statPushInterval = prev })
}

func TestStartClientStatReports_PushesOnEachTick(t *testing.T) {
	withFastTicks(t, 5*time.Millisecond)
	f := &fakeStatSender{done: make(chan struct{})}

	startClientStatReports(f)

	select {
	case <-f.done:
	case <-time.After(3 * time.Second):
		t.Fatalf("only %d stats pushed; the loop is not ticking", f.count())
	}

	msg := f.first()
	if msg == nil {
		t.Fatal("no message captured")
	}
	if msg.Type != ipc.MsgClientStat {
		t.Errorf("message type = %q, want %q", msg.Type, ipc.MsgClientStat)
	}
	var p ipc.ClientStatPayload
	if err := msg.DecodePayload(&p); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if p.RSSBytes == 0 {
		t.Error("RSSBytes = 0; this process occupies memory, so the read failed")
	}
}

// The exit condition. SendDroppable reports an error only for a conn that is
// genuinely gone — a merely-full queue drops the frame and returns nil — so an
// error means there is nothing left to push at.
func TestStartClientStatReports_StopsWhenTheLinkIsGone(t *testing.T) {
	withFastTicks(t, 5*time.Millisecond)
	f := &fakeStatSender{
		done:      make(chan struct{}),
		err:       ipc.ErrSendOverflow,
		failAfter: 2,
	}

	startClientStatReports(f)

	select {
	case <-f.done:
	case <-time.After(3 * time.Second):
		t.Fatal("sender never reached its failure point")
	}

	// Give the loop several more tick windows to misbehave.
	time.Sleep(100 * time.Millisecond)
	if n := f.count(); n != 2 {
		t.Errorf("accepted %d stats, want 2 — the loop kept pushing after the "+
			"link reported gone", n)
	}
}

func TestStartClientStatReports_NilSenderIsANoOp(t *testing.T) {
	withFastTicks(t, 5*time.Millisecond)
	startClientStatReports(nil) // must not panic
	time.Sleep(20 * time.Millisecond)
}

// A *ipc.Client must satisfy the narrow interface, or the production call site
// in sendClientHello would not compile — but a compile-time assertion states it
// where a reader can see it, and catches a signature drift in ipc.
func TestStatSender_IsSatisfiedByTheRealClient(t *testing.T) {
	var _ statSender = (*ipc.Client)(nil)
}

func TestSampleSelfStat_FirstReadingIsUnknownCPUNotZero(t *testing.T) {
	s := proctree.NewSelfSampler()

	p := sampleSelfStat(s, time.Now())

	if p.CPUPct >= 0 {
		t.Errorf("CPUPct = %v on a sampler's first reading, want the negative "+
			"unknown marker — 0 renders as \"0%%\" and claims the process is idle",
			p.CPUPct)
	}
	if p.RSSBytes == 0 {
		t.Error("RSSBytes = 0; RSS needs no baseline and should be valid immediately")
	}
}

// The platform half of this — that a primed sampler then yields a real
// percentage wherever a cumulative counter exists — is pinned by
// proctree.TestNewSelfSampler_ReadsThisProcess, which can assert it against the
// platform's own cpuIsSampled constant. Repeating it here would only be able to
// skip on the platform where it matters.
