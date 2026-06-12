package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/go-github/v66/github"
	"github.com/spf13/cobra"
	"github.com/kmcd/xray/internal/ratelimit"
)

// Base URLs for connector probe HTTP calls. Package-level variables so tests
// can redirect them to httptest.Server instances without touching production
// addresses.
var (
	probeCCIBaseURL       = "https://circleci.com/api/v1.1"
	probeBugsnagBaseURL   = "https://api.bugsnag.com"
	probeHoneycombBaseURL = "https://api.honeycomb.io/1"
	probeSentryBaseURL    = "https://sentry.io/api/0"
)

// ---------- result types ----------

type probeResult struct {
	Org       string
	GitHub    ghProbe
	CircleCI  cciProbe
	Bugsnag   bnProbe
	Honeycomb hcProbe
	Sentry    sentryProbe
}

type ghProbe struct {
	Scopes      []string
	TotalRepos  int
	ActiveRepos []string // not archived, pushed in last 90d
	Skipped     bool
	SkipReason  string
}

type cciProbe struct {
	Followed   []cciFollowed
	Skipped    bool
	SkipReason string
}

type cciFollowed struct {
	CCISlug  string // gh/org/repo
	RepoSlug string // org/repo
}

type bnProbe struct {
	OrgName    string
	Projects   []bnProject
	Matches    []bnMatch
	Unmatched  []bnProject
	Skipped    bool
	SkipReason string
}

type bnProject struct {
	ID   string
	Name string
}

type bnMatch struct {
	Project    bnProject
	RepoSlug   string
	Confidence string // "high" or "medium"
}

type hcProbe struct {
	Datasets    []hcDataset
	Attribution []hcAttr
	NoMarkers   []string // active repos with no markers in any dataset
	Skipped     bool
	SkipReason  string
}

type hcDataset struct {
	Name string
	Slug string
}

type hcAttr struct {
	Dataset     string
	RepoSlug    string
	MarkerCount int
}

type sentryProbe struct {
	OrgSlug    string
	Projects   []sentryProject
	Matches    []sentryMatch
	Unmatched  []sentryProject
	Skipped    bool
	SkipReason string
}

type sentryProject struct {
	Slug string
	Name string
}

type sentryMatch struct {
	Project    sentryProject
	RepoSlug   string
	Confidence string
}

// ---------- entry point ----------

