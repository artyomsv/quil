package winjob

// Job limit flags, mirrored from x/sys/windows so the verdict compiles and is
// tested on Linux CI. TestJobConstants_MatchXSys pins them on Windows.
const (
	jobLimitBreakawayOK    = 0x00000800 // JOB_OBJECT_LIMIT_BREAKAWAY_OK
	jobLimitKillOnJobClose = 0x00002000 // JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
)

// jobVerdict turns "is this process in a job" and the INNERMOST job's limit
// flags into InKillOnCloseJob's answer. Only the innermost job is visible, so
// an outer kill-on-close job (sshd's) can sit behind an inner one without it:
//
//   - not in any job: (false, false).
//   - innermost kills on close: (true, breakaway allowed).
//   - innermost does not kill on close but allows breakaway: (true, true). An
//     unseen outer kill-on-close job may still take the daemon down, and
//     breaking away is safe whether or not one exists.
//   - innermost allows neither: (false, false). Breaking away is impossible, so
//     the normal spawn is the only one that can work.
func jobVerdict(in bool, flags uint32) (inJob, breakawayOK bool) {
	if !in {
		return false, false
	}
	breakaway := flags&jobLimitBreakawayOK != 0
	if flags&jobLimitKillOnJobClose != 0 {
		return true, breakaway
	}
	if breakaway {
		return true, true
	}
	return false, false
}
