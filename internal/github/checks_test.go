package github

import (
	"slices"
	"testing"
)

func TestSummarizeChecks(t *testing.T) {
	run := func(name, status, concl string) checkContext {
		return checkContext{Typename: "CheckRun", Name: name, Status: status, Conclusion: concl}
	}
	status := func(name, state string) checkContext {
		return checkContext{Typename: "StatusContext", Context: name, State: state}
	}
	tests := []struct {
		name       string
		ctxs       []checkContext
		wantState  string
		wantFailed []string
	}{
		{"no checks", nil, "", nil},
		{"only ignored tide pending", []checkContext{status("tide", "PENDING")}, "", nil},
		{"dco ok, tide pending", []checkContext{status("tide", "PENDING"), run("DCO", "COMPLETED", "SUCCESS")}, "SUCCESS", nil},
		{"failure wins over pending", []checkContext{run("lint", "COMPLETED", "FAILURE"), run("test", "IN_PROGRESS", "")}, "FAILURE", []string{"lint"}},
		{"pending", []checkContext{run("test", "QUEUED", ""), run("DCO", "COMPLETED", "SUCCESS")}, "PENDING", nil},
		{"skipped and neutral are fine", []checkContext{run("a", "COMPLETED", "SKIPPED"), run("b", "COMPLETED", "NEUTRAL")}, "SUCCESS", nil},
		{"legacy status error", []checkContext{status("ci/prow/unit", "ERROR")}, "FAILURE", []string{"ci/prow/unit"}},
		{"cancelled counts as failure", []checkContext{run("e2e", "COMPLETED", "CANCELLED")}, "FAILURE", []string{"e2e"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			state, failed := summarizeChecks(tc.ctxs, []string{"tide"})
			if state != tc.wantState || !slices.Equal(failed, tc.wantFailed) {
				t.Errorf("got (%q, %v), want (%q, %v)", state, failed, tc.wantState, tc.wantFailed)
			}
		})
	}
}
