package clientauth

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/textsafe"
)

var (
	ErrRefused   = errors.New("token refused")
	ErrExpired   = errors.New("token expired")
	ErrNotFound  = errors.New("no such token")
	ErrNameTaken = errors.New("a token with that name exists")
	ErrBadName   = errors.New("invalid token name")
)

const (
	// DefaultExpiry is what `--expires` defaults to.
	DefaultExpiry        = "90d"
	maxExpiryDays        = 3650
	maxNameRunes         = 32
	lastUsedPersistEvery = time.Minute
	storeVersion         = 1
	// maxMintAttempts bounds Create's retry when a freshly minted token id
	// collides with one already in the store: at most this many mint() calls
	// are made before Create gives up. The id is 8 hex digits (32 bits), so a
	// collision is rare but not impossible, and a loop with no bound would
	// hang the daemon instead of reporting the problem.
	maxMintAttempts = 8
)

// Entry is one stored token. The token itself is never stored.
type Entry struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	StoredKey string     `json:"stored_key"` // hex
	ServerKey string     `json:"server_key"` // hex
	Rights    Level      `json:"rights"`
	Created   time.Time  `json:"created"`
	Expires   *time.Time `json:"expires,omitempty"` // nil = never
	LastUsed  *time.Time `json:"last_used,omitempty"`
}

// Verifier decodes the stored keys.
func (e Entry) Verifier() (Verifier, error) {
	sk, err1 := hex.DecodeString(e.StoredKey)
	vk, err2 := hex.DecodeString(e.ServerKey)
	if err := errors.Join(err1, err2); err != nil {
		return Verifier{}, fmt.Errorf("token %s: corrupt verifier: %w", e.ID, err)
	}
	return Verifier{StoredKey: sk, ServerKey: vk}, nil
}

// ExpiredAt is checked against the daemon's own clock, not any client-supplied
// time.
func (e Entry) ExpiredAt(now time.Time) bool {
	return e.Expires != nil && !now.Before(*e.Expires)
}

type storeFile struct {
	Version int     `json:"version"`
	Tokens  []Entry `json:"tokens"`
}

// Store is tokens.json. Its mutex is the admission lock: Admit, Revoke and
// ExpireSweep run their callbacks under it, so a login either registers
// before a revoke (and the revoke finds it) or runs after it (and finds no
// entry).
type Store struct {
	path        string
	mu          sync.Mutex
	entries     map[string]*Entry
	dummy       Verifier
	lastPersist map[string]time.Time
	now         func() time.Time
	rename      func(oldpath, newpath string) error
	// mint is NewToken; a test seam so a fixture can pin a token id.
	mint func() (token, id string, err error)
	// dropped is how many entries OpenStore discarded on load (a bad id or an
	// unknown rights level). Set once at construction, before the Store is
	// shared with any other goroutine, so reading it later needs no lock.
	dropped int
}

// Dropped reports how many entries OpenStore discarded on load, so the
// daemon can log it instead of silently serving a smaller token list than
// the file on disk names.
func (s *Store) Dropped() int { return s.dropped }

