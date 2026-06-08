package run

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/kmcd/xray/internal/archive"
	"github.com/kmcd/xray/internal/config"
	"github.com/kmcd/xray/internal/connector"
	"github.com/kmcd/xray/internal/gitcli"
	"github.com/kmcd/xray/internal/manifest"
	"github.com/kmcd/xray/internal/model"
	"github.com/kmcd/xray/internal/postprocess"
	"github.com/kmcd/xray/internal/store"
)

// Run is the entry point for `xray run`. It clones every repo, dispatches
// every (repo, connector) pair across the worker pool, assembles the
// manifest, packages the artifact, and removes the temp dir (unless
// opts.KeepClones is set).
//
// Returns the absolute path of the produced .tar.gz. Errors are returned
// only when the run could not produce an artifact at all; per-connector
// failures are reported in the manifest's extraction_provenance and cause
// a non-nil error to be returned (so the CLI exits non-zero) while still
// completing artifact production.
func Run(ctx context.Context, cfg *config.Config, opts Options) (string, error) {
	if opts.Logger == nil {
		opts.Logger = NewLogger(false, false)
	}
	if opts.Workers <= 0 {
		opts.Workers = 4
	}
	log := opts.Logger

	runID := newRunID()
	startedAt := time.Now().UTC()

	tmpDir, err := os.MkdirTemp("", "xray-"+runID+"-")
	if err != nil {
		return "", fmt.Errorf("run: create temp dir: %w", err)
	}
	keep := opts.KeepClones
	defer func() {
		if !keep {
			_ = os.RemoveAll(tmpDir)
		}
	}()

	log.Info("run: start",
		slog.String("run_id", runID),
		slog.String("temp_dir", tmpDir),
		slog.Int("workers", opts.Workers),
	)

	git := &gitcli.Client{Log: log}

	// 1. Clone each repo in parallel. Each goroutine writes into its own
	// index slot so no mutex is needed on clones[]. Failures are recorded
	// as provenance errors with a synthetic connector entry per failed repo
	// so the manifest still records the failure.
	repos := cfg.AllRepos()
	clones := make([]cloned, len(repos))
	cloneErrors := map[string]error{}

	win := connector.Window{Start: cfg.Window.Start, End: cfg.Window.End}

	var cloneWg sync.WaitGroup
	for i, slug := range repos {
		dest := filepath.Join(tmpDir, "clones", sanitizeSlug(slug))
		if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
			clones[i] = cloned{repo: connector.Repo{Slug: slug, Team: cfg.RepoToTeam(slug)}, err: err}
			continue
		}
		cloneWg.Add(1)
		go func(i int, slug, dest string) {
			defer cloneWg.Done()
			log.Info("run: cloning", slog.String("repo", slug))
			clones[i] = cloneOneRepo(ctx, git, log, slug, dest, cfg, opts, win)
		}(i, slug, dest)
	}
	cloneWg.Wait()

	for _, c := range clones {
		if c.err != nil {
			cloneErrors[c.repo.Slug] = c.err
		}
	}

	// 2. Open store.
	dbPath := filepath.Join(tmpDir, "metrics.sqlite")
	st, err := store.Open(dbPath, opts.ToolVersion)
	if err != nil {
		return "", fmt.Errorf("run: open store: %w", err)
	}

	// 3. Dispatch (repo, connector) pairs across worker pool. Each
	// connector returns a Provenance; we collect them all.
	type job struct {
		repo connector.Repo
		conn connector.Connector
	}
	var jobs []job
	for _, c := range clones {
		if c.err != nil {
			continue
		}
		for _, conn := range opts.Connectors {
			jobs = append(jobs, job{repo: c.repo, conn: conn})
		}
	}

	var (
		provMu sync.Mutex
		provs  []connector.Provenance
	)
	addProv := func(p connector.Provenance) {
		provMu.Lock()
		provs = append(provs, p)
		provMu.Unlock()
	}

	// Synthetic provenance entries for clone failures so the run records
	// the failure even though no connector ran.
	for slug, e := range cloneErrors {
		p := connector.NewProvenance("clone", slug, win)
		p.Errors["clone"] = e.Error()
		p.PaginationComplete = false
		addProv(p)
	}

	if len(jobs) > 0 {
		ch := make(chan job)
		var wg sync.WaitGroup
		workers := opts.Workers
		if workers > len(jobs) {
			workers = len(jobs)
		}
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := range ch {
					select {
					case <-ctx.Done():
						return
					default:
					}
					log.Info("run: extracting",
						slog.String("repo", j.repo.Slug),
						slog.String("connector", j.conn.Name()),
					)
					p := j.conn.Extract(ctx, j.repo, win, st)
					addProv(p)
				}
			}()
		}
		for _, j := range jobs {
			select {
			case <-ctx.Done():
				close(ch)
				wg.Wait()
				_ = st.Close()
				return "", ctx.Err()
			case ch <- j:
			}
		}
		close(ch)
		wg.Wait()
	}

	// 3b. Cross-cutting post-extraction linkage. Errors here are recorded
	// as a synthetic "postprocess" provenance entry but do NOT abort the
	// run — the artifact still ships with whatever extraction produced.
	ppProv := connector.NewProvenance("postprocess", "", win)
	if stats, err := postprocess.Run(ctx, st.DB(), log); err != nil {
		log.Error("run: postprocess failed", slog.String("error", err.Error()))
		ppProv.Errors["postprocess"] = err.Error()
		ppProv.PaginationComplete = false
		addProv(ppProv)
	} else {
		log.Info("run: postprocess",
			slog.Int("incidents_linked", stats.IncidentsLinked),
			slog.Int("deploys_rolled_back", stats.DeploysRolledBack),
			slog.Int("landed_via_pr_matched", stats.LandedViaPRMatched),
		)
	}

	// 4. Build manifest.
	completedAt := time.Now().UTC()
	m := buildManifest(opts.ToolVersion, runID, startedAt, completedAt, cfg, clones, provs, st, log)

	manifestPath := filepath.Join(tmpDir, "manifest.json")
	// #nosec G304 -- manifestPath is under the per-run temp dir.
	mf, err := os.Create(manifestPath)
	if err != nil {
		_ = st.Close()
		return "", fmt.Errorf("run: create manifest: %w", err)
	}
	if _, err := m.WriteTo(mf); err != nil {
		_ = mf.Close()
		_ = st.Close()
		return "", fmt.Errorf("run: write manifest: %w", err)
	}
	if err := mf.Close(); err != nil {
		_ = st.Close()
		return "", fmt.Errorf("run: close manifest: %w", err)
	}
	if err := st.Close(); err != nil {
		return "", fmt.Errorf("run: close store: %w", err)
	}

	// 5. Archive.
	out := opts.Out
	if out == "" {
		out = fmt.Sprintf("./xray-export-%s.tar.gz", startedAt.Format("20060102T150405Z"))
	}
	absOut, err := filepath.Abs(out)
	if err != nil {
		return "", fmt.Errorf("run: resolve out path: %w", err)
	}
	if err := archive.WriteTarGz(absOut, map[string]string{
		dbPath:       "metrics.sqlite",
		manifestPath: "manifest.json",
	}); err != nil {
		return "", fmt.Errorf("run: write archive: %w", err)
	}
	log.Info("run: artifact", slog.String("path", absOut))

	// 6. Run exits non-zero iff any connector returned a non-empty Errors
	// map (or any repo failed to clone).
	if hasErrors(provs) {
		return absOut, errors.New("run: one or more connectors or clones reported errors; see manifest")
	}
	return absOut, nil
}

