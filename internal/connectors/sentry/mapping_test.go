package sentry

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/kmcd/xray/internal/connector"
)

func mustParse(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return v
}

func testWindow(t *testing.T) connector.Window {
	return connector.Window{
		Start: mustParse(t, "2025-01-01T00:00:00Z"),
		End:   mustParse(t, "2025-06-30T23:59:59Z"),
	}
}

func decodeIssue(t *testing.T, body string) issue {
	t.Helper()
	var iss issue
	if err := json.Unmarshal([]byte(body), &iss); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return iss
}

func TestMapIssue_BasicFields(t *testing.T) {
	body := `{
		"id": "9001",
		"status": "resolved",
		"level": "error",
		"culprit": "app/services/widget.rb in process",
		"message": "NoMethodError: undefined method foo",
		"count": "42",
		"firstSeen": "2025-02-15T10:00:00Z",
		"lastSeen":  "2025-03-20T11:00:00Z",
		"isUnhandled": false,
		"firstRelease": {"version": "long-sha-version", "shortVersion": "v1.2.3"}
	}`
	iss := decodeIssue(t, body)
	inc, ok := mapIssue(iss, "kmcd/foo", testWindow(t))
	if !ok {
		t.Fatalf("expected mapping to succeed")
	}
	if inc.ID != "9001" {
		t.Errorf("ID = %q", inc.ID)
	}
	if inc.Repo != "kmcd/foo" {
		t.Errorf("Repo = %q", inc.Repo)
	}
	if inc.Source != "sentry" {
		t.Errorf("Source = %q", inc.Source)
	}
	if inc.Severity != "error" {
		t.Errorf("Severity = %q", inc.Severity)
	}
	if inc.Occurrences != 42 {
		t.Errorf("Occurrences = %d", inc.Occurrences)
	}
	if inc.ReleaseRef != "v1.2.3" {
		t.Errorf("ReleaseRef = %q, want shortVersion", inc.ReleaseRef)
	}
	if inc.CulpritRef != "app/services/widget.rb in process" {
		t.Errorf("CulpritRef = %q", inc.CulpritRef)
	}
	if inc.ResolvedAt == nil {
		t.Fatalf("ResolvedAt = nil, want lastSeen for status=resolved")
	}
	if !inc.ResolvedAt.Equal(mustParse(t, "2025-03-20T11:00:00Z")) {
		t.Errorf("ResolvedAt = %v", inc.ResolvedAt)
	}
	if inc.AcknowledgedAt != nil {
		t.Errorf("AcknowledgedAt = %v, want nil", inc.AcknowledgedAt)
	}
	if inc.DeployID != "" || inc.CommitSHA != "" {
		t.Errorf("DeployID/CommitSHA should be empty in v1, got %q/%q", inc.DeployID, inc.CommitSHA)
	}
}

func TestMapIssue_SeverityPassthrough(t *testing.T) {
	for _, level := range []string{"info", "warning", "error", "fatal"} {
		body := `{"id":"1","level":"` + level + `","firstSeen":"2025-02-01T00:00:00Z","count":"1"}`
		inc, ok := mapIssue(decodeIssue(t, body), "kmcd/foo", testWindow(t))
		if !ok {
			t.Fatalf("level=%s: mapping failed", level)
		}
		if inc.Severity != level {
			t.Errorf("level=%s: Severity = %q", level, inc.Severity)
		}
	}
}

func TestMapIssue_OccurrencesParsing(t *testing.T) {
	cases := map[string]int{
		"0":      0,
		"1":      1,
		"12345":  12345,
		"":       0,
		"bogus":  0,
	}
	for raw, want := range cases {
		body := `{"id":"1","level":"error","firstSeen":"2025-02-01T00:00:00Z","count":"` + raw + `"}`
		inc, ok := mapIssue(decodeIssue(t, body), "kmcd/foo", testWindow(t))
		if !ok {
			t.Fatalf("count=%q: mapping failed", raw)
		}
		if inc.Occurrences != want {
			t.Errorf("count=%q: Occurrences = %d, want %d", raw, inc.Occurrences, want)
		}
	}
}

