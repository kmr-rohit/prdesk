package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
)

// Ollama talks to a local Ollama server.
type Ollama struct {
	URL   string // e.g. http://localhost:11434
	Model string
	http  *http.Client

	// Set after the server says it cannot do constrained output for this
	// model; later calls skip the doomed first attempt.
	noFormat atomic.Bool
}

// NewOllama has no client timeout on purpose: a 26B local model can take a
// minute or more. Callers bound the call with a context deadline instead.
func NewOllama(url, model string) *Ollama {
	return &Ollama{URL: strings.TrimRight(url, "/"), Model: model, http: &http.Client{}}
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model    string         `json:"model"`
	Messages []message      `json:"messages"`
	Stream   bool           `json:"stream"`
	Format   any            `json:"format,omitempty"`
	Think    bool           `json:"think"`
	Options  map[string]any `json:"options"`
}

type chatResponse struct {
	Message message `json:"message"`
	Error   string  `json:"error"`
}

// errNoStructured means "this model/server cannot constrain output to a schema".
var errNoStructured = errors.New("structured output unavailable")

// Analyze asks the model about one PR and returns a validated Analysis.
// It prefers schema-constrained output and degrades to prompt-only JSON.
func (o *Ollama) Analyze(ctx context.Context, in Input) (Analysis, error) {
	a, err := o.analyzeOnce(ctx, in)
	if errors.Is(err, errBadOutput) {
		a, err = o.analyzeOnce(ctx, in) // one retry: output is sampled, a second try often parses
	}
	return a, err
}

func (o *Ollama) analyzeOnce(ctx context.Context, in Input) (Analysis, error) {
	if !o.noFormat.Load() {
		a, err := o.chat(ctx, in, true)
		if !errors.Is(err, errNoStructured) {
			return a, err
		}
		o.noFormat.Store(true)
	}
	return o.chat(ctx, in, false)
}

func (o *Ollama) chat(ctx context.Context, in Input, structured bool) (Analysis, error) {
	cr := chatRequest{
		Model: o.Model,
		Messages: []message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: BuildPrompt(in)},
		},
		Stream: false,
		Think:  false, // we want the JSON answer, not a long reasoning trace
		Options: map[string]any{
			"temperature": 0.2, // low: we want consistent reviews, not creativity
			"num_ctx":     16384,
		},
	}
	if structured {
		cr.Format = schema
	}
	body, err := json.Marshal(cr)
	if err != nil {
		return Analysis{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.URL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return Analysis{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	res, err := o.http.Do(req)
	if err != nil {
		return Analysis{}, fmt.Errorf("ollama unreachable at %s (is `ollama serve` running?): %w", o.URL, err)
	}
	defer res.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return Analysis{}, err
	}
	var out chatResponse
	_ = json.Unmarshal(raw, &out) // Ollama reports errors as {"error": "..."}
	if res.StatusCode != http.StatusOK {
		msg := out.Error
		if msg == "" {
			msg = strings.TrimSpace(string(raw))
		}
		if structured && (res.StatusCode == http.StatusNotImplemented || strings.Contains(msg, "structured output")) {
			return Analysis{}, errNoStructured
		}
		return Analysis{}, fmt.Errorf("ollama %s: %s", res.Status, clip(msg, 300))
	}
	return parseAnalysis(out.Message.Content)
}
