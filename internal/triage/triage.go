// Package triage classifies pull requests into maintainer buckets.
//
// Design rule: Classify is a PURE function. Same PR + same Options => same
// answer. No network, no clock (Options.Now is passed in), no globals that
// change. That is what makes the table-driven tests trivial to write.
package triage

import (
	"time"

	"github.com/rohikuma/prdesk/internal/config"
	"github.com/rohikuma/prdesk/internal/github"
)

// GO LESSON 9: Go has no enums. The idiom is a named type plus constants.
// Using `string` underneath means buckets print and marshal to JSON readably.
type Bucket string

const (
	NeedsOKToTest   Bucket = "needs-ok-to-test"
	NeedsCI         Bucket = "needs-ci"
	NeedsEval       Bucket = "needs-eval"
	WaitingOnMe     Bucket = "waiting-on-me"
	WaitingOnAuthor Bucket = "waiting-on-author"
	ReadyToMerge    Bucket = "ready-to-merge"
	Stale           Bucket = "stale"
	Mine            Bucket = "mine"
	Agenda          Bucket = "agenda"
)

// AllBuckets is the display order, and the order findings are reported in.
var AllBuckets = []Bucket{
	NeedsOKToTest, NeedsCI, NeedsEval, WaitingOnMe, WaitingOnAuthor,
	ReadyToMerge, Stale, Mine, Agenda,
}

// Options is everything the classifier needs to know about "me" and policy.
type Options struct {
	Login                string
	Teams                []string      // team slugs whose review requests count as mine
	StaleAfter           time.Duration // idle longer than this => stale
	AgendaLabel          string
	ReadyLabels          []string // ALL must be present for ready-to-merge
	EvalCommand          string   // e.g. "/eval"; empty disables needs-eval
	EvalPaths            []string // if set, only PRs touching these prefixes need eval
	Approver             string   // login who runs /ok-to-test and /eval for me
	NewContributorWindow time.Duration
	Now                  time.Time
}

func OptionsFromConfig(c *config.Config, now time.Time) Options {
	return Options{
		Login:                c.Login,
		Teams:                c.Teams,
		StaleAfter:           time.Duration(c.StaleDays) * 24 * time.Hour,
		AgendaLabel:          c.AgendaLabel,
		ReadyLabels:          c.ReadyLabels,
		EvalCommand:          c.EvalCommand,
		EvalPaths:            c.EvalPaths,
		Approver:             c.Approver,
		NewContributorWindow: 7 * 24 * time.Hour,
		Now:                  now,
	}
}

// Finding says a PR is in a bucket and what the maintainer should do about it.
type Finding struct {
	Bucket Bucket `json:"bucket"`
	Action string `json:"action"`
}

// Result is one PR plus every bucket it landed in (possibly none).
type Result struct {
	PR       github.PullRequest `json:"pr"`
	Findings []Finding          `json:"findings"`
	Step     Step               `json:"step"` // where this PR is in my review workflow
}

// In reports whether the PR is in bucket b, and the suggested action.
func (r Result) In(b Bucket) (action string, ok bool) {
	for _, f := range r.Findings {
		if f.Bucket == b {
			return f.Action, true
		}
	}
	return "", false
}

// rule decides one bucket. Returning ok=false means "not in this bucket".
type rule func(pr github.PullRequest, o Options) (action string, ok bool)

// GO LESSON 10: functions are values. The rule table below pairs each bucket
// with a function, so adding a bucket = writing one function + one table row.
// Order matters: some rules (needs-ci) consult earlier ones.
var rules = []struct {
	bucket Bucket
	fn     rule
}{
	{NeedsOKToTest, needsOKToTest},
	{NeedsCI, needsCI},
	{NeedsEval, needsEval},
	{WaitingOnMe, waitingOnMe},
	{WaitingOnAuthor, waitingOnAuthor},
	{ReadyToMerge, readyToMerge},
	{Stale, stale},
	{Mine, mine},
	{Agenda, agenda},
}

// Classify runs every rule against one PR.
func Classify(pr github.PullRequest, o Options) Result {
	res := Result{PR: pr, Step: NextStep(pr, o)}
	for _, r := range rules {
		if action, ok := r.fn(pr, o); ok {
			res.Findings = append(res.Findings, Finding{Bucket: r.bucket, Action: action})
		}
	}
	return res
}

// ClassifyAll classifies every PR, preserving input order.
func ClassifyAll(prs []github.PullRequest, o Options) []Result {
	out := make([]Result, 0, len(prs))
	for _, pr := range prs {
		out = append(out, Classify(pr, o))
	}
	return out
}

// Group indexes results by bucket. Display code iterates AllBuckets for order.
func Group(results []Result) map[Bucket][]Result {
	g := make(map[Bucket][]Result)
	for _, r := range results {
		for _, f := range r.Findings {
			g[f.Bucket] = append(g[f.Bucket], r)
		}
	}
	return g
}
