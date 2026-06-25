package honeycomb

import (
	"encoding/json"
	"testing"
	"time"
)

func TestRepoFromMarkerURL(t *testing.T) {
	sha := "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"
	cases := []struct {
		name     string
		url      string
		wantSlug string
		wantSHA  string
	}{
		{
			name:     "valid github commit URL",
			url:      "https://github.com/acme/myapp/commits/" + sha,
			wantSlug: "acme/myapp",
			wantSHA:  sha,
		},
		{
			name:     "valid URL with trailing slash",
			url:      "https://github.com/acme/myapp/commits/" + sha + "/",
			wantSlug: "acme/myapp",
			wantSHA:  sha,
		},
		{
			name:     "empty URL",
			url:      "",
			wantSlug: "",
			wantSHA:  "",
		},
		{
			name:     "non-github URL",
			url:      "https://example.com/release/v1.2.3",
			wantSlug: "",
			wantSHA:  "",
		},
		{
			name:     "github URL without SHA",
			url:      "https://github.com/acme/myapp/commits/",
			wantSlug: "",
			wantSHA:  "",
		},
		{
			name:     "github URL with short SHA",
			url:      "https://github.com/acme/myapp/commits/abc123",
			wantSlug: "",
			wantSHA:  "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotSlug, gotSHA := repoFromMarkerURL(tc.url)
			if gotSlug != tc.wantSlug {
				t.Errorf("slug = %q, want %q", gotSlug, tc.wantSlug)
			}
			if gotSHA != tc.wantSHA {
				t.Errorf("sha = %q, want %q", gotSHA, tc.wantSHA)
			}
		})
	}
}

func TestMarkerToDeploy(t *testing.T) {
	// Sample shape modelled on the Honeycomb /markers response.
	raw := `[
		{
			"id": "abc-123",
			"message": "v1.2.3",
			"type": "production",
			"start_time": 1717420000,
			"end_time": 0,
			"url": "https://example.com/release/v1.2.3"
		},
		{
			"id": "def-456",
			"message": "",
			"type": "",
			"start_time": 1717506400
		}
	]`

	var markers []marker
	if err := json.Unmarshal([]byte(raw), &markers); err != nil {
		t.Fatalf("unmarshal sample markers: %v", err)
	}
	if len(markers) != 2 {
		t.Fatalf("want 2 markers, got %d", len(markers))
	}

	// type="production", no config env → exact match in synonym map.
	d := markerToDeploy(markers[0], "kmcd/foo", "", "")
	if d.ID != "abc-123" {
		t.Errorf("ID = %q, want abc-123", d.ID)
	}
	if d.Repo != "kmcd/foo" {
		t.Errorf("Repo = %q, want kmcd/foo", d.Repo)
	}
	if d.Source != "honeycomb" {
		t.Errorf("Source = %q, want honeycomb", d.Source)
	}
	if d.Environment != "production" {
		t.Errorf("Environment = %q, want production", d.Environment)
	}
	if d.Version != "v1.2.3" {
		t.Errorf("Version = %q, want v1.2.3", d.Version)
	}
	if d.Status != "success" {
		t.Errorf("Status = %q, want success", d.Status)
	}
	if d.CommitSHA != "" {
		t.Errorf("CommitSHA = %q, want empty (no sha passed)", d.CommitSHA)
	}

	// SHA is propagated when provided.
	sha := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	dWithSHA := markerToDeploy(markers[0], "kmcd/foo", sha, "")
	if dWithSHA.CommitSHA != sha {
		t.Errorf("CommitSHA = %q, want %q", dWithSHA.CommitSHA, sha)
	}
	if d.RolledBack {
		t.Errorf("RolledBack = true, want false")
	}
	want := time.Unix(1717420000, 0).UTC()
	if !d.DeployedAt.Equal(want) {
		t.Errorf("DeployedAt = %v, want %v", d.DeployedAt, want)
	}
	if d.DeployedAt.Location() != time.UTC {
		t.Errorf("DeployedAt zone = %v, want UTC", d.DeployedAt.Location())
	}

	// Config environment overrides marker type.
	dCfg := markerToDeploy(markers[0], "kmcd/foo", "", "staging")
	if dCfg.Environment != "staging" {
		t.Errorf("Environment = %q, want staging (config wins)", dCfg.Environment)
	}

	// Marker with no type/message: unknown type maps to "other".
	d2 := markerToDeploy(markers[1], "kmcd/bar", "", "")
	if d2.Environment != "other" {
		t.Errorf("Environment = %q, want other", d2.Environment)
	}
	if d2.Version != "" {
		t.Errorf("Version = %q, want empty", d2.Version)
	}
	if d2.Repo != "kmcd/bar" {
		t.Errorf("Repo = %q, want kmcd/bar", d2.Repo)
	}
}

