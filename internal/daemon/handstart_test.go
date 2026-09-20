package daemon

import (
	"strings"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/plugin"
)

func marker(payload string) string { return handStartOSC + payload + "\x1b\\" }

func noEnv(string) (string, bool) { return "", false }

func hasEnv(names ...string) func(string) (string, bool) {
	set := map[string]bool{}
	for _, n := range names {
		set[n] = true
	}
	return func(n string) (string, bool) {
		if set[n] {
			return "x", true
		}
		return "", false
	}
}

func TestParseHandStart(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		ok      bool
		check   func(*testing.T, handStartMarker)
	}{
		{
			name:    "the #221 command line",
			payload: "cmd;TOK;claude;/Users/mike/code;1758240000.5;;--resume,5799ac23-d81a-462f-b176-515f9be6d07c,--enable-auto-mode",
			ok:      true,
			check: func(t *testing.T, m handStartMarker) {
				if m.Name != "claude" || m.CWD != "/Users/mike/code" {
					t.Fatalf("name/cwd = %q/%q", m.Name, m.CWD)
				}
				want := []string{"--resume", "5799ac23-d81a-462f-b176-515f9be6d07c", "--enable-auto-mode"}
				if len(m.Args) != 3 {
					t.Fatalf("args = %v", m.Args)
				}
				for i := range want {
					if m.Args[i] != want[i] {
						t.Fatalf("args = %v, want %v", m.Args, want)
					}
				}
			},
		},
		{
			// The reason the fields are encoded at all. A directory holding a
			// semicolon would otherwise shift every field after it and the
			// daemon would spawn somewhere else entirely.
			name:    "a semicolon in the working directory",
			payload: "cmd;TOK;claude;/tmp/we%3Bird%2Cdir%25;;;",
			ok:      true,
			check: func(t *testing.T, m handStartMarker) {
				if m.CWD != "/tmp/we;ird,dir%" {
					t.Fatalf("cwd = %q", m.CWD)
				}
			},
		},
		{
			name:    "a comma inside a single argument",
			payload: "cmd;TOK;claude;/tmp;;;--foo,a%2Cb",
			ok:      true,
			check: func(t *testing.T, m handStartMarker) {
				if len(m.Args) != 2 || m.Args[1] != "a,b" {
					t.Fatalf("args = %v, want [--foo a,b]", m.Args)
				}
			},
		},
		{
			name:    "no arguments at all",
			payload: "cmd;TOK;claude;/tmp;;;",
			ok:      true,
			check: func(t *testing.T, m handStartMarker) {
				if len(m.Args) != 0 {
					t.Fatalf("args = %v, want none", m.Args)
				}
			},
		},
		{
			name:    "environment names travel, values never do",
			payload: "cmd;TOK;claude;/tmp;;CLAUDE_CONFIG_DIR,ANTHROPIC_API_KEY;",
			ok:      true,
			check: func(t *testing.T, m handStartMarker) {
				if len(m.EnvNames) != 2 || m.EnvNames[0] != "CLAUDE_CONFIG_DIR" {
					t.Fatalf("env = %v", m.EnvNames)
				}
			},
		},
		{
			name:    "the shell reported it could not fit the payload",
			payload: "cmd;TOK;claude;;;;!",
			ok:      true,
			check: func(t *testing.T, m handStartMarker) {
				if !m.Oversize {
					t.Fatal("oversize marker not flagged")
				}
			},
		},
		{name: "wrong field count", payload: "cmd;TOK;claude;/tmp", ok: false},
		{name: "not a cmd marker", payload: "hello;TOK;claude;/tmp;;;", ok: false},
		{name: "no token", payload: "cmd;;claude;/tmp;;;", ok: false},
		{name: "no name", payload: "cmd;TOK;;/tmp;;;", ok: false},
		{name: "empty", payload: "", ok: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, ok := parseHandStart(tc.payload)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if ok && tc.check != nil {
				tc.check(t, m)
			}
		})
	}
}

// A clipped argv is a different command, so an over-long one is refused rather
// than truncated.
func TestParseHandStart_RefusesTooManyArgs(t *testing.T) {
	args := make([]string, handStartMaxArgs+1)
	for i := range args {
		args[i] = "x"
	}
	payload := "cmd;TOK;claude;/tmp;;;" + strings.Join(args, ",")
	if _, ok := parseHandStart(payload); ok {
		t.Fatal("accepted an argv longer than any interactive launch carries")
	}
}

