package main

import (
	"testing"
	"time"
)

func TestFormatBytes_Boundaries(t *testing.T) {
	tests := []struct {
		name string
		in   uint64
		want string
	}{
		{"zero", 0, "0 B"},
		{"sub-kb", 512, "512 B"},
		{"one-kb", 1024, "1.0 KB"},
		{"kb-fraction", 1536, "1.5 KB"},
		{"one-mb", 1024 * 1024, "1.0 MB"},
		{"mb-fraction", 1024*1024*3 + 400*1024, "3.4 MB"},
		{"one-gb", 1024 * 1024 * 1024, "1.0 GB"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatBytes(tt.in); got != tt.want {
				t.Errorf("formatBytes(%d) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestFormatUptime_Boundaries(t *testing.T) {
	tests := []struct {
		name string
		in   time.Duration
		want string
	}{
		{"under-a-minute", 30 * time.Second, "<1m"},
		{"minutes", 5 * time.Minute, "5m"},
		{"hours-minutes", 2*time.Hour + 13*time.Minute, "2h13m"},
		{"days-hours", 3*24*time.Hour + 4*time.Hour, "3d4h"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatUptime(tt.in); got != tt.want {
				t.Errorf("formatUptime(%v) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
