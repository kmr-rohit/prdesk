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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rohikuma/prdesk/internal/ai"
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

// DiffFunc fetches a PR's diff (cut to a size limit).
type DiffFunc func(ctx context.Context, repo string, number int) (diff string, truncated bool, err error)

// Analyzer is the one method the server needs from an AI backend.
//
// GO LESSON 16: interfaces are satisfied implicitly. *ai.Ollama has an Analyze
// method with this exact shape, so it IS an Analyzer: no "implements" keyword.
// Tests pass a fake that returns canned answers.
type Analyzer interface {
	Analyze(ctx context.Context, in ai.Input) (ai.Analysis, error)
}

// snapshot is the last known state shown to every visitor.
type snapshot struct {
	results   []triage.Result
	fetchedAt time.Time // last SUCCESSFUL fetch
	checkedAt time.Time // last attempt (success or not); drives the TTL
	err       error     // error from the last attempt, if any
}

// aiEntry is one analysis, possibly still running.
type aiEntry struct {
	done chan struct{} // closed when res/err are set
	res  ai.Analysis
	err  error
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

	// Optional AI. Analyses are cached per PR *and head commit*, so a new push
	// naturally invalidates the old analysis.
	analyzer Analyzer
	diff     DiffFunc
	aiModel  string
	aiMu     sync.Mutex
	aiCache  map[string]*aiEntry
}

func New(cfg *config.Config, fetch FetchFunc, ttl, refresh time.Duration) (*Server, error) {
	// html/template (not text/template) escapes by context. PR titles and model
	// output are untrusted; "<script>" in either renders as harmless text.
	tmpl, err := template.ParseFS(assets, "templates/page.html")
	if err != nil {
		return nil, err
	}
	return &Server{cfg: cfg, fetch: fetch, ttl: ttl, refresh: refresh, tmpl: tmpl, aiCache: map[string]*aiEntry{}}, nil
}

// WithAI turns on the "Analyze" buttons.
func (s *Server) WithAI(a Analyzer, diff DiffFunc, model string) {
	s.analyzer, s.diff, s.aiModel = a, diff, model
}

// Handler wires URL paths to functions.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(assets, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	mux.HandleFunc("GET /{$}", s.page)            // full page
	mux.HandleFunc("GET /board", s.board)         // htmx polls this
	mux.HandleFunc("POST /refresh", s.refreshNow) // "Refresh now" button
	mux.HandleFunc("POST /ai/{owner}/{name}/{number}", s.analyze)
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

// ---- AI --------------------------------------------------------------------

func aiKey(pr github.PullRequest) string {
	return fmt.Sprintf("%s#%d@%s", pr.Repo, pr.Number, pr.HeadSHA)
}

