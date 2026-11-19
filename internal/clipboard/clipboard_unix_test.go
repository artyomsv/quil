//go:build linux || darwin || freebsd

package clipboard

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
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
		"echo 'Nothing is copied' >&2; exit 1",
		"echo 'Clipboard content is not available as requested type \"image/png\"' >&2; exit 1",
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
	// `sleep 30; :` makes sh fork sleep rather than exec it, so a grandchild
	// holds stdout. The group kill stops it with sh; WaitDelay is the
	// backstop should it ever survive.
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

// fakeLookPath resolves only the named programs, each to /usr/bin/<name>.
func fakeLookPath(installed ...string) func(string) (string, error) {
	return func(name string) (string, error) {
		if slices.Contains(installed, name) {
			return "/usr/bin/" + name, nil
		}
		return "", exec.ErrNotFound
	}
}

func fakeEnv(env map[string]string) func(string) string {
	return func(k string) string { return env[k] }
}

func TestPickClipTool(t *testing.T) {
	t.Parallel()
	wayland := map[string]string{"WAYLAND_DISPLAY": "wayland-0", "DISPLAY": ":1"}
	x11 := map[string]string{"DISPLAY": ":0"}
	wl := clipHelper{kind: clipWayland, copy: "/usr/bin/wl-copy", paste: "/usr/bin/wl-paste"}
	xc := clipHelper{kind: clipXclip, copy: "/usr/bin/xclip", paste: "/usr/bin/xclip"}
	xs := clipHelper{kind: clipXsel, copy: "/usr/bin/xsel", paste: "/usr/bin/xsel"}
	none := clipHelper{}
	tests := []struct {
		name                  string
		goos                  string
		env                   map[string]string
		installed             []string
		wantPrimary, wantBack clipHelper
	}{
		{"macOS ignores every Linux helper", "darwin", wayland, []string{"wl-copy", "wl-paste", "xclip"},
			clipHelper{kind: clipDarwin, copy: "pbcopy", paste: "pbpaste"}, none},
		{"Wayland prefers wl-clipboard, xclip as fallback", "linux", wayland, []string{"wl-copy", "wl-paste", "xclip", "xsel"}, wl, xc},
		{"Wayland with xsel as fallback", "linux", wayland, []string{"wl-copy", "wl-paste", "xsel"}, wl, xs},
		{"Wayland with no X helper has no fallback", "linux", wayland, []string{"wl-copy", "wl-paste"}, wl, none},
		{"Wayland without wl-clipboard uses xclip", "linux", wayland, []string{"xclip"}, xc, none},
		{"Wayland with only wl-copy is not wl-clipboard", "linux", wayland, []string{"wl-copy", "xsel"}, xs, none},
		{"Wayland with only wl-paste is not wl-clipboard", "linux", wayland, []string{"wl-paste", "xclip"}, xc, none},
		{"X11 keeps xclip even with wl-clipboard installed", "linux", x11, []string{"wl-copy", "wl-paste", "xclip"}, xc, none},
		{"X11 with only xsel", "linux", x11, []string{"xsel"}, xs, none},
		{"FreeBSD Wayland uses wl-clipboard", "freebsd", wayland, []string{"wl-copy", "wl-paste"}, wl, none},
		{"nothing installed", "linux", wayland, nil, none, none},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, f := pickClipTool(tt.goos, fakeEnv(tt.env), fakeLookPath(tt.installed...))
			if p != tt.wantPrimary || f != tt.wantBack {
				t.Errorf("pickClipTool = %+v, %+v; want %+v, %+v", p, f, tt.wantPrimary, tt.wantBack)
			}
		})
	}
}

func TestHelperOrder(t *testing.T) {
	t.Parallel()
	wl := clipHelper{kind: clipWayland, copy: "wl-copy", paste: "wl-paste"}
	xc := clipHelper{kind: clipXclip, copy: "xclip", paste: "xclip"}
	if got := helperOrder(xc, clipHelper{}, false); !slices.Equal(got, []clipHelper{xc}) {
		t.Errorf("no fallback: %+v", got)
	}
	if got := helperOrder(wl, xc, false); !slices.Equal(got, []clipHelper{wl, xc}) {
		t.Errorf("with fallback: %+v", got)
	}
	if got := helperOrder(wl, xc, true); !slices.Equal(got, []clipHelper{xc}) {
		t.Errorf("demoted: %+v", got)
	}
}

