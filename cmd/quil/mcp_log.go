package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/artyomsv/quil/internal/config"
)

// mcpLogger writes per-pane MCP interaction logs with sensitive data redaction.
type mcpLogger struct {
	dir string
	mu  sync.Mutex
}

func newMCPLogger(cfg config.MCPConfig) *mcpLogger {
	dir := config.MCPLogDir(cfg)
	if err := os.MkdirAll(dir, 0700); err != nil {
		log.Printf("mcp-log: create dir %s: %v", dir, err)
	}
	return &mcpLogger{dir: dir}
}

// Log writes a metadata-only entry to the per-pane log file.
// The detail string is passed through Layer 2 regex redaction as a safety net.
func (l *mcpLogger) Log(paneID, tool, detail string) {
	if paneID == "" {
		return
	}
	// Layer 2: regex fallback catches common secret patterns in detail strings
	detail = redactSecrets(detail)

	entry := fmt.Sprintf("[%s] %s pane=%s %s\n",
		time.Now().UTC().Format(time.RFC3339), tool, paneID, detail)

	l.mu.Lock()
	defer l.mu.Unlock()

	// Sanitize paneID to prevent path traversal (CWE-22)
	safeName := filepath.Base(paneID)
	path := filepath.Join(l.dir, safeName+".log")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	f.WriteString(entry)
}

// Redaction: two-layer approach

// Layer 1: AI-assisted markers
var redactMarkerRe = regexp.MustCompile(`<<REDACT>>.*?<</REDACT>>`)

// stripRedactMarkers removes markers and returns the clean text for PTY.
func stripRedactMarkers(s string) string {
	return redactMarkerRe.ReplaceAllStringFunc(s, func(match string) string {
		inner := strings.TrimPrefix(match, "<<REDACT>>")
		inner = strings.TrimSuffix(inner, "<</REDACT>>")
		return inner
	})
}

// countRedactMarkers returns the number of redaction markers in the string.
func countRedactMarkers(s string) int {
	return len(redactMarkerRe.FindAllString(s, -1))
}

// Layer 2: Regex fallback for common secret patterns
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`sk-[a-zA-Z0-9]{20,}`),                                    // OpenAI keys
	regexp.MustCompile(`ghp_[a-zA-Z0-9]{36,}`),                                   // GitHub PATs
	regexp.MustCompile(`ghs_[a-zA-Z0-9]{36,}`),                                   // GitHub app tokens
	regexp.MustCompile(`eyJ[a-zA-Z0-9_-]{20,}\.eyJ[a-zA-Z0-9_-]{20,}`),           // JWT tokens
	regexp.MustCompile(`(?i)(password|passwd|secret|token|api_key)\s*[=:]\s*\S+`), // key=value secrets
	regexp.MustCompile(`[0-9a-fA-F]{64,}`),                                        // long hex (private keys, min 64 to avoid git SHAs)
	regexp.MustCompile(`xprv[a-zA-Z0-9]{100,}|xpub[a-zA-Z0-9]{100,}`),            // BIP-32 extended keys
}

// redactSecrets applies regex fallback to catch common secret patterns.
func redactSecrets(s string) string {
	for _, re := range secretPatterns {
		s = re.ReplaceAllString(s, "[REDACTED]")
	}
	return s
}
