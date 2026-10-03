package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/artyomsv/quil/internal/claudetoken"
	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/userenv"
)

// oauthTokenVar is the credential Claude Code reads for headless
// authentication, and the one a sandbox pane's container is handed BY NAME.
// Spelled here as well as in internal/daemon because these are two binaries.
const oauthTokenVar = "CLAUDE_CODE_OAUTH_TOKEN"

func handleSandbox() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: quil sandbox [login|status]")
		os.Exit(1)
	}
	switch os.Args[2] {
	case "login":
		runSandboxLogin()
	case "status":
		runSandboxStatus()
	default:
		fmt.Fprintf(os.Stderr, "unknown sandbox command: %s\n", os.Args[2])
		os.Exit(1)
	}
}

// runSandboxLogin walks the whole token setup: no environment variable to set,
// no `setx`, no config edit, no daemon restart, and no token to copy by hand.
//
// The token is CAPTURED from `claude setup-token` rather than pasted back. A
// plain pipe cannot do it — measured: with stdout redirected the command emits
// nothing, opens the browser, and waits — so it runs under a PTY, mirrored to
// the user's terminal so the browser step is still visible and interactive.
// See internal/claudetoken. Quil reads the token, hands it to the OS, and never
// keeps a copy.
func runSandboxLogin() {
	if remoteMode() {
		fmt.Fprintf(os.Stderr, "quil sandbox login: not available with --remote (target: %s)\n"+
			"The token belongs in the environment of the daemon that runs the containers,\n"+
			"so run this on that host.\n", remoteDest)
		os.Exit(1)
	}

	claude, err := claudetoken.Find()
	if err != nil {
		fmt.Fprintln(os.Stderr, "quil sandbox login: `claude` is not on PATH.\n"+
			"Install Claude Code first — this command only drives its own token setup.")
		os.Exit(1)
	}

	fmt.Println("Running `claude setup-token`. Authorise in the browser it opens;")
	fmt.Println("Quil takes the token from here.")
	fmt.Println()

	token, err := claudetoken.Capture(claude, claudetoken.Options{Mirror: os.Stdout, Input: os.Stdin})
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nquil sandbox login: %v\n\n", err)
		// The fallback is named rather than attempted: if the capture failed
		// the flow may still have completed and printed a token that scrolled
		// past, and the user can place it themselves. Silently succeeding with
		// nothing stored is the outcome to avoid.
		fmt.Fprintf(os.Stderr, "If a token was printed above, set it yourself:\n"+
			"    setx %s \"<token>\"\n"+
			"then restart the daemon.\n", oauthTokenVar)
		os.Exit(1)
	}

	// The current process FIRST, so the daemon this run restarts inherits it
	// immediately. Windows builds a child's environment from its parent's, not
	// from the registry, so the persistent write below would otherwise not
	// reach the daemon until something re-read it.
	if err := os.Setenv(oauthTokenVar, token); err != nil {
		fmt.Fprintf(os.Stderr, "quil sandbox login: %v\n", err)
		os.Exit(1)
	}

	persisted := true
	switch err := userenv.Set(oauthTokenVar, token); {
	case err == nil:
		fmt.Printf("\nSaved %s to your user environment.\n", oauthTokenVar)
	case errors.Is(err, userenv.ErrUnsupported):
		persisted = false
		// Not a failure of this command — there is nowhere on this platform to
		// put it that the daemon would reliably read. Say exactly that, and
		// print the line to place.
		fmt.Printf("\nQuil cannot persist an environment variable on this platform.\n"+
			"Add this where the daemon's environment comes from:\n\n"+
			"    export %s='<the token printed above>'\n\n", oauthTokenVar)
	default:
		fmt.Fprintf(os.Stderr, "quil sandbox login: could not persist the token: %v\n", err)
		os.Exit(1)
	}

	// Only meaningful when a daemon is running AND the token can outlive this
	// process: restarting one that will re-read nothing changes nothing.
	restarted := persisted && restartDaemonForToken()

	// The closing line has to match what actually happened, because this is
	// the last thing the user reads before they believe the setup is done.
	//
	// On a platform with nowhere to persist a variable, the token exists only
	// in THIS process, which is about to exit. Saying "sandbox panes will use
	// this token" there is the confidently-wrong answer that sends the user
	// back to a container sign-in with no idea why — the same failure the
	// daemon's own "cannot find a token" log line exists to prevent.
	switch {
	case !persisted:
		fmt.Println("\nNot done yet. The token above is not stored anywhere:")
		fmt.Printf("export it as %s where the DAEMON runs, then restart it.\n", oauthTokenVar)
		fmt.Println("Until then sandbox panes will sign in inside the container.")
	case restarted:
		fmt.Println("\nDone. Sandbox panes will use this token — no sign-in inside the container.")
		fmt.Println("Quil keeps no copy of it.")
	default:
		fmt.Println("\nDone. The next daemon to start will use this token — no sign-in inside")
		fmt.Println("the container. Quil keeps no copy of it.")
	}
}

