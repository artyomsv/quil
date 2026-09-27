package winjob

import (
	"strings"
	"testing"
)

// Task names are machine-wide, so two users' production tasks must not share
// one (PR #242 review M-1).
func TestLogonTaskName_TwoUsersDefaultHomes_Differ(t *testing.T) {
	alice := LogonTaskName("quild", `C:\Users\Alice\.quil`)
	bob := LogonTaskName("quild", `C:\Users\Bob\.quil`)
	if alice == bob {
		t.Fatalf("both users get %q", alice)
	}
	for _, n := range []string{alice, bob} {
		if !strings.HasPrefix(n, "Quil daemon (quild ") {
			t.Errorf("name %q does not start with %q", n, "Quil daemon (quild ")
		}
	}
}

func TestLogonTaskName_VariantsAndHomesAreDistinct(t *testing.T) {
	a := LogonTaskName("quild-dev", `E:\p\.quil`)
	b := LogonTaskName("quild-dev", `E:\q\.quil`)
	c := LogonTaskName("quild-debug", `E:\p\.quil`)
	d := LogonTaskName("quild", `E:\p\.quil`)
	seen := map[string]bool{}
	for _, n := range []string{a, b, c, d} {
		if seen[n] {
			t.Fatalf("duplicate task name %q among %q %q %q %q", n, a, b, c, d)
		}
		seen[n] = true
	}
	if want := "Quil daemon (quild-dev "; !strings.HasPrefix(a, want) {
		t.Errorf("dev name %q does not start with %q", a, want)
	}
}

func TestLogonTaskName_StableAcrossTrailingSeparatorAndCase(t *testing.T) {
	base := LogonTaskName("quild-dev", `E:\p\.quil`)
	for _, dir := range []string{`E:\p\.quil\`, `e:\P\.QUIL`, `E:/p/.quil`, `E:\p\.quil\\`} {
		if got := LogonTaskName("quild-dev", dir); got != base {
			t.Errorf("LogonTaskName(%q) = %q, want %q", dir, got, base)
		}
	}
}
