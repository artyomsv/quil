package keymap

import (
	"sort"
	"strings"
)

// Keymap is a resolved, immutable key-to-action mapping.
type Keymap struct {
	// chords maps tier -> canonical chord -> action. Only single-chord
	// bindings land here; multi-step sequences live in seqs and are matched by
	// the prefix machine before either tier lookup runs, never as a bare chord.
	chords   map[Tier]map[string]ActionID
	bindings map[ActionID][]Sequence
	// seqs maps a full canonical sequence ("ctrl+b c") to its action.
	//
	// Two flat maps rather than a trie: with ~50 actions a tree earns nothing,
	// and the partial set below is consulted on EVERY keypress, bound or not,
	// so an O(1) map hit is the difference between the machine costing nothing
	// and costing something in the input path.
	seqs map[string]ActionID
	// partial maps every PROPER prefix of every sequence to one owning action.
	// A map rather than a set so a shadowing conflict can name the winner.
	partial map[string]ActionID
}

// Build resolves a spec map into a Keymap.
//
// It never fails. A malformed spec falls back to that action's shipped default
// and reports a ConflictMalformed; an unknown ID is ignored with a conflict.
// One bad line in config.toml must not cost the user their other 40 bindings.
func Build(specs map[ActionID]string) (*Keymap, []Conflict) {
	km := &Keymap{
		chords:   map[Tier]map[string]ActionID{TierEarly: {}, TierLate: {}},
		bindings: make(map[ActionID][]Sequence, len(specs)),
		seqs:     map[string]ActionID{},
		partial:  map[string]ActionID{},
	}
	var conflicts []Conflict

	// Resolve in registry Order so the duplicate tie-break reproduces today's
	// case order. Map iteration is randomized and an ID sort flips real pairs.
	resolved := make([]Action, 0, len(specs))
	var unknown []ActionID
	for id := range specs {
		a, ok := Lookup(id)
		if !ok {
			unknown = append(unknown, id)
			continue
		}
		resolved = append(resolved, a)
	}
	sort.Slice(resolved, func(i, j int) bool { return resolved[i].Order < resolved[j].Order })

	// Unknown IDs have no Order to sort by — an ID sort is the deterministic
	// fallback. Map iteration is randomized, and this slice feeds the same F1
	// renderer detectShadowing's sort exists for.
	sort.Slice(unknown, func(i, j int) bool { return unknown[i] < unknown[j] })
	for _, id := range unknown {
		// Key carries the SPEC, so the message can name the chord the user
		// wrote next to the ID they misspelled.
		conflicts = append(conflicts, Conflict{Kind: ConflictUnknownAction, Key: specs[id], Loser: id})
	}

	for _, a := range resolved {
		seqs, err := ParseSpec(specs[a.ID])
		if err != nil {
			seqs, _ = ParseSpec(a.Default) // validated by TestActions_RegistryIntegrity
			conflicts = append(conflicts, Conflict{
				Kind: ConflictMalformed, Key: a.Default, Loser: a.ID, Detail: err.Error(),
			})
		}
		if len(seqs) == 0 {
			continue
		}
		km.bindings[a.ID] = seqs
		for _, seq := range seqs {
			if len(seq) > 1 {
				// Multi-step: the prefix machine owns these. They never enter
				// the tier chord maps, so a sequence head cannot be dispatched
				// as a bare chord.
				full := seq.String()
				if prev, taken := km.seqs[full]; taken {
					conflicts = append(conflicts, Conflict{
						Kind: ConflictDuplicate, Key: full, Winner: prev, Loser: a.ID,
					})
					continue // lower Order already claimed it: legacy case order
				}
				km.seqs[full] = a.ID
				// Record every PROPER prefix. First writer wins, so the owner
				// recorded here follows registry Order like every other
				// tie-break in this function.
				for i := 1; i < len(seq); i++ {
					head := seq[:i].String()
					if _, seen := km.partial[head]; !seen {
						km.partial[head] = a.ID
					}
				}
				continue
			}
			key := seq[0].String()
			// Lazy tier map rather than a fixed seed: km.chords was built with
			// entries for exactly TierEarly and TierLate, so adding a third
			// tier would panic on a nil-map write before the TUI drew a frame.
			// A test pinned that; nothing in the code did.
			if km.chords[a.Tier] == nil {
				km.chords[a.Tier] = map[string]ActionID{}
			}
			if prev, taken := km.chords[a.Tier][key]; taken {
				conflicts = append(conflicts, Conflict{
					Kind: ConflictDuplicate, Key: key, Winner: prev, Loser: a.ID,
				})
				continue // lower Order already claimed it: legacy case order
			}
			km.chords[a.Tier][key] = a.ID
		}
	}
	conflicts = append(conflicts, km.detectShadowing()...)
	return km, conflicts
}