// analyze runs (or joins, or reuses) the analysis of one PR and returns the
// result fragment that htmx drops into the row.
func (s *Server) analyze(w http.ResponseWriter, r *http.Request) {
	if s.analyzer == nil {
		http.Error(w, "AI is not enabled", http.StatusNotFound)
		return
	}
	repo := r.PathValue("owner") + "/" + r.PathValue("name")
	number, err := strconv.Atoi(r.PathValue("number"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	pr, ok := s.findPR(repo, number)
	if !ok {
		http.NotFound(w, r)
		return
	}

	res, err := s.runAnalysis(r.Context(), pr)
	v := aiView{ID: aiDomID(pr), Path: aiPath(pr), Model: s.aiModel}
	if err != nil {
		v.Err = err.Error()
	} else {
		v.Result, v.HasResult = res, true
	}
	s.renderTemplate(w, "ai", v)
}

func (s *Server) findPR(repo string, number int) (github.PullRequest, bool) {
	snap := s.current(false)
	for _, r := range snap.results {
		if r.PR.Repo == repo && r.PR.Number == number {
			return r.PR, true
		}
	}
	return github.PullRequest{}, false
}

// runAnalysis starts the model call at most once per (PR, head commit). Every
// concurrent caller waits on the same entry. The work uses its own context, so
// closing the browser tab does not throw away a minute of GPU time; the result
// is cached and appears on the next refresh.
func (s *Server) runAnalysis(ctx context.Context, pr github.PullRequest) (ai.Analysis, error) {
	key := aiKey(pr)

	s.aiMu.Lock()
	e, exists := s.aiCache[key]
	if !exists {
		e = &aiEntry{done: make(chan struct{})}
		s.aiCache[key] = e
		go func() {
			bg, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			e.res, e.err = s.analyzeNow(bg, pr)
			if e.err != nil {
				s.aiMu.Lock()
				delete(s.aiCache, key) // failures are not cached: allow retry
				s.aiMu.Unlock()
			}
			close(e.done)
		}()
	}
	s.aiMu.Unlock()

	select {
	case <-e.done:
		return e.res, e.err
	case <-ctx.Done():
		return ai.Analysis{}, ctx.Err()
	}
}

func (s *Server) analyzeNow(ctx context.Context, pr github.PullRequest) (ai.Analysis, error) {
	var diff string
	var truncated bool
	if s.diff != nil {
		d, t, err := s.diff(ctx, pr.Repo, pr.Number)
		if err != nil {
			// Still useful without the diff: the prompt says it is unavailable.
			log.Printf("diff for %s#%d unavailable: %v", pr.Repo, pr.Number, err)
		}
		diff, truncated = d, t
	}
	return s.analyzer.Analyze(ctx, ai.InputFromPR(pr, diff, truncated, s.cfg.EvalPaths))
}

// cachedAnalysis returns a finished analysis without starting one.
func (s *Server) cachedAnalysis(pr github.PullRequest) (ai.Analysis, bool) {
	s.aiMu.Lock()
	defer s.aiMu.Unlock()
	e, ok := s.aiCache[aiKey(pr)]
	if !ok {
		return ai.Analysis{}, false
	}
	select {
	case <-e.done:
		return e.res, e.err == nil
	default:
		return ai.Analysis{}, false // still running
	}
}

func aiDomID(pr github.PullRequest) string {
	return "ai-" + strings.ReplaceAll(pr.Repo, "/", "-") + "-" + strconv.Itoa(pr.Number)
}

func aiPath(pr github.PullRequest) string {
	return fmt.Sprintf("/ai/%s/%d", pr.Repo, pr.Number)
}

// ---- view model ------------------------------------------------------------

type aiView struct {
	ID, Path, Model string
	HasResult       bool
	Result          ai.Analysis
	Err             string
}

type row struct {
	URL, Ref, Title, Author, Age, Active string
	Action, Why, CopyLabel, CopyText     string
	Draft                                bool
	AI                                   *aiView // nil when AI is disabled
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

// Cross-cutting views: a PR can be here AND in a workflow-stage section.
var extras = []struct {
	bucket    triage.Bucket
	title     string
	blurb     string
	collapsed bool
}{
	{triage.Agenda, "Community meeting", "Agenda-labelled PRs and new first-time contributors.", false},
	{triage.Mine, "My PRs", "PRs you authored, with review state.", false},
	{triage.Stale, "Stale", "No activity for a while.", true},
}

func (s *Server) stageInfo(st triage.Stage) (title, blurb string, collapsed bool) {
	who := "the approver"
	if s.cfg.Approver != "" {
		who = "@" + s.cfg.Approver
	}
	switch st {
	case triage.StageReview:
		return "Review needed", "You haven't reviewed yet, or the author pushed after your review.", false
	case triage.StageApprove:
		return "Ok to merge from my side", "You approved, CI is green, eval is done or not needed: comment /lgtm.", false
	case triage.StageAskOKToTest:
		return "Ask " + who + ": /ok-to-test", "You approved. CI has not run yet.", false
	case triage.StageAskEval:
		return "Ask " + who + ": /eval", "CI is green and the change touches eval paths.", false
	case triage.StageFixCI:
		return "CI failing: request changes", "Checks failed after your approval.", false
	case triage.StageWaitAuthor:
		return "Waiting on author", "Changes requested, or merge conflicts.", false
	case triage.StageWaitCI:
		return "CI running", "Nothing to do yet.", true
	case triage.StageMerge:
		return "Ready for " + who + " to merge", "lgtm + approved and green.", true
	}
	return string(st), "", false
}

func (s *Server) buildView(snap snapshot) view {
	now := time.Now()
	groups := triage.Group(snap.results)

	v := view{
		Login:       s.cfg.Login,
		Repos:       strings.Join(s.cfg.Repos, ", "),
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

	// 1) One section per workflow stage: every reviewable PR appears exactly once.
	byStage := map[triage.Stage][]triage.Result{}
	for _, r := range snap.results {
		if r.Step.Stage != triage.StageNone {
			byStage[r.Step.Stage] = append(byStage[r.Step.Stage], r)
		}
	}
	for _, st := range triage.StageOrder {
		rs := byStage[st]
		if len(rs) == 0 {
			continue
		}
		title, blurb, collapsed := s.stageInfo(st)
		sec := section{ID: string(st), Title: title, Blurb: blurb, Collapsed: collapsed}
		for _, r := range rs {
			sec.Rows = append(sec.Rows, s.makeRow(r, now, r.Step.Action, r.Step.Why))
		}
		v.Sections = append(v.Sections, sec)
	}

	// 2) Cross-cutting sections from the buckets.
	for _, x := range extras {
		rs := groups[x.bucket]
		if len(rs) == 0 {
			continue
		}
		sec := section{ID: string(x.bucket), Title: x.title, Blurb: x.blurb, Collapsed: x.collapsed}
		for _, r := range rs {
			action, _ := r.In(x.bucket)
			sec.Rows = append(sec.Rows, s.makeRow(r, now, action, ""))
		}
		v.Sections = append(v.Sections, sec)
	}
	return v
}

func (s *Server) makeRow(r triage.Result, now time.Time, action, why string) row {
	rw := row{
		URL: r.PR.URL, Ref: fmt.Sprintf("#%d", r.PR.Number), Title: r.PR.Title, Author: r.PR.Author,
		Age: render.Age(now.Sub(r.PR.CreatedAt)), Active: render.Age(now.Sub(r.PR.UpdatedAt)) + " ago",
		Action: action, Why: why, CopyLabel: r.Step.CopyLabel, CopyText: r.Step.CopyText, Draft: r.PR.Draft,
	}
	if s.analyzer != nil {
		av := &aiView{ID: aiDomID(r.PR), Path: aiPath(r.PR), Model: s.aiModel}
		if res, ok := s.cachedAnalysis(r.PR); ok {
			av.Result, av.HasResult = res, true
		}
		rw.AI = av
	}
	return rw
}
