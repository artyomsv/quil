package tui

import "github.com/artyomsv/quil/internal/config"

// applySetting stores v through the shared check (config.Settings) and
// reports whether the VISIBLE value changed; only then is the config marked
// for the exit save. A refused value changes nothing, which is how every
// Settings row behaved before the table existed. Comparing Get values, not
// raw fields, keeps the dim-level rule: accepting the "0.90" a hand-edited
// 1.5 displays must not rewrite config.toml.
func (m *Model) applySetting(key, v string) bool {
	s, ok := config.LookupSetting(key)
	if !ok {
		return false
	}
	next := m.cfg
	if err := s.Set(&next, v); err != nil {
		return false
	}
	if s.Get(next) == s.Get(m.cfg) {
		return false
	}
	m.cfg = next
	m.configChanged = true
	return true
}
