package triage

import (
	"fmt"
	"strings"

	"github.com/rohikuma/prdesk/internal/github"
)

// The docs-agent review workflow, as a state machine over a PR:
//
//  1. I review; if something is off I request changes.
//  2. Once I am happy, I ask the approver to comment /ok-to-test (runs CI).
//  3. If CI is green and the change needs an eval, I ask them to comment /eval.
//  4. If CI or the eval shows problems, I request changes again.
//  5. Otherwise I give my ok-to-merge (/lgtm + approve).
//
// NextStep answers "what is the next thing to do on this PR, and who does it?"
// Unlike buckets (which overlap), a PR has exactly one next step.

type Stage string

const (
	StageNone        Stage = "" // draft, or my own PR: no review workflow
	StageReview      Stage = "review"
	StageApprove     Stage = "approve"
	StageAskOKToTest Stage = "ask-ok-to-test"
	StageAskEval     Stage = "ask-eval"
	StageFixCI       Stage = "fix-ci"
	StageWaitAuthor  Stage = "wait-author"
	StageWaitCI      Stage = "wait-ci"
	StageMerge       Stage = "merge"
)

// StageOrder is the display order: my own to-dos first, other people's last.
var StageOrder = []Stage{
	StageReview, StageApprove, StageAskOKToTest, StageAskEval,
	StageFixCI, StageWaitAuthor, StageWaitCI, StageMerge,
}

// Step is the single next action for a PR.
type Step struct {
	Stage     Stage  `json:"stage"`
	Who       string `json:"who"` // me | approver | author | ci
	Action    string `json:"action"`
	Why       string `json:"why"`
	CopyLabel string `json:"copyLabel,omitempty"` // button text, e.g. "Copy ask for /ok-to-test"
	CopyText  string `json:"copyText,omitempty"`  // ready-to-paste GitHub comment
}

func NextStep(pr github.PullRequest, o Options) Step {
	if pr.Draft || isMine(pr, o) {
		return Step{}
	}
	if pr.Mergeable == "CONFLICTING" {
		return Step{
			Stage: StageWaitAuthor, Who: "author", Action: "Ask the author to rebase", Why: "merge conflicts",
			CopyLabel: "Copy rebase request",
			CopyText:  "Could you please rebase on main? This PR currently has merge conflicts.",
		}
	}

	rev, reviewed := myDecision(pr, o)
	switch {
	case !reviewed:
		why := "you haven't reviewed it yet"
		if requestedFromMe(pr, o) {
			why = "review requested from you"
		}
		return Step{Stage: StageReview, Who: "me", Action: "Review it", Why: why}
	case pr.HeadCommittedAt.After(rev.SubmittedAt):
		return Step{Stage: StageReview, Who: "me", Action: "Re-review", Why: "the author pushed after your review"}
	case rev.State == "CHANGES_REQUESTED":
		return Step{Stage: StageWaitAuthor, Who: "author", Action: "Waiting for the author", Why: "you requested changes and there is no push since"}
	}

	// From here on I have approved the current head commit.
	approver := approverRef(o)
	switch {
	case pr.HasLabel("needs-ok-to-test") || pr.AwaitingApproval || pr.CheckState == "":
		return Step{
			Stage: StageAskOKToTest, Who: "approver", Action: "Ask " + approver + " to run CI",
			Why:       "you approved it and CI has not run",
			CopyLabel: "Copy /ok-to-test ask",
			CopyText: fmt.Sprintf("%s I've reviewed this and it looks good from my side. "+
				"Could you please comment `/ok-to-test` so CI runs?", approver),
		}
	case pr.CheckState == "FAILURE":
		failing := "CI"
		if len(pr.FailedChecks) > 0 {
			failing = strings.Join(pr.FailedChecks, ", ")
		}
		return Step{
			Stage: StageFixCI, Who: "me", Action: "Request changes: CI is failing", Why: "failing: " + failing,
			CopyLabel: "Copy CI request",
			CopyText:  fmt.Sprintf("Thanks for the update! CI is failing on: %s. Could you please take a look and push a fix? `/retest` if it looks flaky.", failing),
		}
	case pr.CheckState == "PENDING":
		return Step{Stage: StageWaitCI, Who: "ci", Action: "Wait for CI", Why: "checks are still running"}
	}

	// CI is green.
	if _, needs := needsEval(pr, o); needs {
		why := "CI is green and it touches eval paths"
		if hasEvalLabel(pr) {
			why = "the previous eval is stale after a new push"
		}
		cmd := o.EvalCommand
		return Step{
			Stage: StageAskEval, Who: "approver", Action: "Ask " + approver + " to run " + cmd, Why: why,
			CopyLabel: "Copy " + cmd + " ask",
			CopyText: fmt.Sprintf("%s CI is green. Could you please comment `%s` so we can compare this against main?",
				approver, cmd),
		}
	}
	if _, ready := readyToMerge(pr, o); ready {
		return Step{
			Stage: StageMerge, Who: "approver", Action: "Ready for " + approver + " to merge", Why: "lgtm + approved, checks green",
			CopyLabel: "Copy merge ask",
			CopyText:  fmt.Sprintf("%s this has lgtm and approved with green checks. Ready to merge from my side.", approver),
		}
	}
	return Step{
		Stage: StageApprove, Who: "me", Action: "Ok to merge from your side: /lgtm", Why: "CI green, eval done or not needed",
		CopyLabel: "Copy /lgtm", CopyText: "/lgtm",
	}
}

// approverRef is how to address the approver in a comment.
func approverRef(o Options) string {
	if o.Approver == "" {
		return "a maintainer"
	}
	return "@" + strings.TrimPrefix(o.Approver, "@")
}

// myDecision is my latest APPROVED or CHANGES_REQUESTED review. COMMENTED and
// DISMISSED reviews are not decisions.
func myDecision(pr github.PullRequest, o Options) (github.Review, bool) {
	var best github.Review
	var found bool
	for _, r := range pr.Reviews {
		if !strings.EqualFold(r.Author, o.Login) || (r.State != "APPROVED" && r.State != "CHANGES_REQUESTED") {
			continue
		}
		if !found || r.SubmittedAt.After(best.SubmittedAt) {
			best, found = r, true
		}
	}
	return best, found
}