func runProbe(cmd *cobra.Command, opts initOpts) error {
	out := cmd.OutOrStdout()
	ctx := cmd.Context()

	githubToken := opts.token
	if githubToken == "" {
		githubToken = os.Getenv("GITHUB_TOKEN")
	}
	if githubToken == "" {
		return fmt.Errorf("no GitHub token provided (set --token or $GITHUB_TOKEN)")
	}

	honeycombToken := os.Getenv("HC_API_KEY")
	if honeycombToken == "" {
		honeycombToken = os.Getenv("HONEYCOMB_API_KEY")
	}
	bugsnagToken := os.Getenv("BUGSNAG_AUTH_TOKEN")
	circleciToken := os.Getenv("CIRCLECI_TOKEN")
	sentryToken := os.Getenv("SENTRY_AUTH_TOKEN")

	rl := &ratelimit.Transport{
		Base:   http.DefaultTransport,
		Policy: ratelimit.DefaultPolicy(),
		Log:    slog.Default(),
	}
	httpClient := &http.Client{Transport: rl}

	result := probeResult{Org: opts.org}

	fmt.Fprintf(out, "Probing GitHub...\n")
	result.GitHub = probeGitHub(ctx, githubToken, opts.org)
	printGitHubProbe(out, result.GitHub)

	if circleciToken != "" {
		fmt.Fprintf(out, "\nProbing CircleCI...\n")
		result.CircleCI = probeCCI(ctx, httpClient, circleciToken, opts.org)
		printCCIProbe(out, result.CircleCI)
	} else {
		fmt.Fprintf(out, "\nSkipping CircleCI (CIRCLECI_TOKEN not set)\n")
		result.CircleCI = cciProbe{Skipped: true, SkipReason: "CIRCLECI_TOKEN not set"}
	}

	if bugsnagToken != "" {
		fmt.Fprintf(out, "\nProbing Bugsnag...\n")
		result.Bugsnag = probeBugsnag(ctx, httpClient, bugsnagToken, result.GitHub.ActiveRepos)
		printBugsnagProbe(out, result.Bugsnag)
	} else {
		fmt.Fprintf(out, "\nSkipping Bugsnag (BUGSNAG_AUTH_TOKEN not set)\n")
		result.Bugsnag = bnProbe{Skipped: true, SkipReason: "BUGSNAG_AUTH_TOKEN not set"}
	}

	if honeycombToken != "" {
		fmt.Fprintf(out, "\nProbing Honeycomb...\n")
		result.Honeycomb = probeHoneycomb(ctx, httpClient, honeycombToken, opts.org, result.GitHub.ActiveRepos)
		printHoneycombProbe(out, result.Honeycomb, opts.org)
	} else {
		fmt.Fprintf(out, "\nSkipping Honeycomb (HC_API_KEY / HONEYCOMB_API_KEY not set)\n")
		result.Honeycomb = hcProbe{Skipped: true, SkipReason: "HC_API_KEY not set"}
	}

	if sentryToken != "" {
		fmt.Fprintf(out, "\nProbing Sentry...\n")
		result.Sentry = probeSentry(ctx, httpClient, sentryToken, result.GitHub.ActiveRepos)
		printSentryProbe(out, result.Sentry)
	} else {
		fmt.Fprintf(out, "\nSkipping Sentry (SENTRY_AUTH_TOKEN not set)\n")
		result.Sentry = sentryProbe{Skipped: true, SkipReason: "SENTRY_AUTH_TOKEN not set"}
	}

	if _, statErr := os.Stat(opts.out); statErr == nil && !opts.force {
		return fmt.Errorf("%s already exists (pass --force to overwrite)", opts.out)
	}
	body, err := renderProbeDraft(result)
	if err != nil {
		return err
	}
	// #nosec G703 -- opts.out is the user-supplied --out path.
	if err := os.WriteFile(opts.out, []byte(body), 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", opts.out, err)
	}
	fmt.Fprintf(out, "\nWrote %s — review before running `xray run`.\n", opts.out)
	return nil
}

// ---------- GitHub probe ----------

func probeGitHub(ctx context.Context, token, org string) ghProbe {
	client := newGitHubClient(ctx, token)

	var scopes []string
	_, resp, err := client.Users.Get(ctx, "")
	if err == nil && resp != nil {
		if s := resp.Header.Get("X-OAuth-Scopes"); s != "" {
			for _, sc := range strings.Split(s, ",") {
				if sc = strings.TrimSpace(sc); sc != "" {
					scopes = append(scopes, sc)
				}
			}
		}
	}

	cutoff := time.Now().UTC().AddDate(0, 0, -90)
	opt := &github.RepositoryListByOrgOptions{
		Type:        "sources",
		ListOptions: github.ListOptions{PerPage: 100},
	}
	var total int
	var active []string
	for {
		repos, ghResp, listErr := client.Repositories.ListByOrg(ctx, org, opt)
		if listErr != nil {
			break
		}
		for _, r := range repos {
			if r == nil {
				continue
			}
			total++
			if r.GetArchived() {
				continue
			}
			if pushedAt := r.GetPushedAt(); !pushedAt.IsZero() && pushedAt.Before(cutoff) {
				continue
			}
			active = append(active, r.GetFullName())
		}
		if ghResp == nil || ghResp.NextPage == 0 {
			break
		}
		opt.Page = ghResp.NextPage
	}
	sort.Strings(active)
	return ghProbe{Scopes: scopes, TotalRepos: total, ActiveRepos: active}
}

// ---------- CircleCI probe ----------

