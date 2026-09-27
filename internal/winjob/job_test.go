package winjob

import "testing"

func TestJobInfo_MapsEachFlagCombination(t *testing.T) {
	tests := []struct {
		name  string
		in    bool
		flags uint32
		want  JobInfo
	}{
		{"not in any job ignores the flags", false, jobLimitKillOnJobClose | jobLimitBreakawayOK, JobInfo{}},
		{"kill-on-close with breakaway", true, jobLimitKillOnJobClose | jobLimitBreakawayOK,
			JobInfo{InJob: true, KillOnClose: true, BreakawayOK: true}},
		{"kill-on-close without breakaway", true, jobLimitKillOnJobClose,
			JobInfo{InJob: true, KillOnClose: true}},
		{"breakaway without kill-on-close", true, jobLimitBreakawayOK,
			JobInfo{InJob: true, BreakawayOK: true}},
		{"no flags is still in a job", true, 0, JobInfo{InJob: true}},
		{"unrelated flags only is still in a job", true, 0x1 | 0x100, JobInfo{InJob: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := jobInfo(tt.in, tt.flags); got != tt.want {
				t.Errorf("jobInfo(%v, %#x) = %+v, want %+v", tt.in, tt.flags, got, tt.want)
			}
		})
	}
}
