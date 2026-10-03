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
//
// The server never changes the ACL of anything that already exists: it
// creates a fresh quil-acl-* directory under QUIL_ACL_DIR (which must exist,
// and which the second account must be able to traverse, e.g.
// C:\Temp\quil-acl) and makes only that new directory permissive. It prints
// the socket path, which the client takes as QUIL_ACL_SOCK:
//
//	server: QUIL_ACL_ROLE=server QUIL_ACL_DIR=<existing dir> QUIL_ACL_SOCKET=protected|permissive
//	client: QUIL_ACL_ROLE=client QUIL_ACL_SOCK=<the LISTENING sock= path>
func TestACLTwoAccount(t *testing.T) {
	role := os.Getenv("QUIL_ACL_ROLE")
	if role == "" {
		t.Skip("manual two-account test: set QUIL_ACL_ROLE=server|client (docs/security.md)")
	}
	switch role {
	case "server":
		runACLServer(t, os.Getenv("QUIL_ACL_DIR"), os.Getenv("QUIL_ACL_SOCKET"))
	case "client":
		sock := os.Getenv("QUIL_ACL_SOCK")
		if sock == "" {
			t.Fatal("QUIL_ACL_SOCK is empty: pass the sock= path the server printed")
		}
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

func runACLServer(t *testing.T, parent, mode string) {
	if mode != "protected" && mode != "permissive" {
		t.Fatalf("QUIL_ACL_SOCKET=%q (want protected|permissive)", mode)
	}
	if parent == "" {
		t.Fatal("QUIL_ACL_DIR is empty: name an existing directory the second account can traverse")
	}
	if fi, err := os.Stat(parent); err != nil || !fi.IsDir() {
		t.Fatalf("QUIL_ACL_DIR=%q must be an existing directory (stat: %v)", parent, err)
	}
	sid, err := winjob.CurrentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	// A directory of our own, so the permissive ACL below can never land on
	// a tree that already holds anything.
	dir, err := os.MkdirTemp(parent, "quil-acl-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	// Permissive directory: BUILTIN\Users may traverse and create (modify).
	if err := applySDDL(dir, "D:P(A;OICI;FA;;;"+sid+")(A;OICI;FA;;;SY)(A;OICI;0x1301bf;;;BU)"); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "t.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if mode == "protected" {
		err = applySDDL(sock, ownerOnlySDDL(sid, false))
	} else {
		err = applySDDL(sock, "D:P(A;;FA;;;"+sid+")(A;;FA;;;SY)(A;;FA;;;BU)")
	}
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("LISTENING mode=%s sock=%s (Ctrl+C to stop; delete %s afterwards if it remains)\n", mode, sock, dir)
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		fmt.Fprintln(c, "ok")
		c.Close()
	}
}
