package clipboard

import "errors"

// ErrNoImage is returned by ReadImage when the clipboard does not contain
// an image in a supported format. Callers typically fall back to text.
var ErrNoImage = errors.New("clipboard: no image")

// Read returns the current system clipboard text content.
// Platform-specific implementations are in clipboard_windows.go and clipboard_unix.go.
func Read() (string, error) {
	return read()
}

// Write sets the system clipboard to the given text.
// Platform-specific implementations are in clipboard_windows.go and clipboard_unix.go.
func Write(text string) error {
	return write(text)
}

// ReadImage returns the current clipboard image encoded as PNG bytes. If the
// clipboard does not contain an image in a supported format, it returns
// ErrNoImage. Where no image reader is available (Linux without xclip), it
// also returns ErrNoImage.
//
// Windows reads CF_DIBV5/CF_DIB, macOS runs osascript, Linux runs xclip. It
// began as a workaround for the Windows-specific upstream bug in claude-code
// where its own clipboard image reader fails to see images that are
// verifiably present on the clipboard (see anthropics/claude-code#32791).
// Quil reads the image itself, saves a temp file, and pastes the path so any
// PTY tool can pick it up via its file-reading tools. On macOS and Linux it
// is needed because Quil takes Ctrl+V before the app in the pane sees it.
func ReadImage() ([]byte, error) {
	return readImage()
}
