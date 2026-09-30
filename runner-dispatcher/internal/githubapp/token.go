// Package githubapp mints GitHub App installation access tokens for REST
// calls the scale-set client does not cover, such as repository variables.
package githubapp

import (
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v4"
)

// Config carries the App credentials and HTTP settings.
type Config struct {
	ClientID       string
	InstallationID int64
	// PrivateKeyPEM is the App private key in PEM form.
	PrivateKeyPEM string
	// BaseURL defaults to https://api.github.com.
	BaseURL string
	// HTTPClient defaults to http.DefaultClient.
	HTTPClient *http.Client
	// Now is injected for tests; defaults to time.Now.
	Now func() time.Time
}

// TokenSource returns cached installation access tokens.
type TokenSource struct {
	cfg Config
	key *rsa.PrivateKey

	mu     sync.Mutex
	token  string
	expiry time.Time
}

// New validates the config and parses the private key once.
func New(cfg Config) (*TokenSource, error) {
	if cfg.ClientID == "" || cfg.InstallationID == 0 {
		return nil, errors.New("githubapp: client id and installation id are required")
	}
	if strings.TrimSpace(cfg.PrivateKeyPEM) == "" {
		return nil, errors.New("githubapp: private key is required")
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
	key, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(cfg.PrivateKeyPEM))
	if err != nil {
		return nil, fmt.Errorf("githubapp: parse private key: %w", err)
	}
	return &TokenSource{cfg: cfg, key: key}, nil
}

// Token returns a valid installation token, refreshing it shortly before
// expiry.
func (t *TokenSource) Token(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.token != "" && t.cfg.Now().Before(t.expiry.Add(-5*time.Minute)) {
		return t.token, nil
	}
	appJWT, err := t.appJWT()
	if err != nil {
		return "", err
	}
	url := fmt.Sprintf("%s/app/installations/%d/access_tokens", strings.TrimSuffix(t.cfg.BaseURL, "/"), t.cfg.InstallationID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader([]byte("{}")))
	if err != nil {
		return "", fmt.Errorf("githubapp: build token request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+appJWT)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := t.cfg.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("githubapp: token request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("githubapp: token request returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("githubapp: decode token response: %w", err)
	}
	if out.Token == "" {
		return "", errors.New("githubapp: token response carried no token")
	}
	t.token = out.Token
	t.expiry = out.ExpiresAt
	if t.expiry.IsZero() {
		t.expiry = t.cfg.Now().Add(time.Hour)
	}
	return t.token, nil
}

func (t *TokenSource) appJWT() (string, error) {
	now := t.cfg.Now()
	claims := jwt.RegisteredClaims{
		Issuer:    t.cfg.ClientID,
		IssuedAt:  jwt.NewNumericDate(now.Add(-30 * time.Second)),
		ExpiresAt: jwt.NewNumericDate(now.Add(5 * time.Minute)),
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(t.key)
	if err != nil {
		return "", fmt.Errorf("githubapp: sign jwt: %w", err)
	}
	return signed, nil
}
