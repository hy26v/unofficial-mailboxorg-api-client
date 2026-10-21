// ABOUTME: Tests for the single-owner OAuth server: discovery, registration, approval and tokens.
// ABOUTME: Runs the full authorization code flow with PKCE over httptest, plus the refusal cases.
package mcpauth

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
)

const testSecret = "correct horse battery staple, long enough"

func init() { wrongSecretDelay = 0 }

type fixture struct {
	t      *testing.T
	server *Server
	http   *httptest.Server
	client *http.Client
	state  string
	mu     sync.Mutex
	now    time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	// The SDK bearer middleware also checks expiry against the real clock.
	f := &fixture{t: t, now: time.Now()}
	f.state = filepath.Join(t.TempDir(), "mcp-oauth.json")
	f.http = httptest.NewUnstartedServer(nil)
	issuer := "http://" + f.http.Listener.Addr().String()
	f.server = f.newServer(issuer)
	mux := http.NewServeMux()
	f.server.Register(mux)
	mux.Handle("/mcp", auth.RequireBearerToken(f.server.Verify, &auth.RequireBearerTokenOptions{
		ResourceMetadataURL: f.server.ResourceMetadataURL(),
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "client="+auth.TokenInfoFromContext(r.Context()).UserID)
	})))
	f.http.Config.Handler = mux
	f.http.Start()
	t.Cleanup(f.http.Close)
	f.client = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return f
}

func (f *fixture) newServer(issuer string) *Server {
	f.t.Helper()
	s, err := New(Options{
		Issuer: issuer, Resource: issuer + "/mcp", OwnerSecret: testSecret, StatePath: f.state,
		Now: func() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.now },
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return s
}

func (f *fixture) advance(d time.Duration) {
	f.mu.Lock()
	f.now = f.now.Add(d)
	f.mu.Unlock()
}

func (f *fixture) url(path string) string { return f.http.URL + path }

func decodeJSON(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	defer resp.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode %s: %v", resp.Request.URL, err)
	}
	return body
}

func (f *fixture) register(body map[string]any) (int, map[string]any) {
	f.t.Helper()
	data, _ := json.Marshal(body)
	resp, err := f.client.Post(f.url("/register"), "application/json", strings.NewReader(string(data)))
	if err != nil {
		f.t.Fatal(err)
	}
	return resp.StatusCode, decodeJSON(f.t, resp)
}

const claudeCallback = "https://claude.ai/api/mcp/auth_callback"

type pkce struct{ verifier, challenge string }

func newPKCE() pkce {
	verifier := strings.Repeat("v", 20) + randomToken()
	return pkce{verifier: verifier, challenge: s256(verifier)}
}

func (f *fixture) authorizeValues(clientID string, p pkce) url.Values {
	return url.Values{
		"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {claudeCallback},
		"code_challenge": {p.challenge}, "code_challenge_method": {"S256"}, "state": {"st-1"},
		"resource": {f.url("/mcp")},
	}
}

// approve posts the consent form and returns the response.
func (f *fixture) approve(values url.Values, secret string) *http.Response {
	f.t.Helper()
	form := url.Values{}
	for k, v := range values {
		form[k] = v
	}
	form.Set("owner_secret", secret)
	form.Set("action", "approve")
	req, _ := http.NewRequest(http.MethodPost, f.url("/authorize"), strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	resp, err := f.client.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	return resp
}

func (f *fixture) token(form url.Values, basicID, basicSecret string) (int, map[string]any) {
	f.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, f.url("/token"), strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if basicID != "" {
		req.SetBasicAuth(url.QueryEscape(basicID), url.QueryEscape(basicSecret))
	}
	resp, err := f.client.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	return resp.StatusCode, decodeJSON(f.t, resp)
}

func (f *fixture) callMCP(accessToken string) (int, string, http.Header) {
	f.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, f.url("/mcp"), nil)
	if accessToken != "" {
		req.Header.Set("Authorization", "Bearer "+accessToken)
	}
	resp, err := f.client.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body), resp.Header
}

// codeFrom approves a fresh authorization and returns the code from the redirect.
func (f *fixture) codeFrom(clientID string, p pkce) string {
	f.t.Helper()
	resp := f.approve(f.authorizeValues(clientID, p), testSecret)
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		f.t.Fatalf("approve status = %d, want 302", resp.StatusCode)
	}
	location, _ := url.Parse(resp.Header.Get("Location"))
	return location.Query().Get("code")
}

