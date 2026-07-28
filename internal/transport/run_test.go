package transport

import (
	"context"
	"strings"
	"testing"
)

// A destination beginning with '-' is parsed by ssh as an option, and
// -oProxyCommand= executes a command on THIS machine before any network
// traffic. The guard is duplicated at every entry point on purpose; this pins
// that RunSSH has its own rather than relying on a caller's.
func TestRunSSH_RejectsOptionLikeDestination(t *testing.T) {
	code, err := RunSSH(context.Background(), "-oProxyCommand=touch /tmp/pwned",
		SSHOptions{}, "true", nil, nil, nil)
	if err == nil {
		t.Fatal("error = nil, want rejection")
	}
	if code != -1 {
		t.Errorf("code = %d, want -1 for a command that never ran", code)
	}
	if !strings.Contains(err.Error(), "must not begin with '-'") {
		t.Errorf("error %q does not explain the rejection", err)
	}
}

func TestRunSSH_RejectsEmptyCommand(t *testing.T) {
	if _, err := RunSSH(context.Background(), "host", SSHOptions{}, "", nil, nil, nil); err == nil {
		t.Fatal("error = nil, want rejection of an empty remote command")
	}
}

func TestRunSSH_ReportsMissingSSHBinary(t *testing.T) {
	code, err := RunSSH(context.Background(), "host",
		SSHOptions{SSHPath: "definitely-not-a-real-ssh-binary"}, "true", nil, nil, nil)
	if err == nil {
		t.Fatal("error = nil, want error")
	}
	if code != -1 {
		t.Errorf("code = %d, want -1", code)
	}
}

// RunSSH must build its argument vector through sshArgs so the hardening
// options cannot diverge between the dial path and the command path.
func TestRunSSH_SharesHardeningWithDial(t *testing.T) {
	args := sshArgs("host", SSHOptions{RemoteCommand: "sh -s"})
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"ForwardAgent=no",
		"ClearAllForwardings=yes",
		"PermitLocalCommand=no",
		"ConnectTimeout=",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("sshArgs is missing %q: %v", want, args)
		}
	}
	// The command must be last and the destination immediately before it.
	if args[len(args)-1] != "sh -s" {
		t.Errorf("last arg = %q, want the remote command", args[len(args)-1])
	}
	if args[len(args)-2] != "host" {
		t.Errorf("second-to-last arg = %q, want the destination", args[len(args)-2])
	}
}
