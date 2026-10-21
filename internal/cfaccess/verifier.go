// ABOUTME: Verifies the Cloudflare Access JWT that Access adds to each request it lets through.
// ABOUTME: Checks the RS256 signature against the team keys, the audience, issuer, lifetime and email.
package cfaccess

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Header carries the signed Access assertion to the origin.
const Header = "Cf-Access-Jwt-Assertion"

const (
	// keysTTL refreshes the team keys regularly, because Cloudflare rotates them.
	keysTTL = time.Hour
	// minRefetch limits key requests when tokens name an unknown key.
	minRefetch = 30 * time.Second
	// leeway allows for clock differences between Cloudflare and this host.
	leeway = time.Minute
	// maxKeysBytes bounds the certificate response.
	maxKeysBytes = 1 << 20
	// maxTokenBytes bounds the assertion header before it is decoded.
	maxTokenBytes = 16 << 10
)

// Options configures New.
type Options struct {
	// TeamDomain is the Access team domain, for example
	// https://example.cloudflareaccess.com. It is the expected issuer.
	TeamDomain string
	// Audience is the application audience (AUD) tag of the Access application.
	Audience string
	// Emails, when not empty, are the only identities accepted, compared without case.
	Emails []string
	// HTTPClient fetches the team keys. Nil selects a client with a 10-second timeout.
	HTTPClient *http.Client
	// Now returns the current time. Nil selects time.Now.
	Now func() time.Time
	// Logger receives key fetch failures. Nil discards them.
	Logger *log.Logger
}

// Verifier checks Access assertions. It is safe for concurrent use.
type Verifier struct {
	issuer   string
	certsURL string
	audience string
	emails   map[string]bool
	client   *http.Client
	now      func() time.Time
	logger   *log.Logger

	mu        sync.Mutex
	keys      map[string]*rsa.PublicKey
	fetchedAt time.Time
	triedAt   time.Time
}

// New validates opts and returns a Verifier. It fetches no keys yet.
func New(opts Options) (*Verifier, error) {
	issuer, err := NormalizeTeamDomain(opts.TeamDomain)
	if err != nil {
		return nil, err
	}
	audience := strings.TrimSpace(opts.Audience)
	if audience == "" {
		return nil, errors.New("the Access application audience (AUD) tag is required")
	}
	emails := map[string]bool{}
	for _, email := range opts.Emails {
		email = strings.ToLower(strings.TrimSpace(email))
		if email == "" || !strings.Contains(email, "@") {
			return nil, fmt.Errorf("invalid Access email %q", email)
		}
		emails[email] = true
	}
	v := &Verifier{
		issuer:   issuer,
		certsURL: issuer + "/cdn-cgi/access/certs",
		audience: audience,
		emails:   emails,
		client:   opts.HTTPClient,
		now:      opts.Now,
		logger:   opts.Logger,
	}
	if v.client == nil {
		v.client = &http.Client{Timeout: 10 * time.Second}
	}
	if v.now == nil {
		v.now = time.Now
	}
	if v.logger == nil {
		v.logger = log.New(io.Discard, "", 0)
	}
	return v, nil
}

// NormalizeTeamDomain accepts "example.cloudflareaccess.com" or its https URL and
// returns the https origin without a trailing slash.
func NormalizeTeamDomain(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw != "" && !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", fmt.Errorf("Access team domain %q must look like https://example.cloudflareaccess.com", raw)
	}
	return "https://" + strings.ToLower(u.Host), nil
}

// Claims are the verified identity of one request.
type Claims struct {
	Email   string
	Subject string
}

type header struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
}

type payload struct {
	Iss   string          `json:"iss"`
	Sub   string          `json:"sub"`
	Email string          `json:"email"`
	Aud   json.RawMessage `json:"aud"`
	Exp   *json.Number    `json:"exp"`
	Nbf   *json.Number    `json:"nbf"`
	Iat   *json.Number    `json:"iat"`
}

