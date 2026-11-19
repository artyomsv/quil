//go:build linux || darwin || freebsd

package clipboard

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/artyomsv/quil/internal/logger"
)

// maxClipboardSize limits clipboard reads to prevent excessive memory allocation.
const maxReadSize = 10 * 1024 * 1024 // 10 MB

// clipKind names the clipboard helper family detection settled on.
type clipKind int

const (
	clipNone clipKind = iota
	clipDarwin
	clipWayland
	clipXclip
	clipXsel
)

// clipHelper is the detected helper: its family plus the resolved paths of
// the programs that write (copy) and read (paste). xclip and xsel use one
// program for both; wl-clipboard ships two.
type clipHelper struct {
	kind  clipKind
	copy  string
	paste string
}

// errNoClipTool is returned when no clipboard helper was found.
var errNoClipTool = errors.New("no clipboard tool found (install wl-clipboard, xclip or xsel)")

// Cached clipboard tool detection - avoids repeated LookPath calls.
var (
	clipToolOnce sync.Once
	clipTool     clipHelper
	// clipFallback is the X helper tried when wl-clipboard fails (see
	// tryHelpers); clipNone when clipTool is not wl-clipboard or there is no
	// X helper.
	clipFallback clipHelper
	// clipHealth is what this process has learned about its helpers.
	clipHealth helperHealth
)

// helperRetryAfter is how long copy and paste fail at once after a helper
// timed out with nothing left to fall back on, before it is tried again.
const helperRetryAfter = 30 * time.Second

// errHelperStalled is returned, at once, while a helper that timed out with
// nothing to fall back on is not being retried (see helperHealth).
var errHelperStalled = errors.New("clipboard helper timed out recently")

// helperHealth is what this process has learned about its clipboard helpers
// from their timeouts. Each timeout costs the user the whole timeout, so it
// is paid once, not on every copy and paste.
type helperHealth struct {
	// demoted is set once wl-clipboard has timed out with an X helper to
	// fall back on. From then on the process goes straight to the fallback
	// (helperOrder).
	demoted atomic.Bool

	// The stall: set when the last helper to try timed out, so there was
	// nothing left to fall back on (wl-clipboard on a compositor without
	// data-control, GNOME's Mutter, that never gives it focus, with no xclip
	// / xsel installed). Until stalledUntil every call fails at once, as it
	// did before this host had a usable helper at all. After it, exactly one
	// call claims the retry (retrying) while the rest keep failing at once;
	// its timeout stalls again, any other result clears the stall.
	//
	// gen counts stall changes. A call records it when it starts, and only
	// the claimed retry may clear a stall, and only one no newer than its
	// claim: a call that started before a stall must not undo it when it
	// finishes, since it says nothing about the helper since then.
	mu           sync.Mutex
	stalledUntil time.Time // zero: not stalled
	retrying     bool
	gen          uint64

	// now is the clock; nil means time.Now. Tests set it.
	now func() time.Time
}

func (h *helperHealth) clock() time.Time {
	if h.now != nil {
		return h.now()
	}
	return time.Now()
}

// helperCall is one tryHelpers call's standing with helperHealth.
type helperCall struct {
	gen   uint64
	retry bool // this call claimed the retry after a stall
}

// begin admits a call, or refuses it while the helper is stalled or another
// call already holds the retry.
func (h *helperHealth) begin() (helperCall, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stalledUntil.IsZero() {
		return helperCall{gen: h.gen}, nil
	}
	if left := h.stalledUntil.Sub(h.clock()); left > 0 {
		return helperCall{}, fmt.Errorf("clipboard: %w; next try in %s", errHelperStalled, left.Round(time.Second))
	}
	if h.retrying {
		return helperCall{}, fmt.Errorf("clipboard: %w; a retry is running", errHelperStalled)
	}
	h.retrying = true
	return helperCall{gen: h.gen, retry: true}, nil
}