// TestMapIssue_RegressionFromUnhandled asserts the only remaining signal for
// is_regression: Sentry's issue.isUnhandled (ADR 018).
func TestMapIssue_RegressionFromUnhandled(t *testing.T) {
	body := `{"id":"1","level":"error","firstSeen":"2025-02-01T00:00:00Z","count":"1","isUnhandled":true}`
	inc, ok := mapIssue(decodeIssue(t, body), "kmcd/foo", testWindow(t))
	if !ok {
		t.Fatalf("mapping failed")
	}
	if !inc.IsRegression {
		t.Errorf("IsRegression = false, want true for isUnhandled=true")
	}
}

// TestMapIssue_NotRegressionWhenTagged is the case ADR 018 explicitly calls
// out: a user-named "regression" substring in message/title/culprit/tags must
// NOT promote a handled issue to is_regression. This is the false-positive
// vector the old substring path created.
func TestMapIssue_NotRegressionWhenTagged(t *testing.T) {
	body := `{
		"id":"1",
		"level":"error",
		"firstSeen":"2025-02-01T00:00:00Z",
		"count":"1",
		"isUnhandled":false,
		"message":"Regression: foo broke",
		"title":"Regression in widget",
		"culprit":"app/regression_suite.rb",
		"tags":[{"key":"category","value":"regression-candidate"}]
	}`
	inc, ok := mapIssue(decodeIssue(t, body), "kmcd/foo", testWindow(t))
	if !ok {
		t.Fatalf("mapping failed")
	}
	if inc.IsRegression {
		t.Errorf("IsRegression = true, want false: substring 'regression' in tags/message/title/culprit must not flip the flag (ADR 018)")
	}
}

// TestMapIssue_NotRegressionWhenClean is the negative baseline: handled and
// no substring noise -> not a regression.
func TestMapIssue_NotRegressionWhenClean(t *testing.T) {
	body := `{"id":"1","level":"error","firstSeen":"2025-02-01T00:00:00Z","count":"1","isUnhandled":false,"message":"timeout calling api"}`
	inc, ok := mapIssue(decodeIssue(t, body), "kmcd/foo", testWindow(t))
	if !ok {
		t.Fatalf("mapping failed")
	}
	if inc.IsRegression {
		t.Errorf("IsRegression = true, want false for handled clean issue")
	}
}

func TestMapIssue_OutsideWindowSkipped(t *testing.T) {
	body := `{"id":"1","level":"error","firstSeen":"2024-01-01T00:00:00Z","count":"1"}`
	if _, ok := mapIssue(decodeIssue(t, body), "kmcd/foo", testWindow(t)); ok {
		t.Fatalf("expected mapping to skip issue outside window")
	}
}

func TestMapIssue_UnresolvedHasNoResolvedAt(t *testing.T) {
	body := `{"id":"1","status":"unresolved","level":"error","firstSeen":"2025-02-01T00:00:00Z","lastSeen":"2025-03-01T00:00:00Z","count":"1"}`
	inc, ok := mapIssue(decodeIssue(t, body), "kmcd/foo", testWindow(t))
	if !ok {
		t.Fatalf("mapping failed")
	}
	if inc.ResolvedAt != nil {
		t.Errorf("ResolvedAt = %v, want nil for unresolved", inc.ResolvedAt)
	}
}

func TestMapIssue_ReleaseFallsBackToVersion(t *testing.T) {
	body := `{"id":"1","level":"error","firstSeen":"2025-02-01T00:00:00Z","count":"1","firstRelease":{"version":"v9.9.9"}}`
	inc, ok := mapIssue(decodeIssue(t, body), "kmcd/foo", testWindow(t))
	if !ok {
		t.Fatalf("mapping failed")
	}
	if inc.ReleaseRef != "v9.9.9" {
		t.Errorf("ReleaseRef = %q, want v9.9.9", inc.ReleaseRef)
	}
}