func TestMetadata(t *testing.T) {
	f := newFixture(t)
	for _, path := range []string{"/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp"} {
		resp, err := http.Get(f.url(path))
		if err != nil {
			t.Fatal(err)
		}
		body := decodeJSON(t, resp)
		if body["resource"] != f.url("/mcp") || body["authorization_servers"].([]any)[0] != f.http.URL {
			t.Errorf("%s = %v", path, body)
		}
	}
	resp, err := http.Get(f.url("/.well-known/oauth-authorization-server"))
	if err != nil {
		t.Fatal(err)
	}
	body := decodeJSON(t, resp)
	if body["issuer"] != f.http.URL || body["registration_endpoint"] != f.url("/register") ||
		body["code_challenge_methods_supported"].([]any)[0] != "S256" || body["authorization_response_iss_parameter_supported"] != true {
		t.Errorf("server metadata = %v", body)
	}

	status, _, header := f.callMCP("")
	if status != http.StatusUnauthorized || !strings.Contains(header.Get("WWW-Authenticate"), `resource_metadata="`+f.url("/.well-known/oauth-protected-resource/mcp")+`"`) {
		t.Errorf("unauthenticated MCP = %d, %q", status, header.Get("WWW-Authenticate"))
	}
}

func TestFullFlow_PublicClient(t *testing.T) {
	f := newFixture(t)
	status, reg := f.register(map[string]any{
		"redirect_uris": []string{claudeCallback}, "client_name": "Claude <script>",
		"token_endpoint_auth_method": "none", "grant_types": []string{"authorization_code", "refresh_token"},
	})
	if status != http.StatusCreated || reg["client_secret"] != nil || reg["token_endpoint_auth_method"] != "none" {
		t.Fatalf("register = %d %v", status, reg)
	}
	clientID := reg["client_id"].(string)
	p := newPKCE()

	// The consent page names the client, escaped, and carries the request.
	resp, err := f.client.Get(f.url("/authorize?" + f.authorizeValues(clientID, p).Encode()))
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(page), "Claude &lt;script&gt;") ||
		!strings.Contains(string(page), p.challenge) || resp.Header.Get("X-Frame-Options") != "DENY" ||
		!strings.Contains(resp.Header.Get("Content-Security-Policy"), "form-action 'self' https://claude.ai") {
		t.Fatalf("consent page = %d %q %v", resp.StatusCode, page, resp.Header)
	}

	resp = f.approve(f.authorizeValues(clientID, p), testSecret)
	resp.Body.Close()
	location, _ := url.Parse(resp.Header.Get("Location"))
	if resp.StatusCode != http.StatusFound || !strings.HasPrefix(location.String(), claudeCallback+"?") ||
		location.Query().Get("state") != "st-1" || location.Query().Get("iss") != f.http.URL || location.Query().Get("code") == "" {
		t.Fatalf("approval redirect = %d %s", resp.StatusCode, location)
	}
	code := location.Query().Get("code")

	exchange := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {claudeCallback},
		"client_id": {clientID}, "code_verifier": {p.verifier}}
	status, tokens := f.token(exchange, "", "")
	if status != http.StatusOK || tokens["token_type"] != "Bearer" || tokens["expires_in"] != float64(3600) {
		t.Fatalf("token = %d %v", status, tokens)
	}
	access := tokens["access_token"].(string)
	if status, body, _ := f.callMCP(access); status != http.StatusOK || body != "client="+clientID {
		t.Fatalf("MCP with token = %d %q", status, body)
	}

	// A code works once.
	if status, body := f.token(exchange, "", ""); status != http.StatusBadRequest || body["error"] != "invalid_grant" {
		t.Errorf("reused code = %d %v", status, body)
	}

	// The state file keeps hashes only, and a restarted server accepts the token.
	data, err := os.ReadFile(f.state)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), access) || strings.Contains(string(data), tokens["refresh_token"].(string)) {
		t.Error("state file contains a raw token")
	}
	if info, err := os.Stat(f.state); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("state file mode = %v, %v", info.Mode(), err)
	}
	if _, err := f.newServer(f.http.URL).Verify(t.Context(), access, nil); err != nil {
		t.Errorf("restarted server rejects the token: %v", err)
	}

	// Refresh rotates the refresh token.
	refresh := tokens["refresh_token"].(string)
	status, rotated := f.token(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {clientID}}, "", "")
	if status != http.StatusOK || rotated["refresh_token"] == refresh {
		t.Fatalf("refresh = %d %v", status, rotated)
	}
	if status, body := f.token(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {clientID}}, "", ""); status != http.StatusBadRequest || body["error"] != "invalid_grant" {
		t.Errorf("old refresh token = %d %v", status, body)
	}

	// Access tokens expire.
	f.advance(DefaultAccessTokenTTL + time.Second)
	if status, _, _ := f.callMCP(rotated["access_token"].(string)); status != http.StatusUnauthorized {
		t.Errorf("expired token status = %d, want 401", status)
	}
}

