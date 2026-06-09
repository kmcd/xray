package run_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kmcd/xray/internal/connector"
	"github.com/kmcd/xray/internal/manifest"
	"github.com/kmcd/xray/internal/run"
)

func sampleManifest() manifest.Manifest {
	return manifest.Manifest{
		SchemaVersion: 2,
		Counts: map[string]int{
			"commits":      12847,
			"commit_files": 84213,
			"prs":          1203,
			"reviews":      2891,
			"pr_comments":  5012,
			"ci_runs":      4818,
			"incidents":    37,
			"deploys":      812,
			"defects":      14,
			"file_metrics": 0, // exercises the zero-skip
		},
		Provenance: []connector.Provenance{
			{
				Connector:          "github",
				Repo:               "kmcd/foo",
				PaginationComplete: true,
				Endpoints: map[string]connector.EndpointStatus{
					"pulls":           {Accessible: true},
					"reviews":         {Accessible: true},
					"checks":          {Accessible: true},
					"workflow_runs":   {Accessible: true},
					"security_alerts": {Accessible: false, Reason: "403"},
				},
				Errors: map[string]string{},
			},
			{
				Connector:          "git",
				Repo:               "kmcd/foo",
				PaginationComplete: true,
				Endpoints:          map[string]connector.EndpointStatus{},
				Errors:             map[string]string{},
			},
		},
	}
}

func TestSummarize_Golden(t *testing.T) {
	got := run.Summarize(run.SummaryInput{
		Manifest:     sampleManifest(),
		ArtifactPath: "/work/xray-export-20260609T141023Z.tar.gz",
		SHA256:       "3f7a9b1cdeadbeefcafef00d",
		Size:         18 * 1024 * 1024,
		Duration:     22*time.Minute + 18*time.Second,
		LogPath:      "/work/xray-export-20260609T141023Z.log",
	})

	want := `Done in 22m 18s

Artifact
  path:     /work/xray-export-20260609T141023Z.tar.gz
  size:     18.0 MiB
  sha256:   3f7a9b1cdeadbeefcafef00d (verify with: sha256sum)
  schema:   v2
  log:      /work/xray-export-20260609T141023Z.log

Rows captured
  commit_files  84,213
  commits       12,847
  pr_comments   5,012
  ci_runs       4,818
  reviews       2,891
  prs           1,203
  deploys       812
  incidents     37
  (10 tables total)

Provenance
  endpoints accessible:  4/5 (1 permission-gated, see manifest)
  per-row errors:        0
  rate-limit waits:      0
  rate-limit truncated:  0
  partial paginations:   0

Next
  Send xray-export-20260609T141023Z.tar.gz to your consultant.
  Do NOT send your config file — it contains your API tokens.
`

	if got != want {
		t.Errorf("Summarize mismatch.\n--- want ---\n%s\n--- got ---\n%s", want, got)
	}
}

func TestSummarize_Partial(t *testing.T) {
	in := run.SummaryInput{
		Manifest:     sampleManifest(),
		ArtifactPath: "/work/x.tar.gz",
		SHA256:       "abc123",
		Size:         1024,
		Duration:     5 * time.Second,
		PartialFails: []run.PartialFailure{
			{Repo: "kmcd/foo", Connector: "github", Reason: "rate limit hit"},
		},
	}
	got := run.Summarize(in)
	if !strings.Contains(got, "Partial") {
		t.Errorf("expected Partial block, got:\n%s", got)
	}
	if !strings.Contains(got, "kmcd/foo / github: rate limit hit") {
		t.Errorf("expected partial failure line, got:\n%s", got)
	}
}

func TestSummarize_DurationFormats(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{42 * time.Second, "Done in 42s"},
		{2*time.Minute + 5*time.Second, "Done in 2m 5s"},
		{1*time.Hour + 30*time.Minute + 15*time.Second, "Done in 1h 30m 15s"},
		{0, "Done in 0s"},
	}
	for _, c := range cases {
		got := run.Summarize(run.SummaryInput{
			Manifest:     sampleManifest(),
			ArtifactPath: "/tmp/x.tar.gz",
			Duration:     c.d,
		})
		if !strings.HasPrefix(got, c.want+"\n") {
			t.Errorf("Duration %s: want prefix %q, got:\n%s", c.d, c.want, got)
		}
	}
}

