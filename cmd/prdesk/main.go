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
	"os"
	"os/signal"
	"time"

	"github.com/rohikuma/prdesk/internal/config"
	"github.com/rohikuma/prdesk/internal/github"
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
	fs.Parse(args)

	cfg, err := config.Load(*cfgPath)
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

	results := triage.ClassifyAll(all, triage.OptionsFromConfig(cfg, time.Now()))

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(results)
	}
	// Plain grouped output. Step 4 replaces this with aligned tables.
	groups := triage.Group(results)
	for _, b := range triage.AllBuckets {
		rs := groups[b]
		if len(rs) == 0 {
			continue
		}
		fmt.Printf("\n== %s (%d)\n", b, len(rs))
		for _, r := range rs {
			action, _ := r.In(b)
			fmt.Printf("  #%-4d %-16s %s\n         -> %s\n", r.PR.Number, r.PR.Author, r.PR.Title, action)
		}
	}
	return nil
}
