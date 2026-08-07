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
	// outside the registry.
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
	Kind   ConflictKind
	Key    string
	Winner ActionID
	Loser  ActionID
	Detail string
}

func (c Conflict) String() string {
	switch c.Kind {
	case ConflictHardcoded:
		return fmt.Sprintf("%s: %q is bound to %s but Quil intercepts it first", c.Kind, c.Key, c.Loser)
	case ConflictMalformed:
		return fmt.Sprintf("%s: %s — %s; using the default %q", c.Kind, c.Loser, c.Detail, c.Key)
	case ConflictUnknownAction:
		return fmt.Sprintf("%s: %s is not a known action; ignored", c.Kind, c.Loser)
	}
	return fmt.Sprintf("%s: %q resolves to %s, so %s will never fire", c.Kind, c.Key, c.Winner, c.Loser)
}

// hardcodedKeys are intercepted by handleKey outside the registry:
// f1 and ctrl+n, alt+1..alt+9 tab switching, and the ctrl+alt+v / f8 paste
// aliases. See internal/tui/model.go, handleKey.
var hardcodedKeys = func() map[string]bool {
	m := map[string]bool{"f1": true, "ctrl+n": true, "ctrl+alt+v": true, "f8": true}
	for _, d := range []string{"1", "2", "3", "4", "5", "6", "7", "8", "9"} {
		m["alt+"+d] = true
	}
	return m
}()
