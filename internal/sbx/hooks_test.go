package sbx

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestMountReadOnlyArgs(t *testing.T) {
	got := MountReadOnlyArgs("app", "/Users/me/.quil/sandbox/bin/1.2.3")
	want := []string{"mount", "app", "/Users/me/.quil/sandbox/bin/1.2.3:/Users/me/.quil/sandbox/bin/1.2.3:ro"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("MountReadOnlyArgs = %v, want %v", got, want)
	}
}

func TestGoArch(t *testing.T) {
	for in, want := range map[string]string{
		"aarch64\n": "arm64", "arm64": "arm64", "x86_64": "amd64", "amd64\n": "amd64",
		"riscv64": "", "": "",
	} {
		got, ok := GoArch(in)
		if got != want || ok != (want != "") {
			t.Errorf("GoArch(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
}

// The probe runs `uname -m` inside the sandbox and reads the last line, so
// a start notice ahead of it is ignored.
func TestArch_RunsUnameInsideTheSandbox(t *testing.T) {
	f := &fakeRunner{answers: func([]string) (string, error) { return "Starting sandbox cf-x-1...\naarch64\n", nil }}
	got, err := (Client{Bin: "sbx", Runner: f}).Arch(context.Background(), "cf-x-1")
	if err != nil || got != "arm64" {
		t.Fatalf("Arch = %q, %v", got, err)
	}
	want := [][]string{{"sbx", "exec", "cf-x-1", "uname", "-m"}}
	if !reflect.DeepEqual(f.calls, want) {
		t.Errorf("calls = %v, want %v", f.calls, want)
	}
}

func TestArch_FailuresAreErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		out  string
		err  error
		want string
	}{
		"exec failed":     {"ERROR: sandbox not found", errors.New("exit status 1"), "sandbox not found"},
		"unknown machine": {"riscv64\n", nil, "riscv64"},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeRunner{answers: func([]string) (string, error) { return tc.out, tc.err }}
			_, err := (Client{Bin: "sbx", Runner: f}).Arch(context.Background(), "cf-x-1")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
	f := &fakeRunner{answers: func([]string) (string, error) { return "aarch64\n", nil }}
	if _, err := (Client{Bin: "sbx", Runner: f}).Arch(context.Background(), "-evil"); err == nil || len(f.calls) != 0 {
		t.Errorf("an unsafe name reached sbx: err=%v calls=%v", err, f.calls)
	}
}