func TestFullFlow_ConfidentialClient(t *testing.T) {
	f := newFixture(t)
	// No auth method means client_secret_basic (RFC 7591).
	status, reg := f.register(map[string]any{"redirect_uris": []string{claudeCallback}})
	if status != http.StatusCreated || reg["token_endpoint_auth_method"] != "client_secret_basic" || reg["client_secret"] == nil {
		t.Fatalf("register = %d %v", status, reg)
	}
	clientID, secret := reg["client_id"].(string), reg["client_secret"].(string)
	p := newPKCE()

	code := f.codeFrom(clientID, p)
	exchange := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {p.verifier}}
	if status, body := f.token(exchange, clientID, "wrong"); status != http.StatusUnauthorized || body["error"] != "invalid_client" {
		t.Errorf("wrong client secret = %d %v", status, body)
	}
	// The failed exchange did not consume the code: client authentication runs first.
	if status, body := f.token(exchange, clientID, secret); status != http.StatusOK {
		t.Fatalf("basic auth exchange = %d %v", status, body)
	}

	code = f.codeFrom(clientID, p)
	post := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {p.verifier},
		"client_id": {clientID}, "client_secret": {secret}}
	if status, body := f.token(post, "", ""); status != http.StatusOK {
		t.Errorf("client_secret_post exchange = %d %v", status, body)
	}
}

func TestRegister_Refusals(t *testing.T) {
	f := newFixture(t)
	for name, body := range map[string]map[string]any{
		"no redirect":       {"client_name": "x"},
		"foreign redirect":  {"redirect_uris": []string{"https://evil.example/callback"}},
		"lookalike claude":  {"redirect_uris": []string{"https://claude.ai.evil.example/api/mcp/auth_callback"}},
		"chatgpt traversal": {"redirect_uris": []string{"https://chatgpt.com/connector/oauth/../../evil"}},
		"https loopback":    {"redirect_uris": []string{"https://127.0.0.1/cb"}},
		"fragment":          {"redirect_uris": []string{"http://127.0.0.1:9/cb#x"}},
		"bad grant":         {"redirect_uris": []string{claudeCallback}, "grant_types": []string{"implicit"}},
		"bad auth method":   {"redirect_uris": []string{claudeCallback}, "token_endpoint_auth_method": "private_key_jwt"},
		"long name":         {"redirect_uris": []string{claudeCallback}, "client_name": strings.Repeat("n", 101)},
	} {
		if status, resp := f.register(body); status != http.StatusBadRequest {
			t.Errorf("%s: register = %d %v, want 400", name, status, resp)
		}
	}
	for _, uri := range []string{
		"https://chatgpt.com/connector_platform_oauth_redirect",
		"https://chatgpt.com/connector/oauth/abc_123",
		"http://localhost:33418/callback",
		"http://127.0.0.1/callback",
	} {
		if status, resp := f.register(map[string]any{"redirect_uris": []string{uri}}); status != http.StatusCreated {
			t.Errorf("register %s = %d %v, want 201", uri, status, resp)
		}
	}
}

