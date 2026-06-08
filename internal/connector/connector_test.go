package connector

import (
	"testing"
	"time"
)

func newProv(t *testing.T) Provenance {
	t.Helper()
	return NewProvenance("github", "kmcd/foo", Window{
		Start: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		End:   time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC),
	})
}

// TestProvenance_Merge_SumsRowsReturned: counters are summed across the
// fragments — useful when one goroutine emits prs rows and another emits
// commit rows, and we want the final prov to record both.
func TestProvenance_Merge_SumsRowsReturned(t *testing.T) {
	a := newProv(t)
	b := newProv(t)
	a.RowsReturned["commits"] = 12
	a.RowsReturned["prs"] = 3
	b.RowsReturned["prs"] = 7
	b.RowsReturned["reviews"] = 22

	a.Merge(b)

	if got := a.RowsReturned["commits"]; got != 12 {
		t.Errorf("commits = %d, want 12 (a's value preserved)", got)
	}
	if got := a.RowsReturned["prs"]; got != 10 {
		t.Errorf("prs = %d, want 10 (3 + 7 summed)", got)
	}
	if got := a.RowsReturned["reviews"]; got != 22 {
		t.Errorf("reviews = %d, want 22 (folded from b)", got)
	}
}

// TestProvenance_Merge_ErrorsFirstWins: when both fragments report the
// same context, p's existing entry wins. New contexts on other are folded.
func TestProvenance_Merge_ErrorsFirstWins(t *testing.T) {
	a := newProv(t)
	b := newProv(t)
	a.Errors["prs"] = "a's prs error"
	b.Errors["prs"] = "b's prs error"
	b.Errors["commits"] = "b's commits error"

	a.Merge(b)

	if got := a.Errors["prs"]; got != "a's prs error" {
		t.Errorf("Errors[prs] = %q, want first-wins (a)", got)
	}
	if got := a.Errors["commits"]; got != "b's commits error" {
		t.Errorf("Errors[commits] = %q, want b's value (new context)", got)
	}
}

// TestProvenance_Merge_PaginationANDed: any incomplete fragment makes the
// merged result incomplete.
func TestProvenance_Merge_PaginationANDed(t *testing.T) {
	t.Run("both_complete", func(t *testing.T) {
		a := newProv(t)
		b := newProv(t)
		a.Merge(b)
		if !a.PaginationComplete {
			t.Errorf("both complete should stay complete")
		}
	})
	t.Run("other_incomplete", func(t *testing.T) {
		a := newProv(t)
		b := newProv(t)
		b.PaginationComplete = false
		a.Merge(b)
		if a.PaginationComplete {
			t.Errorf("other incomplete should flip a to incomplete")
		}
	})
	t.Run("self_incomplete_preserved", func(t *testing.T) {
		a := newProv(t)
		b := newProv(t)
		a.PaginationComplete = false
		a.Merge(b)
		if a.PaginationComplete {
			t.Errorf("self incomplete should remain incomplete")
		}
	})
}

// TestProvenance_Merge_RateLimitORed: any truncation makes the merged
// result truncated.
func TestProvenance_Merge_RateLimitORed(t *testing.T) {
	a := newProv(t)
	b := newProv(t)
	b.RateLimitTruncated = true
	a.Merge(b)
	if !a.RateLimitTruncated {
		t.Errorf("expected truncated after merging a truncated fragment")
	}
}

// TestProvenance_Merge_EndpointsAndFlagsFirstWins: like Errors, existing
// entries on p win.
func TestProvenance_Merge_EndpointsAndFlagsFirstWins(t *testing.T) {
	a := newProv(t)
	b := newProv(t)
	a.Endpoints["branch_protection"] = EndpointStatus{Accessible: true}
	b.Endpoints["branch_protection"] = EndpointStatus{Accessible: false, Reason: "from b"}
	b.Endpoints["releases"] = EndpointStatus{Accessible: false, Reason: "release 403"}
	a.Flags["mailmap_applied"] = true
	b.Flags["mailmap_applied"] = false
	b.Flags["new_flag"] = true

	a.Merge(b)

	if !a.Endpoints["branch_protection"].Accessible {
		t.Errorf("endpoint should stay a's value")
	}
	if a.Endpoints["releases"].Reason != "release 403" {
		t.Errorf("new endpoint should be folded from b")
	}
	if !a.Flags["mailmap_applied"] {
		t.Errorf("flag should stay a's value")
	}
	if !a.Flags["new_flag"] {
		t.Errorf("new flag should be folded from b")
	}
}
