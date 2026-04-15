package model

import (
	"testing"
)

func TestFamily(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", ""},
		{"sonnet", "claude"},
		{"opus", "claude"},
		{"haiku", "claude"},
		{"claude-3-5-sonnet-20241022", "claude"},
		{"gpt-5.4", "codex"},
		{"gpt-5.4-mini", "codex"},
		{"o3", "codex"},
	}
	for _, tc := range tests {
		tc := tc
		t.Run("Family_"+tc.input, func(t *testing.T) {
			got := Family(tc.input)
			if got != tc.want {
				t.Errorf("Family(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestDefaultGateModel(t *testing.T) {
	tests := []struct {
		backend string
		want    string
	}{
		{"claude", "sonnet"},
		{"codex", "gpt-5.4-mini"},
		{"unknown", ""},
	}
	for _, tc := range tests {
		tc := tc
		t.Run("DefaultGateModel_"+tc.backend, func(t *testing.T) {
			got := DefaultGateModel(tc.backend)
			if got != tc.want {
				t.Errorf("DefaultGateModel(%q) = %q, want %q", tc.backend, got, tc.want)
			}
		})
	}
}

func TestDefaultFixModel(t *testing.T) {
	tests := []struct {
		backend string
		want    string
	}{
		{"claude", "sonnet"},
		{"codex", "gpt-5.4"},
		{"unknown", ""},
	}
	for _, tc := range tests {
		tc := tc
		t.Run("DefaultFixModel_"+tc.backend, func(t *testing.T) {
			got := DefaultFixModel(tc.backend)
			if got != tc.want {
				t.Errorf("DefaultFixModel(%q) = %q, want %q", tc.backend, got, tc.want)
			}
		})
	}
}

func TestUsingBackendDefaults(t *testing.T) {
	tests := []struct {
		backend   string
		gateModel string
		fixModel  string
		want      bool
	}{
		{"claude", "sonnet", "sonnet", true},
		{"claude", "opus", "sonnet", false},
		{"codex", "gpt-5.4-mini", "gpt-5.4", true},
		{"codex", "gpt-5.4-mini", "sonnet", false},
		{"codex", "sonnet", "gpt-5.4", false},
	}
	for _, tc := range tests {
		tc := tc
		t.Run("UsingBackendDefaults_"+tc.backend+"_"+tc.gateModel+"_"+tc.fixModel, func(t *testing.T) {
			got := UsingBackendDefaults(tc.backend, tc.gateModel, tc.fixModel)
			if got != tc.want {
				t.Errorf("UsingBackendDefaults(%q, %q, %q) = %v, want %v",
					tc.backend, tc.gateModel, tc.fixModel, got, tc.want)
			}
		})
	}
}
