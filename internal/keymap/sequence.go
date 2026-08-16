package keymap

// MatchKind is the result of probing a pending chord sequence.
type MatchKind uint8

const (
	// MatchNone: the chords match no binding, and extend none.
	MatchNone MatchKind = iota
	// MatchPartial: the chords are a proper prefix of at least one binding.
	// The caller swallows the key and keeps the sequence pending.
	MatchPartial
	// MatchExact: the chords are a complete binding. The caller runs it.
	MatchExact
)

func (k MatchKind) String() string {
	switch k {
	case MatchNone:
		return "none"
	case MatchPartial:
		return "partial"
	case MatchExact:
		return "exact"
	}
	return "unknown"
}

// MatchSeq resolves a pending chord sequence.
//
// Deliberately TIER-AGNOSTIC, and that is the whole subtlety of the feature.
// pane.close is a late-tier action; bind it to "ctrl+b x" and the opening chord
// is in neither tier's chord map. A tier-scoped probe answers MatchNone, the
// key falls through to tryPluginRawKey or to the PTY, and no amount of pressing
// x can ever complete the sequence. The tier split governs Exact resolution of
// SINGLE CHORDS only; partial detection is global.
//
// Exact is checked before Partial so a binding that is both a complete
// sequence and the head of a longer one resolves rather than hanging — that
// state is refused at build time (ConflictShadowed), so this ordering is a
// belt-and-braces guarantee rather than the primary defence.
//
// Nil-safe: TUI tests build Model literals with no keymap and drive handleKey.
func (k *Keymap) MatchSeq(pending []Chord) (ActionID, MatchKind) {
	if k == nil || len(pending) == 0 {
		return "", MatchNone
	}
	key := Sequence(pending).String()
	if id, ok := k.seqs[key]; ok {
		return id, MatchExact
	}
	// A single chord is dispatched by the tier lookups, not here — but MatchSeq
	// is the sequence machine's only view of the keymap, so it must still
	// report a length-1 binding as Exact when asked. Both tiers are consulted
	// because the machine runs before the tier split and has no tier of its own.
	if len(pending) == 1 {
		for _, tier := range []Tier{TierEarly, TierLate} {
			if id, ok := k.chords[tier][key]; ok {
				return id, MatchExact
			}
		}
	}
	if id, ok := k.partial[key]; ok {
		return id, MatchPartial
	}
	return "", MatchNone
}
