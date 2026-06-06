package github

import (
	"testing"

	"github.com/kmcd/xray/internal/gitcli"
)

func TestTrailerCoauthors(t *testing.T) {
	rec := gitcli.CommitRecord{
		SHA:  "abc",
		Body: "Some change.\n\nCo-authored-by: Alice <alice@example.com>\nCo-authored-by: Bob <bob@example.com>\nCo-authored-by: alice <alice@example.com>\n",
	}
	rows := trailerCoauthors(rec, "kmcd/foo", &gitcli.Mailmap{})
	if len(rows) != 2 {
		t.Fatalf("expected 2 deduped rows, got %d", len(rows))
	}
	for _, r := range rows {
		if r.Source != "trailer" {
			t.Errorf("source = %q, want trailer", r.Source)
		}
	}
}

func TestTrailerCoauthorsNone(t *testing.T) {
	rec := gitcli.CommitRecord{SHA: "x", Body: "no trailers here\n"}
	if got := trailerCoauthors(rec, "r", &gitcli.Mailmap{}); got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}

func TestCommitterDistinctCoauthor(t *testing.T) {
	t.Run("distinct", func(t *testing.T) {
		rec := gitcli.CommitRecord{
			SHA: "x", AuthorHandle: "alice", AuthorEmail: "a@x",
			CommitterHandle: "github-actions[bot]", CommitterEmail: "b@x",
		}
		row, ok := committerDistinctCoauthor(rec, "r", &gitcli.Mailmap{})
		if !ok {
			t.Fatal("expected ok=true")
		}
		if row.Source != "api" {
			t.Errorf("source = %q, want api", row.Source)
		}
	})
	t.Run("same_identity", func(t *testing.T) {
		rec := gitcli.CommitRecord{AuthorHandle: "alice", AuthorEmail: "a", CommitterHandle: "alice", CommitterEmail: "a"}
		if _, ok := committerDistinctCoauthor(rec, "r", &gitcli.Mailmap{}); ok {
			t.Errorf("expected ok=false for identical committer")
		}
	})
	t.Run("empty_committer", func(t *testing.T) {
		rec := gitcli.CommitRecord{AuthorHandle: "a", CommitterHandle: ""}
		if _, ok := committerDistinctCoauthor(rec, "r", &gitcli.Mailmap{}); ok {
			t.Errorf("expected ok=false for empty committer")
		}
	})
}
