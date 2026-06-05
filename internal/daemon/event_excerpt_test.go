package daemon

import (
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/ringbuf"
)

func TestPaneOutputExcerpt_LastNLines(t *testing.T) {
	pane := &Pane{OutputBuf: ringbuf.NewRingBuffer(4096)}
	pane.OutputBuf.Write([]byte("first\nsecond\nthird\nfourth\n"))

	got := paneOutputExcerpt(pane, 2)
	want := "third\nfourth"
	if got != want {
		t.Errorf("paneOutputExcerpt: got %q, want %q", got, want)
	}
}

func TestPaneOutputExcerpt_SkipsEmptyLines(t *testing.T) {
	pane := &Pane{OutputBuf: ringbuf.NewRingBuffer(4096)}
	pane.OutputBuf.Write([]byte("alpha\n\n\nbeta\n\n"))

	got := paneOutputExcerpt(pane, 2)
	want := "alpha\nbeta"
	if got != want {
		t.Errorf("paneOutputExcerpt skip empty: got %q, want %q", got, want)
	}
}

func TestPaneOutputExcerpt_StripsANSI(t *testing.T) {
	pane := &Pane{OutputBuf: ringbuf.NewRingBuffer(4096)}
	// Red "error" + reset, then a plain line.
	pane.OutputBuf.Write([]byte("\x1b[31merror\x1b[0m\nokay\n"))

	got := paneOutputExcerpt(pane, 2)
	want := "error\nokay"
	if got != want {
		t.Errorf("paneOutputExcerpt ANSI strip: got %q, want %q", got, want)
	}
}

func TestPaneOutputExcerpt_EmptyBuffer(t *testing.T) {
	pane := &Pane{OutputBuf: ringbuf.NewRingBuffer(4096)}
	if got := paneOutputExcerpt(pane, 3); got != "" {
		t.Errorf("paneOutputExcerpt empty: got %q, want %q", got, "")
	}
}

func TestPaneOutputExcerpt_NilBuffer(t *testing.T) {
	pane := &Pane{}
	if got := paneOutputExcerpt(pane, 3); got != "" {
		t.Errorf("paneOutputExcerpt nil buf: got %q, want %q", got, "")
	}
}

func TestPaneOutputExcerpt_NilPane(t *testing.T) {
	if got := paneOutputExcerpt(nil, 3); got != "" {
		t.Errorf("paneOutputExcerpt nil pane: got %q, want %q", got, "")
	}
}

func TestPaneOutputExcerpt_LargeBufferTrailingCap(t *testing.T) {
	pane := &Pane{OutputBuf: ringbuf.NewRingBuffer(16384)}
	// Build > 4 KiB of "line N\n" — only the trailing window should be read.
	var sb strings.Builder
	for i := 0; i < 500; i++ {
		sb.WriteString("line ")
		sb.WriteString("padding-padding-padding-padding\n")
	}
	pane.OutputBuf.Write([]byte(sb.String()))

	got := paneOutputExcerpt(pane, 1)
	// We only care that something was returned and that we did not OOM on a
	// large buffer. The exact tail depends on the 4 KiB window boundary.
	if got == "" {
		t.Errorf("paneOutputExcerpt large buf: got empty")
	}
}

func TestWithExcerpt_PopulatesMessageAndData(t *testing.T) {
	e := PaneEvent{
		ID:    "evt-1",
		Type:  "process_exit",
		Title: "Process exited (code 0)",
		Data:  map[string]string{"exit_code": "0"},
	}
	got := withExcerpt(e, "last line of output")
	if got.Message != "last line of output" {
		t.Errorf("Message: got %q, want %q", got.Message, "last line of output")
	}
	if got.Data["excerpt"] != "last line of output" {
		t.Errorf("Data[excerpt]: got %q, want %q", got.Data["excerpt"], "last line of output")
	}
	if got.Data["exit_code"] != "0" {
		t.Errorf("preserved exit_code: got %q, want %q", got.Data["exit_code"], "0")
	}
}

func TestWithExcerpt_EmptyExcerptIsNoop(t *testing.T) {
	e := PaneEvent{
		ID:   "evt-1",
		Data: map[string]string{"exit_code": "0"},
	}
	got := withExcerpt(e, "")
	if got.Message != "" {
		t.Errorf("Message: got %q, want empty", got.Message)
	}
	if _, ok := got.Data["excerpt"]; ok {
		t.Errorf("Data[excerpt] should not be set on empty excerpt")
	}
	if got.Data["exit_code"] != "0" {
		t.Errorf("preserved exit_code: got %q, want %q", got.Data["exit_code"], "0")
	}
}

func TestWithExcerpt_NilData_CreatedOnDemand(t *testing.T) {
	e := PaneEvent{ID: "evt-1"}
	got := withExcerpt(e, "context")
	if got.Data == nil {
		t.Fatalf("Data: got nil, want allocated map")
	}
	if got.Data["excerpt"] != "context" {
		t.Errorf("Data[excerpt]: got %q, want %q", got.Data["excerpt"], "context")
	}
}
