package githubactions

import (
	"testing"
	"time"

	"github.com/google/go-github/v66/github"
)

func TestMapBuild_BasicFields(t *testing.T) {
	created := time.Date(2025, 3, 1, 10, 0, 0, 0, time.UTC)
	started := time.Date(2025, 3, 1, 10, 0, 5, 0, time.UTC)
	updated := time.Date(2025, 3, 1, 10, 4, 25, 0, time.UTC)
	prNum := 42

	run := &github.WorkflowRun{
		ID:           github.Int64(99887766),
		Name:         github.String("CI"),
		Status:       github.String("completed"),
		Conclusion:   github.String("success"),
		HeadSHA:      github.String("deadbeefcafef00d"),
		HeadBranch:   github.String("main"),
		Event:        github.String("push"),
		RunAttempt:   github.Int(1),
		CreatedAt:    &github.Timestamp{Time: created},
		RunStartedAt: &github.Timestamp{Time: started},
		UpdatedAt:    &github.Timestamp{Time: updated},
		PullRequests: []*github.PullRequest{
			{Number: &prNum},
		},
	}

	got := mapBuild(run, "kmcd/foo")

	if got.ID != "99887766" {
		t.Errorf("ID: got %q want %q", got.ID, "99887766")
	}
	if got.Repo != "kmcd/foo" {
		t.Errorf("Repo: got %q", got.Repo)
	}
	if got.Source != "github_actions" {
		t.Errorf("Source: got %q want %q", got.Source, "github_actions")
	}
	if got.Pipeline != "CI" {
		t.Errorf("Pipeline: got %q want CI", got.Pipeline)
	}
	if got.Status != "completed" || got.Conclusion != "success" {
		t.Errorf("Status/Conclusion: got %q/%q", got.Status, got.Conclusion)
	}
	if got.CommitSHA != "deadbeefcafef00d" {
		t.Errorf("CommitSHA: got %q", got.CommitSHA)
	}
	if got.Branch != "main" {
		t.Errorf("Branch: got %q", got.Branch)
	}
	if got.Event != "push" {
		t.Errorf("Event: got %q", got.Event)
	}
	if got.Attempt != 1 {
		t.Errorf("Attempt: got %d want 1", got.Attempt)
	}
	if got.RerunOfID != "" {
		t.Errorf("RerunOfID: got %q want empty for attempt=1", got.RerunOfID)
	}
	if !got.CreatedAt.Equal(created) {
		t.Errorf("CreatedAt: got %v want %v", got.CreatedAt, created)
	}
	if got.StartedAt == nil || !got.StartedAt.Equal(started) {
		t.Errorf("StartedAt: got %v", got.StartedAt)
	}
	if got.CompletedAt == nil || !got.CompletedAt.Equal(updated) {
		t.Errorf("CompletedAt: got %v", got.CompletedAt)
	}
	if got.DurationSeconds == nil || *got.DurationSeconds != int(updated.Sub(started).Seconds()) {
		t.Errorf("DurationSeconds: got %v want %d", got.DurationSeconds, int(updated.Sub(started).Seconds()))
	}
	if got.PRNumber == nil || *got.PRNumber != 42 {
		t.Errorf("PRNumber: got %v want 42", got.PRNumber)
	}
}

func TestMapBuild_RerunPopulatesRerunOfID(t *testing.T) {
	run := &github.WorkflowRun{
		ID:         github.Int64(123),
		RunAttempt: github.Int(3),
		CreatedAt:  &github.Timestamp{Time: time.Now().UTC()},
	}
	got := mapBuild(run, "kmcd/foo")
	if got.Attempt != 3 {
		t.Errorf("Attempt: got %d want 3", got.Attempt)
	}
	if got.RerunOfID != "123" {
		t.Errorf("RerunOfID: got %q want %q", got.RerunOfID, "123")
	}
}

func TestMapBuild_DefaultsWhenNilFields(t *testing.T) {
	run := &github.WorkflowRun{
		ID:        github.Int64(7),
		CreatedAt: &github.Timestamp{Time: time.Now().UTC()},
	}
	got := mapBuild(run, "kmcd/foo")
	if got.Attempt != 1 {
		t.Errorf("Attempt default: got %d want 1", got.Attempt)
	}
	if got.StartedAt != nil || got.CompletedAt != nil {
		t.Errorf("expected nil started/completed when run has no timestamps")
	}
	if got.DurationSeconds != nil {
		t.Errorf("DurationSeconds should be nil when started/completed missing")
	}
	if got.PRNumber != nil {
		t.Errorf("PRNumber should be nil when no PRs")
	}
	if got.Pipeline != "" || got.Status != "" || got.Conclusion != "" {
		t.Errorf("string fields should be empty when nil-input")
	}
}