func sanitizeSlug(slug string) string {
	out := make([]byte, 0, len(slug))
	for i := 0; i < len(slug); i++ {
		c := slug[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == '.':
			out = append(out, c)
		default:
			out = append(out, '_')
		}
	}
	return string(out)
}

func cloneTeams(in map[string][]string) map[string][]string {
	out := make(map[string][]string, len(in))
	for k, v := range in {
		cp := make([]string, len(v))
		copy(cp, v)
		sort.Strings(cp)
		out[k] = cp
	}
	return out
}

type cloned struct {
	repo connector.Repo
	err  error
}

// cloneOneRepo clones slug into dest while concurrently firing each
// connector's optional Prefetch (#71). Joins the prefetch goroutines before
// returning so the workers pool downstream sees a consistent state.
// Any prefetch failures are logged but never propagated — Extract will
// fall back to a live fetch on a cache miss.
func cloneOneRepo(ctx context.Context, git *gitcli.Client, log *slog.Logger, slug, dest string, cfg *config.Config, opts Options, win connector.Window) cloned {
	var pfwg sync.WaitGroup
	for _, conn := range opts.Connectors {
		pf, ok := conn.(connector.Prefetcher)
		if !ok {
			continue
		}
		pfwg.Add(1)
		go func(pf connector.Prefetcher) {
			defer pfwg.Done()
			if err := pf.Prefetch(ctx, slug, win); err != nil {
				log.Warn("run: prefetch failed",
					slog.String("repo", slug),
					slog.String("error", err.Error()),
				)
			}
		}(pf)
	}

	var cloneErr error
	if err := git.Clone(ctx, slug, dest, cfg.Window.Start); err != nil {
		log.Error("run: clone failed", slog.String("repo", slug), slog.String("error", err.Error()))
		cloneErr = err
	}
	var head, branch string
	if cloneErr == nil {
		var err error
		if head, err = git.HeadSHA(ctx, dest); err != nil {
			log.Error("run: head-sha failed", slog.String("repo", slug), slog.String("error", err.Error()))
			cloneErr = err
		} else {
			branch, _ = git.DefaultBranch(ctx, dest)
		}
	}
	pfwg.Wait()

	if cloneErr != nil {
		repoRow := connector.Repo{Slug: slug, Team: cfg.RepoToTeam(slug)}
		if head != "" {
			repoRow.Clone = dest
			repoRow.HeadSHA = head
		}
		return cloned{repo: repoRow, err: cloneErr}
	}
	return cloned{
		repo: connector.Repo{
			Slug:          slug,
			DefaultBranch: branch,
			HeadSHA:       head,
			Team:          cfg.RepoToTeam(slug),
			Clone:         dest,
		},
	}
}

