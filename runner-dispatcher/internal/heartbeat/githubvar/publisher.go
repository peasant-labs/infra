// Package githubvar publishes the pool-health record to a GitHub repository
// variable, where the router reads it with the token it already carries.
package githubvar

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/peasant-labs/infra/runner-dispatcher/internal/heartbeat"
)

// TokenSource provides a bearer token for the GitHub API.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// Config carries the target repository and HTTP settings.
type Config struct {
	// Repo is "owner/name".
	Repo string
	// Variable is the repository variable holding the record.
	Variable string
	Tokens   TokenSource
	// BaseURL defaults to https://api.github.com.
	BaseURL string
	// HTTPClient defaults to http.DefaultClient.
	HTTPClient *http.Client
	// Now is injected for tests; defaults to time.Now.
	Now func() time.Time
}

// Publisher writes records to one repository variable.
type Publisher struct {
	cfg Config
}

// New validates the config.
func New(cfg Config) (*Publisher, error) {
	if !strings.Contains(cfg.Repo, "/") {
		return nil, errors.New("githubvar: repo must be owner/name")
	}
	if strings.TrimSpace(cfg.Variable) == "" {
		return nil, errors.New("githubvar: variable name is required")
	}
	if cfg.Tokens == nil {
		return nil, errors.New("githubvar: token source is required")
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.github.com"
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Publisher{cfg: cfg}, nil
}

// Publish writes the record, creating the variable on the first run.
func (p *Publisher) Publish(ctx context.Context, record heartbeat.Record) error {
	if record.Timestamp.IsZero() {
		record.Timestamp = p.cfg.Now().UTC()
	}
	// GitHub Actions and jq's fromdateiso8601 both prefer whole seconds.
	record.Timestamp = record.Timestamp.UTC().Truncate(time.Second)
	recordJSON, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("githubvar: encode record: %w", err)
	}
	payload, err := json.Marshal(map[string]string{
		"name":  p.cfg.Variable,
		"value": string(recordJSON),
	})
	if err != nil {
		return fmt.Errorf("githubvar: encode payload: %w", err)
	}
	status, body, err := p.request(ctx, http.MethodPatch, p.variableURL(), payload)
	if err == nil && status == http.StatusNotFound {
		status, body, err = p.request(ctx, http.MethodPost, p.collectionURL(), payload)
	}
	if err != nil {
		return err
	}
	if status != http.StatusNoContent && status != http.StatusCreated {
		return fmt.Errorf("githubvar: publish returned %d: %s", status, strings.TrimSpace(string(body)))
	}
	return nil
}

func (p *Publisher) variableURL() string {
	return fmt.Sprintf("%s/repos/%s/actions/variables/%s", strings.TrimSuffix(p.cfg.BaseURL, "/"), p.cfg.Repo, p.cfg.Variable)
}

func (p *Publisher) collectionURL() string {
	return fmt.Sprintf("%s/repos/%s/actions/variables", strings.TrimSuffix(p.cfg.BaseURL, "/"), p.cfg.Repo)
}

func (p *Publisher) request(ctx context.Context, method, url string, payload []byte) (int, []byte, error) {
	token, err := p.cfg.Tokens.Token(ctx)
	if err != nil {
		return 0, nil, fmt.Errorf("githubvar: token: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(payload))
	if err != nil {
		return 0, nil, fmt.Errorf("githubvar: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.cfg.HTTPClient.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("githubvar: request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, body, nil
}
