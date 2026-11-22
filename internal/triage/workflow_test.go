package triage

import (
	"strings"
	"testing"
)

// Table-driven test of the review workflow state machine. Each row is one
// moment in a PR's life, named by its fixture in testdata/prs.json.
func TestNextStep(t *testing.T) {
	evalPaths := func(o *Options) {
		o.EvalCommand = "/eval"
		o.EvalPaths = []string{"docs-agent-mcp/mcp-server/", "docs-agent-mcp/pipelines/"}
	}

	tests := []struct {
		fixture string
		tweak   func(*Options)
		want    Stage
		who     string
		copyHas []string // substrings the copy-paste comment must contain
		whyHas  string
	}{
		// 1. my review
		{"wf_unreviewed", nil, StageReview, "me", nil, "haven't reviewed"},
		{"wf_review_requested", nil, StageReview, "me", nil, "requested"},
		{"wf_commented_only", nil, StageReview, "me", nil, ""}, // a comment is not a decision
		{"wf_pushed_after_approval", nil, StageReview, "me", nil, "pushed"},

		// request changes -> author's turn
		{"wf_changes_requested_by_me", nil, StageWaitAuthor, "author", nil, "requested changes"},
		{"wf_conflict_after_approval", nil, StageWaitAuthor, "author", []string{"rebase"}, "conflicts"},

		// 2. ask the approver for /ok-to-test, only once I approved
		{"wf_approved_awaiting_ci", nil, StageAskOKToTest, "approver", []string{"@tarekabouzeid", "`/ok-to-test`"}, ""},
		{"wf_approved_no_ci", nil, StageAskOKToTest, "approver", []string{"/ok-to-test"}, ""},

		// 4. CI problems -> I request changes
		{"wf_approved_ci_failed", nil, StageFixCI, "me", []string{"lint"}, "lint"},
		{"wf_approved_ci_pending", nil, StageWaitCI, "ci", nil, ""},

		// 3. eval
		{"wf_approved_needs_eval", evalPaths, StageAskEval, "approver", []string{"@tarekabouzeid", "`/eval`"}, ""},
		{"wf_approved_eval_current", evalPaths, StageApprove, "me", []string{"/lgtm"}, ""},
		{"wf_approved_readme_only", evalPaths, StageApprove, "me", []string{"/lgtm"}, ""}, // no eval needed

		// 5. ok-to-merge
		{"wf_approved_eval_current", nil, StageApprove, "me", []string{"/lgtm"}, ""},
		{"wf_ready_labels", nil, StageMerge, "approver", []string{"@tarekabouzeid"}, ""},

		// no workflow
		{"wf_draft", nil, StageNone, "", nil, ""},
		{"wf_mine", nil, StageNone, "", nil, ""},
	}

	fixtures := loadFixtures(t)
	for _, tc := range tests {
		name := tc.fixture
		if tc.tweak != nil {
			name += "/eval-paths"
		}
		t.Run(name, func(t *testing.T) {
			pr, ok := fixtures[tc.fixture]
			if !ok {
				t.Fatalf("fixture %q missing", tc.fixture)
			}
			o := baseOptions()
			if tc.tweak != nil {
				tc.tweak(&o)
			}
			got := NextStep(pr, o)

			if got.Stage != tc.want || got.Who != tc.who {
				t.Fatalf("stage/who = %q/%q, want %q/%q (%+v)", got.Stage, got.Who, tc.want, tc.who, got)
			}
			if tc.want != StageNone && (got.Action == "" || got.Why == "") {
				t.Errorf("step needs an action and a reason: %+v", got)
			}
			for _, sub := range tc.copyHas {
				if !strings.Contains(got.CopyText, sub) {
					t.Errorf("CopyText %q missing %q", got.CopyText, sub)
				}
			}
			if !strings.Contains(got.Why, tc.whyHas) {
				t.Errorf("Why = %q, want it to contain %q", got.Why, tc.whyHas)
			}
		})
	}
}

// The ask for /ok-to-test must put the command in backticks: Prow only acts
// on a command at the START of a line, so inline code can't trigger the bot
// from my own comment.
func TestAskCommandsAreInlineCode(t *testing.T) {
	fixtures := loadFixtures(t)
	s := NextStep(fixtures["wf_approved_awaiting_ci"], baseOptions())
	for _, line := range strings.Split(s.CopyText, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "/") {
			t.Errorf("line starts with a slash command, the bot could act on it: %q", line)
		}
	}
}

func TestNoApproverConfigured(t *testing.T) {
	fixtures := loadFixtures(t)
	o := baseOptions()
	o.Approver = ""
	s := NextStep(fixtures["wf_approved_awaiting_ci"], o)
	if !strings.Contains(s.Action, "a maintainer") || strings.Contains(s.CopyText, "@ ") {
		t.Errorf("unexpected wording without approver: %+v", s)
	}
}
