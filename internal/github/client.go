package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const defaultEndpoint = "https://api.github.com/graphql"

// Client talks to the GitHub GraphQL API.
//
// GO LESSON 5: no constructors or classes. A struct plus a `NewX` function is
// the convention. Exported fields (Endpoint) can be changed by callers; we use
// that in tests to point the client at a fake local server.
type Client struct {
	Endpoint string
	// IgnoredChecks are check/status names excluded from CheckState. Default
	// {"tide"}: Prow's merge gate is "pending" on nearly every PR by design.
	IgnoredChecks []string
	token         string
	http          *http.Client
}

func NewClient(token string) *Client {
	return &Client{
		Endpoint:      defaultEndpoint,
		IgnoredChecks: []string{"tide"},
		token:         token,
		http:          &http.Client{Timeout: 30 * time.Second},
	}
}

const openPRsQuery = `
query($owner: String!, $name: String!, $cursor: String) {
  rateLimit { cost remaining resetAt }
  repository(owner: $owner, name: $name) {
    pullRequests(states: OPEN, first: 50, after: $cursor,
                 orderBy: {field: UPDATED_AT, direction: DESC}) {
      pageInfo { hasNextPage endCursor }
      nodes {
        number title url isDraft createdAt updatedAt
        authorAssociation reviewDecision mergeable headRefOid
        author { login }
        labels(first: 30) { nodes { name } }
        reviewRequests(first: 20) {
          nodes { requestedReviewer { __typename ... on User { login } ... on Team { slug } } }
        }
        reviews(last: 30) { nodes { state submittedAt author { login } } }
        comments(last: 10) { nodes { createdAt author { login } } }
        commits(last: 1) {
          nodes { commit {
            committedDate
            statusCheckRollup { contexts(first: 50) { nodes {
              __typename
              ... on CheckRun { name status conclusion }
              ... on StatusContext { context state }
            } } }
            checkSuites(first: 20) { nodes { conclusion app { slug } } }
          } }
        }
      }
    }
  }
}`

// FetchOpenPRs returns every open PR in repo ("owner/name"), following
// pagination until GitHub says there are no more pages.
//
// GO LESSON 6: `ctx context.Context` is always the first parameter of anything
// that does I/O. It carries cancellation and deadlines: if the user hits
// Ctrl-C or a timeout fires, the HTTP request is aborted.
func (c *Client) FetchOpenPRs(ctx context.Context, repo string) ([]PullRequest, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return nil, fmt.Errorf("repo %q must be owner/name", repo)
	}

	var all []PullRequest
	var cursor *string // nil => JSON null => first page
	for {
		var resp openPRsResponse
		vars := map[string]any{"owner": owner, "name": name, "cursor": cursor}
		if err := c.do(ctx, openPRsQuery, vars, &resp); err != nil {
			return nil, fmt.Errorf("fetching %s: %w", repo, err)
		}

		page := resp.Repository.PullRequests
		for _, n := range page.Nodes {
			all = append(all, n.toPullRequest(repo, c.IgnoredChecks))
		}
		if !page.PageInfo.HasNextPage {
			return all, nil
		}
		cursor = &page.PageInfo.EndCursor
	}
}

// do sends one GraphQL request and decodes the "data" part into out.
func (c *Client) do(ctx context.Context, query string, vars map[string]any, out any) error {
	body, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close() // `defer` runs when the function returns, however it returns

	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	if res.StatusCode != http.StatusOK {
		// Step 5 will turn 403/429 into proper rate-limit backoff.
		return fmt.Errorf("github returned %s: %s", res.Status, truncate(string(raw), 200))
	}

	// GraphQL returns HTTP 200 even for query errors; they live in "errors".
	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}
	if len(envelope.Errors) > 0 {
		return fmt.Errorf("graphql error: %s", envelope.Errors[0].Message)
	}
	return json.Unmarshal(envelope.Data, out)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
