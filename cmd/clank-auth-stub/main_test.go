package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestJWT_RoundTrip(t *testing.T) {
	t.Parallel()
	secret := []byte("test-secret")
	tok, err := signJWT(secret, map[string]any{
		"sub":   "user-1",
		"email": "u@example.com",
		"exp":   time.Now().Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	claims, err := verifyJWT(secret, tok)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if claims["sub"] != "user-1" {
		t.Errorf("sub: got %v, want user-1", claims["sub"])
	}
}

func TestJWT_RejectsTamperedSignature(t *testing.T) {
	t.Parallel()
	tok, err := signJWT([]byte("test-secret"), map[string]any{"sub": "x"})
	if err != nil {
		t.Fatal(err)
	}
	// Flip one character in the signature segment.
	parts := strings.Split(tok, ".")
	parts[2] = strings.Repeat("a", len(parts[2]))
	tampered := strings.Join(parts, ".")
	if _, err := verifyJWT([]byte("test-secret"), tampered); err == nil {
		t.Fatal("verifyJWT should reject tampered signature")
	}
}

func TestJWT_RejectsExpired(t *testing.T) {
	t.Parallel()
	tok, err := signJWT([]byte("test-secret"), map[string]any{
		"sub": "x",
		"exp": time.Now().Add(-time.Minute).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifyJWT([]byte("test-secret"), tok); err == nil {
		t.Fatal("expired token should not verify")
	}
}

// TestDeviceFlow_AutoApproves walks the stub through the device-flow
// shape the laptop's internal/cloud client expects: start → poll →
// /me with the returned bearer.
func TestDeviceFlow_AutoApproves(t *testing.T) {
	t.Parallel()
	cfg := config{
		listen:   ":0",
		secret:   []byte("test-secret"),
		userID:   "dev-user",
		email:    "dev@clank.local",
		tokenTTL: time.Minute,
	}
	s := newServer(cfg)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	cfg.publicURL = srv.URL
	s.cfg = cfg

	// start
	resp, err := http.Post(srv.URL+"/auth/device/start", "application/json", nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("start status %d", resp.StatusCode)
	}
	var start struct {
		DeviceCode string `json:"device_code"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&start); err != nil {
		t.Fatal(err)
	}
	if start.DeviceCode == "" {
		t.Fatal("empty device_code")
	}

	// poll
	body, _ := json.Marshal(map[string]string{"device_code": start.DeviceCode})
	resp, err = http.Post(srv.URL+"/auth/device/poll", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("poll status %d", resp.StatusCode)
	}
	var token struct {
		AccessToken string `json:"access_token"`
		UserID      string `json:"user_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&token); err != nil {
		t.Fatal(err)
	}
	if token.AccessToken == "" {
		t.Fatal("empty access_token")
	}
	if token.UserID != "dev-user" {
		t.Errorf("user_id: got %q, want dev-user", token.UserID)
	}

	// /me with the bearer
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/me", nil)
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("me: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("me status %d", resp.StatusCode)
	}

	// second poll with same code fails (single-use)
	resp, err = http.Post(srv.URL+"/auth/device/poll", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatal("second poll with same code should fail")
	}
}
