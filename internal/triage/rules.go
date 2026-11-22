package triage

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/rohikuma/prdesk/internal/github"
)

// Policy notes (decisions made for this repo, see Tasks.md PR tracker):
//   - Draft PRs are never flagged needs-*, waiting-*, or ready: they are not
//     asking for attention yet. They can still be stale or mine.
//   - Kubeflow's docs-agent rarely uses an ok-to-test label. The real signal is
//     GitHub Actions runs stuck as ACTION_REQUIRED (AwaitingApproval). A
//     first-time contributor with no checks at all counts too.
//   - CheckState already ignores Prow's `tide`, which is always pending.
//   - /eval is tied to a commit: it writes labels `eval` and `eval-sha:<sha>`,
//     and a new push makes that eval stale.

func needsOKToTest(pr github.PullRequest, o Options) (string, bool) {
	if pr.Draft {
		return "", false
	}
	if pr.HasLabel("needs-ok-to-test") || pr.AwaitingApproval || (isFirstTimer(pr) && pr.CheckState == "") {
		return "/ok-to-test, or approve the pending workflow runs", true
	}
	return "", false
}

func needsCI(pr github.PullRequest, o Options) (string, bool) {
	if pr.Draft {
		return "", false
	}
	// Don't double-report: if ok-to-test is the reason CI hasn't run, say that.
	if _, blocked := needsOKToTest(pr, o); blocked {
		return "", false
	}
	switch pr.CheckState {
	case "":
		return "CI has not run: approve workflow runs or /retest", true
	case "FAILURE":
		if len(pr.FailedChecks) > 0 {
			return "failing: " + strings.Join(pr.FailedChecks, ", ") + " (read logs, /retest if flaky)", true
		}
		return "CI failing: read the logs, /retest if flaky", true
	}
	return "", false
}

func needsEval(pr github.PullRequest, o Options) (string, bool) {
	if o.EvalCommand == "" || pr.Draft || pr.AwaitingApproval || pr.CheckState != "SUCCESS" || pr.Mergeable == "CONFLICTING" {
		return "", false // eval only makes sense once CI is green and the PR merges cleanly
	}
	if !touchesEvalPaths(pr, o) {
		return "", false // e.g. README-only or test-only change: unit tests are enough
	}
	if evalIsCurrent(pr) {
		return "", false
	}
	if hasEvalLabel(pr) {
		return "eval is stale (new push): comment " + o.EvalCommand, true
	}
	return "CI green, no eval yet: comment " + o.EvalCommand, true
}

func waitingOnMe(pr github.PullRequest, o Options) (string, bool) {
	if pr.Draft || isMine(pr, o) {
		return "", false
	}
	if requestedFromMe(pr, o) {
		return "review requested from you", true
	}
	// I already reviewed/commented, and the author has moved since.
	if last := myLastTouch(pr, o); !last.IsZero() && authorActedAfter(pr, last) {
		return "author updated since your last review: re-review", true
	}
	return "", false
}

func waitingOnAuthor(pr github.PullRequest, o Options) (string, bool) {
	if pr.Draft || isMine(pr, o) {
		return "", false
	}
	if pr.Mergeable == "CONFLICTING" {
		return "merge conflicts: ask author to rebase", true
	}
	if pr.ReviewDecision == "CHANGES_REQUESTED" {
		// ReviewDecision is GitHub's aggregate; the push check is ours.
		if t, ok := latestReviewTime(pr, "CHANGES_REQUESTED"); ok && !pr.HeadCommittedAt.After(t) {
			return "changes requested, no push since", true
		}
	}
	return "", false
}

func readyToMerge(pr github.PullRequest, o Options) (string, bool) {
	if pr.Draft || pr.AwaitingApproval || pr.CheckState != "SUCCESS" || pr.Mergeable == "CONFLICTING" {
		return "", false
	}
	for _, l := range pr.Labels {
		if strings.HasPrefix(l, "do-not-merge/") { // Prow hold / WIP labels
			return "", false
		}
	}
	if len(o.ReadyLabels) == 0 {
		if pr.ReviewDecision != "APPROVED" {
			return "", false
		}
	} else {
		for _, want := range o.ReadyLabels {
			if !pr.HasLabel(want) {
				return "", false
			}
		}
	}
	return "approved and green: merge", true
}

