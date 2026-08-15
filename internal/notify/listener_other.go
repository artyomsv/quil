//go:build !windows

package notify

import "io"

func PipeName(pid int) string { return "" }

func Listen(pid int, onActivate func(paneID string)) (io.Closer, error) {
	return nil, ErrUnsupported
}

func SendActivate(pid int, paneID string) error { return ErrUnsupported }
