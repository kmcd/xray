package run

import (
	"strings"
	"sync"
	"testing"
)

func TestInflightTracker_AddDoneSnapshot(t *testing.T) {
	tr := newInflightTracker()
	tr.add("kmcd/foo", "github")
	tr.add("kmcd/bar", "github")
	tr.add("kmcd/bar", "circleci")

	got := tr.snapshot()
	want := []InflightJob{
		{Repo: "kmcd/bar", Connector: "circleci"},
		{Repo: "kmcd/bar", Connector: "github"},
		{Repo: "kmcd/foo", Connector: "github"},
	}
	if len(got) != len(want) {
		t.Fatalf("snapshot len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("snapshot[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}

	tr.done("kmcd/bar", "github")
	got2 := tr.snapshot()
	if len(got2) != 2 {
		t.Fatalf("snapshot after done len = %d, want 2", len(got2))
	}
	for _, j := range got2 {
		if j.Repo == "kmcd/bar" && j.Connector == "github" {
			t.Errorf("done(kmcd/bar, github) still in snapshot")
		}
	}
}

func TestInflightTracker_EmptySnapshotIsNil(t *testing.T) {
	tr := newInflightTracker()
	if got := tr.snapshot(); got != nil {
		t.Errorf("empty snapshot = %v, want nil", got)
	}
}

func TestInflightTracker_ConcurrentAccess(t *testing.T) {
	tr := newInflightTracker()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			repo := "r"
			conn := "c"
			tr.add(repo, conn)
			_ = tr.snapshot()
			tr.done(repo, conn)
		}(i)
	}
	wg.Wait()
	if got := tr.snapshot(); len(got) != 0 {
		t.Errorf("after balanced add/done, snapshot = %v, want empty", got)
	}
}

func TestInterruptSummary_ExtractWithInflight(t *testing.T) {
	out := InterruptSummary(InterruptSummaryInput{
		Phase: "extract",
		Inflight: []InflightJob{
			{Repo: "kmcd/foo", Connector: "github"},
			{Repo: "kmcd/bar", Connector: "github"},
		},
		TempDir:  "/tmp/xray-abc",
		Cleaned:  true,
		ExitCode: 130,
	})
	for _, want := range []string{
		"Interrupted at phase 'extract' (kmcd/foo:github, kmcd/bar:github in flight).",
		"Cleaned up temp directory /tmp/xray-abc.",
		"No artifact produced. Re-run from scratch to retry; runs are non-incremental.",
		"Exit code: 130.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestInterruptSummary_ClonePhaseNoInflight(t *testing.T) {
	out := InterruptSummary(InterruptSummaryInput{
		Phase:    "clone",
		TempDir:  "/tmp/xray-abc",
		Cleaned:  true,
		ExitCode: 130,
	})
	if !strings.Contains(out, "Interrupted at phase 'clone'.") {
		t.Errorf("missing clone phase line in:\n%s", out)
	}
	if strings.Contains(out, "in flight") {
		t.Errorf("clone phase should not mention in flight:\n%s", out)
	}
}

func TestInterruptSummary_KeepClones(t *testing.T) {
	out := InterruptSummary(InterruptSummaryInput{
		Phase:    "extract",
		TempDir:  "/tmp/xray-abc",
		Cleaned:  false,
		ExitCode: 130,
	})
	if !strings.Contains(out, "Temp directory /tmp/xray-abc preserved (--keep-clones).") {
		t.Errorf("missing keep-clones preserved line in:\n%s", out)
	}
	if strings.Contains(out, "Cleaned up") {
		t.Errorf("Cleaned up shouldn't appear with Cleaned=false:\n%s", out)
	}
}

func TestInterruptSummary_PartialArtifact(t *testing.T) {
	out := InterruptSummary(InterruptSummaryInput{
		Phase:        "extract",
		TempDir:      "/tmp/xray-abc",
		Cleaned:      true,
		ArtifactPath: "/out/xray-export.tar.gz",
		ExitCode:     130,
	})
	for _, want := range []string{
		"Partial artifact written to /out/xray-export.tar.gz (run incomplete; manifest aborted=true).",
		"Re-run from scratch for a complete extraction; runs are non-incremental.",
		"Exit code: 130.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "No artifact produced") {
		t.Errorf("partial artifact should not print the no-artifact line:\n%s", out)
	}
}

func TestInterruptSummary_NoTempDir(t *testing.T) {
	out := InterruptSummary(InterruptSummaryInput{
		Phase:    "clone",
		ExitCode: 130,
	})
	if strings.Contains(out, "temp directory") {
		t.Errorf("no temp dir should yield no cleanup line:\n%s", out)
	}
	if !strings.Contains(out, "No artifact produced") {
		t.Errorf("missing no-artifact line:\n%s", out)
	}
}