func TestRegister_PendingClientsStayInMemoryAndExpire(t *testing.T) {
	f := newFixture(t)
	_, reg := f.register(map[string]any{"redirect_uris": []string{claudeCallback}, "token_endpoint_auth_method": "none"})
	clientID := reg["client_id"].(string)
	if _, err := os.Stat(f.state); !os.IsNotExist(err) {
		t.Fatalf("registration wrote the state file: %v", err)
	}
	// The consent page names the client id and registration time.
	p := newPKCE()
	resp, err := f.client.Get(f.url("/authorize?" + f.authorizeValues(clientID, p).Encode()))
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(page), clientID[:8]+", registered ") {
		t.Errorf("consent page lacks the client reference: %s", page)
	}

	f.advance(pendingTTL + time.Second)
	resp, err = f.client.Get(f.url("/authorize?" + f.authorizeValues(clientID, p).Encode()))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expired registration = %d, want 400", resp.StatusCode)
	}
}

func TestRegister_FloodIsRateLimitedAndEvictsNothing(t *testing.T) {
	f := newFixture(t)
	_, reg := f.register(map[string]any{"redirect_uris": []string{claudeCallback}, "token_endpoint_auth_method": "none"})
	ownerClient := reg["client_id"].(string)
	limited := false
	for i := 0; i < registrationsBurst+5; i++ {
		status, _ := f.register(map[string]any{"redirect_uris": []string{claudeCallback}})
		if status == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("registrations were not rate-limited")
	}
	// The owner's pending client still works.
	p := newPKCE()
	code := f.codeFrom(ownerClient, p)
	if status, body := f.token(url.Values{"grant_type": {"authorization_code"}, "code": {code}, "client_id": {ownerClient}, "code_verifier": {p.verifier}}, "", ""); status != http.StatusOK {
		t.Fatalf("owner client after the flood = %d %v", status, body)
	}
	// The limit refills over time.
	f.advance(registrationsEvery)
	if status, body := f.register(map[string]any{"redirect_uris": []string{claudeCallback}}); status != http.StatusCreated {
		t.Errorf("registration after waiting = %d %v", status, body)
	}
}

func TestAuthorize_Refusals(t *testing.T) {
	f := newFixture(t)
	_, reg := f.register(map[string]any{"redirect_uris": []string{claudeCallback, "http://127.0.0.1:9/cb"}, "token_endpoint_auth_method": "none"})
	clientID := reg["client_id"].(string)
	p := newPKCE()

	get := func(values url.Values) *http.Response {
		resp, err := f.client.Get(f.url("/authorize?" + values.Encode()))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	with := func(key, value string) url.Values {
		values := f.authorizeValues(clientID, p)
		if value == "" {
			values.Del(key)
		} else {
			values.Set(key, value)
		}
		return values
	}

	// Errors about the client or redirect URI stay on the server's page.
	for name, values := range map[string]url.Values{
		"unknown client":        with("client_id", "nope"),
		"unregistered target":   with("redirect_uri", "https://claude.ai/other"),
		"ambiguous missing uri": with("redirect_uri", ""),
	} {
		if resp := get(values); resp.StatusCode != http.StatusBadRequest || resp.Header.Get("Location") != "" {
			t.Errorf("%s: status %d location %q, want 400 page", name, resp.StatusCode, resp.Header.Get("Location"))
		}
	}
	// Other errors go back to the client.
	for name, tt := range map[string]struct {
		values url.Values
		code   string
	}{
		"plain PKCE":     {with("code_challenge_method", "plain"), "invalid_request"},
		"no PKCE":        {with("code_challenge", ""), "invalid_request"},
		"token response": {with("response_type", "token"), "unsupported_response_type"},
		"other resource": {with("resource", "https://other.example/mcp"), "invalid_target"},
	} {
		resp := get(tt.values)
		location, _ := url.Parse(resp.Header.Get("Location"))
		if resp.StatusCode != http.StatusFound || location.Query().Get("error") != tt.code || location.Query().Get("state") != "st-1" {
			t.Errorf("%s: status %d location %s, want error %s", name, resp.StatusCode, location, tt.code)
		}
	}

	// Deny returns access_denied.
	form := f.authorizeValues(clientID, p)
	form.Set("action", "deny")
	req, _ := http.NewRequest(http.MethodPost, f.url("/authorize"), strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if location, _ := url.Parse(resp.Header.Get("Location")); location.Query().Get("error") != "access_denied" {
		t.Errorf("deny = %d %s", resp.StatusCode, location)
	}

	// A cross-site form post is refused.
	req, _ = http.NewRequest(http.MethodPost, f.url("/authorize"), strings.NewReader(f.authorizeValues(clientID, p).Encode()+"&owner_secret="+url.QueryEscape(testSecret)))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	resp, err = f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-site approval = %d, want 403", resp.StatusCode)
	}
}

func TestAuthorize_WrongSecretsDoNotLockOutTheOwner(t *testing.T) {
	f := newFixture(t)
	_, reg := f.register(map[string]any{"redirect_uris": []string{claudeCallback}, "token_endpoint_auth_method": "none"})
	clientID := reg["client_id"].(string)
	p := newPKCE()
	for i := 0; i < 20; i++ {
		resp := f.approve(f.authorizeValues(clientID, p), "wrong secret value")
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("wrong secret %d = %d, want 401", i, resp.StatusCode)
		}
	}
	resp := f.approve(f.authorizeValues(clientID, p), testSecret)
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("correct secret after wrong ones = %d, want 302", resp.StatusCode)
	}
}

