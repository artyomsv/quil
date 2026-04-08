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
// ErrNoImage. On platforms where image clipboard is not implemented, it
// also returns ErrNoImage.
//
// This exists primarily to work around the Windows-specific upstream bug in
// claude-code where its own clipboard image reader fails to see images that
// are verifiably present on the clipboard (see anthropics/claude-code#32791).
// Quil reads the image itself, saves a temp file, and pastes the path so any
// PTY tool can pick it up via its file-reading tools.
func ReadImage() ([]byte, error) {
	return readImage()
}
