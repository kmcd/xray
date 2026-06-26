package connector

import "testing"

func TestNextLink(t *testing.T) {
	cases := []struct {
		name   string
		header string
		want   string
	}{
		{name: "empty", header: "", want: ""},
		{name: "simple next",
			header: `<https://api.bugsnag.com/projects/X/errors?offset=100>; rel="next"`,
			want:   "https://api.bugsnag.com/projects/X/errors?offset=100"},
		{name: "prev and next",
			header: `<https://api.bugsnag.com/p?offset=0>; rel="prev", <https://api.bugsnag.com/p?offset=200>; rel="next"`,
			want:   "https://api.bugsnag.com/p?offset=200"},
		{name: "prev only",
			header: `<https://api.bugsnag.com/p?offset=0>; rel="prev"`,
			want:   ""},
		{name: "space before semicolon",
			header: `<https://api.bugsnag.com/projects/X/errors?offset=100> ; rel="next"`,
			want:   "https://api.bugsnag.com/projects/X/errors?offset=100"},
		{name: "sentry results=true",
			header: `<https://sentry.io/api/0/projects/o/p/issues/?cursor=abc>; rel="next"; results="true"; cursor="abc"`,
			want:   "https://sentry.io/api/0/projects/o/p/issues/?cursor=abc"},
		{name: "sentry results=false",
			header: `<https://sentry.io/api/0/projects/o/p/issues/?cursor=xyz>; rel="next"; results="false"; cursor="xyz"`,
			want:   ""},
		{name: "sentry previous only",
			header: `<https://sentry.io/api/0/projects/o/p/issues/?cursor=p>; rel="previous"; results="true"`,
			want:   ""},
		{name: "sentry prev and next",
			header: `<https://sentry.io/api/0/x?cursor=p>; rel="previous"; results="true", <https://sentry.io/api/0/x?cursor=n>; rel="next"; results="true"`,
			want:   "https://sentry.io/api/0/x?cursor=n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NextLink(tc.header); got != tc.want {
				t.Errorf("NextLink(%q) = %q, want %q", tc.header, got, tc.want)
			}
		})
	}
}
