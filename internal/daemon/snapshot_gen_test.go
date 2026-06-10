package daemon

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/artyomsv/quil/internal/config"
)

// TestSnapshot_SkipsUnchangedBuffers: a pane that produced no output since
// the last snapshot must not have its buffer file rewritten (~20 GB/day of
// identical bytes at defaults). Deleting the file between snapshots makes
// "skipped" directly observable.
func TestSnapshot_SkipsUnchangedBuffers(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())

	d := New(config.Default())
	tab := d.session.CreateTab("Shell")
	pane, err := d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pane.OutputBuf.Write(bytes.Repeat([]byte{'x'}, 1024))

	bufFile := filepath.Join(config.BufferDir(), pane.ID+".bin")

	d.snapshot()
	if _, err := os.Stat(bufFile); err != nil {
		t.Fatalf("first snapshot did not write buffer: %v", err)
	}

	// No writes since → second snapshot must skip this pane entirely.
	if err := os.Remove(bufFile); err != nil {
		t.Fatal(err)
	}
	d.snapshot()
	if _, err := os.Stat(bufFile); !os.IsNotExist(err) {
		t.Error("unchanged buffer was rewritten — generation skip not working")
	}

	// New output → third snapshot must write again.
	pane.OutputBuf.Write([]byte("more"))
	d.snapshot()
	if _, err := os.Stat(bufFile); err != nil {
		t.Errorf("changed buffer was not rewritten: %v", err)
	}
}

// TestSnapshot_PrunesDeadGenerations: snapGens entries for destroyed panes
// must not accumulate.
func TestSnapshot_PrunesDeadGenerations(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())

	d := New(config.Default())
	tab := d.session.CreateTab("Shell")
	d.session.CreateTab("Keep") // avoid auto-create paths interfering
	pane, err := d.session.CreatePane(tab.ID, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	pane.OutputBuf.Write([]byte("data"))
	d.snapshot()
	if _, ok := d.snapGens[pane.ID]; !ok {
		t.Fatal("snapshot did not record a generation for the live pane")
	}

	d.session.DestroyPane(pane.ID)
	d.snapshot()
	if _, ok := d.snapGens[pane.ID]; ok {
		t.Error("snapGens entry survived pane destruction — monotonic map growth")
	}
}
