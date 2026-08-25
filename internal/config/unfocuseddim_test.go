package config

import "testing"

func TestUnfocusedDimAmount_ClampsOutOfRangeValues(t *testing.T) {
	tests := []struct {
		name string
		set  float64
		want float64
	}{
		{"zero disables", 0, 0},
		{"negative is off, never a brightening blend", -0.5, 0},
		{"an ordinary value passes through", 0.45, 0.45},
		{"the maximum passes through", MaxUnfocusedDim, MaxUnfocusedDim},
		{"a full blend is clamped short of invisible", 1.0, MaxUnfocusedDim},
		{"a percentage-shaped typo is clamped, not honoured", 45, MaxUnfocusedDim},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := UIConfig{UnfocusedDim: tt.set}
			if got := u.UnfocusedDimAmount(); got != tt.want {
				t.Errorf("UIConfig{UnfocusedDim: %v}.UnfocusedDimAmount() = %v, want %v", tt.set, got, tt.want)
			}
		})
	}
}

func TestDefault_EnablesUnfocusedDim(t *testing.T) {
	// The feature exists to be noticed without being configured. A default of
	// 0 would ship it switched off, which for a visual affordance means
	// shipping it to nobody.
	got := Default().UI.UnfocusedDimAmount()
	if got != DefaultUnfocusedDim {
		t.Errorf("Default().UI.UnfocusedDimAmount() = %v, want %v", got, DefaultUnfocusedDim)
	}
	if got <= 0 {
		t.Error("the shipped default must actually dim")
	}
}
