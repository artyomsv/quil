//go:build windows

package ipc

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/winjob"
)

// TestACLTwoAccount is the manual check of whether AF_UNIX on Windows enforces
// a socket file's own ACL. It runs as a SERVER in one account and a CLIENT in
// another; docs/security.md lists the exact commands. The DIRECTORY stays
// permissive in every step; only the socket's DACL varies.
func TestACLTwoAccount(t *testing.T) {
	role := os.Getenv("QUIL_ACL_ROLE")
	if role == "" {
		t.Skip("manual two-account test: set QUIL_ACL_ROLE=server|client (docs/security.md)")
	}
	dir := os.Getenv("QUIL_ACL_DIR")
	sock := filepath.Join(dir, "t.sock")
	switch role {
	case "server":
		runACLServer(t, dir, sock, os.Getenv("QUIL_ACL_SOCKET"))
	case "client":
		c, err := net.DialTimeout("unix", sock, 3*time.Second)
		if err != nil {
			fmt.Println("RESULT: REFUSED:", err)
			return
		}
		defer c.Close()
		line, err := bufio.NewReader(c).ReadString('\n')
		if err != nil {
			fmt.Println("RESULT: REFUSED: read:", err)
			return
		}
		fmt.Println("RESULT: ACCEPTED", line)
	default:
		t.Fatalf("QUIL_ACL_ROLE=%q", role)
	}
}

func runACLServer(t *testing.T, dir, sock, mode string) {
	sid, err := winjob.CurrentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Permissive directory: BUILTIN\Users may traverse and create (modify).
	if err := applySDDL(dir, "D:P(A;OICI;FA;;;"+sid+")(A;OICI;FA;;;SY)(A;OICI;0x1301bf;;;BU)"); err != nil {
		t.Fatal(err)
	}
	os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	switch mode {
	case "protected":
		err = applySDDL(sock, ownerOnlySDDL(sid, false))
	case "permissive":
		err = applySDDL(sock, "D:P(A;;FA;;;"+sid+")(A;;FA;;;SY)(A;;FA;;;BU)")
	default:
		t.Fatalf("QUIL_ACL_SOCKET=%q (want protected|permissive)", mode)
	}
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("LISTENING mode=%s sock=%s (Ctrl+C to stop)\n", mode, sock)
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		fmt.Fprintln(c, "ok")
		c.Close()
	}
}
