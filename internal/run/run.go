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

	// 1. Clone each repo. Failures are recorded as provenance errors with
	// a synthetic connector entry per failed repo so the manifest still
	// records the failure.
	repos := cfg.AllRepos()
	clones := make([]cloned, 0, len(repos))
	cloneErrors := map[string]error{}

	for _, slug := range repos {
		dest := filepath.Join(tmpDir, "clones", sanitizeSlug(slug))
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return "", fmt.Errorf("run: mkdir clones: %w", err)
		}
		log.Info("run: cloning", slog.String("repo", slug))
		if err := git.Clone(ctx, slug, dest, cfg.Window.Start); err != nil {
			log.Error("run: clone failed", slog.String("repo", slug), slog.String("error", err.Error()))
			cloneErrors[slug] = err
			clones = append(clones, cloned{repo: connector.Repo{Slug: slug, Team: cfg.RepoToTeam(slug)}, err: err})
			continue
		}
		head, err := git.HeadSHA(ctx, dest)
		if err != nil {
			log.Error("run: head-sha failed", slog.String("repo", slug), slog.String("error", err.Error()))
			cloneErrors[slug] = err
			clones = append(clones, cloned{repo: connector.Repo{Slug: slug, Team: cfg.RepoToTeam(slug), Clone: dest}, err: err})
			continue
		}
		branch, _ := git.DefaultBranch(ctx, dest)
		clones = append(clones, cloned{
			repo: connector.Repo{
				Slug:          slug,
				DefaultBranch: branch,
				HeadSHA:       head,
				Team:          cfg.RepoToTeam(slug),
				Clone:         dest,
			},
		})
	}

	// 2. Open store.
	dbPath := filepath.Join(tmpDir, "metrics.sqlite")
	st, err := store.Open(dbPath, opts.ToolVersion)
	if err != nil {
		return "", fmt.Errorf("run: open store: %w", err)
	}

	// 3. Dispatch (repo, connector) pairs across worker pool. Each
	// connector returns a Provenance; we collect them all.
	win := connector.Window{Start: cfg.Window.Start, End: cfg.Window.End}
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

	// 4. Build manifest.
	completedAt := time.Now().UTC()
	m := &manifest.Manifest{
		ToolVersion:    opts.ToolVersion,
		SchemaVersion:  model.SchemaVersion,
		RunID:          runID,
		RunStartedAt:   startedAt,
		RunCompletedAt: completedAt,
		Window: manifest.WindowJSON{
			Start: cfg.Window.Start.UTC().Format("2006-01-02"),
			End:   cfg.Window.End.UTC().Format("2006-01-02"),
		},
		Teams:          cloneTeams(cfg.Teams),
		Repos:          buildRepoMetas(clones),
		ConnectorsUsed: derivedConnectorsUsed(provs),
		Counts:         sumCounts(provs),
		Provenance:     sortProvs(provs),
	}

	manifestPath := filepath.Join(tmpDir, "manifest.json")
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