// The marker is answered, so missing one costs the user a full second of dead
// air. Every split point must still yield it.
func TestScanHandStart_EveryChunkBoundary(t *testing.T) {
	full := []byte("noise before " + marker("cmd;TOK;claude;/tmp;;;--continue") + " noise after")
	for cut := 0; cut <= len(full); cut++ {
		first, tail := scanHandStart(nil, full[:cut])
		second, _ := scanHandStart(tail, full[cut:])
		got := append(append([]string(nil), first...), second...)
		if len(got) != 1 {
			t.Fatalf("split at %d yielded %d markers, want 1", cut, len(got))
		}
		if got[0] != "cmd;TOK;claude;/tmp;;;--continue" {
			t.Fatalf("split at %d gave payload %q", cut, got[0])
		}
	}
}

func TestScanHandStart_BelTerminatorAndMultiple(t *testing.T) {
	data := []byte(handStartOSC + "cmd;TOK;claude;/a;;;\x07" + handStartOSC + "cmd;TOK;codex;/b;;;\x1b\\")
	got, _ := scanHandStart(nil, data)
	if len(got) != 2 {
		t.Fatalf("got %d markers, want 2: %v", len(got), got)
	}
}

// A pane emitting a lone introducer then megabytes of output must not grow the
// retained tail without bound.
func TestScanHandStart_TailIsBounded(t *testing.T) {
	_, tail := scanHandStart(nil, []byte(handStartOSC+strings.Repeat("x", handStartMaxMarker+10)))
	if len(tail) != 0 {
		t.Fatalf("retained %d bytes, want the tail dropped", len(tail))
	}
	_, tail = scanHandStart(nil, []byte("plain output with no marker at all"))
	if len(tail) >= len(handStartOSC) {
		t.Fatalf("retained %d bytes of ordinary output", len(tail))
	}
}

func TestHandStartMarker_TokenMatches(t *testing.T) {
	m := handStartMarker{Token: "abc"}
	if !m.tokenMatches("abc") {
		t.Error("a matching token did not match")
	}
	for _, bound := range []string{"", "abd", "ab", "abcd"} {
		if m.tokenMatches(bound) {
			t.Errorf("token matched %q", bound)
		}
	}
	if (handStartMarker{}).tokenMatches("abc") {
		t.Error("an empty marker token matched")
	}
}

// Past the cutoff the shell has already run the binary, and eight bytes typed
// into a live agent's composer is the failure being avoided.
func TestHandStartMarker_Freshness(t *testing.T) {
	now := time.Now()
	fresh := handStartMarker{At: now.Add(-100 * time.Millisecond)}
	if !fresh.fresh(now, now) {
		t.Error("a 100ms-old marker was treated as stale")
	}
	stale := handStartMarker{At: now.Add(-2 * time.Second)}
	if stale.fresh(now, now) {
		t.Error("a 2s-old marker was answered")
	}
	// A shell clock ahead of the daemon's is not evidence of staleness.
	ahead := handStartMarker{At: now.Add(5 * time.Second)}
	if !ahead.fresh(now, now) {
		t.Error("a marker from a fast clock was treated as stale")
	}
	// No timestamp: measured from arrival, with a tighter bound.
	none := handStartMarker{}
	if !none.fresh(now, now.Add(-100*time.Millisecond)) {
		t.Error("a timestamp-less marker was rejected inside the arrival bound")
	}
	if none.fresh(now, now.Add(-2*time.Second)) {
		t.Error("a timestamp-less marker was answered past the arrival bound")
	}
}

// EPOCHREALTIME uses the locale's radix character, which is a comma across much
// of Europe. Reading that as an integer would put the marker in 1970 and every
// launch would silently fall out of the freshness window.
func TestParseHandStartTime(t *testing.T) {
	want := time.UnixMilli(1758240000500)
	for _, in := range []string{"1758240000.5", "1758240000,5"} {
		if got := parseHandStartTime(in); !got.Equal(want) {
			t.Errorf("parse(%q) = %v, want %v", in, got, want)
		}
	}
	if got := parseHandStartTime("1758240000500"); !got.Equal(want) {
		t.Errorf("millisecond form = %v, want %v", got, want)
	}
	for _, bad := range []string{"", "abc", "-1", "0"} {
		if got := parseHandStartTime(bad); !got.IsZero() {
			t.Errorf("parse(%q) = %v, want the zero time", bad, got)
		}
	}
}

