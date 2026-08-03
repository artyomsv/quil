package daemon

import (
	"bytes"
	"testing"
)

// A shell's saved buffer ends mid-line by construction: the last thing it
// wrote was a prompt, cursor parked after it waiting for input that never
// came. The respawned shell prints its own prompt at that cursor, so the row
// reads "PS E:\...> PS E:\...>" — and because the seeded OutputBuf is what the
// next snapshot persists, the concatenation is SAVED and the row grows by one
// prompt on every restart (3387 → 3512 bytes across one restore, 2026-08-03).

func TestTerminateGhostLine(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "prompt with no newline gets one",
			in:   "PS E:\\Projects\\Stukans\\quil> ",
			want: "PS E:\\Projects\\Stukans\\quil> \r\n",
		},
		{
			// The property that stops this fix becoming the bug it replaces:
			// a terminated buffer must gain NOTHING, or every restart deposits
			// another blank row forever.
			name: "already terminated is untouched",
			in:   "output\r\n",
			want: "output\r\n",
		},
		{
			name: "bare LF also counts as terminated",
			in:   "output\n",
			want: "output\n",
		},
		{
			name: "empty stays empty",
			in:   "",
			want: "",
		},
		{
			// Escape sequences after the last newline leave the cursor
			// somewhere unknown, so the terminator is still owed.
			name: "trailing escape sequence still gets a terminator",
			in:   "output\r\n\x1b[?25h",
			want: "output\r\n\x1b[?25h\r\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(terminateGhostLine([]byte(tc.in))); got != tc.want {
				t.Errorf("terminateGhostLine(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// Restart-over-restart convergence. The reported bug was unbounded growth of a
// single row, so the fix has to be idempotent once a session adds no new
// output: replaying the restore repeatedly must not keep appending.
func TestTerminateGhostLine_ConvergesAcrossRestarts(t *testing.T) {
	buf := []byte("PS E:\\Projects\\Stukans\\quil> ")

	first := terminateGhostLine(buf)
	if !bytes.HasSuffix(first, []byte("\r\n")) {
		t.Fatalf("first restore did not terminate the line: %q", first)
	}

	// A daemon restarted twice with no shell output in between must produce a
	// byte-identical buffer the second time.
	second := terminateGhostLine(first)
	if !bytes.Equal(first, second) {
		t.Errorf("restore is not idempotent: %q grew to %q — this is the same "+
			"unbounded growth the fix exists to remove", first, second)
	}

	// With a real prompt written by the respawned shell in between, the buffer
	// grows by exactly that prompt plus its terminator — one row per session,
	// which is history rather than an artifact.
	withPrompt := append(append([]byte{}, first...), []byte("PS E:\\Projects\\Stukans\\quil> ")...)
	third := terminateGhostLine(withPrompt)
	if got, want := bytes.Count(third, []byte{'\n'}), 2; got != want {
		t.Errorf("after one more session the buffer holds %d lines, want %d", got, want)
	}
}