func TestMapBuildJob_DurationAndPassthroughFields(t *testing.T) {
	started := time.Date(2025, 4, 1, 0, 0, 0, 0, time.UTC)
	completed := started.Add(90 * time.Second)
	job := &github.WorkflowJob{
		ID:          github.Int64(555),
		Name:        github.String("test"),
		Status:      github.String("completed"),
		Conclusion:  github.String("failure"),
		StartedAt:   &github.Timestamp{Time: started},
		CompletedAt: &github.Timestamp{Time: completed},
	}
	got := mapBuildJob(job, 99887766, "kmcd/foo", 2)
	if got.BuildID != "99887766" {
		t.Errorf("BuildID: got %q", got.BuildID)
	}
	if got.Repo != "kmcd/foo" {
		t.Errorf("Repo: got %q", got.Repo)
	}
	if got.Name != "test" {
		t.Errorf("Name: got %q", got.Name)
	}
	if got.Status != "completed" || got.Conclusion != "failure" {
		t.Errorf("Status/Conclusion: got %q/%q", got.Status, got.Conclusion)
	}
	if got.Attempt != 2 {
		t.Errorf("Attempt: got %d want 2", got.Attempt)
	}
	if got.DurationSeconds == nil || *got.DurationSeconds != 90 {
		t.Errorf("DurationSeconds: got %v want 90", got.DurationSeconds)
	}
}

func TestMapBuildJob_MissingTimestampsLeavesDurationNil(t *testing.T) {
	job := &github.WorkflowJob{
		ID:     github.Int64(1),
		Name:   github.String("lint"),
		Status: github.String("queued"),
	}
	got := mapBuildJob(job, 1, "kmcd/foo", 1)
	if got.DurationSeconds != nil {
		t.Errorf("DurationSeconds should be nil when timestamps absent, got %v", got.DurationSeconds)
	}
}

func TestMapDeploy_FieldsAndDefaults(t *testing.T) {
	created := time.Date(2025, 5, 1, 12, 0, 0, 0, time.UTC)
	d := &github.Deployment{
		ID:          github.Int64(4242),
		SHA:         github.String("cafebabe"),
		Ref:         github.String("v1.2.3"),
		Task:        github.String("deploy:production"),
		Environment: github.String("production"),
		CreatedAt:   &github.Timestamp{Time: created},
	}
	got := mapDeploy(d, "success", "kmcd/foo", created)
	if got.ID != "4242" {
		t.Errorf("ID: got %q", got.ID)
	}
	if got.Source != "github_actions" {
		t.Errorf("Source: got %q", got.Source)
	}
	if got.Environment != "production" {
		t.Errorf("Environment: got %q", got.Environment)
	}
	if got.CommitSHA != "cafebabe" {
		t.Errorf("CommitSHA: got %q", got.CommitSHA)
	}
	if got.Version != "v1.2.3" {
		t.Errorf("Version: got %q", got.Version)
	}
	if got.Trigger != "deploy:production" {
		t.Errorf("Trigger: got %q", got.Trigger)
	}
	if got.Status != "success" {
		t.Errorf("Status: got %q", got.Status)
	}
	if !got.DeployedAt.Equal(created) {
		t.Errorf("DeployedAt: got %v want %v", got.DeployedAt, created)
	}
	if got.ReleaseTag != "" {
		t.Errorf("ReleaseTag should be empty, got %q", got.ReleaseTag)
	}
}

func TestMapDeploy_TriggerDefaultsToDeploy(t *testing.T) {
	created := time.Now().UTC()
	d := &github.Deployment{
		ID:        github.Int64(1),
		CreatedAt: &github.Timestamp{Time: created},
	}
	got := mapDeploy(d, "", "kmcd/foo", created)
	if got.Trigger != "deploy" {
		t.Errorf("Trigger default: got %q want %q", got.Trigger, "deploy")
	}
}

func TestMapDeployStatus(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"success", "success"},
		{"failure", "failed"},
		{"error", "failed"},
		{"in_progress", "in_progress"},
		{"queued", "in_progress"},
		{"pending", "in_progress"},
		{"waiting", "in_progress"},
		{"", "in_progress"},
		{"weird-unknown", "in_progress"},
	}
	for _, c := range cases {
		if got := mapDeployStatus(c.in); got != c.want {
			t.Errorf("mapDeployStatus(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSplitSlug(t *testing.T) {
	owner, name, err := splitSlug("kmcd/foo")
	if err != nil || owner != "kmcd" || name != "foo" {
		t.Errorf("splitSlug(kmcd/foo) = %q,%q,%v", owner, name, err)
	}
	if _, _, err := splitSlug("bogus"); err == nil {
		t.Errorf("splitSlug(bogus) expected error")
	}
	if _, _, err := splitSlug("/foo"); err == nil {
		t.Errorf("splitSlug(/foo) expected error")
	}
	if _, _, err := splitSlug("kmcd/"); err == nil {
		t.Errorf("splitSlug(kmcd/) expected error")
	}
}
