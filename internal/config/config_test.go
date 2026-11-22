package config

import (
	"os"
	"path/filepath"
	"testing"
)

// GO LESSON 3: table-driven tests. Define a slice of cases, loop over them,
// and run each as a named subtest with t.Run. This is THE idiomatic Go test
// style, and we'll use it for the PR classifier too.
func TestLoad(t *testing.T) {
	tests := []struct {
		name      string
		yaml      string
		wantErr   bool
		wantStale int
	}{
		{
			name:      "valid with default stale_days",
			yaml:      "login: me\nrepos: [kubeflow/docs-agent]\n",
			wantStale: 14,
		},
		{
			name:      "explicit stale_days",
			yaml:      "login: me\nrepos: [a/b]\nstale_days: 7\n",
			wantStale: 7,
		},
		{name: "missing login", yaml: "repos: [a/b]\n", wantErr: true},
		{name: "no repos", yaml: "login: me\n", wantErr: true},
		{name: "bad repo format", yaml: "login: me\nrepos: [justname]\n", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml") // auto-cleaned temp dir
			if err := os.WriteFile(path, []byte(tc.yaml), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := Load(path)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Load() error = %v, wantErr %v", err, tc.wantErr)
			}
			if err == nil && got.StaleDays != tc.wantStale {
				t.Errorf("StaleDays = %d, want %d", got.StaleDays, tc.wantStale)
			}
		})
	}
}
