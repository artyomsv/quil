package winjob

import (
	"slices"
	"testing"
)

func TestDaemonEnv(t *testing.T) {
	base := []string{"PATH=C:\\x", "QUIL_HOME=C:\\old", "SSH_CONNECTION=1 2 3 4", "ssh_client=a", "SSH_TTY=x", "HOME=h"}

	t.Run("default home drops QUIL_HOME and keeps SSH when not lowered", func(t *testing.T) {
		got := DaemonEnv(base, `C:\Users\a\.quil`, true, false)
		if slices.ContainsFunc(got, func(e string) bool { return envKey(e) == "QUIL_HOME" }) {
			t.Errorf("QUIL_HOME kept for a default home: %q", got)
		}
		if !slices.Contains(got, "SSH_CONNECTION=1 2 3 4") {
			t.Errorf("SSH_CONNECTION dropped without lowering: %q", got)
		}
	})
	t.Run("non-default home sets QUIL_HOME exactly once", func(t *testing.T) {
		got := DaemonEnv(base, `E:\p\.quil`, false, false)
		n := 0
		for _, e := range got {
			if envKey(e) == "QUIL_HOME" {
				n++
				if e != `QUIL_HOME=E:\p\.quil` {
					t.Errorf("QUIL_HOME = %q", e)
				}
			}
		}
		if n != 1 {
			t.Errorf("QUIL_HOME appears %d times", n)
		}
	})
	t.Run("lowered drops every SSH_ marker, case-insensitively", func(t *testing.T) {
		got := DaemonEnv(base, `E:\p\.quil`, false, true)
		for _, e := range got {
			switch envKey(e) {
			case "SSH_CONNECTION", "SSH_CLIENT", "SSH_TTY":
				t.Errorf("lowered env kept %q", e)
			}
		}
		if !slices.Contains(got, "PATH=C:\\x") || !slices.Contains(got, "HOME=h") {
			t.Errorf("unrelated variables lost: %q", got)
		}
	})
	t.Run("input is not mutated", func(t *testing.T) {
		in := slices.Clone(base)
		_ = DaemonEnv(in, `E:\p\.quil`, false, true)
		if !slices.Equal(in, base) {
			t.Errorf("DaemonEnv mutated its input")
		}
	})
}