func probeCCI(ctx context.Context, client *http.Client, token, org string) cciProbe {
	type cciV11Project struct {
		VCSType  string `json:"vcs_type"` // "github", "bitbucket", etc.
		Username string `json:"username"`
		RepoName string `json:"reponame"`
	}

	_, body, err := probeJSONGET(ctx, client, probeCCIBaseURL+"/projects", map[string]string{
		"Circle-Token": token,
	})
	if err != nil {
		return cciProbe{Skipped: true, SkipReason: err.Error()}
	}

	var raw []cciV11Project
	if err := json.Unmarshal(body, &raw); err != nil {
		return cciProbe{Skipped: true, SkipReason: "parse error: " + err.Error()}
	}

	var followed []cciFollowed
	for _, p := range raw {
		if !strings.EqualFold(p.VCSType, "github") {
			continue
		}
		if !strings.EqualFold(p.Username, org) {
			continue
		}
		followed = append(followed, cciFollowed{
			CCISlug:  "gh/" + p.Username + "/" + p.RepoName,
			RepoSlug: p.Username + "/" + p.RepoName,
		})
	}
	sort.Slice(followed, func(i, j int) bool {
		return followed[i].RepoSlug < followed[j].RepoSlug
	})
	return cciProbe{Followed: followed}
}

// ---------- Bugsnag probe ----------

func probeBugsnag(ctx context.Context, client *http.Client, token string, repos []string) bnProbe {
	hdrs := map[string]string{
		"Authorization": "token " + token,
		"X-Version":     "2",
	}

	type bnOrg struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	_, body, err := probeJSONGET(ctx, client, probeBugsnagBaseURL+"/organizations", hdrs)
	if err != nil {
		return bnProbe{Skipped: true, SkipReason: err.Error()}
	}
	var orgs []bnOrg
	if err := json.Unmarshal(body, &orgs); err != nil || len(orgs) == 0 {
		return bnProbe{Skipped: true, SkipReason: "no organizations found"}
	}
	org := orgs[0]

	type bnAPIProject struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	var allProjects []bnAPIProject
	next := fmt.Sprintf("%s/organizations/%s/projects?per_page=100", probeBugsnagBaseURL, org.ID)
	for next != "" {
		respHdrs, pageBody, pageErr := probeJSONGET(ctx, client, next, hdrs)
		if pageErr != nil {
			break
		}
		var page []bnAPIProject
		if err := json.Unmarshal(pageBody, &page); err != nil {
			break
		}
		allProjects = append(allProjects, page...)
		next = probeLinkNext(respHdrs.Get("Link"))
	}

	projects := make([]bnProject, len(allProjects))
	for i, p := range allProjects {
		projects[i] = bnProject(p)
	}

	var matches []bnMatch
	var unmatched []bnProject
	for _, p := range projects {
		repoSlug, conf := matchRepo(p.Name, repos)
		if repoSlug != "" {
			matches = append(matches, bnMatch{Project: p, RepoSlug: repoSlug, Confidence: conf})
		} else {
			unmatched = append(unmatched, p)
		}
	}
	return bnProbe{OrgName: org.Name, Projects: projects, Matches: matches, Unmatched: unmatched}
}

// ---------- Honeycomb probe ----------

var githubCommitRE = regexp.MustCompile(`github\.com/([^/?#\s]+/[^/?#\s]+)`)

