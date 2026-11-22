package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// fakeGitHub serves fixture files the way the real API would: page1 when no
// cursor is sent, page2 when the cursor from page1 comes back.
//
// GO LESSON 8: httptest.NewServer starts a real HTTP server on localhost for
// the duration of the test. No mocking framework needed: you test the real
// client code against a real (fake) server.
func fakeGitHub(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			http.Error(w, "bad auth: "+got, http.StatusUnauthorized)
			return
		}
		var req struct {
			Variables struct {
				Cursor *string `json:"cursor"`
			} `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		file := "testdata/page1.json"
		if req.Variables.Cursor != nil && *req.Variables.Cursor == "CURSOR_1" {
			file = "testdata/page2.json"
		}
		data, err := os.ReadFile(file)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Write(data)
	}))
	t.Cleanup(srv.Close) // runs when the test finishes
	return srv
}

func TestFetchOpenPRs(t *testing.T) {
	srv := fakeGitHub(t)
	c := NewClient("test-token")
	c.Endpoint = srv.URL

	prs, err := c.FetchOpenPRs(context.Background(), "kubeflow/docs-agent")
	if err != nil {
		t.Fatal(err)
	}
	if len(prs) != 3 {
		t.Fatalf("got %d PRs, want 3 (pagination across 2 pages)", len(prs))
	}

	byNum := map[int]PullRequest{}
	for _, p := range prs {
		byNum[p.Number] = p
	}

	t.Run("first-time contributor with review requests", func(t *testing.T) {
		p := byNum[101]
		if p.Author != "newcomer" || p.AuthorAssociation != "FIRST_TIME_CONTRIBUTOR" {
			t.Errorf("author fields wrong: %+v", p)
		}
		if !p.HasLabel("needs-ok-to-test") || !p.HasLabel("community-meeting") {
			t.Errorf("labels = %v", p.Labels)
		}
		if len(p.ReviewRequests) != 2 || p.ReviewRequests[0] != "kmr-rohit" || p.ReviewRequests[1] != "docs-reviewers" {
			t.Errorf("review requests = %v", p.ReviewRequests)
		}
		// tide (ignored) is pending and DCO passed, so only DCO counts...
		if p.CheckState != "SUCCESS" {
			t.Errorf("CheckState = %q, want SUCCESS (tide ignored, DCO passed)", p.CheckState)
		}
		// ...but the Actions suite is blocked waiting for approval.
		if !p.AwaitingApproval {
			t.Error("AwaitingApproval = false, want true")
		}
		if p.HeadSHA != "abc1234def" || p.Mergeable != "MERGEABLE" {
			t.Errorf("HeadSHA/Mergeable = %q/%q", p.HeadSHA, p.Mergeable)
		}
	})

	t.Run("deleted author becomes ghost", func(t *testing.T) {
		p := byNum[99]
		if p.Author != "ghost" {
			t.Errorf("Author = %q, want ghost", p.Author)
		}
		if p.Mergeable != "CONFLICTING" {
			t.Errorf("Mergeable = %q, want CONFLICTING", p.Mergeable)
		}
		if p.CheckState != "FAILURE" || len(p.FailedChecks) != 1 || p.FailedChecks[0] != "lint" || p.AwaitingApproval {
			t.Errorf("checks wrong: state=%q failed=%v awaiting=%v", p.CheckState, p.FailedChecks, p.AwaitingApproval)
		}
		if p.ReviewDecision != "CHANGES_REQUESTED" {
			t.Errorf("state wrong: %+v", p)
		}
		if len(p.Reviews) != 1 || p.Reviews[0].Author != "kmr-rohit" {
			t.Errorf("reviews = %+v", p.Reviews)
		}
	})

	t.Run("page two draft PR", func(t *testing.T) {
		p := byNum[80]
		// tide is PENDING but ignored, so the PR reads as green.
		if !p.Draft || p.CheckState != "SUCCESS" || p.HeadCommittedAt.IsZero() {
			t.Errorf("unexpected: %+v", p)
		}
	})
}

func TestFetchOpenPRsErrors(t *testing.T) {
	tests := []struct {
		name    string
		repo    string
		handler http.HandlerFunc
	}{
		{"bad repo format", "justname", func(w http.ResponseWriter, r *http.Request) {}},
		{"http 401", "a/b", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "Bad credentials", http.StatusUnauthorized)
		}},
		{"graphql error", "a/b", func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"errors":[{"message":"Could not resolve to a Repository"}]}`))
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()
			c := NewClient("x")
			c.Endpoint = srv.URL
			if _, err := c.FetchOpenPRs(context.Background(), tc.repo); err == nil {
				t.Fatal("expected an error, got nil")
			}
		})
	}
}

func TestFetchDiff(t *testing.T) {
	const diff = "diff --git a/x b/x\n+hello\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/kubeflow/docs-agent/pulls/7" || r.Header.Get("Accept") != "application/vnd.github.v3.diff" {
			http.Error(w, "bad request: "+r.URL.Path+" "+r.Header.Get("Accept"), http.StatusBadRequest)
			return
		}
		w.Write([]byte(diff))
	}))
	defer srv.Close()
	c := NewClient("t")
	c.RESTBase = srv.URL

	t.Run("whole diff", func(t *testing.T) {
		got, trunc, err := c.FetchDiff(context.Background(), "kubeflow/docs-agent", 7, 1000)
		if err != nil || trunc || got != diff {
			t.Errorf("got (%q, %v, %v)", got, trunc, err)
		}
	})
	t.Run("truncated at limit", func(t *testing.T) {
		got, trunc, err := c.FetchDiff(context.Background(), "kubeflow/docs-agent", 7, 10)
		if err != nil || !trunc || got != diff[:10] {
			t.Errorf("got (%q, %v, %v)", got, trunc, err)
		}
	})
	t.Run("http error", func(t *testing.T) {
		if _, _, err := c.FetchDiff(context.Background(), "kubeflow/docs-agent", 8, 10); err == nil {
			t.Error("expected error for a non-200 response")
		}
	})
}
