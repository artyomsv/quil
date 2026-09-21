package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/sandbox"
)

// claudeAuthStampName records which sign-in mode a Claude config directory was
// last prepared under.
//
// It exists because the two modes leave INCOMPATIBLE state behind and nothing
// else can tell them apart. seedClaudeConfig writes `hasCompletedOnboarding`
// so a token pane opens on a working prompt instead of four first-run screens
// — and the SIGN-IN is one of the screens that answer skips. Move the same
// directory to the browser flow and the token is gone while the answer stays,
// so the pane opens on a prompt with no credential and no way to log in. That
// is not hypothetical: changing the default from token to browser did it to
// every existing pane at once.
//
// Kept beside the config rather than on the Pane, because the CONFIG DIRECTORY
// is what carries the stale answer — and under `shared_claude_config` one
// directory serves every sandbox pane, so a per-pane record would say nothing
// about the file being repaired.
const claudeAuthStampName = ".quil-auth"

// readClaudeAuthStamp answers the mode the directory was last prepared under,
// or "" when there is no stamp or it cannot be understood.
//
// Through an os.Root over the config directory, like every other daemon-side
// read under a pane's tree: the directory is written by a process inside the
// container, and on a Linux host a symlink planted there resolves on the HOST.
func readClaudeAuthStamp(m sandbox.Mapping) (config.SandboxAuthMode, error) {
	root, err := os.OpenRoot(m.HostClaudeConfig())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	defer root.Close()

	body, err := root.ReadFile(claudeAuthStampName)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	// Through ResolveAuth so the stamp can only ever name a mode this build
	// acts on. A stamp written by a future version, or a truncated one, reads
	// as "" and takes the same path as no stamp at all.
	switch config.SandboxAuthMode(body) {
	case config.SandboxAuthToken:
		return config.SandboxAuthToken, nil
	case config.SandboxAuthBrowser:
		return config.SandboxAuthBrowser, nil
	}
	return "", nil
}

// writeClaudeAuthStamp records the mode for the next prepare.
func writeClaudeAuthStamp(m sandbox.Mapping, mode config.SandboxAuthMode) error {
	root, err := os.OpenRoot(m.HostClaudeConfig())
	if err != nil {
		return err
	}
	defer root.Close()
	// Written directly rather than through a temp+rename, for the reason the
	// seed gives: a half-written stamp reads as no stamp, which is the safe
	// side of this decision and self-repairs on the next prepare.
	return root.WriteFile(claudeAuthStampName, []byte(mode), 0o600)
}

// reconcileClaudeAuthMode repairs a Claude config directory whose sign-in mode
// has changed under it, then records the mode it ran under.
//
// ONLY token → browser repairs. The other direction gains a credential, so the
// answer already on disk is still correct, and repeating a sign-in the user has
// already done is a cost with nothing on the other side of it.
//
// A directory with NO stamp and an existing config is treated as token-era.
// That inference is the whole reason the stamp is not enough on its own: panes
// created before it existed have no record, and they are exactly the ones the
// default change stranded. It is imprecise in one direction — someone who had
// explicitly set `auth = "browser"` before the stamp existed, and signed in,
// gets asked to sign in once more. That is the better failure: an extra
// sign-in is visible and self-explanatory, while the state it replaces is a
// prompt that silently cannot authenticate.
//
// Never fails the spawn. Every error here is logged and swallowed: the repair
// restores a sign-in screen, and refusing to open the pane instead would be a
// worse outcome than the screen it is trying to bring back.
func reconcileClaudeAuthMode(m sandbox.Mapping, mode config.SandboxAuthMode) error {
	prev, err := readClaudeAuthStamp(m)
	if err != nil {
		log.Printf("sandbox: reading the sign-in stamp failed, assuming none: %v", err)
	}
	if prev == "" && claudeConfigExists(m) {
		// No stamp but a config: written by a build that had no stamp, whose
		// default was the token flow.
		prev = config.SandboxAuthToken
	}

	if prev == config.SandboxAuthToken && mode == config.SandboxAuthBrowser {
		if err := reopenClaudeOnboarding(m); err != nil {
			log.Printf("sandbox: could not reopen the Claude sign-in after the mode "+
				"changed to %s; the pane may need /login: %v", mode, err)
		}
	}

	if err := writeClaudeAuthStamp(m, mode); err != nil {
		log.Printf("sandbox: recording the sign-in mode failed; the next prepare will "+
			"have to infer it: %v", err)
	}
	return nil
}

// claudeConfigExists reports whether the directory already holds a Claude
// config, which is what makes a missing stamp mean "older build" rather than
// "first run".
func claudeConfigExists(m sandbox.Mapping) bool {
	root, err := os.OpenRoot(m.HostClaudeConfig())
	if err != nil {
		return false
	}
	defer root.Close()
	_, err = root.Stat(claudeConfigName)
	return err == nil
}

// reopenClaudeOnboarding removes the onboarding answer and NOTHING else.
//
// A wholesale rewrite is not an option: the file holds the user's theme, their
// per-project trust answers and their history, and under
// `shared_claude_config` it holds them for every sandbox pane at once. The
// answer is deleted rather than set false so the file reads exactly as one
// that was never onboarded.
//
// A config that does not decode is left untouched. Re-encoding what could not
// be read would replace the user's state with whatever survived the parse.
func reopenClaudeOnboarding(m sandbox.Mapping) error {
	root, err := os.OpenRoot(m.HostClaudeConfig())
	if err != nil {
		return err
	}
	defer root.Close()

	body, err := root.ReadFile(claudeConfigName)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	var cfg map[string]any
	if err := json.Unmarshal(body, &cfg); err != nil {
		return fmt.Errorf("claude config did not decode, leaving it alone: %w", err)
	}
	if _, ok := cfg[claudeOnboardingKey]; !ok {
		return nil
	}
	delete(cfg, claudeOnboardingKey)

	out, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("re-encode claude config: %w", err)
	}
	if err := root.WriteFile(claudeConfigName, out, 0o600); err != nil {
		return fmt.Errorf("write claude config: %w", err)
	}
	log.Printf("sandbox: reopened the Claude sign-in — this config directory was " +
		"prepared for a forwarded token and no longer receives one")
	return nil
}