func TestToken_Refusals(t *testing.T) {
	f := newFixture(t)
	_, reg := f.register(map[string]any{"redirect_uris": []string{claudeCallback}, "token_endpoint_auth_method": "none"})
	clientID := reg["client_id"].(string)
	_, other := f.register(map[string]any{"redirect_uris": []string{claudeCallback}, "token_endpoint_auth_method": "none"})
	p := newPKCE()
	f.codeFrom(other["client_id"].(string), p) // approve the other client, so only the code binding fails

	exchange := func(code string, change func(url.Values)) (int, map[string]any) {
		form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {claudeCallback},
			"client_id": {clientID}, "code_verifier": {p.verifier}}
		change(form)
		return f.token(form, "", "")
	}
	for name, tt := range map[string]struct {
		change func(url.Values)
		status int
		error  string
	}{
		"wrong verifier":    {func(v url.Values) { v.Set("code_verifier", newPKCE().verifier) }, 400, "invalid_grant"},
		"short verifier":    {func(v url.Values) { v.Set("code_verifier", "short") }, 400, "invalid_grant"},
		"other redirect":    {func(v url.Values) { v.Set("redirect_uri", "http://127.0.0.1/cb") }, 400, "invalid_grant"},
		"other client":      {func(v url.Values) { v.Set("client_id", other["client_id"].(string)) }, 400, "invalid_grant"},
		"unknown client":    {func(v url.Values) { v.Set("client_id", "nope") }, 401, "invalid_client"},
		"secret for public": {func(v url.Values) { v.Set("client_secret", "x") }, 401, "invalid_client"},
		"unsupported grant": {func(v url.Values) { v.Set("grant_type", "password") }, 400, "unsupported_grant_type"},
	} {
		code := f.codeFrom(clientID, p)
		if status, body := exchange(code, tt.change); status != tt.status || body["error"] != tt.error {
			t.Errorf("%s: %d %v, want %d %s", name, status, body, tt.status, tt.error)
		}
	}

	code := f.codeFrom(clientID, p)
	f.advance(codeTTL + time.Second)
	if status, body := exchange(code, func(url.Values) {}); status != 400 || body["error"] != "invalid_grant" {
		t.Errorf("expired code = %d %v", status, body)
	}

	if status, _, _ := f.callMCP("not-a-token"); status != http.StatusUnauthorized {
		t.Errorf("unknown bearer = %d, want 401", status)
	}
	// A refresh token is not an access token.
	f.advance(-codeTTL - time.Second)
	_, tokens := exchange(f.codeFrom(clientID, p), func(url.Values) {})
	if status, _, _ := f.callMCP(tokens["refresh_token"].(string)); status != http.StatusUnauthorized {
		t.Errorf("refresh token as bearer = %d, want 401", status)
	}
}

func TestNew_RejectsShortSecret(t *testing.T) {
	_, err := New(Options{Issuer: "https://x", Resource: "https://x/mcp", OwnerSecret: "short", StatePath: filepath.Join(t.TempDir(), "s.json")})
	if err == nil {
		t.Fatal("short owner secret accepted")
	}
}

func TestS256MatchesRFC7636Example(t *testing.T) {
	// RFC 7636 appendix B.
	if got := s256("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"); got != "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM" {
		t.Errorf("s256 = %s", got)
	}
	if _, err := base64.RawURLEncoding.DecodeString(randomToken()); err != nil {
		t.Errorf("random token is not base64url: %v", err)
	}
}
