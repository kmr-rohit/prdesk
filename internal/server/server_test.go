package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rohikuma/prdesk/internal/ai"
	"github.com/rohikuma/prdesk/internal/config"
	"github.com/rohikuma/prdesk/internal/github"
)

func testConfig() *config.Config {
	return &config.Config{Login: "kmr-rohit", Repos: []string{"kubeflow/docs-agent"}, StaleDays: 14, ReadyLabels: []string{"lgtm", "approved"}}
}

func samplePRs() []github.PullRequest {
	now := time.Now()
	return []github.PullRequest{{
		Repo: "kubeflow/docs-agent", Number: 42, Title: `fix: <script>alert(1)</script> & more`,
		URL: "https://github.com/kubeflow/docs-agent/pull/42", Author: "alice", AuthorAssociation: "CONTRIBUTOR",
		ReviewRequests: []string{"kmr-rohit"}, CheckState: "SUCCESS", Mergeable: "MERGEABLE",
		CreatedAt: now.Add(-48 * time.Hour), UpdatedAt: now.Add(-time.Hour),
	}}
}

func get(t *testing.T, h http.Handler, method, path string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	body, _ := io.ReadAll(rec.Body)
	return rec.Code, string(body)
}

func TestPageRendersAndEscapes(t *testing.T) {
	s, err := New(testConfig(), func(context.Context) ([]github.PullRequest, error) { return samplePRs(), nil }, time.Minute, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	code, body := get(t, s.Handler(), "GET", "/")
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	for _, want := range []string{"Review needed", "#42", "alice", "review requested from you", `class="chev"`, "Collapse all", "Expand all", "/static/htmx.min.js", `id="board"`} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	// PR titles are untrusted: the raw tag must never appear.
	if strings.Contains(body, "<script>alert(1)") {
		t.Error("title was not HTML-escaped (XSS)")
	}
	if !strings.Contains(body, "&lt;script&gt;alert(1)") {
		t.Error("expected escaped title in output")
	}
}

func TestSnapshotIsCachedForTTL(t *testing.T) {
	var calls atomic.Int32
	s, _ := New(testConfig(), func(context.Context) ([]github.PullRequest, error) {
		calls.Add(1)
		return samplePRs(), nil
	}, time.Hour, time.Minute)
	h := s.Handler()

	get(t, h, "GET", "/")
	get(t, h, "GET", "/board")
	get(t, h, "GET", "/api/triage.json")
	if n := calls.Load(); n != 1 {
		t.Errorf("fetched %d times for 3 requests inside the TTL, want 1", n)
	}
	get(t, h, "POST", "/refresh") // forced
	if n := calls.Load(); n != 2 {
		t.Errorf("after /refresh fetched %d times, want 2", n)
	}
}

func TestFetchErrorKeepsLastGoodData(t *testing.T) {
	fail := false
	s, _ := New(testConfig(), func(context.Context) ([]github.PullRequest, error) {
		if fail {
			return nil, errors.New("rate limited")
		}
		return samplePRs(), nil
	}, time.Hour, time.Minute)
	h := s.Handler()

	get(t, h, "GET", "/") // good fetch
	fail = true
	_, body := get(t, h, "POST", "/refresh") // forced, fails

	if !strings.Contains(body, "rate limited") {
		t.Error("error banner missing")
	}
	if !strings.Contains(body, "#42") {
		t.Error("previous data should still be shown after a failed refresh")
	}
}

func TestHealthzAndJSON(t *testing.T) {
	s, _ := New(testConfig(), func(context.Context) ([]github.PullRequest, error) { return samplePRs(), nil }, time.Minute, time.Minute)
	if code, body := get(t, s.Handler(), "GET", "/healthz"); code != 200 || strings.TrimSpace(body) != "ok" {
		t.Errorf("healthz = %d %q", code, body)
	}
	if code, body := get(t, s.Handler(), "GET", "/api/triage.json"); code != 200 || !strings.Contains(body, `"waiting-on-me"`) || !strings.Contains(body, `"nextStep": "Review it"`) {
		t.Errorf("json = %d %q", code, body)
	}
	if code, _ := get(t, s.Handler(), "GET", "/nope"); code != 404 {
		t.Errorf("unknown path = %d, want 404", code)
	}
}

// ---- workflow actions on the dashboard ----

func approvedAwaitingCI() []github.PullRequest {
	now := time.Now()
	return []github.PullRequest{{
		Repo: "kubeflow/docs-agent", Number: 50, Title: "feat: thing", URL: "https://github.com/kubeflow/docs-agent/pull/50",
		Author: "bob", AuthorAssociation: "FIRST_TIME_CONTRIBUTOR", AwaitingApproval: true, CheckState: "SUCCESS",
		Mergeable: "MERGEABLE", HeadSHA: "abc123",
		Reviews:         []github.Review{{Author: "kmr-rohit", State: "APPROVED", SubmittedAt: now.Add(-2 * time.Hour)}},
		HeadCommittedAt: now.Add(-5 * time.Hour), CreatedAt: now.Add(-72 * time.Hour), UpdatedAt: now.Add(-time.Hour),
	}}
}

func TestStageSectionAndCopyButton(t *testing.T) {
	cfg := testConfig()
	cfg.Approver = "tarekabouzeid"
	s, _ := New(cfg, func(context.Context) ([]github.PullRequest, error) { return approvedAwaitingCI(), nil }, time.Minute, time.Minute)
	_, body := get(t, s.Handler(), "GET", "/")

	for _, want := range []string{"Ask @tarekabouzeid: /ok-to-test", "Copy /ok-to-test ask", "data-copy=", "tarekabouzeid"} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	if strings.Contains(body, "Review needed") {
		t.Error("an approved PR must not show up under Review needed")
	}
}

// ---- AI ----

type fakeAnalyzer struct {
	mu    sync.Mutex
	calls int
	res   ai.Analysis
	err   error
	gate  chan struct{} // if set, Analyze blocks until it is closed
}

func (f *fakeAnalyzer) Analyze(ctx context.Context, in ai.Input) (ai.Analysis, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if f.gate != nil {
		<-f.gate
	}
	return f.res, f.err
}

func (f *fakeAnalyzer) count() int { f.mu.Lock(); defer f.mu.Unlock(); return f.calls }

func newAIServer(t *testing.T, fa *fakeAnalyzer) *Server {
	t.Helper()
	s, _ := New(testConfig(), func(context.Context) ([]github.PullRequest, error) { return samplePRs(), nil }, time.Hour, time.Minute)
	s.WithAI(fa, func(context.Context, string, int) (string, bool, error) { return "+code", false, nil }, "test-model")
	return s
}

var goodAnalysis = ai.Analysis{
	Summary: "Adds a thing <b>bold</b>.", Risk: "low", Verdict: "approve", NeedsEval: true,
	ReviewFocus: []string{"check the regex"}, DraftComment: "LGTM, thanks!",
}

func TestAIDisabledShowsNoButton(t *testing.T) {
	s, _ := New(testConfig(), func(context.Context) ([]github.PullRequest, error) { return samplePRs(), nil }, time.Minute, time.Minute)
	_, body := get(t, s.Handler(), "GET", "/")
	if strings.Contains(body, "Analyze with AI") {
		t.Error("AI button shown although AI is not configured")
	}
	if code, _ := get(t, s.Handler(), "POST", "/ai/kubeflow/docs-agent/42"); code != 404 {
		t.Errorf("POST /ai without AI = %d, want 404", code)
	}
}

func TestAIAnalyzeFlow(t *testing.T) {
	fa := &fakeAnalyzer{res: goodAnalysis}
	s := newAIServer(t, fa)
	h := s.Handler()

	_, body := get(t, h, "GET", "/")
	if !strings.Contains(body, "Analyze with AI") || !strings.Contains(body, `hx-post="/ai/kubeflow/docs-agent/42"`) {
		t.Fatal("expected an Analyze button wired to /ai/kubeflow/docs-agent/42")
	}

	code, frag := get(t, h, "POST", "/ai/kubeflow/docs-agent/42")
	if code != 200 || !strings.Contains(frag, "LGTM, thanks!") || !strings.Contains(frag, "risk-low") {
		t.Fatalf("analysis fragment wrong (%d): %s", code, frag)
	}
	// Model output is untrusted too: HTML must be escaped.
	if strings.Contains(frag, "<b>bold</b>") || !strings.Contains(frag, "&lt;b&gt;bold&lt;/b&gt;") {
		t.Error("model output was not HTML-escaped")
	}

	// The result now renders inline on the page, with no second model call.
	_, body = get(t, h, "GET", "/board")
	if !strings.Contains(body, "LGTM, thanks!") || strings.Contains(body, "Analyze with AI") {
		t.Error("cached analysis should appear inline instead of the button")
	}
	get(t, h, "POST", "/ai/kubeflow/docs-agent/42")
	if n := fa.count(); n != 1 {
		t.Errorf("analyzer called %d times, want 1 (cached)", n)
	}
}

func TestAIConcurrentRequestsShareOneRun(t *testing.T) {
	fa := &fakeAnalyzer{res: goodAnalysis, gate: make(chan struct{})}
	s := newAIServer(t, fa)
	h := s.Handler()
	get(t, h, "GET", "/") // load the snapshot

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); get(t, h, "POST", "/ai/kubeflow/docs-agent/42") }()
	}
	time.Sleep(100 * time.Millisecond) // let all five arrive while the model is "busy"
	close(fa.gate)
	wg.Wait()
	if n := fa.count(); n != 1 {
		t.Errorf("5 concurrent clicks caused %d model runs, want 1", n)
	}
}

