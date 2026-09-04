//go:build !windows

package codexhook

// shortPathName is a Windows notion (8.3 names); elsewhere the hook command
// is quoted for the shell and never needs it.
func shortPathName(p string) (string, error) {
	return p, nil
}