// Verify checks one assertion and returns its claims.
func (v *Verifier) Verify(ctx context.Context, token string) (Claims, error) {
	if token == "" {
		return Claims{}, errors.New("no Access assertion")
	}
	if len(token) > maxTokenBytes {
		return Claims{}, errors.New("the Access assertion is too large")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Claims{}, errors.New("the Access assertion is not a JWT")
	}
	var h header
	if err := decodeSegment(parts[0], &h); err != nil {
		return Claims{}, fmt.Errorf("JWT header: %w", err)
	}
	if h.Alg != "RS256" {
		return Claims{}, fmt.Errorf("JWT algorithm %q is not RS256", h.Alg)
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return Claims{}, errors.New("JWT signature is not base64url")
	}
	key, err := v.key(ctx, h.Kid)
	if err != nil {
		return Claims{}, err
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature); err != nil {
		return Claims{}, errors.New("JWT signature is not valid")
	}

	var p payload
	if err := decodeSegment(parts[1], &p); err != nil {
		return Claims{}, fmt.Errorf("JWT claims: %w", err)
	}
	if strings.TrimSuffix(p.Iss, "/") != v.issuer {
		return Claims{}, fmt.Errorf("JWT issuer %q is not %s", p.Iss, v.issuer)
	}
	if !audienceContains(p.Aud, v.audience) {
		return Claims{}, errors.New("JWT audience does not match the Access application")
	}
	now := v.now()
	exp, err := numericTime(p.Exp)
	if err != nil || exp.IsZero() {
		return Claims{}, errors.New("JWT has no valid expiry")
	}
	if !now.Before(exp.Add(leeway)) {
		return Claims{}, errors.New("JWT has expired")
	}
	for _, claim := range []*json.Number{p.Nbf, p.Iat} {
		at, err := numericTime(claim)
		if err != nil {
			return Claims{}, errors.New("JWT has an invalid time claim")
		}
		if !at.IsZero() && at.After(now.Add(leeway)) {
			return Claims{}, errors.New("JWT is not valid yet")
		}
	}
	if len(v.emails) > 0 && !v.emails[strings.ToLower(strings.TrimSpace(p.Email))] {
		return Claims{}, fmt.Errorf("Access identity %q is not allowed", p.Email)
	}
	return Claims{Email: p.Email, Subject: p.Sub}, nil
}

// Middleware refuses every request without a valid Access assertion. A tunnel only
// forwards requests that Access let through, so a missing assertion means that the
// request came another way or that the Access application is not set up.
func (v *Verifier) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := v.Verify(r.Context(), r.Header.Get(Header)); err != nil {
			v.logger.Printf("cloudflare access: refused %s %s: %v", r.Method, r.URL.Path, err)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"error":"forbidden","error_description":"this server accepts only requests that Cloudflare Access let through"}`)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// key returns the team key with kid. It fetches the keys when they are old or when kid
// is unknown, but not more often than minRefetch.
func (v *Verifier) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	now := v.now()
	key, known := v.keys[kid]
	stale := now.Sub(v.fetchedAt) > keysTTL
	if (known && !stale) || now.Sub(v.triedAt) < minRefetch {
		if known {
			return key, nil
		}
		return nil, fmt.Errorf("unknown Access signing key %q", kid)
	}
	v.triedAt = now
	keys, err := v.fetchKeys(ctx)
	if err != nil {
		v.logger.Printf("cloudflare access: could not fetch the team keys: %v", err)
		if known {
			return key, nil // keep using a known key while Cloudflare is unreachable
		}
		return nil, errors.New("the Access signing keys are not available")
	}
	v.keys, v.fetchedAt = keys, now
	if key, ok := keys[kid]; ok {
		return key, nil
	}
	return nil, fmt.Errorf("unknown Access signing key %q", kid)
}

type jwk struct {
	Kid string `json:"kid"`
	Kty string `json:"kty"`
	Alg string `json:"alg"`
	Use string `json:"use"`
	N   string `json:"n"`
	E   string `json:"e"`
}

func (v *Verifier) fetchKeys(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.certsURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s returned HTTP %d", v.certsURL, resp.StatusCode)
	}
	var set struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxKeysBytes)).Decode(&set); err != nil {
		return nil, fmt.Errorf("decode %s: %w", v.certsURL, err)
	}
	keys := map[string]*rsa.PublicKey{}
	for _, k := range set.Keys {
		if k.Kty != "RSA" || (k.Use != "" && k.Use != "sig") || (k.Alg != "" && k.Alg != "RS256") {
			continue
		}
		n, errN := base64.RawURLEncoding.DecodeString(k.N)
		e, errE := base64.RawURLEncoding.DecodeString(k.E)
		if errN != nil || errE != nil || len(e) == 0 || len(e) > 4 {
			continue
		}
		key := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
		if key.N.BitLen() < 2048 {
			continue
		}
		keys[k.Kid] = key
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("%s has no usable RS256 keys", v.certsURL)
	}
	return keys, nil
}

func decodeSegment(segment string, dst any) error {
	data, err := base64.RawURLEncoding.DecodeString(segment)
	if err != nil {
		return errors.New("not base64url")
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	if err := decoder.Decode(dst); err != nil {
		return errors.New("not a JSON object")
	}
	return nil
}

// audienceContains accepts the aud claim as one string or an array of strings.
func audienceContains(raw json.RawMessage, want string) bool {
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return one == want
	}
	var many []string
	if json.Unmarshal(raw, &many) == nil {
		for _, aud := range many {
			if aud == want {
				return true
			}
		}
	}
	return false
}

// numericTime converts a NumericDate claim. A missing claim gives the zero time.
func numericTime(n *json.Number) (time.Time, error) {
	if n == nil {
		return time.Time{}, nil
	}
	seconds, err := n.Float64()
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(int64(seconds), 0), nil
}
