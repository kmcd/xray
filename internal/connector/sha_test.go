package connector

import "testing"

func TestIsFullSHA(t *testing.T) {
	cases := []struct {
		name string
		s    string
		want bool
	}{
		{name: "valid lowercase", s: "a3f1e2d4c5b6a7f8e9d0c1b2a3f4e5d6c7b8a9f0", want: true},
		{name: "valid uppercase", s: "A3F1E2D4C5B6A7F8E9D0C1B2A3F4E5D6C7B8A9F0", want: true},
		{name: "valid mixed case", s: "A3f1E2d4C5b6A7f8E9d0C1b2A3f4E5d6C7b8A9f0", want: true},
		{name: "all zeros", s: "0000000000000000000000000000000000000000", want: true},
		{name: "too short", s: "a3f1e2d4c5b6a7f8e9d0c1b2a3f4e5d6c7b8a9", want: false},
		{name: "too long", s: "a3f1e2d4c5b6a7f8e9d0c1b2a3f4e5d6c7b8a9f00", want: false},
		{name: "empty", s: "", want: false},
		{name: "contains g", s: "g3f1e2d4c5b6a7f8e9d0c1b2a3f4e5d6c7b8a9f0", want: false},
		{name: "semver", s: "v1.2.3", want: false},
		{name: "build number", s: "1234", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsFullSHA(tc.s); got != tc.want {
				t.Errorf("IsFullSHA(%q) = %v, want %v", tc.s, got, tc.want)
			}
		})
	}
}

func TestNormalizeFullSHA(t *testing.T) {
	cases := []struct {
		name    string
		s       string
		wantOut string
		wantOK  bool
	}{
		{name: "lowercase passthrough", s: "a3f1e2d4c5b6a7f8e9d0c1b2a3f4e5d6c7b8a9f0", wantOut: "a3f1e2d4c5b6a7f8e9d0c1b2a3f4e5d6c7b8a9f0", wantOK: true},
		{name: "uppercase normalized", s: "A3F1E2D4C5B6A7F8E9D0C1B2A3F4E5D6C7B8A9F0", wantOut: "a3f1e2d4c5b6a7f8e9d0c1b2a3f4e5d6c7b8a9f0", wantOK: true},
		{name: "mixed case normalized", s: "A3f1E2d4C5b6A7f8E9d0C1b2A3f4E5d6C7b8A9f0", wantOut: "a3f1e2d4c5b6a7f8e9d0c1b2a3f4e5d6c7b8a9f0", wantOK: true},
		{name: "invalid rejected", s: "not-a-sha", wantOut: "", wantOK: false},
		{name: "empty rejected", s: "", wantOut: "", wantOK: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := NormalizeFullSHA(tc.s)
			if ok != tc.wantOK || got != tc.wantOut {
				t.Errorf("NormalizeFullSHA(%q) = (%q, %v), want (%q, %v)", tc.s, got, ok, tc.wantOut, tc.wantOK)
			}
		})
	}
}
