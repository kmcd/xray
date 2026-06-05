package circleci

import (
	"encoding/json"
	"testing"
	"time"
)

const samplePipeline = `{
  "id": "11111111-1111-1111-1111-111111111111",
  "number": 42,
  "created_at": "2025-03-01T12:00:00Z",
  "state": "created",
  "vcs": {
    "revision": "abc123def456",
    "branch": "main",
    "tag": "",
    "origin_repository_url": "https://github.com/kmcd/foo",
    "target_repository_url": "https://github.com/kmcd/foo"
  }
}`

const sampleWorkflow = `{
  "id": "22222222-2222-2222-2222-222222222222",
  "name": "build-and-test",
  "status": "success",
  "created_at": "2025-03-01T12:00:05Z",
  "stopped_at": "2025-03-01T12:10:05Z",
  "pipeline_id": "11111111-1111-1111-1111-111111111111"
}`

const sampleJobs = `{
  "items": [
    {
      "id": "j1",
      "name": "build",
      "status": "success",
      "job_number": 101,
      "started_at": "2025-03-01T12:00:10Z",
      "stopped_at": "2025-03-01T12:02:10Z"
    },
    {
      "id": "j2",
      "name": "test",
      "status": "failed",
      "job_number": 102,
      "started_at": "2025-03-01T12:02:15Z",
      "stopped_at": "2025-03-01T12:09:15Z"
    },
    {
      "id": "j3",
      "name": "deploy",
      "status": "canceled",
      "job_number": 103
    }
  ],
  "next_page_token": ""
}`

func TestBuildFromWorkflow(t *testing.T) {
	var p pipeline
	if err := json.Unmarshal([]byte(samplePipeline), &p); err != nil {
		t.Fatalf("pipeline unmarshal: %v", err)
	}
	var w workflow
	if err := json.Unmarshal([]byte(sampleWorkflow), &w); err != nil {
		t.Fatalf("workflow unmarshal: %v", err)
	}

	build := buildFromWorkflow("kmcd/foo", p, w)

	if build.ID != "22222222-2222-2222-2222-222222222222" {
		t.Errorf("ID = %q", build.ID)
	}
	if build.Repo != "kmcd/foo" {
		t.Errorf("Repo = %q", build.Repo)
	}
	if build.Source != "circleci" {
		t.Errorf("Source = %q", build.Source)
	}
	if build.Pipeline != "build-and-test" {
		t.Errorf("Pipeline = %q", build.Pipeline)
	}
	if build.Status != "success" {
		t.Errorf("Status = %q", build.Status)
	}
	if build.Conclusion != "success" {
		t.Errorf("Conclusion = %q", build.Conclusion)
	}
	if build.CommitSHA != "abc123def456" {
		t.Errorf("CommitSHA = %q", build.CommitSHA)
	}
	if build.Branch != "main" {
		t.Errorf("Branch = %q", build.Branch)
	}
	if build.Event != "push" {
		t.Errorf("Event = %q", build.Event)
	}
	if build.Attempt != 1 {
		t.Errorf("Attempt = %d", build.Attempt)
	}
	if build.RerunOfID != "" {
		t.Errorf("RerunOfID = %q (want empty)", build.RerunOfID)
	}
	if build.PRNumber != nil {
		t.Errorf("PRNumber = %v (want nil)", build.PRNumber)
	}
	if build.StartedAt == nil || !build.StartedAt.Equal(time.Date(2025, 3, 1, 12, 0, 5, 0, time.UTC)) {
		t.Errorf("StartedAt = %v", build.StartedAt)
	}
	if build.CompletedAt == nil || !build.CompletedAt.Equal(time.Date(2025, 3, 1, 12, 10, 5, 0, time.UTC)) {
		t.Errorf("CompletedAt = %v", build.CompletedAt)
	}
	if build.DurationSeconds == nil || *build.DurationSeconds != 600 {
		t.Errorf("DurationSeconds = %v (want 600)", build.DurationSeconds)
	}
}

