package triage

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/rohikuma/prdesk/internal/github"
)

func loadFixtures(t *testing.T) map[string]github.PullRequest {
	t.Helper()
	data, err := os.ReadFile("testdata/prs.json")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]github.PullRequest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// baseOptions is "me = kmr-rohit, on Sunday 2026-11-22", so every fixture's
// dates are relative to a fixed clock and the tests never flake.
func baseOptions() Options {
	return Options{
		Login:                "kmr-rohit",
		Approver:             "tarekabouzeid",
		Teams:                []string{"docs-reviewers"},
		StaleAfter:           14 * 24 * time.Hour,
		AgendaLabel:          "community-meeting",
		ReadyLabels:          []string{"lgtm", "approved"},
		NewContributorWindow: 7 * 24 * time.Hour,
		Now:                  time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC),
	}
}

// Each row: which fixture PR, any option tweaks, and the EXACT buckets
// expected (in AllBuckets order). An empty want means "needs no attention".
func TestClassify(t *testing.T) {
	withEval := func(o *Options) { o.EvalCommand = "/eval" }

	withEvalPaths := func(o *Options) {
		o.EvalCommand = "/eval"
		o.EvalPaths = []string{"docs-agent-mcp/mcp-server/", "docs-agent-mcp/pipelines/"}
	}

	tests := []struct {
		fixture string
		tweak   func(*Options) // nil = baseOptions as-is
		want    []Bucket
	}{
		// needs-ok-to-test / needs-ci
		{"first_timer_no_ci", nil, []Bucket{NeedsOKToTest, Agenda}},
		{"prow_label_needs_ok", nil, []Bucket{NeedsOKToTest}}, // not also needs-ci
		{"contributor_ci_failed", nil, []Bucket{NeedsCI}},
		{"contributor_no_ci", nil, []Bucket{NeedsCI}},
		{"contributor_awaiting_approval", nil, []Bucket{NeedsOKToTest}},      // DCO green, Actions blocked
		{"contributor_awaiting_approval", withEval, []Bucket{NeedsOKToTest}}, // never eval before CI ran
		{"failed_checks_named", nil, []Bucket{NeedsCI}},
		{"draft_by_other", nil, nil}, // drafts don't ask for attention

		// needs-eval (only when eval_command is configured)
		{"ci_green_no_eval", withEval, []Bucket{NeedsEval}},
		{"ci_green_no_eval", nil, nil}, // eval disabled => nothing
		{"green_touches_mcp", withEvalPaths, []Bucket{NeedsEval}},
		{"green_readme_only", withEvalPaths, nil},            // outside eval_paths: unit tests are enough
		{"green_readme_only", withEval, []Bucket{NeedsEval}}, // no eval_paths => every PR
		{"eval_current", withEval, nil},
		{"eval_stale_after_push", withEval, []Bucket{NeedsEval}},

		// waiting-on-me / waiting-on-author
		{"review_requested_me", nil, []Bucket{WaitingOnMe}},
		{"review_requested_team", nil, []Bucket{WaitingOnMe}},
		{"author_pushed_after_my_review", nil, []Bucket{WaitingOnMe}}, // not waiting-on-author: they pushed
		{"changes_requested_no_push", nil, []Bucket{WaitingOnAuthor}},
		{"merge_conflict", nil, []Bucket{WaitingOnAuthor}},
		{"merge_conflict", withEval, []Bucket{WaitingOnAuthor}}, // no eval on a PR that can't merge

		// ready-to-merge
		{"ready_lgtm_approved", nil, []Bucket{ReadyToMerge}},
		{"ready_but_on_hold", nil, nil},
		{"only_lgtm", nil, nil}, // needs lgtm AND approved

		// stale / mine / agenda
		{"idle_21_days", nil, []Bucket{Stale}},
		{"mine_changes_requested", nil, []Bucket{Mine}}, // my own PR is never waiting-on-author
		{"mine_draft_idle", nil, []Bucket{Stale, Mine}},
		{"mine_ci_failed", nil, []Bucket{NeedsCI, Mine}},
		{"agenda_label", nil, []Bucket{Agenda}},
	}

	fixtures := loadFixtures(t)
	for _, tc := range tests {
		name := tc.fixture
		if tc.tweak != nil {
			name += "/with-opts"
		}
		t.Run(name, func(t *testing.T) {
			pr, ok := fixtures[tc.fixture]
			if !ok {
				t.Fatalf("fixture %q not found in testdata/prs.json", tc.fixture)
			}
			opts := baseOptions()
			if tc.tweak != nil {
				tc.tweak(&opts)
			}

			res := Classify(pr, opts)

			var got []Bucket
			for _, f := range res.Findings {
				got = append(got, f.Bucket)
				if f.Action == "" {
					t.Errorf("bucket %s has no suggested action", f.Bucket)
				}
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("buckets = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestGroup(t *testing.T) {
	fixtures := loadFixtures(t)
	prs := []github.PullRequest{fixtures["mine_draft_idle"], fixtures["idle_21_days"], fixtures["ready_lgtm_approved"]}
	g := Group(ClassifyAll(prs, baseOptions()))

	if n := len(g[Stale]); n != 2 {
		t.Errorf("stale bucket has %d PRs, want 2", n)
	}
	if n := len(g[ReadyToMerge]); n != 1 {
		t.Errorf("ready bucket has %d PRs, want 1", n)
	}
	if g[Stale][0].PR.Number != 18 { // input order preserved
		t.Errorf("first stale PR = #%d, want #18", g[Stale][0].PR.Number)
	}
}
