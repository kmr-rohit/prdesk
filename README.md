# prdesk

Maintainer triage for GitHub PRs. Classifies open PRs into buckets
(waiting-on-me, needs-ok-to-test, needs-ci, needs-eval, waiting-on-author,
ready-to-merge, stale, mine, agenda) so nothing is missed before a community meeting.

## Setup

```bash
brew install go
cp config.example.yaml config.yaml     # set your login and repos
export GITHUB_TOKEN=github_pat_...     # read-only token is enough
```

## Use

```bash
go build -o prdesk ./cmd/prdesk

./prdesk triage                              # tables in the terminal
./prdesk triage --bucket waiting-on-me,stale
./prdesk triage --json                       # grouped JSON

./prdesk serve                               # dashboard at http://127.0.0.1:8080
./prdesk serve --addr 127.0.0.1:9000 --ttl 5m
```

The dashboard caches GitHub data for `--ttl` (default 2m), the browser re-polls every
`--refresh` (default 1m), and `/api/triage.json` serves the same JSON as the CLI.
If GitHub is unreachable it keeps showing the last good data with a warning.

## Review workflow

Each PR gets exactly one **next step**, following the docs-agent flow:

1. **Review needed**: you haven't approved/requested changes yet, or the author pushed since.
2. **Waiting on author**: you requested changes (or the PR has merge conflicts).
3. **Ask the approver: /ok-to-test**: you approved but CI hasn't run. A copy button gives you the comment to post.
4. **Ask the approver: /eval**: CI is green and the PR touches `eval_paths`.
5. **CI failing: request changes**, **CI running**, **Ok to merge from my side** (`/lgtm`), **Ready to merge**.

Set `approver:` in `config.yaml` to the maintainer who runs the slash commands. Note that only an
**Approve** or **Request changes** review counts as your decision; a plain "Comment" review does not.

## AI analysis (Ollama)

With `ai.enabled: true`, every row gets an **Analyze with AI** button. It sends the PR description and
diff to your local Ollama model and shows a summary, risk, needs-eval hint, things to check by hand and a
**draft review comment** you can copy. It is advisory only: it never posts anything and never changes
a PR's next step. Results are cached per PR and head commit, so a new push means a fresh analysis.

```bash
./prdesk analyze 298        # try it from the terminal
```

## Develop

```bash
go test ./...
```

Layout: `internal/github` (GraphQL client) -> `internal/triage` (pure classifier) ->
`internal/render` (tables/JSON) and `internal/server` (web dashboard).
