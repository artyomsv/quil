package keymap

import "fmt"

// ConflictKind classifies a binding problem found at build time.
type ConflictKind uint8

const (
	// ConflictDuplicate: two actions claim one chord in one tier.
	ConflictDuplicate ConflictKind = iota
	// ConflictCrossTier: one chord is claimed in both tiers. The early action
	// always wins at runtime, so the late one can never fire.
	ConflictCrossTier
	// ConflictHardcoded: the chord collides with a key handleKey intercepts
	// outside the registry. Which side wins is NOT uniform — it depends on
	// where that interception sits relative to the two tier lookups. See
	// hardcodedKeys.
	ConflictHardcoded
	// ConflictMalformed: the spec did not parse; the action fell back to its
	// shipped default.
	ConflictMalformed
	// ConflictUnknownAction: a spec named an ID that is not registered.
	ConflictUnknownAction
)

func (k ConflictKind) String() string {
	switch k {
	case ConflictDuplicate:
		return "duplicate binding"
	case ConflictCrossTier:
		return "cross-tier shadowing"
	case ConflictHardcoded:
		return "collides with a built-in key"
	case ConflictMalformed:
		return "unreadable binding"
	case ConflictUnknownAction:
		return "unknown action"
	}
	return "unknown conflict"
}

// Conflict is one binding problem. Conflicts never block startup; they are
// surfaced in F1 -> Shortcuts and the log, because a silently dropped binding
// is worse than a loud one.
type Conflict struct {
	Kind ConflictKind
	Key  string
	// Winner and Loser are the two actions claiming Key. ConflictHardcoded is
	// the exception: it collides with built-in behaviour rather than with
	// another action, so only one action is involved and it is carried in
	// Loser whichever side actually wins — String derives the direction from
	// the key itself (see hardcodedString).
	Winner ActionID
	Loser  ActionID
	Detail string
}

// String is both the log line and the F1 -> Shortcuts row, and that row is
// width-bounded — so the key, the winner and the loser come first, in that
// order, and the consequence clause last. Truncation then eats the half a
// reader can infer before the half they cannot.
func (c Conflict) String() string {
	switch c.Kind {
	case ConflictHardcoded:
		return c.hardcodedString()
	case ConflictMalformed:
		// Fallback BEFORE the parser's complaint: Detail quotes the spec twice
		// (ParseSpec wraps ParseChord, and for a one-chord spec those are the
		// same string), so it is both the longest clause and the one a reader
		// can reconstruct from their own config.toml. Truncation eats it first.
		return fmt.Sprintf("%s: %s → default %q; %s", c.Kind, c.Loser, c.Key, c.Detail)
	case ConflictUnknownAction:
		return fmt.Sprintf("%s: %s is not a known action; ignored", c.Kind, c.Loser)
	}
	return fmt.Sprintf("%s: %q → %s wins, %s never fires", c.Kind, c.Key, c.Winner, c.Loser)
}

// hardcodedString names the side that actually wins.
//
// The direction is DERIVED — from where handleKey checks the key, and for the
// mid-dispatch ones from the losing action's own tier — rather than decided at
// detection time, because those are the two facts dispatch itself turns on. A
// direction stored on the Conflict is a second copy of that logic, and the
// shipped one was a copy that had drifted: it told every one of the 21 keys
// "Quil intercepts it first", while 13 of them are checked only once BOTH tier
// switches have declined, where the bound action wins and the built-in dies.
func (c Conflict) hardcodedString() string {
	hk, ok := hardcodedKeys[c.Key]
	if !ok {
		// A Conflict assembled outside detectShadowing. Say what is certain
		// and claim no winner — a wrong direction is what this exists to fix.
		return fmt.Sprintf("%s: %q is bound to %s", c.Kind, c.Key, c.Loser)
	}
	if hk.pos == afterBothTiers || tierOf(c.Loser) == TierEarly {
		// "is lost", not "never fires": what stops happening here is Quil's
		// own behaviour, and the label is not an action ID. The kind label
		// has already said the other side is built-in, so it is not repeated
		// — those nine cells are the difference between this line landing
		// whole in the dialog and losing its last clause.
		return fmt.Sprintf("%s: %q → %s wins, %s is lost", c.Kind, c.Key, c.Loser, hk.label)
	}
	return fmt.Sprintf("%s: %q → %s wins, %s never fires", c.Kind, c.Key, hk.label, c.Loser)
}

// tierOf reports an action's dispatch tier. An ID that is not registered
// cannot reach the chord map, so the fallback is unreachable from Build; it
// answers TierLate because "the built-in wins" is the claim that costs a user
// nothing if it is wrong — they try the key and it does the built-in thing.
func tierOf(id ActionID) Tier {
	if a, ok := Lookup(id); ok {
		return a.Tier
	}
	return TierLate
}

// hardcodedPos is WHERE handleKey checks a built-in key, relative to the two
// registry lookups. It is what decides who wins the chord, and therefore the
// entire content of a ConflictHardcoded message.
type hardcodedPos uint8

const (
	// afterBothTiers: checked only once BOTH tier switches have declined the
	// key. A bound action of either tier wins and the built-in behaviour is
	// what becomes unreachable. TestHandleKey_PasteAliasesLoseToLateActions
	// (internal/tui) pins this for the paste aliases.
	afterBothTiers hardcodedPos = iota
	// betweenTiers: checked after the early switch and before the late one,
	// so an early action wins and a late one never fires.
	betweenTiers
)

// hardcodedKey is one such key: its position, and a short name for what the
// built-in does, so a message can say which behaviour is at stake. The labels
// are kept short deliberately — they render inside a bounded dialog row.
type hardcodedKey struct {
	pos   hardcodedPos
	label string
}

// hardcodedKeys are intercepted by handleKey outside the registry. See
// internal/tui/model.go, handleKey (the paste aliases and the reserved-key
// switch, both after the late-tier lookup) and isSelectionExtendKey (between
// the two lookups).
var hardcodedKeys = func() map[string]hardcodedKey {
	m := map[string]hardcodedKey{
		"f1":         {afterBothTiers, "help"},
		"ctrl+n":     {afterBothTiers, "new pane"},
		"ctrl+alt+v": {afterBothTiers, "paste"},
		"f8":         {afterBothTiers, "paste"},
	}
	for _, d := range []string{"1", "2", "3", "4", "5", "6", "7", "8", "9"} {
		m["alt+"+d] = hardcodedKey{afterBothTiers, "tab " + d}
	}
	for _, dir := range []string{"left", "right", "up", "down"} {
		m["shift+"+dir] = hardcodedKey{betweenTiers, "text selection"}
	}
	for _, dir := range []string{"left", "right"} {
		m["ctrl+shift+"+dir] = hardcodedKey{betweenTiers, "text selection"}
		m["ctrl+alt+shift+"+dir] = hardcodedKey{betweenTiers, "text selection"}
	}
	return m
}()
