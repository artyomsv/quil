//go:build !linux && !darwin && !windows

package proctree

import "time"

// No signalling primitives on this platform. Every operation refuses, so the
// sweep signals nothing and reports it — rather than appearing to succeed.

// DefaultKillOps returns primitives that refuse.
func DefaultKillOps() KillOps {
	return KillOps{
		Term:  func(int, time.Time) error { return ErrUnsupported },
		Kill:  func(int, time.Time) error { return ErrUnsupported },
		Alive: func(int, time.Time) bool { return false },
		Sleep: time.Sleep,
	}
}