func TestSummarize_NoLogPath(t *testing.T) {
	got := run.Summarize(run.SummaryInput{
		Manifest:     sampleManifest(),
		ArtifactPath: "/tmp/x.tar.gz",
	})
	if strings.Contains(got, "log:") {
		t.Errorf("expected no log line when LogPath empty, got:\n%s", got)
	}
}

func TestBuildRunSummary_JSONShape(t *testing.T) {
	in := run.SummaryInput{
		Manifest:     sampleManifest(),
		ArtifactPath: "/work/x.tar.gz",
		SHA256:       "deadbeef",
		Size:         19320832,
		Duration:     18*time.Minute + 22*time.Second,
		LogPath:      "/work/x.log",
	}
	rs := run.BuildRunSummary(in, true)

	buf, err := json.Marshal(rs)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(buf, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["kind"] != "run_summary" {
		t.Errorf("kind = %v, want run_summary", m["kind"])
	}
	if m["ok"] != true {
		t.Errorf("ok = %v, want true", m["ok"])
	}
	if m["duration_s"].(float64) != 1102 {
		t.Errorf("duration_s = %v, want 1102", m["duration_s"])
	}
	art := m["artifact"].(map[string]any)
	if art["path"] != "/work/x.tar.gz" {
		t.Errorf("artifact.path = %v", art["path"])
	}
	if art["sha256"] != "deadbeef" {
		t.Errorf("artifact.sha256 = %v", art["sha256"])
	}
	if art["size_bytes"].(float64) != 19320832 {
		t.Errorf("artifact.size_bytes = %v", art["size_bytes"])
	}
	if art["schema_version"].(float64) != 2 {
		t.Errorf("artifact.schema_version = %v", art["schema_version"])
	}
	rows := m["rows"].(map[string]any)
	if rows["_table_count"].(float64) != 10 {
		t.Errorf("rows._table_count = %v, want 10", rows["_table_count"])
	}
	if rows["commits"].(float64) != 12847 {
		t.Errorf("rows.commits = %v, want 12847", rows["commits"])
	}
	prov := m["provenance"].(map[string]any)
	if prov["endpoints_accessible"].(float64) != 4 {
		t.Errorf("endpoints_accessible = %v", prov["endpoints_accessible"])
	}
	if prov["endpoints_total"].(float64) != 5 {
		t.Errorf("endpoints_total = %v", prov["endpoints_total"])
	}
	if m["partial"] == nil {
		t.Errorf("partial should be [] (not null) when no partial fails")
	}
}

func TestBuildRunSummary_PartialFalse(t *testing.T) {
	in := run.SummaryInput{
		Manifest:     sampleManifest(),
		ArtifactPath: "/work/x.tar.gz",
		PartialFails: []run.PartialFailure{
			{Repo: "kmcd/foo", Connector: "github", Reason: "503"},
		},
	}
	rs := run.BuildRunSummary(in, false)
	if rs.OK {
		t.Errorf("OK should be false on partial")
	}
	if len(rs.Partial) != 1 {
		t.Errorf("Partial length = %d, want 1", len(rs.Partial))
	}
}

func TestExtractPartialFailures(t *testing.T) {
	provs := []connector.Provenance{
		{Connector: "github", Repo: "kmcd/foo", Errors: map[string]string{"pulls": "503"}},
		{Connector: "git", Repo: "kmcd/foo", Errors: map[string]string{}},
		{Connector: "github", Repo: "kmcd/bar", Errors: map[string]string{"reviews": "rate limit"}},
	}
	got := run.ExtractPartialFailures(provs)
	if len(got) != 2 {
		t.Fatalf("got %d failures, want 2", len(got))
	}
	if got[0].Repo != "kmcd/bar" || got[0].Connector != "github" {
		t.Errorf("first failure = %+v, want kmcd/bar github", got[0])
	}
	if got[1].Repo != "kmcd/foo" {
		t.Errorf("second failure = %+v, want kmcd/foo", got[1])
	}
}
