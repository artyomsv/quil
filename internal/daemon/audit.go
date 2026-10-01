package daemon

import (
	"encoding/json"
	"io"
	"log"
	"path/filepath"
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
	if err := ipc.ProtectFile(filepath.Join(dir, auditFile)); err != nil {
		log.Printf("audit: could not restrict %s to this account: %v", auditFile, err)
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
