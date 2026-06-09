// Package main implements the xray CLI subcommands.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"

	"github.com/spf13/cobra"

	"github.com/kmcd/xray/internal/config"
	"github.com/kmcd/xray/internal/connector"
	xgithub "github.com/kmcd/xray/internal/connectors/github"
	"github.com/kmcd/xray/internal/gitcli"
	"github.com/kmcd/xray/internal/preflight"
	"github.com/kmcd/xray/internal/run"
)

type checkOpts struct {
	noCostPreview bool
}

func newCheckCmd() *cobra.Command {
	var opts checkOpts
	cmd := &cobra.Command{
		Use:   "check <config>",
		Short: "Live preflight against configured connectors and repos",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCheck(cmd, args[0], opts)
		},
	}
	cmd.Flags().BoolVar(&opts.noCostPreview, "no-cost-preview", false, "skip the cost-preview block for fast iteration")
	return cmd
}

// checkResult is the JSON shape emitted by `xray check --output json`.
type checkResult struct {
	Kind                  string                 `json:"kind"`
	OK                    bool                   `json:"ok"`
	Connectors            []checkConnector       `json:"connectors,omitempty"`
	Plan                  *checkPlan             `json:"plan,omitempty"`
	InaccessibleEndpoints []checkInaccessibleEnd `json:"inaccessible_endpoints,omitempty"`
}

type checkConnector struct {
	Name        string   `json:"name"`
	Scopes      []string `json:"scopes,omitempty"`
	ExtraScopes []string `json:"extra_scopes,omitempty"`
}

type checkPlan struct {
	Repos                     int      `json:"repos"`
	WindowDays                int      `json:"window_days"`
	Connectors                []string `json:"connectors"`
	EstimatedCloneBytes       int64    `json:"estimated_clone_bytes"`
	EstimatedAPICalls         int      `json:"estimated_api_calls"`
	EstimatedWallClockSeconds int      `json:"estimated_wall_clock_seconds"`
}

type checkInaccessibleEnd struct {
	Repo     string `json:"repo"`
	Endpoint string `json:"endpoint"`
	Reason   string `json:"reason"`
}

func runCheck(cmd *cobra.Command, path string, opts checkOpts) error {
	out := cmd.OutOrStdout()
	errOut := cmd.ErrOrStderr()

	mode, err := ResolveMode(flagOutput, flagQuiet)
	if err != nil {
		return silentCode(err, 1)
	}

	cfg, meta, err := config.Load(path)
	if err != nil {
		fmt.Fprintf(errOut, "%s: %v\n", path, err)
		return silentCode(errors.New("config load failed"), 1)
	}
	diags := config.Validate(cfg, meta, path)
	if len(diags) > 0 {
		for _, d := range diags {
			fmt.Fprintln(errOut, d.Error())
		}
		return silentCode(errors.New("config invalid"), 1)
	}

	res := checkResult{Kind: "check_summary", OK: true}
	textln(mode, out, "ok  config valid")

	anyFail := false
	if _, err := exec.LookPath("git"); err != nil {
		anyFail = true
		fmt.Fprintln(errOut, "FAIL git              not found on PATH")
	} else {
		textln(mode, out, "ok  git              on PATH")
	}

	logger := run.NewLogger(flagVerbose, flagQuiet || mode == ModeQuiet || mode == ModeJSON)
	conns, err := buildConnectors(cfg, logger)
	if err != nil {
		fmt.Fprintf(errOut, "connector setup: %v\n", err)
		return silentCode(err, 1)
	}
	ctx := cmd.Context()

	textln(mode, out, "")
	textln(mode, out, "read-only contract")
	for _, c := range conns {
		if err := c.Ping(ctx); err != nil {
			anyFail = true
			fmt.Fprintf(errOut, "FAIL %-16s %v\n", c.Name(), err)
			continue
		}
		entry := checkConnector{Name: c.Name()}
		if gc, ok := c.(*xgithub.Connector); ok {
			info, scopeErr := gc.Scopes(ctx)
			if scopeErr == nil {
				entry.Scopes = info.Granted
				entry.ExtraScopes = info.Extra
				printScopeBanner(mode, out, c.Name(), info)
			} else {
				textf(mode, out, "ok  %-16s authenticated (read-only; scopes unavailable)\n", c.Name())
			}
		} else {
			textf(mode, out, "ok  %-16s authenticated (read-only)\n", c.Name())
		}
		res.Connectors = append(res.Connectors, entry)
	}

	gitClient := &gitcli.Client{Log: logger}
	repos := cfg.AllRepos()
	sort.Strings(repos)

	textln(mode, out, "")
	textln(mode, out, "clone access")
	for _, r := range repos {
		if err := gitClient.LsRemote(ctx, r); err != nil {
			anyFail = true
			fmt.Fprintf(errOut, "FAIL %-16s %v\n", r, err)
		} else {
			textf(mode, out, "ok  %-16s clone access ok\n", r)
		}
	}

	// Block 2: cost preview. Gated on --no-cost-preview.
	if !opts.noCostPreview {
		plan, statErr := buildCostPreview(ctx, cfg, conns, repos)
		if statErr != nil {
			fmt.Fprintf(errOut, "cost-preview probe failed: %v\n", statErr)
		}
		printPlan(mode, out, plan)
		res.Plan = planToCheckPlan(plan)
	}

	// Block 3: inaccessible endpoint probe (always on).
	inacc := probeInaccessibleEndpoints(ctx, conns, repos)
	printInaccessible(mode, out, inacc)
	for _, e := range inacc {
		res.InaccessibleEndpoints = append(res.InaccessibleEndpoints, checkInaccessibleEnd{
			Repo: e.Repo, Endpoint: e.Endpoint, Reason: e.Reason,
		})
	}

	if anyFail {
		res.OK = false
		if mode == ModeJSON {
			_ = emitJSONLine(out, res)
		}
		return silentCode(errors.New("preflight failed"), 1)
	}
	if mode == ModeJSON {
		if err := emitJSONLine(out, res); err != nil {
			return silentCode(err, 1)
		}
	}
	return nil
}

