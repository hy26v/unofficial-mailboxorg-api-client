// ABOUTME: Tests for the Cloudflare Access JWT verifier with locally signed tokens.
// ABOUTME: A fake team key endpoint serves the public key; no request leaves the test process.
package cfaccess

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testAudience = "aud-tag-123"

type fixture struct {
	key      *rsa.PrivateKey
	kid      string
	issuer   string
	fetches  atomic.Int32
	verifier *Verifier
	now      time.Time
}

func newFixture(t *testing.T, emails ...string) *fixture {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{key: key, kid: "kid-1", now: time.Unix(1_800_000_000, 0)}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/cdn-cgi/access/certs" {
			http.NotFound(w, r)
			return
		}
		f.fetches.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kid": f.kid, "kty": "RSA", "alg": "RS256", "use": "sig",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	}))
	t.Cleanup(server.Close)
	f.issuer = server.URL
	v, err := New(Options{TeamDomain: server.URL, Audience: testAudience, Emails: emails, HTTPClient: server.Client(), Now: func() time.Time { return f.now }})
	if err != nil {
		t.Fatal(err)
	}
	f.verifier = v
	return f
}

func (f *fixture) sign(t *testing.T, hdr map[string]any, claims map[string]any) string {
	t.Helper()
	h, _ := json.Marshal(hdr)
	c, _ := json.Marshal(claims)
	input := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(c)
	digest := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func (f *fixture) claims() map[string]any {
	return map[string]any{
		"iss":   f.issuer,
		"aud":   []string{testAudience},
		"email": "owner@example.org",
		"sub":   "user-1",
		"iat":   f.now.Add(-time.Minute).Unix(),
		"nbf":   f.now.Add(-time.Minute).Unix(),
		"exp":   f.now.Add(10 * time.Minute).Unix(),
	}
}

func (f *fixture) header() map[string]any {
	return map[string]any{"alg": "RS256", "kid": f.kid, "typ": "JWT"}
}

func TestVerifyAcceptsValidAssertion(t *testing.T) {
	f := newFixture(t, "Owner@Example.org")
	claims, err := f.verifier.Verify(context.Background(), f.sign(t, f.header(), f.claims()))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if claims.Email != "owner@example.org" || claims.Subject != "user-1" {
		t.Fatalf("claims = %+v", claims)
	}
	// A second token uses the cached keys.
	if _, err := f.verifier.Verify(context.Background(), f.sign(t, f.header(), f.claims())); err != nil {
		t.Fatal(err)
	}
	if got := f.fetches.Load(); got != 1 {
		t.Fatalf("key fetches = %d, want 1", got)
	}
}

func TestVerifyAcceptsStringAudience(t *testing.T) {
	f := newFixture(t)
	claims := f.claims()
	claims["aud"] = testAudience
	if _, err := f.verifier.Verify(context.Background(), f.sign(t, f.header(), claims)); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyRejects(t *testing.T) {
	f := newFixture(t, "owner@example.org")
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]func() string{
		"empty":   func() string { return "" },
		"garbage": func() string { return "not-a-jwt" },
		"alg none": func() string {
			h := f.header()
			h["alg"] = "none"
			return f.sign(t, h, f.claims())
		},
		"alg HS256": func() string {
			h := f.header()
			h["alg"] = "HS256"
			return f.sign(t, h, f.claims())
		},
		"wrong audience": func() string {
			c := f.claims()
			c["aud"] = []string{"other"}
			return f.sign(t, f.header(), c)
		},
		"wrong issuer": func() string {
			c := f.claims()
			c["iss"] = "https://evil.cloudflareaccess.com"
			return f.sign(t, f.header(), c)
		},
		"expired": func() string {
			c := f.claims()
			c["exp"] = f.now.Add(-2 * time.Minute).Unix()
			return f.sign(t, f.header(), c)
		},
		"no expiry": func() string {
			c := f.claims()
			delete(c, "exp")
			return f.sign(t, f.header(), c)
		},
		"not yet valid": func() string {
			c := f.claims()
			c["nbf"] = f.now.Add(5 * time.Minute).Unix()
			return f.sign(t, f.header(), c)
		},
		"other email": func() string {
			c := f.claims()
			c["email"] = "intruder@example.org"
			return f.sign(t, f.header(), c)
		},
		"tampered claims": func() string {
			token := f.sign(t, f.header(), f.claims())
			parts := strings.Split(token, ".")
			c := f.claims()
			c["email"] = "owner@example.org "
			data, _ := json.Marshal(c)
			return parts[0] + "." + base64.RawURLEncoding.EncodeToString(data) + "." + parts[2]
		},
		"other signing key": func() string {
			saved := f.key
			f.key = other
			defer func() { f.key = saved }()
			return f.sign(t, f.header(), f.claims())
		},
		"unknown kid": func() string {
			h := f.header()
			h["kid"] = "kid-unknown"
			return f.sign(t, h, f.claims())
		},
	}
	for name, token := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := f.verifier.Verify(context.Background(), token()); err == nil {
				t.Fatal("Verify accepted the assertion")
			}
		})
	}
}

func TestUnknownKidRefetchIsLimited(t *testing.T) {
	f := newFixture(t)
	h := f.header()
	h["kid"] = "kid-unknown"
	for i := 0; i < 5; i++ {
		_, _ = f.verifier.Verify(context.Background(), f.sign(t, h, f.claims()))
	}
	if got := f.fetches.Load(); got != 1 {
		t.Fatalf("key fetches = %d, want 1 within the refetch interval", got)
	}
	f.now = f.now.Add(minRefetch + time.Second)
	_, _ = f.verifier.Verify(context.Background(), f.sign(t, h, f.claims()))
	if got := f.fetches.Load(); got != 2 {
		t.Fatalf("key fetches = %d, want 2 after the refetch interval", got)
	}
}

func TestMiddleware(t *testing.T) {
	f := newFixture(t)
	handler := f.verifier.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) }))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("without assertion: status %d, want 403", rec.Code)
	}

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set(Header, f.sign(t, f.header(), f.claims()))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusTeapot {
		t.Fatalf("with assertion: status %d, want the wrapped handler", rec.Code)
	}
}

func TestNewValidatesOptions(t *testing.T) {
	for name, opts := range map[string]Options{
		"no team":        {Audience: "a"},
		"http team":      {TeamDomain: "http://example.cloudflareaccess.com", Audience: "a"},
		"team with path": {TeamDomain: "https://example.cloudflareaccess.com/x", Audience: "a"},
		"no audience":    {TeamDomain: "example.cloudflareaccess.com"},
		"bad email":      {TeamDomain: "example.cloudflareaccess.com", Audience: "a", Emails: []string{"nobody"}},
	} {
		if _, err := New(opts); err == nil {
			t.Errorf("%s: New accepted invalid options", name)
		}
	}
	if got, err := NormalizeTeamDomain("Example.CloudflareAccess.com/"); err != nil || got != "https://example.cloudflareaccess.com" {
		t.Fatalf("NormalizeTeamDomain = %q, %v", got, err)
	}
}
