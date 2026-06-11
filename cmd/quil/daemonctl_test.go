package main

import "testing"

func TestParsePidData(t *testing.T) {
	tests := []struct {
		name string
		data string
		want int
	}{
		{"plain pid", "29153", 29153},
		{"trailing newline", "29153\n", 29153},
		{"surrounding whitespace", "  4242 \r\n", 4242},
		{"empty", "", 0},
		{"garbage", "not-a-pid", 0},
		{"negative", "-5", 0},
		{"zero", "0", 0},
		{"float", "12.5", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parsePidData([]byte(tt.data)); got != tt.want {
				t.Errorf("parsePidData(%q) = %d, want %d", tt.data, got, tt.want)
			}
		})
	}
}

func TestIsQuildName(t *testing.T) {
	tests := []struct {
		name string
		comm string
		want bool
	}{
		{"plain", "quild", true},
		{"windows exe", "quild.exe", true},
		{"windows exe uppercase", "QUILD.EXE", true},
		{"dev variant", "quild-dev", true},
		{"dev variant exe", "quild-dev.exe", true},
		{"debug variant", "quild-debug", true},
		{"full path macos", "/Users/foo/.local/bin/quild", true},
		{"full path dev", "/home/foo/projects/quil/quild-dev", true},
		{"trailing newline from ps", "quild\n", true},
		{"unrelated process", "bash", false},
		{"tui binary not daemon", "quil", false},
		{"prefix without separator", "quilded", false},
		{"empty", "", false},
		{"windows path", `C:\Users\foo\quild.exe`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isQuildName(tt.comm); got != tt.want {
				t.Errorf("isQuildName(%q) = %v, want %v", tt.comm, got, tt.want)
			}
		})
	}
}
