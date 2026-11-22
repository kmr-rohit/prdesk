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
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/rohikuma/prdesk/internal/config"
	"github.com/rohikuma/prdesk/internal/github"
	"github.com/rohikuma/prdesk/internal/render"
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
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: prdesk <triage> [flags]")
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
	var all []github.PullRequest
	for _, repo := range cfg.Repos { // sequential for now; step 5 makes this concurrent
		prs, err := client.FetchOpenPRs(ctx, repo)
		if err != nil {
			return err
		}
		all = append(all, prs...)
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
