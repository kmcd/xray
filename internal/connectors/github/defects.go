package github

import (
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/kmcd/xray/internal/connector"
	"github.com/kmcd/xray/internal/model"
)

// Patterns per spec:
//   - <PREFIX>-<N>: prefix is at least two characters, first uppercase letter,
//     rest uppercase letters or digits, then hyphen, then positive integer.
//   - #<N>: # followed by positive integer, with a non-word character (or
//     start of input) on the left so things like foo#123 do NOT match.
var (
	defectPrefixRe = regexp.MustCompile(`\b([A-Z][A-Z0-9]+)-(\d+)\b`)
	defectHashRe   = regexp.MustCompile(`(?:^|\W)(#\d+)\b`)
)

// extractTicketRefs finds occurrences of <PREFIX>-<N> and #<N> in text and
// returns deduplicated refs in their original (PROJ-123 / #123) form. The
// returned slice preserves first-seen order.
func extractTicketRefs(text string) []string {
	if text == "" {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(ref string) {
		if ref == "" || seen[ref] {
			return
		}
		seen[ref] = true
		out = append(out, ref)
	}
	for _, m := range defectPrefixRe.FindAllStringSubmatchIndex(text, -1) {
		// m is [start,end, group1Start,group1End, group2Start,group2End].
		// The full match (text[m[0]:m[1]]) is the PROJ-123 form.
		add(text[m[0]:m[1]])
	}
	for _, m := range defectHashRe.FindAllStringSubmatchIndex(text, -1) {
		// Group 1 (#123) starts at m[2]; m[0] may include the preceding
		// non-word character.
		add(text[m[2]:m[3]])
	}
	return out
}

// emitDefects pushes one model.Defect per unique ticket ref found in text.
// id is a stable string built as fmt.Sprintf("%s:%s:%s:%s", repo, source,
// scopeID, ref). scopeID is the PR number (string) for PR sources or the
// commit SHA for commit_message; this keeps two PRs each referencing the
// same ticket as two distinct rows.
func emitDefects(
	sink connector.Sink,
	repo string,
	source string,
	scopeID string,
	text string,
	openedAt time.Time,
	closedAt *time.Time,
	prov *connector.Provenance,
) {
	for _, ref := range extractTicketRefs(text) {
		row := model.Defect{
			ID:        fmt.Sprintf("%s:%s:%s:%s", repo, source, scopeID, ref),
			Repo:      repo,
			TicketRef: ref,
			Source:    source,
			OpenedAt:  openedAt,
			ClosedAt:  closedAt,
		}
		if err := sink.InsertDefect(row); err != nil {
			if prov.Errors["defects"] == "" {
				prov.Errors["defects"] = err.Error()
			}
			continue
		}
		prov.RowsReturned["defects"]++
	}
}

// emitPRDefects collects unique ticket refs across a PR's title and body and
// emits one model.Defect per unique ref. Per ADR 019, refs that match both
// title and body emit a single row with source = "pr_title" (title wins);
// refs found only in the body get source = "pr_body". The ID stays stable as
// repo:source:prNumber:ref so two PRs referencing the same ticket remain two
// distinct rows.
func emitPRDefects(
	sink connector.Sink,
	repo string,
	prNumber int,
	title, body string,
	openedAt time.Time,
	closedAt *time.Time,
	prov *connector.Provenance,
) {
	titleRefs := extractTicketRefs(title)
	bodyRefs := extractTicketRefs(body)
	if len(titleRefs) == 0 && len(bodyRefs) == 0 {
		return
	}

	// Walk title first so title-matched refs win; then body refs not seen
	// in the title get pr_body. Preserves first-seen order within each
	// source (title before body).
	seen := make(map[string]bool, len(titleRefs)+len(bodyRefs))
	type entry struct {
		ref    string
		source string
	}
	ordered := make([]entry, 0, len(titleRefs)+len(bodyRefs))
	for _, ref := range titleRefs {
		if seen[ref] {
			continue
		}
		seen[ref] = true
		ordered = append(ordered, entry{ref: ref, source: "pr_title"})
	}
	for _, ref := range bodyRefs {
		if seen[ref] {
			continue
		}
		seen[ref] = true
		ordered = append(ordered, entry{ref: ref, source: "pr_body"})
	}

	scopeID := strconv.Itoa(prNumber)
	for _, e := range ordered {
		row := model.Defect{
			ID:        fmt.Sprintf("%s:%s:%s:%s", repo, e.source, scopeID, e.ref),
			Repo:      repo,
			TicketRef: e.ref,
			Source:    e.source,
			OpenedAt:  openedAt,
			ClosedAt:  closedAt,
		}
		if err := sink.InsertDefect(row); err != nil {
			if prov.Errors["defects"] == "" {
				prov.Errors["defects"] = err.Error()
			}
			continue
		}
		prov.RowsReturned["defects"]++
	}
}
