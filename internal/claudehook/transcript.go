package claudehook

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// transcriptTailBytes caps how much of the session transcript the hook reads.
// Transcripts grow to many MB over a long session; the last assistant entry is
// always within the final few KB, so 256 KB is a generous window that keeps
// the hot-path hook subprocess fast even against a multi-GB pathological file.
const transcriptTailBytes = 256 << 10

// transcriptLine mirrors the subset of a Claude Code transcript JSONL entry
// needed to extract model + context usage. Extra fields are ignored.
type transcriptLine struct {
	Type        string `json:"type"`
	IsSidechain bool   `json:"isSidechain"`
	Message     struct {
		Model string `json:"model"`
		Usage struct {
			InputTokens              int64 `json:"input_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// readTranscriptUsage tail-reads a Claude Code session transcript and returns
// the model id and approximate context-token count (input + cache read +
// cache creation) from the most recent main-conversation assistant message.
// Sidechain (subagent) entries are skipped — their usage reflects the
// subagent's context, not the pane's conversation.
//
// Best-effort by contract: any failure (missing file, non-absolute path,
// unparseable tail) returns ok=false and the caller emits its event without
// model data, exactly as before this feature existed.
func readTranscriptUsage(path string) (model string, contextTokens int64, ok bool) {
	// Claude Code sends transcript_path with a literal "~/" prefix (matching
	// its hook docs), which filepath.IsAbs rejects — expand it first. This
	// was the cause of the silent no-data Stop events in live testing.
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", 0, false
		}
		path = filepath.Join(home, path[2:])
	}
	// The path arrives via Claude's hook stdin. Defense-in-depth against a
	// forged payload: only absolute .jsonl paths are eligible.
	if !filepath.IsAbs(path) || !strings.HasSuffix(path, ".jsonl") {
		return "", 0, false
	}
	f, err := os.Open(path)
	if err != nil {
		return "", 0, false
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil || st.IsDir() {
		return "", 0, false
	}
	offset := st.Size() - transcriptTailBytes
	truncated := offset > 0
	if !truncated {
		offset = 0
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return "", 0, false
	}
	buf, err := io.ReadAll(io.LimitReader(f, transcriptTailBytes))
	if err != nil {
		return "", 0, false
	}
	lines := bytes.Split(buf, []byte{'\n'})
	if truncated && len(lines) > 0 {
		lines = lines[1:] // drop the partial line the seek landed inside
	}
	for i := len(lines) - 1; i >= 0; i-- {
		line := bytes.TrimSpace(lines[i])
		// Cheap pre-filter: assistant entries are a minority of lines and a
		// full Unmarshal per line is the expensive part of this scan.
		if len(line) == 0 || !bytes.Contains(line, []byte(`"assistant"`)) {
			continue
		}
		var tl transcriptLine
		if err := json.Unmarshal(line, &tl); err != nil {
			continue
		}
		if tl.Type != "assistant" || tl.IsSidechain || tl.Message.Model == "" {
			continue
		}
		u := tl.Message.Usage
		return tl.Message.Model, u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens, true
	}
	return "", 0, false
}
