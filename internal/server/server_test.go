package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
	for _, want := range []string{"Waiting on me", "#42", "alice", "review requested from you", "/static/htmx.min.js", `id="board"`} {
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
	if code, body := get(t, s.Handler(), "GET", "/api/triage.json"); code != 200 || !strings.Contains(body, `"waiting-on-me"`) {
		t.Errorf("json = %d %q", code, body)
	}
	if code, _ := get(t, s.Handler(), "GET", "/nope"); code != 404 {
		t.Errorf("unknown path = %d, want 404", code)
	}
}