func probeHoneycomb(ctx context.Context, client *http.Client, token, org string, repos []string) hcProbe {
	hdrs := map[string]string{"X-Honeycomb-Team": token}

	type hcAPIDataset struct {
		Name string `json:"name"`
		Slug string `json:"slug"`
	}
	_, body, err := probeJSONGET(ctx, client, probeHoneycombBaseURL+"/datasets", hdrs)
	if err != nil {
		return hcProbe{Skipped: true, SkipReason: err.Error()}
	}
	var apiDatasets []hcAPIDataset
	if err := json.Unmarshal(body, &apiDatasets); err != nil {
		return hcProbe{Skipped: true, SkipReason: "parse error: " + err.Error()}
	}

	datasets := make([]hcDataset, len(apiDatasets))
	for i, d := range apiDatasets {
		datasets[i] = hcDataset(d)
	}

	type hcAPIMarker struct {
		URL string `json:"url"`
	}

	// attribution: dataset slug → repo slug → count
	attrMap := make(map[string]map[string]int)
	orgPrefix := strings.ToLower(org) + "/"

	for _, ds := range datasets {
		u := probeHoneycombBaseURL + "/markers/" + url.PathEscape(ds.Slug)
		_, mBody, mErr := probeJSONGET(ctx, client, u, hdrs)
		if mErr != nil {
			continue
		}
		var markers []hcAPIMarker
		if err := json.Unmarshal(mBody, &markers); err != nil {
			continue
		}
		for _, m := range markers {
			sm := githubCommitRE.FindStringSubmatch(m.URL)
			if sm == nil {
				continue
			}
			// Normalise to lowercase: GitHub repo slugs are case-insensitive
			// and ActiveRepos uses the canonical lowercase form from the API.
			slug := strings.ToLower(sm[1])
			if !strings.HasPrefix(slug, orgPrefix) {
				continue
			}
			if attrMap[ds.Slug] == nil {
				attrMap[ds.Slug] = make(map[string]int)
			}
			attrMap[ds.Slug][slug]++
		}
	}

	var attrs []hcAttr
	covered := make(map[string]bool)
	for ds, repoMap := range attrMap {
		for repo, cnt := range repoMap {
			attrs = append(attrs, hcAttr{Dataset: ds, RepoSlug: repo, MarkerCount: cnt})
			covered[repo] = true
		}
	}
	sort.Slice(attrs, func(i, j int) bool {
		if attrs[i].Dataset != attrs[j].Dataset {
			return attrs[i].Dataset < attrs[j].Dataset
		}
		return attrs[i].MarkerCount > attrs[j].MarkerCount
	})

	var noMarkers []string
	for _, r := range repos {
		if !covered[r] {
			noMarkers = append(noMarkers, r)
		}
	}
	return hcProbe{Datasets: datasets, Attribution: attrs, NoMarkers: noMarkers}
}

// ---------- Sentry probe ----------

func probeSentry(ctx context.Context, client *http.Client, token string, repos []string) sentryProbe {
	hdrs := map[string]string{"Authorization": "Bearer " + token}

	type sentryAPIProject struct {
		Slug         string `json:"slug"`
		Name         string `json:"name"`
		Organization struct {
			Slug string `json:"slug"`
		} `json:"organization"`
	}

	var all []sentryAPIProject
	next := probeSentryBaseURL + "/projects/"
	for next != "" {
		respHdrs, body, err := probeJSONGET(ctx, client, next, hdrs)
		if err != nil {
			break
		}
		var page []sentryAPIProject
		if err := json.Unmarshal(body, &page); err != nil {
			break
		}
		all = append(all, page...)
		next = probeSentryNextLink(respHdrs.Get("Link"))
	}

	if len(all) == 0 {
		return sentryProbe{Skipped: true, SkipReason: "no projects found (check token scope)"}
	}

	// Determine primary org slug by frequency.
	orgCounts := make(map[string]int)
	for _, p := range all {
		orgCounts[p.Organization.Slug]++
	}
	var primaryOrg string
	var maxCount int
	for slug, count := range orgCounts {
		if count > maxCount {
			maxCount = count
			primaryOrg = slug
		}
	}

	var projects []sentryProject
	for _, p := range all {
		projects = append(projects, sentryProject{Slug: p.Slug, Name: p.Name})
	}

	var matches []sentryMatch
	var unmatched []sentryProject
	for _, p := range projects {
		repoSlug, conf := matchRepo(p.Name, repos)
		if repoSlug != "" {
			matches = append(matches, sentryMatch{Project: p, RepoSlug: repoSlug, Confidence: conf})
		} else {
			unmatched = append(unmatched, p)
		}
	}
	return sentryProbe{OrgSlug: primaryOrg, Projects: projects, Matches: matches, Unmatched: unmatched}
}

// ---------- print helpers ----------

