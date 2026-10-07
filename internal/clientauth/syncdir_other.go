//go:build !unix

package clientauth

// syncDir is a no-op where a directory handle cannot be synced: on Windows
// FlushFileBuffers refuses a directory handle, so there is nothing to call,
// and an error here must never fail a token write.
func syncDir(string) error { return nil }
