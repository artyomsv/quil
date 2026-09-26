package winjob

import "testing"

func TestLogonTaskName_ProdDefaultHome_IsPlain(t *testing.T) {
	if got := LogonTaskName("quild", `C:\Users\a\.quil`, true); got != "Quil daemon" {
		t.Errorf("got %q, want %q", got, "Quil daemon")
	}
}

func TestLogonTaskName_VariantsAndHomesAreDistinct(t *testing.T) {
	a := LogonTaskName("quild-dev", `E:\p\.quil`, false)
	b := LogonTaskName("quild-dev", `E:\q\.quil`, false)
	c := LogonTaskName("quild-debug", `E:\p\.quil`, false)
	d := LogonTaskName("quild", `E:\p\.quil`, false)
	seen := map[string]bool{}
	for _, n := range []string{a, b, c, d, "Quil daemon"} {
		if seen[n] {
			t.Fatalf("duplicate task name %q among %q %q %q %q", n, a, b, c, d)
		}
		seen[n] = true
	}
	if want := "Quil daemon (quild-dev "; len(a) < len(want) || a[:len(want)] != want {
		t.Errorf("dev name %q does not start with %q", a, want)
	}
}

func TestLogonTaskName_StableAcrossTrailingSeparatorAndCase(t *testing.T) {
	base := LogonTaskName("quild-dev", `E:\p\.quil`, false)
	for _, dir := range []string{`E:\p\.quil\`, `e:\P\.QUIL`, `E:/p/.quil`, `E:\p\.quil\\`} {
		if got := LogonTaskName("quild-dev", dir, false); got != base {
			t.Errorf("LogonTaskName(%q) = %q, want %q", dir, got, base)
		}
	}
}
