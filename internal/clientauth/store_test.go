package clientauth

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func openTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tokens.json")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	return s, path
}

// mustCreate is Create for a fixture: a failed create fails the test instead
// of handing it a zero Entry whose id matches nothing.
func mustCreate(t *testing.T, s *Store, name string, rights Level, expires *time.Time) (string, Entry) {
	t.Helper()
	tok, e, err := s.Create(name, rights, expires)
	if err != nil {
		t.Fatalf("Create(%q): %v", name, err)
	}
	return tok, e
}

func TestStore_CreatePersistsNoSecret(t *testing.T) {
	s, path := openTestStore(t)
	tok, e, err := s.Create("laptop", LevelStandard, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	secret := strings.SplitN(strings.TrimPrefix(tok, "qtk_"), "_", 2)[1]
	if strings.Contains(string(data), secret) {
		t.Fatal("tokens.json contains the token secret")
	}
	if !strings.Contains(string(data), e.StoredKey) || !strings.Contains(string(data), `"rights": "standard"`) {
		t.Fatalf("tokens.json lacks the verifier or rights:\n%s", data)
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(path)
		if perm := fi.Mode().Perm(); perm != 0o600 {
			t.Fatalf("tokens.json mode %04o, want 0600", perm)
		}
	}
	re, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := re.List(); len(got) != 1 || got[0].ID != e.ID || got[0].Name != "laptop" {
		t.Fatalf("reopened store = %+v", got)
	}
}

func TestStore_NamesValidatedAndUnique(t *testing.T) {
	s, _ := openTestStore(t)
	if _, _, err := s.Create("CI", LevelFull, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Create("ci", LevelFull, nil); !errors.Is(err, ErrNameTaken) {
		t.Errorf("case-insensitive duplicate: err = %v", err)
	}
	bidi := "evil" + string(rune(0x202e)) + "txt"
	for _, bad := range []string{"", "   ", strings.Repeat("x", 33), bidi, "tab\tname"} {
		if _, _, err := s.Create(bad, LevelFull, nil); !errors.Is(err, ErrBadName) {
			t.Errorf("Create(%q) err = %v, want ErrBadName", bad, err)
		}
	}
}

func TestStore_AdmitVerifiesThenExpiry(t *testing.T) {
	s, _ := openTestStore(t)
	tok, e := mustCreate(t, s, "a", LevelReadOnly, nil)
	am := AuthMessage(e.ID, "nc", "ns")
	var admitted []string
	_, _, err := s.Admit(e.ID, time.Now(), func(v Verifier) bool { return VerifyProof(v, am, ClientProof(tok, am)) },
		func(x Entry) { admitted = append(admitted, x.ID) })
	if err != nil || len(admitted) != 1 {
		t.Fatalf("good proof: err=%v admitted=%v", err, admitted)
	}
	if _, _, err := s.Admit(e.ID, time.Now(), func(Verifier) bool { return false }, func(Entry) { t.Error("admitted a bad proof") }); !errors.Is(err, ErrRefused) {
		t.Fatalf("bad proof err = %v", err)
	}
	past := time.Now().Add(-time.Hour)
	tok2, e2 := mustCreate(t, s, "old", LevelFull, &past)
	am2 := AuthMessage(e2.ID, "nc", "ns")
	if _, _, err := s.Admit(e2.ID, time.Now(), func(v Verifier) bool { return VerifyProof(v, am2, ClientProof(tok2, am2)) },
		func(Entry) { t.Error("admitted an expired token") }); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired err = %v", err)
	}
	// A wrong proof against an EXPIRED token still reads as refused, never as
	// expired: the proof is checked before expiry, so expiry is never
	// revealed to someone who does not hold the key.
	if _, _, err := s.Admit(e2.ID, time.Now(), func(Verifier) bool { return false },
		func(Entry) { t.Error("admitted an expired token with a bad proof") }); !errors.Is(err, ErrRefused) {
		t.Fatalf("expired + bad proof err = %v, want ErrRefused", err)
	}
}

// The dummy-key path: an unknown id still runs verify, so the answer time
// does not reveal which ids exist.
func TestStore_AdmitUnknownIDVerifiesDummy(t *testing.T) {
	s, _ := openTestStore(t)
	called := false
	_, _, err := s.Admit("deadbeef", time.Now(), func(v Verifier) bool { called = len(v.StoredKey) == 32; return true },
		func(Entry) { t.Error("admitted an unknown id") })
	if !called {
		t.Fatal("verify was not run against the dummy verifier for an unknown id")
	}
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("err = %v, want ErrRefused", err)
	}
}

