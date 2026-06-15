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
	"github.com/kmcd/xray/internal/progress"
	"github.com/kmcd/xray/internal/store"
)

// ErrPartial signals a completed run that produced an artifact but where
// at least one connector or clone reported an error. The cmd-layer maps
// this to exit code 2 (artifact present, manifest records the failure);
// any other non-nil error from Run maps to exit code 3 (fatal).
var ErrPartial = errors.New("run: one or more connectors or clones reported errors; see manifest")

// Result is everything the CLI needs after a successful (or partial) run
// to render the post-run summary block (issue #84). ArtifactPath is always
// set when Run returns either nil or ErrPartial; the other fields are
// derived from the same data the manifest records.
//
// On graceful interrupt (Run returns context.Canceled), ArtifactPath is ""
// and the Interrupt* fields are populated so the cmd layer can render the
// stderr summary.
type Result struct {
	ArtifactPath string
	SHA256       string
	Size         int64
	Duration     time.Duration
	Manifest     manifest.Manifest

	// RateLimitWaits and RateLimitWaitSeconds are the cumulative wait
	// count and total wait time across every connector's ratelimit
	// transport, drained from the progress.RateLimitCounter that the CLI
	// tees alongside the user-facing sink. Populated by the cmd layer
	// after Run returns (Run itself does not aggregate the counter).
	RateLimitWaits       int
	RateLimitWaitSeconds int

	// Interrupted is true iff Run returned because ctx was canceled.
	Interrupted      bool
	InterruptedPhase string        // "clone", "extract", "postprocess"
	InflightJobs     []InflightJob // populated only during "extract" cancel
	TempDir          string        // absolute path; non-empty whenever cleanup ran (or was skipped via KeepClones)
}

