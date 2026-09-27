package winjob

import "testing"

func TestSessionQualifies(t *testing.T) {
	tests := []struct {
		name      string
		id, state uint32
		user, dom string
		want      bool
	}{
		{"session 0 refused", 0, wtsStateActive, "alice", "HOST", false},
		{"active ok", 1, wtsStateActive, "alice", "HOST", true},
		{"disconnected ok", 2, wtsStateDisconnected, "alice", "HOST", true},
		{"listen state refused", 1, 6, "alice", "HOST", false},
		{"name match but domain mismatch refused", 1, wtsStateActive, "alice", "OTHER", false},
		{"other user refused", 1, wtsStateActive, "bob", "HOST", false},
		{"case-insensitive match ok", 1, wtsStateActive, "ALICE", "host", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sessionQualifies(tt.id, tt.state, tt.user, tt.dom, "alice", "HOST"); got != tt.want {
				t.Errorf("sessionQualifies(%d, %d, %q, %q) = %v, want %v", tt.id, tt.state, tt.user, tt.dom, got, tt.want)
			}
		})
	}
}
