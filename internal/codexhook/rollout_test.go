package codexhook

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tokenCountLine is a real line from a codex 0.146.0 rollout (2026-09-01),
// rate-limit fields trimmed.
const tokenCountLine = `{"timestamp":"2026-09-01T15:58:43.196Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":14064,"cached_input_tokens":9984,"cache_write_input_tokens":0,"output_tokens":8,"reasoning_output_tokens":0,"total_tokens":14072},"last_token_usage":{"input_tokens":14064,"cached_input_tokens":9984,"cache_write_input_tokens":0,"output_tokens":8,"reasoning_output_tokens":0,"total_tokens":14072},"model_context_window":258400},"rate_limits":{"limit_id":"codex"}}}`

func writeRollout(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "rollout-2026-09-01T17-58-36-01a05db1-9f44-73b2-b426-8aad5f5232f4.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReadRolloutUsage_LastTokenCountWins(t *testing.T) {
	t.Parallel()
	older := strings.Replace(tokenCountLine, `"total_tokens":14072},"model_context_window"`, `"total_tokens":999},"model_context_window"`, 1)
	if older == tokenCountLine {
		t.Fatal("fixture edit did not apply")
	}
	p := writeRollout(t,
		`{"type":"session_meta","payload":{"id":"x"}}`,
		older,
		`{"type":"event_msg","payload":{"type":"agent_message","message":"hi"}}`,
		tokenCountLine,
		`{"type":"event_msg","payload":{"type":"task_complete"}}`,
	)
	tokens, ok := readRolloutUsage(p)
	if !ok || tokens != 14072 {
		t.Errorf("readRolloutUsage = %d, %v; want 14072, true", tokens, ok)
	}
}

// A token_count whose info is null (rate-limit-only update) must not shadow
// the last real usage line.
func TestReadRolloutUsage_SkipsNullInfo(t *testing.T) {
	t.Parallel()
	p := writeRollout(t, tokenCountLine,
		`{"type":"event_msg","payload":{"type":"token_count","info":null,"rate_limits":{}}}`)
	tokens, ok := readRolloutUsage(p)
	if !ok || tokens != 14072 {
		t.Errorf("readRolloutUsage = %d, %v; want 14072, true", tokens, ok)
	}
}

func TestReadRolloutUsage_Refusals(t *testing.T) {
	t.Parallel()
	if _, ok := readRolloutUsage("relative/rollout.jsonl"); ok {
		t.Error("relative path accepted")
	}
	if _, ok := readRolloutUsage(filepath.Join(t.TempDir(), "missing.jsonl")); ok {
		t.Error("missing file reported ok")
	}
	p := writeRollout(t, `{"type":"session_meta"}`)
	if _, ok := readRolloutUsage(p); ok {
		t.Error("rollout without token_count reported ok")
	}
	notJSONL := filepath.Join(t.TempDir(), "rollout.txt")
	if err := os.WriteFile(notJSONL, []byte(tokenCountLine+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := readRolloutUsage(notJSONL); ok {
		t.Error("non-.jsonl path accepted")
	}
	if _, ok := readRolloutUsage(t.TempDir()); ok {
		t.Error("directory accepted")
	}
}

func TestReadRolloutUsage_TailOnly(t *testing.T) {
	t.Parallel()
	pad := strings.Repeat(`{"type":"event_msg","payload":{"type":"agent_message","message":"`+strings.Repeat("x", 1000)+`"}}`+"\n", 400)
	p := filepath.Join(t.TempDir(), "big.jsonl")
	if err := os.WriteFile(p, []byte(pad+tokenCountLine+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tokens, ok := readRolloutUsage(p)
	if !ok || tokens != 14072 {
		t.Errorf("tail read failed: %d %v", tokens, ok)
	}
}