// InflightJob is one (repo, connector) pair that was being extracted when
// ctx was canceled.
type InflightJob struct {
	Repo      string
	Connector string
}

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
func Run(ctx context.Context, cfg *config.Config, opts Options) (Result, error) {
	if opts.Logger == nil {
		opts.Logger = NewLogger(false, false)
	}
	if opts.Workers <= 0 {
		opts.Workers = 4
	}
	if opts.Progress == nil {
		opts.Progress = progress.NopSink{}
	}
	log := opts.Logger
	sink := opts.Progress

	runID := newRunID()
	startedAt := time.Now().UTC()

	tmpDir, err := os.MkdirTemp("", "xray-"+runID+"-")
	if err != nil {
		return Result{}, fmt.Errorf("run: create temp dir: %w", err)
	}
	if opts.OnTempDir != nil {
		opts.OnTempDir(tmpDir)
	}
	keep := opts.KeepClones
	defer func() {
		if !keep {
			_ = os.RemoveAll(tmpDir)
		}
	}()

	inflight := newInflightTracker()

	log.Info("run: start",
		slog.String("run_id", runID),
		slog.String("temp_dir", tmpDir),
		slog.Int("workers", opts.Workers),
	)

	git := &gitcli.Client{Log: log, GitHubToken: cfg.GitHubToken()}
	win := connector.Window{Start: cfg.Window.Start, End: cfg.Window.End}

	// 1. Open store before workers start so extract goroutines can write rows.
	dbPath := filepath.Join(tmpDir, "metrics.sqlite")
	st, err := store.Open(dbPath, opts.ToolVersion)
	if err != nil {
		return Result{}, fmt.Errorf("run: open store: %w", err)
	}

	type job struct {
		repo connector.Repo
		conn connector.Connector
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

	// 2. Start the extract worker pool. Workers block on jobCh until jobs
	// arrive; the channel is closed after all clones have been processed.
	jobCh := make(chan job)
	var extractWg sync.WaitGroup
	for i := 0; i < opts.Workers; i++ {
		extractWg.Add(1)
		go func() {
			defer extractWg.Done()
			for j := range jobCh {
				select {
				case <-ctx.Done():
					return
				default:
				}
				log.Info("run: extracting",
					slog.String("repo", j.repo.Slug),
					slog.String("connector", j.conn.Name()),
				)
				sink.Emit(progress.Event{
					Kind: progress.PhaseStart, Repo: j.repo.Slug, Connector: j.conn.Name(), Phase: j.conn.Name(), At: time.Now().UTC(),
				})
				inflight.add(j.repo.Slug, j.conn.Name())
				p := j.conn.Extract(ctx, j.repo, win, st)
				inflight.done(j.repo.Slug, j.conn.Name())
				addProv(p)
				emitExtractResult(sink, j.repo.Slug, j.conn.Name(), p)
			}
		}()
	}

	// 3. Clone each repo in parallel. As each clone completes, route it
	// directly to the worker pool rather than waiting for all clones to
	// finish first. Prefetch goroutines (started inside cloneOneRepo) run
	// concurrently with extract; connectors block on their done channel only
	// when they actually need the prefetch result.
	var clones []cloned
	extractStarted := false
	cloneCh := streamClones(ctx, cfg, opts, log, sink, tmpDir, git, win)
	for c := range cloneCh {
		clones = append(clones, c)
		if c.err != nil {
			// Synthetic provenance so the manifest records the failure even
			// though no connector ran for this repo.
			p := connector.NewProvenance("clone", c.repo.Slug, win)
			p.Errors["clone"] = c.err.Error()
			p.PaginationComplete = false
			addProv(p)
			continue
		}
		for _, conn := range opts.Connectors {
			select {
			case <-ctx.Done():
				// Snapshot inflight before signalling workers to stop; after
				// Wait() all done() calls have fired and the snapshot is empty.
				snap := inflight.snapshot()
				// Drain remaining clone results so their goroutines can exit.
				go func() {
					for range cloneCh {
					}
				}()
				close(jobCh)
				extractWg.Wait()
				phase := "extract"
				if !extractStarted {
					phase = "clone"
				}
				_ = st.Close()
				return interruptedResult(tmpDir, phase, snap), ctx.Err()
			case jobCh <- job{repo: c.repo, conn: conn}:
				extractStarted = true
			}
		}
	}
	// Snapshot inflight before signalling workers to stop; after Wait() all
	// done() calls have fired and the snapshot would always be empty.
	snap := inflight.snapshot()
	close(jobCh)
	extractWg.Wait()

	if err := ctx.Err(); err != nil {
		phase := "extract"
		if !extractStarted {
			phase = "clone"
		}
		_ = st.Close()
		return interruptedResult(tmpDir, phase, snap), err
	}

	addProv(runPostprocess(ctx, st, log, sink, win))

	if err := ctx.Err(); err != nil {
		_ = st.Close()
		return interruptedResult(tmpDir, "postprocess", nil), err
	}

	// 4. Build manifest.
	completedAt := time.Now().UTC()
	m := buildManifest(opts.ToolVersion, runID, startedAt, completedAt, cfg, clones, provs, st, log)
	manifestPath, err := writeManifestAndCloseStore(tmpDir, m, st)
	if err != nil {
		return Result{}, err
	}

	// 5. Archive.
	out := opts.Out
	if out == "" {
		out = fmt.Sprintf("./xray-export-%s.tar.gz", startedAt.Format("20060102T150405Z"))
	}
	absOut, err := filepath.Abs(out)
	if err != nil {
		return Result{}, fmt.Errorf("run: resolve out path: %w", err)
	}
	ar, err := archive.WriteTarGz(absOut, map[string]string{
		dbPath:       "metrics.sqlite",
		manifestPath: "manifest.json",
	})
	if err != nil {
		return Result{}, fmt.Errorf("run: write archive: %w", err)
	}
	log.Info("run: artifact",
		slog.String("path", absOut),
		slog.Int64("size", ar.Size),
		slog.String("sha256", ar.SHA256),
	)

	res := Result{
		ArtifactPath: absOut,
		SHA256:       ar.SHA256,
		Size:         ar.Size,
		Duration:     completedAt.Sub(startedAt),
		Manifest:     *m,
	}

	// 6. Run exits non-zero iff any connector returned a non-empty Errors
	// map (or any repo failed to clone).
	if hasErrors(provs) {
		return res, ErrPartial
	}
	return res, nil
}

// writeManifestAndCloseStore serialises the manifest under tmpDir and
// closes the SQLite store. Errors are wrapped with the "run:" boundary
// prefix; the store is always closed even when manifest write fails so
// no SQLite file handle leaks if archive packaging is then skipped.
func writeManifestAndCloseStore(tmpDir string, m *manifest.Manifest, st *store.Store) (string, error) {
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
	return manifestPath, nil
}

// streamClones fans out one goroutine per repo to clone in parallel. Each
// goroutine sends its result to the returned channel as soon as the git clone
// (and HeadSHA/DefaultBranch) completes; Prefetch for that repo continues in
// background goroutines started inside cloneOneRepo. The channel is closed
// after all repos have completed or failed.
func streamClones(
	ctx context.Context,
	cfg *config.Config,
	opts Options,
	log *slog.Logger,
	sink progress.Sink,
	tmpDir string,
	git *gitcli.Client,
	win connector.Window,
) <-chan cloned {
	repos := cfg.AllRepos()
	ch := make(chan cloned, len(repos))

	var wg sync.WaitGroup
	for _, slug := range repos {
		dest := filepath.Join(tmpDir, "clones", sanitizeSlug(slug))
		if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
			ch <- cloned{repo: connector.Repo{Slug: slug, Team: cfg.RepoToTeam(slug)}, err: err}
			continue
		}
		wg.Add(1)
		go func(slug, dest string) {
			defer wg.Done()
			log.Info("run: cloning", slog.String("repo", slug))
			sink.Emit(progress.Event{
				Kind: progress.PhaseStart, Repo: slug, Connector: "clone", Phase: "clone", At: time.Now().UTC(),
			})
			c := cloneOneRepo(ctx, git, log, slug, dest, cfg, opts, win)
			ev := progress.Event{Repo: slug, Connector: "clone", Phase: "clone", At: time.Now().UTC()}
			if c.err != nil {
				ev.Kind = progress.PhaseError
				ev.Message = c.err.Error()
			} else {
				ev.Kind = progress.PhaseDone
			}
			sink.Emit(ev)
			ch <- c
		}(slug, dest)
	}

	go func() {
		wg.Wait()
		close(ch)
	}()

	return ch
}

