// Package claudesessions enumerates the Claude Code sessions recorded for a
// working directory. Claude writes one JSONL transcript per session under
// ~/.claude/projects/<escaped-cwd>/<session-id>.jsonl; this package maps a CWD
// to that directory and reads a short, human-recognizable title out of each
// transcript.
//
// Pure reads with no process spawning and no network I/O: every failure
// (missing directory, unreadable file, malformed JSON) degrades to "fewer
// sessions" rather than an error, so session discovery can never block pane
// creation. Titles are sanitized of control characters — a transcript records
// whatever the user typed, and the value is rendered into a TUI.
package claudesessions

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"
)

const (
	// MaxSessions bounds how many sessions List returns. Claude never prunes
	// transcripts, so a long-lived project accumulates hundreds; the newest
	// ones are the only plausible resume targets and the cap keeps both the
	// scan cost and the IPC frame bounded.
	MaxSessions = 200

	// MaxTitleRunes bounds a title before it reaches the wire. Consumers
	// truncate again to their render width; this only stops a pasted wall of
	// text from riding along in every response.
	MaxTitleRunes = 240

	// titleScanBytes caps how much of a transcript head is read while looking
	// for the first typed prompt. Transcripts reach several MB, but the first
	// human prompt sits near the top — the largest offset observed across this
	// repo's 22 transcripts was byte 14442 in a 3 MB file, so 64 KB is a wide
	// margin. A transcript whose first prompt falls outside the window simply
	// yields no title.
	titleScanBytes = 64 << 10

	// MaxPromptRunes bounds each prompt ReadDetail returns. Larger than
	// MaxTitleRunes because the detail view shows a prompt as a paragraph
	// rather than a row, but still bounded: a prompt can carry a pasted file.
	MaxPromptRunes = 1200

	// startScanLines bounds the search for the session's start timestamp. The
	// first entry normally carries one, but a transcript may open with a
	// summary or meta entry that does not.
	startScanLines = 20
)

// Session is one Claude Code transcript discovered for a working directory.
type Session struct {
	// ID is the session UUID — the transcript's filename stem, and the value
	// passed to `claude --resume`.
	ID string
	// Title is the first prompt the user typed in this session, sanitized and
	// truncated. Empty when no typed prompt was found in the scanned head.
	Title string
	// Modified is the transcript file's modification time, i.e. when the
	// session was last active.
	Modified time.Time
}

// EscapeCWD mirrors Claude Code's per-project directory naming under
// ~/.claude/projects/, transcribed from the claude binary (2026-07-05, v2.x):
//
//	t = cwd.replace(/[^a-zA-Z0-9]/g, "-")           // per UTF-16 code unit
//	if (t.length <= 200) return t
//	return t.slice(0, 200) + "-" + base36(abs(h))   // h = Java-31x hash
//	// h: for each UTF-16 unit u: h = (h<<5) - h + u | 0   (int32 wrap)
//
// Operating per UTF-16 code unit (not rune) matches the JS regex without the
// /u flag: an astral char (emoji) becomes TWO dashes. Note the hash argument
// is the ORIGINAL cwd, not the dashified string.
//
// Getting this wrong fails quietly rather than loudly, which is why it is
// transcribed rather than approximated: an earlier version replaced only
// : \ / and _ and missed '.', so any CWD containing a dot probed a
// nonexistent directory and every restored pane fell back to --continue,
// all resuming the SAME session after a daemon restart (2026-07-05 incident).
func EscapeCWD(cwd string) string {
	const maxLen = 200 // claude's truncation threshold
	units := utf16.Encode([]rune(cwd))
	b := make([]byte, len(units))
	for i, u := range units {
		switch {
		case u >= 'a' && u <= 'z', u >= 'A' && u <= 'Z', u >= '0' && u <= '9':
			b[i] = byte(u)
		default:
			b[i] = '-'
		}
	}
	t := string(b)
	if len(t) <= maxLen {
		return t
	}
	var h int32
	for _, u := range units {
		h = (h << 5) - h + int32(u) // int32 wrap == JS |0
	}
	abs := int64(h)
	if abs < 0 {
		abs = -abs // JS Math.abs; int64 widening handles MinInt32 exactly
	}
	return t[:maxLen] + "-" + strconv.FormatInt(abs, 36)
}