func TestClipArgv(t *testing.T) {
	t.Parallel()
	wl := clipHelper{kind: clipWayland, copy: "/usr/bin/wl-copy", paste: "/usr/bin/wl-paste"}
	xc := clipHelper{kind: clipXclip, copy: "/usr/bin/xclip", paste: "/usr/bin/xclip"}
	xs := clipHelper{kind: clipXsel, copy: "/usr/bin/xsel", paste: "/usr/bin/xsel"}
	mac := clipHelper{kind: clipDarwin, copy: "pbcopy", paste: "pbpaste"}
	tests := []struct {
		name  string
		h     clipHelper
		read  []string
		write []string
		image []string // nil: no image reader
	}{
		{"wayland", wl,
			[]string{"/usr/bin/wl-paste", "--no-newline", "--type", "text"},
			[]string{"/usr/bin/wl-copy", "--type", "text/plain;charset=utf-8"},
			[]string{"/usr/bin/wl-paste", "--type", "image/png"}},
		{"xclip", xc,
			[]string{"/usr/bin/xclip", "-selection", "clipboard", "-o"},
			[]string{"/usr/bin/xclip", "-selection", "clipboard"},
			[]string{"/usr/bin/xclip", "-selection", "clipboard", "-t", "image/png", "-o"}},
		{"xsel", xs,
			[]string{"/usr/bin/xsel", "--clipboard", "--output"},
			[]string{"/usr/bin/xsel", "--clipboard", "--input"},
			nil},
		{"darwin", mac, []string{"pbpaste"}, []string{"pbcopy"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, err := textReadArgv(tt.h); err != nil || !slices.Equal(got, tt.read) {
				t.Errorf("textReadArgv = %q, %v; want %q", got, err, tt.read)
			}
			if got, err := textWriteArgv(tt.h); err != nil || !slices.Equal(got, tt.write) {
				t.Errorf("textWriteArgv = %q, %v; want %q", got, err, tt.write)
			}
			got, ok := imageReadArgv(tt.h)
			if ok != (tt.image != nil) || !slices.Equal(got, tt.image) {
				t.Errorf("imageReadArgv = %q, %v; want %q", got, ok, tt.image)
			}
		})
	}
}

func TestClipArgv_NoToolIsAnError(t *testing.T) {
	t.Parallel()
	if _, err := textReadArgv(clipHelper{}); !errors.Is(err, errNoClipTool) {
		t.Errorf("textReadArgv err = %v, want errNoClipTool", err)
	}
	if _, err := textWriteArgv(clipHelper{}); !errors.Is(err, errNoClipTool) {
		t.Errorf("textWriteArgv err = %v, want errNoClipTool", err)
	}
	if _, ok := imageReadArgv(clipHelper{}); ok {
		t.Error("imageReadArgv ok = true with no tool")
	}
}

// The tests below drive readText / writeText / readImageFrom with shell
// scripts standing in for wl-paste / wl-copy / xclip, so the fallback rules,
// timeouts and pipe handling run against real processes. Each script ignores
// the helper flags it is given. They are not parallel: writing an
// executable while another test forks can fail the exec with ETXTBSY.

// fakeHelper writes an executable sh script and returns its path.
func fakeHelper(t *testing.T, script string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "helper")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func waylandFake(t *testing.T, script string) clipHelper {
	p := fakeHelper(t, script)
	return clipHelper{kind: clipWayland, copy: p, paste: p}
}

func xclipFake(t *testing.T, script string) clipHelper {
	p := fakeHelper(t, script)
	return clipHelper{kind: clipXclip, copy: p, paste: p}
}

func TestReadText_FallsBackWhenWaylandFails(t *testing.T) {
	var health helperHealth
	hs := []clipHelper{
		waylandFake(t, "echo 'Failed to connect to a Wayland server' >&2; exit 1"),
		xclipFake(t, "printf 'from-x\\n'"),
	}
	got, err := readText(hs, &health, 5*time.Second)
	if err != nil || got != "from-x" {
		t.Fatalf("readText = %q, %v; want %q", got, err, "from-x")
	}
	if health.demoted.Load() {
		t.Error("a fast failure demoted wl-clipboard; only a timeout should")
	}
}

func TestReadText_NoFallbackWhenWaylandHasNoText(t *testing.T) {
	// The X clipboard may hold a stale copy: an answer from wl-paste, even
	// "nothing of that type", must never be second-guessed by xclip.
	marker := filepath.Join(t.TempDir(), "x-ran")
	for _, msg := range []string{
		`Clipboard content is not available as requested type "text"`,
		"Nothing is copied",
	} {
		var health helperHealth
		hs := []clipHelper{
			waylandFake(t, "echo '"+msg+"' >&2; exit 1"),
			xclipFake(t, "touch '"+marker+"'; printf stale"),
		}
		got, err := readText(hs, &health, 5*time.Second)
		if err != nil || got != "" {
			t.Errorf("%s: readText = %q, %v; want empty", msg, got, err)
		}
		if _, err := os.Stat(marker); err == nil {
			t.Fatalf("%s: the X fallback ran", msg)
		}
	}
}

