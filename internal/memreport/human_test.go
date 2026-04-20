package memreport

import "testing"

func TestHumanBytes(t *testing.T) {
	tests := []struct {
		name string
		in   uint64
		want string
	}{
		{"zero", 0, "0 B"},
		{"bytes", 512, "512 B"},
		{"kb_boundary_low", 1023, "1023 B"},
		{"kb_boundary_high", 1024, "1.0 KB"},
		{"kb_mid", 1536, "1.5 KB"},
		{"mb", 4_400_000, "4.2 MB"},
		{"gb", 1_503_238_553, "1.4 GB"},
		{"tb_cap", 1_500_000_000_000, "1.4 TB"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := HumanBytes(tt.in)
			if got != tt.want {
				t.Errorf("HumanBytes(%d) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
