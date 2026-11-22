// Command prdesk is a maintainer triage tool for GitHub PRs.
//
// GO LESSON 4: `package main` + `func main()` is an executable. By convention
// binaries live in cmd/<name>/, and reusable logic lives in internal/.
// `internal/` is special: the compiler forbids other modules from importing it.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"time"

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
	fmt.Fprintln(os.Stderr, "usage: prdesk <triage|serve> [flags]")
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
