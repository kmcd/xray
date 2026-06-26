package bugsnag

import (
	"encoding/json"
	"testing"
	"time"
)

func TestToIncident_FixedWithReleaseAndReopen(t *testing.T) {
	const raw = `{
		"id": "5d5a8b9c0e1f2a3b4c5d6e7f",
		"first_seen": "2025-02-01T12:00:00Z",
		"last_seen":  "2025-02-15T18:30:00Z",
		"status": "fixed",
		"severity": "error",
		"events": 42,
		"reopened_at": "2025-02-10T09:00:00Z",
		"release": {"app_version": "1.4.7", "revision": "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"}
	}`

	var be bugsnagError
	if err := json.Unmarshal([]byte(raw), &be); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	inc := toIncident(be, "kmcd/foo")

	if inc.ID != "5d5a8b9c0e1f2a3b4c5d6e7f" {
		t.Errorf("ID = %q, want bugsnag id", inc.ID)
	}
	if inc.Repo != "kmcd/foo" {
		t.Errorf("Repo = %q", inc.Repo)
	}
	if inc.Source != "bugsnag" {
		t.Errorf("Source = %q, want bugsnag", inc.Source)
	}
	wantOpen := time.Date(2025, 2, 1, 12, 0, 0, 0, time.UTC)
	if !inc.OpenedAt.Equal(wantOpen) {
		t.Errorf("OpenedAt = %v, want %v", inc.OpenedAt, wantOpen)
	}
	if inc.ResolvedAt == nil {
		t.Fatal("ResolvedAt = nil, want last_seen because status=fixed")
	}
	wantResolved := time.Date(2025, 2, 15, 18, 30, 0, 0, time.UTC)
	if !inc.ResolvedAt.Equal(wantResolved) {
		t.Errorf("ResolvedAt = %v, want %v", *inc.ResolvedAt, wantResolved)
	}
	if inc.Severity != "error" {
		t.Errorf("Severity = %q", inc.Severity)
	}
	if inc.Occurrences != 42 {
		t.Errorf("Occurrences = %d, want 42", inc.Occurrences)
	}
	if inc.ReleaseRef != "1.4.7" {
		t.Errorf("ReleaseRef = %q, want 1.4.7", inc.ReleaseRef)
	}
	if inc.DeployID != "" {
		t.Errorf("DeployID = %q, want empty (no deploy-tracking endpoint)", inc.DeployID)
	}
	if inc.CommitSHA != "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef" {
		t.Errorf("CommitSHA = %q, want deadbeefdeadbeefdeadbeefdeadbeefdeadbeef from release.revision", inc.CommitSHA)
	}
	if inc.AcknowledgedAt != nil {
		t.Errorf("AcknowledgedAt = %v, want nil (no native concept)", *inc.AcknowledgedAt)
	}
	if !inc.IsRegression {
		t.Error("IsRegression = false, want true because reopened_at is set")
	}
	if inc.CulpritRef != "" {
		t.Errorf("CulpritRef = %q, want empty per spec", inc.CulpritRef)
	}
}

func TestToIncident_OpenNoReleaseNoReopen(t *testing.T) {
	const raw = `{
		"id": "abc",
		"first_seen": "2025-03-01T00:00:00Z",
		"last_seen":  "2025-03-02T00:00:00Z",
		"status": "open",
		"severity": "warning",
		"events": 3
	}`

	var be bugsnagError
	if err := json.Unmarshal([]byte(raw), &be); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	inc := toIncident(be, "kmcd/bar")

	if inc.ResolvedAt != nil {
		t.Errorf("ResolvedAt = %v, want nil for status=open", *inc.ResolvedAt)
	}
	if inc.IsRegression {
		t.Error("IsRegression = true, want false when reopened_at absent")
	}
	if inc.ReleaseRef != "" {
		t.Errorf("ReleaseRef = %q, want empty when release absent", inc.ReleaseRef)
	}
	if inc.Severity != "warning" {
		t.Errorf("Severity = %q", inc.Severity)
	}
	if inc.Occurrences != 3 {
		t.Errorf("Occurrences = %d, want 3", inc.Occurrences)
	}
	if inc.CulpritRef != "" {
		t.Errorf("CulpritRef = %q, want empty per spec", inc.CulpritRef)
	}
	if inc.CommitSHA != "" {
		t.Errorf("CommitSHA = %q, want empty when release absent", inc.CommitSHA)
	}
}

func TestToIncident_RevisionCases(t *testing.T) {
	cases := []struct {
		name       string
		releaseJSON string
		wantSHA    string
	}{
		{
			name:        "revision absent",
			releaseJSON: `{"app_version": "1.4.7"}`,
			wantSHA:     "",
		},
		{
			name:        "revision is build number, not sha",
			releaseJSON: `{"app_version": "1.4.7", "revision": "1024"}`,
			wantSHA:     "",
		},
		{
			name:        "revision is semver, not sha",
			releaseJSON: `{"app_version": "1.4.7", "revision": "v1.4.7"}`,
			wantSHA:     "",
		},
		{
			name:        "revision is valid 40-char sha",
			releaseJSON: `{"app_version": "1.4.7", "revision": "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"}`,
			wantSHA:     "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := `{"id":"x","first_seen":"2025-01-01T00:00:00Z","last_seen":"2025-01-02T00:00:00Z","status":"open","severity":"error","events":1,"release":` + tc.releaseJSON + `}`
			var be bugsnagError
			if err := json.Unmarshal([]byte(raw), &be); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			inc := toIncident(be, "kmcd/foo")
			if inc.CommitSHA != tc.wantSHA {
				t.Errorf("CommitSHA = %q, want %q", inc.CommitSHA, tc.wantSHA)
			}
		})
	}
}

