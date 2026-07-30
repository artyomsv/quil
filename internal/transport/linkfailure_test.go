package transport

import "testing"

// TestClassifyLinkFailure covers both directions, because both are dangerous.
//
// A missed permanent failure means the reconnect loop keeps authenticating
// forever. A false permanent means a session that would have healed is parked
// instead — the worse of the two, which is why every unmatched string must come
// back transient.
func TestClassifyLinkFailure(t *testing.T) {
	tests := []struct {
		name   string
		stderr string
		want   LinkFailure
	}{
		{"empty", "", LinkFailureTransient},

		// Permanent: an identical retry cannot succeed.
		{"publickey denied", "user@host: Permission denied (publickey).", LinkFailurePermanent},
		{"denied among ssh's own noise", "debug1: Offering public key\nPermission denied (publickey,password).\n", LinkFailurePermanent},
		{"host key changed", "@@@@ WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED! @@@@\nHost key verification failed.", LinkFailurePermanent},
		{"batch refused the host-key prompt", "Host key verification failed.", LinkFailurePermanent},
		{"host key type negotiation", "Unable to negotiate with 203.0.113.1 port 22: no matching host key type found.", LinkFailurePermanent},
		{"cipher negotiation", "Unable to negotiate with 203.0.113.1 port 22: no matching cipher found.", LinkFailurePermanent},
		{"mac negotiation", "Unable to negotiate with 203.0.113.1 port 22: no matching mac found.", LinkFailurePermanent},
		{"agent offered too many keys", "Received disconnect from 203.0.113.1 port 22:2: Too many authentication failures", LinkFailurePermanent},

		// Transient: retrying is exactly the right response.
		{"connect timeout", "ssh: connect to host gpu01 port 22: Connection timed out", LinkFailureTransient},
		{"refused", "ssh: connect to host 127.0.0.1 port 59999: Connection refused", LinkFailureTransient},
		{"dns", "ssh: Could not resolve hostname gpu01: Name or service not known", LinkFailureTransient},
		{"keepalive gave up", "Timeout, server 203.0.113.1 not responding.", LinkFailureTransient},
		{"broken pipe", "packet_write_wait: Connection to 203.0.113.1 port 22: Broken pipe", LinkFailureTransient},
		{"remote rebooted", "Connection to gpu01 closed by remote host.", LinkFailureTransient},
		{"network unreachable", "ssh: connect to host gpu01 port 22: Network is unreachable", LinkFailureTransient},

		// A message we have never seen must not park the session.
		{"unknown wording", "ssh: something nobody has written down yet", LinkFailureTransient},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyLinkFailure(tt.stderr, false, ExitSSHOwnFailure); got != tt.want {
				t.Errorf("ClassifyLinkFailure(%q, unestablished, 255) = %v, want %v", tt.stderr, got, tt.want)
			}
		})
	}
}

// TestClassifyLinkFailure_IsCaseInsensitive guards the lower-casing.
//
// Without it the matcher is a silent no-op on any casing ssh did not use, and
// nothing fails loudly — the loop simply never parks.
func TestClassifyLinkFailure_IsCaseInsensitive(t *testing.T) {
	if got := ClassifyLinkFailure("PERMISSION DENIED (PUBLICKEY).", false, ExitSSHOwnFailure); got != LinkFailurePermanent {
		t.Errorf("upper-cased denial classified as %v, want %v", got, LinkFailurePermanent)
	}
}

// TestLinkFailure_String pins the log wording, which is the only place an
// operator sees this value.
func TestLinkFailure_String(t *testing.T) {
	if got := LinkFailurePermanent.String(); got != "permanent" {
		t.Errorf("LinkFailurePermanent.String() = %q", got)
	}
	if got := LinkFailureTransient.String(); got != "transient" {
		t.Errorf("LinkFailureTransient.String() = %q", got)
	}
}

// TestClassifyLinkFailure_RemoteShellNoiseCannotPark is the finding this
// signature exists for.
//
// ssh multiplexes the remote command's fd 2 onto its own stderr, so the text
// includes whatever the far side's rc files printed — and "permission denied"
// is among the most common strings any Unix shell emits. The raw marker match
// cannot tell that from ssh's own denial, so the gates must, or an unreadable
// path in someone's ~/.bashrc parks their session and a compromised remote can
// do it on purpose.
func TestClassifyLinkFailure_RemoteShellNoiseCannotPark(t *testing.T) {
	shellNoise := "bash: /etc/profile.d/corp.sh: Permission denied"

	// The raw matcher DOES match it — that is the hazard, stated explicitly so
	// the gates below are not mistaken for belt-and-braces.
	if !matchesPermanentMarker(shellNoise) {
		t.Fatal("premise changed: the raw matcher no longer matches shell noise, " +
			"so the gates below may no longer be load-bearing — re-derive them")
	}

	tests := []struct {
		name        string
		established bool
		exitCode    int
		want        LinkFailure
	}{
		// The remote command ran and spoke, so ssh authenticated: any auth
		// marker in this stream belongs to the far side, not to ssh.
		{"remote command produced output", true, ExitSSHOwnFailure, LinkFailureTransient},
		// ssh passed through the remote command's status, so ssh itself did not
		// fail — again the text is the far side's.
		{"remote command's own exit status", false, 127, LinkFailureTransient},
		// Close killed a still-live ssh: a transient drop, not ssh's verdict.
		{"killed rather than exited", false, 1, LinkFailureTransient},
		{"never reaped", false, -1, LinkFailureTransient},
		// Both gates say ssh failed on its own before the remote ever ran.
		{"ssh's own failure", false, ExitSSHOwnFailure, LinkFailurePermanent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyLinkFailure(shellNoise, tt.established, tt.exitCode)
			if got != tt.want {
				t.Errorf("ClassifyLinkFailure(shellNoise, established=%v, exit=%d) = %v, want %v",
					tt.established, tt.exitCode, got, tt.want)
			}
		})
	}
}

// TestClassifyLinkFailure_EstablishedNeverParks pins the stronger of the two
// gates against EVERY permanent marker, so a marker added later cannot quietly
// become parkable on a link that already proved it authenticated.
func TestClassifyLinkFailure_EstablishedNeverParks(t *testing.T) {
	for _, m := range permanentMarkers {
		if got := ClassifyLinkFailure(m, true, ExitSSHOwnFailure); got != LinkFailureTransient {
			t.Errorf("marker %q on an established link = %v, want %v", m, got, LinkFailureTransient)
		}
	}
}