func TestBuildFromWorkflowFallsBackToPipelineNumber(t *testing.T) {
	var p pipeline
	if err := json.Unmarshal([]byte(samplePipeline), &p); err != nil {
		t.Fatalf("pipeline unmarshal: %v", err)
	}
	w := workflow{
		ID:        "w-noname",
		Name:      "",
		Status:    "running",
		CreatedAt: time.Date(2025, 3, 1, 12, 0, 5, 0, time.UTC),
	}
	build := buildFromWorkflow("kmcd/foo", p, w)
	if build.Pipeline != "42" {
		t.Errorf("Pipeline fallback = %q, want \"42\"", build.Pipeline)
	}
	if build.Conclusion != "" {
		t.Errorf("Conclusion = %q (want empty for running)", build.Conclusion)
	}
	if build.CompletedAt != nil {
		t.Errorf("CompletedAt = %v (want nil)", build.CompletedAt)
	}
	if build.DurationSeconds != nil {
		t.Errorf("DurationSeconds = %v (want nil)", build.DurationSeconds)
	}
}

func TestBuildJobFromJob(t *testing.T) {
	var page jobsPage
	if err := json.Unmarshal([]byte(sampleJobs), &page); err != nil {
		t.Fatalf("jobs unmarshal: %v", err)
	}
	if len(page.Items) != 3 {
		t.Fatalf("expected 3 jobs, got %d", len(page.Items))
	}

	wantBuildID := "22222222-2222-2222-2222-222222222222"

	bj0 := buildJobFromJob("kmcd/foo", wantBuildID, page.Items[0])
	if bj0.BuildID != wantBuildID {
		t.Errorf("BuildID = %q", bj0.BuildID)
	}
	if bj0.Name != "build" {
		t.Errorf("Name = %q", bj0.Name)
	}
	if bj0.Status != "success" {
		t.Errorf("Status = %q", bj0.Status)
	}
	if bj0.Conclusion != "success" {
		t.Errorf("Conclusion = %q", bj0.Conclusion)
	}
	if bj0.DurationSeconds == nil || *bj0.DurationSeconds != 120 {
		t.Errorf("DurationSeconds = %v (want 120)", bj0.DurationSeconds)
	}
	if bj0.Attempt != 1 {
		t.Errorf("Attempt = %d", bj0.Attempt)
	}

	bj1 := buildJobFromJob("kmcd/foo", wantBuildID, page.Items[1])
	if bj1.Conclusion != "failure" {
		t.Errorf("failed job Conclusion = %q (want failure)", bj1.Conclusion)
	}
	if bj1.DurationSeconds == nil || *bj1.DurationSeconds != 420 {
		t.Errorf("failed job DurationSeconds = %v (want 420)", bj1.DurationSeconds)
	}

	bj2 := buildJobFromJob("kmcd/foo", wantBuildID, page.Items[2])
	if bj2.Conclusion != "cancelled" {
		t.Errorf("canceled job Conclusion = %q (want cancelled)", bj2.Conclusion)
	}
	if bj2.DurationSeconds != nil {
		t.Errorf("canceled job DurationSeconds = %v (want nil)", bj2.DurationSeconds)
	}
}

func TestMapWorkflowConclusion(t *testing.T) {
	cases := map[string]string{
		"success":     "success",
		"failed":      "failure",
		"failing":     "failure",
		"error":       "failure",
		"canceled":    "cancelled",
		"cancelled":   "cancelled",
		"not_run":     "skipped",
		"skipped":     "skipped",
		"needs_setup": "neutral",
		"running":     "",
		"on_hold":     "",
		"":            "",
	}
	for status, want := range cases {
		if got := mapWorkflowConclusion(status); got != want {
			t.Errorf("mapWorkflowConclusion(%q) = %q, want %q", status, got, want)
		}
	}
}

func TestMapJobConclusion(t *testing.T) {
	cases := map[string]string{
		"success":             "success",
		"failed":              "failure",
		"infrastructure_fail": "failure",
		"canceled":            "cancelled",
		"timedout":            "timed_out",
		"not_run":             "skipped",
		"skipped":             "skipped",
		"running":             "",
		"queued":              "",
		"blocked":             "",
	}
	for status, want := range cases {
		if got := mapJobConclusion(status); got != want {
			t.Errorf("mapJobConclusion(%q) = %q, want %q", status, got, want)
		}
	}
}

func TestProjectSlug(t *testing.T) {
	cases := map[string]string{
		"kmcd/foo":     "gh/kmcd/foo",
		"owner/repo":   "gh/owner/repo",
		"nopath":       "",
		"":             "",
		"/missing":     "",
		"missing/":     "",
		"a/b/c":        "gh/a/b/c",
	}
	for in, want := range cases {
		if got := projectSlug(in); got != want {
			t.Errorf("projectSlug(%q) = %q, want %q", in, got, want)
		}
	}
}
