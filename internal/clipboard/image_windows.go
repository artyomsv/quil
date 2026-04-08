//go:build windows

package clipboard

import (
	"bytes"
	"fmt"
	"image/png"
	"unsafe"

	"github.com/artyomsv/quil/internal/logger"
)

// Win32 clipboard format codes.
//
// CF_DIBV5 is the modern superset that preserves alpha and color spaces;
// we probe it first so screenshots from Snipping Tool keep their alpha.
// CF_DIB is the auto-synthesized fallback that Windows produces from any
// other image format on the clipboard.
const (
	cfDIB   = 8
	cfDIBV5 = 17
)

// readImage reads an image from the Windows clipboard, parses its
// device-independent bitmap (DIB) representation, and returns the image
// re-encoded as PNG bytes. Returns ErrNoImage if no image is on the
// clipboard.
//
// Debug-level traces at every Win32 step. Off by default; flip
// [logging] level = "debug" in config.toml to see them.
func readImage() ([]byte, error) {
	logger.Debug("clipboard.readImage: attempting OpenClipboard")
	if r, _, callErr := openClipboard.Call(0); r == 0 {
		logger.Debug("clipboard.readImage: OpenClipboard returned 0, last error: %v", callErr)
		return nil, fmt.Errorf("OpenClipboard failed: %v", callErr)
	}
	defer closeClipboard.Call()
	logger.Debug("clipboard.readImage: OpenClipboard succeeded")

	// Probe DIBV5 first (preserves alpha), then fall back to plain DIB.
	var h uintptr
	var matchedFormat uintptr
	for _, cf := range []uintptr{cfDIBV5, cfDIB} {
		hh, _, _ := getClipboardData.Call(cf)
		logger.Debug("clipboard.readImage: GetClipboardData(format=%d) returned 0x%x", cf, hh)
		if hh != 0 {
			h = hh
			matchedFormat = cf
			break
		}
	}
	if h == 0 {
		logger.Debug("clipboard.readImage: no DIB/DIBV5 on clipboard, returning ErrNoImage")
		return nil, ErrNoImage
	}
	logger.Debug("clipboard.readImage: using clipboard format %d, handle=0x%x", matchedFormat, h)

	ptr, _, callErr := globalLock.Call(h)
	if ptr == 0 {
		logger.Debug("clipboard.readImage: GlobalLock failed: %v", callErr)
		return nil, fmt.Errorf("GlobalLock failed: %v", callErr)
	}
	defer globalUnlock.Call(h)
	logger.Debug("clipboard.readImage: GlobalLock succeeded, ptr=0x%x", ptr)

	size, _, _ := globalSize.Call(h)
	logger.Debug("clipboard.readImage: GlobalSize = %d bytes", size)
	if size == 0 {
		return nil, fmt.Errorf("clipboard image is empty")
	}
	if size > maxClipboardImageBytes {
		return nil, fmt.Errorf("clipboard image too large: %d bytes (max %d)", size, maxClipboardImageBytes)
	}

	// Copy the DIB into a Go-managed buffer so we can drop the GlobalLock.
	// unsafe.Slice gives us a temporary view; copy it before unlocking.
	view := unsafe.Slice((*byte)(unsafe.Pointer(ptr)), int(size))
	dib := make([]byte, len(view))
	copy(dib, view)
	logger.Debug("clipboard.readImage: copied %d bytes of DIB data", len(dib))

	img, err := decodeDIB(dib)
	if err != nil {
		logger.Debug("clipboard.readImage: decodeDIB failed: %v", err)
		return nil, fmt.Errorf("decode DIB: %w", err)
	}
	bounds := img.Bounds()
	logger.Debug("clipboard.readImage: decoded image %dx%d", bounds.Dx(), bounds.Dy())

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		logger.Debug("clipboard.readImage: png.Encode failed: %v", err)
		return nil, fmt.Errorf("encode PNG: %w", err)
	}
	logger.Debug("clipboard.readImage: encoded PNG, %d bytes", buf.Len())
	return buf.Bytes(), nil
}

// maxClipboardImageBytes caps the DIB we accept from the clipboard. The
// 8000x8000 px limit Claude Code documents is ~256 MB at 32bpp, which is
// excessive for a clipboard read. 64 MB is plenty for any reasonable
// screenshot.
const maxClipboardImageBytes = 64 * 1024 * 1024
