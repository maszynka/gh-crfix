package score

import (
	"testing"
)

func TestIsValidScore(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"0", true},
		{"1", true},
		{"0.5", true},
		{".9", true},
		{"1.0", true},
		{"1.1", false},
		{"-0.5", false},
		{"abc", false},
		{"", false},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.input, func(t *testing.T) {
			got := IsValidScore(tc.input)
			if got != tc.want {
				t.Errorf("IsValidScore(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestSum(t *testing.T) {
	t.Run("multiple values", func(t *testing.T) {
		got := Sum(0.2, 0.4, 1.0)
		want := 1.6
		if got < want-1e-9 || got > want+1e-9 {
			t.Errorf("Sum(0.2, 0.4, 1.0) = %v, want %v", got, want)
		}
	})
	t.Run("no values", func(t *testing.T) {
		got := Sum()
		want := 0.0
		if got != want {
			t.Errorf("Sum() = %v, want %v", got, want)
		}
	})
}

func TestNormalize(t *testing.T) {
	tests := []struct {
		input float64
		want  string
	}{
		{0.333333, "0.333"},
		{1.0, "1.000"},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.want, func(t *testing.T) {
			got := Normalize(tc.input)
			if got != tc.want {
				t.Errorf("Normalize(%v) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestAtLeastOne(t *testing.T) {
	tests := []struct {
		input float64
		want  bool
	}{
		{1.0, true},
		{0.999, false},
		{1.5, true},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(Normalize(tc.input), func(t *testing.T) {
			got := AtLeastOne(tc.input)
			if got != tc.want {
				t.Errorf("AtLeastOne(%v) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}
