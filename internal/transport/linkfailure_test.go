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
		{"host key type negotiation", "Unable to negotiate with 10.0.0.1 port 22: no matching host key type found.", LinkFailurePermanent},
		{"cipher negotiation", "Unable to negotiate with 10.0.0.1 port 22: no matching cipher found.", LinkFailurePermanent},
		{"mac negotiation", "Unable to negotiate with 10.0.0.1 port 22: no matching mac found.", LinkFailurePermanent},
		{"agent offered too many keys", "Received disconnect from 10.0.0.1 port 22:2: Too many authentication failures", LinkFailurePermanent},

		// Transient: retrying is exactly the right response.
		{"connect timeout", "ssh: connect to host gpu01 port 22: Connection timed out", LinkFailureTransient},
		{"refused", "ssh: connect to host 127.0.0.1 port 59999: Connection refused", LinkFailureTransient},
		{"dns", "ssh: Could not resolve hostname gpu01: Name or service not known", LinkFailureTransient},
		{"keepalive gave up", "Timeout, server 10.0.0.1 not responding.", LinkFailureTransient},
		{"broken pipe", "packet_write_wait: Connection to 10.0.0.1 port 22: Broken pipe", LinkFailureTransient},
		{"remote rebooted", "Connection to gpu01 closed by remote host.", LinkFailureTransient},
		{"network unreachable", "ssh: connect to host gpu01 port 22: Network is unreachable", LinkFailureTransient},

		// A message we have never seen must not park the session.
		{"unknown wording", "ssh: something nobody has written down yet", LinkFailureTransient},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyLinkFailure(tt.stderr); got != tt.want {
				t.Errorf("ClassifyLinkFailure(%q) = %v, want %v", tt.stderr, got, tt.want)
			}
		})
	}
}

// TestClassifyLinkFailure_IsCaseInsensitive guards the lower-casing.
//
// Without it the matcher is a silent no-op on any casing ssh did not use, and
// nothing fails loudly — the loop simply never parks.
func TestClassifyLinkFailure_IsCaseInsensitive(t *testing.T) {
	if got := ClassifyLinkFailure("PERMISSION DENIED (PUBLICKEY)."); got != LinkFailurePermanent {
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
