package run

import (
	"testing"

	"github.com/kmcd/xray/internal/connector"
)

func TestAggregateMailmapApplied(t *testing.T) {
	mk := func(name string, flag *bool) connector.Provenance {
		p := connector.NewProvenance(name, "kmcd/r", connector.Window{})
		if flag != nil {
			p.Flags["mailmap_applied"] = *flag
		}
		return p
	}
	tru, fls := true, false

	cases := []struct {
		name string
		in   []connector.Provenance
		want bool
	}{
		{"empty", nil, false},
		{
			"single repo, applied",
			[]connector.Provenance{mk("github", &tru)},
			true,
		},
		{
			"single repo, missing flag (e.g. clone failure)",
			[]connector.Provenance{mk("github", nil)},
			false,
		},
		{
			"two repos, one true one false -> false",
			[]connector.Provenance{mk("github", &tru), mk("github", &fls)},
			false,
		},
		{
			"two repos, both true",
			[]connector.Provenance{mk("github", &tru), mk("github", &tru)},
			true,
		},
		{
			"synthetic clone/postprocess provs ignored",
			[]connector.Provenance{mk("clone", nil), mk("postprocess", nil), mk("github", &tru)},
			true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := aggregateMailmapApplied(tc.in); got != tc.want {
				t.Errorf("aggregateMailmapApplied = %v, want %v", got, tc.want)
			}
		})
	}
}
