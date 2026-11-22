// Command prdesk is a maintainer triage tool for GitHub PRs.
//
// GO LESSON 4: `package main` + `func main()` is an executable. By convention
// binaries live in cmd/<name>/, and reusable logic lives in internal/.
// `internal/` is special: the compiler forbids other modules from importing it.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"github.com/rohikuma/prdesk/internal/ai"
	"github.com/rohikuma/prdesk/internal/config"
	"github.com/rohikuma/prdesk/internal/github"
	"github.com/rohikuma/prdesk/internal/render"
	"github.com/rohikuma/prdesk/internal/server"
	"github.com/rohikuma/prdesk/internal/triage"
)

func main() {
	// Subcommands by hand with the stdlib `flag` package: no framework yet,
	// so you can see exactly how it works.
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	switch os.Args[1] {
	case "triage":
		if err := runTriage(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "analyze":
		if err := runAnalyze(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "serve":
		if err := runServe(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: prdesk <triage|serve|analyze> [flags]")
}

func runTriage(args []string) error {
	fs := flag.NewFlagSet("triage", flag.ExitOnError)
	cfgPath := fs.String("config", "config.yaml", "path to config file")
	asJSON := fs.Bool("json", false, "print JSON instead of tables")
	bucketFlag := fs.String("bucket", "", "only show these buckets (comma-separated), e.g. waiting-on-me,stale")
	fs.Parse(args)

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}

	only, err := parseBuckets(*bucketFlag) // validate flags before any network call
	if err != nil {
		return err
	}

	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		return errors.New("GITHUB_TOKEN is not set")
	}

	// signal.NotifyContext cancels ctx on Ctrl-C, aborting in-flight requests.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	client := github.NewClient(token)
	all, err := client.FetchRepos(ctx, cfg.Repos)
	if err != nil {
		return err
	}

	now := time.Now()
	results := triage.ClassifyAll(all, triage.OptionsFromConfig(cfg, now))

	if *asJSON {
		return render.JSON(os.Stdout, results, now, only)
	}
	return render.Tables(os.Stdout, results, now, only)
}

// parseBuckets turns "stale,mine" into a lookup set, rejecting typos.
func parseBuckets(csv string) (map[triage.Bucket]bool, error) {
	if csv == "" {
		return nil, nil // nil = no filter
	}
	valid := map[triage.Bucket]bool{}
	for _, b := range triage.AllBuckets {
		valid[b] = true
	}
	only := map[triage.Bucket]bool{}
	for _, name := range strings.Split(csv, ",") {
		b := triage.Bucket(strings.TrimSpace(name))
		if !valid[b] {
			return nil, fmt.Errorf("unknown bucket %q (valid: %v)", name, triage.AllBuckets)
		}
		only[b] = true
	}
	return only, nil
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	cfgPath := fs.String("config", "config.yaml", "path to config file")
	addr := fs.String("addr", "127.0.0.1:8080", "listen address (localhost only by default)")
	ttl := fs.Duration("ttl", 2*time.Minute, "how long fetched data is reused before asking GitHub again")
	refresh := fs.Duration("refresh", time.Minute, "how often the browser re-polls the dashboard")
	fs.Parse(args)

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		return errors.New("GITHUB_TOKEN is not set")
	}

	client := github.NewClient(token)
	srv, err := server.New(cfg, func(ctx context.Context) ([]github.PullRequest, error) {
		return client.FetchRepos(ctx, cfg.Repos)
	}, *ttl, *refresh)
	if err != nil {
		return err
	}
	if cfg.AI.Enabled {
		srv.WithAI(ai.NewOllama(cfg.AI.URL, cfg.AI.Model), diffFetcher(client, cfg), cfg.AI.Model)
		fmt.Printf("AI analysis on: %s via %s\n", cfg.AI.Model, cfg.AI.URL)
	}

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second, // guards against slowloris clients
	}

	// Graceful shutdown: on Ctrl-C, stop accepting connections, let in-flight
	// requests finish (up to 5s), then exit.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		httpSrv.Shutdown(shutCtx)
	}()

	fmt.Printf("prdesk dashboard: http://%s  (Ctrl-C to stop)\n", *addr)
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	<-done
	return nil
}

// diffFetcher adapts the GitHub client to the server's DiffFunc.
func diffFetcher(client *github.Client, cfg *config.Config) server.DiffFunc {
	return func(ctx context.Context, repo string, n int) (string, bool, error) {
		return client.FetchDiff(ctx, repo, n, int64(cfg.AI.MaxDiffChars))
	}
}

// runAnalyze: `prdesk analyze 296` prints the AI review of one PR, handy for
// trying the model without the dashboard.
func runAnalyze(args []string) error {
	fs := flag.NewFlagSet("analyze", flag.ExitOnError)
	cfgPath := fs.String("config", "config.yaml", "path to config file")
	repo := fs.String("repo", "", "owner/name (default: first repo in config)")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return errors.New("usage: prdesk analyze [--repo owner/name] <pr-number>")
	}
	number, err := strconv.Atoi(fs.Arg(0))
	if err != nil {
		return fmt.Errorf("bad PR number %q", fs.Arg(0))
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		return errors.New("GITHUB_TOKEN is not set")
	}
	if *repo == "" {
		*repo = cfg.Repos[0]
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	client := github.NewClient(token)
	prs, err := client.FetchOpenPRs(ctx, *repo)
	if err != nil {
		return err
	}
	for _, pr := range prs {
		if pr.Number != number {
			continue
		}
		diff, truncated, err := diffFetcher(client, cfg)(ctx, *repo, number)
		if err != nil {
			fmt.Fprintln(os.Stderr, "warning: no diff:", err)
		}
		fmt.Fprintf(os.Stderr, "asking %s about %s#%d (diff %d bytes, truncated=%v)...\n", cfg.AI.Model, *repo, number, len(diff), truncated)
		start := time.Now()
		a, err := ai.NewOllama(cfg.AI.URL, cfg.AI.Model).Analyze(ctx, ai.InputFromPR(pr, diff, truncated, cfg.EvalPaths))
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "done in %s\n", time.Since(start).Round(time.Second))
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(a)
	}
	return fmt.Errorf("PR #%d is not open in %s", number, *repo)
}