func printGitHubProbe(w io.Writer, p ghProbe) {
	if p.Skipped {
		fmt.Fprintf(w, "  skipped: %s\n", p.SkipReason)
		return
	}
	if len(p.Scopes) > 0 {
		fmt.Fprintf(w, "  token scopes: %s\n", strings.Join(p.Scopes, ", "))
	}
	fmt.Fprintf(w, "  %d repos total; %d active (not archived, pushed in last 90d)\n",
		p.TotalRepos, len(p.ActiveRepos))
}

func printCCIProbe(w io.Writer, p cciProbe) {
	if p.Skipped {
		fmt.Fprintf(w, "  skipped: %s\n", p.SkipReason)
		return
	}
	fmt.Fprintf(w, "  %d followed projects for this org\n", len(p.Followed))
	if len(p.Followed) > 0 {
		fmt.Fprintf(w, "  mapping convention: gh/<org>/<repo> → <org>/<repo>\n")
	}
}

func printBugsnagProbe(w io.Writer, p bnProbe) {
	if p.Skipped {
		fmt.Fprintf(w, "  skipped: %s\n", p.SkipReason)
		return
	}
	if p.OrgName != "" {
		fmt.Fprintf(w, "  org: %s\n", p.OrgName)
	}
	fmt.Fprintf(w, "  %d projects found\n", len(p.Projects))
	if len(p.Matches) > 0 {
		fmt.Fprintf(w, "  suggested repo mappings:\n")
		for _, m := range p.Matches {
			fmt.Fprintf(w, "    %-32s → %s [%s]\n", m.Project.Name, m.RepoSlug, m.Confidence)
		}
	}
	if len(p.Unmatched) > 0 {
		fmt.Fprintf(w, "  projects with no repo match (fill in manually):\n")
		for _, u := range p.Unmatched {
			fmt.Fprintf(w, "    %s\n", u.Name)
		}
	}
}

func printHoneycombProbe(w io.Writer, p hcProbe, org string) {
	if p.Skipped {
		fmt.Fprintf(w, "  skipped: %s\n", p.SkipReason)
		return
	}
	fmt.Fprintf(w, "  %d datasets found\n", len(p.Datasets))

	// Group attribution by dataset, sorted by total marker count descending.
	type dsTotal struct {
		slug  string
		total int
		repos []hcAttr
	}
	dsMap := make(map[string]*dsTotal)
	for _, a := range p.Attribution {
		if dsMap[a.Dataset] == nil {
			dsMap[a.Dataset] = &dsTotal{slug: a.Dataset}
		}
		dsMap[a.Dataset].total += a.MarkerCount
		dsMap[a.Dataset].repos = append(dsMap[a.Dataset].repos, a)
	}
	if len(dsMap) > 0 {
		sorted := make([]*dsTotal, 0, len(dsMap))
		for _, v := range dsMap {
			sorted = append(sorted, v)
		}
		sort.Slice(sorted, func(i, j int) bool {
			return sorted[i].total > sorted[j].total
		})
		fmt.Fprintf(w, "  datasets with deploy markers linking to %s GitHub commits:\n", org)
		for _, ds := range sorted {
			fmt.Fprintf(w, "    %s: %d markers covering %d repos\n",
				ds.slug, ds.total, len(ds.repos))
			for _, a := range ds.repos {
				fmt.Fprintf(w, "      %s  %d markers\n", a.RepoSlug, a.MarkerCount)
			}
		}
	}
	if len(p.NoMarkers) > 0 {
		fmt.Fprintf(w, "  repos with no markers in any dataset:\n")
		for _, r := range p.NoMarkers {
			fmt.Fprintf(w, "    %s  ← CI pipeline may not post markers\n", r)
		}
	}
}

func printSentryProbe(w io.Writer, p sentryProbe) {
	if p.Skipped {
		fmt.Fprintf(w, "  skipped: %s\n", p.SkipReason)
		return
	}
	fmt.Fprintf(w, "  %d projects found (org: %s)\n", len(p.Projects), p.OrgSlug)
	if len(p.Matches) > 0 {
		fmt.Fprintf(w, "  suggested repo mappings:\n")
		for _, m := range p.Matches {
			fmt.Fprintf(w, "    %-32s → %s [%s]\n", m.Project.Name, m.RepoSlug, m.Confidence)
		}
	}
	if len(p.Unmatched) > 0 {
		fmt.Fprintf(w, "  projects with no repo match (fill in manually):\n")
		for _, u := range p.Unmatched {
			fmt.Fprintf(w, "    %s\n", u.Name)
		}
	}
}

