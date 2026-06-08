package github

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/shurcooL/githubv4"

	"github.com/kmcd/xray/internal/config"
)

// newPureConnector builds a connector for the no-HTTP resolveMergeMethod
// tests. resolveMergeMethod takes its parent count from the inline GraphQL
// PR node, so the network is not exercised — we just need a logger.
func newPureConnector(t *testing.T) *Connector {
	t.Helper()
	c, err := New(config.GitHubConn{Token: "test-token"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestResolveMergeMethod_Unmerged(t *testing.T) {
	c := newPureConnector(t)
	if got := c.resolveMergeMethod(context.Background(), prGraph{}, "", nil); got != "" {
		t.Errorf("resolveMergeMethod unmerged = %q, want empty", got)
	}
}

func TestResolveMergeMethod_NoMergeSHAReturnsRebase(t *testing.T) {
	c := newPureConnector(t)
	now := githubv4.DateTime{Time: time.Now()}
	if got := c.resolveMergeMethod(context.Background(), prGraph{MergedAt: &now}, "", nil); got != "rebase" {
		t.Errorf("resolveMergeMethod no-merge-sha = %q, want rebase", got)
	}
}

func TestResolveMergeMethod_TwoParentsMerge(t *testing.T) {
	c := newPureConnector(t)
	now := githubv4.DateTime{Time: time.Now()}
	p := prGraph{MergedAt: &now}
	p.MergeCommit.Oid = "m2"
	p.MergeCommit.Parents.TotalCount = 2
	if got := c.resolveMergeMethod(context.Background(), p, "", []string{"oid1"}); got != "merge" {
		t.Errorf("resolveMergeMethod two-parents = %q, want merge", got)
	}
}

func TestResolveMergeMethod_NoCloneFallback(t *testing.T) {
	c := newPureConnector(t)
	now := githubv4.DateTime{Time: time.Now()}
	p := prGraph{MergedAt: &now}
	p.MergeCommit.Oid = "mergesha"
	p.MergeCommit.Parents.TotalCount = 1
	if got := c.resolveMergeMethod(context.Background(), p, "", []string{"oid1"}); got != "squash" {
		t.Errorf("resolveMergeMethod no-clone fallback = %q, want squash", got)
	}
}