// OpenStore loads path; a missing file is an empty store. The path is a
// parameter (the internal/keymap pattern), so tests never touch QUIL_HOME.
// An entry with an invalid id or an unrecognized rights level is dropped
// rather than kept half-understood; a file whose version is newer than this
// build supports is refused outright rather than silently reread as v1 and
// possibly rewritten with fields this build does not know about.
func OpenStore(path string) (*Store, error) {
	dummyToken, _, err := NewToken()
	if err != nil {
		return nil, err
	}
	s := &Store{
		path:        path,
		entries:     map[string]*Entry{},
		dummy:       DeriveVerifier(dummyToken),
		lastPersist: map[string]time.Time{},
		now:         time.Now,
		rename:      os.Rename,
		mint:        NewToken,
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var f storeFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if f.Version > storeVersion {
		return nil, fmt.Errorf("%s: version %d is newer than this build supports (%d)", path, f.Version, storeVersion)
	}
	for i := range f.Tokens {
		e := f.Tokens[i]
		if !ValidID(e.ID) {
			s.dropped++
			continue
		}
		if _, err := ParseLevel(string(e.Rights)); err != nil {
			s.dropped++
			continue
		}
		s.entries[e.ID] = &e
	}
	return s, nil
}

// ValidName: 1-32 runes, not blank, no control or bidi characters (the
// internal/textsafe rule), so a name cannot spoof a list row or a log line.
func ValidName(name string) error {
	if strings.TrimSpace(name) == "" || utf8.RuneCountInString(name) > maxNameRunes {
		return fmt.Errorf("%w: 1-%d characters", ErrBadName, maxNameRunes)
	}
	if textsafe.HasStripped(name) {
		return fmt.Errorf("%w: control or bidi characters", ErrBadName)
	}
	return nil
}

// ParseExpiry reads "<N>d" (1..3650) or "never"; "" is DefaultExpiry.
func ParseExpiry(s string) (days int, never bool, err error) {
	if s == "" {
		s = DefaultExpiry
	}
	if s == "never" {
		return 0, true, nil
	}
	num, ok := strings.CutSuffix(s, "d")
	if !ok {
		return 0, false, fmt.Errorf("expiry %q: want <N>d or never", s)
	}
	n, err := strconv.Atoi(num)
	if err != nil || n < 1 || n > maxExpiryDays {
		return 0, false, fmt.Errorf("expiry %q: N must be 1..%d", s, maxExpiryDays)
	}
	return n, false, nil
}

// ExpiryFrom turns an expiry spec into an instant; nil means never.
func ExpiryFrom(s string, now time.Time) (*time.Time, error) {
	days, never, err := ParseExpiry(s)
	if err != nil || never {
		return nil, err
	}
	t := now.Add(time.Duration(days) * 24 * time.Hour).UTC()
	return &t, nil
}

// Create mints a token, stores its verifier and returns the token ONCE.
func (s *Store) Create(name string, rights Level, expires *time.Time) (string, Entry, error) {
	if err := ValidName(name); err != nil {
		return "", Entry{}, err
	}
	if rights == "" {
		rights = LevelStandard
	}
	if _, err := ParseLevel(string(rights)); err != nil {
		return "", Entry{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.entries {
		if strings.EqualFold(e.Name, name) {
			return "", Entry{}, ErrNameTaken
		}
	}
	// The id is 8 hex digits (32 bits): a fresh mint can collide with an
	// entry already in the store. Retry with an exact bound — at most
	// maxMintAttempts calls to mint() — rather than silently overwrite the
	// existing entry or loop forever.
	var token, id string
	minted := false
	for attempt := 0; attempt < maxMintAttempts; attempt++ {
		t, tid, err := s.mint()
		if err != nil {
			return "", Entry{}, err
		}
		if _, taken := s.entries[tid]; !taken {
			token, id = t, tid
			minted = true
			break
		}
	}
	if !minted {
		return "", Entry{}, errors.New("could not mint a unique token id")
	}
	v := DeriveVerifier(token)
	e := &Entry{
		ID: id, Name: name, Rights: rights, Created: s.now().UTC(), Expires: expires,
		StoredKey: hex.EncodeToString(v.StoredKey), ServerKey: hex.EncodeToString(v.ServerKey),
	}
	s.entries[id] = e
	if err := s.writeLocked(); err != nil {
		delete(s.entries, id)
		return "", Entry{}, err
	}
	return token, *e, nil
}

// List returns copies, oldest first.
func (s *Store) List() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sortedLocked()
}

// Revoke removes the entry, writes tokens.json, and only after the write
// succeeded calls onRevoked — still under the admission lock, so the caller
// can mark every live conn of the token revoked before any new login of it
// can register. An exact id beats a name; names match case-insensitively.
// onRevoked runs while the store's lock is held and must not call back into
// the Store (Admit, Revoke, TouchLastUsed, …) — sync.Mutex is not reentrant,
// so such a call would deadlock.
func (s *Store) Revoke(target string, onRevoked func(Entry)) (Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[target]
	if !ok {
		for _, cand := range s.entries {
			if strings.EqualFold(cand.Name, target) {
				e, ok = cand, true
				break
			}
		}
	}
	if !ok {
		return Entry{}, fmt.Errorf("%w: %s", ErrNotFound, target)
	}
	delete(s.entries, e.ID)
	if err := s.writeLocked(); err != nil {
		s.entries[e.ID] = e
		return Entry{}, err
	}
	delete(s.lastPersist, e.ID)
	if onRevoked != nil {
		onRevoked(*e)
	}
	return *e, nil
}

// Admit is the single admission step, under one lock: lookup, verify, expiry
// check, then admit. An unknown id is verified against a fixed dummy so the
// answer time does not reveal which ids exist; the proof is checked BEFORE
// expiry, so a wrong proof against an expired token still reads as refused,
// never as expired — expiry is not revealed to someone without the key.
// admit runs while the store's lock is held and must not call back into the
// Store (Admit, Revoke, TouchLastUsed, …) — sync.Mutex is not reentrant, so
// such a call would deadlock.
func (s *Store) Admit(id string, now time.Time, verify func(Verifier) bool, admit func(Entry)) (Entry, Verifier, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, known := s.entries[id]
	v := s.dummy
	if known {
		var err error
		if v, err = e.Verifier(); err != nil {
			v = s.dummy
			known = false
		}
	}
	proven := verify(v)
	if !known || !proven {
		return Entry{}, Verifier{}, ErrRefused
	}
	if e.ExpiredAt(now) {
		return Entry{}, Verifier{}, ErrExpired
	}
	if admit != nil {
		admit(*e)
	}
	return *e, v, nil
}

// ExpireSweep calls onExpired for every expired entry, under the lock. An
// entry without an expiry is skipped. Expired entries stay listed. onExpired
// runs while the store's lock is held and must not call back into the Store
// (Admit, Revoke, TouchLastUsed, …) — sync.Mutex is not reentrant, so such a
// call would deadlock.
func (s *Store) ExpireSweep(now time.Time, onExpired func(Entry)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.entries {
		if e.ExpiredAt(now) {
			onExpired(*e)
		}
	}
}

// TouchLastUsed records a login in memory and reports whether a write is due
// (at most one per token per minute). The caller writes on a worker.
func (s *Store) TouchLastUsed(id string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[id]
	if !ok {
		return false
	}
	t := now.UTC()
	e.LastUsed = &t
	if last, seen := s.lastPersist[id]; seen && now.Sub(last) < lastUsedPersistEvery {
		return false
	}
	s.lastPersist[id] = now
	return true
}

// Persist writes the store.
func (s *Store) Persist() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeLocked()
}

func (s *Store) sortedLocked() []Entry {
	out := make([]Entry, 0, len(s.entries))
	for _, e := range s.entries {
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Created.Equal(out[j].Created) {
			return out[i].Created.Before(out[j].Created)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// writeLocked writes temp + rename. The temp file is created PRIVATE (0600;
// on Windows with the owner-only descriptor in CreateFile).
func (s *Store) writeLocked() error {
	data, err := json.MarshalIndent(storeFile{Version: storeVersion, Tokens: s.sortedLocked()}, "", "  ")
	if err != nil {
		return err
	}
	var suffix [6]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return err
	}
	tmp := s.path + ".tmp-" + hex.EncodeToString(suffix[:])
	f, err := ipc.CreatePrivateFile(tmp)
	if err != nil {
		return fmt.Errorf("create %s: %w", tmp, err)
	}
	_, werr := f.Write(append(data, '\n'))
	serr := f.Sync()
	cerr := f.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := s.rename(tmp, s.path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("replace %s: %w", s.path, err)
	}
	return nil
}
