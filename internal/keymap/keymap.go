package keymap

import (
	"sort"
	"strings"
)

// Keymap is a resolved, immutable key-to-action mapping.
type Keymap struct {
	// chords maps tier -> canonical chord -> action. Only single-chord
	// bindings land here; multi-step sequences live in bindings and are
	// matched by the prefix machine (Stage 2), never as a bare chord.
	chords   map[Tier]map[string]ActionID
	bindings map[ActionID][]Sequence
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
		conflicts = append(conflicts, Conflict{Kind: ConflictUnknownAction, Loser: id})
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
			if len(seq) != 1 {
				continue // multi-step: Stage 2 owns these
			}
			key := seq[0].String()
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
