// Package render turns classified PRs into human tables or machine JSON.
package render

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"
	"time"
	"unicode/utf8"

	"github.com/rohikuma/prdesk/internal/triage"
)

const maxTitle = 56

// Tables prints one aligned table per non-empty bucket.
//
// GO LESSON 12: take an io.Writer, not os.Stdout. Production passes os.Stdout;
// tests pass a bytes.Buffer and compare the text. io.Writer is one of Go's
// small, famous interfaces: anything with Write([]byte) (int, error) fits.
func Tables(w io.Writer, results []triage.Result, now time.Time, only map[triage.Bucket]bool) error {
	groups := triage.Group(results)
	showRepo := multipleRepos(results)

	for _, b := range triage.AllBuckets {
		rs := groups[b]
		if len(rs) == 0 || (len(only) > 0 && !only[b]) {
			continue
		}
		fmt.Fprintf(w, "\n%s (%d)\n", b, len(rs))

		// tabwriter aligns columns separated by \t. Flush writes the table.
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "PR\tTITLE\tAUTHOR\tAGE\tACTIVE\tSUGGESTED ACTION")
		for _, r := range rs {
			action, _ := r.In(b)
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
				ref(r, showRepo), truncate(r.PR.Title, maxTitle), r.PR.Author,
				Age(now.Sub(r.PR.CreatedAt)), Age(now.Sub(r.PR.UpdatedAt))+" ago", action)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}
	return nil
}

// jsonPR is the stable JSON shape for one PR inside a bucket.
type jsonPR struct {
	Repo     string `json:"repo"`
	Number   int    `json:"number"`
	Title    string `json:"title"`
	URL      string `json:"url"`
	Author   string `json:"author"`
	AgeDays  int    `json:"ageDays"`
	IdleDays int    `json:"idleDays"`
	Action   string `json:"action"`   // why it is in this bucket
	Stage    string `json:"stage"`    // review workflow stage (see triage.Stage)
	NextStep string `json:"nextStep"` // the single next thing to do on this PR
}

type jsonReport struct {
	GeneratedAt time.Time           `json:"generatedAt"`
	Counts      map[string]int      `json:"counts"`
	Buckets     map[string][]jsonPR `json:"buckets"`
}

// JSON writes the grouped report: {generatedAt, counts, buckets:{name:[...]}}.
func JSON(w io.Writer, results []triage.Result, now time.Time, only map[triage.Bucket]bool) error {
	rep := jsonReport{GeneratedAt: now.UTC(), Counts: map[string]int{}, Buckets: map[string][]jsonPR{}}
	for b, rs := range triage.Group(results) {
		if len(only) > 0 && !only[b] {
			continue
		}
		for _, r := range rs {
			action, _ := r.In(b)
			rep.Buckets[string(b)] = append(rep.Buckets[string(b)], jsonPR{
				Repo: r.PR.Repo, Number: r.PR.Number, Title: r.PR.Title, URL: r.PR.URL, Author: r.PR.Author,
				AgeDays:  int(now.Sub(r.PR.CreatedAt).Hours() / 24),
				IdleDays: int(now.Sub(r.PR.UpdatedAt).Hours() / 24),
				Action:   action,
				Stage:    string(r.Step.Stage),
				NextStep: r.Step.Action,
			})
		}
		rep.Counts[string(b)] = len(rs)
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(rep)
}

// Age renders a duration compactly: 5m, 7h, 12d, 3mo, 2y.
func Age(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", max(int(d.Minutes()), 0))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 60*24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%dmo", int(d.Hours()/24/30))
	}
	return fmt.Sprintf("%dy", int(d.Hours()/24/365))
}

func ref(r triage.Result, showRepo bool) string {
	if showRepo {
		return fmt.Sprintf("%s#%d", r.PR.Repo, r.PR.Number)
	}
	return fmt.Sprintf("#%d", r.PR.Number)
}

func multipleRepos(results []triage.Result) bool {
	seen := map[string]bool{}
	for _, r := range results {
		seen[r.PR.Repo] = true
	}
	return len(seen) > 1
}

// truncate shortens s to n runes. Count runes, not bytes: titles contain
// emoji and non-ASCII, and slicing bytes could cut a character in half.
func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}