// fixedToken is a well-formed token whose id holds hex LETTERS, so
// upper-casing it always changes it (a random id can be all digits, and then
// the case-insensitive path below would silently not run).
const fixedToken = "qtk_0a1b2c3d_AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8"

func TestStore_RevokeTargetResolution(t *testing.T) {
	s, path := openTestStore(t)
	s.mint = func() (string, string, error) { return fixedToken, "0a1b2c3d", nil }
	_, laptop, err := s.Create("Laptop", LevelStandard, nil)
	if err != nil || laptop.ID != "0a1b2c3d" {
		t.Fatalf("fixture: %+v %v", laptop, err)
	}
	s.mint = NewToken
	// A token whose NAME equals another token's id: an exact id wins.
	_, impostor := mustCreate(t, s, laptop.ID, LevelStandard, nil)
	if upper := strings.ToUpper(laptop.ID); upper == laptop.ID {
		t.Fatalf("fixture id %q has no hex letter: the case-insensitive path cannot run", laptop.ID)
	}
	var seen []string
	got, err := s.Revoke(laptop.ID, func(e Entry) { seen = append(seen, e.ID) })
	if err != nil || got.ID != laptop.ID || len(seen) != 1 {
		t.Fatalf("revoke by id: %+v %v %v", got, err, seen)
	}
	if got, err := s.Revoke(strings.ToUpper(laptop.ID), nil); err != nil || got.ID != impostor.ID {
		t.Fatalf("revoke by (case-insensitive) name: %+v %v", got, err)
	}
	if _, err := s.Revoke("nope", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown target err = %v", err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), laptop.ID+`"`) {
		t.Fatal("tokens.json still lists a revoked token")
	}
}

func TestStore_RevokeWriteFailureKeepsEntry(t *testing.T) {
	s, _ := openTestStore(t)
	_, e := mustCreate(t, s, "a", LevelFull, nil)
	s.rename = func(string, string) error { return errors.New("disk full") }
	if _, err := s.Revoke(e.ID, func(Entry) { t.Error("onRevoked ran although the write failed") }); err == nil {
		t.Fatal("revoke reported success with an unwritten file")
	}
	if len(s.List()) != 1 {
		t.Fatal("a failed revoke dropped the entry from memory")
	}
}

func TestStore_ExpireSweepSkipsNever(t *testing.T) {
	s, _ := openTestStore(t)
	soon := time.Now().Add(time.Minute)
	_, short := mustCreate(t, s, "short", LevelFull, &soon)
	mustCreate(t, s, "forever", LevelFull, nil)
	var expired []string
	s.ExpireSweep(time.Now().Add(2*time.Minute), func(e Entry) { expired = append(expired, e.ID) })
	if len(expired) != 1 || expired[0] != short.ID {
		t.Fatalf("expired = %v, want [%s]", expired, short.ID)
	}
}

func TestStore_TouchLastUsedThrottlesPersist(t *testing.T) {
	s, _ := openTestStore(t)
	_, e := mustCreate(t, s, "a", LevelFull, nil)
	now := time.Unix(1_800_000_000, 0)
	if !s.TouchLastUsed(e.ID, now) {
		t.Fatal("first touch did not ask for a write")
	}
	if s.TouchLastUsed(e.ID, now.Add(30*time.Second)) {
		t.Fatal("a second touch inside 60 s asked for a write")
	}
	if !s.TouchLastUsed(e.ID, now.Add(61*time.Second)) {
		t.Fatal("a touch after 60 s did not ask for a write")
	}
	if got := s.List()[0].LastUsed; got == nil || !got.Equal(now.Add(61*time.Second).UTC()) {
		t.Fatalf("LastUsed = %v", got)
	}
}

// TestStore_CreateRetriesOnMintCollision covers the case where a freshly
// minted token id already names an entry in the store: Create must mint
// again rather than silently overwrite the existing entry.
func TestStore_CreateRetriesOnMintCollision(t *testing.T) {
	s, _ := openTestStore(t)
	_, first, err := s.Create("first", LevelFull, nil)
	if err != nil {
		t.Fatal(err)
	}
	realMint := NewToken
	calls := 0
	s.mint = func() (string, string, error) {
		calls++
		if calls == 1 {
			// Collide with the id already in the store.
			return fixedToken, first.ID, nil
		}
		return realMint()
	}
	tok2, second, err := s.Create("second", LevelFull, nil)
	if err != nil {
		t.Fatalf("Create did not recover from a mint collision: %v", err)
	}
	if calls < 2 {
		t.Fatalf("mint called %d time(s), want a retry after the collision", calls)
	}
	if second.ID == first.ID {
		t.Fatal("Create kept a colliding id instead of minting again")
	}
	if tok2 == "" {
		t.Fatal("Create returned an empty token after retrying")
	}
	if got := s.List(); len(got) != 2 {
		t.Fatalf("store has %d entries, want 2 (the collision must not overwrite the first entry)", len(got))
	}
}

// TestStore_CreateFailsOnMintCollisions covers the exact bound on the retry:
// a mint seam that always returns an id already in the store must make
// exactly maxMintAttempts calls, then report an error instead of looping
// forever or trying one call more or fewer than the documented bound.
func TestStore_CreateFailsOnMintCollisions(t *testing.T) {
	s, _ := openTestStore(t)
	_, first, err := s.Create("first", LevelFull, nil)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	s.mint = func() (string, string, error) {
		calls++
		return fixedToken, first.ID, nil
	}
	if _, _, err := s.Create("second", LevelFull, nil); err == nil {
		t.Fatal("Create succeeded despite a mint seam that never produces a free id")
	}
	if calls != maxMintAttempts {
		t.Fatalf("mint called %d time(s), want exactly %d", calls, maxMintAttempts)
	}
	if got := s.List(); len(got) != 1 {
		t.Fatalf("store has %d entries after a failed Create, want 1 (unchanged)", len(got))
	}
}

// TestStore_CallbacksRunUnderTheLock pins the store's main security
// property: Admit, Revoke and ExpireSweep call their callback WHILE the
// store's lock is still held, so there is no window in which a login can
// register between a revoke's removal and its write, or between an expiry
// sweep's check and its report. Each callback asserts TryLock() fails.
func TestStore_CallbacksRunUnderTheLock(t *testing.T) {
	s, _ := openTestStore(t)
	assertLocked := func(t *testing.T, who string) {
		t.Helper()
		if s.mu.TryLock() {
			s.mu.Unlock()
			t.Errorf("%s's callback ran without the store lock held", who)
		}
	}

	tok, e := mustCreate(t, s, "a", LevelFull, nil)
	am := AuthMessage(e.ID, "nc", "ns")
	admitChecked := false
	if _, _, err := s.Admit(e.ID, time.Now(), func(v Verifier) bool { return VerifyProof(v, am, ClientProof(tok, am)) },
		func(Entry) { admitChecked = true; assertLocked(t, "Admit") }); err != nil || !admitChecked {
		t.Fatalf("Admit: err=%v checked=%v", err, admitChecked)
	}

	revokeChecked := false
	if _, err := s.Revoke(e.ID, func(Entry) { revokeChecked = true; assertLocked(t, "Revoke") }); err != nil || !revokeChecked {
		t.Fatalf("Revoke: err=%v checked=%v", err, revokeChecked)
	}

	soon := time.Now().Add(time.Minute)
	_, short := mustCreate(t, s, "short", LevelFull, &soon)
	sweepChecked := false
	s.ExpireSweep(time.Now().Add(2*time.Minute), func(x Entry) {
		if x.ID != short.ID {
			return
		}
		sweepChecked = true
		assertLocked(t, "ExpireSweep")
	})
	if !sweepChecked {
		t.Fatal("ExpireSweep did not report the expired entry")
	}
}

// TestOpenStore_DropsInvalidEntriesAndCounts covers loading a file written
// by something other than this Store: an entry with a malformed id, an
// unrecognized rights level, or a blank/missing rights field is dropped
// rather than kept half-understood, and the count is exposed so the daemon
// can log it instead of silently serving fewer tokens than the file on disk
// names. A blank or absent "rights" is deliberately NOT the same as
// ParseLevel("")'s CLI default (LevelStandard, no error): that default is
// for a human leaving `--rights` unset on the command line, not for
// reinterpreting a corrupt or hand-edited on-disk record as standard rights.
func TestOpenStore_DropsInvalidEntriesAndCounts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.json")
	raw := `{"version":1,"tokens":[
		{"id":"0a1b2c3d","name":"good","stored_key":"00","server_key":"00","rights":"standard","created":"2026-01-01T00:00:00Z"},
		{"id":"not-hex!","name":"bad-id","stored_key":"00","server_key":"00","rights":"standard","created":"2026-01-01T00:00:00Z"},
		{"id":"deadbeef","name":"bad-rights","stored_key":"00","server_key":"00","rights":"super-admin","created":"2026-01-01T00:00:00Z"},
		{"id":"cafebabe","name":"blank-rights","stored_key":"00","server_key":"00","rights":"","created":"2026-01-01T00:00:00Z"},
		{"id":"f00dfeed","name":"missing-rights","stored_key":"00","server_key":"00","created":"2026-01-01T00:00:00Z"}
	]}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Dropped(); got != 4 {
		t.Fatalf("Dropped() = %d, want 4", got)
	}
	if got := s.List(); len(got) != 1 || got[0].ID != "0a1b2c3d" {
		t.Fatalf("List() = %+v, want only the valid entry", got)
	}
}

// TestOpenStore_RefusesANewerVersion covers a tokens.json written by a later
// build: OpenStore must refuse it outright rather than silently treat it as
// v1 and risk rewriting away fields this build does not know about.
func TestOpenStore_RefusesANewerVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.json")
	if err := os.WriteFile(path, []byte(`{"version":2,"tokens":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(path); err == nil {
		t.Fatal("OpenStore accepted a tokens.json from a newer version")
	}
}

func TestParseExpiry(t *testing.T) {
	tests := []struct {
		in    string
		days  int
		never bool
		ok    bool
	}{
		{"", 90, false, true}, {"90d", 90, false, true}, {"1d", 1, false, true},
		{"never", 0, true, true}, {"0d", 0, false, false}, {"3651d", 0, false, false},
		{"90", 0, false, false}, {"-5d", 0, false, false},
	}
	for _, tt := range tests {
		days, never, err := ParseExpiry(tt.in)
		if (err == nil) != tt.ok || (tt.ok && (days != tt.days || never != tt.never)) {
			t.Errorf("ParseExpiry(%q) = %d %v %v", tt.in, days, never, err)
		}
	}
}

// A repeated id keeps the FIRST entry. The map used to take the last, so a
// line appended to the file replaced the token already listed under that id,
// and nothing counted it.
func TestOpenStore_DuplicateIDKeepsTheFirst(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.json")
	raw := `{"version":1,"tokens":[
		{"id":"0a1b2c3d","name":"first","stored_key":"00","server_key":"00","rights":"read-only","created":"2026-01-01T00:00:00Z"},
		{"id":"0a1b2c3d","name":"second","stored_key":"00","server_key":"00","rights":"full","created":"2026-01-02T00:00:00Z"}
	]}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Dropped(); got != 1 {
		t.Fatalf("Dropped() = %d, want 1", got)
	}
	if got := s.List(); len(got) != 1 || got[0].Name != "first" || got[0].Rights != LevelReadOnly {
		t.Fatalf("List() = %+v, want only the first entry", got)
	}
}

// A known id whose stored keys do not decode, or decode to the wrong length,
// is refused with an error that names the token for the daemon's log. verify
// still runs, against the dummy, so the answer takes as long as for an
// unknown id. A wrong-length key used to pass Verifier and read as a plain
// wrong proof, with nothing logged.
func TestStore_AdmitCorruptVerifierNamesTheToken(t *testing.T) {
	good := strings.Repeat("ab", 32)
	for name, keys := range map[string][2]string{
		"stored key not hex":      {"not hex", good},
		"server key not hex":      {good, "zz"},
		"stored key one byte":     {"00", good},
		"server key short":        {good, strings.Repeat("ab", 31)},
		"stored key one too many": {good + "ab", good},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "tokens.json")
			raw := `{"version":1,"tokens":[{"id":"0a1b2c3d","name":"damaged","stored_key":"` + keys[0] +
				`","server_key":"` + keys[1] + `","rights":"full","created":"2026-01-01T00:00:00Z"}]}`
			if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			s, err := OpenStore(path)
			if err != nil {
				t.Fatal(err)
			}
			called := false
			_, _, err = s.Admit("0a1b2c3d", time.Now(), func(v Verifier) bool { called = len(v.StoredKey) == 32; return true },
				func(Entry) { t.Error("admitted a token with a corrupt verifier") })
			if !called {
				t.Fatal("verify was not run against the dummy verifier")
			}
			if !errors.Is(err, ErrRefused) || !errors.Is(err, ErrCorruptVerifier) || !strings.Contains(err.Error(), "0a1b2c3d") {
				t.Fatalf("err = %v, want ErrRefused + ErrCorruptVerifier naming the id", err)
			}
		})
	}
}

