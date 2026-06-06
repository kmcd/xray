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
// returns CommitCoauthor rows with source="trailer". The handle stored is
// the hashed canonical identity after .mailmap resolution; bot-vs-human
// classification still runs against the pre-hash name+email so the kind
// signal isn't lost.
func trailerCoauthors(rec gitcli.CommitRecord, repo string, mm *gitcli.Mailmap) []model.CommitCoauthor {
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
		canonName, canonEmail := mm.Resolve(name, email)
		handle := hashHandle(canonicalCommitIdent(canonName, canonEmail))
		if handle == "" {
			continue
		}
		if seen[handle] {
			continue
		}
		seen[handle] = true
		out = append(out, model.CommitCoauthor{
			CommitSHA: rec.SHA,
			Repo:      repo,
			Handle:    handle,
			Source:    "trailer",
			Kind:      kindFor(name, email),
		})
	}
	return out
}

// committerDistinctCoauthor emits a source="api" coauthor row when the
// committer differs from the author. Tools, web UI, and bots commit under
// separate identities; this captures that signal without an API call.
// Mailmap resolution applies before hashing so a single human committing
// under multiple emails doesn't reappear as a distinct coauthor.
func committerDistinctCoauthor(rec gitcli.CommitRecord, repo string, mm *gitcli.Mailmap) (model.CommitCoauthor, bool) {
	authorName, authorEmail := mm.Resolve(rec.AuthorHandle, rec.AuthorEmail)
	committerName, committerEmail := mm.Resolve(rec.CommitterHandle, rec.CommitterEmail)
	if strings.EqualFold(authorName, committerName) &&
		strings.EqualFold(authorEmail, committerEmail) {
		return model.CommitCoauthor{}, false
	}
	if committerName == "" && committerEmail == "" {
		return model.CommitCoauthor{}, false
	}
	handle := hashHandle(canonicalCommitIdent(committerName, committerEmail))
	if handle == "" {
		return model.CommitCoauthor{}, false
	}
	return model.CommitCoauthor{
		CommitSHA: rec.SHA,
		Repo:      repo,
		Handle:    handle,
		Source:    "api",
		Kind:      kindFor(rec.CommitterHandle, rec.CommitterEmail),
	}, true
}
