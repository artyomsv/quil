package main

import (
	"reflect"
	"testing"
)

func TestParseRemoteFlag(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantDest string
		wantRest []string
		wantErr  bool
	}{
		{
			name:     "absent leaves args untouched",
			args:     []string{"quil"},
			wantDest: "",
			wantRest: []string{"quil"},
		},
		{
			name:     "separate value",
			args:     []string{"quil", "--remote", "gpu01"},
			wantDest: "gpu01",
			wantRest: []string{"quil"},
		},
		{
			name:     "equals form",
			args:     []string{"quil", "--remote=gpu01"},
			wantDest: "gpu01",
			wantRest: []string{"quil"},
		},
		{
			name:     "user@host is passed through verbatim",
			args:     []string{"quil", "--remote", "user@gpu01"},
			wantDest: "user@gpu01",
			wantRest: []string{"quil"},
		},
		{
			name:     "other args survive",
			args:     []string{"quil", "--remote", "gpu01", "status"},
			wantDest: "gpu01",
			wantRest: []string{"quil", "status"},
		},
		{
			name:    "missing value is an error",
			args:    []string{"quil", "--remote"},
			wantErr: true,
		},
		{
			name:    "empty value is an error",
			args:    []string{"quil", "--remote="},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dest, rest, err := parseRemoteFlag(tt.args)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if dest != tt.wantDest {
				t.Errorf("dest = %q, want %q", dest, tt.wantDest)
			}
			if !reflect.DeepEqual(rest, tt.wantRest) {
				t.Errorf("rest = %v, want %v", rest, tt.wantRest)
			}
		})
	}
}
