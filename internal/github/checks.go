package github

import "slices"

// checkContext is one entry of statusCheckRollup.contexts. GraphQL returns a
// union: a CheckRun (GitHub Actions, DCO app, ...) or a legacy StatusContext
// (Prow's tide). Only the fields for the matching __typename are filled in.
type checkContext struct {
	Typename   string `json:"__typename"`
	Name       string `json:"name"`       // CheckRun
	Status     string `json:"status"`     // CheckRun: COMPLETED, IN_PROGRESS, QUEUED...
	Conclusion string `json:"conclusion"` // CheckRun: SUCCESS, FAILURE, ...
	Context    string `json:"context"`    // StatusContext name
	State      string `json:"state"`      // StatusContext: SUCCESS, FAILURE, ERROR, PENDING
}

func (c checkContext) label() string {
	if c.Typename == "StatusContext" {
		return c.Context
	}
	return c.Name
}

// summarizeChecks folds individual checks into one state, skipping ignored
// names. Failure beats pending beats success. No checks => "".
func summarizeChecks(ctxs []checkContext, ignored []string) (state string, failed []string) {
	pending, seen := false, false
	for _, c := range ctxs {
		if slices.Contains(ignored, c.label()) {
			continue
		}
		seen = true
		switch classifyCheck(c) {
		case "FAILURE":
			failed = append(failed, c.label())
		case "PENDING":
			pending = true
		}
	}
	switch {
	case len(failed) > 0:
		return "FAILURE", failed
	case pending:
		return "PENDING", nil
	case seen:
		return "SUCCESS", nil
	}
	return "", nil
}

func classifyCheck(c checkContext) string {
	if c.Typename == "StatusContext" {
		switch c.State {
		case "SUCCESS":
			return "SUCCESS"
		case "FAILURE", "ERROR":
			return "FAILURE"
		}
		return "PENDING" // PENDING, EXPECTED
	}
	if c.Status != "COMPLETED" {
		return "PENDING"
	}
	switch c.Conclusion {
	case "SUCCESS", "NEUTRAL", "SKIPPED":
		return "SUCCESS"
	}
	return "FAILURE" // FAILURE, TIMED_OUT, CANCELLED, ACTION_REQUIRED, STALE, ...
}