// end records how a call admitted by begin went: err is its final error.
func (h *helperHealth) end(c helperCall, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch {
	case errors.Is(err, errHelperTimedOut):
		h.stalledUntil = h.clock().Add(helperRetryAfter)
		h.retrying = false
		h.gen++
		logger.Warn("clipboard: %v and there is nothing to fall back on; copy and paste fail at once for %s", err, helperRetryAfter)
	case c.retry && c.gen == h.gen:
		h.stalledUntil = time.Time{}
		h.retrying = false
		h.gen++
	}
}

// stalledFor reports how long calls still fail at once, or 0.
func (h *helperHealth) stalledFor() time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stalledUntil.IsZero() {
		return 0
	}
	return max(h.stalledUntil.Sub(h.clock()), 0)
}

func detectClipTool() {
	clipTool, clipFallback = pickClipTool(runtime.GOOS, os.Getenv, exec.LookPath)
}

// sessionHelpers returns the helpers to try, in order.
func sessionHelpers() []clipHelper {
	clipToolOnce.Do(detectClipTool)
	return helperOrder(clipTool, clipFallback, clipHealth.demoted.Load())
}

// helperOrder is the order helpers are tried in: the session's helper, then
// the fallback when there is one, or only the fallback once demoted.
func helperOrder(primary, fallback clipHelper, demoted bool) []clipHelper {
	switch {
	case fallback.kind == clipNone:
		return []clipHelper{primary}
	case demoted:
		return []clipHelper{fallback}
	default:
		return []clipHelper{primary, fallback}
	}
}

// pickClipTool chooses the clipboard helper for this session, and the
// helper to fall back on when that one fails.
//
// A Wayland session (WAYLAND_DISPLAY set) prefers wl-copy / wl-paste. xclip
// and xsel only reach the Wayland clipboard through the compositor's
// XWayland bridge, which is not dependable: KWin, for one, syncs the
// Wayland selection into X only lazily, so an xclip read after copying in a
// native Wayland app returns nothing or stale text; and a compositor running
// without XWayland has no X clipboard at all. When wl-clipboard is not
// installed, xclip / xsel are still tried, which is what Quil did before.
// When it is, xclip / xsel (if installed) become the fallback, for a
// wl-clipboard that cannot reach the compositor or never gets focus.
//
// The choice follows the session, not the desktop: X11 desktops (XFCE, MATE,
// i3, ...) keep using xclip / xsel even when wl-clipboard is installed.
func pickClipTool(goos string, getenv func(string) string, lookPath func(string) (string, error)) (primary, fallback clipHelper) {
	if goos == "darwin" {
		return clipHelper{kind: clipDarwin, copy: "pbcopy", paste: "pbpaste"}, clipHelper{}
	}
	var x clipHelper
	if p, err := lookPath("xclip"); err == nil {
		x = clipHelper{kind: clipXclip, copy: p, paste: p}
	} else if p, err := lookPath("xsel"); err == nil {
		x = clipHelper{kind: clipXsel, copy: p, paste: p}
	}
	if getenv("WAYLAND_DISPLAY") != "" {
		c, cErr := lookPath("wl-copy")
		p, pErr := lookPath("wl-paste")
		if cErr == nil && pErr == nil {
			return clipHelper{kind: clipWayland, copy: c, paste: p}, x
		}
	}
	return x, clipHelper{}
}

// textReadArgv returns the command line that prints the clipboard text.
func textReadArgv(h clipHelper) ([]string, error) {
	switch h.kind {
	case clipDarwin:
		return []string{h.paste}, nil
	case clipWayland:
		// --no-newline: wl-paste otherwise appends a newline to text that
		// lacks one. --type text: any text type the owner offers; an
		// image-only clipboard then fails and reads as "" rather than
		// handing back the image bytes.
		return []string{h.paste, "--no-newline", "--type", "text"}, nil
	case clipXclip:
		return []string{h.paste, "-selection", "clipboard", "-o"}, nil
	case clipXsel:
		return []string{h.paste, "--clipboard", "--output"}, nil
	default:
		return nil, errNoClipTool
	}
}

