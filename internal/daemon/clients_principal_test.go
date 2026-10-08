package daemon

import (
	"errors"
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
)

// An attach names the clients it tells under the registry's own lock: the
// ones attached at its registration, never one that registers after it.
func TestClients_AttachListsTheOthersAtRegistration(t *testing.T) {
	h := newClientsHarness(t, testGrace)
	r := &h.d.clients
	a, b := new(ipc.Conn), new(ipc.Conn)
	first, err := r.attach(a, "A", 200, 50, "", false, ipc.PrincipalLocal, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.others) != 0 {
		t.Fatalf("the first attach lists %d others, want none", len(first.others))
	}
	second, err := r.attach(b, "B", 100, 30, "", false, ipc.PrincipalLocal, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.others) != 1 || second.others[0] != a {
		t.Fatalf("the second attach lists %v, want only A's conn", second.others)
	}
	// The first attach's list was fixed when it registered: B is not on it.
	if len(first.others) != 0 {
		t.Fatal("the first attach's list grew after the fact")
	}
}

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

// wantNoMasterConn requires that no attached conn holds the slot, by every
// reader of it: the slot lookup, each conn, list_clients, and the state
// frame — which must still name SOME master (a TUI reads "" as "no master"
// and leaves follower mode while every resize is still dropped), but one no
// attached client can mistake for itself.
func wantNoMasterConn(t *testing.T, d *Daemon, conns ...*ipc.Conn) {
	t.Helper()
	if c := d.masterConn(); c != nil {
		t.Fatal("a conn holds the master slot")
	}
	for _, c := range conns {
		if d.isMasterConn(c) {
			t.Fatal("isMasterConn is true for a conn that must not hold the slot")
		}
	}
	for _, info := range d.listClients() {
		if info.Master {
			t.Fatalf("list_clients reports %q as master", info.Client)
		}
	}
	id := d.sizeMasterForState()
	if id == "" {
		t.Fatal("the state frame names no size master, so every TUI leaves follower mode")
	}
	for _, info := range d.listClients() {
		if info.Client == id {
			t.Fatalf("the state frame names attached client %q as size master", id)
		}
	}
}

// lostTokenMaster leaves a lost-master reservation for "owner" that recorded
// the token principal it was held by, with a local follower attached.
func lostTokenMaster(t *testing.T, h *clientsHarness, principal string) {
	t.Helper()
	r := &h.d.clients
	m := new(ipc.Conn)
	if _, err := r.attach(m, "owner", 200, 50, "", false, principal, false); err != nil {
		t.Fatal(err)
	}
	if _, err := r.attach(new(ipc.Conn), "follower", 200, 50, "", false, ipc.PrincipalLocal, false); err != nil {
		t.Fatal(err)
	}
	h.wantMaster("owner")
	r.lose(m)
	if r.reserved == nil || r.reserved.principal != principal {
		t.Fatalf("reservation = %+v, want one recording %q", r.reserved, principal)
	}
}

// A viewer naming an id a restart reserve keeps is admitted as an ordinary
// client: a read-only record never holds the slot, and the reserve keeps
// waiting for a local claimant.
func TestClients_ViewerUnderRestartReserveGetsNoSlot(t *testing.T) {
	h := newClientsHarness(t, testGrace)
	h.d.clients.reserveAfterRestart("owner")
	c := new(ipc.Conn)
	if _, err := h.d.clients.attach(c, "owner", 200, 50, "", true, "token 0a1b2c3d", true); err != nil {
		t.Fatalf("a viewer naming a restart-reserved id was refused: %v", err)
	}
	wantNoMasterConn(t, h.d, c)
	if h.d.clients.reserved == nil {
		t.Fatal("the viewer's attach consumed the restart reserve")
	}
}

