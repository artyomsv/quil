package tui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/artyomsv/quil/internal/config"
)

// grpNames lists a groups value's names in display order.
func grpNames(g projectGroups) string {
	names := make([]string, len(g.Groups))
	for i, grp := range g.Groups {
		names[i] = grp.Name
	}
	return strings.Join(names, ",")
}

// grpMembers lists group i's members as dest/id.
func grpMembers(g projectGroups, i int) string {
	out := make([]string, 0, len(g.Groups[i].Members))
	for _, mb := range g.Groups[i].Members {
		out = append(out, mb.Dest+"/"+mb.ID)
	}
	return strings.Join(out, ",")
}

// grpFromNames builds empty, expanded groups with the given names.
func grpFromNames(names ...string) projectGroups {
	var g projectGroups
	for _, n := range names {
		g.Groups = append(g.Groups, projectGroup{Name: n})
	}
	return g
}

func TestProjectGroups_AddTrimsAndRefusesEmptyOrDuplicateNames(t *testing.T) {
	t.Parallel()
	var g projectGroups
	i, err := g.addGroup("  Work  ")
	if err != nil || i != 0 {
		t.Fatalf("addGroup = (%d, %v), want (0, nil)", i, err)
	}
	if g.Groups[0].Name != "Work" {
		t.Fatalf("name = %q, want the trimmed %q", g.Groups[0].Name, "Work")
	}
	for _, tc := range []struct {
		name string
		want error
	}{
		{"", errGroupNameEmpty},
		{"   ", errGroupNameEmpty},
		{"work", errGroupNameTaken},
		{" WORK ", errGroupNameTaken},
	} {
		if _, err := g.addGroup(tc.name); !errors.Is(err, tc.want) {
			t.Errorf("addGroup(%q) error = %v, want %v", tc.name, err, tc.want)
		}
	}
	if len(g.Groups) != 1 {
		t.Fatalf("groups = %s, want only Work — a refused add must add nothing", grpNames(g))
	}
	if g.indexOf(" work ") != 0 || g.indexOf("nope") != -1 {
		t.Errorf("indexOf = %d / %d, want 0 (case-insensitive, trimmed) / -1", g.indexOf(" work "), g.indexOf("nope"))
	}
}

