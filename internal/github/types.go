// Package github fetches open pull requests via GitHub's GraphQL API.
package github

import "time"

// PullRequest is prdesk's own flattened view of a PR. The classifier (step 3)
// only ever sees this type, never raw API JSON. That keeps the classifier a
// pure function: easy to test, and independent of GitHub's response shape.
type PullRequest struct {
	Repo              string    `json:"repo"` // "owner/name"
	Number            int       `json:"number"`
	Title             string    `json:"title"`
	Body              string    `json:"body"` // description, truncated
	URL               string    `json:"url"`
	Draft             bool      `json:"draft"`
	Author            string    `json:"author"`
	AuthorAssociation string    `json:"authorAssociation"` // FIRST_TIME_CONTRIBUTOR, MEMBER, OWNER, ...
	Labels            []string  `json:"labels"`
	ReviewRequests    []string  `json:"reviewRequests"` // user logins or team slugs
	Reviews           []Review  `json:"reviews"`
	Comments          []Comment `json:"comments"`        // most recent few
	Files             []string  `json:"files"`           // changed paths (first 100)
	HeadCommittedAt   time.Time `json:"headCommittedAt"` // latest push (approximated by last commit)
	CheckState        string    `json:"checkState"`      // SUCCESS, FAILURE, PENDING, or "" if no checks ran (ignored checks excluded)
	FailedChecks      []string  `json:"failedChecks"`
	AwaitingApproval  bool      `json:"awaitingApproval"` // workflow runs blocked until a maintainer approves them
	ReviewDecision    string    `json:"reviewDecision"`   // APPROVED, CHANGES_REQUESTED, REVIEW_REQUIRED, or ""
	Mergeable         string    `json:"mergeable"`        // MERGEABLE, CONFLICTING, UNKNOWN
	HeadSHA           string    `json:"headSha"`          // commit the PR currently points at
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

type Review struct {
	Author      string    `json:"author"`
	State       string    `json:"state"` // APPROVED, CHANGES_REQUESTED, COMMENTED, DISMISSED
	SubmittedAt time.Time `json:"submittedAt"`
}

type Comment struct {
	Author    string    `json:"author"`
	CreatedAt time.Time `json:"createdAt"`
}

// HasLabel reports whether the PR carries the given label.
func (p PullRequest) HasLabel(name string) bool {
	for _, l := range p.Labels {
		if l == name {
			return true
		}
	}
	return false
}
