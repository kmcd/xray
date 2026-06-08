package github

import (
	"testing"

	"github.com/shurcooL/githubv4"
)

func TestBuildBranchProtectionFromRule(t *testing.T) {
	t.Run("with_required_reviews_and_checks", func(t *testing.T) {
		rule := branchProtectionRuleGraph{
			RequiresApprovingReviews:    githubv4.Boolean(true),
			RequiredApprovingReviewCount: githubv4.Int(2),
			RequiredStatusCheckContexts: []githubv4.String{"ci/lint", "ci/test"},
			IsAdminEnforced:             githubv4.Boolean(true),
			RestrictsPushes:             githubv4.Boolean(true),
		}
		row := buildBranchProtectionFromRule("r", "main", rule)
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
	t.Run("no_reviews_required", func(t *testing.T) {
		rule := branchProtectionRuleGraph{
			RequiresApprovingReviews: githubv4.Boolean(false),
		}
		row := buildBranchProtectionFromRule("r", "main", rule)
		if row.RequiredReviews != nil {
			t.Errorf("required_reviews should be nil when requiresApprovingReviews=false, got %v", row.RequiredReviews)
		}
	})
	t.Run("empty_rule", func(t *testing.T) {
		row := buildBranchProtectionFromRule("r", "main", branchProtectionRuleGraph{})
		if row.RequiredReviews != nil {
			t.Errorf("required_reviews should be nil")
		}
		if row.RequiredChecks != "" || row.EnforceAdmins || row.RestrictsPushes {
			t.Errorf("expected zero-valued fields on empty rule")
		}
	})
}
