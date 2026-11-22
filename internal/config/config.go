// Package config loads and validates prdesk's config.yaml.
//
// GO LESSON 1: a package is a directory. Everything in this directory starts
// with `package config`. Names that start with a Capital letter (Config, Load)
// are exported (public); lowercase names are private to the package.
package config

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config mirrors config.yaml. The `yaml:"..."` strings are "struct tags":
// metadata that the yaml library reads (via reflection) to map keys to fields.
type Config struct {
	Login       string   `yaml:"login"`
	Repos       []string `yaml:"repos"`
	StaleDays   int      `yaml:"stale_days"`
	AgendaLabel string   `yaml:"agenda_label"`

	// Teams whose review requests count as "waiting on me" (team slugs).
	Teams []string `yaml:"teams"`
	// Labels that must ALL be present for ready-to-merge (Prow: lgtm + approved).
	ReadyLabels []string `yaml:"ready_labels"`
	// Comment command that starts an eval run, e.g. "/eval". Empty disables needs-eval.
	EvalCommand string `yaml:"eval_command"`
	// If set, needs-eval only fires for PRs changing a file under one of these prefixes.
	EvalPaths []string `yaml:"eval_paths"`
}

// Load reads path, applies defaults and validates.
//
// GO LESSON 2: Go has no exceptions. Functions that can fail return an extra
// `error` value as the last result, and callers check `if err != nil`.
// `%w` in fmt.Errorf wraps the original error so callers can still inspect it.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

	var c Config                                     // zero value: "" / nil / 0. Go has no uninitialised variables.
	if err := yaml.Unmarshal(data, &c); err != nil { // &c = pointer, so Unmarshal can fill it in
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}

	if c.StaleDays == 0 {
		c.StaleDays = 14
	}
	if len(c.ReadyLabels) == 0 {
		c.ReadyLabels = []string{"lgtm", "approved"}
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// validate is a method (note the receiver `c *Config` before the name).
// Lowercase name => only code inside package config can call it.
func (c *Config) validate() error {
	var errs []error
	if c.Login == "" {
		errs = append(errs, errors.New("login is required"))
	}
	if len(c.Repos) == 0 {
		errs = append(errs, errors.New("at least one repo is required"))
	}
	for _, r := range c.Repos {
		// each repo must look like owner/name
		if owner, name, ok := strings.Cut(r, "/"); !ok || owner == "" || name == "" {
			errs = append(errs, fmt.Errorf("repo %q must be owner/name", r))
		}
	}
	if c.StaleDays < 0 {
		errs = append(errs, errors.New("stale_days must be positive"))
	}
	return errors.Join(errs...) // nil if errs is empty
}