// claudeConfigDirEnv is Claude Code's own override for its config directory.
// Setting it relocates everything Claude stores, projects/ included — which is
// how a user separates work from personal config, and how wrapper
// distributions keep their sessions apart from upstream's.
const claudeConfigDirEnv = "CLAUDE_CONFIG_DIR"

// ConfigDir returns Claude Code's config directory: $CLAUDE_CONFIG_DIR when
// set, else ~/.claude. Returns "" when neither can be resolved.
//
// A "~/"-prefixed or relative value is expanded here so every caller gets an
// absolute path. Resolution belongs on whichever machine owns the disk — the
// daemon — because the directory describes that machine's filesystem; a
// client-supplied path would be the laptop's answer about the server's.
func ConfigDir() string {
	if v := strings.TrimSpace(os.Getenv(claudeConfigDirEnv)); v != "" {
		if v == "~" || strings.HasPrefix(v, "~/") || strings.HasPrefix(v, `~\`) {
			home, err := os.UserHomeDir()
			if err != nil {
				return ""
			}
			if v == "~" {
				return home
			}
			return filepath.Join(home, v[2:])
		}
		if abs, err := filepath.Abs(v); err == nil {
			return abs
		}
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude")
}

// ProjectDirIn maps a CWD to its transcript directory under an explicit config
// dir. Takes the directory rather than reading the environment so a caller that
// already resolved one is not at the mercy of a concurrent Setenv, and so tests
// need no environment at all.
func ProjectDirIn(configDir, cwd string) string {
	if configDir == "" || cwd == "" {
		return ""
	}
	return filepath.Join(configDir, "projects", EscapeCWD(cwd))
}

// ProjectDir returns the absolute directory Claude stores this CWD's session
// transcripts in, or "" when the config directory cannot be resolved.
func ProjectDir(cwd string) string {
	return ProjectDirIn(ConfigDir(), cwd)
}

// TranscriptPathIn returns one session's transcript path under an explicit
// config dir, or "" when the directory cannot be resolved or either remaining
// argument is empty. It takes the directory for the same reason ProjectDirIn
// does: a caller that already resolved one must not be at the mercy of a
// concurrent Setenv, and tests need no environment at all.
//
// This is the ONLY place the session-id-to-filename join lives. ReadDetailIn
// used to inline its own copy, which left the exported spelling below with no
// production caller and its test certifying a join nothing ran.
//
// It does NOT validate sessionID, and deliberately so — it is a path
// constructor, not a gate. filepath.Join CLEANS a traversal rather than
// refusing it, so a caller building a path from untrusted input must reject
// separators and ".." itself first. ReadDetailIn does exactly that before it
// calls here; any new caller owes the same check.
func TranscriptPathIn(configDir, cwd, sessionID string) string {
	dir := ProjectDirIn(configDir, cwd)
	if dir == "" || sessionID == "" {
		return ""
	}
	return filepath.Join(dir, sessionID+".jsonl")
}

// TranscriptPath returns the absolute path of one session's transcript, or ""
// when the home directory is unavailable or either argument is empty.
func TranscriptPath(cwd, sessionID string) string {
	return TranscriptPathIn(ConfigDir(), cwd, sessionID)
}

// List returns the sessions recorded for cwd, newest first, capped at
// MaxSessions. truncated reports whether older sessions were dropped by that
// cap, so callers can say so rather than inferring it from a full-looking
// result (a directory holding exactly MaxSessions is not truncated).
//
// A CWD Claude has never been run in has no project directory; that is a normal
// state, not an error, and yields an empty slice with a nil error. Individual
// unreadable transcripts are skipped rather than failing the whole listing.
// A cancelled ctx is treated as one more way to end up with fewer sessions: it
// yields what was gathered, with a nil error. The listing feeds a pane-creation
// dialog that must never be blocked by discovery, so surfacing cancellation as a
// failure there would be a downgrade.
func List(ctx context.Context, cwd string) (sessions []Session, truncated bool, err error) {
	return ListIn(ctx, ConfigDir(), cwd)
}

// ListIn is List against an explicit config directory. List resolves one from
// the environment and delegates here.
func ListIn(ctx context.Context, configDir, cwd string) (sessions []Session, truncated bool, err error) {
	dir := ProjectDirIn(configDir, cwd)
	if dir == "" || ctx.Err() != nil {
		return nil, false, nil
	}
	return listDir(ctx, dir)
}

// listDir is the filesystem half of List, split out so tests can point it at a
// t.TempDir() instead of depending on the real home directory.
func listDir(ctx context.Context, dir string) (sessions []Session, truncated bool, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}

	type candidate struct {
		id   string
		path string
		info os.FileInfo
	}
	candidates := make([]candidate, 0, len(entries))
	for _, e := range entries {
		// Guarded as well as the title loop below, and arguably more
		// importantly: that one is capped at MaxSessions, this one runs over
		// EVERY entry in the project directory and calls e.Info() — a stat per
		// entry on most platforms. The capped loop costs more per item; this
		// one has no bound on how many items there are.
		// Guarded as well as the title loop below, and arguably more
		// importantly: that one is capped at MaxSessions, this one runs over
		// EVERY entry in the project directory and calls e.Info() — a stat per
		// entry on most platforms. The capped loop costs more per item; this
		// one has no bound on how many items there are.
		if ctx.Err() != nil {
			return nil, false, nil
		}
		if !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		// The entry must be a REGULAR file, which rules out two things the
		// suffix alone does not. Session UUIDs also appear as sibling
		// DIRECTORIES (claude keeps per-session scratch state there). And
		// DirEntry.Info() reports lstat data, so a symlink fails IsRegular
		// here rather than being followed by the os.Open in readTitle —
		// matching the symlink discipline in persist/notes.go and
		// opencodehook.ReadPersistedSessionID. That also keeps a
		// symlink-to-FIFO from parking the scan in open(2) forever.
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			continue
		}
		candidates = append(candidates, candidate{
			id:   strings.TrimSuffix(e.Name(), ".jsonl"),
			path: filepath.Join(dir, e.Name()),
			info: info,
		})
	}

	// Newest first. Ties break on ID so the order is deterministic — two
	// transcripts can share an mtime at filesystem timestamp granularity.
	sort.Slice(candidates, func(i, j int) bool {
		ti, tj := candidates[i].info.ModTime(), candidates[j].info.ModTime()
		if ti.Equal(tj) {
			return candidates[i].id < candidates[j].id
		}
		return ti.After(tj)
	})
	if len(candidates) > MaxSessions {
		candidates = candidates[:MaxSessions]
		truncated = true
	}

	// Title extraction is deferred until after the cap so a project with 500
	// transcripts only pays for the 200 that can actually be shown.
	out := make([]Session, 0, len(candidates))
	for _, c := range candidates {
		// The expensive loop: up to MaxSessions × titleScanBytes of reads, and
		// the span the daemon holds its single-flight slot across. Cancelling
		// here returns the titles already read; the rest list by ID.
		if ctx.Err() != nil {
			return out, truncated, nil
		}
		out = append(out, Session{
			ID:       c.id,
			Title:    readTitle(c.path),
			Modified: c.info.ModTime(),
		})
	}
	return out, truncated, nil
}

// jsonPresent records only WHETHER a field appeared, never its value.
//
// json.RawMessage would answer the same question but keep a copy of the whole
// blob: toolUseResult carries entire command outputs, and the rescan decodes
// every tool-result line on a transcript that can reach tens of MB. Presence is
// all isMachinery asks, so nothing is copied.
//
// An explicit null counts as ABSENT. encoding/json hands null to a custom
// unmarshaler rather than skipping it, and this package has already been bitten
// once by treating null as a value — see contentIsString.
type jsonPresent bool

func (p *jsonPresent) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	*p = jsonPresent(len(b) > 0 && !bytes.Equal(b, []byte("null")))
	return nil
}

// transcriptLine mirrors the subset of a transcript entry needed to recognize
// a typed human prompt and pull its text. Extra fields are ignored.
type transcriptLine struct {
	Type         string `json:"type"`
	IsSidechain  bool   `json:"isSidechain"`
	PromptSource string `json:"promptSource"`
	// AiTitle carries the session title on builds that emit a
	// {"type":"ai-title"} entry. Absent on builds that do not; the title scan
	// consults it only after the typed-prompt pass finds nothing.
	AiTitle string `json:"aiTitle"`
	// IsMeta marks text claude injected into the user turn rather than text the
	// user wrote — hook notices, local-command caveats, system reminders.
	IsMeta bool `json:"isMeta"`
	// ToolUseResult is present, at top level, on every entry that is a tool
	// RESULT. It is what separates a tool result from a prompt, because content
	// SHAPE does not — a tool result's content is a bare string far more often
	// than not.
	ToolUseResult jsonPresent `json:"toolUseResult"`
	Timestamp     string      `json:"timestamp"`
	Message       struct {
		// Content is a string for a plain prompt and an array of content
		// blocks when the prompt carries attachments — both shapes occur in
		// the wild, so it is decoded in a second pass.
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// Detail is the on-demand deep read of a single transcript, answering "is this
// the conversation I want to resume?" without opening it. Unlike List — which
// head-reads every transcript in a directory — this reads one file end to end,
// so it is issued per user request, never per listing.
type Detail struct {
	// ID is the session UUID the detail describes.
	ID string
	// FirstPrompt and LastPrompt are the first and last prompts the user
	// typed, sanitized but NOT collapsed to one line: the detail view renders
	// them as paragraphs. Either may be empty.
	FirstPrompt string
	LastPrompt  string
	// UserPrompts counts entries the user typed. Tool results are recorded as
	// type "user" too, so they are excluded by the same promptSource=="typed"
	// test the title scan uses — counting raw user entries would report
	// hundreds for a conversation with a few dozen actual prompts.
	//
	// Deliberately the ONLY count reported. An assistant-entry count was tried
	// and dropped: measured against real transcripts it reads 11638 against 87
	// prompts, because every tool call is its own entry — a number no human
	// reads as "replies". Dropping it also made the scan ~20× faster, since
	// confirming those entries meant unmarshaling the bulk of the file.
	UserPrompts int
	// Started is the timestamp on the transcript's first dated entry; zero when
	// none of the opening entries carried one.
	Started time.Time
	// Modified is the file's mtime — when the session was last active.
	Modified time.Time
	// SizeBytes is the transcript size on disk.
	SizeBytes int64
}

// ReadDetail reads one session's transcript for cwd and summarizes it.
//
// sessionID is a filename component, so it is validated here as well as at the
// IPC boundary: this package is importable on its own, and a caller that passes
// through an unchecked id would otherwise turn "../../.ssh/id_rsa" into a read
// outside the project directory.
// Unlike List, a cancelled ctx here is an ERROR rather than a degraded result.
// ReadDetail answers about one specific highlighted session, and a partial read
// would silently under-report that session's prompt count — a wrong number the
// user has no way to distinguish from a right one. An empty panel is honest;
// a truncated one is not.
func ReadDetail(ctx context.Context, cwd, sessionID string) (Detail, error) {
	return ReadDetailIn(ctx, ConfigDir(), cwd, sessionID)
}

// ReadDetailIn is ReadDetail against an explicit config directory. ReadDetail
// resolves one from the environment and delegates here.
func ReadDetailIn(ctx context.Context, configDir, cwd, sessionID string) (Detail, error) {
	if err := ctx.Err(); err != nil {
		return Detail{}, fmt.Errorf("read session detail: %w", err)
	}
	if sessionID == "" || sessionID != filepath.Base(sessionID) ||
		sessionID == "." || sessionID == ".." || strings.ContainsRune(sessionID, filepath.Separator) {
		return Detail{}, fmt.Errorf("invalid session id")
	}
	path := TranscriptPathIn(configDir, cwd, sessionID)
	if path == "" {
		return Detail{}, fmt.Errorf("no transcript path for this session")
	}
	return readDetail(ctx, path, sessionID)
}

// readDetail is the filesystem half of ReadDetail, split out so tests can point
// it at a t.TempDir() rather than the real home directory.
func readDetail(ctx context.Context, path, id string) (Detail, error) {
	// Lstat, not Stat: a symlinked transcript is rejected rather than followed,
	// matching listDir's IsRegular filter.
	info, err := os.Lstat(path)
	if err != nil {
		return Detail{}, err
	}
	if !info.Mode().IsRegular() {
		return Detail{}, fmt.Errorf("transcript is not a regular file")
	}

	f, err := os.Open(path)
	if err != nil {
		return Detail{}, err
	}
	defer f.Close()

	d := Detail{ID: id, Modified: info.ModTime(), SizeBytes: info.Size()}

	// The whole file is streamed — the last prompt is only knowable at the end —
	// but a line is rejected by a byte-compare before any JSON parsing, the same
	// pre-filter readTitle uses. Typed prompts are a tiny minority of entries,
	// so a multi-MB transcript costs a memchr pass and a few dozen unmarshals
	// rather than one per entry. That is the difference between this being
	// affordable on a keypress and not: the largest transcript on hand is 88 MB.
	r := bufio.NewReaderSize(f, 64<<10)
	typedMark := []byte(`"promptSource":"typed"`)
	for lineNo := 0; ; lineNo++ {
		// Checked in blocks rather than per line: the whole point of this loop
		// is that it byte-compares millions of lines cheaply, and a ctx.Err()
		// call on each one would be a measurable share of that budget.
		if lineNo%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return Detail{}, fmt.Errorf("scan transcript %s at line %d: %w", id, lineNo, err)
			}
		}
		// A non-nil error still yields the trailing bytes: a transcript a live
		// claude process is appending to routinely ends mid-line, and that
		// fragment simply fails to parse and is skipped.
		line, readErr := r.ReadBytes('\n')
		if len(line) > 0 {
			d.consume(line, lineNo, typedMark)
		}
		if readErr != nil {
			break
		}
	}

	// Fallback: a transcript from a build that does not record promptSource
	// yields zero prompts above. Re-scan classifying by what each entry IS.
	//
	// This comment used to say the promptSource=="" test meant "a current-schema
	// transcript can never be reclassified here". It does not — that is a
	// per-entry test, and plenty of entries on a current-schema transcript carry
	// no promptSource. Array content is rejected because a tool result whose
	// content is a block list is not a prompt, but that check is not enough
	// either: a tool result's content is a bare STRING more often than not.
	// isMachinery is what carries the classification (see readTitle's
	// fallback 2), and it must run BEFORE the count below — the increment and
	// the text are set together, so one rejection point covers both.
	//
	// A SECOND pass rather than one combined pass, deliberately: the loop above
	// owes its speed to rejecting non-typed lines with a byte compare before
	// any JSON parse, and parsing every "type":"user" line unconditionally
	// would pay a full unmarshal per tool result on an 88 MB transcript for
	// every existing user. This runs only when the fast path found nothing.
	// Operand order is load-bearing. && short-circuits, so recordsPromptSource
	// runs only on transcripts that already found no typed prompt — the rare
	// path. Reversing it puts a seek and a 64 KiB read on EVERY detail read.
	if d.UserPrompts == 0 && !recordsPromptSource(f) {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return d, nil // keep the timestamps the first pass gathered
		}
		r = bufio.NewReaderSize(f, 64<<10)
		userMark := []byte(`"type":"user"`)
		for lineNo := 0; ; lineNo++ {
			if lineNo%4096 == 0 {
				if err := ctx.Err(); err != nil {
					return Detail{}, fmt.Errorf("rescan transcript %s at line %d: %w", id, lineNo, err)
				}
			}
			line, readErr := r.ReadBytes('\n')
			if len(line) > 0 && bytes.Contains(line, userMark) {
				var tl transcriptLine
				if json.Unmarshal(bytes.TrimSpace(line), &tl) == nil &&
					tl.Type == "user" && !tl.IsSidechain && tl.PromptSource == "" &&
					contentIsString(tl.Message.Content) {
					// NOT `continue`: this sits inside the read loop, above the
					// readErr check that ends it, so skipping the rest of the
					// iteration would skip the break too.
					if raw := contentText(tl.Message.Content, MaxPromptRunes); !isMachinery(tl, raw) {
						d.UserPrompts++
						if text := sanitizePrompt(raw); text != "" {
							if d.FirstPrompt == "" {
								d.FirstPrompt = text
							}
							d.LastPrompt = text
						}
					}
				}
			}
			if readErr != nil {
				break
			}
		}
	}
	return d, nil
}

// consume folds one transcript line into the accumulating detail.
func (d *Detail) consume(line []byte, lineNo int, typedMark []byte) {
	// The start timestamp comes from the first dated entry of any type, so it
	// is the only reason to parse an opening line that is not a typed prompt.
	needStart := d.Started.IsZero() && lineNo < startScanLines

	if !needStart && !bytes.Contains(line, typedMark) {
		return
	}

	var tl transcriptLine
	if err := json.Unmarshal(bytes.TrimSpace(line), &tl); err != nil {
		return
	}
	if needStart && tl.Timestamp != "" {
		if ts, err := time.Parse(time.RFC3339, tl.Timestamp); err == nil {
			d.Started = ts
		}
	}
	// Sidechain entries belong to subagents, not the user's conversation.
	if tl.IsSidechain || tl.Type != "user" || tl.PromptSource != "typed" {
		return
	}
	d.UserPrompts++
	if text := sanitizePrompt(contentText(tl.Message.Content, MaxPromptRunes)); text != "" {
		if d.FirstPrompt == "" {
			d.FirstPrompt = text
		}
		d.LastPrompt = text
	}
}

// readTitle returns the first typed human prompt in the head of a transcript,
// sanitized to a single line. Any failure yields "" — a session with no title
// still lists, it just displays by ID.
func readTitle(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	buf, err := io.ReadAll(io.LimitReader(f, titleScanBytes))
	if err != nil || len(buf) == 0 {
		return ""
	}
	lines := bytes.Split(buf, []byte{'\n'})
	// A filled window almost certainly ends mid-line; that trailing fragment
	// would fail to parse anyway, but dropping it explicitly keeps the intent
	// clear. Only when the window ends ON a newline is the last element a
	// complete line (in fact the empty string after it) — a file exactly
	// titleScanBytes long with no trailing newline would otherwise lose a
	// perfectly good final line.
	if len(buf) == titleScanBytes && buf[len(buf)-1] != '\n' && len(lines) > 0 {
		lines = lines[:len(lines)-1]
	}

	for _, line := range lines {
		// Cheap pre-filter: typed prompts are a small minority of lines and a
		// full Unmarshal per line is the expensive part of this scan.
		if !bytes.Contains(line, []byte(`"promptSource":"typed"`)) {
			continue
		}
		var tl transcriptLine
		if err := json.Unmarshal(bytes.TrimSpace(line), &tl); err != nil {
			continue
		}
		// Sidechain entries belong to subagents, not the user's conversation.
		if tl.Type != "user" || tl.IsSidechain || tl.PromptSource != "typed" {
			continue
		}
		if text := sanitizeTitle(contentText(tl.Message.Content, MaxTitleRunes)); text != "" {
			return text
		}
	}

	// Fallback 1 — a {"type":"ai-title"} entry. Reached only when the typed
	// pass above found nothing.
	//
	// Deliberately NOT gated on the transcript's schema. Measured 2026-08-20:
	// current Claude builds emit ai-title entries AND promptSource, so gating
	// this on the field's absence would make the pass dead code. It fires for
	// any transcript with no typed prompt, whatever its schema.
	//
	// A guard would buy nothing anyway, because of what this pass reads.
	// aiTitle is a dedicated title field: the only values it can yield are a
	// title or nothing. Measured: one distinct value per transcript, however
	// many times the entry repeats. Contrast the passes that promote message
	// CONTENT, which must first establish that the content is a prompt at all.
	for _, line := range lines {
		if !bytes.Contains(line, []byte(`"type":"ai-title"`)) {
			continue
		}
		var tl transcriptLine
		if err := json.Unmarshal(bytes.TrimSpace(line), &tl); err != nil {
			continue
		}
		// The pre-filter is a SUBSTRING match, so a line merely containing that
		// text — an entry with a nested content block, say — also lands here.
		// Confirm the entry really is an ai-title, and exclude sidechains for
		// the same reason every other pass does: they belong to subagents, not
		// to the user's conversation, so a subagent's title is not this
		// session's title.
		if tl.Type != "ai-title" || tl.IsSidechain {
			continue
		}
		if text := sanitizeTitle(tl.AiTitle); text != "" {
			return text
		}
	}

	// Fallback 2 — the first user entry whose content is a bare STRING and is
	// not claude's own text.
	//
	// PromptSource=="" is necessary but nowhere near sufficient, and this
	// comment used to claim it "confines this to schemas that never record"
	// the field. It cannot: it is a PER-ENTRY test and says nothing about the
	// transcript. A current-schema file is full of user entries carrying no
	// promptSource — tool results, slash-command expansions, hook notices —
	// and every one of them passed. Sessions were titled "a", "main-agent" and
	// a directory path because of it.
	//
	// isMachinery is what establishes the content is a prompt. The
	// promptSource test STAYS: it is what excludes the non-typed SOURCES
	// (sdk, queued, compact) — real input, but not typed input, and widening
	// that set is a separate decision.
	for _, line := range lines {
		if !bytes.Contains(line, []byte(`"type":"user"`)) {
			continue
		}
		var tl transcriptLine
		if err := json.Unmarshal(bytes.TrimSpace(line), &tl); err != nil {
			continue
		}
		if tl.Type != "user" || tl.IsSidechain || tl.PromptSource != "" ||
			!contentIsString(tl.Message.Content) {
			continue
		}
		text := contentText(tl.Message.Content, MaxTitleRunes)
		if isMachinery(tl, text) {
			continue
		}
		if title := sanitizeTitle(text); title != "" {
			return title
		}
	}
	return ""
}

// recordsPromptSource reports whether the transcript's head mentions
// promptSource, i.e. whether the build that wrote it records the field.
//
// This is a CORRECTNESS gate that also saves work, not a pure optimisation. On
// a transcript that records the field, an entry LACKING it is not a typed
// prompt — whatever else it may be — so skipping the shape-detecting pass
// cannot lose a prompt. It can only suppress entries that pass would
// misclassify.
//
// The two error directions are NOT symmetric, which is what makes a head sample
// acceptable for a whole-file question. A false positive is near impossible:
// the head is the OLDEST part of the file, and a build that records the field
// at session start does not stop mid-session. A false negative — the field
// appearing only past the window, which happens on transcripts that outlived a
// claude upgrade — costs one re-read that happens today anyway.
//
// The needle includes the opening quote of the VALUE, and that is what bounds
// the false-positive case. Message content cannot fake it at all — a mention of
// promptSource inside a JSON string is stored escaped, as \"promptSource\". A
// nested KEY is not escaped, so a tool result carrying its own promptSource
// field could still match; requiring `:"` narrows that to a nested key whose
// value is also a string. The residual cost is one blank detail panel on an
// old-schema transcript that happens to contain such a key, which is why this
// is a substring test and not a parse.
//
// That `:"` also makes the test sensitive to JSON spacing — a
// `"promptSource": "typed"` with a space would miss it. That cannot cost a
// prompt, and the reason is structural rather than a fact about Claude's
// serializer: this needle is a strict prefix of typedMark, the mark the
// whole-file pass above searches for. Any formatting that hides the field from
// this gate hides it from that pass first, so the pass finds no typed prompt,
// the gate answers false, and the rescan runs — which is the behaviour without
// a gate at all. The gate can never be stricter than the path it guards.
func recordsPromptSource(f *os.File) bool {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return false
	}
	head, err := io.ReadAll(io.LimitReader(f, titleScanBytes))
	if err != nil {
		return false
	}
	found := bytes.Contains(head, []byte(`"promptSource":"`))
	// Restore the offset for the caller. Deliberately NOT checked: the answer is
	// already computed, and letting a failed restore turn a true into a false
	// would send the caller into the very pass this gate exists to skip. The
	// pass re-seeks and handles its own failure anyway.
	_, _ = f.Seek(0, io.SeekStart)
	return found
}

// machineryTags are the wrappers claude puts around its own text in the user
// turn. Derived from the transcripts on one developer's machine, counting only
// entries whose message.content is a STRING — the only ones the shape-detecting
// passes can reach.
//
// Six, not more: local-command-caveat and system-reminder are covered by isMeta
// on every observed entry, and listing them here as well would hide which
// mechanism is load-bearing. tool_use_error, persisted-output and
// retrieval_status occur only inside ARRAY content, which contentIsString
// rejects before this runs.
var machineryTags = []string{
	"task-notification",
	"command-name",
	"command-message",
	"local-command-stdout",
	"bash-stdout",
	"bash-input",
}

// isMachinery reports whether a user entry is claude's own text rather than the
// user's, and so must not become a title or count as a prompt.
//
// Three tests, in cost order. The first two are field reads on the already
// decoded entry; only the third touches the content, which the caller passes in
// because it already decoded it — that is the whole reason for the parameter,
// not any difference between the two call sites' rune limits. All three run
// solely on the shape-detecting fallback paths: an entry reaches them only
// after failing the promptSource=="typed" test, so the fast path pays nothing.
//
// Each is independently load-bearing: a tool result carries no tag and no
// isMeta; a hook notice carries isMeta and no tag; command markup carries a tag
// and neither field.
func isMachinery(tl transcriptLine, content string) bool {
	if bool(tl.ToolUseResult) {
		return true
	}
	if tl.IsMeta {
		return true
	}
	// Prefix, never substring: a prompt that MENTIONS <command-name> is prose.
	// Both delimiters, because <command-name> closes immediately while
	// <task-notification …> carries attributes. No TrimSpace — every observed
	// machinery entry opens its tag at byte 0, and a whitespace-prefixed tag is
	// therefore a miss, which yields the behaviour we have today.
	for _, tag := range machineryTags {
		if strings.HasPrefix(content, "<"+tag+">") || strings.HasPrefix(content, "<"+tag+" ") {
			return true
		}
	}
	return false
}

// contentIsString reports whether a message content field decoded as a plain
// JSON string. It tells a prompt from a tool result whose content is an ARRAY
// of blocks — but NOT from one whose content is a bare string, which is the
// common case; isMachinery's toolUseResult test is what covers those.
// Consulted only on transcripts that do not record promptSource at all.
// Tests the raw token rather than round-tripping through json.Unmarshal, which
// accepts JSON null into a string and reports success — so `"content": null`
// counted as a prompt and inflated UserPrompts by one, with empty text.
func contentIsString(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) > 0 && raw[0] == '"'
}

// contentText flattens a message content field into plain text, accepting both
// the bare-string shape and the content-block array shape. limit caps how much
// text is assembled from blocks — the caller truncates for display, this only
// stops a multi-block prompt being concatenated in full just to be thrown away.
func contentText(raw json.RawMessage, limit int) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}
	var b strings.Builder
	for _, blk := range blocks {
		if blk.Type != "text" || blk.Text == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(blk.Text)
		if b.Len() >= limit {
			break
		}
	}
	return b.String()
}