func TestReadText_TimeoutFallsBackAndDemotes(t *testing.T) {
	var health helperHealth
	hs := []clipHelper{
		waylandFake(t, "sleep 30; :"),
		xclipFake(t, "printf from-x"),
	}
	start := time.Now()
	got, err := readText(hs, &health, 200*time.Millisecond)
	if err != nil || got != "from-x" {
		t.Fatalf("readText = %q, %v; want %q", got, err, "from-x")
	}
	if d := time.Since(start); d > quick {
		t.Errorf("returned after %s, want under %s", d, quick)
	}
	if !health.demoted.Load() {
		t.Error("a wl-clipboard timeout did not demote it")
	}
}

func TestReadText_LastHelperExitErrorReadsEmpty(t *testing.T) {
	// Quil has always read a failing helper as an empty clipboard; the CWD
	// dialogs would otherwise show the helper's error inline.
	var health helperHealth
	hs := []clipHelper{xclipFake(t, "echo 'Error: Can not open display' >&2; exit 1")}
	got, err := readText(hs, &health, 5*time.Second)
	if err != nil || got != "" {
		t.Fatalf("readText = %q, %v; want empty and no error", got, err)
	}
}

func TestReadText_StalledHelperIsAnError(t *testing.T) {
	var health helperHealth
	hs := []clipHelper{xclipFake(t, "sleep 30; :")}
	start := time.Now()
	_, err := readText(hs, &health, 100*time.Millisecond)
	if !errors.Is(err, errHelperTimedOut) {
		t.Fatalf("err = %v, want a timeout", err)
	}
	if d := time.Since(start); d > quick {
		t.Errorf("returned after %s, want under %s", d, quick)
	}
}

func TestReadText_OversizedTextStopsTheHelper(t *testing.T) {
	// Before the cap stopped the helper, reading stopped at maxReadSize and
	// Wait then blocked forever on a helper stuck writing to a full pipe.
	var health helperHealth
	hs := []clipHelper{xclipFake(t, "exec yes")}
	start := time.Now()
	_, err := readText(hs, &health, 30*time.Second)
	if !errors.Is(err, errTextTooLarge) {
		t.Fatalf("err = %v, want errTextTooLarge", err)
	}
	if d := time.Since(start); d > quick {
		t.Errorf("returned after %s, want under %s", d, quick)
	}
}

func TestWriteText_FallsBackWhenWaylandFails(t *testing.T) {
	var health helperHealth
	dir := t.TempDir()
	wlOut, xOut := filepath.Join(dir, "wl"), filepath.Join(dir, "x")
	hs := []clipHelper{
		waylandFake(t, "cat >'"+wlOut+"'; exit 1"),
		xclipFake(t, "cat >'"+xOut+"'"),
	}
	if err := writeText(hs, &health, "copied", 5*time.Second); err != nil {
		t.Fatalf("writeText: %v", err)
	}
	b, err := os.ReadFile(xOut)
	if err != nil {
		t.Fatalf("read what the fallback wrote: %v", err)
	}
	if string(b) != "copied" {
		t.Errorf("fallback got %q, want %q", b, "copied")
	}
}

func TestWriteText_NoFallbackWhenWaylandSucceeds(t *testing.T) {
	var health helperHealth
	dir := t.TempDir()
	wlOut, xOut := filepath.Join(dir, "wl"), filepath.Join(dir, "x")
	hs := []clipHelper{
		waylandFake(t, "cat >'"+wlOut+"'"),
		xclipFake(t, "cat >'"+xOut+"'"),
	}
	if err := writeText(hs, &health, "copied", 5*time.Second); err != nil {
		t.Fatalf("writeText: %v", err)
	}
	b, err := os.ReadFile(wlOut)
	if err != nil {
		t.Fatalf("read what the wl-copy wrote: %v", err)
	}
	if string(b) != "copied" {
		t.Errorf("wl-copy got %q, want %q", b, "copied")
	}
	if _, err := os.Stat(xOut); err == nil {
		t.Error("the X fallback ran after wl-copy succeeded")
	}
}