func TestNormalizeGroupName_CapsAt32RunesAndRetrims(t *testing.T) {
	t.Parallel()
	if got := normalizeGroupName(strings.Repeat("é", 40)); utf8.RuneCountInString(got) != maxGroupNameRunes {
		t.Errorf("40 runes normalised to %d, want %d", utf8.RuneCountInString(got), maxGroupNameRunes)
	}
	// The cut lands just after a space; the result is re-trimmed rather than
	// keeping a trailing blank the user cannot see.
	if got, want := normalizeGroupName(strings.Repeat("a", 31)+"  b"), strings.Repeat("a", 31); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestProjectGroups_RenameKeepsNamesUniqueButMayChangeItsOwnCase(t *testing.T) {
	t.Parallel()
	g := grpFromNames("Work", "Home")
	if err := g.renameGroup(0, "home"); !errors.Is(err, errGroupNameTaken) {
		t.Errorf("rename onto another group's name: err = %v, want errGroupNameTaken", err)
	}
	if err := g.renameGroup(0, "WORK"); err != nil {
		t.Errorf("a case change of its own name was refused: %v", err)
	}
	if err := g.renameGroup(1, " "); !errors.Is(err, errGroupNameEmpty) {
		t.Errorf("rename to blank: err = %v, want errGroupNameEmpty", err)
	}
	if err := g.renameGroup(5, "x"); !errors.Is(err, errNoSuchGroup) {
		t.Errorf("rename out of range: err = %v, want errNoSuchGroup", err)
	}
	if got := grpNames(g); got != "WORK,Home" {
		t.Errorf("names = %s, want WORK,Home", got)
	}
}

func TestProjectGroups_AssignMovesAProjectOutOfItsOtherGroup(t *testing.T) {
	t.Parallel()
	g := grpFromNames("A", "B")
	if !g.assign(0, "", "p1") {
		t.Fatal("the first assign reported no change")
	}
	if g.assign(0, "", "p1") {
		t.Error("re-assigning to the same group reported a change")
	}
	if !g.assign(1, "", "p1") {
		t.Fatal("moving p1 to B reported no change")
	}
	if got := grpMembers(g, 0); got != "" {
		t.Errorf("A = %q, want empty — a project is in at most one group", got)
	}
	if got := grpMembers(g, 1); got != "/p1" {
		t.Errorf("B = %q, want /p1", got)
	}
	if !g.unassign("", "p1") || g.groupOf("", "p1") != -1 {
		t.Error("unassign did not remove the member")
	}
	if g.unassign("", "p1") {
		t.Error("unassigning an ungrouped project reported a change")
	}
	if g.assign(7, "", "p1") || g.assign(0, "", "") {
		t.Error("assign accepted an out-of-range group or an empty id")
	}
}

func TestProjectGroups_SameIDOnTwoDaemonsAreTwoMembers(t *testing.T) {
	t.Parallel()
	g := grpFromNames("A", "B")
	g.assign(0, "", "proj-1")
	g.assign(1, "gpu01", "proj-1")
	if a, b := g.groupOf("", "proj-1"), g.groupOf("gpu01", "proj-1"); a != 0 || b != 1 {
		t.Fatalf("groupOf = local %d / gpu01 %d, want 0 / 1 — the key is (dest, id)", a, b)
	}
	idx := g.memberIndex()
	if idx[groupMember{ID: "proj-1"}] != 0 || idx[groupMember{Dest: "gpu01", ID: "proj-1"}] != 1 {
		t.Errorf("memberIndex = %v, want local→0 and gpu01→1", idx)
	}
	g.unassign("gpu01", "proj-1")
	if g.groupOf("", "proj-1") != 0 {
		t.Error("unassigning the remote twin removed the local one")
	}
}

func TestProjectGroups_MoveGroupSlides(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		from, to int
		changed  bool
		want     string
	}{
		{"down one", 0, 1, true, "B,A,C"},
		{"bottom to top", 2, 0, true, "C,A,B"},
		{"top to bottom", 0, 2, true, "B,C,A"},
		{"same slot", 1, 1, false, "A,B,C"},
		{"out of range", 0, 3, false, "A,B,C"},
		{"negative", -1, 0, false, "A,B,C"},
	} {
		g := grpFromNames("A", "B", "C")
		if got := g.moveGroup(tc.from, tc.to); got != tc.changed {
			t.Errorf("%s: moveGroup(%d,%d) = %v, want %v", tc.name, tc.from, tc.to, got, tc.changed)
		}
		if got := grpNames(g); got != tc.want {
			t.Errorf("%s: order = %s, want %s", tc.name, got, tc.want)
		}
	}
}

func TestProjectGroups_DeleteAndCollapseReportWhetherAnythingChanged(t *testing.T) {
	t.Parallel()
	g := grpFromNames("A", "B")
	g.assign(0, "", "p1")
	if !g.setCollapsed(1, true) || g.setCollapsed(1, true) || g.setCollapsed(9, true) {
		t.Error("setCollapsed must report a change once, then none, and none out of range")
	}
	if g.deleteGroup(5) {
		t.Error("deleteGroup accepted an out-of-range index")
	}
	if !g.deleteGroup(0) {
		t.Fatal("deleteGroup(0) reported no change")
	}
	if grpNames(g) != "B" || g.groupOf("", "p1") != -1 || !g.Groups[0].Collapsed {
		t.Errorf("after delete: %s, p1 in %d, B collapsed %v — want B, -1, true", grpNames(g), g.groupOf("", "p1"), g.Groups[0].Collapsed)
	}
}

func TestProjectGroups_PruneOnlyTouchesDestinationsItWasGiven(t *testing.T) {
	t.Parallel()
	g := projectGroups{Groups: []projectGroup{{Name: "A", Members: []groupMember{
		{Dest: "", ID: "keep"}, {Dest: "", ID: "gone"}, {Dest: "gpu01", ID: "offline"},
	}}}}
	if !g.prune(map[string]map[string]bool{"": {"keep": true}}) {
		t.Fatal("prune reported no change")
	}
	if got := grpMembers(g, 0); got != "/keep,gpu01/offline" {
		t.Errorf("members = %s, want /keep,gpu01/offline — a destination absent from the map "+
			"was not heard from and keeps its members", got)
	}
	if g.prune(map[string]map[string]bool{"": {"keep": true}}) {
		t.Error("a second identical prune reported a change")
	}
	g.prune(map[string]map[string]bool{"gpu01": {}})
	if got := grpMembers(g, 0); got != "/keep" {
		t.Errorf("members = %s, want /keep — gpu01 WAS heard from and holds none of them", got)
	}
}