func stale(pr github.PullRequest, o Options) (string, bool) {
	if o.StaleAfter <= 0 {
		return "", false
	}
	idle := o.Now.Sub(pr.UpdatedAt)
	if idle < o.StaleAfter {
		return "", false
	}
	return fmt.Sprintf("idle %d days: ping author or close", int(idle.Hours()/24)), true
}

func mine(pr github.PullRequest, o Options) (string, bool) {
	if !isMine(pr, o) {
		return "", false
	}
	switch {
	case pr.Mergeable == "CONFLICTING":
		return "yours: rebase needed", true
	case pr.ReviewDecision == "CHANGES_REQUESTED":
		return "yours: address requested changes", true
	case pr.ReviewDecision == "APPROVED":
		return "yours: approved, waiting on merge", true
	default:
		return "yours: waiting on reviewers", true
	}
}

func agenda(pr github.PullRequest, o Options) (string, bool) {
	if o.AgendaLabel != "" && pr.HasLabel(o.AgendaLabel) {
		return "on the meeting agenda (label)", true
	}
	if isFirstTimer(pr) && o.Now.Sub(pr.CreatedAt) <= o.NewContributorWindow {
		return "new first-time contributor this week: welcome them", true
	}
	return "", false
}

// ---- helpers ----------------------------------------------------------

func isMine(pr github.PullRequest, o Options) bool {
	return strings.EqualFold(pr.Author, o.Login) // GitHub logins are case-insensitive
}

func isFirstTimer(pr github.PullRequest) bool {
	return pr.AuthorAssociation == "FIRST_TIME_CONTRIBUTOR" || pr.AuthorAssociation == "FIRST_TIMER"
}

func requestedFromMe(pr github.PullRequest, o Options) bool {
	for _, r := range pr.ReviewRequests {
		if strings.EqualFold(r, o.Login) || slices.Contains(o.Teams, r) {
			return true
		}
	}
	return false
}

// myLastTouch is the latest time I reviewed or commented (zero if never).
// GO LESSON 11: time.Time{} (the zero value) is "never"; check with IsZero().
func myLastTouch(pr github.PullRequest, o Options) time.Time {
	var last time.Time
	for _, r := range pr.Reviews {
		if strings.EqualFold(r.Author, o.Login) && r.SubmittedAt.After(last) {
			last = r.SubmittedAt
		}
	}
	for _, c := range pr.Comments {
		if strings.EqualFold(c.Author, o.Login) && c.CreatedAt.After(last) {
			last = c.CreatedAt
		}
	}
	return last
}

// authorActedAfter: the PR author commented or pushed after t.
func authorActedAfter(pr github.PullRequest, t time.Time) bool {
	if pr.HeadCommittedAt.After(t) {
		return true
	}
	for _, c := range pr.Comments {
		if strings.EqualFold(c.Author, pr.Author) && c.CreatedAt.After(t) {
			return true
		}
	}
	return false
}

// latestReviewTime returns when the newest review with the given state was submitted.
func latestReviewTime(pr github.PullRequest, state string) (time.Time, bool) {
	var best time.Time
	found := false
	for _, r := range pr.Reviews {
		if r.State == state && (!found || r.SubmittedAt.After(best)) {
			best, found = r.SubmittedAt, true
		}
	}
	return best, found
}

const evalSHAPrefix = "eval-sha:"

func hasEvalLabel(pr github.PullRequest) bool {
	return pr.HasLabel("eval") || evalSHALabel(pr) != ""
}

func evalSHALabel(pr github.PullRequest) string {
	for _, l := range pr.Labels {
		if sha, ok := strings.CutPrefix(l, evalSHAPrefix); ok {
			return sha
		}
	}
	return ""
}

// evalIsCurrent: the eval-sha label matches the commit the PR points at now.
// Prefix-match both ways because the label may hold a short or a full SHA.
func evalIsCurrent(pr github.PullRequest) bool {
	sha := evalSHALabel(pr)
	if sha == "" || pr.HeadSHA == "" {
		return false
	}
	return strings.HasPrefix(pr.HeadSHA, sha) || strings.HasPrefix(sha, pr.HeadSHA)
}

// touchesEvalPaths: true if no eval_paths are configured, or the PR changes a
// file under one of them.
func touchesEvalPaths(pr github.PullRequest, o Options) bool {
	if len(o.EvalPaths) == 0 {
		return true
	}
	for _, f := range pr.Files {
		for _, prefix := range o.EvalPaths {
			if strings.HasPrefix(f, prefix) {
				return true
			}
		}
	}
	return false
}