// ---------- draft TOML ----------

func renderProbeDraft(r probeResult) (string, error) {
	var sb strings.Builder

	fmt.Fprintf(&sb, "# generated by xray init --probe --org %s\n", r.Org)
	fmt.Fprintln(&sb, "# probe discovered the connector state below; review and edit before running xray run.")
	fmt.Fprintln(&sb, "# For each connector: paste your token, verify suggested mappings, remove unneeded blocks.")
	fmt.Fprintln(&sb)
	today := time.Now().UTC().Format("2006-01-02")
	fmt.Fprintln(&sb, "# Extraction window: starts 2021-01-01 to capture the pre-Copilot baseline (~18 months before")
	fmt.Fprintln(&sb, "# Copilot GA in June 2022). On a large repo, validate connectors with a shorter window first.")
	fmt.Fprintf(&sb, "window = %q\n", "2021-01-01.."+today)
	fmt.Fprintln(&sb)
	fmt.Fprintln(&sb, `capture_harness_content = false`)
	fmt.Fprintln(&sb)
	fmt.Fprintln(&sb, "[teams]")
	fmt.Fprintln(&sb, "unassigned = [")
	for _, repo := range r.GitHub.ActiveRepos {
		fmt.Fprintf(&sb, "    %q,\n", repo)
	}
	fmt.Fprintln(&sb, "]")
	fmt.Fprintln(&sb)

	fmt.Fprintln(&sb, "[connectors.github]")
	fmt.Fprintln(&sb, `token = ""`)
	fmt.Fprintln(&sb)

	fmt.Fprintln(&sb, "[connectors.github_actions]")
	fmt.Fprintln(&sb, "# Inherits token from [connectors.github] by default; set token here to override.")
	fmt.Fprintln(&sb)

	fmt.Fprintln(&sb, "[connectors.circleci]")
	fmt.Fprintln(&sb, `token = ""`)
	fmt.Fprintln(&sb, "[connectors.circleci.projects]")
	if !r.CircleCI.Skipped && len(r.CircleCI.Followed) > 0 {
		for _, f := range r.CircleCI.Followed {
			fmt.Fprintf(&sb, "%q = %q\n", f.CCISlug, f.RepoSlug)
		}
	} else {
		fmt.Fprintln(&sb, `# "gh/owner/repo" = "owner/repo"`)
	}
	fmt.Fprintln(&sb)

	fmt.Fprintln(&sb, "[connectors.sentry]")
	fmt.Fprintln(&sb, `token = ""`)
	sentryOrg := ""
	if !r.Sentry.Skipped {
		sentryOrg = r.Sentry.OrgSlug
	}
	fmt.Fprintf(&sb, "organization = %q\n", sentryOrg)
	fmt.Fprintln(&sb, "[connectors.sentry.projects]")
	if !r.Sentry.Skipped && (len(r.Sentry.Matches) > 0 || len(r.Sentry.Unmatched) > 0) {
		for _, m := range r.Sentry.Matches {
			fmt.Fprintf(&sb, "%q = %q  # %s [%s]\n", m.Project.Slug, m.RepoSlug, m.Project.Name, m.Confidence)
		}
		for _, u := range r.Sentry.Unmatched {
			fmt.Fprintf(&sb, "# %q = \"\"  # %s [needs operator input]\n", u.Slug, u.Name)
		}
	} else {
		fmt.Fprintln(&sb, `# "sentry-project-slug" = "owner/repo"`)
	}
	fmt.Fprintln(&sb)

	fmt.Fprintln(&sb, "[connectors.bugsnag]")
	fmt.Fprintln(&sb, `token = ""`)
	fmt.Fprintln(&sb, "[connectors.bugsnag.projects]")
	if !r.Bugsnag.Skipped && (len(r.Bugsnag.Matches) > 0 || len(r.Bugsnag.Unmatched) > 0) {
		for _, m := range r.Bugsnag.Matches {
			fmt.Fprintf(&sb, "%q = %q  # %s [%s]\n", m.Project.ID, m.RepoSlug, m.Project.Name, m.Confidence)
		}
		for _, u := range r.Bugsnag.Unmatched {
			fmt.Fprintf(&sb, "# %q = \"\"  # %s [needs operator input]\n", u.ID, u.Name)
		}
	} else {
		fmt.Fprintln(&sb, `# "bugsnag-project-id" = "owner/repo"`)
	}
	fmt.Fprintln(&sb)

	fmt.Fprintln(&sb, "[connectors.honeycomb]")
	fmt.Fprintln(&sb, `token = ""`)
	recommended := ""
	if !r.Honeycomb.Skipped && len(r.Honeycomb.Attribution) > 0 {
		totals := make(map[string]int)
		for _, a := range r.Honeycomb.Attribution {
			totals[a.Dataset] += a.MarkerCount
		}
		var best string
		var bestCount int
		for ds, cnt := range totals {
			if cnt > bestCount {
				bestCount = cnt
				best = ds
			}
		}
		recommended = best
	}
	fmt.Fprintf(&sb, "dataset = %q\n", recommended)

	return sb.String(), nil
}

