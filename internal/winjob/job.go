package winjob

// Job limit flags, mirrored from x/sys/windows so the mapping compiles and is
// tested on Linux CI. TestJobConstants_MatchXSys pins them on Windows.
const (
	jobLimitBreakawayOK    = 0x00000800 // JOB_OBJECT_LIMIT_BREAKAWAY_OK
	jobLimitKillOnJobClose = 0x00002000 // JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
)

// JobInfo describes the innermost job this process is in.
//
// Only the innermost job is visible to the query, and an inner job can hide an
// outer kill-on-close one (sshd's). So InJob without KillOnClose does NOT mean
// "safe to spawn normally": StartDaemon decides what each combination means.
type JobInfo struct {
	InJob       bool // in any job at all
	KillOnClose bool // the innermost job kills its members when it closes
	BreakawayOK bool // the innermost job lets a child break away
}

// jobInfo maps "is this process in a job" and the innermost job's limit flags
// into a JobInfo. It is a pure mapping of the flags; every policy lives in
// StartDaemon.
func jobInfo(in bool, flags uint32) JobInfo {
	if !in {
		return JobInfo{}
	}
	return JobInfo{
		InJob:       true,
		KillOnClose: flags&jobLimitKillOnJobClose != 0,
		BreakawayOK: flags&jobLimitBreakawayOK != 0,
	}
}
