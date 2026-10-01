package daemon

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func readAudit(t *testing.T, dir string) []auditEntry {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, auditFile))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []auditEntry
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e auditEntry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("audit line is not JSON: %q: %v", sc.Text(), err)
		}
		out = append(out, e)
	}
	return out
}

func TestAuditLog_OneJSONLinePerEvent(t *testing.T) {
	dir := t.TempDir()
	a, err := openAuditLog(dir)
	if err != nil {
		t.Fatal(err)
	}
	a.now = func() time.Time { return time.Unix(1_800_000_000, 0) }
	forged := "x\n{\"event\":\"login_ok\"}"
	a.write(auditEntry{Event: "login_failed", Transport: "tcp", ClientID: forged, Reason: "token refused"})
	a.write(auditEntry{Event: "tcp_disconnect", Transport: "tcp"})
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	got := readAudit(t, dir)
	if len(got) != 2 {
		t.Fatalf("%d lines, want 2 — a client value forged a line", len(got))
	}
	if got[0].ClientID != forged || got[0].Event != "login_failed" || got[0].TS == "" {
		t.Fatalf("entry = %+v", got[0])
	}
}

// GUARD (ruling P-7): cannot fail once auditLog exists. It pins the nil-safety
// every caller relies on when audit.log could not be opened (d.audit == nil).
func TestAuditLog_NilIsANoOp(t *testing.T) {
	var a *auditLog
	a.write(auditEntry{Event: "x"})
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAuditLog_FileIsOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows DACLs are covered by internal/ipc's native tests")
	}
	dir := t.TempDir()
	a, err := openAuditLog(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	fi, err := os.Stat(filepath.Join(dir, auditFile))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("audit.log mode %04o, want 0600", perm)
	}
}
