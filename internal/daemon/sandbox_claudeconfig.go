package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/artyomsv/quil/internal/sandbox"
)

// Seeding a sandbox pane's Claude config so the pane OPENS READY.
//
// A forwarded CLAUDE_CODE_OAUTH_TOKEN authenticates API calls — measured, a
// `claude -p` in a fresh container answers correctly with nothing but the env
// var. It does NOT skip first-run onboarding, and interactive Claude Code on an
// empty config directory runs that: theme picker, sign-in screen, trust
// prompt, bypass-permissions banner. Every sandbox pane gets its own config
// directory, so the user met all four EVERY TIME — which reads as "the token
// did not work" when the token was working the whole time.
//
// Each key below was verified against the real image rather than guessed, by
// running the container and reading what came up:
//
//	empty config                          -> theme picker, then sign-in
//	hasCompletedOnboarding + theme        -> trust prompt
//	+ per-project hasTrustDialogAccepted  -> bypass-permissions banner
//	+ bypassPermissionsModeAccepted       -> a working prompt
//
// The `projects` entry needs the fuller shape it has here: a minimal one made
// Claude rewrite the file and the theme picker came back.

// claudeSeedTheme is the one cosmetic choice this has to make. Skipping the
// picker means answering it, and a pane on a dark terminal is the common case;
// `/theme` changes it and the answer persists in the pane's own config.
const claudeSeedTheme = "dark"

// claudeConfigName and claudeOnboardingKey are named because TWO files now
// depend on them meaning the same thing: this one writes the answer, and
// sandbox_authstamp.go removes it when the pane stops receiving the credential
// that answer assumed. A drift between the two is a repair that silently
// repairs nothing.
const (
	claudeConfigName    = ".claude.json"
	claudeOnboardingKey = "hasCompletedOnboarding"
)

// seedClaudeConfig writes a first-run config for a sandbox pane, unless one is
// already there.
//
// ONLY when the container will actually receive a credential. Skipping
// onboarding also skips the SIGN-IN SCREEN INSIDE IT, so seeding an
// unauthenticated pane hands the user a working-looking prompt that fails on
// its first request with no way to log in — measured, and a regression this
// function introduced for `auth = "browser"` before the gate existed. In that
// mode the onboarding login is not a nuisance to skip past; it is the entire
// sign-in.
//
// NEVER overwrites. The file is the pane's own state once Claude has written
// to it, and under `shared_claude_config` it belongs to every sandbox pane at
// once — clobbering it would discard a real sign-in, the user's theme, and
// their per-project trust answers.
func seedClaudeConfig(m sandbox.Mapping, authed bool) error {
	if !authed {
		return nil
	}
	path := filepath.Join(m.HostClaudeConfig(), claudeConfigName)
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat claude config: %w", err)
	}

	// The container's own view of the working directory, not the host's:
	// Claude keys its project entries by the path IT sees, which is where
	// `docker run -w` puts it.
	project := map[string]any{
		"allowedTools":                  []string{},
		"history":                       []any{},
		"hasTrustDialogAccepted":        true,
		"projectOnboardingSeenCount":    1,
		"bypassPermissionsModeAccepted": true,
	}
	seed := map[string]any{
		claudeOnboardingKey:             true,
		"theme":                         claudeSeedTheme,
		"bypassPermissionsModeAccepted": true,
		"projects":                      map[string]any{m.ContainerCWD(): project},
	}

	body, err := json.Marshal(seed)
	if err != nil {
		return fmt.Errorf("encode claude config seed: %w", err)
	}
	// 0600 and written directly rather than through a temp+rename: the
	// directory is this pane's own, nothing else is reading the file yet, and
	// a half-written seed is recovered by deleting it — Claude rewrites the
	// file wholesale on its first run anyway.
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return fmt.Errorf("write claude config seed: %w", err)
	}
	return nil
}
