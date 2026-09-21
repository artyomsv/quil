package config

import "testing"

// The zero value has now meant each mode in turn, and the reason it settled on
// the browser flow is not a preference — it is that the token flow reaches
// FURTHER THAN THE PANE. `claude setup-token` mints a credential the daemon
// saves to the user's own environment, and every process started afterwards
// inherits it, so a subscriber who opened one sandbox pane then found every
// ORDINARY Claude pane authenticating as "Claude API" with a smaller /model
// list. A default that can silently move a subscriber's usage onto API billing
// is the wrong default whatever the sign-in cost of the alternative.
//
// It briefly meant browser before, for an unrelated reason, and was changed to
// token because "" was then indistinguishable from "unset": no one could ASK
// for the fallback, so every pane re-prompted with nothing explaining why. That
// is no longer true — "browser" is a value a config can name — so the fallback
// being the zero value costs nothing that cannot be opted out of.
func TestResolveAuth(t *testing.T) {
	tests := []struct {
		name           string
		auth           string
		wantMode       SandboxAuthMode
		wantUnrecognis string
	}{
		// The load-bearing row. Every config.toml already on disk names
		// `auth = ""`, and Load lets the decoder overwrite the keys a file
		// names — so changing Default() alone would reach no existing
		// install. This is the only change that reaches everyone.
		{"unset resolves to the browser flow", "", SandboxAuthBrowser, ""},
		// Still reachable, and still exactly what it says: a user who names it
		// has accepted that the token is saved machine-wide.
		{"token is explicit", "token", SandboxAuthToken, ""},
		{"browser is explicit", "browser", SandboxAuthBrowser, ""},
		// A typo must not silently look like a deliberate choice.
		{"a typo falls back and is reported", "toekn", SandboxAuthBrowser, "toekn"},
		{"case is not guessed at", "Token", SandboxAuthBrowser, "Token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mode, unrecognised := SandboxConfig{Auth: tt.auth}.ResolveAuth()
			if mode != tt.wantMode {
				t.Errorf("mode = %q, want %q", mode, tt.wantMode)
			}
			if unrecognised != tt.wantUnrecognis {
				t.Errorf("unrecognised = %q, want %q", unrecognised, tt.wantUnrecognis)
			}
		})
	}
}

// Default() does not mention Sandbox at all, so the zero value IS the shipped
// default. If that ever changes, the migration reasoning above changes with
// it — this pins the premise rather than the value.
func TestDefault_SandboxAuthIsTheZeroValue(t *testing.T) {
	if got := Default().Sandbox.Auth; got != "" {
		t.Errorf("Default().Sandbox.Auth = %q; the ResolveAuth migration assumes the zero value", got)
	}
	if mode, _ := Default().Sandbox.ResolveAuth(); mode != SandboxAuthBrowser {
		t.Errorf("a default config resolves to %q, want the browser flow — a default "+
			"that mints a machine-wide token can move a subscriber onto API billing", mode)
	}
}

// The property the default exists to protect, stated as a test rather than
// left to the comment: nothing a user has not explicitly asked for may select
// the mode that writes CLAUDE_CODE_OAUTH_TOKEN into the user's environment.
//
// Written against the resolver rather than against a literal, because the
// defect this guards is a DEFAULT changing, and a test naming "browser" twice
// would still pass if "" started resolving to token again.
func TestResolveAuth_OnlyAnExplicitChoiceSelectsTheToken(t *testing.T) {
	for _, auth := range []string{"", "browser", "toekn", "Token", "TOKEN", "  token  "} {
		if mode, _ := (SandboxConfig{Auth: auth}).ResolveAuth(); mode == SandboxAuthToken {
			t.Errorf("auth = %q resolved to the token flow; only the exact string "+
				"%q may, or a typo silently persists a credential machine-wide",
				auth, SandboxAuthToken)
		}
	}
}
