package github

import "testing"

func TestDeriveMergeMethod(t *testing.T) {
	cases := []struct {
		name      string
		parents   int
		commits   []string
		reachable map[string]bool
		want      string
	}{
		{
			name:    "two_parents_is_merge",
			parents: 2,
			commits: []string{"a", "b"},
			// Reachability irrelevant for the 2-parent branch.
			reachable: nil,
			want:      "merge",
		},
		{
			name:      "two_parents_with_unreachable_still_merge",
			parents:   2,
			commits:   []string{"a"},
			reachable: map[string]bool{"a": false},
			want:      "merge",
		},
		{
			name:      "one_parent_all_reachable_is_rebase",
			parents:   1,
			commits:   []string{"a", "b"},
			reachable: map[string]bool{"a": true, "b": true},
			want:      "rebase",
		},
		{
			name:      "one_parent_one_unreachable_is_squash",
			parents:   1,
			commits:   []string{"a", "b"},
			reachable: map[string]bool{"a": true, "b": false},
			want:      "squash",
		},
		{
			name:      "one_parent_none_reachable_is_squash",
			parents:   1,
			commits:   []string{"a", "b"},
			reachable: map[string]bool{},
			want:      "squash",
		},
		{
			name:    "one_parent_no_pr_commits_treated_as_rebase",
			parents: 1,
			// No commits to check -> vacuously reachable -> rebase. Edge
			// case but consistent with the ADR's "all reachable" wording.
			commits:   nil,
			reachable: nil,
			want:      "rebase",
		},
		{
			name:    "zero_parents_unknown",
			parents: 0,
			want:    "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := deriveMergeMethod(c.parents, c.commits, c.reachable)
			if got != c.want {
				t.Errorf("deriveMergeMethod(%d, %v, %v) = %q, want %q",
					c.parents, c.commits, c.reachable, got, c.want)
			}
		})
	}
}
