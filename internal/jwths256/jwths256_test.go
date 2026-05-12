package jwths256

import (
	"strings"
	"testing"
	"time"
)

func TestRoundTrip(t *testing.T) {
	t.Parallel()
	secret := []byte("test-secret")
	tok, err := Sign(secret, map[string]any{
		"sub":   "user-1",
		"email": "u@example.com",
		"exp":   time.Now().Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	claims, err := Verify(secret, tok)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if claims["sub"] != "user-1" {
		t.Errorf("sub: got %v, want user-1", claims["sub"])
	}
}

func TestRejectsTamperedSignature(t *testing.T) {
	t.Parallel()
	tok, err := Sign([]byte("test-secret"), map[string]any{"sub": "x"})
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(tok, ".")
	parts[2] = strings.Repeat("a", len(parts[2]))
	tampered := strings.Join(parts, ".")
	if _, err := Verify([]byte("test-secret"), tampered); err == nil {
		t.Fatal("Verify should reject tampered signature")
	}
}

func TestRejectsExpired(t *testing.T) {
	t.Parallel()
	tok, err := Sign([]byte("test-secret"), map[string]any{
		"sub": "x",
		"exp": time.Now().Add(-time.Minute).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Verify([]byte("test-secret"), tok); err == nil {
		t.Fatal("expired token should not verify")
	}
}

// TestRejectsAlgNone guards against the well-known JWT
// algorithm-confusion vulnerability: an attacker forges a token with
// alg="none" and no signature, hoping the verifier accepts it.
func TestRejectsAlgNone(t *testing.T) {
	t.Parallel()
	// Hand-craft an alg=none token. Header.Payload.<empty sig>.
	const noneToken = "eyJhbGciOiJub25lIiwidHlwIjoiSldUIn0.eyJzdWIiOiJ4In0."
	if _, err := Verify([]byte("test-secret"), noneToken); err == nil {
		t.Fatal("alg=none must be rejected")
	}
}

func TestLooksLikeJWT(t *testing.T) {
	t.Parallel()
	tok, _ := Sign([]byte("k"), map[string]any{"sub": "x"})
	if !LooksLikeJWT(tok) {
		t.Error("signed token should look like a JWT")
	}
	if LooksLikeJWT("not-a-jwt") {
		t.Error("plain string should not look like a JWT")
	}
	if LooksLikeJWT("a.b.c") {
		t.Error("too short to be a real JWT")
	}
}