// textWriteArgv returns the command line that sets the clipboard text from
// its stdin.
func textWriteArgv(h clipHelper) ([]string, error) {
	switch h.kind {
	case clipDarwin:
		return []string{h.copy}, nil
	case clipWayland:
		// An explicit type: left to infer, wl-copy may guess a non-text
		// type (text/html for markup-looking output, for one), which a
		// plain-text paste target then cannot read. A text type also makes
		// wl-copy offer the TEXT / STRING / UTF8_STRING aliases X apps ask
		// for.
		return []string{h.copy, "--type", "text/plain;charset=utf-8"}, nil
	case clipXclip:
		return []string{h.copy, "-selection", "clipboard"}, nil
	case clipXsel:
		return []string{h.copy, "--clipboard", "--input"}, nil
	default:
		return nil, errNoClipTool
	}
}

// imageReadArgv returns the command line that prints the clipboard image as
// PNG, or ok=false when the helper cannot ask for a type (xsel) or there is
// no Linux helper (macOS is handled separately, through osascript).
func imageReadArgv(h clipHelper) (argv []string, ok bool) {
	switch h.kind {
	case clipWayland:
		return []string{h.paste, "--type", "image/png"}, true
	case clipXclip:
		return []string{h.paste, "-selection", "clipboard", "-t", "image/png", "-o"}, true
	default:
		return nil, false
	}
}

// textHelperTimeout bounds one text read or write. Both normally take
// milliseconds. The bound is for wl-clipboard on a compositor without the
// data-control protocol (GNOME's Mutter): there wl-copy and wl-paste need
// keyboard focus, through a popup surface, and wait forever if the
// compositor never gives it. It also turns a helper stuck for any other
// reason into an error instead of a paste that never arrives.
const textHelperTimeout = 5 * time.Second

// imageReadTimeout bounds one clipboard image read. The pasting key waits on
// it, so a wedged helper must not hold the paste forever.
const imageReadTimeout = 10 * time.Second

// helperWaitDelay is how long Wait gives a killed helper's pipes to close. A
// grandchild that inherited stdout (a shell's forked child) would otherwise
// keep the read open after the helper itself is gone.
const helperWaitDelay = time.Second

// maxHelperStderr caps what a helper may write to stderr; only the "no image"
// message and an error's first line are ever read from it.
const maxHelperStderr = 64 * 1024

// errHelperTimedOut marks a helper stopped by its timeout.
var errHelperTimedOut = errors.New("timed out")

// errTextTooLarge reports clipboard text over maxReadSize.
var errTextTooLarge = errors.New("clipboard: text larger than 10 MB")

func read() (string, error) {
	return readText(sessionHelpers(), &clipHealth, textHelperTimeout)
}

func write(text string) error {
	return writeText(sessionHelpers(), &clipHealth, text, textHelperTimeout)
}

// readImage returns the clipboard image as PNG bytes (#261).
//
// macOS: `osascript -e 'the clipboard as «class PNGf»'` — ships with the OS,
// and AppleScript converts a TIFF-only clipboard to PNG itself. It prints a
// «data PNGf…» hex literal, decoded by decodeAppleScriptPNG. pbpaste cannot
// help: it only ever returns text.
//
// Linux and FreeBSD: `wl-paste --type image/png` in a Wayland session with
// wl-clipboard installed, else `xclip -selection clipboard -t image/png -o`
// (see pickClipTool). xsel has no way to ask for a target type, so an
// xsel-only host reports ErrNoImage.
func readImage() ([]byte, error) {
	hs := sessionHelpers()
	if hs[0].kind == clipDarwin {
		// Two hex digits per byte, plus the literal's framing.
		out, err := runImageHelper(imageReadTimeout, 2*maxImageSize+64,
			"osascript", "-e", "the clipboard as «class PNGf»")
		if err != nil {
			return nil, err
		}
		return decodeAppleScriptPNG(out)
	}
	return readImageFrom(hs, &clipHealth, imageReadTimeout)
}

