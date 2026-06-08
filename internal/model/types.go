package model

import "time"

// SchemaVersion is bumped on any breaking change to the canonical model.
// See CLAUDE.md "Schema versioning" for the bump rules.
//
// v2 (ADR 023): author-identity columns — commits.author_handle,
// commits.committer_handle, commit_coauthors.handle, prs.author_handle,
// reviews.reviewer_handle, pr_comments.author_handle — shifted from raw
// login / git ident to the opaque "h_<digits>" form assay's boundary check
// enforces. The column types and names are unchanged; the semantics are not.
const SchemaVersion = 2

type Repo struct {
	Slug             string
	DefaultBranch    string
	HeadSHA          string
	Team             string
	PrimaryLanguage  string
	CreatedAt        *time.Time
	IsFork           bool
	IsArchived       bool
	Visibility       string
	ContributorCount int
	CommitsInWindow  int
	PRsInWindow      int
	CommitsAllTime   int
	PRsAllTime       int
}

type RepoLanguage struct {
	Repo     string
	Language string
	Bytes    int64
}

type Branch struct {
	Repo          string
	Name          string
	LastCommitSHA string
	LastCommitAt  time.Time
	IsDefault     bool
}

type BranchProtection struct {
	Repo            string
	Branch          string
	RequiredReviews *int
	RequiredChecks  string
	EnforceAdmins   bool
	RestrictsPushes bool
}

type Codeowner struct {
	Repo        string
	Pattern     string
	OwnerHandle string
	OwnerType   string // "user" | "team"
}

type Commit struct {
	SHA               string
	Repo              string
	AuthorHandle      string
	CommitterHandle   string
	AuthoredAt        time.Time
	CommittedAt       time.Time
	Additions         int
	Deletions         int
	FilesChanged      int
	MessageSubject    string
	AuthorIsBot       bool
	CommitterIsBot    bool
	SignatureVerified *bool
	LandedViaPR       *bool
	RevertsSHA        string
	IsRevert          bool
	IsMerge           bool
	HasHotfixMarker   bool
}

type CommitFile struct {
	CommitSHA  string
	Repo       string
	Path       string
	Additions  int
	Deletions  int
	ChangeType string // A | M | D | R | C
	PrevPath   string
}

type CommitCoauthor struct {
	CommitSHA string
	Repo      string
	Handle    string
	Source    string // "trailer" | "api"
	Kind      string // "human" | "bot" | "ai_tool"
}

type PR struct {
	Number                 int
	Repo                   string
	Title                  string
	OpenedAt               time.Time
	MergedAt               *time.Time
	ClosedAt               *time.Time
	AuthorHandle           string
	Additions              int
	Deletions              int
	FilesChanged           int
	BaseBranch             string
	HeadSHA                string
	MergeSHA               string
	MergeMethod            string
	IsDraft                bool
	ReadyForReviewAt       *time.Time
	FirstReviewAt          *time.Time
	CommitCount            int
	HeadRepo               string
	ForcePushedAfterReview bool
	BodyLength             int
	TemplateMatch          *float64
	ChecklistTotal         int
	ChecklistChecked       int
	HasRiskMarker          bool
	CodeBlockCount         int
	ImageCount             int
	LinkCount              int
	IssueRefsCount         int
}

type PRCommit struct {
	PRNumber int
	Repo     string
	SHA      string
}

type Review struct {
	PRNumber       int
	Repo           string
	ReviewerHandle string
	SubmittedAt    time.Time
	State          string
	BodyLength     int
}

type PRComment struct {
	PRNumber     int
	Repo         string
	AuthorHandle string
	AuthorIsBot  bool
	CreatedAt    time.Time
	Kind         string // "issue_comment" | "review_comment"
	BodyLength   int
	InReplyTo    *int64
	Path         string
}

type PRReviewRequest struct {
	PRNumber        int
	Repo            string
	RequestedHandle string
	RequestedType   string // "user" | "team"
	RequestedAt     time.Time
}

type PRLabel struct {
	PRNumber int
	Repo     string
	Label    string
}

type Build struct {
	ID              string
	Repo            string
	Source          string // "circleci" | "github_actions"
	Pipeline        string
	Status          string
	Conclusion      string
	StartedAt       *time.Time
	CompletedAt     *time.Time
	DurationSeconds *int
	CommitSHA       string
	Branch          string
	Event           string // push|pull_request|schedule|manual
	Attempt         int
	RerunOfID       string
	CreatedAt       time.Time
	PRNumber        *int
}

type BuildJob struct {
	BuildID         string
	Repo            string
	Name            string
	Status          string
	Conclusion      string
	DurationSeconds *int
	Attempt         int
}

type Deploy struct {
	ID                 string
	Repo               string
	Environment        string
	DeployedAt         time.Time
	CommitSHA          string
	Source             string // "github" | "github_actions" | "honeycomb"
	Status             string
	SupersedesDeployID string
	RolledBack         bool
	Trigger            string
	ReleaseTag         string
	Version            string
}

type Release struct {
	Repo         string
	Tag          string
	Name         string
	CreatedAt    time.Time
	SHA          string
	IsPrerelease bool
}

type Incident struct {
	ID             string
	Repo           string
	Source         string // "sentry" | "bugsnag" | "honeycomb"
	OpenedAt       time.Time
	ResolvedAt     *time.Time
	Severity       string
	Occurrences    int
	ReleaseRef     string
	DeployID       string
	CommitSHA      string
	AcknowledgedAt *time.Time
	IsRegression   bool
	CulpritRef     string
}

type Defect struct {
	ID        string
	Repo      string
	TicketRef string
	Source    string // "pr_title" | "pr_body" | "commit_message"
	OpenedAt  time.Time
	ClosedAt  *time.Time
}

type FileMetric struct {
	Repo                 string
	Path                 string
	SnapshotSHA          string
	Language             string
	IsBinary             bool
	IsGenerated          bool
	IsVendored           bool
	IsTest               bool
	IsDependencyManifest bool
	SizeBytes            int64
	LOCTotal             int
	LOCCode              int
	LOCBlank             int
	MaxIndent            int
	MeanIndent           float64
	MaxLineLength        int
	P95LineLength        int
}

type HarnessArtifact struct {
	Repo            string
	Path            string
	Tool            string
	Kind            string
	LineCount       int
	FirstSeenCommit string
	FirstSeenAt     time.Time
	LastModifiedAt  time.Time
	Content         string
}

// RepoFile is one row per file tracked at HEAD per repo, populated from
// git ls-files. Path is relative to the repo root and uses forward slashes.
// No content or metadata is stored.
type RepoFile struct {
	Repo string
	Path string
}

// FileComplexityHistory is one per-revision row consumed by assay's
// stage2.flow.hotspot_complexity_trend. The indent_* fields use the
// Hindle/Godfrey/Holt 2008 logical-indent proxy (4 spaces or 1 tab = 1
// level), not the raw-space measure file_metrics uses.
type FileComplexityHistory struct {
	CommitSHA   string
	Repo        string
	Path        string
	N           int
	IndentTotal int
	IndentMean  float64
	IndentSD    float64
	IndentMax   int
}
