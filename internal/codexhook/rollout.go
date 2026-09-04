package codexhook

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// rolloutTailBytes caps how much of the rollout the hook reads. Rollouts grow
// to many MB over a long session; the last token_count line is always within
// the final few KB, so 256 KB keeps the hot-path subprocess fast against a
// pathological file.
const rolloutTailBytes = 256 << 10

// rolloutLine mirrors the subset of a codex rollout JSONL entry needed to read
// context usage:
//
//	{"type":"event_msg","payload":{"type":"token_count",
//	  "info":{"last_token_usage":{"total_tokens":N},"model_context_window":W}}}
//
// info is nullable upstream — a rate-limit-only update carries none.
type rolloutLine struct {
	Type    string `json:"type"`
	Payload struct {
		Type string `json:"type"`
		Info *struct {
			LastTokenUsage struct {
				TotalTokens int64 `json:"total_tokens"`
			} `json:"last_token_usage"`
		} `json:"info"`
	} `json:"payload"`
}

// readRolloutUsage tail-reads a codex rollout and returns the context-token
// count of the most recent token_count line. Codex's own
// tokens_in_context_window() is last_token_usage.total_tokens, so that is the
// figure shown. Best-effort by contract: any failure returns ok=false and the
// Stop event goes out without usage, exactly as before the feature.
//
// The path arrives via the hook stdin; only absolute .jsonl paths are
// eligible, as a defence against a forged payload.
func readRolloutUsage(path string) (contextTokens int64, ok bool) {
	if !filepath.IsAbs(path) || !strings.HasSuffix(path, ".jsonl") {
		return 0, false
	}
	// Refuse anything but a regular file BEFORE opening it: a FIFO at the
	// named path would park the Stop hook on Open until codex's timeout, and
	// Lstat sees a symlink where Stat would follow it.
	if li, err := os.Lstat(path); err != nil || !li.Mode().IsRegular() {
		return 0, false
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return 0, false
	}
	offset := st.Size() - rolloutTailBytes
	if offset < 0 {
		offset = 0
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return 0, false
	}
	buf, err := io.ReadAll(io.LimitReader(f, rolloutTailBytes))
	if err != nil {
		return 0, false
	}
	lines := bytes.Split(buf, []byte{'\n'})
	for i := len(lines) - 1; i >= 0; i-- {
		ln := bytes.TrimSpace(lines[i])
		if len(ln) == 0 || !bytes.Contains(ln, []byte(`"token_count"`)) {
			continue
		}
		var rl rolloutLine
		if err := json.Unmarshal(ln, &rl); err != nil {
			continue // the cut-off first line of the tail window, or noise
		}
		if rl.Type != "event_msg" || rl.Payload.Type != "token_count" || rl.Payload.Info == nil {
			continue
		}
		return rl.Payload.Info.LastTokenUsage.TotalTokens, true
	}
	return 0, false
}
