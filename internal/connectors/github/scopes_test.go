package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func TestParseScopes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"single", "repo", []string{"repo"}},
		{"comma-space", "repo, read:org, workflow", []string{"read:org", "repo", "workflow"}},
		{"dedup", "repo, repo, read:org", []string{"read:org", "repo"}},
		{"trims-empties", "repo,  ,read:org", []string{"read:org", "repo"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseScopes(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseScopes(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestDiffScopes(t *testing.T) {
	got := diffScopes([]string{"read:org", "repo", "workflow"}, []string{"repo", "read:org"})
	want := []string{"workflow"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("diffScopes = %v, want %v", got, want)
	}

	if got := diffScopes([]string{"repo"}, []string{"repo", "read:org"}); got != nil {
		t.Errorf("diffScopes no-extra = %v, want nil", got)
	}
}

func TestScopes_ReadsHeader(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-OAuth-Scopes", "repo, read:org, workflow")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"login":"x"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := newTestConnector(t, srv)
	info, err := c.Scopes(context.Background())
	if err != nil {
		t.Fatalf("Scopes: %v", err)
	}
	wantGranted := []string{"read:org", "repo", "workflow"}
	if !reflect.DeepEqual(info.Granted, wantGranted) {
		t.Errorf("Granted = %v, want %v", info.Granted, wantGranted)
	}
	wantExtra := []string{"workflow"}
	if !reflect.DeepEqual(info.Extra, wantExtra) {
		t.Errorf("Extra = %v, want %v", info.Extra, wantExtra)
	}
}

func TestScopes_EmptyHeader(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"login":"x"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := newTestConnector(t, srv)
	info, err := c.Scopes(context.Background())
	if err != nil {
		t.Fatalf("Scopes: %v", err)
	}
	if info.Granted != nil {
		t.Errorf("Granted = %v, want nil", info.Granted)
	}
	if info.Extra != nil {
		t.Errorf("Extra = %v, want nil", info.Extra)
	}
}

func TestRepoStats_ReadsAggregates(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		_ = r
		_, _ = w.Write([]byte(`{"data":{"repository":{
			"diskUsage": 12345,
			"pullRequests": {"totalCount": 99},
			"defaultBranchRef": {"target": {"history": {"totalCount": 500}}}
		}}}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := newTestConnector(t, srv)
	stats, err := c.RepoStats(context.Background(), []string{"kmcd/foo"}, zeroTime(), zeroTime())
	if err != nil {
		t.Fatalf("RepoStats: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("len(stats) = %d, want 1", len(stats))
	}
	if stats[0].Slug != "kmcd/foo" {
		t.Errorf("Slug = %q, want kmcd/foo", stats[0].Slug)
	}
	if stats[0].DiskUsageKB != 12345 {
		t.Errorf("DiskUsageKB = %d, want 12345", stats[0].DiskUsageKB)
	}
	if stats[0].PullRequests != 99 {
		t.Errorf("PullRequests = %d, want 99", stats[0].PullRequests)
	}
	if stats[0].Commits != 500 {
		t.Errorf("Commits = %d, want 500", stats[0].Commits)
	}
}

func TestRepoStats_ProbeFailureEmitsEmpty(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"errors":[{"message":"not found"}]}`, http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := newTestConnector(t, srv)
	stats, _ := c.RepoStats(context.Background(), []string{"kmcd/foo"}, zeroTime(), zeroTime())
	if len(stats) != 1 || stats[0].Slug != "kmcd/foo" {
		t.Fatalf("stats = %+v, want one empty entry for kmcd/foo", stats)
	}
	if stats[0].DiskUsageKB != 0 || stats[0].PullRequests != 0 || stats[0].Commits != 0 {
		t.Errorf("expected zero stats on probe failure, got %+v", stats[0])
	}
}

func TestProbeEndpoints_NoneInaccessible(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"repository":{"branchProtectionRules":{"totalCount":2}}}}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := newTestConnector(t, srv)
	entries, err := c.ProbeEndpoints(context.Background(), []string{"kmcd/foo"})
	if err != nil {
		t.Fatalf("ProbeEndpoints: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("entries = %+v, want empty", entries)
	}
}

func TestProbeEndpoints_AdminMissing(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"errors":[{"message":"You must have admin permissions"}]}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := newTestConnector(t, srv)
	entries, err := c.ProbeEndpoints(context.Background(), []string{"kmcd/foo"})
	if err != nil {
		t.Fatalf("ProbeEndpoints: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %+v, want one inaccessible", entries)
	}
	if entries[0].Endpoint != "branch_protection" {
		t.Errorf("Endpoint = %q, want branch_protection", entries[0].Endpoint)
	}
	if entries[0].Reason != "admin scope required" {
		t.Errorf("Reason = %q, want 'admin scope required'", entries[0].Reason)
	}
}

func TestCondenseProbeReason(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"GraphQL: You must have admin permissions", "admin scope required"},
		{"saml enforcement required", "SSO authorization required"},
		{"Could not resolve to a Repository: Not Found", "endpoint not visible to this token"},
		{"some other error", "some other error"},
	}
	for _, tc := range cases {
		if got := condenseProbeReason(tc.in); got != tc.want {
			t.Errorf("condenseProbeReason(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func zeroTime() time.Time { return time.Time{} }

func TestScopes_Unauthorized(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad credentials", http.StatusUnauthorized)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := newTestConnector(t, srv)
	if _, err := c.Scopes(context.Background()); err == nil {
		t.Fatal("Scopes err = nil, want non-nil on 401")
	}
}
