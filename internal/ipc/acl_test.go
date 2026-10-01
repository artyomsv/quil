package ipc

import (
	"reflect"
	"testing"
)

const testSID = "S-1-5-21-1111111111-2222222222-3333333333-1001"

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
	got := foreignAllowSIDs(sddl, testSID, "SY")
	if want := []string{"BU", "BA"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("foreign = %v, want %v (deny ACEs ignored)", got, want)
	}
	if got := foreignAllowSIDs(ownerOnlySDDL(testSID, true), testSID, "SY"); len(got) != 0 {
		t.Fatalf("owner-only DACL reported foreign entries %v", got)
	}
}