func TestWriteText_BackgroundChildDoesNotStallTheCopy(t *testing.T) {
	// wl-copy and xclip leave a child behind serving the selection, and
	// wl-copy's keeps stderr. A captured stderr would hold the copy for
	// helperWaitDelay and then fail it.
	var health helperHealth
	hs := []clipHelper{waylandFake(t, "cat >/dev/null; sleep 5 & exit 0")}
	start := time.Now()
	if err := writeText(hs, &health, "copied", 5*time.Second); err != nil {
		t.Fatalf("writeText: %v", err)
	}
	if d := time.Since(start); d >= helperWaitDelay {
		t.Errorf("returned after %s, want well under helperWaitDelay (%s)", d, helperWaitDelay)
	}
}

func TestReadImageFrom_Fallback(t *testing.T) {
	png := "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"
	var health helperHealth
	hs := []clipHelper{
		waylandFake(t, "echo 'Failed to connect to a Wayland server' >&2; exit 1"),
		xclipFake(t, "printf '"+png+"'"),
	}
	got, err := readImageFrom(hs, &health, 5*time.Second)
	if err != nil || !bytes.HasPrefix(got, pngMagic) {
		t.Fatalf("readImageFrom = %q, %v; want the fallback's PNG", got, err)
	}

	marker := filepath.Join(t.TempDir(), "x-ran")
	hs = []clipHelper{
		waylandFake(t, `echo 'Clipboard content is not available as requested type "image/png"' >&2; exit 1`),
		xclipFake(t, "touch '"+marker+"'; printf '"+png+"'"),
	}
	if _, err := readImageFrom(hs, &health, 5*time.Second); !errors.Is(err, ErrNoImage) {
		t.Errorf("err = %v, want ErrNoImage", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("the X fallback ran after wl-paste reported no image")
	}
}

// pidAlive reports whether pid still exists, waiting up to 2s for it to go:
// a killed process lingers as a zombie until its new parent reaps it.
func pidAlive(pid int) bool {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
	return true
}

// readPID reads the pid a fake helper wrote to path.
func readPID(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read child pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatalf("child pid %q: %v", b, err)
	}
	return pid
}

// wl-paste forks cat to read the clipboard owner's pipe and waits for it.
// When the owner never writes, a timeout must stop cat too, not only
// wl-paste: a cat left blocked is one more stray process per paste.
func TestRead_TimeoutKillsTheReadersChildren(t *testing.T) {
	for name, read := range map[string]func(clipHelper) error{
		"text": func(h clipHelper) error {
			var health helperHealth
			_, err := readText([]clipHelper{h}, &health, 200*time.Millisecond)
			return err
		},
		"image": func(h clipHelper) error {
			var health helperHealth
			_, err := readImageFrom([]clipHelper{h}, &health, 200*time.Millisecond)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			pidFile := filepath.Join(t.TempDir(), "child")
			// The child stands in for cat: blocked, holding nothing we wait on.
			h := waylandFake(t, "sleep 30 </dev/null >/dev/null 2>&1 & echo $! >'"+pidFile+"'; wait")
			start := time.Now()
			if err := read(h); !errors.Is(err, errHelperTimedOut) {
				t.Fatalf("err = %v, want a timeout", err)
			}
			if d := time.Since(start); d > quick {
				t.Errorf("returned after %s, want under %s", d, quick)
			}
			pid := readPID(t, pidFile)
			if pidAlive(pid) {
				_ = syscall.Kill(pid, syscall.SIGKILL) // not to leak it past the test
				t.Errorf("the reader's child %d outlived the timeout", pid)
			}
		})
	}
}

// A copy helper's child serves the selection after the helper exits, so
// nothing may stop it: only reads are run and killed as a group.
func TestWriteText_CopyHelpersChildOutlivesIt(t *testing.T) {
	var health helperHealth
	pidFile := filepath.Join(t.TempDir(), "child")
	hs := []clipHelper{waylandFake(t, "cat >/dev/null; sleep 30 </dev/null >/dev/null 2>&1 & echo $! >'"+pidFile+"'; exit 0")}
	if err := writeText(hs, &health, "copied", 5*time.Second); err != nil {
		t.Fatalf("writeText: %v", err)
	}
	pid := readPID(t, pidFile)
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	if err := syscall.Kill(pid, 0); err != nil {
		t.Errorf("the copy helper's child %d is gone (%v); it must keep serving the selection", pid, err)
	}
}

