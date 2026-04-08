//go:build linux || darwin || freebsd

package clipboard

import (
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strings"
	"sync"
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

// readImage is not yet implemented on Unix — Quil's primary need for the
// image-paste proxy is Windows (claude-code's upstream bug). macOS could
// read via `pbpaste -Prefer png` or `osascript` reading NSPasteboard; Linux
// via `xclip -selection clipboard -t image/png -o`. For now this is a stub
// that always reports "no image" so the caller falls back to text paste.
func readImage() ([]byte, error) {
	return nil, ErrNoImage
}
