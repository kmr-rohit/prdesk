// Package server is prdesk's web dashboard: net/http + html/template + htmx.
package server

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/rohikuma/prdesk/internal/config"
	"github.com/rohikuma/prdesk/internal/github"
	"github.com/rohikuma/prdesk/internal/render"
	"github.com/rohikuma/prdesk/internal/triage"
)

// GO LESSON 13: //go:embed bakes files into the compiled binary, so the
// dashboard ships as ONE executable with its HTML and htmx inside.
//
//go:embed templates static
var assets embed.FS

// FetchFunc is how the server gets PRs. Passing a function (instead of a
// concrete GitHub client) lets tests plug in a fake: no network, no token.
type FetchFunc func(ctx context.Context) ([]github.PullRequest, error)

// snapshot is the last known state shown to every visitor.
type snapshot struct {
	results   []triage.Result
	fetchedAt time.Time // last SUCCESSFUL fetch
	checkedAt time.Time // last attempt (success or not); drives the TTL
	err       error     // error from the last attempt, if any
}

type Server struct {
	cfg     *config.Config
	fetch   FetchFunc
	ttl     time.Duration // how long a snapshot is served before refetching
	refresh time.Duration // how often the browser re-polls /board
	tmpl    *template.Template

	// GO LESSON 14: many HTTP requests run concurrently, each in its own
	// goroutine. Shared state needs a lock. Holding mu while refreshing also
	// means ten simultaneous visitors cause ONE GitHub fetch, not ten.
	mu   sync.Mutex
	snap snapshot
}

func New(cfg *config.Config, fetch FetchFunc, ttl, refresh time.Duration) (*Server, error) {
	// html/template (not text/template) escapes by context. PR titles are
	// attacker-controlled text; "<script>" in a title renders as harmless text.
	tmpl, err := template.ParseFS(assets, "templates/page.html")
	if err != nil {
		return nil, err
	}
	return &Server{cfg: cfg, fetch: fetch, ttl: ttl, refresh: refresh, tmpl: tmpl}, nil
}

// Handler wires URL paths to functions.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(assets, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	mux.HandleFunc("GET /{$}", s.page)            // full page
	mux.HandleFunc("GET /board", s.board)         // htmx polls this
	mux.HandleFunc("POST /refresh", s.refreshNow) // "Refresh now" button
	mux.HandleFunc("GET /api/triage.json", s.apiJSON)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { fmt.Fprintln(w, "ok") })
	return mux
}

// current returns the snapshot, refetching if it is older than the TTL.
func (s *Server) current(force bool) snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()

	if force || s.snap.checkedAt.IsZero() || time.Since(s.snap.checkedAt) > s.ttl {
		// Detached context: a visitor closing their tab must not cancel a
		// fetch that every other visitor is waiting on.
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()

		now := time.Now()
		prs, err := s.fetch(ctx)
		s.snap.checkedAt = now
		s.snap.err = err
		if err != nil {
			log.Printf("fetch failed (serving previous data): %v", err)
		} else {
			s.snap.results = triage.ClassifyAll(prs, triage.OptionsFromConfig(s.cfg, now))
			s.snap.fetchedAt = now
		}
	}
	return s.snap
}

func (s *Server) page(w http.ResponseWriter, r *http.Request) {
	s.renderTemplate(w, "page", s.buildView(s.current(false)))
}

func (s *Server) board(w http.ResponseWriter, r *http.Request) {
	s.renderTemplate(w, "board", s.buildView(s.current(false)))
}

func (s *Server) refreshNow(w http.ResponseWriter, r *http.Request) {
	s.renderTemplate(w, "board", s.buildView(s.current(true)))
}

func (s *Server) apiJSON(w http.ResponseWriter, r *http.Request) {
	snap := s.current(false)
	w.Header().Set("Content-Type", "application/json")
	if err := render.JSON(w, snap.results, time.Now(), nil); err != nil {
		log.Printf("json: %v", err)
	}
}

// renderTemplate renders into a buffer first, so a template error becomes a
// clean 500 instead of a half-written page with a 200 status.
func (s *Server) renderTemplate(w http.ResponseWriter, name string, data any) {
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		log.Printf("template %s: %v", name, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(buf.Bytes())
}

// ---- view model ------------------------------------------------------------

type row struct {
	URL, Ref, Title, Author, Age, Active, Action string
	Draft                                        bool
}

type section struct {
	ID, Title, Blurb string
	Rows             []row
	Collapsed        bool
}

type view struct {
	Login, Repos string
	Updated      string // "3m ago" or "never"
	Err          string
	Total        int
	Sections     []section
	RefreshSecs  int
}

// Display order: things that need ME first, background noise last.
var layout = []struct {
	bucket    triage.Bucket
	title     string
	blurb     string
	collapsed bool
}{
	{triage.WaitingOnMe, "Waiting on me", "Review requested, or the author updated since your last review.", false},
	{triage.NeedsOKToTest, "Needs ok-to-test", "CI is blocked until a maintainer approves the workflow runs.", false},
	{triage.ReadyToMerge, "Ready to merge", "Labels, checks and merge state all look good.", false},
	{triage.NeedsCI, "Needs CI", "Checks failed, or none have run.", false},
	{triage.NeedsEval, "Needs eval", "CI is green and the PR touches eval paths. Comment /eval.", false},
	{triage.WaitingOnAuthor, "Waiting on author", "Changes requested or merge conflicts.", false},
	{triage.Agenda, "Community meeting", "Agenda-labelled PRs and new first-time contributors.", false},
	{triage.Mine, "My PRs", "PRs you authored, with review state.", false},
	{triage.Stale, "Stale", "No activity for a while.", true},
}

func (s *Server) buildView(snap snapshot) view {
	now := time.Now()
	groups := triage.Group(snap.results)

	v := view{
		Login:       s.cfg.Login,
		Repos:       joinRepos(s.cfg.Repos),
		Updated:     "never",
		Total:       len(snap.results),
		RefreshSecs: int(s.refresh.Seconds()),
	}
	if !snap.fetchedAt.IsZero() {
		v.Updated = render.Age(now.Sub(snap.fetchedAt)) + " ago"
	}
	if snap.err != nil {
		v.Err = snap.err.Error()
	}

	for _, l := range layout {
		rs := groups[l.bucket]
		if len(rs) == 0 {
			continue
		}
		sec := section{ID: string(l.bucket), Title: l.title, Blurb: l.blurb, Collapsed: l.collapsed}
		for _, r := range rs {
			action, _ := r.In(l.bucket)
			sec.Rows = append(sec.Rows, row{
				URL: r.PR.URL, Ref: fmt.Sprintf("#%d", r.PR.Number), Title: r.PR.Title, Author: r.PR.Author,
				Age: render.Age(now.Sub(r.PR.CreatedAt)), Active: render.Age(now.Sub(r.PR.UpdatedAt)) + " ago",
				Action: action, Draft: r.PR.Draft,
			})
		}
		v.Sections = append(v.Sections, sec)
	}
	return v
}

func joinRepos(repos []string) string {
	out := ""
	for i, r := range repos {
		if i > 0 {
			out += ", "
		}
		out += r
	}
	return out
}