func TestClassifyHandStart(t *testing.T) {
	p := &plugin.PanePlugin{Name: "claude-code"}
	cases := []struct {
		name string
		m    handStartMarker
		env  func(string) (string, bool)
		want handStartClass
	}{
		{"bare launch", handStartMarker{Name: "claude"}, noEnv, handStartConvert},
		{"resume with an id", handStartMarker{Name: "claude", Args: []string{"--resume", "5799ac23-d81a-462f-b176-515f9be6d07c"}}, noEnv, handStartConvert},
		{"the #221 line", handStartMarker{Name: "claude", Args: []string{"--resume", "abc", "--enable-auto-mode"}}, noEnv, handStartConvert},
		{"continue", handStartMarker{Name: "claude", Args: []string{"--continue"}}, noEnv, handStartConvert},
		{"a positional prompt", handStartMarker{Name: "claude", Args: []string{"fix the build"}}, noEnv, handStartConvert},
		{"an unknown subcommand is still a session", handStartMarker{Name: "claude", Args: []string{"brand-new-verb"}}, noEnv, handStartConvert},

		{"mcp is not a session", handStartMarker{Name: "claude", Args: []string{"mcp", "list"}}, noEnv, handStartRun},
		{"setup-token is not a session", handStartMarker{Name: "claude", Args: []string{"setup-token"}}, noEnv, handStartRun},
		{"codex exec is not a session", handStartMarker{Name: "codex", Args: []string{"exec", "x"}}, noEnv, handStartRun},
		{"opencode serve is not a session", handStartMarker{Name: "opencode", Args: []string{"serve"}}, noEnv, handStartRun},
		{"a subcommand word later in the line is positional", handStartMarker{Name: "claude", Args: []string{"--continue", "mcp"}}, noEnv, handStartConvert},

		{"a typed --settings contends with Quil's", handStartMarker{Name: "claude", Args: []string{"--settings", "/tmp/s.json"}}, noEnv, handStartRun},
		{"two session flags contradict", handStartMarker{Name: "claude", Args: []string{"--resume", "a", "--continue"}}, noEnv, handStartRun},
		{"the shell could not fit the line", handStartMarker{Name: "claude", Oversize: true}, noEnv, handStartRun},
		{"a control character in an argument", handStartMarker{Name: "claude", Args: []string{"a\x07b"}}, noEnv, handStartRun},

		{"an env name the daemon lacks", handStartMarker{Name: "claude", EnvNames: []string{"CLAUDE_CONFIG_DIR"}}, noEnv, handStartRun},
		{"an env name the daemon also has", handStartMarker{Name: "claude", EnvNames: []string{"CLAUDE_CONFIG_DIR"}}, hasEnv("CLAUDE_CONFIG_DIR"), handStartConvert},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyHandStart(p, tc.m, tc.env)
			if got.Class != tc.want {
				t.Fatalf("class = %v (%q), want %v", got.Class, got.Reason, tc.want)
			}
			if got.Class == handStartRun && got.Reason == "" {
				t.Error("refused without a reason the card could show")
			}
		})
	}
}

// Every refusal must run the command exactly as typed, which is the
// pre-feature behaviour — that is what lets the subcommand list be a denylist.
func TestClassifyHandStart_NilPluginRuns(t *testing.T) {
	got := classifyHandStart(nil, handStartMarker{Name: "claude"}, noEnv)
	if got.Class != handStartRun {
		t.Fatal("a marker with no plugin behind it did not fall through to running as typed")
	}
}

// Both replies are read as a fixed length by the shell. A different length
// would hang the read until its one-second deadline on every launch.
func TestHandStartReplies_AreEightBytes(t *testing.T) {
	for _, r := range []string{handStartReplyRun, handStartReplyConvert} {
		if len(r) != 8 {
			t.Errorf("reply %q is %d bytes, want 8", r, len(r))
		}
	}
}
