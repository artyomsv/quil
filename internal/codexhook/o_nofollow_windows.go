//go:build windows

package codexhook

// oNoFollow is unavailable on Windows; symlink creation there requires
// elevated privilege, so the practical surface is narrower. The regular-file
// check in ReadPersistedSession still refuses anything that is not a file.
// Mirrors internal/opencodehook.
const oNoFollow = 0
