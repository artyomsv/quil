//go:build linux || darwin || freebsd

package clipboard

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// maxClipboardSize limits clipboard reads to prevent excessive memory allocation.
const maxReadSize = 10 * 1024 * 1024 // 10 MB

// Cached clipboard tool detection — avoids repeated LookPath calls.
var (
	clipToolOnce sync.Once
	clipToolPath string // "darwin", or path to xclip/xsel, or ""
)

func detectClipTool() {
	if runtime.GOOS == "darwin" {
		clipToolPath = "darwin"
		return
	}
	if p, err := exec.LookPath("xclip"); err == nil {
		clipToolPath = p
	} else if p, err := exec.LookPath("xsel"); err == nil {
		clipToolPath = p
	}
}

func readCmd() (*exec.Cmd, error) {
	clipToolOnce.Do(detectClipTool)
	switch {
	case clipToolPath == "darwin":
		return exec.Command("pbpaste"), nil
	case strings.HasSuffix(clipToolPath, "xclip"):
		return exec.Command(clipToolPath, "-selection", "clipboard", "-o"), nil
	case strings.HasSuffix(clipToolPath, "xsel"):
		return exec.Command(clipToolPath, "--clipboard", "--output"), nil
	default:
		return nil, fmt.Errorf("no clipboard tool found (install xclip or xsel)")
	}
}

func writeCmd() (*exec.Cmd, error) {
	clipToolOnce.Do(detectClipTool)
	switch {
	case clipToolPath == "darwin":
		return exec.Command("pbcopy"), nil
	case strings.HasSuffix(clipToolPath, "xclip"):
		return exec.Command(clipToolPath, "-selection", "clipboard"), nil
	case strings.HasSuffix(clipToolPath, "xsel"):
		return exec.Command(clipToolPath, "--clipboard", "--input"), nil
	default:
		return nil, fmt.Errorf("no clipboard tool found (install xclip or xsel)")
	}
}

func read() (string, error) {
	cmd, err := readCmd()
	if err != nil {
		return "", err
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("clipboard stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("clipboard start: %w", err)
	}

	limited := io.LimitReader(stdout, maxReadSize)
	out, err := io.ReadAll(limited)
	if err != nil {
		return "", fmt.Errorf("clipboard read: %w", err)
	}
	cmd.Wait()

	return clipboardText(out), nil
}

func write(text string) error {
	cmd, err := writeCmd()
	if err != nil {
		return err
	}
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}

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

// readImage returns the clipboard image as PNG bytes (#261).
//
// macOS: `osascript -e 'the clipboard as «class PNGf»'` — ships with the OS,
// and AppleScript converts a TIFF-only clipboard to PNG itself. It prints a
// «data PNGf…» hex literal, decoded by decodeAppleScriptPNG. pbpaste cannot
// help: it only ever returns text.
//
// Linux: `xclip -selection clipboard -t image/png -o`. xsel has no way to ask
// for a target type, so an xsel-only host reports ErrNoImage.
func readImage() ([]byte, error) {
	clipToolOnce.Do(detectClipTool)
	switch {
	case clipToolPath == "darwin":
		// Two hex digits per byte, plus the literal's framing.
		out, err := runImageHelper(imageReadTimeout, 2*maxImageSize+64,
			"osascript", "-e", "the clipboard as «class PNGf»")
		if err != nil {
			return nil, err
		}
		return decodeAppleScriptPNG(out)
	case strings.HasSuffix(clipToolPath, "xclip"):
		out, err := runImageHelper(imageReadTimeout, maxImageSize,
			clipToolPath, "-selection", "clipboard", "-t", "image/png", "-o")
		if err != nil {
			return nil, err
		}
		return checkPNG(out)
	default:
		return nil, ErrNoImage
	}
}

// cappedBuffer collects a helper's output up to limit bytes. Past the limit it
// cancels the helper's context, so an oversized image ends the helper at once
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

// runImageHelper runs one clipboard helper and returns its stdout. It is
// bounded three ways: timeout, limit bytes of stdout, and helperWaitDelay for
// the pipes after a kill. A failed run whose stderr says "no image"
// (isNoImageStderr) is ErrNoImage; any other failure is an error.
func runImageHelper(timeout time.Duration, limit int, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	stdout := &cappedBuffer{limit: limit, cancel: cancel}
	stderr := &cappedBuffer{limit: maxHelperStderr, cancel: cancel}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.WaitDelay = helperWaitDelay
	err := cmd.Run()
	switch {
	case stdout.over:
		return nil, errImageTooLarge
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return nil, fmt.Errorf("clipboard image: %s timed out after %s", name, timeout)
	case err != nil && isNoImageStderr(stderr.buf.String()):
		return nil, ErrNoImage
	case err != nil:
		return nil, fmt.Errorf("clipboard image: %s: %w: %s", name, err, strings.TrimSpace(stderr.buf.String()))
	}
	return stdout.buf.Bytes(), nil
}
