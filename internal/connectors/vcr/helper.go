// Package vcr provides a shared VCR (cassette-replay) test helper for
// connector packages that make real HTTP calls.
package vcr

import (
	"errors"
	"net/http"
	"os"
	"testing"

	"golang.org/x/oauth2"
	"gopkg.in/dnaeon/go-vcr.v4/pkg/cassette"
	"gopkg.in/dnaeon/go-vcr.v4/pkg/recorder"
)

// NewGitHubClient returns an *http.Client backed by a VCR recorder for
// the cassette at cassettePath. In normal test runs the cassette is
// replayed (ModeReplayOnly); if the cassette file is absent, the test is
// skipped with a hint.
//
// Set VCR_RECORD=1 to force re-recording. The environment variable
// GITHUB_TOKEN must be set when recording; the token is stripped from
// cassette request headers before saving so it is safe to commit.
func NewGitHubClient(t *testing.T, cassettePath string) *http.Client {
	t.Helper()

	if os.Getenv("VCR_RECORD") != "" {
		return newRecordingClient(t, cassettePath)
	}
	return newReplayClient(t, cassettePath)
}

func newRecordingClient(t *testing.T, cassettePath string) *http.Client {
	t.Helper()
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		t.Skip("VCR_RECORD set but GITHUB_TOKEN not found")
	}
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	realTransport := &oauth2.Transport{Source: ts, Base: http.DefaultTransport}

	r, err := recorder.New(cassettePath,
		recorder.WithMode(recorder.ModeRecordOnly),
		recorder.WithRealTransport(realTransport),
		recorder.WithHook(scrubAuthorization, recorder.BeforeSaveHook),
		recorder.WithMatcher(matchURLMethod),
		recorder.WithSkipRequestLatency(true),
	)
	if err != nil {
		t.Fatalf("vcr record: open %s: %v", cassettePath, err)
	}
	t.Cleanup(func() { _ = r.Stop() })
	return r.GetDefaultClient()
}

func newReplayClient(t *testing.T, cassettePath string) *http.Client {
	t.Helper()
	r, err := recorder.New(cassettePath,
		recorder.WithMode(recorder.ModeReplayOnly),
		recorder.WithMatcher(matchURLMethod),
		recorder.WithSkipRequestLatency(true),
	)
	if errors.Is(err, cassette.ErrCassetteNotFound) {
		t.Skipf("cassette not found: %s — run VCR_RECORD=1 GITHUB_TOKEN=<token> to record", cassettePath)
	}
	if err != nil {
		t.Fatalf("vcr replay: open %s: %v", cassettePath, err)
	}
	t.Cleanup(func() { _ = r.Stop() })
	return r.GetDefaultClient()
}

func scrubAuthorization(i *cassette.Interaction) error {
	delete(i.Request.Headers, "Authorization")
	return nil
}

// matchURLMethod matches a cassette interaction solely on HTTP method and URL.
// Header-independent matching lets the same cassette work under replay when
// the Authorization header differs from the recording token.
func matchURLMethod(r *http.Request, i cassette.Request) bool {
	return r.Method == i.Method && r.URL.String() == i.URL
}
