package daemon

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/logger"
)

// audit.log: JSON lines in its own rotated file, the only record of network
// logins. NEVER written here, at any level: a token, a stored or server key,
// a nonce, a proof, terminal output, note text, pane input.
const (
	auditFile     = "audit.log"
	auditMaxBytes = 5 << 20
	auditMaxFiles = 10
	// maxAuditField bounds every client-supplied string in a line.
	maxAuditField = 64
)

// auditEntry is one line. Every client-supplied value is a JSON string, so a
// chosen name cannot forge a second line.
type auditEntry struct {
	TS        string `json:"ts"`
	Event     string `json:"event"`
	Transport string `json:"transport,omitempty"`
	ClientID  string `json:"client_id,omitempty"`
	Kind      string `json:"kind,omitempty"` // self-declared
	TokenID   string `json:"token_id,omitempty"`
	TokenName string `json:"token_name,omitempty"`
	Rights    string `json:"rights,omitempty"`
	Type      string `json:"type,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// protectAuditFile is ipc.ProtectFile, a seam so a test can make it fail.
var protectAuditFile = ipc.ProtectFile

// preLoginAuditPerMinute caps the lines written for conns that have not
// logged in (tcp_connect, login_failed, their tcp_disconnect). Anyone who
// can reach loopback can open conns without a token; uncapped, a fast loop
// of failed connects rotates every older line out of the 5 MiB x 10 log —
// the logins and privileged requests it exists to keep. Lines past the cap
// are counted and written as ONE audit_suppressed line per minute.
const preLoginAuditPerMinute = 120

// auditBudget is that cap: a fixed one-minute window, daemon-wide.
type auditBudget struct {
	mu         sync.Mutex
	window     time.Time
	used       int
	suppressed int
}

// take reports whether a pre-login line may be written now, and how many
// lines the window that just ended suppressed (to report before this one).
func (b *auditBudget) take(now time.Time) (ok bool, ended int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	ended = b.rollLocked(now)
	if b.used < preLoginAuditPerMinute {
		b.used++
		return true, ended
	}
	b.suppressed++
	return false, ended
}

// drain returns the count of an ENDED window, so a flood that stopped is
// still reported without waiting for the next pre-login line. force ends
// the current window as well (shutdown).
func (b *auditBudget) drain(now time.Time, force bool) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	if force {
		n := b.suppressed
		b.suppressed, b.used, b.window = 0, 0, time.Time{}
		return n
	}
	return b.rollLocked(now)
}

func (b *auditBudget) rollLocked(now time.Time) int {
	if !b.window.IsZero() && now.Sub(b.window) < time.Minute {
		return 0
	}
	n := b.suppressed
	b.window, b.used, b.suppressed = now, 0, 0
	return n
}

func suppressedEntry(n int) auditEntry {
	return auditEntry{Event: "audit_suppressed", Transport: ipc.TransportTCP,
		Reason: strconv.Itoa(n) + " pre-login lines over the per-minute cap were not written"}
}

type auditLog struct {
	mu  sync.Mutex
	w   io.WriteCloser
	now func() time.Time
}

// openAuditLog opens QUIL_HOME/audit.log with its own 5 MiB x 10 cap.
func openAuditLog(dir string) (*auditLog, error) {
	w, err := logger.NewRotatingWriter(dir, auditFile, auditMaxBytes, auditMaxFiles)
	if err != nil {
		return nil, err
	}
	// A log other accounts can read names every token and client that logged
	// in, so an unprotected one is treated as one that did not open: the TCP
	// listener stays off, the unix socket is unaffected.
	if err := protectAuditFile(filepath.Join(dir, auditFile)); err != nil {
		if cerr := w.Close(); cerr != nil {
			log.Printf("audit: close: %v", cerr)
		}
		return nil, fmt.Errorf("restrict %s to this account: %w", auditFile, err)
	}
	return &auditLog{w: w, now: time.Now}, nil
}

func (a *auditLog) write(e auditEntry) {
	if a == nil {
		return
	}
	e.TS = a.now().UTC().Format(time.RFC3339Nano)
	line, err := json.Marshal(e)
	if err != nil {
		log.Printf("audit: marshal %s: %v", e.Event, err)
		return
	}
	line = append(line, '\n')
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, err := a.w.Write(line); err != nil {
		log.Printf("audit: write %s: %v", e.Event, err)
	}
}

func (a *auditLog) Close() error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.w.Close()
}
