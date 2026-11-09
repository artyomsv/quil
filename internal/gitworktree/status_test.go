package gitworktree

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// -z is what makes a file name with a space, a quote or a newline one entry:
// without it git quotes such names and a line split cuts them apart. A rename
// carries a SECOND path (the source) that is not an entry of its own — listing
// it would show the user a file that no longer exists in the checkout.
//
// Fails if ParseStatusZ splits on "\n", keeps the rename's source as an entry,
// or trims the leading space of a " M" code.
func TestParseStatusZ_SpacesRenamesIgnored(t *testing.T) {
	out := " M a b.txt\x00?? new dir/\x00R  new.go\x00old.go\x00!! .env\x00"

	files, total := ParseStatusZ(out)

	want := []FileChange{
		{Code: " M", Path: "a b.txt"},
		{Code: "??", Path: "new dir/"},
		{Code: "R ", Path: "new.go"},
		{Code: "!!", Path: ".env"},
	}
	if total != 4 {
		t.Errorf("total = %d, want 4", total)
	}
	if len(files) != len(want) {
		t.Fatalf("files = %#v, want %#v", files, want)
	}
	for i := range want {
		if files[i] != want[i] {
			t.Errorf("files[%d] = %#v, want %#v", i, files[i], want[i])
		}
	}
}

// The list is a DISPLAY bound, the count is not: a worktree with thousands of
// untracked files must still report how many there are, so the dialog can say
// "only the first 200 are listed" rather than imply 200 is everything.
//
// Fails if the cap is applied to total as well as to files.
func TestParseStatusZ_CapsAtMaxStatusFiles(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 250; i++ {
		fmt.Fprintf(&b, "?? f%d\x00", i)
	}

	files, total := ParseStatusZ(b.String())

	if len(files) != MaxStatusFiles {
		t.Errorf("len(files) = %d, want %d", len(files), MaxStatusFiles)
	}
	if total != 250 {
		t.Errorf("total = %d, want 250", total)
	}
}

// Status must ask for the -z form, or ParseStatusZ is handed newline-separated
// quoted names and reads the whole listing as one entry.
//
// Fails if "-z" is dropped from the argv.
func TestStatus_UsesNULSeparatedPorcelain(t *testing.T) {
	skipPreflight(t)
	calls := stubGit(t, "?? a\x00", nil)

	rep, err := Status(context.Background(), "/w/feat-a")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	var sawZ bool
	for _, a := range (*calls)[0] {
		if a == "-z" {
			sawZ = true
		}
	}
	if !sawZ {
		t.Errorf("argv = %v, want -z", (*calls)[0])
	}
	if rep.Changes != 1 || len(rep.Files) != 1 || rep.Files[0].Path != "a" {
		t.Errorf("report = %#v, want one entry named a", rep)
	}
}

// Truncated says the list is short, never the count: the dialog prints the
// count and a "first 200" note from these two fields.
func TestStatus_ReportsTruncatedList(t *testing.T) {
	var b strings.Builder
	for i := 0; i < MaxStatusFiles+1; i++ {
		fmt.Fprintf(&b, "?? f%d\x00", i)
	}
	skipPreflight(t)
	stubGit(t, b.String(), nil)

	rep, err := Status(context.Background(), "/w/feat-a")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !rep.Truncated || rep.Changes != MaxStatusFiles+1 {
		t.Errorf("report Truncated=%v Changes=%d, want true and %d", rep.Truncated, rep.Changes, MaxStatusFiles+1)
	}
}
