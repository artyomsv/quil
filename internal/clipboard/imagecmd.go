package clipboard

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// This file holds the platform-neutral half of the Unix image readers
// (clipboard_unix.go): parsing what osascript and xclip print. It carries no
// build tag on purpose — CI runs on Linux, so a parser that lived behind
// `//go:build darwin` would never be compiled or tested there.

// maxImageSize caps a clipboard image. A full-screen Retina screenshot is a
// few MB of PNG; 50 MB leaves ample room while still bounding the read.
const maxImageSize = 50 * 1024 * 1024

// pngMagic is the 8-byte PNG file signature.
var pngMagic = []byte("\x89PNG\r\n\x1a\n")

// errImageTooLarge reports a clipboard image over maxImageSize.
var errImageTooLarge = errors.New("clipboard: image too large")

// AppleScript prints `the clipboard as «class PNGf»` as a data literal:
// «data PNGf89504E47…». osascript writes the guillemets as UTF-8.
const (
	appleDataPrefix = "«data PNGf"
	appleDataSuffix = "»"
)

// appleScriptNoImage is the error number AppleScript reports when the
// clipboard cannot be coerced to PNG — that is, it holds no image.
const appleScriptNoImage = "(-1700)"

// decodeAppleScriptPNG turns osascript's «data PNGf…» literal into PNG bytes.
func decodeAppleScriptPNG(out []byte) ([]byte, error) {
	s := strings.TrimSpace(string(out))
	if !strings.HasPrefix(s, appleDataPrefix) || !strings.HasSuffix(s, appleDataSuffix) {
		return nil, fmt.Errorf("clipboard: unexpected osascript output (%d bytes)", len(out))
	}
	h := s[len(appleDataPrefix) : len(s)-len(appleDataSuffix)]
	if len(h)/2 > maxImageSize {
		return nil, errImageTooLarge
	}
	img, err := hex.DecodeString(h)
	if err != nil {
		return nil, fmt.Errorf("clipboard: decode osascript hex: %w", err)
	}
	return checkPNG(img)
}

// checkPNG returns b when it starts with the PNG signature. An empty b means
// there was no image; anything else is a reader that returned the wrong type,
// which must not be saved under a .png name and typed into a pane.
func checkPNG(b []byte) ([]byte, error) {
	if len(b) == 0 {
		return nil, ErrNoImage
	}
	if len(b) > maxImageSize {
		return nil, errImageTooLarge
	}
	if !bytes.HasPrefix(b, pngMagic) {
		return nil, fmt.Errorf("clipboard: image data is not PNG")
	}
	return b, nil
}

// isNoImageStderr reports whether a failed reader's stderr means "the
// clipboard holds no image" rather than a real failure. osascript says -1700;
// xclip says the image/png target is not available.
func isNoImageStderr(stderr string) bool {
	return strings.Contains(stderr, appleScriptNoImage) ||
		strings.Contains(stderr, "not available")
}
