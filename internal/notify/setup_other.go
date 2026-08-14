//go:build !windows

package notify

// Setup, Remove, Registered and ShortcutPath exist on every platform so
// cmd/quil needs no build tags of its own; they refuse where there is nothing
// to register.
func Setup(opts Options, exePath, home string) ([]string, error) { return nil, ErrUnsupported }

func Remove(opts Options) ([]string, error) { return nil, ErrUnsupported }

// Registered returns (false, nil) rather than the error: it answers a question
// that has a true answer everywhere ("no"), and returning an error would make
// every caller special-case a platform that simply has no registration.
func Registered(opts Options) (bool, error) { return false, nil }

func ShortcutPath(opts Options) (string, error) { return "", ErrUnsupported }
