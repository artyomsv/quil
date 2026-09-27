package main

import (
	"errors"
	"fmt"
	"syscall"
	"testing"
)

func TestOpenFailureMeansAlive_OnlyInvalidParameterIsDead(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"no such process", win32ErrorInvalidParameter, false},
		{"wrapped no such process", fmt.Errorf("open: %w", win32ErrorInvalidParameter), false},
		// The measured ssh case: the process exists but is not ours.
		{"access denied", win32ErrorAccessDenied, true},
		{"unknown errno", syscall.Errno(31), true},
		{"not an errno", errors.New("boom"), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := openFailureMeansAlive(tt.err); got != tt.want {
				t.Errorf("openFailureMeansAlive(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestExitCodeMeansAlive_OnlyStillActive(t *testing.T) {
	tests := []struct {
		code uint32
		want bool
	}{
		{259, true},
		{0, false},
		{1, false},
		{3, false},
	}
	for _, tt := range tests {
		if got := exitCodeMeansAlive(tt.code); got != tt.want {
			t.Errorf("exitCodeMeansAlive(%d) = %v, want %v", tt.code, got, tt.want)
		}
	}
}