// ---------- fuzzy matching ----------

// matchRepo returns the best-matching repo slug from repos for the given
// project name. Confidence is "high" for exact normalised match, "medium" for
// substring match. Returns ("", "") when no match is found.
func matchRepo(projectName string, repos []string) (repoSlug, confidence string) {
	normProject := normalizeSlug(projectName)

	type candidate struct {
		slug string
		norm string
	}
	candidates := make([]candidate, 0, len(repos))
	for _, r := range repos {
		parts := strings.SplitN(r, "/", 2)
		if len(parts) != 2 {
			continue
		}
		candidates = append(candidates, candidate{r, normalizeSlug(parts[1])})
	}

	for _, c := range candidates {
		if normProject == c.norm {
			return c.slug, "high"
		}
	}
	for _, c := range candidates {
		if strings.Contains(normProject, c.norm) || strings.Contains(c.norm, normProject) {
			return c.slug, "medium"
		}
	}
	return "", ""
}

// normalizeSlug lower-cases s, maps separators to underscores, and strips
// common language-suffix tokens used in multi-language org setups.
func normalizeSlug(s string) string {
	s = strings.ToLower(s)
	for _, sep := range []string{"-", " ", "."} {
		s = strings.ReplaceAll(s, sep, "_")
	}
	for _, suffix := range []string{"_js", "_ruby", "_python", "_go", "_java", "_clojure"} {
		if strings.HasSuffix(s, suffix) {
			s = strings.TrimSuffix(s, suffix)
			break
		}
	}
	return s
}

// ---------- HTTP plumbing ----------

// probeJSONGET performs a GET request and returns the response headers, body
// bytes, and any error. Non-2xx responses are returned as an error.
func probeJSONGET(ctx context.Context, client *http.Client, u string, hdrs map[string]string) (http.Header, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range hdrs {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.Header, nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.Header, body, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return resp.Header, body, nil
}

// probeNextLinkRE matches the `<url>; rel="next"` form in a Link header.
var probeNextLinkRE = regexp.MustCompile(`<([^>]+)>\s*;\s*rel="next"`)

// probeLinkNext extracts the URL from a `rel="next"` Link header (RFC 5988).
// Used for Bugsnag pagination.
func probeLinkNext(header string) string {
	m := probeNextLinkRE.FindStringSubmatch(header)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// probeSentryNextLink extracts the next-page URL from Sentry's Link header.
// Sentry includes `results="true"` on the next link only when there are more
// pages; absent or `results="false"` means the last page.
func probeSentryNextLink(header string) string {
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if !strings.Contains(part, `rel="next"`) || !strings.Contains(part, `results="true"`) {
			continue
		}
		m := probeNextLinkRE.FindStringSubmatch(part)
		if len(m) >= 2 {
			return m[1]
		}
	}
	return ""
}