// A reservation that recorded its principal refuses any other: another
// token, and a viewer.
func TestClients_GraceReserveRefusesAnotherPrincipal(t *testing.T) {
	h := newClientsHarness(t, testGrace)
	lostTokenMaster(t, h, "token 0a1b2c3d")
	r := &h.d.clients
	if _, err := r.attach(new(ipc.Conn), "owner", 200, 50, "", true, "token ffffffff", false); !errors.Is(err, errClientIDInUse) {
		t.Fatalf("err = %v, want errClientIDInUse for another token", err)
	}
	if _, err := r.attach(new(ipc.Conn), "owner", 200, 50, "", true, "token ffffffff", true); !errors.Is(err, errClientIDInUse) {
		t.Fatalf("err = %v, want errClientIDInUse for a viewer", err)
	}
	back := new(ipc.Conn)
	if _, err := r.attach(back, "owner", 200, 50, "", true, "token 0a1b2c3d", false); err != nil {
		t.Fatalf("the principal that lost the slot could not reclaim it: %v", err)
	}
	if !h.d.isMasterConn(back) {
		t.Fatal("the returning principal did not get its slot back")
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

// A RESTART reservation does not know the lost master's principal, so only
// the LOCAL principal can claim it. A full-rights token naming that id is
// ADMITTED as an ordinary client — refusing it would strand a remote TUI,
// which logs an attach error and never retries — but does not inherit the
// slot, and the reserve stays claimable by a later local attach.
func TestClients_TokenUnderRestartReserveIsAdmittedWithoutTheSlot(t *testing.T) {
	h := newClientsHarness(t, testGrace)
	r := &h.d.clients
	r.reserveAfterRestart("owner")
	tok := new(ipc.Conn)
	if _, err := r.attach(tok, "owner", 200, 50, "", true, "token 0a1b2c3d", false); err != nil {
		t.Fatalf("a full token naming a restart-reserved id was refused: %v", err)
	}
	if h.d.clientCount() != 1 {
		t.Fatalf("clients = %d, want the token's record registered", h.d.clientCount())
	}
	wantNoMasterConn(t, h.d, tok)
	if r.reserved == nil {
		t.Fatal("the token's attach consumed the restart reserve")
	}

	owner := new(ipc.Conn)
	if _, err := r.attach(owner, "owner", 200, 50, "", true, ipc.PrincipalLocal, false); err != nil {
		t.Fatalf("the local owner could not claim its restart slot: %v", err)
	}
	if !h.d.isMasterConn(owner) || h.d.sizeMasterForState() != "owner" {
		t.Fatal("the local owner did not get the reserved slot")
	}
	if _, ok := r.byConn[tok]; ok {
		t.Error("the token conn still holds a record for the owner's id")
	}
}

// A first attach (Reattach false) that names the reserved id without
// claiming it is no cold start of a new process, so it does not end the wait
// for the local claimant even when it is alone.
func TestClients_TokenFirstAttachUnderRestartReserveKeepsTheReserve(t *testing.T) {
	h := newClientsHarness(t, testGrace)
	r := &h.d.clients
	r.reserveAfterRestart("owner")
	tok := new(ipc.Conn)
	if _, err := r.attach(tok, "owner", 200, 50, "", false, "token 0a1b2c3d", false); err != nil {
		t.Fatal(err)
	}
	if r.reserved == nil {
		t.Fatal("a lone first attach naming the reserved id cleared the restart reserve")
	}
	wantNoMasterConn(t, h.d, tok)
}

// A restart reserve waits for the LOCAL principal, so a token client — a
// viewer or a full one — attaching first and alone under ANOTHER id is no
// cold start that may end the wait.
func TestClients_TokenFirstAttachAloneKeepsTheRestartReserve(t *testing.T) {
	for _, readOnly := range []bool{true, false} {
		h := newClientsHarness(t, testGrace)
		r := &h.d.clients
		r.reserveAfterRestart("owner")
		if _, err := r.attach(new(ipc.Conn), "newcomer", 200, 50, "", false, "token 0a1b2c3d", readOnly); err != nil {
			t.Fatal(err)
		}
		if r.reserved == nil {
			t.Fatalf("readOnly=%v: a lone token first attach cleared the restart reserve", readOnly)
		}
		h.wantMaster("owner")
	}
	// Control: a lone LOCAL first attach is the cold start that clears it.
	h := newClientsHarness(t, testGrace)
	r := &h.d.clients
	r.reserveAfterRestart("owner")
	if _, err := r.attach(new(ipc.Conn), "newcomer", 200, 50, "", false, ipc.PrincipalLocal, false); err != nil {
		t.Fatal(err)
	}
	if r.reserved != nil {
		t.Fatal("control: a lone local first attach did not clear the restart reserve")
	}
	h.wantMaster("newcomer")
}

// The marker a state frame names for a shadowed slot is longer than any
// client id attach can register, so no TUI can read it as its own.
func TestReservedMasterMarker_NamesNoClient(t *testing.T) {
	for _, id := range []string{"a", "owner", strings.Repeat("x", maxClientIDLen)} {
		m := reservedMasterMarker(id)
		if !strings.HasPrefix(m, reservedMasterPrefix) {
			t.Errorf("marker %q lacks the prefix", m)
		}
		if len(m) <= maxClientIDLen || truncateField(m, maxClientIDLen) == m {
			t.Errorf("marker %q (len %d) fits a client id", m, len(m))
		}
	}
}

// Losing the admitted token's link leaves the restart reserve as it was: the
// token never held the slot, so it leaves no grace reserve of its own behind.
func TestClients_LosingTheTokenKeepsTheRestartReserve(t *testing.T) {
	h := newClientsHarness(t, testGrace)
	r := &h.d.clients
	r.reserveAfterRestart("owner")
	tok := new(ipc.Conn)
	if _, err := r.attach(tok, "owner", 200, 50, "", true, "token 0a1b2c3d", false); err != nil {
		t.Fatal(err)
	}
	if _, err := r.attach(new(ipc.Conn), "follower", 200, 50, "", true, ipc.PrincipalLocal, false); err != nil {
		t.Fatal(err)
	}
	r.lose(tok)
	if r.reserved == nil || r.reserved.principal != "" {
		t.Fatalf("reservation = %+v, want the restart reserve kept", r.reserved)
	}
	owner := new(ipc.Conn)
	if _, err := r.attach(owner, "owner", 200, 50, "", true, ipc.PrincipalLocal, false); err != nil {
		t.Fatal(err)
	}
	if !h.d.isMasterConn(owner) {
		t.Fatal("the local owner did not get its restart slot")
	}
}

// With no local claimant, the restart reserve lapses and the admitted token
// is elected like any other client — and the change is published, because
// the id the state frame names goes from "" to its own.
func TestClients_TokenUnderRestartReserveElectedWhenItLapses(t *testing.T) {
	h := newClientsHarness(t, testGrace)
	r := &h.d.clients
	r.reserveAfterRestart("owner")
	tok := new(ipc.Conn)
	if _, err := r.attach(tok, "owner", 200, 50, "", true, "token 0a1b2c3d", false); err != nil {
		t.Fatal(err)
	}
	wantNoMasterConn(t, h.d, tok)
	h.advance(restartReserveCap)
	h.fire()
	if !h.d.isMasterConn(tok) || h.d.sizeMasterForState() != "owner" {
		t.Fatal("the token was not elected once the reserve lapsed")
	}
	if h.changes != 1 {
		t.Fatalf("changes = %d, want the election published once", h.changes)
	}
}