// detectShadowing reports chords claimed in both tiers, and chords colliding
// with Quil's hardcoded keys.
func (k *Keymap) detectShadowing() []Conflict {
	var out []Conflict
	for key, lateID := range k.chords[TierLate] {
		if earlyID, ok := k.chords[TierEarly][key]; ok {
			out = append(out, Conflict{Kind: ConflictCrossTier, Key: key, Winner: earlyID, Loser: lateID})
		}
	}
	for _, tier := range []Tier{TierEarly, TierLate} {
		for key, id := range k.chords[tier] {
			if _, ok := hardcodedKeys[key]; ok {
				out = append(out, Conflict{Kind: ConflictHardcoded, Key: key, Loser: id})
			}
		}
	}
	// Sequence shadowing. A chord or sequence that is a proper prefix of a
	// longer sequence can never fire: the probe reports Partial and swallows
	// the key every time. REFUSE the shorter one — leaving it in the tables
	// would advertise a binding dispatch can never reach, which is the failure
	// mode every other conflict in this file exists to prevent.
	//
	// Deleting the current key while ranging a map is defined behaviour in Go.
	for _, tier := range []Tier{TierEarly, TierLate} {
		for key, id := range k.chords[tier] {
			if winner, ok := k.partial[key]; ok {
				out = append(out, Conflict{Kind: ConflictShadowed, Key: key, Winner: winner, Loser: id})
				delete(k.chords[tier], key)
			}
		}
	}
	for full, id := range k.seqs {
		if winner, ok := k.partial[full]; ok {
			out = append(out, Conflict{Kind: ConflictShadowed, Key: full, Winner: winner, Loser: id})
			delete(k.seqs, full)
		}
	}
	// Deterministic order: these render in F1, and a list that reshuffles
	// between launches is unreadable.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Key != out[j].Key {
			return out[i].Key < out[j].Key
		}
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Loser < out[j].Loser
	})
	return out
}

// MatchTier resolves a raw key string against one tier. The key is
// canonicalized here, so callers pass tea.KeyPressMsg.String() directly.
//
// Nil-safe: TUI tests build Model literals with no keymap and drive handleKey.
func (k *Keymap) MatchTier(t Tier, key string) (ActionID, bool) {
	if k == nil {
		return "", false
	}
	c, err := ParseChord(key)
	if err != nil {
		return "", false
	}
	id, ok := k.chords[t][c.String()]
	return id, ok
}

// Bindings returns the sequences bound to an action, or nil.
//
// Returns the unflattened Sequence — how many chords, and which — where the
// display readers want a string (Display) or the canonical chords (Keys).
// Dispatch itself goes through MatchTier for chords and MatchSeq for
// sequences; this is for callers that need to inspect a binding's shape.
func (k *Keymap) Bindings(id ActionID) []Sequence {
	if k == nil {
		return nil
	}
	return k.bindings[id]
}

// Keys returns each binding as a canonical string, for callers that need the
// individual keys rather than one display line.
func (k *Keymap) Keys(id ActionID) []string {
	if k == nil {
		return nil
	}
	seqs := k.bindings[id]
	if len(seqs) == 0 {
		return nil
	}
	out := make([]string, len(seqs))
	for i, s := range seqs {
		out[i] = s.String()
	}
	return out
}

// Display renders an action's bindings for help text, joined with " / " —
// the format kbDisplay produced before the registry existed.
func (k *Keymap) Display(id ActionID) string {
	return strings.Join(k.Keys(id), " / ")
}
