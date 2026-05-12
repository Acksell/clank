// Package jwths256 is a minimal HS256 JWT signer/verifier — RFC 7519
// with one algorithm (HMAC-SHA256). Used by:
//
//   - cmd/clank-auth-stub to mint dev/test tokens after auto-approving
//     a device flow.
//   - the clankd gateway's bearer middleware to accept those tokens
//     alongside the static CLANK_AUTH_TOKEN.
//
// Stays minimal on purpose: no JWKS, no RS256, no audience/issuer
// claim checks beyond exp. Production deployments verify against a
// real auth server; this package is the dev-stack bridge.
package jwths256

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Sign produces an HS256-signed JWT over the given claims map.
func Sign(secret []byte, claims map[string]any) (string, error) {
	header := map[string]string{"alg": "HS256", "typ": "JWT"}
	hb, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	cb, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signingInput := base64.RawURLEncoding.EncodeToString(hb) + "." +
		base64.RawURLEncoding.EncodeToString(cb)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(signingInput))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return signingInput + "." + sig, nil
}

// Verify validates an HS256 JWT against secret, checks the `exp` claim
// (if present) against the current time, and returns the decoded
// claims. The header `alg` MUST be "HS256"; anything else (including
// "none") is rejected — guards against the well-known algorithm-
// confusion vulnerability.
func Verify(secret []byte, token string) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("jwths256: malformed jwt")
	}

	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("jwths256: decode header: %w", err)
	}
	var header struct {
		Alg string `json:"alg"`
	}
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return nil, fmt.Errorf("jwths256: parse header: %w", err)
	}
	if header.Alg != "HS256" {
		return nil, fmt.Errorf("jwths256: unsupported alg %q (only HS256)", header.Alg)
	}

	signingInput := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(signingInput))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(parts[2])) {
		return nil, errors.New("jwths256: bad signature")
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("jwths256: decode payload: %w", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("jwths256: parse payload: %w", err)
	}
	if expFloat, ok := claims["exp"].(float64); ok {
		if time.Now().Unix() > int64(expFloat) {
			return nil, errors.New("jwths256: expired")
		}
	}
	return claims, nil
}

// LooksLikeJWT is a cheap heuristic — three base64url segments
// separated by dots. Used by callers that accept both JWT and static
// bearers to pick which verifier to try first. Not authoritative; a
// random hex string with two dots in it would also "look like" a JWT.
func LooksLikeJWT(token string) bool {
	return strings.Count(token, ".") == 2 && len(token) > 16
}