func buildCostPreview(ctx context.Context, cfg *config.Config, conns []connector.Connector, repos []string) (preflight.Plan, error) {
	var stats []preflight.RepoStat
	var lastErr error
	for _, c := range conns {
		gc, ok := c.(*xgithub.Connector)
		if !ok {
			continue
		}
		s, err := gc.RepoStats(ctx, repos, cfg.Window.Start, cfg.Window.End)
		if err != nil {
			lastErr = err
		}
		stats = s
		break
	}
	return preflight.BuildPlan(cfg, stats), lastErr
}

func probeInaccessibleEndpoints(ctx context.Context, conns []connector.Connector, repos []string) []preflight.InaccessibleEndpoint {
	var out []preflight.InaccessibleEndpoint
	for _, c := range conns {
		gc, ok := c.(*xgithub.Connector)
		if !ok {
			continue
		}
		entries, _ := gc.ProbeEndpoints(ctx, repos)
		out = append(out, entries...)
	}
	return out
}

func printScopeBanner(mode Mode, w io.Writer, name string, info xgithub.ScopeInfo) {
	if mode == ModeQuiet || mode == ModeJSON {
		return
	}
	scopes := "none reported"
	if len(info.Granted) > 0 {
		scopes = joinComma(info.Granted)
	}
	fmt.Fprintf(w, "%-16s token scopes: %s\n", name, scopes)
	fmt.Fprintf(w, "               xray will call only read endpoints (assertion: no\n")
	fmt.Fprintf(w, "               Create/Update/Delete/Add/Remove methods invoked)\n")
	if len(info.Extra) > 0 {
		fmt.Fprintf(w, "               note: token has write scope; xray uses read calls only\n")
	}
}

func printPlan(mode Mode, w io.Writer, p preflight.Plan) {
	if mode == ModeQuiet || mode == ModeJSON {
		return
	}
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Plan")
	fmt.Fprintf(w, "  repos:      %d across %d teams\n", p.Repos, p.Teams)
	if !p.WindowStart.IsZero() && !p.WindowEnd.IsZero() {
		fmt.Fprintf(w, "  window:     %s..%s (%d days)\n",
			p.WindowStart.Format("2006-01-02"),
			p.WindowEnd.Format("2006-01-02"),
			p.WindowDays,
		)
	}
	fmt.Fprintf(w, "  connectors: %s\n", joinComma(p.Connectors))
	fmt.Fprintf(w, "  estimated:  ~%s clone, ~%s API calls, ~%s wall-clock\n",
		humanBytes(p.CloneBytes),
		humanCount(p.APICalls),
		humanSeconds(p.WallClockSecs),
	)
}

func printInaccessible(mode Mode, w io.Writer, entries []preflight.InaccessibleEndpoint) {
	if mode == ModeQuiet || mode == ModeJSON {
		return
	}
	if len(entries) == 0 {
		return
	}
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "permission-gated endpoints")
	for _, e := range entries {
		fmt.Fprintf(w, "  %-20s %-22s inaccessible (%s)\n", e.Repo, e.Endpoint, e.Reason)
	}
}

func planToCheckPlan(p preflight.Plan) *checkPlan {
	return &checkPlan{
		Repos:                     p.Repos,
		WindowDays:                p.WindowDays,
		Connectors:                p.Connectors,
		EstimatedCloneBytes:       p.CloneBytes,
		EstimatedAPICalls:         p.APICalls,
		EstimatedWallClockSeconds: p.WallClockSecs,
	}
}

// textln writes the line only in non-quiet, non-json modes.
func textln(mode Mode, w io.Writer, s string) {
	if mode == ModeQuiet || mode == ModeJSON {
		return
	}
	fmt.Fprintln(w, s)
}

func textf(mode Mode, w io.Writer, format string, args ...any) {
	if mode == ModeQuiet || mode == ModeJSON {
		return
	}
	fmt.Fprintf(w, format, args...)
}

// emitJSONLine writes one compact JSON object followed by a single \n.
func emitJSONLine(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

func joinComma(ss []string) string {
	if len(ss) == 0 {
		return ""
	}
	out := ss[0]
	for _, s := range ss[1:] {
		out += ", " + s
	}
	return out
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/float64(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func humanCount(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%dk", n/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func humanSeconds(s int) string {
	switch {
	case s >= 3600:
		return fmt.Sprintf("%d h %d min", s/3600, (s%3600)/60)
	case s >= 60:
		return fmt.Sprintf("%d min", s/60)
	default:
		return fmt.Sprintf("%d s", s)
	}
}
