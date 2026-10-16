package sbx

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// TagEnv names the variable each pane's agent carries inside the sandbox.
//
// Ending the `sbx exec` process on the host does NOT end the program it
// started inside the VM (measured on sbx 0.48: a closed pane's claude kept
// running for a day). So every agent is started with a tag, and closing the
// pane stops exactly the processes that carry it — the agent and everything
// it started, since children inherit the environment.
const TagEnv = "QUIL_SBX_PANE"

// stopTimeout bounds one stop: an sbx exec plus the script's own wait.
const stopTimeout = 15 * time.Second

// NewTag returns a tag for ONE spawn of a pane: the pane id, a dash, and a
// random part. Per spawn rather than per pane, because a restart closes the
// old process and starts the new one concurrently — a pane-wide tag would let
// the old stop kill the new agent.
func NewTag(paneID string) string {
	b := make([]byte, 6)
	_, _ = rand.Read(b) // crypto/rand.Read does not fail on supported platforms
	return paneID + "-" + hex.EncodeToString(b)
}

// TagPrefix matches every spawn of a pane, for leftovers a crash left behind.
func TagPrefix(paneID string) string { return paneID + "-" }

// stopScript runs inside the sandbox. $1 is the tag (or prefix), $2 "exact"
// or "prefix". It reads each process's environment from /proc, sends TERM to
// the matches, waits up to 2 s, then KILLs what is left. The tag is an
// argument, never part of the script text, so nothing in it is parsed by sh.
const stopScript = `want=$1 mode=$2 pids=
for d in /proc/[0-9]*; do
  p=${d#/proc/}
  [ "$p" = "$$" ] && continue
  for e in $(tr '\0' '\n' 2>/dev/null < "$d/environ" | grep '^` + TagEnv + `='); do
    v=${e#` + TagEnv + `=}
    case $mode in
      exact) [ "$v" = "$want" ] && pids="$pids $p" ;;
      prefix) case $v in "$want"*) pids="$pids $p" ;; esac ;;
    esac
  done
done
[ -n "$pids" ] || exit 0
kill -TERM $pids 2>/dev/null
i=0
while [ $i -lt 10 ]; do
  alive=
  for p in $pids; do kill -0 "$p" 2>/dev/null && alive="$alive $p"; done
  [ -n "$alive" ] || exit 0
  pids=$alive
  i=$((i+1))
  sleep 0.2
done
kill -KILL $pids 2>/dev/null
exit 0`

// StopArgs is the sbx call that stops the processes tagged tag (or, with
// prefix, every tag that starts with it).
func StopArgs(name, tag string, prefix bool) []string {
	mode := "exact"
	if prefix {
		mode = "prefix"
	}
	return []string{"exec", name, "sh", "-c", stopScript, "sh", tag, mode}
}

// Stop stops the tagged processes inside the sandbox. No match is success.
func (c Client) Stop(ctx context.Context, name, tag string, prefix bool) error {
	ctx, cancel := context.WithTimeout(ctx, stopTimeout)
	defer cancel()
	var buf bytes.Buffer
	if err := c.run(ctx, "", &buf, StopArgs(name, tag, prefix)...); err != nil {
		return fmt.Errorf("sbx: stopping %s in %s: %w%s", tag, name, err, errorLine(buf.String()))
	}
	return nil
}