func TestStore_ExpireSweepNilCallback(t *testing.T) {
	s, _ := openTestStore(t)
	past := time.Now().Add(-time.Hour)
	mustCreate(t, s, "old", LevelFull, &past)
	s.ExpireSweep(time.Now(), nil) // panicked on the expired entry
}

// Create keeps its own copy of the expiry: the caller's time.Time is the
// caller's to reuse.
func TestStore_CreateCopiesTheExpiry(t *testing.T) {
	s, _ := openTestStore(t)
	exp := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	_, e := mustCreate(t, s, "a", LevelFull, &exp)
	exp = exp.Add(-24 * 365 * time.Hour)
	if got := s.List()[0].Expires; got == nil || !got.Equal(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("stored expiry = %v: the caller's change reached the store", got)
	}
	if e.Expires == &exp {
		t.Fatal("the returned entry shares the caller's pointer")
	}
}

// writeLocked syncs the parent directory AFTER the rename, since the rename
// is what the directory sync makes durable. A sync failure does not fail the
// write: the file is already replaced, and a failed Revoke would put back an
// entry the disk no longer lists.
func TestStore_WriteSyncsTheParentAfterRename(t *testing.T) {
	s, path := openTestStore(t)
	var calls []string
	s.rename = func(oldpath, newpath string) error {
		calls = append(calls, "rename")
		return os.Rename(oldpath, newpath)
	}
	s.syncDir = func(dir string) error {
		calls = append(calls, "sync "+dir)
		return errors.New("sync refused")
	}
	if _, _, err := s.Create("a", LevelFull, nil); err != nil {
		t.Fatalf("a failed directory sync failed the write: %v", err)
	}
	if want := []string{"rename", "sync " + filepath.Dir(path)}; strings.Join(calls, "|") != strings.Join(want, "|") {
		t.Fatalf("calls = %q, want %q", calls, want)
	}
}
