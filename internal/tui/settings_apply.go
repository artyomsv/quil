package tui

import (
	"reflect"

	"github.com/artyomsv/quil/internal/config"
)

// applySetting stores v through the shared check (config.Settings) and
// reports whether the stored value changed; only then is the config marked
// for the exit save. A refused value changes nothing, which is how every
// Settings row behaved before the table existed.
//
// A float row compares Get values, not raw fields: that keeps the dim-level
// rule, where accepting the "0.90" a hand-edited 1.5 displays must not
// rewrite config.toml. Every other row compares the stored config, because
// a Get can be lossy the other way — the sign-in row reads "token" for the
// forbidden auth = token + shared pair, and picking Token must repair it.
func (m *Model) applySetting(key, v string) bool {
	s, ok := config.LookupSetting(key)
	if !ok {
		return false
	}
	next := m.cfg
	if err := s.Set(&next, v); err != nil {
		return false
	}
	if s.Kind == config.SettingFloat {
		if s.Get(next) == s.Get(m.cfg) {
			return false
		}
	} else if reflect.DeepEqual(next, m.cfg) {
		return false
	}
	m.cfg = next
	m.configChanged = true
	return true
}
