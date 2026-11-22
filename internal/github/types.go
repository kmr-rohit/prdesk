// Package github fetches open pull requests via GitHub's GraphQL API.
package github

import "time"

// PullRequest is prdesk's own flattened view of a PR. The classifier (step 3)
// only ever sees this type, never raw API JSON. That keeps the classifier a
// pure function: easy to test, and independent of GitHub's response shape.
type PullRequest struct {
	Repo              string // "owner/name"
	Number            int
	Title             string
	URL               string
	Draft             bool
	Author            string
	AuthorAssociation string // FIRST_TIME_CONTRIBUTOR, MEMBER, OWNER, ...
	Labels            []string
	ReviewRequests    []string // user logins or team slugs
	Reviews           []Review
	Comments          []Comment // most recent few
	HeadCommittedAt   time.Time // time of the latest push (approximated by last commit)
	CheckState        string    // SUCCESS, FAILURE, ERROR, PENDING, EXPECTED, or "" if no checks ran
	ReviewDecision    string    // APPROVED, CHANGES_REQUESTED, REVIEW_REQUIRED, or ""
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type Review struct {
	Author      string
	State       string // APPROVED, CHANGES_REQUESTED, COMMENTED, DISMISSED
	SubmittedAt time.Time
}

type Comment struct {
	Author    string
	CreatedAt time.Time
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