// With nothing to fall back on (wl-clipboard on GNOME without xclip / xsel),
// a timeout must not be paid again on every copy and paste: calls fail at
// once for helperRetryAfter, then one tries again.
func TestTimeoutWithNoFallback_FailsFastThenRetries(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	health := helperHealth{now: func() time.Time { return now }}
	runs := filepath.Join(t.TempDir(), "runs")
	stalled := []clipHelper{waylandFake(t, "echo run >>'"+runs+"'; sleep 30; :")}
	countRuns := func() int {
		b, _ := os.ReadFile(runs) // absent until the first run: zero runs
		return strings.Count(string(b), "run")
	}

	if _, err := readText(stalled, &health, 100*time.Millisecond); !errors.Is(err, errHelperTimedOut) {
		t.Fatalf("first read: err = %v, want a timeout", err)
	}
	start := time.Now()
	_, err := readText(stalled, &health, 100*time.Millisecond)
	if !errors.Is(err, errHelperStalled) {
		t.Fatalf("second read: err = %v, want errHelperStalled", err)
	}
	if err := writeText(stalled, &health, "x", 100*time.Millisecond); !errors.Is(err, errHelperStalled) {
		t.Errorf("a copy during the stall: err = %v, want errHelperStalled", err)
	}
	if d := time.Since(start); d > 50*time.Millisecond || countRuns() != 1 {
		t.Errorf("stalled calls took %s and ran the helper %d times; want at once and once", d, countRuns())
	}

	now = now.Add(helperRetryAfter + time.Second)
	if _, err := readText(stalled, &health, 100*time.Millisecond); !errors.Is(err, errHelperTimedOut) || countRuns() != 2 {
		t.Fatalf("after %s: err = %v, runs = %d; want one fresh try that times out", helperRetryAfter, err, countRuns())
	}

	now = now.Add(helperRetryAfter + time.Second)
	working := []clipHelper{waylandFake(t, "printf back")}
	if got, err := readText(working, &health, 5*time.Second); err != nil || got != "back" {
		t.Fatalf("recovered read = %q, %v", got, err)
	}
	if left := health.stalledFor(); left != 0 {
		t.Errorf("a success left calls stalled for %s", left)
	}
}

// waitForFile waits for a fake helper to create path, which is how a test
// knows the helper has started rather than guessing with a sleep.
func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("helper never created %s", path)
}

// When the stall expires, exactly one of the calls arriving together may try
// the helper again; the rest keep failing at once until it reports back.
func TestStall_OnlyOneCallRetriesAfterItExpires(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	health := helperHealth{now: func() time.Time { return now }}
	runs := filepath.Join(t.TempDir(), "runs")
	stalled := []clipHelper{waylandFake(t, "echo run >>'"+runs+"'; sleep 30; :")}
	countRuns := func() int {
		b, _ := os.ReadFile(runs) // absent until the first run: zero runs
		return strings.Count(string(b), "run")
	}
	if _, err := readText(stalled, &health, 100*time.Millisecond); !errors.Is(err, errHelperTimedOut) {
		t.Fatalf("setup: err = %v, want a timeout", err)
	}
	now = now.Add(helperRetryAfter + time.Second)

	const callers = 6
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := readText(stalled, &health, 300*time.Millisecond)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	var timedOut, refused int
	for err := range errs {
		switch {
		case errors.Is(err, errHelperTimedOut):
			timedOut++
		case errors.Is(err, errHelperStalled):
			refused++
		default:
			t.Errorf("unexpected err %v", err)
		}
	}
	if timedOut != 1 || refused != callers-1 || countRuns() != 2 {
		t.Errorf("retried %d, refused %d, helper runs %d; want 1 retry, %d refused, 2 runs", timedOut, refused, countRuns(), callers-1)
	}
	if health.stalledFor() == 0 {
		t.Error("the failed retry did not stall again")
	}
}

// A call that started before another call's timeout set the stall says
// nothing about the helper since then, so finishing must not clear it.
func TestStall_ACallStartedEarlierCannotClearIt(t *testing.T) {
	var health helperHealth
	dir := t.TempDir()
	started, release := filepath.Join(dir, "started"), filepath.Join(dir, "release")
	// Slow but working: it waits for the test's go-ahead, then answers.
	slow := []clipHelper{waylandFake(t, "touch '"+started+"'; while [ ! -e '"+release+"' ]; do sleep 0.01; done; printf ok")}
	stalled := []clipHelper{waylandFake(t, "sleep 30; :")}

	done := make(chan error, 1)
	go func() {
		_, err := readText(slow, &health, 10*time.Second)
		done <- err
	}()
	waitForFile(t, started) // the slow call has begun, before any stall
	if _, err := readText(stalled, &health, 100*time.Millisecond); !errors.Is(err, errHelperTimedOut) {
		t.Fatalf("stalling call: err = %v, want a timeout", err)
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("slow call: %v", err)
	}
	if health.stalledFor() == 0 {
		t.Error("a call that began before the stall cleared it when it finished")
	}
}
