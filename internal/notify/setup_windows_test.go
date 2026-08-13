//go:build windows

package notify

import (
	"os"
	"strings"
	"testing"

	"golang.org/x/sys/windows/registry"
)

// Runs only on a real Windows host. Build with:
//
//	docker run --rm -v "$PWD":/src -w /src -e GOOS=windows -e GOARCH=amd64 \
//	  golang:1.25 go test -c -o /src/notify_win_test.exe ./internal/notify/
//
// then run ./notify_win_test.exe -test.v on the host.

// scratchVariant keeps every test off the real prod and dev registrations on
// the developer's own machine.
func scratchVariant() Options {
	return Options{AUMID: "artyomsv.quil.selftest", Scheme: "quil-selftest"}
}

func TestSetupRemove_RoundTrip(t *testing.T) {
	opts := scratchVariant()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() = %v", err)
	}
	t.Cleanup(func() { _, _ = Remove(opts) })

	written, err := Setup(opts, exe)
	if err != nil {
		t.Fatalf("Setup() = %v", err)
	}
	if len(written) < 2 {
		t.Errorf("Setup reported %v, want both the shortcut and the registry key", written)
	}

	if ok, err := Registered(opts); err != nil || !ok {
		t.Fatalf("Registered() = %v, %v after Setup", ok, err)
	}

	k, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Classes\`+opts.Scheme+`\shell\open\command`, registry.QUERY_VALUE)
	if err != nil {
		t.Fatalf("protocol command key missing: %v", err)
	}
	cmd, _, err := k.GetStringValue("")
	k.Close()
	if err != nil {
		t.Fatalf("reading command value: %v", err)
	}
	if !strings.Contains(cmd, exe) || !strings.Contains(cmd, "%1") {
		t.Errorf("command = %q, want it to invoke %q with %%1", cmd, exe)
	}
	// The ceiling the security model depends on: a click can reach `activate`
	// and nothing else.
	if !strings.Contains(cmd, "activate") {
		t.Errorf("command = %q, want it to invoke the activate subcommand", cmd)
	}

	// Remove must be a TRUE inverse. A setup command the user cannot undo is
	// one they should not have been asked to run.
	if _, err := Remove(opts); err != nil {
		t.Fatalf("Remove() = %v", err)
	}
	if ok, _ := Registered(opts); ok {
		t.Error("Registered() still true after Remove")
	}
	if _, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Classes\`+opts.Scheme, registry.QUERY_VALUE); err == nil {
		t.Error("protocol key survived Remove")
	}
	sc, _ := ShortcutPath(opts)
	if _, err := os.Stat(sc); err == nil {
		t.Errorf("shortcut %s survived Remove", sc)
	}
}

func TestRemove_IsIdempotent(t *testing.T) {
	if _, err := Remove(scratchVariant()); err != nil {
		t.Errorf("Remove() on an unregistered variant = %v, want nil", err)
	}
}

// Half a registration is not success: a shortcut with no protocol key yields a
// toast that cannot be clicked.
func TestRegistered_FalseWithoutProtocolKey(t *testing.T) {
	opts := scratchVariant()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() = %v", err)
	}
	t.Cleanup(func() { _, _ = Remove(opts) })

	if _, err := Setup(opts, exe); err != nil {
		t.Fatalf("Setup() = %v", err)
	}
	for _, sub := range []string{`\shell\open\command`, `\shell\open`, `\shell`, ``} {
		_ = registry.DeleteKey(registry.CURRENT_USER, `Software\Classes\`+opts.Scheme+sub)
	}
	if ok, _ := Registered(opts); ok {
		t.Error("Registered() = true with the shortcut present but no protocol key")
	}
}

func TestShortcutPath_DevAndProdDiffer(t *testing.T) {
	prod, err := ShortcutPath(Variant(false))
	if err != nil {
		t.Fatalf("ShortcutPath(prod) = %v", err)
	}
	dev, err := ShortcutPath(Variant(true))
	if err != nil {
		t.Fatalf("ShortcutPath(dev) = %v", err)
	}
	if prod == dev {
		t.Fatalf("dev and prod share a shortcut path %q — a dev setup would overwrite production", prod)
	}
}
