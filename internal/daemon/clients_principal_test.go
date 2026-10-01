package daemon

import (
	"errors"
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
)

func TestClients_ReadOnlyNeverEligible(t *testing.T) {
	h := newClientsHarness(t, testGrace)
	r := &h.d.clients
	if _, err := r.attach(new(ipc.Conn), "viewer", 200, 50, "", false, "token 0a1b2c3d", true); err != nil {
		t.Fatal(err)
	}
	h.wantMaster("")
	c := new(ipc.Conn)
	if _, err := r.attach(c, "viewer2", 200, 50, "", false, "token 0a1b2c3d", true); err != nil {
		t.Fatal(err)
	}
	// The two asserts below cannot fail ALONE: takeControl and setGeometry
	// already consult eligible, which refuses a read-only record. Their own
	// readOnly checks stay as defence in depth; the readOnly term in eligible
	// is what this test holds.
	if changed, accepted := r.takeControl(c); changed || accepted {
		t.Fatal("take_control accepted from a read-only record")
	}
	if r.setGeometry(c, 300, 80) {
		t.Fatal("setGeometry changed the election for a read-only record")
	}
}

func TestClients_PrincipalMismatchRefused(t *testing.T) {
	h := newClientsHarness(t, testGrace)
	r := &h.d.clients
	if _, err := r.attach(new(ipc.Conn), "owner", 200, 50, "", false, ipc.PrincipalLocal, false); err != nil {
		t.Fatal(err)
	}
	if _, err := r.attach(new(ipc.Conn), "owner", 200, 50, "", false, "token 0a1b2c3d", false); !errors.Is(err, errClientIDInUse) {
		t.Fatalf("err = %v, want errClientIDInUse", err)
	}
}

// The local socket always wins: a token conn that took the owner's id while
// the owner was away cannot lock the owner's own reattach out.
func TestClients_LocalOwnerReclaimsIDFromTokenConn(t *testing.T) {
	h := newClientsHarness(t, testGrace)
	r := &h.d.clients
	squatter := new(ipc.Conn)
	if _, err := r.attach(squatter, "owner", 200, 50, "", false, "token 0a1b2c3d", false); err != nil {
		t.Fatal(err)
	}
	owner := new(ipc.Conn)
	if _, err := r.attach(owner, "owner", 200, 50, "", true, ipc.PrincipalLocal, false); err != nil {
		t.Fatalf("the owner's local reattach was refused: %v", err)
	}
	if _, ok := r.byConn[squatter]; ok {
		t.Error("the token conn still holds a record for the owner's id")
	}
	if rec, ok := r.byConn[owner]; !ok || rec.principal != ipc.PrincipalLocal {
		t.Errorf("owner record = %+v, want a local record", rec)
	}
}

func TestClients_ViewerCannotTakeReservedSlot(t *testing.T) {
	h := newClientsHarness(t, testGrace)
	h.d.clients.reserveAfterRestart("owner")
	if _, err := h.d.clients.attach(new(ipc.Conn), "owner", 200, 50, "", true, "token 0a1b2c3d", true); !errors.Is(err, errClientIDInUse) {
		t.Fatalf("err = %v, want errClientIDInUse for a viewer claiming a reserved id", err)
	}
}

// reservationAdmits refuses a read-only claimant even under the principal
// that lost the slot. Unreachable through attach today (a read-only record is
// never master, so it never leaves a grace reserve behind), which is why it
// is pinned here directly.
func TestReservationAdmits(t *testing.T) {
	grace := &reservation{id: "owner", principal: "token 0a1b2c3d"}
	restart := &reservation{id: "owner"}
	tests := []struct {
		name      string
		res       *reservation
		principal string
		readOnly  bool
		want      bool
	}{
		{"grace, same principal", grace, "token 0a1b2c3d", false, true},
		{"grace, same principal, read-only", grace, "token 0a1b2c3d", true, false},
		{"grace, other token", grace, "token ffffffff", false, false},
		{"grace, local", grace, ipc.PrincipalLocal, false, false},
		{"restart, local", restart, ipc.PrincipalLocal, false, true},
		{"restart, token", restart, "token 0a1b2c3d", false, false},
		{"restart, local read-only", restart, ipc.PrincipalLocal, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := reservationAdmits(tt.res, tt.principal, tt.readOnly); got != tt.want {
				t.Errorf("reservationAdmits = %v, want %v", got, tt.want)
			}
		})
	}
}

// A RESTART reservation does not know the lost master's principal, so it
// admits only the LOCAL principal — even a full-rights token that was master
// before the restart attaches as an ordinary client.
func TestClients_TokenCannotTakeRestartSlot(t *testing.T) {
	h := newClientsHarness(t, testGrace)
	h.d.clients.reserveAfterRestart("owner")
	if _, err := h.d.clients.attach(new(ipc.Conn), "owner", 200, 50, "", true, "token 0a1b2c3d", false); !errors.Is(err, errClientIDInUse) {
		t.Fatalf("err = %v, want errClientIDInUse for a full token claiming a restart-reserved id", err)
	}
	if _, err := h.d.clients.attach(new(ipc.Conn), "owner", 200, 50, "", true, ipc.PrincipalLocal, false); err != nil {
		t.Fatalf("the local owner could not claim its restart slot: %v", err)
	}
	h.wantMaster("owner")
}