// restartDaemonForToken restarts a running local daemon so it inherits the
// token from this process.
//
// A running daemon has the environment it was started with, and nothing
// re-reads it — so without this the user does everything right, sees
// "Done", and the next sandbox pane still asks them to sign in. That silent
// gap is the whole reason this command exists.
// Reports whether it actually restarted one, so the closing message can say
// "sandbox panes will use this token" only when a daemon is holding it.
func restartDaemonForToken() bool {
	if daemonPID() == 0 {
		return false // nothing running; the next start inherits it anyway
	}
	fmt.Println("Restarting the daemon so it picks up the token…")
	restartDaemonCmd()
	return true
}

// runSandboxStatus answers the question the log line can only answer after the
// fact: is this set up, and if not, what exactly is missing.
func runSandboxStatus() {
	// A load failure is not fatal here: Load returns the defaults alongside the
	// error, and reporting the DEFAULT mode is the honest answer for a config
	// that could not be read — refusing to answer would hide the fact that a
	// sandbox pane would still spawn, under exactly those defaults.
	cfg, _ := config.Load(config.ConfigPath())
	mode, unrecognised := cfg.Sandbox.ResolveAuth()

	fmt.Printf("auth mode : %s", mode)
	switch {
	case unrecognised != "":
		fmt.Printf("   (config says %q, which is not a value Quil knows)\n", unrecognised)
	case cfg.Sandbox.Auth == "":
		fmt.Print("   (default)\n")
	default:
		fmt.Print("\n")
	}

	// The two are reported separately because they fail differently: a value
	// present here but not in the process environment means "persisted, but
	// this shell predates it", which is a restart rather than a re-run.
	inProcess := os.Getenv(oauthTokenVar) != ""
	fmt.Printf("token in this process : %s\n", yesNo(inProcess))

	persisted, err := userenv.Get(oauthTokenVar)
	switch {
	case errors.Is(err, userenv.ErrUnsupported):
		fmt.Println("token persisted       : (not readable on this platform)")
	case err != nil:
		fmt.Printf("token persisted       : error: %v\n", err)
	default:
		fmt.Printf("token persisted       : %s\n", yesNo(persisted != ""))
	}

	if mode == config.SandboxAuthBrowser {
		if cfg.Sandbox.SharedClaudeConfig {
			fmt.Println("\nSandbox panes default to Shared: you sign in inside a container once, and")
			fmt.Println("every Shared pane reuses it. The create dialog can still pick per pane.")
		} else {
			fmt.Println("\nSandbox panes will ask you to sign in inside the container, once per pane,")
			fmt.Println("unless you pick Shared in the create dialog (sign in once for all Shared panes).")
		}
		return
	}
	if !inProcess {
		fmt.Println("\nNo token in this process's environment, so a sandbox pane would fall back")
		fmt.Println("to signing in inside the container. Run:  quil sandbox login")
	}
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
