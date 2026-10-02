package ipc

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

const testSID = "S-1-5-21-1111111111-2222222222-3333333333-1001"

// ownTest stands in for the Windows matcher, which compares SID values.
func ownTest(sid string) bool { return sid == testSID || sid == "SY" }

func TestOwnerOnlySDDL_Forms(t *testing.T) {
	if got, want := ownerOnlySDDL(testSID, true), "D:P(A;OICI;FA;;;"+testSID+")(A;OICI;FA;;;SY)"; got != want {
		t.Errorf("inherit form = %q, want %q", got, want)
	}
	if got, want := ownerOnlySDDL(testSID, false), "D:P(A;;FA;;;"+testSID+")(A;;FA;;;SY)"; got != want {
		t.Errorf("file form = %q, want %q", got, want)
	}
}

func TestForeignAllowSIDs(t *testing.T) {
	sddl := "O:" + testSID + "G:" + testSID + "D:P(A;OICI;FA;;;" + testSID + ")(A;OICI;FA;;;SY)" +
		"(A;OICI;0x1301bf;;;BU)(D;;FA;;;WD)(A;ID;FA;;;BA)"
	got := foreignAllowSIDs(sddl, ownTest)
	if want := []string{"BU", "BA"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("foreign = %v, want %v (deny ACEs ignored)", got, want)
	}
	if got := foreignAllowSIDs(ownerOnlySDDL(testSID, true), ownTest); len(got) != 0 {
		t.Fatalf("owner-only DACL reported foreign entries %v", got)
	}
}

// A NULL or absent DACL lets everyone in, so it must never read as owner-only
// — while an EMPTY DACL (no entries) lets nobody in and is not foreign.
func TestForeignAllowSIDs_NullDACLIsForeign(t *testing.T) {
	for _, sddl := range []string{"D:NO_ACCESS_CONTROL", "D:PNO_ACCESS_CONTROL", "O:SYG:SYD:NO_ACCESS_CONTROL", ""} {
		if got := foreignAllowSIDs(sddl, ownTest); !reflect.DeepEqual(got, []string{nullDACL}) {
			t.Errorf("foreignAllowSIDs(%q) = %v, want [%s]", sddl, got, nullDACL)
		}
	}
	if got := foreignAllowSIDs("D:P", ownTest); len(got) != 0 {
		t.Errorf("empty DACL reported foreign entries %v", got)
	}
}

func TestSocketACLFailure(t *testing.T) {
	aclErr := errors.New("acl refused")
	if err := socketACLFailure("s", aclErr, "", nil); err != nil {
		t.Errorf("owner-only directory: got %v, want nil (a warning only)", err)
	}
	if err := socketACLFailure("s", aclErr, "dir also grants access to [BU]", nil); err == nil ||
		!errors.Is(err, aclErr) || !strings.Contains(err.Error(), "BU") {
		t.Errorf("open directory: got %v, want a fatal error naming both failures", err)
	}
	dirErr := errors.New("read denied")
	if err := socketACLFailure("s", aclErr, "", dirErr); err == nil ||
		!errors.Is(err, dirErr) || !strings.Contains(err.Error(), "acl refused") {
		t.Errorf("unreadable directory: got %v, want a fatal error naming both failures", err)
	}
}
