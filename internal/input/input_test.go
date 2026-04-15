package input

import (
	"testing"
)

func TestParseURL(t *testing.T) {
	tests := []struct {
		name      string
		rawURL    string
		wantOwner string
		wantPRs   []int
		wantErr   bool
	}{
		{
			name:      "single PR",
			rawURL:    "https://github.com/owner/repo/pull/93",
			wantOwner: "owner/repo",
			wantPRs:   []int{93},
		},
		{
			name:      "range",
			rawURL:    "https://github.com/owner/repo/pull/93-95",
			wantOwner: "owner/repo",
			wantPRs:   []int{93, 94, 95},
		},
		{
			name:      "bracketed comma list",
			rawURL:    "https://github.com/owner/repo/pull/[93,94,95]",
			wantOwner: "owner/repo",
			wantPRs:   []int{93, 94, 95},
		},
		{
			name:      "trailing slash",
			rawURL:    "https://github.com/owner/repo/pull/93/",
			wantOwner: "owner/repo",
			wantPRs:   []int{93},
		},
		{
			name:      "comma list without brackets",
			rawURL:    "https://github.com/owner/repo/pull/1,2,3",
			wantOwner: "owner/repo",
			wantPRs:   []int{1, 2, 3},
		},
		{
			name:      "single-element range",
			rawURL:    "https://github.com/owner/repo/pull/5-5",
			wantOwner: "owner/repo",
			wantPRs:   []int{5},
		},
		{
			name:      "real repo range",
			rawURL:    "https://github.com/maszynka/cdstn-turbo/pull/100-102",
			wantOwner: "maszynka/cdstn-turbo",
			wantPRs:   []int{100, 101, 102},
		},
		{
			name:    "bad input returns error",
			rawURL:  "https://github.com/owner/repo/pull/abc",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			gotOwner, gotPRs, err := ParseURL(tc.rawURL)
			if tc.wantErr {
				if err == nil {
					t.Errorf("ParseURL(%q) expected error, got nil", tc.rawURL)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseURL(%q) unexpected error: %v", tc.rawURL, err)
			}
			if gotOwner != tc.wantOwner {
				t.Errorf("ParseURL(%q) ownerRepo = %q, want %q", tc.rawURL, gotOwner, tc.wantOwner)
			}
			if len(gotPRs) != len(tc.wantPRs) {
				t.Fatalf("ParseURL(%q) prNumbers = %v (len %d), want %v (len %d)",
					tc.rawURL, gotPRs, len(gotPRs), tc.wantPRs, len(tc.wantPRs))
			}
			for i := range tc.wantPRs {
				if gotPRs[i] != tc.wantPRs[i] {
					t.Errorf("ParseURL(%q) prNumbers[%d] = %d, want %d", tc.rawURL, i, gotPRs[i], tc.wantPRs[i])
				}
			}
		})
	}
}

func TestParseURL_LargeRange(t *testing.T) {
	_, prs, err := ParseURL("https://github.com/o/r/pull/1-20")
	if err != nil {
		t.Fatalf("ParseURL large range error: %v", err)
	}
	if len(prs) != 20 {
		t.Errorf("ParseURL large range len = %d, want 20", len(prs))
	}
	if prs[0] != 1 || prs[19] != 20 {
		t.Errorf("ParseURL large range = [%d..%d], want [1..20]", prs[0], prs[len(prs)-1])
	}
}

func TestParseBare(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantPRs []int
		wantErr bool
	}{
		{
			name:    "single number",
			input:   "42",
			wantPRs: []int{42},
		},
		{
			name:    "range",
			input:   "10-12",
			wantPRs: []int{10, 11, 12},
		},
		{
			name:    "comma list",
			input:   "1,2,3",
			wantPRs: []int{1, 2, 3},
		},
		{
			name:    "bracketed list",
			input:   "[7,8,9]",
			wantPRs: []int{7, 8, 9},
		},
		{
			name:    "bad input returns error",
			input:   "abc",
			wantErr: true,
		},
		{
			name:    "descending range returns error",
			input:   "10-8",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			gotPRs, err := ParseBare(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Errorf("ParseBare(%q) expected error, got nil", tc.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseBare(%q) unexpected error: %v", tc.input, err)
			}
			if len(gotPRs) != len(tc.wantPRs) {
				t.Fatalf("ParseBare(%q) = %v (len %d), want %v (len %d)",
					tc.input, gotPRs, len(gotPRs), tc.wantPRs, len(tc.wantPRs))
			}
			for i := range tc.wantPRs {
				if gotPRs[i] != tc.wantPRs[i] {
					t.Errorf("ParseBare(%q)[%d] = %d, want %d", tc.input, i, gotPRs[i], tc.wantPRs[i])
				}
			}
		})
	}
}