func TestProjectGroups_CloneIsDeep(t *testing.T) {
	g := projectGroups{Groups: []projectGroup{{Name: "A", Members: []groupMember{{ID: "p1"}}}}}
	c := g.clone()
	g.Groups[0].Name = "changed"
	g.Groups[0].Members[0].ID = "changed"
	if c.Groups[0].Name != "A" || c.Groups[0].Members[0].ID != "p1" {
		t.Fatalf("clone = %+v, want it untouched by edits to the original", c)
	}
}

func TestProjectGroups_SaveLoadRoundTrip(t *testing.T) {
	// A QUIL_HOME that does not exist yet: the save must create it.
	t.Setenv("QUIL_HOME", filepath.Join(t.TempDir(), "qh"))
	path := config.ProjectGroupsPath()
	want := projectGroups{Groups: []projectGroup{
		{Name: "Work", Collapsed: true, Members: []groupMember{{Dest: "", ID: "p1"}, {Dest: "user@gpu01", ID: "p2"}}},
		{Name: "构建"},
	}}
	if err := saveProjectGroups(path, want); err != nil {
		t.Fatalf("save: %v", err)
	}
	if left := grpTempFiles(t, path); len(left) != 0 {
		t.Errorf("temp files left behind: %v", left)
	}
	got, err := loadProjectGroups(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}
}

// grpTempFiles lists the save temp files beside path — the fixed ".tmp" name
// included, so a regression to it is seen too.
func grpTempFiles(t *testing.T, path string) []string {
	t.Helper()
	matches, err := filepath.Glob(path + ".tmp*")
	if err != nil {
		t.Fatal(err)
	}
	return matches
}

// Two TUIs on one machine save the same file — and after a broadcast prune,
// at the same moment. groupsWriter orders saves inside ONE process only, so
// the temp file must be unique per save: a shared fixed ".tmp" is truncated
// and rewritten under the other writer, a longer write then a shorter one
// leaves mixed JSON, and the rename installs it for the next start to
// quarantine. Every save must succeed and the file must always parse.
func TestSaveProjectGroups_ConcurrentSavesNeverCorruptTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "project-groups.json")
	long := projectGroups{}
	for i := 0; i < 40; i++ {
		long.Groups = append(long.Groups, projectGroup{Name: fmt.Sprintf("group-%02d-%s", i, strings.Repeat("x", 20))})
	}
	short := grpFromNames("s")
	var wg sync.WaitGroup
	errs := make(chan error, 400)
	for _, snap := range []projectGroups{long, short} {
		wg.Add(1)
		go func(g projectGroups) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				if err := saveProjectGroups(path, g); err != nil {
					errs <- err
					return
				}
			}
		}(snap)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("a concurrent save failed: %v", err)
	}
	got, err := loadProjectGroups(path)
	if err != nil {
		t.Fatalf("the file does not parse after concurrent saves: %v", err)
	}
	if !reflect.DeepEqual(got, long) && !reflect.DeepEqual(got, short) {
		t.Fatalf("the file holds %s, neither snapshot", grpNames(got))
	}
	if left := grpTempFiles(t, path); len(left) != 0 {
		t.Errorf("temp files left behind: %v", left)
	}
}

