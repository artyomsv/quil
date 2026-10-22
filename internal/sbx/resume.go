package sbx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// A pane's claude session inside the VM.
//
// An sbx pane runs no Quil hook inside the cage, so nothing in the VM tells
// Quil which session the agent is in. Quil keeps the one fact it needs on the
// host instead — the session id it handed the agent at its first start — and
// before a later start asks the VM whether that session has a transcript.
// The answer decides the start arguments: claude refuses `--resume <id>` for
// an id it never saw and `--session-id <id>` for one that already has a
// transcript (exit 129), so the choice has to be made from evidence.
//
// The transcript stays where claude writes it, in the VM. Whether it
// survives the sandbox being removed is the owning tool's business; Quil's
// part is to find it again whenever it is there.

// transcriptMarker is what the probe prints when the transcript exists; any
// other output (a notice that sbx started the sandbox) is ignored.
const transcriptMarker = "QUIL_SBX_TRANSCRIPT"

// transcriptScript looks for $1.jsonl in any project folder under claude's
// config directory: CLAUDE_CONFIG_DIR when the environment sets one, else
// claude's default. The id is an ARGUMENT, never script text, and it is
// checked to be a UUID before it gets here, so the glob holds no
// metacharacters of the id's own.
const transcriptScript = `d="${CLAUDE_CONFIG_DIR:-$HOME/.claude}"; for f in "$d"/projects/*/"$1".jsonl; do if [ -f "$f" ]; then echo ` + transcriptMarker + `; break; fi; done`

// TranscriptArgs is the probe's argv.
func TranscriptArgs(name, id string) []string {
	return []string{"exec", name, "sh", "-c", transcriptScript, "sh", id}
}

// sessionIDRe is a claude session id: a UUID, as claude prints and accepts
// it. The same shape the daemon accepts for a resume target.
var sessionIDRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// ValidSessionID reports whether id has the shape of a claude session id.
// Anything else never reaches the VM: a persisted value may have been
// hand-edited.
func ValidSessionID(id string) bool { return sessionIDRe.MatchString(id) }

// HasTranscript asks the sandbox whether the session id has a transcript in
// claude's project folders. It changes nothing; `sbx exec` starts a stopped
// sandbox, which the pane's own exec would do next anyway.
func (c Client) HasTranscript(ctx context.Context, name, id string) (bool, error) {
	if !ValidName(name) {
		return false, errors.New("invalid sandbox name")
	}
	if !ValidSessionID(id) {
		return false, errors.New("invalid session id")
	}
	ctx, cancel := context.WithTimeout(ctx, attachProbeTimeout)
	defer cancel()
	var buf bytes.Buffer
	if err := c.run(ctx, "", &buf, TranscriptArgs(name, id)...); err != nil {
		return false, fmt.Errorf("sbx exec: %w%s", err, tail(buf.String()))
	}
	for _, l := range strings.Split(buf.String(), "\n") {
		if strings.TrimSpace(l) == transcriptMarker {
			return true, nil
		}
	}
	return false, nil
}
