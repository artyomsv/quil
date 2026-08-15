package keymap

import (
	"fmt"
	"strings"
)

// Sequence is one binding: a chord, or several chords pressed in order.
type Sequence []Chord

// String renders the sequence space-separated, each chord canonical.
func (s Sequence) String() string {
	parts := make([]string, len(s))
	for i, c := range s {
		parts[i] = c.String()
	}
	return strings.Join(parts, " ")
}

// ParseSpec parses a binding spec into its alternatives.
//
//	"ctrl+b c, ctrl+t"  ->  [seq(ctrl+b, c), seq(ctrl+t)]
//
// An empty or whitespace-only spec means deliberately unbound and returns
// (nil, nil) — next_pane and prev_pane ship that way.
func ParseSpec(spec string) ([]Sequence, error) {
	if strings.TrimSpace(spec) == "" {
		return nil, nil
	}
	alts := strings.Split(spec, ",")
	out := make([]Sequence, 0, len(alts))
	for _, alt := range alts {
		// Space AROUND an alternative is cosmetic; space INSIDE separates
		// sequence steps. An internal empty step (double space) is malformed
		// and is caught by the loop below.
		alt = strings.Trim(alt, " \t")
		if alt == "" {
			return nil, fmt.Errorf("spec %q has an empty alternative", spec)
		}
		steps := strings.Split(alt, " ")
		seq := make(Sequence, 0, len(steps))
		for _, step := range steps {
			if step == "" {
				return nil, fmt.Errorf("spec %q has an empty sequence step", spec)
			}
			c, err := ParseChord(step)
			if err != nil {
				return nil, fmt.Errorf("spec %q: %w", spec, err)
			}
			seq = append(seq, c)
		}
		out = append(out, seq)
	}
	return out, nil
}