func TestResolveEnvironment(t *testing.T) {
	cases := []struct {
		name           string
		cfgEnvironment string
		markerType     string
		want           string
	}{
		// Config wins over any type string.
		{"config-staging overrides deploy", "staging", "deploy", "staging"},
		{"config-production overrides blank", "production", "", "production"},
		// Canonical pass-through via synonym map.
		{"canonical production", "", "production", "production"},
		{"canonical staging", "", "staging", "staging"},
		{"canonical preview", "", "preview", "preview"},
		{"canonical release", "", "release", "release"},
		{"canonical other", "", "other", "other"},
		// Synonym normalization.
		{"synonym prod", "", "prod", "production"},
		{"synonym stage", "", "stage", "staging"},
		// Unknown strings → other (the Carwow bug case and variants).
		{"unknown deploy", "", "deploy", "other"},
		{"blank type", "", "", "other"},
		{"unknown foo", "", "foo", "other"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveEnvironment(tc.cfgEnvironment, tc.markerType)
			if got != tc.want {
				t.Errorf("resolveEnvironment(%q, %q) = %q, want %q",
					tc.cfgEnvironment, tc.markerType, got, tc.want)
			}
		})
	}
}

func TestBurnAlertSeverity(t *testing.T) {
	cases := []struct {
		mins int
		want string
	}{
		{60, "error"},
		{5 * 60, "error"},
		{6 * 60, "warning"},
		{24 * 60, "warning"},
		{0, "warning"},
		{-1, "warning"},
	}
	for _, c := range cases {
		got := burnAlertSeverity(c.mins)
		if got != c.want {
			t.Errorf("burnAlertSeverity(%d) = %q, want %q", c.mins, got, c.want)
		}
	}
}

func TestBurnAlertToIncident(t *testing.T) {
	b := burnAlert{
		ID:                "burn-1",
		SLOID:             "slo-x",
		ExhaustionMinutes: 30,
		CreatedAt:         "2025-03-04T05:06:07Z",
	}
	inc, ok := burnAlertToIncident(b, "kmcd/foo")
	if !ok {
		t.Fatalf("burnAlertToIncident: ok=false, want true")
	}
	if inc.ID != "burn-1" {
		t.Errorf("ID = %q, want burn-1", inc.ID)
	}
	if inc.Repo != "kmcd/foo" {
		t.Errorf("Repo = %q, want kmcd/foo", inc.Repo)
	}
	if inc.Source != "honeycomb" {
		t.Errorf("Source = %q, want honeycomb", inc.Source)
	}
	if inc.Severity != "error" {
		t.Errorf("Severity = %q, want error", inc.Severity)
	}
	want := time.Date(2025, 3, 4, 5, 6, 7, 0, time.UTC)
	if !inc.OpenedAt.Equal(want) {
		t.Errorf("OpenedAt = %v, want %v", inc.OpenedAt, want)
	}

	// Missing created_at -> dropped.
	if _, ok := burnAlertToIncident(burnAlert{ID: "x"}, "kmcd/foo"); ok {
		t.Errorf("burnAlertToIncident with empty created_at: ok=true, want false")
	}
	// Garbage created_at -> dropped.
	if _, ok := burnAlertToIncident(burnAlert{ID: "x", CreatedAt: "not a time"}, "kmcd/foo"); ok {
		t.Errorf("burnAlertToIncident with bad created_at: ok=true, want false")
	}
}
