package github

import (
	"regexp"
	"strings"

	"github.com/kmcd/xray/internal/gitcli"
	"github.com/kmcd/xray/internal/model"
)

// coauthorTrailerRe matches the canonical "Co-authored-by: Name <email>"
// trailer. Case-insensitive; tolerant of leading whitespace.
var coauthorTrailerRe = regexp.MustCompile(`(?im)^\s*Co-authored-by:\s*([^<]+?)\s*<([^>]+)>\s*$`)

// trailerCoauthors parses Co-authored-by trailers from a commit body and
// returns CommitCoauthor rows with source="trailer". The "handle" stored
// is the trailer's "Name" component verbatim — there is no reliable
// mapping from email to GitHub handle without an API round-trip per
// trailer, which is too expensive at extract time.
func trailerCoauthors(rec gitcli.CommitRecord, repo string) []model.CommitCoauthor {
	matches := coauthorTrailerRe.FindAllStringSubmatch(rec.Body, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := make([]model.CommitCoauthor, 0, len(matches))
	for _, m := range matches {
		name := strings.TrimSpace(m[1])
		email := strings.TrimSpace(m[2])
		if name == "" {
			continue
		}
		key := strings.ToLower(name) + "|" + strings.ToLower(email)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, model.CommitCoauthor{
			CommitSHA: rec.SHA,
			Repo:      repo,
			Handle:    name,
			Source:    "trailer",
			Kind:      kindFor(name, email),
		})
	}
	return out
}

// committerDistinctCoauthor emits a source="api" coauthor row when the
// committer differs from the author. Tools, web UI, and bots commit under
// separate identities; this captures that signal without an API call.
func committerDistinctCoauthor(rec gitcli.CommitRecord, repo string) (model.CommitCoauthor, bool) {
	if strings.EqualFold(rec.AuthorHandle, rec.CommitterHandle) &&
		strings.EqualFold(rec.AuthorEmail, rec.CommitterEmail) {
		return model.CommitCoauthor{}, false
	}
	if rec.CommitterHandle == "" {
		return model.CommitCoauthor{}, false
	}
	return model.CommitCoauthor{
		CommitSHA: rec.SHA,
		Repo:      repo,
		Handle:    rec.CommitterHandle,
		Source:    "api",
		Kind:      kindFor(rec.CommitterHandle, rec.CommitterEmail),
	}, true
}
