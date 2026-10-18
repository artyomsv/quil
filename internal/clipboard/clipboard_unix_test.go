//go:build linux || darwin || freebsd

package clipboard

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// These drive runImageHelper with `sh` standing in for osascript/xclip, so
// the timeout, size cap, kill and stderr classification run for real.

// quick is the bound a stopped helper must return within: the kill itself is
// immediate, plus helperWaitDelay for a grandchild still holding the pipe.
const quick = helperWaitDelay + 2*time.Second

func TestRunImageHelper_ReturnsStdout(t *testing.T) {
	t.Parallel()
	out, err := runImageHelper(5*time.Second, 1024, "sh", "-c", "printf 'img-bytes'")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if string(out) != "img-bytes" {
		t.Errorf("out = %q, want %q", out, "img-bytes")
	}
}

func TestRunImageHelper_NoImageStderrIsErrNoImage(t *testing.T) {
	t.Parallel()
	for _, script := range []string{
		"echo 'Error: target image/png not available' >&2; exit 1",
		"echo \"0:29: execution error: Can't make some data into the expected type. (-1700)\" >&2; exit 1",
	} {
		_, err := runImageHelper(5*time.Second, 1024, "sh", "-c", script)
		if !errors.Is(err, ErrNoImage) {
			t.Errorf("%q: err = %v, want ErrNoImage", script, err)
		}
	}
}

func TestRunImageHelper_OtherFailureIsAnError(t *testing.T) {
	t.Parallel()
	_, err := runImageHelper(5*time.Second, 1024, "sh", "-c", "echo 'cannot open display' >&2; exit 3")
	if err == nil || errors.Is(err, ErrNoImage) {
		t.Fatalf("err = %v, want a real error", err)
	}
	if !strings.Contains(err.Error(), "cannot open display") {
		t.Errorf("err = %v, want the helper's stderr in it", err)
	}
}

func TestRunImageHelper_StalledHelperTimesOut(t *testing.T) {
	t.Parallel()
	// `sleep 30; :` makes sh fork sleep rather than exec it, so the
	// grandchild keeps stdout open after sh is killed — the WaitDelay case.
	start := time.Now()
	_, err := runImageHelper(100*time.Millisecond, 1024, "sh", "-c", "sleep 30; :")
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v, want a timeout", err)
	}
	if d := time.Since(start); d > quick {
		t.Errorf("returned after %s, want under %s", d, quick)
	}
}

func TestRunImageHelper_OversizedOutputStopsTheHelper(t *testing.T) {
	t.Parallel()
	// `yes` never ends on its own: only the cap's cancel can stop it before
	// the 30 s timeout.
	start := time.Now()
	_, err := runImageHelper(30*time.Second, 4096, "yes")
	if !errors.Is(err, errImageTooLarge) {
		t.Fatalf("err = %v, want errImageTooLarge", err)
	}
	if d := time.Since(start); d > quick {
		t.Errorf("returned after %s, want under %s", d, quick)
	}
}

func TestRunImageHelper_OutputAtTheLimitIsAccepted(t *testing.T) {
	t.Parallel()
	out, err := runImageHelper(5*time.Second, 8, "sh", "-c", "printf '12345678'")
	if err != nil || len(out) != 8 {
		t.Fatalf("out = %q, err = %v; want 8 bytes and no error", out, err)
	}
}
