package notify

import "testing"

// The dev build writes machine-global artifacts that QUIL_HOME cannot
// redirect — a Start Menu shortcut and an HKCU class key. Namespacing them is
// the only thing keeping a dev build from overwriting production's
// registration and pointing quil:// at the dev binary, which
// .claude/rules/dev-environment.md forbids.
func TestVariant_DevAndProdAreDisjoint(t *testing.T) {
	t.Parallel()
	prod := Variant(false)
	dev := Variant(true)

	if prod.AUMID != "artyomsv.quil" {
		t.Errorf("prod AUMID = %q, want artyomsv.quil", prod.AUMID)
	}
	if prod.Scheme != "quil" {
		t.Errorf("prod Scheme = %q, want quil", prod.Scheme)
	}
	if dev.AUMID != "artyomsv.quil.dev" {
		t.Errorf("dev AUMID = %q, want artyomsv.quil.dev", dev.AUMID)
	}
	if dev.Scheme != "quil-dev" {
		t.Errorf("dev Scheme = %q, want quil-dev", dev.Scheme)
	}
	if prod.AUMID == dev.AUMID || prod.Scheme == dev.Scheme {
		t.Fatal("dev and prod variants must not share an AUMID or a scheme")
	}
}

// The shortcut filename is derived from the AUMID, so the two variants cannot
// collide on disk even if someone edits one of the identifiers.
func TestShortcutBaseName_DiffersPerVariant(t *testing.T) {
	t.Parallel()
	if ShortcutBaseName(Variant(false)) == ShortcutBaseName(Variant(true)) {
		t.Fatal("dev and prod shortcuts share a filename — a dev setup would overwrite production")
	}
	if got := ShortcutBaseName(Variant(false)); got != "Quil.lnk" {
		t.Errorf("prod shortcut = %q, want Quil.lnk", got)
	}
	if got := ShortcutBaseName(Variant(true)); got != "Quil (dev).lnk" {
		t.Errorf("dev shortcut = %q, want \"Quil (dev).lnk\"", got)
	}
}
