package github

import "time"

// These types mirror the raw GraphQL JSON shape. They stay private
// (lowercase): only toPullRequest below converts them into our clean type.

type openPRsResponse struct {
	Repository struct {
		PullRequests struct {
			PageInfo struct {
				HasNextPage bool   `json:"hasNextPage"`
				EndCursor   string `json:"endCursor"`
			} `json:"pageInfo"`
			Nodes []prNode `json:"nodes"`
		} `json:"pullRequests"`
	} `json:"repository"`
}

// actor is nullable in GraphQL: deleted accounts come back as `null`.
type actor struct {
	Login string `json:"login"`
}

// GO LESSON 7: methods work on nil pointers. Calling a.name() when a == nil is
// legal as long as the method checks for it, so no nil-check at every call site.
func (a *actor) name() string {
	if a == nil {
		return "ghost" // GitHub's own name for deleted users
	}
	return a.Login
}

type prNode struct {
	Number            int       `json:"number"`
	Title             string    `json:"title"`
	URL               string    `json:"url"`
	IsDraft           bool      `json:"isDraft"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
	AuthorAssociation string    `json:"authorAssociation"`
	ReviewDecision    string    `json:"reviewDecision"` // null => ""
	Mergeable         string    `json:"mergeable"`
	HeadRefOid        string    `json:"headRefOid"`
	Author            *actor    `json:"author"`
	Labels            struct {
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"labels"`
	ReviewRequests struct {
		Nodes []struct {
			RequestedReviewer *struct {
				Login string `json:"login"` // set when it's a User
				Slug  string `json:"slug"`  // set when it's a Team
			} `json:"requestedReviewer"`
		} `json:"nodes"`
	} `json:"reviewRequests"`
	Reviews struct {
		Nodes []struct {
			State       string    `json:"state"`
			SubmittedAt time.Time `json:"submittedAt"`
			Author      *actor    `json:"author"`
		} `json:"nodes"`
	} `json:"reviews"`
	Comments struct {
		Nodes []struct {
			CreatedAt time.Time `json:"createdAt"`
			Author    *actor    `json:"author"`
		} `json:"nodes"`
	} `json:"comments"`
	Commits struct {
		Nodes []struct {
			Commit struct {
				CommittedDate     time.Time `json:"committedDate"`
				StatusCheckRollup *struct {
					Contexts struct {
						Nodes []checkContext `json:"nodes"`
					} `json:"contexts"`
				} `json:"statusCheckRollup"`
				CheckSuites struct {
					Nodes []struct {
						Conclusion string `json:"conclusion"`
					} `json:"nodes"`
				} `json:"checkSuites"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
}

func (n prNode) toPullRequest(repo string, ignoredChecks []string) PullRequest {
	pr := PullRequest{
		Repo:              repo,
		Number:            n.Number,
		Title:             n.Title,
		URL:               n.URL,
		Draft:             n.IsDraft,
		Author:            n.Author.name(),
		AuthorAssociation: n.AuthorAssociation,
		ReviewDecision:    n.ReviewDecision,
		Mergeable:         n.Mergeable,
		HeadSHA:           n.HeadRefOid,
		CreatedAt:         n.CreatedAt,
		UpdatedAt:         n.UpdatedAt,
	}
	for _, l := range n.Labels.Nodes {
		pr.Labels = append(pr.Labels, l.Name)
	}
	for _, rr := range n.ReviewRequests.Nodes {
		if rr.RequestedReviewer == nil {
			continue
		}
		if rr.RequestedReviewer.Login != "" {
			pr.ReviewRequests = append(pr.ReviewRequests, rr.RequestedReviewer.Login)
		} else {
			pr.ReviewRequests = append(pr.ReviewRequests, rr.RequestedReviewer.Slug)
		}
	}
	for _, r := range n.Reviews.Nodes {
		pr.Reviews = append(pr.Reviews, Review{Author: r.Author.name(), State: r.State, SubmittedAt: r.SubmittedAt})
	}
	for _, c := range n.Comments.Nodes {
		pr.Comments = append(pr.Comments, Comment{Author: c.Author.name(), CreatedAt: c.CreatedAt})
	}
	if len(n.Commits.Nodes) > 0 {
		c := n.Commits.Nodes[0].Commit
		pr.HeadCommittedAt = c.CommittedDate
		if c.StatusCheckRollup != nil {
			pr.CheckState, pr.FailedChecks = summarizeChecks(c.StatusCheckRollup.Contexts.Nodes, ignoredChecks)
		}
		for _, cs := range c.CheckSuites.Nodes {
			// GitHub Actions runs from first-time contributors sit as
			// ACTION_REQUIRED until a maintainer approves them.
			if cs.Conclusion == "ACTION_REQUIRED" {
				pr.AwaitingApproval = true
			}
		}
	}
	return pr
}