// tryHelpers runs op with each helper in turn, moving to the next only when
// op reports that the helper itself failed (retry): it timed out, could not
// start, or exited with an error. Never when the helper answered, even with
// "the clipboard holds nothing of that type": an X fallback would then read
// the stale XWayland copy that wl-clipboard is there to avoid.
//
// Timeouts are remembered in health. A wl-clipboard timeout with a fallback
// demotes it, so later calls skip it (helperOrder). A timeout of the last
// helper, with nothing to fall back on, makes calls fail at once for
// helperRetryAfter instead of each one waiting out the timeout again.
func tryHelpers[T any](hs []clipHelper, health *helperHealth, op func(clipHelper) (v T, retry bool, err error)) (T, error) {
	var v T
	call, err := health.begin()
	if err != nil {
		return v, err
	}
	for i, h := range hs {
		var retry bool
		v, retry, err = op(h)
		if !retry || i == len(hs)-1 {
			break
		}
		next := filepath.Base(hs[i+1].paste)
		if h.kind == clipWayland && errors.Is(err, errHelperTimedOut) && health.demoted.CompareAndSwap(false, true) {
			logger.Warn("clipboard: %v; using %s from now on", err, next)
		} else {
			logger.Debug("clipboard: %v; trying %s", err, next)
		}
	}
	// A timeout here came from the last helper: a timeout always asks for
	// the next one, so the loop only stops on it when none is left.
	health.end(call, err)
	return v, err
}

// helperExitError is a text read whose helper exited with an error other
// than "nothing of that type". It lets tryHelpers move to the next helper;
// readText reports it as an empty clipboard, as Quil always has.
type helperExitError struct {
	name   string
	err    error
	stderr string
}

func (e *helperExitError) Error() string {
	return fmt.Sprintf("%s: %v: %s", e.name, e.err, e.stderr)
}

func (e *helperExitError) Unwrap() error { return e.err }

// readText reads the clipboard text through the first helper in hs that
// does not fail (see tryHelpers). A run that fails with a "nothing of that
// type" message reads as an empty clipboard. A helper that exits with any
// other error also reads as empty when no helper is left, as it always has:
// the paste then falls through to the image path. The two failures that
// used to hold the paste for good are errors instead: a timeout, and more
// than maxReadSize of text (the helper is stopped, not left blocked on a
// full pipe).
func readText(hs []clipHelper, health *helperHealth, timeout time.Duration) (string, error) {
	text, err := tryHelpers(hs, health, func(h clipHelper) (string, bool, error) {
		argv, err := textReadArgv(h)
		if err != nil {
			return "", false, err
		}
		name := filepath.Base(argv[0])
		r := runHelper(timeout, maxReadSize, nil, true, argv[0], argv[1:]...)
		var exitErr *exec.ExitError
		switch {
		case r.over:
			return "", false, errTextTooLarge
		case r.timedOut:
			return "", true, fmt.Errorf("clipboard: %s %w after %s", name, errHelperTimedOut, timeout)
		case r.err != nil && isTypeNotOfferedStderr(r.stderr):
			return "", false, nil
		case errors.As(r.err, &exitErr):
			return clipboardText(r.stdout), true, &helperExitError{name: name, err: r.err, stderr: strings.TrimSpace(r.stderr)}
		case r.err != nil:
			return "", true, fmt.Errorf("clipboard start: %w", r.err)
		}
		return clipboardText(r.stdout), false, nil
	})
	var exitErr *helperExitError
	if errors.As(err, &exitErr) {
		return text, nil
	}
	return text, err
}

// writeText sets the clipboard text through the first helper in hs that
// does not fail (see tryHelpers).
//
// The helper's stdout and stderr are not captured. wl-copy and xclip fork a
// child that stays behind to serve the selection, and that child keeps any
// pipe it inherited open (wl-copy points stdin and stdout at /dev/null but
// keeps stderr): capturing would stall every copy for helperWaitDelay and
// then fail it.
func writeText(hs []clipHelper, health *helperHealth, text string, timeout time.Duration) error {
	_, err := tryHelpers(hs, health, func(h clipHelper) (struct{}, bool, error) {
		argv, err := textWriteArgv(h)
		if err != nil {
			return struct{}{}, false, err
		}
		name := filepath.Base(argv[0])
		r := runHelper(timeout, 0, strings.NewReader(text), false, argv[0], argv[1:]...)
		switch {
		case r.timedOut:
			return struct{}{}, true, fmt.Errorf("clipboard: %s %w after %s", name, errHelperTimedOut, timeout)
		case r.err != nil:
			return struct{}{}, true, fmt.Errorf("clipboard: %s: %w", name, r.err)
		}
		return struct{}{}, false, nil
	})
	return err
}

