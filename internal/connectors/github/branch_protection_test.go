package github

import (
	"testing"

	gh "github.com/google/go-github/v66/github"
)

func TestBuildBranchProtection(t *testing.T) {
	t.Run("with_required_reviews_and_checks_via_Contexts", func(t *testing.T) {
		ctxs := []string{"ci/lint", "ci/test"}
		bp := &gh.Protection{
			RequiredPullRequestReviews: &gh.PullRequestReviewsEnforcement{RequiredApprovingReviewCount: 2},
			RequiredStatusChecks:       &gh.RequiredStatusChecks{Contexts: &ctxs},
			EnforceAdmins:              &gh.AdminEnforcement{Enabled: true},
			Restrictions:               &gh.BranchRestrictions{},
		}
		row := buildBranchProtection("r", "main", bp)
		if row.RequiredReviews == nil || *row.RequiredReviews != 2 {
			t.Errorf("required_reviews = %v", row.RequiredReviews)
		}
		if row.RequiredChecks != "ci/lint,ci/test" {
			t.Errorf("required_checks = %q", row.RequiredChecks)
		}
		if !row.EnforceAdmins {
			t.Errorf("enforce_admins=false")
		}
		if !row.RestrictsPushes {
			t.Errorf("restricts_pushes=false")
		}
	})
	t.Run("with_required_checks_via_Checks", func(t *testing.T) {
		checks := []*gh.RequiredStatusCheck{{Context: "ci/build"}, {Context: "ci/qa"}}
		bp := &gh.Protection{
			RequiredStatusChecks: &gh.RequiredStatusChecks{Checks: &checks},
		}
		row := buildBranchProtection("r", "main", bp)
		if row.RequiredChecks != "ci/build,ci/qa" {
			t.Errorf("required_checks = %q", row.RequiredChecks)
		}
	})
	t.Run("empty_protection_struct", func(t *testing.T) {
		row := buildBranchProtection("r", "main", &gh.Protection{})
		if row.RequiredReviews != nil {
			t.Errorf("required_reviews should be nil")
		}
		if row.RequiredChecks != "" || row.EnforceAdmins || row.RestrictsPushes {
			t.Errorf("expected zero-valued fields on empty Protection")
		}
	})
}