// sanitizeTitle collapses a prompt to a single clean line, then truncates it
// on a rune boundary.
//
// Whitespace controls (newline, tab, CR) become spaces so the words either side
// stay separated; every other control character — notably ESC, the lead byte of
// an ANSI sequence — is dropped outright, so a prompt containing a pasted
// terminal capture cannot inject control codes into the TUI. Dropping rather
// than space-substituting matters for readability: substituting would split
// "\x1b[31mtext" into two visible fragments.
//
// Unicode format characters (category Cf) are dropped too, which IsControl does
// NOT cover: a bidi override or isolate (U+202A–U+202E, U+2066–U+2069) survives
// both IsControl and strings.Fields, and would render the row reversed while
// still measuring its pre-override width — Trojan-source-style display spoofing
// that could have you resume a different session than the one you read. A user
// only has to paste web or repo content into their first prompt to trigger it.
// ZWSP goes for the same reason: invisible, but it defeats whitespace collapse.
// Exported so the TUI can re-apply it to titles arriving over IPC — a title is
// user-authored text that reaches the screen without passing through the VT
// emulator, so it is worth sanitizing on both sides of the socket.
func SanitizeTitle(s string) string { return sanitizeTitle(s) }

// SanitizePrompt is sanitizeTitle's multi-line counterpart, for text rendered
// as a paragraph rather than a row: line breaks are PRESERVED (the shape of a
// prompt is most of its readability) while every other control character and
// Unicode format character is dropped, for the same display-spoofing reasons
// documented on SanitizeTitle. Runs of blank lines collapse to one so a prompt
// padded with newlines cannot push the rest of the panel off-screen, and
// trailing whitespace goes because it only ever renders as ragged rows.
//
// Exported for the same reason SanitizeTitle is: the TUI re-applies it to text
// arriving over IPC, which reaches the screen without passing through the VT
// emulator.
func SanitizePrompt(s string) string { return sanitizePrompt(s) }

func sanitizePrompt(s string) string {
	mapped := strings.Map(func(r rune) rune {
		switch {
		case r == '\n':
			return r
		case r == '\r':
			return -1 // CRLF collapses to LF rather than doubling the break
		case r == '\t':
			return ' '
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r):
			return -1
		default:
			return r
		}
	}, s)

	var out []string
	blank := 0
	for _, line := range strings.Split(mapped, "\n") {
		line = strings.TrimRight(line, " \t")
		if line == "" {
			blank++
			// Drop leading blanks entirely; collapse interior runs to one.
			if blank > 1 || len(out) == 0 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, line)
	}
	text := strings.TrimRight(strings.Join(out, "\n"), "\n")

	runes := []rune(text)
	if len(runes) > MaxPromptRunes {
		text = strings.TrimRight(string(runes[:MaxPromptRunes]), " \n") + "…"
	}
	return text
}

func sanitizeTitle(s string) string {
	mapped := strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			return ' '
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r):
			return -1
		default:
			return r
		}
	}, s)
	title := strings.Join(strings.Fields(mapped), " ")

	runes := []rune(title)
	if len(runes) > MaxTitleRunes {
		title = strings.TrimSpace(string(runes[:MaxTitleRunes])) + "…"
	}
	return title
}
