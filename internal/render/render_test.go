package render

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/rohikuma/prdesk/internal/github"
	"github.com/rohikuma/prdesk/internal/triage"
)

var now = time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)

func TestAge(t *testing.T) {
	tests := []struct {
		in   time.Duration
		want string
	}{
		{-time.Minute, "0m"}, // clock skew must not print "-1m"
		{5 * time.Minute, "5m"},
		{7 * time.Hour, "7h"},
		{3 * 24 * time.Hour, "3d"},
		{59 * 24 * time.Hour, "59d"},
		{90 * 24 * time.Hour, "3mo"},
		{400 * 24 * time.Hour, "1y"},
	}
	for _, tc := range tests {
		if got := Age(tc.in); got != tc.want {
			t.Errorf("Age(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("short", 10); got != "short" {
		t.Errorf("got %q", got)
	}
	if got := truncate("héllo wörld ✨✨✨", 8); got != "héllo w…" {
		t.Errorf("got %q (must cut on rune boundaries)", got)
	}
}

func sample() []triage.Result {
	pr := github.PullRequest{
		Repo: "kubeflow/docs-agent", Number: 7, Title: "fix: thing", URL: "https://x/7", Author: "alice",
		CreatedAt: now.Add(-10 * 24 * time.Hour), UpdatedAt: now.Add(-3 * time.Hour),
	}
	return []triage.Result{{PR: pr, Findings: []triage.Finding{{Bucket: triage.WaitingOnMe, Action: "review requested from you"}}}}
}

func TestTables(t *testing.T) {
	var buf bytes.Buffer
	if err := Tables(&buf, sample(), now, nil); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"waiting-on-me (1)", "SUGGESTED ACTION", "#7", "alice", "10d", "3h ago", "review requested from you"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "kubeflow/docs-agent#7") {
		t.Error("single-repo output should not repeat the repo name")
	}
}

func TestTablesBucketFilter(t *testing.T) {
	var buf bytes.Buffer
	_ = Tables(&buf, sample(), now, map[triage.Bucket]bool{triage.Stale: true})
	if buf.Len() != 0 {
		t.Errorf("filtered to stale, expected no output, got:\n%s", buf.String())
	}
}

func TestJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := JSON(&buf, sample(), now, nil); err != nil {
		t.Fatal(err)
	}
	var rep jsonReport
	if err := json.Unmarshal(buf.Bytes(), &rep); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	prs := rep.Buckets["waiting-on-me"]
	if rep.Counts["waiting-on-me"] != 1 || len(prs) != 1 || prs[0].Number != 7 || prs[0].AgeDays != 10 || prs[0].IdleDays != 0 {
		t.Errorf("unexpected report: %+v", rep)
	}
}
