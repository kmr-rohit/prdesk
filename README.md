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

## Develop

```bash
go test ./...
```

Layout: `internal/github` (GraphQL client) -> `internal/triage` (pure classifier) ->
`internal/render` (tables/JSON) and `internal/server` (web dashboard).
