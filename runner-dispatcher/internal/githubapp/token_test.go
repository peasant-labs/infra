package githubapp

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"
)

func testKeyPEM(t *testing.T) (string, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	der := x509.MarshalPKCS1PrivateKey(key)
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der})
	return string(pemBytes), key
}

func TestTokenFetchesAndCaches(t *testing.T) {
	fixed := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	pemStr, key := testKeyPEM(t)

	var calls int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&calls, 1)
		if r.URL.Path != "/app/installations/42/access_tokens" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		auth := r.Header.Get("Authorization")
		raw := auth[len("Bearer "):]
		parsed, err := jwt.ParseWithClaims(raw, &jwt.RegisteredClaims{}, func(token *jwt.Token) (any, error) {
			return &key.PublicKey, nil
		}, jwt.WithoutClaimsValidation())
		if err != nil {
			t.Errorf("parse app jwt: %v", err)
		} else if claims, ok := parsed.Claims.(*jwt.RegisteredClaims); !ok || claims.Issuer != "12345" {
			t.Errorf("jwt claims = %+v", parsed.Claims)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":      "install-token",
			"expires_at": fixed.Add(time.Hour),
		})
	}))
	defer server.Close()

	source, err := New(Config{
		ClientID:       "12345",
		InstallationID: 42,
		PrivateKeyPEM:  pemStr,
		BaseURL:        server.URL,
		HTTPClient:     server.Client(),
		Now:            func() time.Time { return fixed },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	for i := 0; i < 2; i++ {
		token, err := source.Token(context.Background())
		if err != nil {
			t.Fatalf("Token: %v", err)
		}
		if token != "install-token" {
			t.Fatalf("token = %q", token)
		}
	}
	if got := atomic.LoadInt64(&calls); got != 1 {
		t.Fatalf("token endpoint calls = %d, want 1 (cached)", got)
	}
}

func TestTokenRefreshNearExpiry(t *testing.T) {
	pemStr, _ := testKeyPEM(t)
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)

	var calls int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&calls, 1)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":      "token-" + string(rune('0'+n)),
			"expires_at": now.Add(time.Hour),
		})
	}))
	defer server.Close()

	clock := now
	source, err := New(Config{
		ClientID:       "1",
		InstallationID: 2,
		PrivateKeyPEM:  pemStr,
		BaseURL:        server.URL,
		HTTPClient:     server.Client(),
		Now:            func() time.Time { return clock },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := source.Token(context.Background()); err != nil {
		t.Fatalf("first Token: %v", err)
	}
	// Move beyond the refresh margin before the recorded expiry.
	clock = now.Add(56 * time.Minute)
	token, err := source.Token(context.Background())
	if err != nil {
		t.Fatalf("second Token: %v", err)
	}
	if token != "token-2" {
		t.Fatalf("token = %q, want a refreshed token", token)
	}
	if got := atomic.LoadInt64(&calls); got != 2 {
		t.Fatalf("token endpoint calls = %d, want 2", got)
	}
}