// readImageFrom reads the clipboard image through the first helper in hs
// that does not fail (see tryHelpers). ErrNoImage and an oversized image are
// answers, not failures, so they never move on to the next helper.
func readImageFrom(hs []clipHelper, health *helperHealth, timeout time.Duration) ([]byte, error) {
	return tryHelpers(hs, health, func(h clipHelper) ([]byte, bool, error) {
		argv, ok := imageReadArgv(h)
		if !ok {
			return nil, false, ErrNoImage
		}
		out, err := runImageHelper(timeout, maxImageSize, argv[0], argv[1:]...)
		switch {
		case err == nil:
			png, err := checkPNG(out)
			return png, false, err
		case errors.Is(err, ErrNoImage), errors.Is(err, errImageTooLarge):
			return nil, false, err
		default:
			return nil, true, err
		}
	})
}

// cappedBuffer collects a helper's output up to limit bytes. Past the limit it
// cancels the helper's context, so oversized output ends the helper at once
// instead of leaving it blocked on a full pipe until the timeout.
type cappedBuffer struct {
	buf    bytes.Buffer
	limit  int
	over   bool
	cancel context.CancelFunc
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if c.buf.Len()+len(p) > c.limit {
		c.over = true
		c.cancel()
		return 0, errImageTooLarge
	}
	return c.buf.Write(p)
}

// helperRun is the outcome of one runHelper call.
type helperRun struct {
	stdout   []byte
	stderr   string
	err      error // from Start or Wait
	timedOut bool  // the run failed after its timeout fired
	over     bool  // stdout passed the limit and the helper was stopped
}

// runHelper runs one clipboard helper with stdin as its input (nil for
// none). It is bounded three ways: timeout, limit bytes of stdout, and
// helperWaitDelay for the pipes after a kill. With capture false, stdout and
// stderr go to /dev/null and limit is unused (see writeText for why).
//
// capture also marks a READ helper, which runs in its own process group and
// is stopped as a group: wl-paste forks cat to read the clipboard owner's
// pipe and waits for it, so killing wl-paste alone leaves cat blocked on an
// owner that never writes, one more each paste. A copy helper (capture
// false) is left alone, since the child it forks must outlive it to serve
// the selection.
func runHelper(timeout time.Duration, limit int, stdin io.Reader, capture bool, name string, args ...string) helperRun {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = stdin
	cmd.WaitDelay = helperWaitDelay
	var stdout, stderr *cappedBuffer
	if capture {
		stdout = &cappedBuffer{limit: limit, cancel: cancel}
		stderr = &cappedBuffer{limit: maxHelperStderr, cancel: cancel}
		cmd.Stdout = stdout
		cmd.Stderr = stderr
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error { return killGroup(cmd.Process.Pid) }
	}
	err := cmd.Run()
	r := helperRun{
		err:      err,
		timedOut: err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded),
	}
	if capture {
		r.stdout, r.stderr, r.over = stdout.buf.Bytes(), stderr.buf.String(), stdout.over
	}
	return r
}

// killGroup kills the process group a reader helper leads (its pgid is its
// pid, from Setpgid). A group already gone is not an error.
func killGroup(pid int) error {
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

// runImageHelper runs one clipboard image helper and returns its stdout,
// bounded as runHelper is. A failed run whose stderr says "no image"
// (isNoImageStderr) is ErrNoImage; any other failure is an error.
func runImageHelper(timeout time.Duration, limit int, name string, args ...string) ([]byte, error) {
	r := runHelper(timeout, limit, nil, true, name, args...)
	switch {
	case r.over:
		return nil, errImageTooLarge
	case r.timedOut:
		return nil, fmt.Errorf("clipboard image: %s %w after %s", name, errHelperTimedOut, timeout)
	case r.err != nil && isNoImageStderr(r.stderr):
		return nil, ErrNoImage
	case r.err != nil:
		return nil, fmt.Errorf("clipboard image: %s: %w: %s", name, r.err, strings.TrimSpace(r.stderr))
	}
	return r.stdout, nil
}
