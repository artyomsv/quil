package keymap

import "sort"

// Resolve merges binding layers, lowest priority first.
//
// Semantics are REPLACE, not union: per action, a higher layer replaces the
// lower entirely. Absence inherits, presence replaces, and "" is a value — an
// explicit unbind — rather than a deletion that propagates upward.
//
// That last distinction is what lets a user reclaim an action a preset
// unbound. Treating "" as a removal would make a preset able to veto an
// explicit user override, inverting the layering model.
//
// Pure: no I/O, no paths, no config types. Reading the layers off disk belongs
// to internal/config, which already owns every QuilDir()-derived path.
func Resolve(layers ...map[ActionID]string) map[ActionID]string {
	merged, _ := resolveWithOrigin(layers...)
	return merged
}

// resolveWithOrigin is Resolve plus the index of the layer each surviving spec
// came from. BuildLayered needs it: "which layer claimed this" is the whole
// input to the cross-layer shadowing rule, and it is unrecoverable once the
// layers are flattened.
func resolveWithOrigin(layers ...map[ActionID]string) (map[ActionID]string, map[ActionID]int) {
	merged := map[ActionID]string{}
	origin := map[ActionID]int{}
	for i, layer := range layers {
		for id, spec := range layer {
			merged[id] = spec
			origin[id] = i
		}
	}
	return merged, origin
}

// BuildLayered resolves layers and builds a Keymap, applying the cross-layer
// shadowing rule that Build cannot see: Build receives one flat map, by which
// point the layer a binding came from is gone.
//
// Cross-layer, the HIGHER layer wins regardless of length. Within one layer the
// SHORTER binding is refused. The asymmetry is deliberate — a user override
// `pane.rename = "ctrl+b"` under the tmux preset must be able to reclaim the
// prefix key as a plain chord, and a length rule would let a preset veto an
// explicit user override. Length is safe within a layer precisely because no
// cross-layer inversion is possible there.
func BuildLayered(layers ...map[ActionID]string) (*Keymap, []Conflict) {
	merged, origin := resolveWithOrigin(layers...)
	return buildWithOrigin(merged, origin)
}

// layerOf reports which layer claimed an action's spec. A nil origin means a
// single flat map, so everything ties at 0.
func layerOf(origin map[ActionID]int, id ActionID) int {
	if origin == nil {
		return 0
	}
	return origin[id]
}

// isProperPrefix reports whether short is a strict prefix of long.
func isProperPrefix(short, long Sequence) bool {
	if len(short) == 0 || len(short) >= len(long) {
		return false
	}
	for i := range short {
		if short[i] != long[i] {
			return false
		}
	}
	return true
}

// altRef points at one alternative of one parsed binding.
type altRef struct{ bi, si int }

// resolveShadowing drops every alternative that a longer sequence makes
// unreachable, mutating parsed in place and returning one conflict per drop.
//
// A binding whose sequence is a strict prefix of another can never fire: the
// prefix machine reports Partial and swallows the key every time. So one of the
// pair has to go, and which one is the entire subtlety:
//
//   - different layers — the HIGHER layer wins, whichever is shorter. This is
//     what lets a user override reclaim a chord the selected preset uses as a
//     sequence head.
//   - same layer — the SHORTER is refused. Both sides tie on layer, so length
//     is the only unambiguous tie-break left, and no cross-layer inversion is
//     possible to get wrong.
//
// Only genuine prefix relationships are considered. Two bindings claiming the
// SAME sequence are an ordinary duplicate, resolved by registry Order during
// insertion — reporting those here would turn every routine rebind into a
// spurious "shadowed by a longer sequence" row.
func resolveShadowing(parsed []parsedBinding, origin map[ActionID]int) []Conflict {
	type entry struct {
		ref altRef
		seq Sequence
	}
	var all []entry
	for bi := range parsed {
		for si, seq := range parsed[bi].seqs {
			all = append(all, entry{ref: altRef{bi, si}, seq: seq})
		}
	}

	dropped := map[altRef]bool{}
	var out []Conflict
	for i := range all {
		for j := range all {
			short, long := all[i], all[j]
			if short.ref.bi == long.ref.bi {
				continue // same action: its own alternatives cannot shadow it
			}
			if !isProperPrefix(short.seq, long.seq) {
				continue
			}
			shortID := parsed[short.ref.bi].action.ID
			longID := parsed[long.ref.bi].action.ID

			loser, winnerID := short, longID
			if layerOf(origin, shortID) > layerOf(origin, longID) {
				loser, winnerID = long, shortID
			}
			if dropped[loser.ref] {
				continue // already lost to something else; one row is enough
			}
			dropped[loser.ref] = true
			out = append(out, Conflict{
				Kind:   ConflictShadowed,
				Key:    loser.seq.String(),
				Winner: winnerID,
				Loser:  parsed[loser.ref.bi].action.ID,
			})
		}
	}

	if len(dropped) == 0 {
		return nil
	}
	for bi := range parsed {
		kept := parsed[bi].seqs[:0]
		for si, seq := range parsed[bi].seqs {
			if !dropped[altRef{bi, si}] {
				kept = append(kept, seq)
			}
		}
		parsed[bi].seqs = kept
	}

	// Deterministic order: these render in F1, and a list that reshuffles
	// between launches is unreadable.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Key != out[j].Key {
			return out[i].Key < out[j].Key
		}
		return out[i].Loser < out[j].Loser
	})
	return out
}
