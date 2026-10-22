package sbx

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

const testSessionID = "2db05609-f1d5-4576-b5b2-ff114519726b"

// The probe runs one `sbx exec` with the id as an argument, and the marker
// line in the output is the only yes.
func TestHasTranscript_AsksTheSandboxWithTheIDAsAnArgument(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  string
		want bool
	}{
		{"marker", "QUIL_SBX_TRANSCRIPT\n", true},
		{"marker after a start notice", "Starting sandbox cf-x-1...\nQUIL_SBX_TRANSCRIPT\n", true},
		{"nothing", "", false},
		{"noise only", "Starting sandbox cf-x-1...\n", false},
		{"marker inside other text", "xQUIL_SBX_TRANSCRIPTx\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeRunner{answers: func([]string) (string, error) { return tc.out, nil }}
			got, err := (Client{Bin: "sbx", Runner: f}).HasTranscript(context.Background(), "cf-x-1", "", nil, testSessionID)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("HasTranscript = %v, want %v", got, tc.want)
			}
			want := [][]string{{"sbx", "exec", "cf-x-1", "sh", "-c", transcriptScript, "sh", testSessionID}}
			if !reflect.DeepEqual(f.calls, want) {
				t.Errorf("calls =\n%v\nwant\n%v", f.calls, want)
			}
		})
	}
}

// The probe runs where and how the agent will: the pane's folder and the
// plugin's environment ride along, so a CLAUDE_CONFIG_DIR the plugin sets
// (even a relative one) is the folder searched.
func TestHasTranscript_RunsWithTheAgentsFolderAndEnvironment(t *testing.T) {
	f := &fakeRunner{answers: func([]string) (string, error) { return "QUIL_SBX_TRANSCRIPT\n", nil }}
	env := []string{"CLAUDE_CONFIG_DIR=.claude-cfg", "FOO=bar"}
	got, err := (Client{Bin: "sbx", Runner: f}).HasTranscript(context.Background(), "cf-x-1", "/w/ticket", env, testSessionID)
	if err != nil || !got {
		t.Fatalf("HasTranscript = %v, %v", got, err)
	}
	want := [][]string{{"sbx", "exec", "-w", "/w/ticket", "-e", "CLAUDE_CONFIG_DIR=.claude-cfg", "-e", "FOO=bar",
		"cf-x-1", "sh", "-c", transcriptScript, "sh", testSessionID}}
	if !reflect.DeepEqual(f.calls, want) {
		t.Errorf("calls =\n%v\nwant\n%v", f.calls, want)
	}
	// And it is the same prefix the agent's own exec gets.
	exec := ExecArgs("cf-x-1", "/w/ticket", env, "claude", nil)
	if !reflect.DeepEqual(exec[2:len(exec)-2], f.calls[0][2:len(f.calls[0])-6]) {
		t.Errorf("probe %v and exec %v disagree on -w/-e", f.calls[0], exec)
	}
}

// A failed exec is an error, never "no transcript": the caller would start a
// fresh session under an id that may already hold one.
func TestHasTranscript_FailedExecIsAnError(t *testing.T) {
	f := &fakeRunner{answers: func([]string) (string, error) {
		return "ERROR: sandbox not found", errors.New("exit status 1")
	}}
	_, err := (Client{Bin: "sbx", Runner: f}).HasTranscript(context.Background(), "cf-x-1", "", nil, testSessionID)
	if err == nil || !strings.Contains(err.Error(), "sandbox not found") {
		t.Errorf("err = %v", err)
	}
}

// Nothing runs for a name or an id sbx or a shell could misread.
func TestHasTranscript_RefusesUnsafeNamesAndIDs(t *testing.T) {
	f := &fakeRunner{answers: func([]string) (string, error) { return "", nil }}
	c := Client{Bin: "sbx", Runner: f}
	for _, tc := range []struct{ name, id string }{
		{"-evil", testSessionID},
		{"ok", ""},
		{"ok", "not-a-uuid"},
		{"ok", testSessionID + "/x"},
		{"ok", "*"},
		{"ok", testSessionID + "\n"},
	} {
		if _, err := c.HasTranscript(context.Background(), tc.name, "", nil, tc.id); err == nil {
			t.Errorf("HasTranscript(%q, %q) was accepted", tc.name, tc.id)
		}
	}
	if len(f.calls) != 0 {
		t.Errorf("sbx was called %d times for refused inputs", len(f.calls))
	}
}

func TestValidSessionID(t *testing.T) {
	for id, want := range map[string]bool{
		testSessionID:                          true,
		strings.ToUpper(testSessionID):         true,
		"":                                     false,
		"2db05609":                             false,
		testSessionID + "x":                    false,
		"2db05609-f1d5-4576-b5b2-ff114519726g": false,
	} {
		if got := ValidSessionID(id); got != want {
			t.Errorf("ValidSessionID(%q) = %v, want %v", id, got, want)
		}
	}
}