func buildRepoMetas(clones []cloned) []manifest.RepoMeta {
	metas := make([]manifest.RepoMeta, 0, len(clones))
	for _, c := range clones {
		metas = append(metas, manifest.RepoMeta{
			Slug:          c.repo.Slug,
			HeadSHA:       c.repo.HeadSHA,
			DefaultBranch: c.repo.DefaultBranch,
		})
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].Slug < metas[j].Slug })
	return metas
}

func derivedConnectorsUsed(provs []connector.Provenance) []string {
	seen := map[string]bool{}
	for _, p := range provs {
		if p.Connector == "clone" {
			continue
		}
		if len(p.RowsReturned) > 0 {
			seen[p.Connector] = true
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sumCounts(provs []connector.Provenance) map[string]int {
	out := map[string]int{}
	for _, p := range provs {
		for k, v := range p.RowsReturned {
			out[k] += v
		}
	}
	return out
}

func sortProvs(in []connector.Provenance) []connector.Provenance {
	out := make([]connector.Provenance, len(in))
	copy(out, in)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Repo != out[j].Repo {
			return out[i].Repo < out[j].Repo
		}
		return out[i].Connector < out[j].Connector
	})
	return out
}

func hasErrors(provs []connector.Provenance) bool {
	for _, p := range provs {
		if len(p.Errors) > 0 {
			return true
		}
	}
	return false
}

// buildManifest assembles the manifest from per-connector provenance and a
// post-extraction query against the store. SquashStats failure degrades to
// zero counts with a logged warning — assay reads squash_rate == 0 as "no
// caveat", which is the safe default if the rollup fails.
func buildManifest(
	toolVersion, runID string,
	startedAt, completedAt time.Time,
	cfg *config.Config,
	clones []cloned,
	provs []connector.Provenance,
	st *store.Store,
	log *slog.Logger,
) *manifest.Manifest {
	nSquash, nMerged, sqErr := st.SquashStats()
	if sqErr != nil {
		log.Warn("run: squash stats", slog.String("error", sqErr.Error()))
	}
	var squashRate float64
	if nMerged > 0 {
		squashRate = float64(nSquash) / float64(nMerged)
	}
	return &manifest.Manifest{
		ToolVersion:    toolVersion,
		SchemaVersion:  model.SchemaVersion,
		RunID:          runID,
		RunStartedAt:   startedAt,
		RunCompletedAt: completedAt,
		Window: manifest.WindowJSON{
			Start: cfg.Window.Start.UTC().Format("2006-01-02"),
			End:   cfg.Window.End.UTC().Format("2006-01-02"),
		},
		Teams:            cloneTeams(cfg.Teams),
		Repos:            buildRepoMetas(clones),
		ConnectorsUsed:   derivedConnectorsUsed(provs),
		Counts:           sumCounts(provs),
		MailmapApplied:   aggregateMailmapApplied(provs),
		NSquashMergedPRs: nSquash,
		NTotalMergedPRs:  nMerged,
		SquashRate:       squashRate,
		Provenance:       sortProvs(provs),
	}
}

// aggregateMailmapApplied collapses per-repo "mailmap_applied" flags into a
// single run-level boolean. The semantics mirror assay's expectation: true
// only when every repo that produced commit data also carried a parsed,
// non-empty .mailmap. Synthetic "clone" / "postprocess" provenance entries
// don't carry the flag and are skipped.
func aggregateMailmapApplied(provs []connector.Provenance) bool {
	saw := false
	for _, p := range provs {
		if p.Connector == "clone" || p.Connector == "postprocess" {
			continue
		}
		if _, ok := p.Flags["mailmap_applied"]; !ok {
			continue
		}
		saw = true
		if !p.Flags["mailmap_applied"] {
			return false
		}
	}
	return saw
}

// newRunID returns a sortable, opaque run identifier. We don't depend on a
// ULID library, so this is a millisecond timestamp + 8 bytes of randomness
// hex-encoded; sortable by time and unique enough for run identification.
func newRunID() string {
	var b [8]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		// Fall back to time-only on the vanishingly unlikely rand failure.
		return fmt.Sprintf("%013x", time.Now().UTC().UnixMilli())
	}
	return fmt.Sprintf("%013x%s", time.Now().UTC().UnixMilli(), hex.EncodeToString(b[:]))
}