func TestAIErrorIsShownAndRetryable(t *testing.T) {
	fa := &fakeAnalyzer{err: errors.New("ollama unreachable")}
	s := newAIServer(t, fa)
	h := s.Handler()
	get(t, h, "GET", "/")

	_, frag := get(t, h, "POST", "/ai/kubeflow/docs-agent/42")
	if !strings.Contains(frag, "ollama unreachable") || !strings.Contains(frag, "Retry") {
		t.Errorf("error fragment missing message or retry button: %s", frag)
	}
	fa.mu.Lock()
	fa.err, fa.res = nil, goodAnalysis
	fa.mu.Unlock()
	if _, frag = get(t, h, "POST", "/ai/kubeflow/docs-agent/42"); !strings.Contains(frag, "LGTM") {
		t.Errorf("retry after failure should run again and succeed: %s", frag)
	}
}

func TestAIUnknownPR(t *testing.T) {
	s := newAIServer(t, &fakeAnalyzer{res: goodAnalysis})
	h := s.Handler()
	get(t, h, "GET", "/")
	for _, path := range []string{"/ai/kubeflow/docs-agent/999", "/ai/kubeflow/docs-agent/abc", "/ai/other/repo/42"} {
		if code, _ := get(t, h, "POST", path); code != 404 {
			t.Errorf("POST %s = %d, want 404 (only PRs from the dashboard may be analyzed)", path, code)
		}
	}
}

func TestAICacheKeyedByHeadCommit(t *testing.T) {
	fa := &fakeAnalyzer{res: goodAnalysis}
	pr := samplePRs()[0]
	s, _ := New(testConfig(), func(context.Context) ([]github.PullRequest, error) {
		p := pr
		return []github.PullRequest{p}, nil
	}, time.Hour, time.Minute)
	s.WithAI(fa, nil, "m")
	h := s.Handler()

	pr.HeadSHA = "aaa"
	get(t, h, "POST", "/refresh")
	get(t, h, "POST", "/ai/kubeflow/docs-agent/42")
	pr.HeadSHA = "bbb" // author pushed
	get(t, h, "POST", "/refresh")
	get(t, h, "POST", "/ai/kubeflow/docs-agent/42")
	if n := fa.count(); n != 2 {
		t.Errorf("analyzer called %d times, want 2 (a new push invalidates the analysis)", n)
	}
}
