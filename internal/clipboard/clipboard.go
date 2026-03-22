package clipboard

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
