// Command prdesk is a maintainer triage tool for GitHub PRs.
//
// GO LESSON 4: `package main` + `func main()` is an executable. By convention
// binaries live in cmd/<name>/, and reusable logic lives in internal/.
// `internal/` is special: the compiler forbids other modules from importing it.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/rohikuma/prdesk/internal/config"
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

	// Placeholder: step 2 will fetch real PRs from GitHub here.
	fmt.Printf("login=%s repos=%v stale_days=%d json=%v\n",
		cfg.Login, cfg.Repos, cfg.StaleDays, *asJSON)
	return nil
}
