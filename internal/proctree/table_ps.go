package proctree

import (
	"strconv"
	"strings"
	"time"
)

// The Darwin ps parsers live here, with NO build tag, deliberately.
//
// CI is Linux. Anything behind //go:build darwin is never compiled, never
// vetted and never tested — so a parse bug there ships and stays shipped. This
// platform's Start comes entirely from this parse, and Start gates both Build's
// splice rejection and every kill, which makes it the least affordable place in
// the package to have untested code.
//
// The build-tagged file keeps what genuinely cannot run here: the exec.

// psLStartFields is how many whitespace-separated fields `lstart` occupies:
// "Wed Aug 20 12:00:00 2026".
const psLStartFields = 5

// psLStartLayout matches lstart's fixed format. Day-of-month is space-padded by
// ps, which Fields collapses, so the layout uses "2" rather than "_2".
const psLStartLayout = "Mon Jan 2 15:04:05 2006"

// parsePSTable parses `ps -axo pid=,ppid=,lstart=,comm=` output.
func parsePSTable(out string) []ProcessEntry {
	lines := strings.Split(out, "\n")
	entries := make([]ProcessEntry, 0, len(lines))
	for _, line := range lines {
		e, ok := parsePSLine(line)
		if !ok {
			continue
		}
		entries = append(entries, e)
	}
	return entries
}

func parsePSLine(line string) (ProcessEntry, bool) {
	fields := strings.Fields(line)
	// pid + ppid + five lstart fields + at least one comm field.
	if len(fields) < 2+psLStartFields+1 {
		return ProcessEntry{}, false
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil {
		return ProcessEntry{}, false
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return ProcessEntry{}, false
	}

	// A start time that will not parse stays ZERO rather than defaulting to
	// something plausible. Zero means "unknown" everywhere in this package, and
	// every consumer already picks a safe direction for it.
	start, err := time.ParseInLocation(psLStartLayout,
		strings.Join(fields[2:2+psLStartFields], " "), time.Local)
	if err != nil {
		start = time.Time{}
	}

	// comm is EVERYTHING remaining, joined rather than taken as one field. The
	// removed attempt took the first field of a space-split, which turned
	// "Google Chrome Helper" into "Google".
	name := strings.Join(fields[2+psLStartFields:], " ")

	return ProcessEntry{PID: pid, PPID: ppid, Name: name, Start: start}, true
}

// parsePSStart parses `ps -o lstart= -p <pid>` output into a start time.
//
// Untagged like the rest of this file: it is the identity check the entire
// Darwin kill path rests on, and behind a build tag it would never be compiled
// by CI, let alone tested.
func parsePSStart(out string) (time.Time, bool) {
	fields := strings.Fields(out)
	if len(fields) < psLStartFields {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation(psLStartLayout,
		strings.Join(fields[:psLStartFields], " "), time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// parsePSPercent parses `ps -o pid=,pcpu=` output into a percentage per PID.
func parsePSPercent(out string) map[int]float64 {
	res := map[int]float64{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		pct, err := strconv.ParseFloat(fields[1], 64)
		if err != nil || pct < 0 {
			continue
		}
		res[pid] = pct
	}
	return res
}
