//go:build !windows

package notify

// New returns a nil Notifier everywhere but Windows.
//
// Nil rather than a no-op struct, and nil rather than an error: the caller
// stores it in an interface field and checks that field once, so no call site
// carries a platform branch. Desktop toasts on macOS and Linux are separate,
// lesser work — no transport there supports click routing, which is this
// feature's point.
func New(opts Options) (Notifier, error) { return nil, nil }
