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

	return strings.TrimSuffix(string(out), "\n"), nil
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
	var name string
	var args []string
	switch {
	case clipToolPath == "darwin":
		name, args = "osascript", []string{"-e", "the clipboard as «class PNGf»"}
	case strings.HasSuffix(clipToolPath, "xclip"):
		name, args = clipToolPath, []string{"-selection", "clipboard", "-t", "image/png", "-o"}
	default:
		return nil, ErrNoImage
	}

	ctx, cancel := context.WithTimeout(context.Background(), imageReadTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("clipboard image stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("clipboard image start: %w", err)
	}
	// osascript prints two hex digits per byte, plus the literal's framing.
	limit := int64(maxImageSize + 1)
	if clipToolPath == "darwin" {
		limit = 2*maxImageSize + 64
	}
	out, readErr := io.ReadAll(io.LimitReader(stdout, limit))
	if int64(len(out)) >= limit {
		// Stop the helper now: it is blocked writing the rest into a pipe
		// nobody reads, and Wait would otherwise sit out the whole timeout.
		_ = cmd.Process.Kill() // best effort; Wait below reaps it either way
		_ = cmd.Wait()
		return nil, errImageTooLarge
	}
	waitErr := cmd.Wait()
	if readErr != nil {
		return nil, fmt.Errorf("clipboard image read: %w", readErr)
	}
	if waitErr != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("clipboard image: %s timed out after %s", name, imageReadTimeout)
		}
		if isNoImageStderr(stderr.String()) {
			return nil, ErrNoImage
		}
		return nil, fmt.Errorf("clipboard image: %s: %w: %s", name, waitErr, strings.TrimSpace(stderr.String()))
	}
	if clipToolPath == "darwin" {
		return decodeAppleScriptPNG(out)
	}
	return checkPNG(out)
}
