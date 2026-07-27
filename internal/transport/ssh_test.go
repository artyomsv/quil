package transport

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestSSHArgs_PassesDestinationVerbatim(t *testing.T) {
	// A ~/.ssh/config Host alias must survive untouched — parsing it into
	// user/host/port and reassembling would discard HostName, Port, User and
	// ProxyJump, and a bastion hop is the common case for a cluster.
	for _, dest := range []string{"gpu01", "user@gpu01", "user@gpu01.example.com"} {
		args := sshArgs(dest, SSHOptions{})
		found := false
		for i, a := range args {
			if a == dest {
				found = true
				// The destination must be immediately followed by the remote
				// command and be the last option-free token before it.
				if i != len(args)-2 {
					t.Errorf("dest %q at index %d, want second-to-last of %v", dest, i, args)
				}
			}
		}
		if !found {
			t.Errorf("dest %q not present verbatim in %v", dest, args)
		}
	}
}

func TestSSHArgs_ForcesSecurityOptions(t *testing.T) {
	args := sshArgs("gpu01", SSHOptions{})
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"ForwardAgent=no",
		"ForwardX11=no",
		"ForwardX11Trusted=no",
		"PermitLocalCommand=no",
		"ClearAllForwardings=yes",
		"RequestTTY=no",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing forced option %q in %v", want, args)
		}
	}
}

// TestSSHArgs_DoesNotForceHostKeyChecking pins a deliberate decision:
// StrictHostKeyChecking=accept-new is WEAKER than the default prompt, so the
// user's own policy is left alone.
func TestSSHArgs_DoesNotForceHostKeyChecking(t *testing.T) {
	joined := strings.Join(sshArgs("gpu01", SSHOptions{}), " ")
	if strings.Contains(joined, "StrictHostKeyChecking") {
		t.Errorf("StrictHostKeyChecking must not be forced, got %q", joined)
	}
}

// TestSSHArgs_DoesNotForceProxyOptions pins that bastion support survives.
func TestSSHArgs_DoesNotForceProxyOptions(t *testing.T) {
	joined := strings.Join(sshArgs("gpu01", SSHOptions{}), " ")
	for _, forbidden := range []string{"ProxyCommand", "ProxyJump", "ControlMaster", "IdentityFile"} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("must not force %q, got %q", forbidden, joined)
		}
	}
}

func TestSSHArgs_NoTTY(t *testing.T) {
	// -T is mandatory: a PTY would apply CRLF translation and corrupt the
	// 4-byte big-endian length prefix on every frame.
	args := sshArgs("gpu01", SSHOptions{})
	hasT := false
	for _, a := range args {
		if a == "-T" {
			hasT = true
		}
	}
	if !hasT {
		t.Errorf("missing -T in %v", args)
	}
}

func TestSSHArgs_BatchMode(t *testing.T) {
	tests := []struct {
		name  string
		batch bool
		want  bool
	}{
		{"first dial is interactive so host-key and passphrase prompts work", false, false},
		{"reconnect is batch because no terminal is available", true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			joined := strings.Join(sshArgs("gpu01", SSHOptions{Batch: tt.batch}), " ")
			got := strings.Contains(joined, "BatchMode=yes")
			if got != tt.want {
				t.Errorf("BatchMode present = %v, want %v (args: %q)", got, tt.want, joined)
			}
		})
	}
}

func TestSSHArgs_DefaultRemoteCommand(t *testing.T) {
	args := sshArgs("gpu01", SSHOptions{})
	if got := args[len(args)-1]; got != DefaultRemoteCommand {
		t.Errorf("remote command = %q, want %q", got, DefaultRemoteCommand)
	}
}

func TestSSHArgs_CustomRemoteCommand(t *testing.T) {
	// A non-interactive login shell often lacks ~/.local/bin, so an absolute
	// path must be usable.
	args := sshArgs("gpu01", SSHOptions{RemoteCommand: "/opt/bin/quil --stdio"})
	if got := args[len(args)-1]; got != "/opt/bin/quil --stdio" {
		t.Errorf("remote command = %q, want the override", got)
	}
}

func TestSSHArgs_KeepsOptionOrderStable(t *testing.T) {
	// Regression guard: the args builder is pure, so its output must be
	// deterministic for a given input.
	a := sshArgs("gpu01", SSHOptions{Batch: true})
	b := sshArgs("gpu01", SSHOptions{Batch: true})
	if !reflect.DeepEqual(a, b) {
		t.Errorf("sshArgs is not deterministic:\n%v\n%v", a, b)
	}
}

func TestSSH_MissingBinary_ReturnsError(t *testing.T) {
	dial := SSH("gpu01", SSHOptions{SSHPath: "definitely-not-a-real-ssh-binary"})
	if _, err := dial(context.Background()); err == nil {
		t.Fatal("dial with a bogus ssh path succeeded, want error")
	}
}
