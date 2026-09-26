package winjob

import "testing"

func TestJobVerdict(t *testing.T) {
	tests := []struct {
		name          string
		in            bool
		flags         uint32
		wantInJob     bool
		wantBreakaway bool
	}{
		{"not in any job", false, jobLimitKillOnJobClose | jobLimitBreakawayOK, false, false},
		{"kill-on-close with breakaway", true, jobLimitKillOnJobClose | jobLimitBreakawayOK, true, true},
		{"kill-on-close without breakaway", true, jobLimitKillOnJobClose, true, false},
		{"breakaway without kill-on-close may hide an outer job", true, jobLimitBreakawayOK, true, true},
		{"no flags cannot break away so spawns normally", true, 0, false, false},
		{"unrelated flags only", true, 0x1 | 0x100, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inJob, breakaway := jobVerdict(tt.in, tt.flags)
			if inJob != tt.wantInJob || breakaway != tt.wantBreakaway {
				t.Errorf("jobVerdict(%v, %#x) = (%v, %v), want (%v, %v)",
					tt.in, tt.flags, inJob, breakaway, tt.wantInJob, tt.wantBreakaway)
			}
		})
	}
}