// runPostprocess runs cross-cutting post-extraction linkage. Errors are
// recorded as a synthetic "postprocess" provenance entry but do NOT abort
// the run — the artifact still ships with whatever extraction produced.
func runPostprocess(ctx context.Context, st *store.Store, log *slog.Logger, sink progress.Sink, win connector.Window) connector.Provenance {
	p := connector.NewProvenance("postprocess", "", win)
	sink.Emit(progress.Event{
		Kind: progress.PhaseStart, Phase: "postprocess", Message: "postprocess: linking", At: time.Now().UTC(),
	})
	stats, err := postprocess.Run(ctx, st.DB(), log)
	if err != nil {
		log.Error("run: postprocess failed", slog.String("error", err.Error()))
		p.Errors["postprocess"] = err.Error()
		p.PaginationComplete = false
		sink.Emit(progress.Event{
			Kind: progress.PhaseError, Phase: "postprocess", Message: err.Error(), At: time.Now().UTC(),
		})
		return p
	}
	log.Info("run: postprocess",
		slog.Int("incidents_linked", stats.IncidentsLinked),
		slog.Int("deploys_rolled_back", stats.DeploysRolledBack),
		slog.Int("landed_via_pr_matched", stats.LandedViaPRMatched),
	)
	sink.Emit(progress.Event{
		Kind: progress.PhaseDone, Phase: "postprocess", At: time.Now().UTC(),
	})
	return p
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
// connector's optional Prefetch (#71). Prefetch goroutines run independently
// and are not joined before returning — the connector's consumePRPrefetch
// blocks on the done channel when it actually needs the result during Extract.
// Any prefetch failures are logged but never propagated — Extract will
// fall back to a live fetch on a cache miss.
func cloneOneRepo(ctx context.Context, git *gitcli.Client, log *slog.Logger, slug, dest string, cfg *config.Config, opts Options, win connector.Window) cloned {
	for _, conn := range opts.Connectors {
		pf, ok := conn.(connector.Prefetcher)
		if !ok {
			continue
		}
		go func(pf connector.Prefetcher) {
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
		// One subprocess resolves both HEAD's SHA and the default branch;
		// at N repos the saved fork+exec compounds noticeably.
		if head, branch, err = git.HeadAndDefaultBranch(ctx, dest); err != nil {
			log.Error("run: head-sha failed", slog.String("repo", slug), slog.String("error", err.Error()))
			cloneErr = err
		}
	}

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

// emitExtractResult translates a returned Provenance into the
// PhaseDone / PhaseError / PhaseSkipped event the sink expects. The
// classifier mirrors the manifest's existing logic: any populated
// Errors map → PhaseError; otherwise an EndpointStatus with
// Accessible=false at the top level → PhaseSkipped; otherwise
// PhaseDone with the summed row count.
func emitExtractResult(sink progress.Sink, repo, conn string, p connector.Provenance) {
	at := time.Now().UTC()
	if len(p.Errors) > 0 {
		sink.Emit(progress.Event{
			Kind: progress.PhaseError, Repo: repo, Connector: conn, Phase: conn,
			Message: firstErrorMessage(p.Errors), At: at,
		})
		return
	}
	if anyEndpointInaccessible(p) {
		sink.Emit(progress.Event{
			Kind: progress.PhaseSkipped, Repo: repo, Connector: conn, Phase: conn, At: at,
		})
		return
	}
	var rows int64
	for _, n := range p.RowsReturned {
		rows += int64(n)
	}
	sink.Emit(progress.Event{
		Kind: progress.PhaseDone, Repo: repo, Connector: conn, Phase: conn,
		Done: rows, At: at,
	})
}

// firstErrorMessage returns the value at the lexicographically first key in
// errs. Map iteration in Go is unordered, so picking by sorted key keeps the
// rendered message stable across reruns of the same provenance.
func firstErrorMessage(errs map[string]string) string {
	if len(errs) == 0 {
		return ""
	}
	keys := make([]string, 0, len(errs))
	for k := range errs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return errs[keys[0]]
}

func anyEndpointInaccessible(p connector.Provenance) bool {
	if len(p.RowsReturned) > 0 {
		// Connector produced rows — even if some endpoints were
		// permission-gated, the overall pair counts as done.
		return false
	}
	for _, st := range p.Endpoints {
		if !st.Accessible {
			return true
		}
	}
	return false
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
