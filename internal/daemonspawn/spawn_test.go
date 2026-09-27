package daemonspawn

import (
	"os"
	"path/filepath"
	"testing"
)

// A non-existent binary fails at Start, and the stderr log is still created in
// Home — proving Home is created and used as the working directory.
func TestStart_MissingBinary_ErrorsAndCreatesHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), "h")
	_, err := Start(Spec{Quild: filepath.Join(home, "no-such-quild"), Home: home})
	if err == nil {
		t.Fatal("Start succeeded with a missing binary")
	}
	if _, statErr := os.Stat(filepath.Join(home, "quild.stderr.log")); statErr != nil {
		t.Errorf("stderr log not created in home: %v", statErr)
	}
}
