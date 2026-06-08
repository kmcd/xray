package connector

import "github.com/kmcd/xray/internal/model"

// Sink is the typed insertion surface every connector writes against. Methods
// are typed per canonical table so the compiler enforces that connectors
// cannot invent tables outside the schema.
type Sink interface {
	InsertRepo(model.Repo) error
	InsertTeamRepo(team, repo string) error
	InsertRepoLanguage(model.RepoLanguage) error
	InsertBranch(model.Branch) error
	InsertBranchProtection(model.BranchProtection) error
	InsertCodeowner(model.Codeowner) error

	InsertCommit(model.Commit) error
	InsertCommitFile(model.CommitFile) error
	InsertCommitCoauthor(model.CommitCoauthor) error

	InsertPR(model.PR) error
	InsertPRCommit(model.PRCommit) error
	InsertReview(model.Review) error
	InsertPRComment(model.PRComment) error
	InsertPRReviewRequest(model.PRReviewRequest) error
	InsertPRLabel(model.PRLabel) error

	InsertBuild(model.Build) error
	InsertBuildJob(model.BuildJob) error
	InsertDeploy(model.Deploy) error
	InsertRelease(model.Release) error

	InsertIncident(model.Incident) error
	InsertDefect(model.Defect) error

	InsertFileMetric(model.FileMetric) error
	InsertHarnessArtifact(model.HarnessArtifact) error
	InsertFileComplexityHistory(model.FileComplexityHistory) error
	InsertRepoFile(model.RepoFile) error
}