// A save that fails at the rename leaves no temp file behind: the name is
// unique per save, so nothing would ever reuse or overwrite a leftover.
func TestSaveProjectGroups_FailedRenameRemovesItsTempFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "project-groups.json")
	// A non-empty DIRECTORY where the file should be: the rename fails.
	if err := os.MkdirAll(filepath.Join(path, "occupied"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := saveProjectGroups(path, grpFromNames("a")); err == nil {
		t.Fatal("a save over a directory succeeded")
	}
	if left := grpTempFiles(t, path); len(left) != 0 {
		t.Errorf("a failed save left temp files behind: %v", left)
	}
}

func TestProjectGroups_MissingFileStartsEmptyWithoutAnError(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	got, err := loadProjectGroups(config.ProjectGroupsPath())
	if err != nil || len(got.Groups) != 0 {
		t.Fatalf("load of a missing file = (%+v, %v), want (empty, nil) — the first launch is not an error", got, err)
	}
}

func TestProjectGroups_CorruptFileIsMovedToBakAndStartsEmpty(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	path := config.ProjectGroupsPath()
	if err := os.WriteFile(path, []byte(`{"groups": [`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadProjectGroups(path)
	if err == nil {
		t.Fatal("a corrupt file loaded without an error to log")
	}
	if len(got.Groups) != 0 {
		t.Fatalf("groups = %+v, want none", got.Groups)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the corrupt file is still in place (stat err = %v): the next save would overwrite it", err)
	}
	if data, err := os.ReadFile(path + ".bak"); err != nil || string(data) != `{"groups": [` {
		t.Errorf(".bak = %q (err %v), want the original bytes", data, err)
	}
}

func TestProjectGroups_QuarantineNeverOverwritesAnEarlierBackup(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	path := config.ProjectGroupsPath()
	oldBak := `{"version":1,"groups":[{"name":"OldBackup"}]}`
	if err := os.WriteFile(path+".bak", []byte(oldBak), 0o600); err != nil {
		t.Fatal(err)
	}
	corrupt := `{"groups": [`
	if err := os.WriteFile(path, []byte(corrupt), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadProjectGroups(path); err == nil {
		t.Fatal("a corrupt file loaded without an error to log")
	}
	if data, err := os.ReadFile(path + ".bak"); err != nil || string(data) != oldBak {
		t.Fatalf(".bak = %q (err %v), want the untouched earlier backup %q", data, err, oldBak)
	}
	if data, err := os.ReadFile(path + ".bak.1"); err != nil || string(data) != corrupt {
		t.Fatalf(".bak.1 = %q (err %v), want the corrupt bytes %q — a second quarantine "+
			"must never destroy the first backup", data, err, corrupt)
	}
}

func TestProjectGroups_OversizedFileIsRefusedAndMovedToBak(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	path := config.ProjectGroupsPath()
	head := `{"version":1,"groups":[{"name":"A"}]}`
	// VALID JSON padded past the cap with whitespace, so only the size can refuse it.
	if err := os.WriteFile(path, []byte(head+strings.Repeat(" ", projectGroupsFileCap)), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadProjectGroups(path)
	if err == nil || len(got.Groups) != 0 {
		t.Fatalf("an over-cap file loaded as (%+v, %v), want (empty, error)", got, err)
	}
	if _, err := os.Stat(path + ".bak"); err != nil {
		t.Errorf("the over-cap file was not moved to .bak: %v", err)
	}
	// Control: the same content under the cap loads, or the test above could
	// pass against a loader that refuses everything.
	if err := os.WriteFile(path, []byte(head), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := loadProjectGroups(path); err != nil || grpNames(got) != "A" {
		t.Fatalf("control load = (%s, %v), want (A, nil)", grpNames(got), err)
	}
}

func TestProjectGroups_LoadDropsBlankAndDuplicateNamesAndSecondMemberships(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	path := config.ProjectGroupsPath()
	raw := `{"version":1,"groups":[
		{"name":" Work ","members":[{"dest":"","id":"p1"},{"dest":"","id":""}]},
		{"name":"work","members":[{"dest":"","id":"p2"}]},
		{"name":"  ","members":[]},
		{"name":"Home","members":[{"dest":"","id":"p1"},{"dest":"gpu01","id":"p1"}]}
	]}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadProjectGroups(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if names := grpNames(got); names != "Work,Home" {
		t.Fatalf("names = %s, want Work,Home (trimmed; duplicate and blank dropped)", names)
	}
	if m := grpMembers(got, 0); m != "/p1" {
		t.Errorf("Work = %s, want /p1 (the empty id dropped)", m)
	}
	if m := grpMembers(got, 1); m != "gpu01/p1" {
		t.Errorf("Home = %s, want gpu01/p1 (local p1 already belongs to Work)", m)
	}
}

func TestGroupsWriter_AnOlderSnapshotNeverOverwritesANewerOne(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	path := config.ProjectGroupsPath()
	w := &groupsWriter{}
	if err := w.write(path, 2, grpFromNames("newer")); err != nil {
		t.Fatal(err)
	}
	// Bubble Tea runs Cmds concurrently: seq 1 can land after seq 2.
	if err := w.write(path, 1, grpFromNames("older")); err != nil {
		t.Fatal(err)
	}
	got, err := loadProjectGroups(path)
	if err != nil || grpNames(got) != "newer" {
		t.Fatalf("file holds %s (err %v), want newer", grpNames(got), err)
	}
}

func TestLoadProjectGroups_CarriesTheLoadedGroups(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	path := config.ProjectGroupsPath()
	if err := saveProjectGroups(path, grpFromNames("Work")); err != nil {
		t.Fatal(err)
	}
	s, err := LoadProjectGroups(path)
	if err != nil || grpNames(s.groups) != "Work" {
		t.Fatalf("LoadProjectGroups = (%s, %v), want (Work, nil)", grpNames(s.groups), err)
	}
}
