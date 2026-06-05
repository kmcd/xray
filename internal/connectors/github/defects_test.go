package github

import (
	"reflect"
	"sort"
	"testing"
)

func TestExtractTicketRefs(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "empty",
			in:   "",
			want: nil,
		},
		{
			name: "single jira prefix",
			in:   "Fix PROJ-1 crash",
			want: []string{"PROJ-1"},
		},
		{
			name: "linear style",
			in:   "ENG-4567: tighten retry budget",
			want: []string{"ENG-4567"},
		},
		{
			name: "shortcut style",
			in:   "SC-89 done",
			want: []string{"SC-89"},
		},
		{
			name: "hash ref",
			in:   "closes #123",
			want: []string{"#123"},
		},
		{
			name: "hash ref at start of line",
			in:   "#42 first",
			want: []string{"#42"},
		},
		{
			name: "mixed",
			in:   "PROJ-1 and #7 and ENG-4567",
			want: []string{"PROJ-1", "ENG-4567", "#7"},
		},
		{
			name: "dedup repeated ref",
			in:   "PROJ-1 see PROJ-1 again",
			want: []string{"PROJ-1"},
		},
		{
			name: "non-match: lowercase prefix",
			in:   "Foo-12 not a ticket",
			want: nil,
		},
		{
			name: "non-match: single-char prefix",
			in:   "A-1 too short",
			want: nil,
		},
		{
			name: "non-match: leading digit prefix",
			in:   "1-foo not a ticket",
			want: nil,
		},
		{
			name: "non-match: hash attached to word",
			in:   "foo#123 inline",
			want: nil,
		},
		{
			name: "hash with leading punctuation",
			in:   "(#9) parenthesised",
			want: []string{"#9"},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := extractTicketRefs(tc.in)

			// For "mixed" we care about set equality; first-seen order
			// is checked implicitly elsewhere. Sort both sides for
			// stable comparison except where the test explicitly asserts
			// preservation order.
			gotSorted := append([]string(nil), got...)
			wantSorted := append([]string(nil), tc.want...)
			sort.Strings(gotSorted)
			sort.Strings(wantSorted)
			if !reflect.DeepEqual(gotSorted, wantSorted) {
				t.Fatalf("extractTicketRefs(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestExtractTicketRefsPreservesFirstSeenOrder(t *testing.T) {
	t.Parallel()
	got := extractTicketRefs("first ENG-1 then PROJ-2 then #3")
	want := []string{"ENG-1", "PROJ-2", "#3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order: got %v, want %v", got, want)
	}
}
